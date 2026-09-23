// Package localserver serves the local endpoints of package localapi from
// a running NodeHoster server.
package localserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/parthh37/nodehoster/internal/api"
	"github.com/parthh37/nodehoster/internal/config"
	"github.com/parthh37/nodehoster/internal/core"
	"github.com/parthh37/nodehoster/internal/events"
	"github.com/parthh37/nodehoster/internal/localapi"
	"github.com/parthh37/nodehoster/internal/model"
)

// Server runs both endpoints.
type Server struct {
	servers []*http.Server
	wg      sync.WaitGroup
}

// Serve starts the local endpoints. A failure to open one is logged and
// the other still runs: the server must keep hosting sites regardless.
func Serve(c *core.Core) *Server {
	s := &Server{}
	s.start(c.Log, localapi.Admin, c.Paths, api.LocalHandler(c), func(ctx context.Context, conn net.Conn) context.Context {
		account, err := clientAccount(conn)
		if err != nil {
			c.Log.Warn("local admin pipe: cannot identify the client", "err", err)
			account = "unknown"
		}
		return api.WithLocalUser(ctx, account)
	})
	s.start(c.Log, localapi.Status, c.Paths, statusHandler(c), nil)
	return s
}

func (s *Server) start(log *slog.Logger, e localapi.Endpoint, paths config.Paths, h http.Handler, connCtx func(context.Context, net.Conn) context.Context) {
	ln, addr, err := listen(e, paths)
	if err != nil {
		log.Error("local "+e.String()+" endpoint unavailable; the desktop manager cannot connect", "err", err)
		return
	}
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ConnContext:       connCtx,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelDebug),
	}
	s.servers = append(s.servers, srv)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("local "+e.String()+" endpoint stopped", "err", err)
		}
	}()
	log.Info("local "+e.String()+" endpoint listening", "address", addr)
}

// Close stops both endpoints. Open event streams are cut, not drained.
func (s *Server) Close() {
	for _, srv := range s.servers {
		srv.Close()
	}
	s.wg.Wait()
}

func statusHandler(c *core.Core) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(summary(c))
	})
	// status/stream sends a summary every few seconds and notices as they
	// happen, as server-sent events ("summary" and "notice").
	mux.HandleFunc("GET /status/stream", func(w http.ResponseWriter, r *http.Request) {
		feed, cancel := c.Bus.Subscribe()
		defer cancel()
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-store")
		rc := http.NewResponseController(w)
		send := func(event string, v any) error {
			data, _ := json.Marshal(v)
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
				return err
			}
			return rc.Flush()
		}
		tick := time.NewTicker(3 * time.Second)
		defer tick.Stop()
		if send("summary", summary(c)) != nil {
			return
		}
		for {
			select {
			case <-r.Context().Done():
				return
			case e := <-feed:
				if n, ok := notice(c, e); ok && send("notice", n) != nil {
					return
				}
			case <-tick.C:
				if send("summary", summary(c)) != nil {
					return
				}
			}
		}
	})
	return mux
}

func summary(c *core.Core) localapi.Summary {
	out := localapi.Summary{Version: config.Version, StartedAt: c.StartedAt, AdminURL: c.AdminURL, AdminError: c.AdminError, Sites: []localapi.SiteSummary{}}
	for _, s := range c.Sites() {
		st := c.Status(s)
		ss := localapi.SiteSummary{ID: s.ID, Name: s.Name, Type: s.Type, AutoStart: s.AutoStart, State: st.State, Message: st.Message}
		if s.Node != nil {
			ss.Instances = s.Node.Instances
		}
		for _, in := range st.Instances {
			if in.State == "ready" {
				ss.Ready++
			}
		}
		out.Sites = append(out.Sites, ss)
	}
	return out
}

// noticeTypes are the events worth interrupting someone for.
var noticeTypes = map[string]bool{
	events.SiteCrashed: true, events.SiteFailed: true, events.SiteUnhealthy: true,
	events.DeployFailed: true, events.DeploySucceeded: true,
	events.CertFailed: true, events.CertExpiring: true, events.UpstreamDown: true,
	events.MailFailed: true, events.MailError: true,
}

func notice(c *core.Core, e model.Event) (localapi.Notice, bool) {
	if !noticeTypes[e.Type] {
		return localapi.Notice{}, false
	}
	n := localapi.Notice{Time: e.Time, Level: e.Level, Type: e.Type, Message: e.Message}
	if e.SiteID != "" {
		if s, err := c.Site(e.SiteID); err == nil {
			n.Site = s.Name
		}
	}
	return n, true
}
