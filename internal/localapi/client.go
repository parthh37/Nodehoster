package localapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

// Client talks to one local endpoint.
type Client struct {
	http   *http.Client // requests with a timeout
	stream *http.Client // long-lived event streams
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
	}
}

// The host name is a placeholder: the transport dials the pipe regardless.
const base = "http://nodehoster.local"

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
	req, err := http.NewRequestWithContext(ctx, method, base+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return connError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return decodeError(resp)
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

// Stream reads server-sent events from path until ctx ends or the stream
// breaks, calling fn for each event. It returns the reason it stopped.
func (c *Client) Stream(ctx context.Context, path string, fn func(event string, data []byte)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.stream.Do(req)
	if err != nil {
		return connError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return decodeError(resp)
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
