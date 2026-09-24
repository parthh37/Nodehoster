package localapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/parthh37/nodehoster/internal/remote"
)

// TestRemoteClient: the same client, over HTTPS with a pinned certificate
// and a token, for NodeHoster Manager's remote servers and --server.
func TestRemoteClient(t *testing.T) {
	t.Parallel()
	var uploaded []byte
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer nh_ok" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"not signed in"}`))
			return
		}
		switch r.URL.Path {
		case "/prefix/api/sites":
			w.Write([]byte(`[{"id":"a","name":"shop","status":{"state":"running"}}]`))
		case "/prefix/api/sites/a/deploy/zip":
			f, _, err := r.FormFile("file")
			if err == nil {
				uploaded, _ = io.ReadAll(f)
			}
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{}`))
		case "/prefix/api/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			for i := range 3 {
				fmt.Fprintf(w, "event: status\ndata: %d\n\n", i)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"no such endpoint"}`))
		}
	}))
	defer srv.Close()
	fp := remote.Fingerprint(srv.Certificate().Raw)
	ctx := context.Background()

	cl, err := ConnectRemote(srv.URL+"/prefix", "nh_ok", fp)
	if err != nil {
		t.Fatal(err)
	}
	if !cl.Remote() || cl.BaseURL() != srv.URL+"/prefix" {
		t.Fatalf("remote %v, base %q", cl.Remote(), cl.BaseURL())
	}
	sites, err := cl.Sites(ctx)
	if err != nil || len(sites) != 1 || sites[0].Name != "shop" {
		t.Fatalf("sites %+v, %v", sites, err)
	}
	if err := cl.UploadFile(ctx, "/api/sites/a/deploy/zip", "file", "app.zip", bytes.NewReader([]byte("PKzip")), nil); err != nil || string(uploaded) != "PKzip" {
		t.Fatalf("upload %q, %v", uploaded, err)
	}
	var events []string
	cl.Stream(ctx, "/api/stream", func(ev string, data []byte) { events = append(events, ev+"="+string(data)) })
	if strings.Join(events, ",") != "status=0,status=1,status=2" {
		t.Fatalf("events %v", events)
	}
	var apiErr *Error
	if err := cl.Get(ctx, "/api/newer", nil); !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Fatalf("404: %v", err)
	}

	// A refused token, an unpinned self-signed certificate, no token.
	bad, _ := ConnectRemote(srv.URL+"/prefix", "nh_revoked", fp)
	if _, err := bad.Sites(ctx); !errors.As(err, &apiErr) || !strings.Contains(apiErr.Message, "refused the API token") {
		t.Fatalf("revoked: %v", err)
	}
	unpinned, _ := ConnectRemote(srv.URL+"/prefix", "nh_ok", "")
	if _, err := unpinned.Sites(ctx); err == nil || !strings.Contains(err.Error(), "cannot reach") || !strings.Contains(err.Error(), "fingerprint") {
		t.Fatalf("unpinned: %v", err)
	}
	if _, err := ConnectRemote(srv.URL, "", fp); err == nil {
		t.Fatal("no token accepted")
	}
	if _, err := ConnectRemote("http://web02:8484", "t", ""); err == nil {
		t.Fatal("plain http to another computer accepted")
	}
}
