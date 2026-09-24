package backup

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

// A small S3 client (PutObject, ListObjectsV2, GetObject, DeleteObject with
// Signature Version 4) rather than the AWS SDK, which would add megabytes
// to the binary for four calls. It works with every S3-compatible service
// that accepts SigV4: AWS, Cloudflare R2, Backblaze B2, MinIO, Wasabi.
//
// Uploads are a single PUT, so an archive can be at most 5 GiB, the S3
// limit for one request; multipart upload is not implemented. The payload
// is hashed before sending (x-amz-content-sha256) rather than sent
// UNSIGNED-PAYLOAD, which some S3-compatible services refuse.

// MaxS3Object is the largest archive an S3 destination accepts.
const MaxS3Object = 5 << 30

type s3Target struct {
	cfg    model.S3Dest
	client *http.Client
	now    func() time.Time
}

func newS3(cfg model.S3Dest, client *http.Client) *s3Target {
	return &s3Target{cfg: cfg, client: client, now: time.Now}
}

// url is the address of key (or the bucket, for key ""), virtual-hosted
// (bucket.host) unless path style is asked for or the bucket name has dots,
// which breaks the wildcard TLS certificate.
func (t *s3Target) url(key string, q url.Values) *url.URL {
	ep := t.cfg.Endpoint
	if ep == "" {
		ep = "https://s3." + t.cfg.Region + ".amazonaws.com"
	}
	u, _ := url.Parse(ep)
	base := strings.TrimRight(u.Path, "/")
	if t.cfg.PathStyle || strings.Contains(t.cfg.Bucket, ".") {
		u.Path = base + "/" + t.cfg.Bucket + "/" + key
	} else {
		u.Host = t.cfg.Bucket + "." + u.Host
		u.Path = base + "/" + key
	}
	u.RawPath = ""
	u.RawQuery = encodeQuery(q)
	return u
}

func (t *s3Target) do(ctx context.Context, method string, u *url.URL, body io.Reader, size int64, payloadHash string, hdr http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	// Keep the path exactly as signed (Go would re-escape some characters).
	req.URL.Opaque = ""
	req.URL.RawPath = s3EscapePath(u.Path)
	if body != nil {
		req.ContentLength = size
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	if payloadHash == "" {
		payloadHash = emptySHA256
	}
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	signV4(req, t.cfg.AccessKeyID, t.cfg.SecretAccessKey, t.cfg.Region, "s3", payloadHash, t.now())
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		return nil, s3Error(resp)
	}
	return resp, nil
}

func (t *s3Target) Put(ctx context.Context, name, path string) error {
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
	if st.Size() > MaxS3Object {
		return fmt.Errorf("the archive is %s, over the 5 GiB an S3 destination accepts in one upload; leave large shared folders out of the backup or use a folder, SFTP or Azure destination", sizeString(st.Size()))
	}
	h := sha256.New()
	if _, err := io.Copy(h, readerCtx{ctx, f}); err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	hdr := http.Header{"Content-Type": {"application/zip"}}
	resp, err := t.do(ctx, http.MethodPut, t.url(t.cfg.Prefix+name, nil), readerCtx{ctx, f}, st.Size(), hex.EncodeToString(h.Sum(nil)), hdr)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

type s3List struct {
	Contents []struct {
		Key          string    `xml:"Key"`
		Size         int64     `xml:"Size"`
		LastModified time.Time `xml:"LastModified"`
	} `xml:"Contents"`
	IsTruncated           bool   `xml:"IsTruncated"`
	NextContinuationToken string `xml:"NextContinuationToken"`
}

func (t *s3Target) List(ctx context.Context) ([]Object, error) {
	out := []Object{}
	token := ""
	for page := 0; page < 1000; page++ {
		q := url.Values{"list-type": {"2"}, "delimiter": {"/"}}
		if t.cfg.Prefix != "" {
			q.Set("prefix", t.cfg.Prefix)
		}
		if token != "" {
			q.Set("continuation-token", token)
		}
		resp, err := t.do(ctx, http.MethodGet, t.url("", q), nil, 0, "", nil)
		if err != nil {
			return nil, err
		}
		var l s3List
		err = xml.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&l)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("S3 returned an unexpected listing: %w", err)
		}
		for _, c := range l.Contents {
			name := strings.TrimPrefix(c.Key, t.cfg.Prefix)
			if name == "" || strings.Contains(name, "/") {
				continue
			}
			out = append(out, Object{Name: name, Size: c.Size, Modified: c.LastModified})
		}
		if !l.IsTruncated || l.NextContinuationToken == "" {
			break
		}
		token = l.NextContinuationToken
	}
	return out, nil
}

func (t *s3Target) Open(ctx context.Context, name string) (io.ReadCloser, error) {
	if err := checkObjectName(name); err != nil {
		return nil, err
	}
	resp, err := t.do(ctx, http.MethodGet, t.url(t.cfg.Prefix+name, nil), nil, 0, "", nil)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func (t *s3Target) Delete(ctx context.Context, name string) error {
	if err := checkObjectName(name); err != nil {
		return err
	}
	resp, err := t.do(ctx, http.MethodDelete, t.url(t.cfg.Prefix+name, nil), nil, 0, "", nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (t *s3Target) Close() error { return nil }

func s3Error(resp *http.Response) error {
	var e struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
		Region  string `xml:"Region"`
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if xml.Unmarshal(data, &e) != nil || e.Code == "" {
		msg := strings.TrimSpace(string(data))
		if len(msg) > 200 || strings.HasPrefix(msg, "<") {
			msg = ""
		}
		if msg == "" {
			msg = resp.Status
		}
		return fmt.Errorf("S3: %s", msg)
	}
	msg := fmt.Sprintf("S3: %s: %s (HTTP %d)", e.Code, e.Message, resp.StatusCode)
	if e.Region != "" {
		msg += "; the bucket is in region " + e.Region
	}
	return fmt.Errorf("%s", msg)
}

// ---- Signature Version 4

const emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// signV4 adds X-Amz-Date and Authorization to req, signing the host and
// every header already set. payloadHash is the hex SHA-256 of the body.
func signV4(req *http.Request, accessKey, secretKey, region, service, payloadHash string, now time.Time) {
	now = now.UTC()
	amzDate := now.Format("20060102T150405Z")
	day := now.Format("20060102")
	req.Header.Set("X-Amz-Date", amzDate)

	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	headers := map[string]string{"host": host}
	for k, v := range req.Header {
		lk := strings.ToLower(k)
		if lk == "authorization" || lk == "user-agent" {
			continue
		}
		for i := range v {
			v[i] = strings.Join(strings.Fields(v[i]), " ")
		}
		headers[lk] = strings.Join(v, ",")
	}
	names := make([]string, 0, len(headers))
	for k := range headers {
		names = append(names, k)
	}
	sort.Strings(names)
	var ch strings.Builder
	for _, k := range names {
		ch.WriteString(k + ":" + headers[k] + "\n")
	}
	signed := strings.Join(names, ";")

	path := req.URL.RawPath
	if path == "" {
		path = s3EscapePath(req.URL.Path)
	}
	if path == "" {
		path = "/"
	}
	canonical := strings.Join([]string{
		req.Method,
		path,
		encodeQuery(req.URL.Query()),
		ch.String(),
		signed,
		payloadHash,
	}, "\n")
	scope := day + "/" + region + "/" + service + "/aws4_request"
	sum := sha256.Sum256([]byte(canonical))
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(sum[:])

	k := hmacSHA256([]byte("AWS4"+secretKey), day)
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, service)
	k = hmacSHA256(k, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(k, toSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+accessKey+"/"+scope+", SignedHeaders="+signed+", Signature="+sig)
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

// uriEncode is SigV4's encoding: RFC 3986 unreserved characters are kept,
// everything else is %XX (so a space is %20, not +).
func uriEncode(s string, keepSlash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~' || (keepSlash && c == '/') {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func s3EscapePath(p string) string { return uriEncode(p, true) }

// encodeQuery is the canonical query string: sorted by name, then value.
func encodeQuery(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	type kv struct{ k, v string }
	var pairs []kv
	for k, vs := range q {
		for _, v := range vs {
			pairs = append(pairs, kv{uriEncode(k, false), uriEncode(v, false)})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].k != pairs[j].k {
			return pairs[i].k < pairs[j].k
		}
		return pairs[i].v < pairs[j].v
	})
	parts := make([]string, len(pairs))
	for i, p := range pairs {
		parts[i] = p.k + "=" + p.v
	}
	return strings.Join(parts, "&")
}
