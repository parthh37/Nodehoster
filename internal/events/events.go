// Package events records operational events, fans them out to live
// subscribers (the UI's SSE stream) and delivers them to webhooks.
package events

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/store"
)

// Event types. Webhook targets filter on these.
const (
	SiteStarted     = "site.started"
	SiteStopped     = "site.stopped"
	SiteCrashed     = "site.crashed"
	SiteFailed      = "site.failed" // rapid-fail protection tripped
	SiteRecycled    = "site.recycled"
	SiteUnhealthy   = "site.unhealthy"
	DeploySucceeded = "deploy.succeeded"
	DeployFailed    = "deploy.failed"
	CertIssued      = "cert.issued"
	CertRenewed     = "cert.renewed"
	CertFailed      = "cert.failed"
	CertExpiring    = "cert.expiring"
	UpstreamDown    = "upstream.down"
	UpstreamUp      = "upstream.up"
	ServerStarted   = "server.started"
	MailFailed      = "mail.failed"  // a message could not be delivered
	MailError       = "mail.error"   // the SMTP server cannot listen
	TaskFailed      = "task.failed"  // a scheduled task exited with an error or could not start
	TaskTimeout     = "task.timeout" // a scheduled task ran past its timeout and was killed
	BackupCompleted = "backup.completed"
	BackupFailed    = "backup.failed" // no destination, or some, received the archive

	UpdateAvailable  = "update.available"  // the release feed has a newer version
	UpdateInstalling = "update.installing" // the updater is starting setup
	UpdateInstalled  = "update.installed"  // the service runs the new version
	UpdateFailed     = "update.failed"     // downloading or installing failed
)

// SecurityBanned: automatic IP banning banned an address (or an
// administrator did).
const SecurityBanned = "security.banned"

// SecurityWAF: the web application firewall blocked a request (at most
// 10 a minute; the rest are summarized).
const SecurityWAF = "security.waf"

var AllTypes = []string{SiteStarted, SiteStopped, SiteCrashed, SiteFailed, SiteRecycled, SiteUnhealthy,
	DeploySucceeded, DeployFailed, CertIssued, CertRenewed, CertFailed, CertExpiring, UpstreamDown, UpstreamUp, ServerStarted,
	MailFailed, MailError, SecurityBanned, TaskFailed, TaskTimeout, BackupCompleted, BackupFailed,
	UpdateAvailable, UpdateInstalling, UpdateInstalled, UpdateFailed, SecurityWAF}

type Bus struct {
	store    *store.Store
	log      *slog.Logger
	settings func() model.Settings
	siteName func(id string) string
	client   *http.Client

	mu   sync.Mutex
	subs map[chan model.Event]struct{}

	// OnEmit, set once before use, sees every event (log shipping). It
	// must not block.
	OnEmit func(model.Event)
}

func New(st *store.Store, log *slog.Logger, settings func() model.Settings, siteName func(string) string) *Bus {
	return &Bus{
		store: st, log: log, settings: settings, siteName: siteName,
		client: &http.Client{Timeout: 10 * time.Second},
		subs:   map[chan model.Event]struct{}{},
	}
}

func (b *Bus) Info(typ, siteID, format string, a ...any) {
	b.Emit("info", typ, siteID, fmt.Sprintf(format, a...))
}
func (b *Bus) Warn(typ, siteID, format string, a ...any) {
	b.Emit("warning", typ, siteID, fmt.Sprintf(format, a...))
}
func (b *Bus) Error(typ, siteID, format string, a ...any) {
	b.Emit("error", typ, siteID, fmt.Sprintf(format, a...))
}

func (b *Bus) Emit(level, typ, siteID, msg string) {
	e := model.Event{Time: time.Now(), Level: level, Type: typ, SiteID: siteID, Message: msg}
	if err := b.store.AddEvent(context.Background(), &e); err != nil {
		b.log.Error("store event", "err", err)
	}
	lvl := slog.LevelInfo
	switch level {
	case "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	}
	b.log.Log(context.Background(), lvl, msg, "event", typ, "site", siteID)

	b.mu.Lock()
	for ch := range b.subs {
		select {
		case ch <- e:
		default: // a slow subscriber drops events rather than blocking the server
		}
	}
	b.mu.Unlock()

	if b.OnEmit != nil {
		b.OnEmit(e)
	}
	go b.deliver(e)
}

// Subscribe returns a channel of live events and a function to unsubscribe.
func (b *Bus) Subscribe() (<-chan model.Event, func()) {
	ch := make(chan model.Event, 64)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

func (b *Bus) deliver(e model.Event) {
	if b.settings == nil {
		return
	}
	for _, w := range b.settings().Webhooks {
		if !w.Enabled || (len(w.Events) > 0 && !slices.Contains(w.Events, e.Type)) {
			continue
		}
		if err := b.Send(context.Background(), w, e); err != nil {
			b.log.Warn("webhook delivery failed", "webhook", w.Name, "err", err)
		}
	}
}

// Send delivers one event to one webhook, retrying transient failures.
func (b *Bus) Send(ctx context.Context, w model.WebhookTarget, e model.Event) error {
	body, err := json.Marshal(b.payload(w.Format, e))
	if err != nil {
		return err
	}
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt*2) * time.Second)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "NodeHoster-Webhook")
		resp, err := b.client.Do(req)
		if err != nil {
			last = err
			continue
		}
		resp.Body.Close()
		if resp.StatusCode < 300 {
			return nil
		}
		last = fmt.Errorf("HTTP %d", resp.StatusCode)
		if resp.StatusCode < 500 && resp.StatusCode != 429 {
			return last
		}
	}
	return last
}

func (b *Bus) payload(format string, e model.Event) any {
	site := ""
	if e.SiteID != "" && b.siteName != nil {
		site = b.siteName(e.SiteID)
	}
	icon := map[string]string{"info": "ℹ️", "warning": "⚠️", "error": "🛑"}[e.Level]
	title := fmt.Sprintf("%s %s", icon, e.Type)
	text := e.Message
	if site != "" {
		text = fmt.Sprintf("[%s] %s", site, e.Message)
	}
	switch strings.ToLower(format) {
	case "slack":
		return map[string]any{"text": fmt.Sprintf("*%s*\n%s", title, text)}
	case "discord":
		return map[string]any{"content": fmt.Sprintf("**%s**\n%s", title, text)}
	case "teams":
		color := map[string]string{"info": "Good", "warning": "Warning", "error": "Attention"}[e.Level]
		return map[string]any{
			"type": "message",
			"attachments": []any{map[string]any{
				"contentType": "application/vnd.microsoft.card.adaptive",
				"content": map[string]any{
					"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
					"type":    "AdaptiveCard", "version": "1.4",
					"body": []any{
						map[string]any{"type": "TextBlock", "text": title, "weight": "Bolder", "color": color},
						map[string]any{"type": "TextBlock", "text": text, "wrap": true},
					},
				},
			}},
		}
	default:
		return map[string]any{"event": e, "site": site, "source": "nodehoster"}
	}
}
