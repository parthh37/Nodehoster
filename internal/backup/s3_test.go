package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

// AWS's published Signature Version 4 examples.
func TestSigV4Vectors(t *testing.T) {
	t.Parallel()
	const (
		s3Key    = "AKIAIOSFODNN7EXAMPLE"
		s3Secret = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	)
	s3Time := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	sig := func(req *http.Request) string {
		_, s, _ := strings.Cut(req.Header.Get("Authorization"), "Signature=")
		return s
	}
	path := func(req *http.Request) *http.Request {
		req.URL.RawPath = s3EscapePath(req.URL.Path)
		return req
	}

	// Amazon S3 API reference, "Signature Calculations for the
	// Authorization Header: Transferring Payload in a Single Chunk".
	t.Run("GET object", func(t *testing.T) {
		req := path(httptest.NewRequest("GET", "https://examplebucket.s3.amazonaws.com/test.txt", nil))
		req.Host = ""
		req.Header.Set("Range", "bytes=0-9")
		req.Header.Set("X-Amz-Content-Sha256", emptySHA256)
		signV4(req, s3Key, s3Secret, "us-east-1", "s3", emptySHA256, s3Time)
		if got := sig(req); got != "f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41" {
			t.Errorf("signature = %s", got)
		}
		if !strings.Contains(req.Header.Get("Authorization"), "SignedHeaders=host;range;x-amz-content-sha256;x-amz-date,") {
			t.Errorf("Authorization = %s", req.Header.Get("Authorization"))
		}
	})
	t.Run("PUT object", func(t *testing.T) {
		body := "Welcome to Amazon S3."
		sum := sha256.Sum256([]byte(body))
		hash := hex.EncodeToString(sum[:])
		req := path(httptest.NewRequest("PUT", "https://examplebucket.s3.amazonaws.com/test$file.text", strings.NewReader(body)))
		req.Host = ""
		req.Header.Set("Date", "Fri, 24 May 2013 00:00:00 GMT")
		req.Header.Set("X-Amz-Storage-Class", "REDUCED_REDUNDANCY")
		req.Header.Set("X-Amz-Content-Sha256", hash)
		signV4(req, s3Key, s3Secret, "us-east-1", "s3", hash, s3Time)
		if got := sig(req); got != "98ad721746da40c64f1a55b78f14c238d841ea1380cd77a1b5971af0ece108bd" {
			t.Errorf("signature = %s", got)
		}
	})
	t.Run("GET bucket lifecycle", func(t *testing.T) {
		req := path(httptest.NewRequest("GET", "https://examplebucket.s3.amazonaws.com/?lifecycle", nil))
		req.Host = ""
		req.Header.Set("X-Amz-Content-Sha256", emptySHA256)
		signV4(req, s3Key, s3Secret, "us-east-1", "s3", emptySHA256, s3Time)
		if got := sig(req); got != "fea454ca298b7da1c68078a5d1bdbfbbe0d65c699e0f91ac7a200a0136783543" {
			t.Errorf("signature = %s", got)
		}
	})
	t.Run("list objects", func(t *testing.T) {
		req := path(httptest.NewRequest("GET", "https://examplebucket.s3.amazonaws.com/?max-keys=2&prefix=J", nil))
		req.Host = ""
		req.Header.Set("X-Amz-Content-Sha256", emptySHA256)
		signV4(req, s3Key, s3Secret, "us-east-1", "s3", emptySHA256, s3Time)
		if got := sig(req); got != "34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7" {
			t.Errorf("signature = %s", got)
		}
	})
	// The generic Signature Version 4 test suite, "get-vanilla".
	t.Run("get-vanilla", func(t *testing.T) {
		req := path(httptest.NewRequest("GET", "https://example.amazonaws.com/", nil))
		req.Host = ""
		signV4(req, "AKIDEXAMPLE", "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", "us-east-1", "service", emptySHA256, time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC))
		if got := sig(req); got != "5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31" {
			t.Errorf("signature = %s", got)
		}
	})
}

// fakeS3 is an in-memory bucket that checks every request's signature by
// signing what it received again.
type fakeS3 struct {
	t       *testing.T
	secret  string
	mu      sync.Mutex
	objects map[string][]byte
	hosts   []string
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	_, rest, _ := strings.Cut(auth, "SignedHeaders=")
	signedList, _, _ := strings.Cut(rest, ",")
	check, _ := http.NewRequest(r.Method, "http://"+r.Host+r.URL.RequestURI(), nil)
	check.URL.RawPath = r.URL.RawPath
	if check.URL.RawPath == "" {
		check.URL.RawPath = r.URL.EscapedPath()
	}
	for _, h := range strings.Split(signedList, ";") {
		if h != "host" && h != "x-amz-date" {
			check.Header.Set(h, r.Header.Get(h))
		}
	}
	date, _ := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
	body, _ := io.ReadAll(r.Body)
	sum := sha256.Sum256(body)
	if r.Header.Get("X-Amz-Content-Sha256") != hex.EncodeToString(sum[:]) {
		w.WriteHeader(400)
		io.WriteString(w, `<Error><Code>XAmzContentSHA256Mismatch</Code><Message>bad hash</Message></Error>`)
		return
	}
	signV4(check, "AK", f.secret, "eu-west-1", "s3", r.Header.Get("X-Amz-Content-Sha256"), date)
	if check.Header.Get("Authorization") != auth {
		w.WriteHeader(403)
		io.WriteString(w, `<?xml version="1.0"?><Error><Code>SignatureDoesNotMatch</Code><Message>The request signature we calculated does not match the signature you provided.</Message></Error>`)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hosts = append(f.hosts, r.Host)
	key := strings.TrimPrefix(r.URL.Path, "/")
	switch {
	case r.Method == http.MethodPut:
		f.objects[key] = body
	case r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2":
		prefix := r.URL.Query().Get("prefix")
		type content struct {
			Key          string
			Size         int
			LastModified string
		}
		var res struct {
			XMLName     xml.Name `xml:"ListBucketResult"`
			Contents    []content
			IsTruncated bool
		}
		var keys []string
		for k := range f.objects {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if strings.HasPrefix(k, "bucket/"+prefix) {
				res.Contents = append(res.Contents, content{Key: strings.TrimPrefix(k, "bucket/"), Size: len(f.objects[k]), LastModified: "2026-03-01T02:30:05.000Z"})
			}
		}
		xml.NewEncoder(w).Encode(res)
	case r.Method == http.MethodGet:
		b, ok := f.objects[key]
		if !ok {
			w.WriteHeader(404)
			io.WriteString(w, `<Error><Code>NoSuchKey</Code><Message>The specified key does not exist.</Message></Error>`)
			return
		}
		w.Write(b)
	case r.Method == http.MethodDelete:
		delete(f.objects, key)
		w.WriteHeader(204)
	}
}

func tempFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "archive.zip")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// exercise runs a destination through what a backup and a restore do.
func exercise(t *testing.T, tg Target) {
	t.Helper()
	ctx := context.Background()
	name := ArchiveName("web01", time.Date(2026, 3, 1, 2, 30, 5, 0, time.UTC))
	if err := tg.Put(ctx, name, tempFile(t, "archive body")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := tg.Put(ctx, "notes with space & $.txt", tempFile(t, "x")); err != nil {
		t.Fatalf("Put with odd characters: %v", err)
	}
	list, err := tg.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, o := range list {
		if o.Name == name && o.Size == int64(len("archive body")) {
			found = true
		}
	}
	if !found || len(list) != 2 {
		t.Fatalf("List = %+v", list)
	}
	rc, err := tg.Open(ctx, name)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "archive body" {
		t.Errorf("Open = %q", b)
	}
	if err := tg.Delete(ctx, name); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if list, _ := tg.List(ctx); len(list) != 1 {
		t.Errorf("after Delete: %+v", list)
	}
	if err := tg.Put(ctx, "../escape.zip", tempFile(t, "x")); err == nil {
		t.Error("a name with a path was accepted")
	}
	if err := Probe(ctx, tg, t.TempDir()); err != nil {
		t.Errorf("Probe: %v", err)
	}
}

func TestS3Target(t *testing.T) {
	t.Parallel()
	fake := &fakeS3{t: t, secret: "s3cr3t", objects: map[string][]byte{}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	cfg := model.S3Dest{Endpoint: srv.URL, Region: "eu-west-1", Bucket: "bucket", Prefix: "nightly/", AccessKeyID: "AK", SecretAccessKey: "s3cr3t", PathStyle: true}
	exercise(t, newS3(cfg, srv.Client()))
	if _, ok := fake.objects["bucket/nightly/notes with space & $.txt"]; !ok {
		t.Errorf("objects = %v", fake.objects)
	}

	// A wrong secret is reported with S3's own words.
	cfg.SecretAccessKey = "wrong"
	err := newS3(cfg, srv.Client()).Put(context.Background(), "x.zip", tempFile(t, "x"))
	if err == nil || !strings.Contains(err.Error(), "SignatureDoesNotMatch") {
		t.Errorf("wrong secret: %v", err)
	}
}

func TestS3VirtualHostedURL(t *testing.T) {
	t.Parallel()
	tg := newS3(model.S3Dest{Region: "eu-west-1", Bucket: "my-bucket", Prefix: "p/"}, nil)
	if u := tg.url("p/a b.zip", nil); u.Host != "my-bucket.s3.eu-west-1.amazonaws.com" || u.Path != "/p/a b.zip" {
		t.Errorf("virtual-hosted URL = %v", u)
	}
	tg = newS3(model.S3Dest{Endpoint: "https://acc.r2.cloudflarestorage.com", Region: "auto", Bucket: "b", PathStyle: true}, nil)
	if u := tg.url("k", nil); u.String() != "https://acc.r2.cloudflarestorage.com/b/k" {
		t.Errorf("path-style URL = %v", u)
	}
	// Dotted bucket names break the TLS wildcard: always path style.
	tg = newS3(model.S3Dest{Region: "us-east-1", Bucket: "backups.example.com"}, nil)
	if u := tg.url("k", nil); u.Host != "s3.us-east-1.amazonaws.com" || u.Path != "/backups.example.com/k" {
		t.Errorf("dotted bucket URL = %v", u)
	}
}

func TestS3RefusesOversizedArchive(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "big.zip")
	f, _ := os.Create(p)
	if err := f.Truncate(MaxS3Object + 1); err != nil {
		t.Skip("sparse files unavailable:", err)
	}
	f.Close()
	tg := newS3(model.S3Dest{Endpoint: "http://127.0.0.1:1", Region: "x", Bucket: "b", AccessKeyID: "a", SecretAccessKey: "b"}, http.DefaultClient)
	if err := tg.Put(context.Background(), "a.zip", p); err == nil || !strings.Contains(err.Error(), "5 GiB") {
		t.Errorf("err = %v", err)
	}
}
