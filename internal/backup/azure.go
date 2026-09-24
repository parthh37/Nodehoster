package backup

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

// A small Azure Blob Storage client over the REST API (Put Blob, Put Block
// and Put Block List, List Blobs, Get Blob, Delete Blob), authorized with a
// SAS token or Shared Key, for the same reason as the S3 client: the SDK is
// large and four calls are not.
//
// Archives up to azureSingleMax go up in one Put Blob; larger ones in
// azureBlockSize blocks, which allows archives far larger than any server
// will produce (50,000 blocks).

const azureVersion = "2021-08-06"

// Variables so tests can use small blocks.
var (
	azureSingleMax int64 = 64 << 20
	azureBlockSize int64 = 32 << 20
)

type azureTarget struct {
	cfg    model.AzureDest
	key    []byte // decoded account key, when not using SAS
	client *http.Client
	now    func() time.Time
}

func newAzure(cfg model.AzureDest, client *http.Client) *azureTarget {
	t := &azureTarget{cfg: cfg, client: client, now: time.Now}
	if cfg.SASToken == "" {
		t.key, _ = base64.StdEncoding.DecodeString(strings.TrimSpace(cfg.AccountKey))
	}
	return t
}

func (t *azureTarget) url(blob string, q url.Values) *url.URL {
	ep := t.cfg.Endpoint
	if ep == "" {
		ep = "https://" + t.cfg.Account + ".blob.core.windows.net"
	}
	u, _ := url.Parse(ep)
	u.Path = strings.TrimRight(u.Path, "/") + "/" + t.cfg.Container
	if blob != "" {
		u.Path += "/" + blob
	}
	u.RawPath = ""
	if q == nil {
		q = url.Values{}
	}
	if t.cfg.SASToken != "" {
		sas, _ := url.ParseQuery(t.cfg.SASToken)
		for k, v := range sas {
			q[k] = v
		}
	}
	u.RawQuery = q.Encode()
	return u
}

func (t *azureTarget) do(ctx context.Context, method string, u *url.URL, body io.Reader, size int64, hdr http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	req.ContentLength = size
	if body == nil {
		req.ContentLength = 0
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	req.Header.Set("x-ms-version", azureVersion)
	req.Header.Set("x-ms-date", t.now().UTC().Format(http.TimeFormat))
	if t.cfg.SASToken == "" {
		if len(t.key) == 0 {
			return nil, fmt.Errorf("the Azure account key is not valid base64")
		}
		req.Header.Set("Authorization", "SharedKey "+t.cfg.Account+":"+azureSign(req, t.cfg.Account, t.key))
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, redactQuery(err)
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		return nil, azureError(resp)
	}
	return resp, nil
}

func (t *azureTarget) Put(ctx context.Context, name, path string) error {
	if err := checkObjectName(name); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	blob := t.cfg.Prefix + name
	if st.Size() <= azureSingleMax {
		hdr := http.Header{"X-Ms-Blob-Type": {"BlockBlob"}, "Content-Type": {"application/zip"}}
		resp, err := t.do(ctx, http.MethodPut, t.url(blob, nil), readerCtx{ctx, f}, st.Size(), hdr)
		if err != nil {
			return err
		}
		resp.Body.Close()
		return nil
	}
	var ids []string
	for off, i := int64(0), 0; off < st.Size(); off, i = off+azureBlockSize, i+1 {
		n := min(azureBlockSize, st.Size()-off)
		id := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("nhblock-%08d", i)))
		q := url.Values{"comp": {"block"}, "blockid": {id}}
		resp, err := t.do(ctx, http.MethodPut, t.url(blob, q), readerCtx{ctx, io.NewSectionReader(f, off, n)}, n, nil)
		if err != nil {
			return fmt.Errorf("block %d: %w", i+1, err)
		}
		resp.Body.Close()
		ids = append(ids, id)
	}
	var list bytes.Buffer
	list.WriteString(`<?xml version="1.0" encoding="utf-8"?><BlockList>`)
	for _, id := range ids {
		list.WriteString("<Latest>" + id + "</Latest>")
	}
	list.WriteString("</BlockList>")
	hdr := http.Header{"X-Ms-Blob-Content-Type": {"application/zip"}, "Content-Type": {"application/xml"}}
	resp, err := t.do(ctx, http.MethodPut, t.url(blob, url.Values{"comp": {"blocklist"}}), bytes.NewReader(list.Bytes()), int64(list.Len()), hdr)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

type azureList struct {
	Blobs struct {
		Blob []struct {
			Name       string `xml:"Name"`
			Properties struct {
				Length       int64  `xml:"Content-Length"`
				LastModified string `xml:"Last-Modified"`
			} `xml:"Properties"`
		} `xml:"Blob"`
	} `xml:"Blobs"`
	NextMarker string `xml:"NextMarker"`
}

func (t *azureTarget) List(ctx context.Context) ([]Object, error) {
	out := []Object{}
	marker := ""
	for page := 0; page < 1000; page++ {
		q := url.Values{"restype": {"container"}, "comp": {"list"}, "delimiter": {"/"}}
		if t.cfg.Prefix != "" {
			q.Set("prefix", t.cfg.Prefix)
		}
		if marker != "" {
			q.Set("marker", marker)
		}
		resp, err := t.do(ctx, http.MethodGet, t.url("", q), nil, 0, nil)
		if err != nil {
			return nil, err
		}
		var l azureList
		err = xml.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&l)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("Azure returned an unexpected listing: %w", err)
		}
		for _, b := range l.Blobs.Blob {
			name := strings.TrimPrefix(b.Name, t.cfg.Prefix)
			if name == "" || strings.Contains(name, "/") {
				continue
			}
			mod, _ := time.Parse(http.TimeFormat, b.Properties.LastModified)
			out = append(out, Object{Name: name, Size: b.Properties.Length, Modified: mod})
		}
		if l.NextMarker == "" {
			break
		}
		marker = l.NextMarker
	}
	return out, nil
}

func (t *azureTarget) Open(ctx context.Context, name string) (io.ReadCloser, error) {
	if err := checkObjectName(name); err != nil {
		return nil, err
	}
	resp, err := t.do(ctx, http.MethodGet, t.url(t.cfg.Prefix+name, nil), nil, 0, nil)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func (t *azureTarget) Delete(ctx context.Context, name string) error {
	if err := checkObjectName(name); err != nil {
		return err
	}
	resp, err := t.do(ctx, http.MethodDelete, t.url(t.cfg.Prefix+name, nil), nil, 0, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (t *azureTarget) Close() error { return nil }

// redactQuery drops the query string from the URL a transport error
// quotes ("Put \"https://…?sv=…&sig=…\": dial tcp …"): with a SAS token
// the query is the credential, and the error text is kept in the backup
// history and sent in the backup.failed event (webhooks, shipped logs).
// Go only redacts a password in the URL's user info.
func redactQuery(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		ue.URL, _, _ = strings.Cut(ue.URL, "?")
	}
	return err
}

func azureError(resp *http.Response) error {
	var e struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if xml.Unmarshal(data, &e) != nil || e.Code == "" {
		if c := resp.Header.Get("x-ms-error-code"); c != "" {
			return fmt.Errorf("Azure: %s (HTTP %d)", c, resp.StatusCode)
		}
		return fmt.Errorf("Azure: %s", resp.Status)
	}
	msg, _, _ := strings.Cut(strings.TrimSpace(e.Message), "\n") // drops the RequestId/Time lines
	return fmt.Errorf("Azure: %s: %s (HTTP %d)", e.Code, strings.TrimSpace(msg), resp.StatusCode)
}

// azureSign is the Shared Key signature of a Blob service request (version
// 2015-02-21 and later: a zero Content-Length is an empty line).
func azureSign(req *http.Request, account string, key []byte) string {
	h := req.Header
	length := ""
	if req.ContentLength > 0 {
		length = strconv.FormatInt(req.ContentLength, 10)
	}
	var b strings.Builder
	for _, v := range []string{
		req.Method,
		h.Get("Content-Encoding"),
		h.Get("Content-Language"),
		length,
		h.Get("Content-MD5"),
		h.Get("Content-Type"),
		"", // Date: x-ms-date is used instead
		h.Get("If-Modified-Since"),
		h.Get("If-Match"),
		h.Get("If-None-Match"),
		h.Get("If-Unmodified-Since"),
		h.Get("Range"),
	} {
		b.WriteString(v + "\n")
	}
	var ms []string
	for k := range h {
		if lk := strings.ToLower(k); strings.HasPrefix(lk, "x-ms-") {
			ms = append(ms, lk)
		}
	}
	sort.Strings(ms)
	for _, k := range ms {
		b.WriteString(k + ":" + strings.TrimSpace(h.Get(k)) + "\n")
	}
	b.WriteString("/" + account + req.URL.EscapedPath())
	q := req.URL.Query()
	var names []string
	for k := range q {
		names = append(names, k)
	}
	sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })
	for _, k := range names {
		vs := append([]string(nil), q[k]...)
		sort.Strings(vs)
		b.WriteString("\n" + strings.ToLower(k) + ":" + strings.Join(vs, ","))
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte(b.String()))
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}
