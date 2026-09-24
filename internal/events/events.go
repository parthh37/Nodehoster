// Package events records operational events, fans them out to live
// subscribers (the UI's SSE stream) and delivers them to webhooks.
package events

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
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

	RuntimeInstalled = "runtime.installed" // a Bun or Deno version was installed
	RuntimeFailed    = "runtime.failed"    // installing a Bun or Deno version failed
)

// SecurityBanned: automatic IP banning banned an address (or an
// administrator did).
const SecurityBanned = "security.banned"

// OCSP stapling: a certificate's CA reports it revoked; a Must-Staple
// certificate has no valid response to staple.
const (
	CertRevoked  = "cert.revoked"
	CertStapling = "cert.stapling"
)

// Preview deployments, reported on the parent site.
const (
	PreviewCreated = "preview.created" // the first deployment of a preview succeeded: it is up
	PreviewUpdated = "preview.updated" // a later deployment succeeded
	PreviewDeleted = "preview.deleted" // closed, merged, branch deleted, expired or evicted
	PreviewFailed  = "preview.failed"  // a preview could not be created, deployed or deleted
)

// A server connection (another NodeHoster server managed from this one)
// stopped answering, or answers again.
const (
	RemoteDown = "remote.down"
	RemoteUp   = "remote.up"
)

// Resource alerts (internal/alerts): a rule's condition has held for its
// "for" period (and, as reminders, still does), and it has cleared.
// Critical alerts are errors, warnings warnings.
const (
	AlertFiring   = "alert.firing"
	AlertResolved = "alert.resolved"
)

// Deployment slots: a swap moved a slot's release into production, or it
// was abandoned (warm-up or preparation failed) and nothing changed.
const (
	SlotSwapped    = "slot.swapped"
	SlotSwapFailed = "slot.swap_failed"
)

// SecurityWAF: the web application firewall blocked a request (at most
// 10 a minute; the rest are summarized).
const SecurityWAF = "security.waf"

var AllTypes = []string{SiteStarted, SiteStopped, SiteCrashed, SiteFailed, SiteRecycled, SiteUnhealthy,
	DeploySucceeded, DeployFailed, CertIssued, CertRenewed, CertFailed, CertExpiring, UpstreamDown, UpstreamUp, ServerStarted,
	MailFailed, MailError, SecurityBanned, TaskFailed, TaskTimeout, BackupCompleted, BackupFailed,
	UpdateAvailable, UpdateInstalling, UpdateInstalled, UpdateFailed, CertRevoked, CertStapling,
	PreviewCreated, PreviewUpdated, PreviewDeleted, PreviewFailed,
	RemoteDown, RemoteUp,
	AlertFiring, AlertResolved,
	SlotSwapped, SlotSwapFailed,
	RuntimeInstalled, RuntimeFailed,
	SecurityWAF}

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

// payload is the body of a webhook in its format. Chat formats show the
// event's message and site name, which can hold text from anyone (a
// blocked request's path, a site named by a site-scoped user): it is
// escaped for the chat so that it cannot mention everyone, link somewhere
// or format the message.
func (b *Bus) payload(format string, e model.Event) any {
	site := ""
	if e.SiteID != "" && b.siteName != nil {
		site = b.siteName(e.SiteID)
	}
	icon := map[string]string{"info": "ℹ️", "warning": "⚠️", "error": "🛑"}[e.Level]
	title := fmt.Sprintf("%s %s", icon, e.Type)
	text := func(esc func(string) string) string {
		if site != "" {
			return fmt.Sprintf("[%s] %s", esc(site), esc(e.Message))
		}
		return esc(e.Message)
	}
	switch strings.ToLower(format) {
	case "slack":
		return map[string]any{"text": fmt.Sprintf("*%s*\n%s", title, text(slackEscape))}
	case "discord":
		return map[string]any{
			"content": fmt.Sprintf("**%s**\n%s", title, text(discordEscape)),
			// No pings, whatever the text says.
			"allowed_mentions": map[string]any{"parse": []string{}},
		}
	case "teams":
		color := map[string]string{"info": "Good", "warning": "Warning", "error": "Attention"}[e.Level]
		// The text is a TextRun of a RichTextBlock, which Teams shows as
		// it is: a TextBlock would render it as Markdown.
		run := func(text string, style map[string]any) map[string]any {
			tr := map[string]any{"type": "TextRun", "text": text}
			maps.Copy(tr, style)
			return map[string]any{"type": "RichTextBlock", "inlines": []any{tr}}
		}
		return map[string]any{
			"type": "message",
			"attachments": []any{map[string]any{
				"contentType": "application/vnd.microsoft.card.adaptive",
				"content": map[string]any{
					"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
					"type":    "AdaptiveCard", "version": "1.4",
					"body": []any{
						run(title, map[string]any{"weight": "Bolder", "color": color}),
						run(text(func(s string) string { return s }), nil),
					},
				},
			}},
		}
	default:
		return map[string]any{"event": e, "site": site, "source": "nodehoster"}
	}
}

// slackEscape escapes text for a Slack message: &, < and > as entities, as
// Slack asks, which leaves no <!channel>, <@user> or <url|link>.
var slackEscape = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace

// discordMarkdown are the characters Discord's Markdown gives a meaning.
const discordMarkdown = "\\*_~`|<>[]()#-:"

// discordEscape escapes text for a Discord message: a backslash before
// every Markdown character (no formatting, masked links, headings or
// mentions) and a zero-width space in @everyone and @here, which
// allowed_mentions already keeps from pinging.
func discordEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(discordMarkdown, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return strings.NewReplacer("@everyone", "@\u200beveryone", "@here", "@\u200bhere").Replace(b.String())
}
