package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/localapi"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/remote"
)

// Remote servers. `nodehoster --server <name|url> <command>` runs a
// management command against another server's web console over HTTPS
// with an API token created there, instead of the local admin pipe: a
// connection saved with `nodehoster server add` (shared with NodeHoster
// Manager's "Connect to a server…", token protected with DPAPI for the
// current Windows user), or a URL with --token or NODEHOSTER_TOKEN. The
// token's role on that server applies; commands that work on this
// computer's files (deps, server) cannot target another.

// Server and Token are --server and --token; main sets them.
var Server, Token string

// Environment variables for scripts, so that the token is not on the
// command line (where other processes can read it).
const (
	tokenEnv       = "NODEHOSTER_TOKEN"
	fingerprintEnv = "NODEHOSTER_FINGERPRINT" // pins the certificate of a --server URL
)

// savedPath is where the current user's connections are kept (tests
// replace it).
var savedPath = remote.SavedPath

// localOnly are the command groups that work on this computer only.
var localOnly = []string{"deps", "server"}

func init() {
	register(
		&Command{Name: "server list", Summary: "List the connections to other servers saved for this Windows user",
			Setup: func(*flag.FlagSet) Runner { return serverList }},
		&Command{Name: "server add", Args: "<name> <url>", MinArgs: 2, MaxArgs: 2,
			Summary: "Save a connection to another server's web console (token: --token, " + tokenEnv + " or asked)",
			Setup:   serverAdd},
		&Command{Name: "server remove", Args: "<name>", MinArgs: 1, MaxArgs: 1,
			Summary: "Remove a saved connection", Setup: func(*flag.FlagSet) Runner { return serverRemove }},
		&Command{Name: "server test", Args: "<name>", MinArgs: 1, MaxArgs: 1,
			Summary: "Check a saved connection: version, load, sites and the token's role",
			Setup:   func(*flag.FlagSet) Runner { return serverTest }},
	)
}

// clientFor returns the client management commands use: the local admin
// endpoint, or the server --server names.
func clientFor(dataDir, server, token string) (*localapi.Client, error) {
	if server == "" {
		return localapi.Connect(localapi.Admin, dataDir), nil
	}
	return remoteClient(server, token)
}

// remoteClient connects to a saved connection (by name or URL) or a URL.
// token, or NODEHOSTER_TOKEN, overrides a saved connection's token.
func remoteClient(server, token string) (*localapi.Client, error) {
	if token == "" {
		token = os.Getenv(tokenEnv)
	}
	list, err := loadSavedConnections()
	if err != nil {
		return nil, err
	}
	if s, ok := remote.FindSaved(list, server); ok {
		if token == "" {
			token = s.Token
		}
		if token == "" {
			return nil, fmt.Errorf("the token of %s cannot be read by this Windows account: add the connection again, or pass --token", s.Name)
		}
		return localapi.ConnectRemote(s.URL, token, s.Fingerprint)
	}
	if !strings.Contains(server, "://") && !strings.ContainsAny(server, ".:") {
		return nil, fmt.Errorf("no connection is named %q (nodehoster server list shows them)", server)
	}
	if token == "" {
		return nil, fmt.Errorf("--server %s needs an API token of that server: --token or %s (or save it with nodehoster server add)", server, tokenEnv)
	}
	return localapi.ConnectRemote(server, token, os.Getenv(fingerprintEnv))
}

func loadSavedConnections() ([]remote.Saved, error) {
	p, err := savedPath()
	if err != nil {
		return nil, err
	}
	return remote.LoadSavedFrom(p)
}

func storeSavedConnections(list []remote.Saved) error {
	p, err := savedPath()
	if err != nil {
		return err
	}
	return remote.StoreSavedTo(p, list)
}

// refuseRemote refuses a command that works on this computer when the
// client reaches another server.
func refuseRemote(e *Env, c *Command) error {
	if e.Client == nil || !e.Client.Remote() || !slices.Contains(localOnly, strings.Fields(c.Name)[0]) {
		return nil
	}
	return usagef("nodehoster %s works on this computer: it cannot target --server", strings.Fields(c.Name)[0])
}

// savedView is a saved connection as `server list --json` prints it (no
// token).
type savedView struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Fingerprint string `json:"fingerprint,omitempty"`
	TokenSaved  bool   `json:"tokenSaved"` // readable by this Windows account
}

func serverList(e *Env, _ []string) error {
	list, err := loadSavedConnections()
	if err != nil {
		return err
	}
	if e.JSON {
		out := make([]savedView, 0, len(list))
		for _, s := range list {
			out = append(out, savedView{s.Name, s.URL, s.Fingerprint, s.Token != ""})
		}
		return e.printJSON(out)
	}
	if len(list) == 0 {
		e.printf("No connections are saved. Add one with: nodehoster server add <name> https://<server>:8484\n")
		return nil
	}
	rows := make([][]string, 0, len(list))
	for _, s := range list {
		cert, tok := "trusted roots", "saved"
		if fp := model.FormatFingerprint(s.Fingerprint); len(fp) > 23 {
			cert = "pinned " + fp[:23] + "…"
		}
		if s.Token == "" {
			tok = "unreadable"
		}
		rows = append(rows, []string{s.Name, s.URL, cert, tok})
	}
	e.table([]string{"NAME", "URL", "CERTIFICATE", "TOKEN"}, rows)
	return nil
}

func serverAdd(fs *flag.FlagSet) Runner {
	fp := fs.String("fingerprint", "", "SHA-256 fingerprint of the server's certificate, to pin it (self-signed certificates)")
	return func(e *Env, args []string) error {
		list, err := loadSavedConnections()
		if err != nil {
			return err
		}
		s := remote.Saved{Name: args[0], URL: args[1], Fingerprint: *fp, Token: Token}
		if s.Token == "" {
			s.Token = os.Getenv(tokenEnv)
		}
		in := bufio.NewReader(e.Stdin)
		if s.Token == "" && e.Interactive {
			fmt.Fprintf(e.Stdout, "API token created on %s (Account → API tokens there): ", args[1])
			line, _ := in.ReadString('\n')
			s.Token = strings.TrimSpace(line)
		}
		if err := s.Validate(list); err != nil {
			return usagef("%v", err)
		}

		ctx, cancel := context.WithTimeout(e.Ctx, 2*remote.CheckTimeout)
		defer cancel()
		cert, err := remote.Probe(ctx, s.URL)
		if err != nil {
			return err
		}
		switch {
		case cert == nil || (s.Fingerprint == "" && cert.Verified):
		case s.Fingerprint != "":
			if cert.Fingerprint != s.Fingerprint {
				return fmt.Errorf("%s presented another certificate: its SHA-256 fingerprint is %s", s.URL, model.FormatFingerprint(cert.Fingerprint))
			}
		default:
			// Trust on first use, after the administrator compared the
			// fingerprint with the server's own.
			fmt.Fprintf(e.Stdout, "The certificate of %s is not trusted (%s).\n  Subject:     %s\n  Issuer:      %s\n  Valid until: %s\n  SHA-256:     %s\n",
				s.URL, cert.VerifyError, cert.Subject, cert.Issuer, cert.NotAfter.Local().Format(time.DateOnly), model.FormatFingerprint(cert.Fingerprint))
			if !e.Interactive {
				return usagef("check the fingerprint on the server, then add --fingerprint %s", cert.Fingerprint)
			}
			fmt.Fprint(e.Stdout, "Compare the fingerprint with the server's. Trust this certificate? [y/N] ")
			answer, _ := in.ReadString('\n')
			if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
				return errors.New("cancelled; nothing was saved")
			}
			s.Fingerprint = cert.Fingerprint
		}

		tr, err := remote.NewTransport(s.URL, s.Fingerprint)
		if err != nil {
			return err
		}
		defer tr.CloseIdleConnections()
		h := remote.Check(ctx, remote.NewClient(tr), s.URL, s.Token)
		if !h.Reachable {
			return fmt.Errorf("%s: %s", s.URL, h.Error)
		}
		if err := storeSavedConnections(append(list, s)); err != nil {
			return err
		}
		if e.JSON {
			return e.printJSON(savedView{s.Name, s.URL, s.Fingerprint, true})
		}
		e.printf("Saved %s: NodeHoster %s on %s, as %s (%s). Use it with: nodehoster --server %s <command>\n",
			s.Name, h.Version, h.Hostname, h.User, roleText(h.Role), s.Name)
		return nil
	}
}

func serverRemove(e *Env, args []string) error {
	list, err := loadSavedConnections()
	if err != nil {
		return err
	}
	s, ok := remote.FindSaved(list, args[0])
	if !ok {
		return fmt.Errorf("no connection is named %q (nodehoster server list shows them)", args[0])
	}
	list = slices.DeleteFunc(list, func(x remote.Saved) bool { return x.Name == s.Name })
	if err := storeSavedConnections(list); err != nil {
		return err
	}
	e.printf("Removed %s. Revoke its token on the server if nothing else uses it.\n", s.Name)
	return nil
}

func serverTest(e *Env, args []string) error {
	list, err := loadSavedConnections()
	if err != nil {
		return err
	}
	s, ok := remote.FindSaved(list, args[0])
	if !ok {
		return fmt.Errorf("no connection is named %q (nodehoster server list shows them)", args[0])
	}
	token := Token
	if token == "" {
		token = s.Token
	}
	tr, err := remote.NewTransport(s.URL, s.Fingerprint)
	if err != nil {
		return err
	}
	defer tr.CloseIdleConnections()
	h := remote.Check(e.Ctx, remote.NewClient(tr), s.URL, token)
	if e.JSON {
		if err := e.printJSON(h); err != nil {
			return err
		}
	} else if h.Reachable {
		e.printf("%s (%s): reachable in %d ms\n  NodeHoster %s on %s (%s)\n  CPU %.0f%%, memory %s of %s\n  Sites: %d (%d running, %d degraded, %d failed, %d stopped)\n  Token: %s, %s\n",
			s.Name, s.URL, h.LatencyMs, h.Version, h.Hostname, h.OS, h.CPUPercent, sizeText(int64(h.MemUsed)), sizeText(int64(h.MemTotal)),
			h.Sites, h.Running, h.Degraded, h.Failed, h.Stopped, h.User, roleText(h.Role))
	}
	if !h.Reachable {
		return fmt.Errorf("%s: %s", s.Name, h.Error)
	}
	return nil
}

func roleText(r model.Role) string {
	if r == model.RoleSites {
		return "limited to some sites"
	}
	if r == "" {
		return "role unknown"
	}
	return string(r)
}
