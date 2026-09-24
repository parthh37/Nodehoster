package localapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/remote"
)

// Client talks to one local endpoint, or to another server's web console
// over HTTPS (ConnectRemote): the same API either way, so that NodeHoster
// Manager's pages and the command line work against both.
type Client struct {
	http   *http.Client // requests with a timeout
	stream *http.Client // long-lived event streams
	base   string       // what paths are relative to
	token  string       // remote servers: the API token
}

// ErrNotRunning means nothing is listening on the endpoint: the service is
// stopped (or not installed), not merely failing requests.
var ErrNotRunning = errors.New("NodeHoster is not running")

// Error is an API error response.
type Error struct {
	Status  int
	Message string
	Field   string // set on validation errors
}

func (e *Error) Error() string { return e.Message }

// Connect returns a client for an endpoint of the local server. dataDir
// only matters outside Windows, where endpoints are sockets in it.
func Connect(e Endpoint, dataDir string) *Client {
	return NewClient(func(ctx context.Context) (net.Conn, error) { return dial(ctx, e, dataDir) })
}

// NewClient returns a client that reaches the server through dial.
func NewClient(dial func(context.Context) (net.Conn, error)) *Client {
	tr := &http.Transport{
		DialContext:         func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx) },
		MaxIdleConns:        4,
		IdleConnTimeout:     30 * time.Second,
		DisableCompression:  true,
		TLSHandshakeTimeout: time.Second,
	}
	return &Client{
		http:   &http.Client{Transport: tr, Timeout: 60 * time.Second},
		stream: &http.Client{Transport: tr},
		base:   localBase,
	}
}

// The host name is a placeholder: the transport dials the pipe regardless.
const localBase = "http://nodehoster.local"

// ConnectRemote returns a client for another server's web console at
// baseURL (see model.NormalizeServerURL), authenticated by an API token
// created there. TLS is verified against the trusted roots, or pinned to
// fingerprint when one is given; redirects are never followed. What the
// client may do is what the token allows; the account endpoints and the
// local pipe's own (/api/local/...) are not there.
func ConnectRemote(baseURL, token, fingerprint string) (*Client, error) {
	base, err := model.NormalizeServerURL(baseURL)
	if err != nil {
		return nil, err
	}
	fp, err := model.ParseFingerprint(fingerprint)
	if err != nil {
		return nil, err
	}
	if token == "" {
		return nil, errors.New("an API token of that server is needed")
	}
	tr, err := remote.NewTransport(base, fp)
	if err != nil {
		return nil, err
	}
	hc := remote.NewClient(tr)
	hc.Timeout = 60 * time.Second
	return &Client{http: hc, stream: remote.NewClient(tr), base: base, token: token}, nil
}

// Remote reports whether the client reaches another server over HTTPS
// rather than the local pipe.
func (c *Client) Remote() bool { return c.token != "" }

// BaseURL is the remote server's web console URL ("" for the local pipe).
func (c *Client) BaseURL() string {
	if !c.Remote() {
		return ""
	}
	return c.base
}

// newRequest builds a request to path, authenticated for a remote server.
func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return req, nil
}

// fail turns an error answer into an *Error; a remote server refusing the
// token is said so, not "not signed in".
func (c *Client) fail(resp *http.Response) error {
	err := decodeError(resp)
	if e, ok := err.(*Error); ok && c.Remote() && e.Status == http.StatusUnauthorized {
		e.Message = remote.Describe(&remote.StatusError{Status: e.Status})
	}
	return err
}

// connError explains a failed connection: to the local pipe, whether the
// service runs; to a remote server, why it could not be reached.
func (c *Client) connError(err error) error {
	if c.Remote() {
		if errors.Is(err, context.Canceled) {
			return err
		}
		return fmt.Errorf("cannot reach %s: %s", c.base, remote.Describe(err))
	}
	return connError(err)
}

// Do sends a request and decodes a JSON response into out (unless nil).
func (c *Client) Do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := c.newRequest(ctx, method, path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return c.connError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return c.fail(resp)
	}
	if out == nil {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) Get(ctx context.Context, path string, out any) error {
	return c.Do(ctx, http.MethodGet, path, nil, out)
}

func (c *Client) Post(ctx context.Context, path string, in, out any) error {
	return c.Do(ctx, http.MethodPost, path, in, out)
}

func (c *Client) Put(ctx context.Context, path string, in, out any) error {
	return c.Do(ctx, http.MethodPut, path, in, out)
}

func (c *Client) Delete(ctx context.Context, path string) error {
	return c.Do(ctx, http.MethodDelete, path, nil, nil)
}

// UploadFile posts a file as a multipart form (field name field), streaming it:
// release archives can be large, so no request timeout applies. The JSON
// response is decoded into out (unless nil).
func (c *Client) UploadFile(ctx context.Context, path, field, filename string, r io.Reader, out any) error {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		fw, err := mw.CreateFormFile(field, filename)
		if err == nil {
			_, err = io.Copy(fw, r)
		}
		if err == nil {
			err = mw.Close()
		}
		pw.CloseWithError(err)
	}()
	req, err := c.newRequest(ctx, http.MethodPost, path, pr)
	if err != nil {
		pr.Close()
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := c.stream.Do(req)
	if err != nil {
		return c.connError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return c.fail(resp)
	}
	if out == nil {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Download copies the body of a GET to w and returns the file name the
// server suggests (Content-Disposition) and the number of bytes copied.
// Unlike Get it has no timeout of its own: large downloads (backups) are
// bounded by ctx only.
func (c *Client) Download(ctx context.Context, path string, w io.Writer) (name string, n int64, err error) {
	req, err := c.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return "", 0, err
	}
	resp, err := c.stream.Do(req)
	if err != nil {
		return "", 0, c.connError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", 0, c.fail(resp)
	}
	if _, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition")); err == nil {
		name = params["filename"]
	}
	n, err = io.Copy(w, resp.Body)
	return name, n, err
}

// Upload POSTs body as is (not JSON) and decodes a JSON response into out
// (unless nil). Like Download it is bounded by ctx only.
func (c *Client) Upload(ctx context.Context, path, contentType string, body io.Reader, header http.Header, out any) error {
	req, err := c.newRequest(ctx, http.MethodPost, path, body)
	if err != nil {
		return err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := c.stream.Do(req)
	if err != nil {
		return c.connError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return c.fail(resp)
	}
	if out == nil {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Stream reads server-sent events from path until ctx ends or the stream
// breaks, calling fn for each event. It returns the reason it stopped.
func (c *Client) Stream(ctx context.Context, path string, fn func(event string, data []byte)) error {
	req, err := c.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	resp, err := c.stream.Do(req)
	if err != nil {
		return c.connError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return c.fail(resp)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	var event string
	var data []byte
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if event != "" || data != nil {
				fn(event, data)
			}
			event, data = "", nil
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(line[len("event:"):])
		case strings.HasPrefix(line, "data:"):
			if data != nil {
				data = append(data, '\n')
			}
			data = append(data, strings.TrimPrefix(line[len("data:"):], " ")...)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return io.ErrUnexpectedEOF
}

func decodeError(resp *http.Response) error {
	var e struct {
		Error string `json:"error"`
		Field string `json:"field"`
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if json.Unmarshal(data, &e) != nil || e.Error == "" {
		e.Error = strings.TrimSpace(string(data))
		if e.Error == "" {
			e.Error = resp.Status
		}
	}
	return &Error{Status: resp.StatusCode, Message: e.Error, Field: e.Field}
}

// connError turns "nothing is listening" into ErrNotRunning. Other failures
// (for example access denied on the pipe) are returned as they are.
func connError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	if notListening(err) {
		return fmt.Errorf("%w (%v)", ErrNotRunning, err)
	}
	return err
}

// ---- typed helpers for the calls the desktop manager makes most

// Site is a site with its live status, as the API lists them.
type Site struct {
	*model.Site
	Status model.SiteStatus `json:"status"`
}

func (c *Client) Sites(ctx context.Context) ([]Site, error) {
	var out []Site
	return out, c.Get(ctx, "/api/sites", &out)
}

func (c *Client) Site(ctx context.Context, id string) (*Site, error) {
	var out Site
	return &out, c.Get(ctx, "/api/sites/"+url.PathEscape(id), &out)
}

// SiteAction is start, stop, restart or recycle.
func (c *Client) SiteAction(ctx context.Context, id, action string) error {
	return c.Post(ctx, "/api/sites/"+url.PathEscape(id)+"/"+action, nil, nil)
}

func (c *Client) UpdateSite(ctx context.Context, s *model.Site) (*Site, error) {
	var out Site
	return &out, c.Put(ctx, "/api/sites/"+url.PathEscape(s.ID), s, &out)
}

func (c *Client) ServerInfo(ctx context.Context) (*model.ServerInfo, error) {
	var out model.ServerInfo
	return &out, c.Get(ctx, "/api/server/info", &out)
}

// Summary reads the status endpoint.
func (c *Client) Summary(ctx context.Context) (*Summary, error) {
	var out Summary
	return &out, c.Get(ctx, "/status", &out)
}
