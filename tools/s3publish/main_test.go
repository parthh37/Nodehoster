package main

import (
	"context"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func fakeS3(t *testing.T) *minio.Client {
	t.Helper()
	srv := httptest.NewServer(gofakes3.New(s3mem.New()).Server())
	t.Cleanup(srv.Close)
	c, err := minio.New(strings.TrimPrefix(srv.URL, "http://"), &minio.Options{
		Creds: credentials.NewStaticV4("id", "secret", ""), BucketLookup: minio.BucketLookupPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.MakeBucket(context.Background(), "rel-bucket", minio.MakeBucketOptions{}); err != nil {
		t.Fatal(err)
	}
	return c
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "NodeHoster-1.0.0-windows-x64.zip")
	os.WriteFile(p, []byte(content), 0o644)
	return p
}

func TestUploadStoresContentAndChecksum(t *testing.T) {
	ctx, c := context.Background(), fakeS3(t)
	e, err := upload(ctx, c, "rel-bucket", "rel/1.0.0/", writeFile(t, "hello"), false)
	if err != nil {
		t.Fatal(err)
	}
	if e.Key != "rel/1.0.0/NodeHoster-1.0.0-windows-x64.zip" || e.Size != 5 ||
		e.SHA256 != "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" {
		t.Fatalf("unexpected entry %+v", e)
	}
	obj, err := c.GetObject(ctx, "rel-bucket", e.Key, minio.GetObjectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(obj)
	if string(body) != "hello" {
		t.Fatalf("stored %q", body)
	}
	st, _ := c.StatObject(ctx, "rel-bucket", e.Key, minio.StatObjectOptions{})
	if st.UserMetadata["Sha256"] != e.SHA256 {
		t.Fatalf("sha256 metadata = %q", st.UserMetadata["Sha256"])
	}
}

func TestImmutableUpload(t *testing.T) {
	ctx, c := context.Background(), fakeS3(t)
	if _, err := upload(ctx, c, "rel-bucket", "rel/", writeFile(t, "v1"), true); err != nil {
		t.Fatal(err)
	}
	// Re-running a release job with identical bytes succeeds.
	if _, err := upload(ctx, c, "rel-bucket", "rel/", writeFile(t, "v1"), true); err != nil {
		t.Fatalf("identical re-upload: %v", err)
	}
	// Different bytes under a published name are refused.
	if _, err := upload(ctx, c, "rel-bucket", "rel/", writeFile(t, "v2"), true); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("expected immutability error, got %v", err)
	}
	// Without -immutable (dev builds) overwriting is allowed.
	if _, err := upload(ctx, c, "rel-bucket", "rel/", writeFile(t, "v2"), false); err != nil {
		t.Fatal(err)
	}
}
