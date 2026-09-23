package backup

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/parthh37/nodehoster/internal/model"
)

// The Azurite emulator's well-known account.
const (
	azAccount = "devstoreaccount1"
	azKey     = "Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw=="
)

// Requests as Microsoft's Go SDK (azblob v1.8.1) signed them, captured on
// the wire, with the Authorization it computed.
func TestAzureSharedKeyVectors(t *testing.T) {
	t.Parallel()
	key, _ := base64.StdEncoding.DecodeString(azKey)
	for _, tc := range []struct {
		name, method, url string
		length            int64
		headers           map[string]string
		want              string
	}{
		{
			name: "Put Blob", method: "PUT",
			url:    "http://127.0.0.1:52311/devstoreaccount1/backups/nb%2Fnodehoster-backup-web01-20260301-023005.zip",
			length: 5,
			headers: map[string]string{
				"Content-Type": "application/octet-stream", "X-Ms-Blob-Type": "BlockBlob",
				"X-Ms-Date": "Wed, 23 Sep 2026 23:14:30 GMT", "X-Ms-Version": "2026-12-06",
				"Accept-Encoding": "gzip", "User-Agent": "azsdk-go-azblob/v1.8.1 (go1.26.0; darwin)",
			},
			want: "V/ggnBiZwFwjpMRSqqOwd4qWujv08qrKNPECn79Qnh0=",
		},
		{
			name: "List Blobs", method: "GET",
			url: "http://127.0.0.1:52311/devstoreaccount1/backups?comp=list&prefix=nb%2F&restype=container",
			headers: map[string]string{
				"Accept": "application/xml", "X-Ms-Date": "Wed, 23 Sep 2026 23:14:30 GMT", "X-Ms-Version": "2026-12-06",
			},
			want: "ngG7nOjEXkvsUcRwAAsZk+2BoYrHmmmNTjlISIC5Qrs=",
		},
	} {
		req := httptest.NewRequest(tc.method, tc.url, nil)
		req.ContentLength = tc.length
		for k, v := range tc.headers {
			req.Header.Set(k, v)
		}
		if got := azureSign(req, azAccount, key); got != tc.want {
			t.Errorf("%s: signature = %s, want %s", tc.name, got, tc.want)
		}
	}
}

// fakeAzure is an in-memory container that checks Shared Key signatures
// (or a SAS token) and implements the calls the target makes.
type fakeAzure struct {
	sas    string // "" = Shared Key
	mu     sync.Mutex
	blobs  map[string][]byte
	blocks map[string][]byte
	calls  []string
}

func (f *fakeAzure) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if f.sas != "" {
		if q.Get("sig") != f.sas {
			w.WriteHeader(403)
			io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?><Error><Code>AuthenticationFailed</Code><Message>Signature did not match.
RequestId:1</Message></Error>`)
			return
		}
	} else {
		key, _ := base64.StdEncoding.DecodeString(azKey)
		if r.Header.Get("Authorization") != "SharedKey "+azAccount+":"+azureSign(r, azAccount, key) {
			w.WriteHeader(403)
			io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?><Error><Code>AuthenticationFailed</Code><Message>Server failed to authenticate the request.</Message></Error>`)
			return
		}
	}
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	// /devstoreaccount1/<container>/<blob>
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 3)
	blob := ""
	if len(parts) == 3 {
		blob = parts[2]
	}
	f.calls = append(f.calls, r.Method+" "+q.Get("comp"))
	switch {
	case r.Method == "PUT" && q.Get("comp") == "block":
		f.blocks[q.Get("blockid")] = body
		w.WriteHeader(201)
	case r.Method == "PUT" && q.Get("comp") == "blocklist":
		var l struct {
			Latest []string `xml:"Latest"`
		}
		xml.Unmarshal(body, &l)
		var all []byte
		for _, id := range l.Latest {
			all = append(all, f.blocks[id]...)
		}
		f.blobs[blob] = all
		w.WriteHeader(201)
	case r.Method == "PUT":
		if r.Header.Get("X-Ms-Blob-Type") != "BlockBlob" {
			w.WriteHeader(400)
			return
		}
		f.blobs[blob] = body
		w.WriteHeader(201)
	case r.Method == "GET" && q.Get("comp") == "list":
		var names []string
		for k := range f.blobs {
			if strings.HasPrefix(k, q.Get("prefix")) {
				names = append(names, k)
			}
		}
		sort.Strings(names)
		var b bytes.Buffer
		b.WriteString(`<?xml version="1.0" encoding="utf-8"?><EnumerationResults><Blobs>`)
		for _, n := range names {
			b.WriteString("<Blob><Name>")
			xml.EscapeText(&b, []byte(n))
			b.WriteString("</Name><Properties><Last-Modified>Sun, 01 Mar 2026 02:30:05 GMT</Last-Modified><Content-Length>")
			b.WriteString(itoa(len(f.blobs[n])) + "</Content-Length></Properties></Blob>")
		}
		b.WriteString(`</Blobs><NextMarker /></EnumerationResults>`)
		w.Write(b.Bytes())
	case r.Method == "GET":
		data, ok := f.blobs[blob]
		if !ok {
			w.WriteHeader(404)
			io.WriteString(w, `<Error><Code>BlobNotFound</Code><Message>The specified blob does not exist.</Message></Error>`)
			return
		}
		w.Write(data)
	case r.Method == "DELETE":
		delete(f.blobs, blob)
		w.WriteHeader(202)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for ; n > 0; n /= 10 {
		d = append([]byte{byte('0' + n%10)}, d...)
	}
	return string(d)
}

func TestAzureTargetSharedKey(t *testing.T) {
	t.Parallel()
	fake := &fakeAzure{blobs: map[string][]byte{}, blocks: map[string][]byte{}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	cfg := model.AzureDest{Account: azAccount, Container: "backups", Prefix: "nightly/", AccountKey: azKey, Endpoint: srv.URL + "/" + azAccount}
	exercise(t, newAzure(cfg, srv.Client()))

	cfg.AccountKey = base64.StdEncoding.EncodeToString([]byte("not the key"))
	err := newAzure(cfg, srv.Client()).Put(context.Background(), "x.zip", tempFile(t, "x"))
	if err == nil || !strings.Contains(err.Error(), "AuthenticationFailed") {
		t.Errorf("wrong key: %v", err)
	}
}

func TestAzureTargetSASAndBlocks(t *testing.T) {
	// Not parallel: it changes the block size.
	oldSingle, oldBlock := azureSingleMax, azureBlockSize
	azureSingleMax, azureBlockSize = 10, 4
	defer func() { azureSingleMax, azureBlockSize = oldSingle, oldBlock }()

	fake := &fakeAzure{sas: "s1gn4ture", blobs: map[string][]byte{}, blocks: map[string][]byte{}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	cfg := model.AzureDest{Account: azAccount, Container: "backups", SASToken: "sv=2022-11-02&ss=b&srt=co&sp=rwdlc&sig=s1gn4ture", Endpoint: srv.URL + "/" + azAccount}
	exercise(t, newAzure(cfg, srv.Client()))

	// Above the single-upload limit, blocks and a block list.
	tg := newAzure(cfg, srv.Client())
	body := strings.Repeat("0123456789", 3)
	if err := tg.Put(context.Background(), "big.zip", tempFile(t, body)); err != nil {
		t.Fatal(err)
	}
	if string(fake.blobs["big.zip"]) != body {
		t.Errorf("block upload assembled %d bytes, want %d", len(fake.blobs["big.zip"]), len(body))
	}
	blocks := 0
	for _, c := range fake.calls {
		if c == "PUT block" {
			blocks++
		}
	}
	if blocks < 2 {
		t.Errorf("block uploads = %d", blocks)
	}

	cfg.SASToken = "sig=expired"
	if _, err := newAzure(cfg, srv.Client()).List(context.Background()); err == nil || !strings.Contains(err.Error(), "Signature did not match") || strings.Contains(err.Error(), "RequestId") {
		t.Errorf("bad SAS: %v", err)
	}
}
