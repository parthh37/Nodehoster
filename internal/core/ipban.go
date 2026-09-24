package core

import (
	"context"
	"errors"
	"net"

	"github.com/parthh37/nodehoster/internal/events"
	"github.com/parthh37/nodehoster/internal/ipban"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/store"
)

// bansDoc holds the bans in force (and recent ones, so that repeat
// offenders are banned for longer) across restarts. A settings document
// rather than a table: the list is small and always read whole.
const bansDoc = "ipban.bans"

func (c *Core) openBans(ctx context.Context) error {
	c.Bans = ipban.New(ipban.Options{
		Log: c.Log,
		Save: func(b []model.Ban) error {
			return c.Store.PutDoc(context.Background(), bansDoc, b)
		},
		OnBan: func(b model.Ban, msg string) {
			c.Bus.Warn(events.SecurityBanned, "", "%s", msg)
		},
	})
	var saved []model.Ban
	if err := c.Store.GetDoc(ctx, bansDoc, &saved); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	c.Bans.Load(saved)
	c.applyBans()
	return nil
}

// applyBans hands the current settings to the ban manager. The trusted
// proxies are never banned: bans apply to the client address they report.
func (c *Core) applyBans() {
	s := c.Settings()
	var trusted []*net.IPNet
	for _, p := range s.Proxy.TrustedProxies {
		if n, err := model.ParseCIDROrIP(p); err == nil {
			trusted = append(trusted, n)
		}
	}
	c.Bans.Apply(s.IPBan, trusted)
}
