package proxy

import "crypto/tls"

// insecureTLS is used only for upstreams explicitly configured to skip
// certificate verification (typically self-signed internal services).
func insecureTLS() *tls.Config {
	return &tls.Config{InsecureSkipVerify: true} //nolint:gosec // opt-in per site
}
