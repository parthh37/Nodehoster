// Command nodehoster is an IIS-style application server for Node.js on
// Windows: sites with bindings, a reverse proxy, automatic HTTPS and managed
// Node.js processes, administered from a web console.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/parthh37/nodehoster/internal/api"
	"github.com/parthh37/nodehoster/internal/auth"
	"github.com/parthh37/nodehoster/internal/certs"
	"github.com/parthh37/nodehoster/internal/cli"
	"github.com/parthh37/nodehoster/internal/config"
	"github.com/parthh37/nodehoster/internal/core"
	"github.com/parthh37/nodehoster/internal/localapi"
	"github.com/parthh37/nodehoster/internal/localapi/localserver"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/secrets"
	"github.com/parthh37/nodehoster/internal/service"
	"github.com/parthh37/nodehoster/internal/store"
	"gopkg.in/natefinch/lumberjack.v2"
)

const usage = `NodeHoster %s — Node.js application server

Usage:
  nodehoster [--data DIR] [--json] <command>

Commands:
  run                      Run in the foreground (default)
  service install          Install the Windows service (run as administrator)
  service uninstall        Stop and remove the Windows service
  service start|stop       Start or stop the Windows service
  service status           Show the service state
  reset-password [user]    Set a new random password (default user: admin);
                           also turns password sign-in back on if single
                           sign-on turned it off
  version                  Print the version

Management (talks to the running service; on Windows from an elevated
prompt, like NodeHoster Manager). <site> is a site name or ID:
%s
  --json prints the API's JSON instead of tables, for scripts. Add --help
  after a command for its flags. Exit codes: 0 done, 1 failed (including a
  failed deployment, or the service not running), 2 wrong usage.

The data directory defaults to %s
(override with --data or the NODEHOSTER_DATA environment variable).
`

func main() {
	dataDir := flag.String("data", config.DefaultDataDir(), "data directory")
	jsonOut := flag.Bool("json", false, "machine-readable output for management commands")
	flag.Usage = func() { fmt.Fprintf(os.Stderr, usage, config.Version, cli.Usage(), config.DefaultDataDir()) }
	flag.Parse()
	args := flag.Args()

	if service.IsService() {
		err := service.Run(func(stop <-chan struct{}) error { return run(*dataDir, stop, true) })
		if err != nil {
			os.Exit(1)
		}
		return
	}

	if cli.Has(args) {
		os.Exit(cli.Main(args, *dataDir, *jsonOut))
	}
	cmd := "run"
	if len(args) > 0 {
		cmd = args[0]
	}
	var err error
	switch cmd {
	case "run":
		stop := make(chan struct{})
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		go func() { <-sig; close(stop) }()
		err = run(*dataDir, stop, false)
	case "service":
		err = serviceCmd(args[1:], *dataDir)
	case "reset-password":
		name := "admin"
		if len(args) > 1 {
			name = args[1]
		}
		err = resetPassword(*dataDir, name)
	case "version":
		fmt.Printf("NodeHoster %s (%s)\n", config.Version, config.Commit)
	case "help", "-h", "--help":
		fmt.Fprintf(os.Stdout, usage, config.Version, cli.Usage(), config.DefaultDataDir())
	default:
		flag.Usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func serviceCmd(args []string, dataDir string) error {
	if len(args) == 0 {
		return errors.New("service: expected install, uninstall, start, stop or status")
	}
	switch args[0] {
	case "install":
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		var svcArgs []string
		if dataDir != config.DefaultDataDir() {
			abs, _ := filepath.Abs(dataDir)
			svcArgs = append(svcArgs, "--data", abs)
		}
		if err := service.Install(exe, svcArgs...); err != nil {
			return err
		}
		fmt.Println("Service registered. Start it with: nodehoster service start")
	case "uninstall":
		if err := service.Uninstall(); err != nil {
			return err
		}
		fmt.Println("Service removed.")
	case "start":
		if err := service.Start(); err != nil {
			return err
		}
		fmt.Println("Service started.")
	case "stop":
		if err := service.Stop(); err != nil {
			return err
		}
		fmt.Println("Service stopped.")
	case "status":
		st, err := service.Status()
		if err != nil {
			return err
		}
		fmt.Println(st)
	default:
		return fmt.Errorf("service: unknown command %q", args[0])
	}
	return nil
}

func resetPassword(dataDir, name string) error {
	paths := config.NewPaths(dataDir)
	st, err := store.Open(paths.DB)
	if err != nil {
		return err
	}
	defer st.Close()
	box, err := secrets.Open(filepath.Join(paths.Data, "master.key"))
	if err != nil {
		return err
	}
	pw, err := auth.New(st, box).ResetPassword(context.Background(), name)
	if err != nil {
		return err
	}
	fmt.Printf("New password for %s: %s\nYou will be asked to change it at the next sign-in.\n", name, pw)
	switch on, err := allowPasswordSignIn(dataDir, st); {
	case err != nil:
		fmt.Fprintf(os.Stderr, "Password sign-in is turned off by the single sign-on settings and could not be turned back on: %v\n", err)
	case on:
		fmt.Println("Password sign-in was turned off by the single sign-on settings; it is on again.")
	}
	return nil
}

// allowPasswordSignIn turns password sign-in back on when single sign-on
// turned it off, since the new password would be useless otherwise: this
// is the break-glass for an identity provider that is down or
// misconfigured. The running service caches its settings, so it is asked
// over the admin pipe; when it is not running, the stored settings are
// changed directly.
func allowPasswordSignIn(dataDir string, st *store.Store) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var cur model.Settings
	if err := st.GetDoc(ctx, "settings", &cur); err != nil {
		return false, nil // never saved: password sign-in is on
	}
	if cur.SSO.PasswordAllowed() {
		return false, nil
	}
	cl := localapi.Connect(localapi.Admin, dataDir)
	// The settings document is sent back as it came (secrets masked),
	// so that fields this build does not know survive.
	var doc map[string]any
	err := cl.Get(ctx, "/api/settings", &doc)
	if errors.Is(err, localapi.ErrNotRunning) {
		cur.SSO.DisablePassword = false
		return true, st.PutDoc(ctx, "settings", cur)
	}
	if err != nil {
		return false, err
	}
	sso, _ := doc["sso"].(map[string]any)
	if sso == nil {
		return false, errors.New("the service's settings have no single sign-on section")
	}
	sso["disablePassword"] = false
	return true, cl.Put(ctx, "/api/settings", doc, nil)
}

func newLogger(paths config.Paths, level string, interactive bool) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	var out io.Writer = &lumberjack.Logger{
		Filename: filepath.Join(paths.Logs, "nodehoster.log"), MaxSize: 20, MaxBackups: 10, MaxAge: 30, LocalTime: true,
	}
	if interactive {
		out = io.MultiWriter(out, os.Stderr)
	}
	return slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: lvl}))
}

func run(dataDir string, stop <-chan struct{}, isService bool) error {
	paths := config.NewPaths(dataDir)
	if err := paths.Ensure(); err != nil {
		return err
	}
	boot, err := config.LoadBootstrap(paths.Config)
	if err != nil {
		return err
	}
	log := newLogger(paths, boot.LogLevel, !isService)
	log.Info("starting NodeHoster", "version", config.Version, "data", paths.Data, "service", isService)

	c, err := core.Open(paths, boot, log)
	if err != nil {
		log.Error("startup failed", "err", err)
		return err
	}
	c.IsService = isService

	if pw, err := c.Auth.EnsureAdmin(context.Background()); err != nil {
		return err
	} else if pw != "" {
		f := filepath.Join(paths.Data, "initial-admin-password.txt")
		config.WriteFileAtomic(f, []byte("Username: admin\nPassword: "+pw+"\n\nYou must change this password at first sign-in. Delete this file afterwards.\n"), 0o600)
		log.Warn("created the initial administrator; the password is in "+f, "username", "admin")
		if !isService {
			fmt.Fprintf(os.Stderr, "\n  Initial login: admin / %s\n\n", pw)
		}
	}

	// A web console that cannot start (its port is taken, its certificate
	// is gone) is reported, not fatal: the sites keep running and the
	// desktop manager, over the local pipe, can fix the console settings.
	admin, err := adminServer(c)
	if err != nil {
		c.AdminError = err.Error()
		log.Error("the web console is not available; use NodeHoster Manager to change its settings", "err", err)
	}
	local := localserver.Serve(c)
	c.Start()

	<-stop
	log.Info("shutting down")
	if admin != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		admin.Shutdown(ctx)
		cancel()
	}
	local.Close()
	c.Shutdown()
	log.Info("stopped")
	return nil
}

// adminServer starts the console listener.
func adminServer(c *core.Core) (*http.Server, error) {
	cfg := c.Boot.Admin
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           api.Handler(c),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
		ErrorLog:          slog.NewLogLogger(c.Log.Handler(), slog.LevelDebug),
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return nil, err
	}
	switch cfg.TLS {
	case "none":
		c.Log.Warn("admin console is served over plain HTTP; enable TLS unless it only listens on localhost", "listen", cfg.Listen)
		go srv.Serve(ln)
	case "certificate":
		id := cfg.CertificateID
		if c.Certs.Get(id) == nil {
			// It listens, but every handshake will fail until the
			// certificate is back or the console setting is changed.
			c.AdminError = "the web console's certificate is not available; browsers cannot connect"
		}
		srv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			if cert := c.Certs.Get(id); cert != nil {
				return cert, nil
			}
			return nil, errors.New("admin certificate is not available")
		}}
		go srv.ServeTLS(ln, "", "")
	default:
		host, _ := os.Hostname()
		pair, err := certs.LoadOrCreateSelfSigned(filepath.Join(c.Paths.Data, "admin-cert.pem"), filepath.Join(c.Paths.Data, "admin-key.pem"),
			[]string{strings.ToLower(host), "localhost", "127.0.0.1", "::1"})
		if err != nil {
			ln.Close()
			return nil, err
		}
		srv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{*pair}}
		go srv.ServeTLS(ln, "", "")
	}
	scheme := "https"
	if cfg.TLS == "none" {
		scheme = "http"
	}
	c.AdminURL = fmt.Sprintf("%s://%s", scheme, cfg.Listen)
	c.Log.Info("admin console listening", "url", c.AdminURL)
	return srv, nil
}
