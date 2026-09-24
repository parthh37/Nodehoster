package core

import (
	"context"

	"github.com/parthh37/nodehoster/internal/model"
)

// TLSView is the server-wide TLS settings and the HTTP/3 listeners they
// opened (or could not open).
func (c *Core) TLSView() model.TLSView {
	return model.TLSView{TLSSettings: c.Settings().TLS, HTTP3Listeners: c.Proxy.HTTP3Listeners()}
}

// UpdateTLS changes only the TLS settings (for the command line and
// NodeHoster Manager, which do not edit the rest). Listeners follow at
// once: turning HTTP/3 on opens a UDP listener next to each HTTPS one.
func (c *Core) UpdateTLS(ctx context.Context, in model.TLSSettings) (model.TLSView, error) {
	if in.MinVersion != "1.2" && in.MinVersion != "1.3" {
		return c.TLSView(), &model.ValidationError{Field: "minVersion", Message: "must be 1.2 or 1.3"}
	}
	// The masked settings round-trip: UpdateSettings keeps the stored
	// secrets for masked values, as for the web console's settings page.
	s := c.MaskedSettings()
	s.TLS = in
	if _, err := c.UpdateSettings(ctx, s); err != nil {
		return c.TLSView(), err
	}
	return c.TLSView(), nil
}
