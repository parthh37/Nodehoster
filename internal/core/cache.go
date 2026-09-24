package core

import (
	"context"

	"github.com/parthh37/nodehoster/internal/events"
)

// invalidateCaches empties a site's response cache whenever its
// application is replaced without its configuration changing: a recycle
// (manual, scheduled, on file changes) may bring new code. A configuration
// change or a deployment activation compiles the site afresh, which starts
// an empty cache anyway.
func (c *Core) invalidateCaches(ctx context.Context) {
	ch, unsubscribe := c.Bus.Subscribe()
	defer unsubscribe()
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-ch:
			if e.Type == events.SiteRecycled && e.SiteID != "" {
				c.Proxy.PurgeCache(e.SiteID, "")
			}
		}
	}
}
