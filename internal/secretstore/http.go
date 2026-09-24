package secretstore

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/parthh37/nodehoster/internal/config"
)

// requestTimeout bounds one HTTP request to a store; fetchTimeout bounds
// everything a resolution asks one store (sign-in included).
const (
	requestTimeout = 20 * time.Second
	fetchTimeout   = 45 * time.Second
	maxBody        = 1 << 20 // answers are small; a larger one is not a store's
)

// newHTTPClient returns a client that trusts the system's roots and the
// store's own CA certificates. Verification is never turned off.
func newHTTPClient(caPEM string) (*http.Client, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if strings.TrimSpace(caPEM) != "" {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM([]byte(caPEM)) {
			return nil, errors.New("the store's CA certificate cannot be read")
		}
		tr.TLSClientConfig.RootCAs = pool
	}
	tr.ResponseHeaderTimeout = requestTimeout
	tr.MaxIdleConnsPerHost = 4
	return &http.Client{Transport: tr, Timeout: requestTimeout}, nil
}

// expiry publishes when a provider's session ends, for Status, without
// the provider's lock, which is held during requests that may be slow.
type expiry struct{ unixNano atomic.Int64 } // 0 = unknown

func (e *expiry) set(t time.Time) {
	if t.IsZero() {
		e.unixNano.Store(0)
		return
	}
	e.unixNano.Store(t.UnixNano())
}

func (e *expiry) get() time.Time {
	if n := e.unixNano.Load(); n != 0 {
		return time.Unix(0, n)
	}
	return time.Time{}
}

// userAgent identifies NodeHoster to the stores (their audit logs show it).
func userAgent() string { return "NodeHoster/" + config.Version }

// httpError is a store's answer other than success. Message is the
// store's own explanation when it gave one; bodies of errors never carry
// secret values.
type httpError struct {
	Status  int
	Message string
}

func (e *httpError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("%s (HTTP %d)", e.Message, e.Status)
	}
	return fmt.Sprintf("HTTP %d %s", e.Status, http.StatusText(e.Status))
}

// errNotFound marks an answer that the secret does not exist: resolution
// then fails even with an older value in memory, unlike an outage.
type notFoundError struct{ msg string }

func (e *notFoundError) Error() string { return e.msg }

func notFound(format string, a ...any) error { return &notFoundError{fmt.Sprintf(format, a...)} }

// IsNotFound reports whether err says a secret does not exist in its store.
func IsNotFound(err error) bool {
	var nf *notFoundError
	return errors.As(err, &nf)
}

// do sends a request with a JSON (or form) body and decodes a JSON answer
// into out. A non-2xx answer is an *httpError with the message found by
// errMsg in its body.
func do(ctx context.Context, hc *http.Client, method, url string, header http.Header, body any, out any) error {
	var rd io.Reader
	ctype := ""
	switch b := body.(type) {
	case nil:
	case formBody:
		rd, ctype = strings.NewReader(string(b)), "application/x-www-form-urlencoded; charset=utf-8"
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			return err
		}
		rd, ctype = bytes.NewReader(raw), "application/json"
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent())
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return err
	}
	if len(raw) > maxBody {
		return errors.New("the store's answer is too large")
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &httpError{Status: resp.StatusCode, Message: errMsg(raw)}
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("the store's answer is not what was expected: %v", err)
	}
	return nil
}

// formBody is sent as application/x-www-form-urlencoded.
type formBody string

// errMsg finds the explanation in an error body of any of the stores:
// Vault's {"errors": [...]}, Infisical's {"message"}, Bitwarden's
// {"message"} or OAuth {"error_description", "error"}.
func errMsg(raw []byte) string {
	var b struct {
		Errors           []string `json:"errors"`
		Message          string   `json:"message"`
		ErrorDescription string   `json:"error_description"`
		Error            any      `json:"error"`
	}
	if json.Unmarshal(raw, &b) != nil {
		return ""
	}
	msg := ""
	switch {
	case len(b.Errors) > 0:
		msg = strings.Join(b.Errors, "; ")
	case b.ErrorDescription != "":
		msg = b.ErrorDescription
	case b.Message != "":
		msg = b.Message
	default:
		if s, ok := b.Error.(string); ok {
			msg = s
		}
	}
	msg = strings.Join(strings.Fields(msg), " ")
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	return msg
}

// statusOf is the HTTP status of a store's error answer, or 0.
func statusOf(err error) int {
	var he *httpError
	if errors.As(err, &he) {
		return he.Status
	}
	return 0
}

// valueString turns a JSON value into an environment variable's text:
// strings as they are, anything else (numbers, booleans, objects) as JSON.
func valueString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	raw, _ := json.Marshal(v)
	return string(raw)
}
