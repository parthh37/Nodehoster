package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/parthh37/nodehoster/internal/logship"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/secrets"
)

// Log shipping: the server log (through logship.Tee), the sites' output
// (procmgr), access logs (proxy), events and the audit log all go through
// c.Ship, which queues them for the configured targets without ever
// blocking the caller.

// newShipper builds the shipper. Its own warnings go to the log file only:
// if the server logger is a Tee, the shipper gets the Tee's base handler,
// so a collector that is down is never asked to receive its own errors.
func newShipper(log *slog.Logger, siteName func(string) string) *logship.Shipper {
	own := log
	if t, ok := log.Handler().(*logship.Tee); ok {
		own = slog.New(t.Base())
	}
	host, _ := os.Hostname()
	return logship.New(own, host, siteName, logship.Options{})
}

// attachShipper starts shipping the server log, if it is a Tee.
func (c *Core) attachShipper() {
	if t, ok := c.Log.Handler().(*logship.Tee); ok {
		t.Attach(c.Ship)
	}
}

func eventRecord(e model.Event) logship.Record {
	return logship.Record{Time: e.Time, Source: model.LogSourceEvent, Level: e.Level, Message: e.Message,
		SiteID: e.SiteID, Attrs: map[string]string{"event": e.Type}}
}

func appRecord(siteID string, l model.LogLine) logship.Record {
	r := logship.Record{Time: l.Time, Source: model.LogSourceApp, Level: logship.AppLevel(l.Stream, l.Text),
		Message: l.Text, SiteID: siteID, Stream: l.Stream}
	if l.Instance >= 0 {
		i := l.Instance
		r.Instance = &i
	}
	return r
}

// AddAudit records an audit entry and ships it.
func (c *Core) AddAudit(ctx context.Context, e model.AuditEntry) {
	c.Store.AddAudit(ctx, e)
	if c.Ship.Wants(model.LogSourceAudit) {
		msg := fmt.Sprintf("%s: %s %s", e.User, e.Action, e.Target)
		if e.Detail != "" {
			msg += " (" + e.Detail + ")"
		}
		c.Ship.Ship(logship.Record{Time: e.Time, Source: model.LogSourceAudit, Level: "info", Message: msg,
			Attrs: map[string]string{"user": e.User, "ip": e.IP, "action": e.Action, "target": e.Target, "detail": e.Detail}})
	}
}

// targetSecrets are the secret fields of a log target: Seq's API key and
// the HTTP headers marked secret.
func targetSecrets(t *model.LogTarget) []*string {
	var out []*string
	if t.Seq != nil {
		out = append(out, &t.Seq.APIKey)
	}
	if t.HTTP != nil {
		for i := range t.HTTP.Headers {
			if t.HTTP.Headers[i].Secret {
				out = append(out, &t.HTTP.Headers[i].Value)
			}
		}
	}
	return out
}

func cloneTarget(t model.LogTarget) model.LogTarget {
	t.Sources = append([]string(nil), t.Sources...)
	t.SiteIDs = append([]string(nil), t.SiteIDs...)
	if t.Syslog != nil {
		v := *t.Syslog
		t.Syslog = &v
	}
	if t.Seq != nil {
		v := *t.Seq
		t.Seq = &v
	}
	if t.HTTP != nil {
		v := *t.HTTP
		v.Headers = append([]model.HTTPHeader(nil), v.Headers...)
		t.HTTP = &v
	}
	return t
}

// mergeTargetSecrets replaces masked secrets with the stored ones of the
// target with the same ID (headers matched by name).
func mergeTargetSecrets(t *model.LogTarget, stored []model.LogTarget) {
	var old *model.LogTarget
	for i := range stored {
		if t.ID != "" && stored[i].ID == t.ID && stored[i].Type == t.Type {
			o := cloneTarget(stored[i])
			old = &o
		}
	}
	if t.Seq != nil && t.Seq.APIKey == secrets.Mask {
		t.Seq.APIKey = ""
		if old != nil && old.Seq != nil {
			t.Seq.APIKey = old.Seq.APIKey
		}
	}
	if t.HTTP != nil {
		for i := range t.HTTP.Headers {
			h := &t.HTTP.Headers[i]
			if h.Value != secrets.Mask {
				continue
			}
			h.Value = ""
			if old != nil && old.HTTP != nil {
				for _, o := range old.HTTP.Headers {
					if strings.EqualFold(o.Name, h.Name) && o.Secret {
						h.Value = o.Value
					}
				}
			}
		}
	}
}

// prepareLogShipping validates targets and seals their secrets, keeping
// masked ones from cur.
func (c *Core) prepareLogShipping(in *model.LogShippingSettings, cur model.LogShippingSettings) error {
	if in.Targets == nil {
		in.Targets = []model.LogTarget{}
	}
	for i := range in.Targets {
		t := &in.Targets[i]
		*t = cloneTarget(*t)
		if t.ID == "" {
			t.ID = uuid.NewString()
		}
		mergeTargetSecrets(t, cur.Targets)
	}
	if err := in.Validate(); err != nil {
		return err
	}
	for i := range in.Targets {
		for _, p := range targetSecrets(&in.Targets[i]) {
			v, err := c.Box.Seal(*p)
			if err != nil {
				return err
			}
			*p = v
		}
	}
	return nil
}

func maskLogShipping(s *model.LogShippingSettings) {
	out := make([]model.LogTarget, len(s.Targets))
	for i, t := range s.Targets {
		t = cloneTarget(t)
		for _, p := range targetSecrets(&t) {
			if *p != "" {
				*p = secrets.Mask
			}
		}
		out[i] = t
	}
	s.Targets = out
}

func (c *Core) unsealTarget(t model.LogTarget) (model.LogTarget, error) {
	t = cloneTarget(t)
	for _, p := range targetSecrets(&t) {
		v, err := c.Box.Unseal(*p)
		if err != nil {
			return t, fmt.Errorf("a secret of log target %q cannot be decrypted; enter it again", t.Name)
		}
		*p = v
	}
	return t, nil
}

// applyLogShipping (re)configures the shipper from settings.
func (c *Core) applyLogShipping(s model.LogShippingSettings) {
	var plain []model.LogTarget
	for _, t := range s.Targets {
		p, err := c.unsealTarget(t)
		if err != nil {
			c.Log.Warn("log shipping target not started", "target", t.Name, "err", err)
			continue
		}
		plain = append(plain, p)
	}
	c.Ship.Configure(plain)
}

// LogShippingStatus is every target with its delivery counters.
func (c *Core) LogShippingStatus() []logship.Status {
	running := map[string]logship.Status{}
	for _, st := range c.Ship.Status() {
		running[st.ID] = st
	}
	out := []logship.Status{}
	for _, t := range c.Settings().LogShipping.Targets {
		st, ok := running[t.ID]
		if !ok {
			st = logship.Status{ID: t.ID, Name: t.Name, Type: t.Type, Enabled: false}
		}
		out = append(out, st)
	}
	return out
}

// TestLogTarget sends a test message to a target as edited (masked secrets
// come from the saved target with the same ID).
func (c *Core) TestLogTarget(ctx context.Context, in model.LogTarget) error {
	t := cloneTarget(in)
	mergeTargetSecrets(&t, c.Settings().LogShipping.Targets)
	t, err := c.unsealTarget(t)
	if err != nil {
		return err
	}
	if t.Name == "" {
		t.Name = "test"
	}
	if err := t.Validate("target"); err != nil {
		return err
	}
	if err := c.Ship.Test(ctx, t); err != nil {
		var ve *model.ValidationError
		if errors.As(err, &ve) {
			return err
		}
		return &TargetError{err}
	}
	return nil
}

// TargetError is a collector's refusal or a connection failure.
type TargetError struct{ Err error }

func (e *TargetError) Error() string { return "delivery failed: " + e.Err.Error() }
func (e *TargetError) Unwrap() error { return e.Err }
