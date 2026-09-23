package logship

import (
	"context"
	"log/slog"
	"strings"
	"sync/atomic"

	"github.com/parthh37/nodehoster/internal/model"
)

// Tee is the server logger's handler: records go to the log file (the base
// handler) and, once a shipper is attached, to the targets that take the
// server log. The shipper itself logs through the base handler only, so a
// failing collector's errors are never shipped back to it.
type Tee struct {
	base   slog.Handler
	ship   *atomic.Pointer[Shipper]
	attrs  []slog.Attr
	groups string // "a.b." prefix for attributes added after WithGroup
}

func NewTee(base slog.Handler) *Tee {
	return &Tee{base: base, ship: &atomic.Pointer[Shipper]{}}
}

// Attach starts shipping to s.
func (t *Tee) Attach(s *Shipper) { t.ship.Store(s) }

// Base is the handler without shipping, for the shipper's own logging.
func (t *Tee) Base() slog.Handler { return t.base }

func levelName(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "error"
	case l >= slog.LevelWarn:
		return "warning"
	case l >= slog.LevelInfo:
		return "info"
	}
	return "debug"
}

func (t *Tee) Enabled(ctx context.Context, l slog.Level) bool {
	return t.base.Enabled(ctx, l) || t.ship.Load().WantsServerLevel(levelName(l))
}

func (t *Tee) Handle(ctx context.Context, r slog.Record) error {
	var err error
	if t.base.Enabled(ctx, r.Level) {
		err = t.base.Handle(ctx, r)
	}
	s := t.ship.Load()
	if lvl := levelName(r.Level); s.WantsServerLevel(lvl) {
		rec := Record{Time: r.Time, Source: model.LogSourceServer, Level: lvl, Message: r.Message}
		attrs := map[string]string{}
		for _, a := range t.attrs {
			flatten(attrs, "", a)
		}
		r.Attrs(func(a slog.Attr) bool {
			flatten(attrs, t.groups, a)
			return true
		})
		// Records about a site carry it, like the site's own output.
		if id := attrs["site"]; id != "" && !strings.ContainsAny(id, " ") {
			rec.SiteID = id
		}
		if len(attrs) > 0 {
			rec.Attrs = attrs
		}
		s.Ship(rec)
	}
	return err
}

func flatten(m map[string]string, prefix string, a slog.Attr) {
	v := a.Value.Resolve()
	if v.Kind() == slog.KindGroup {
		p := prefix
		if a.Key != "" {
			p += a.Key + "."
		}
		for _, g := range v.Group() {
			flatten(m, p, g)
		}
		return
	}
	if a.Key == "" {
		return
	}
	m[prefix+a.Key] = v.String()
}

func (t *Tee) WithAttrs(as []slog.Attr) slog.Handler {
	c := *t
	c.base = t.base.WithAttrs(as)
	c.attrs = append([]slog.Attr(nil), t.attrs...)
	for _, a := range as {
		if t.groups != "" {
			a = slog.Attr{Key: strings.TrimSuffix(t.groups, ".") + "." + a.Key, Value: a.Value}
		}
		c.attrs = append(c.attrs, a)
	}
	return &c
}

func (t *Tee) WithGroup(name string) slog.Handler {
	if name == "" {
		return t
	}
	c := *t
	c.base = t.base.WithGroup(name)
	c.groups = t.groups + name + "."
	return &c
}
