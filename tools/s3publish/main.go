// Command s3publish uploads build outputs to an S3-compatible bucket, so
// release files live in our own object storage instead of GitHub.
//
//	S3_ACCESS_KEY_ID=... S3_SECRET_ACCESS_KEY=... \
//	s3publish -endpoint s3.example.com -bucket releases -prefix nodehoster/releases/1.2.3/ \
//	    -version 1.2.3 -commit abc123 -latest nodehoster/releases/latest.json -immutable dist/*.zip
//
// Credentials are read from the environment only, never from flags, so they
// do not appear in process listings or CI logs. Every file is uploaded with
// its SHA-256 as metadata and verified by size afterwards; a manifest.json
// describing the upload is written next to the files, and optionally copied
// to a fixed "latest" key that download scripts can poll.
//
// With -sign, each manifest gets a detached Ed25519 signature next to it
// (<key>.sig, base64), made with the key in UPDATE_SIGNING_KEY (the base64
// 32-byte seed). NodeHoster's automatic updates only trust a latest.json
// whose signature verifies with a public key built into them.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type fileEntry struct {
	Name   string `json:"name"`
	Key    string `json:"key"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type manifest struct {
	Version   string      `json:"version"`
	Commit    string      `json:"commit,omitempty"`
	Published time.Time   `json:"published"`
	Files     []fileEntry `json:"files"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "s3publish:", err)
		os.Exit(1)
	}
}

func run() error {
	endpoint := flag.String("endpoint", "", "S3 endpoint host[:port], without scheme")
	bucket := flag.String("bucket", "", "bucket name")
	prefix := flag.String("prefix", "", "key prefix for the files, e.g. nodehoster/releases/1.2.3/")
	region := flag.String("region", "us-east-1", "bucket region")
	insecure := flag.Bool("insecure", false, "use plain HTTP (local testing only)")
	version := flag.String("version", "", "version recorded in manifest.json")
	commit := flag.String("commit", "", "commit recorded in manifest.json")
	latest := flag.String("latest", "", "also write the manifest to this key")
	immutable := flag.Bool("immutable", false, "fail instead of overwriting files that already exist")
	sign := flag.Bool("sign", false, "sign the manifests with the Ed25519 key in UPDATE_SIGNING_KEY")
	flag.Parse()

	files := flag.Args()
	switch {
	case *endpoint == "" || *bucket == "" || *version == "":
		return errors.New("-endpoint, -bucket and -version are required")
	case len(files) == 0:
		return errors.New("no files to upload")
	case strings.Contains(*endpoint, "://"):
		return errors.New("-endpoint takes a host name, not a URL")
	}
	if *prefix != "" && !strings.HasSuffix(*prefix, "/") {
		*prefix += "/"
	}
	id, secret := os.Getenv("S3_ACCESS_KEY_ID"), os.Getenv("S3_SECRET_ACCESS_KEY")
	if id == "" || secret == "" {
		return errors.New("S3_ACCESS_KEY_ID and S3_SECRET_ACCESS_KEY must be set")
	}
	// Checked before anything is uploaded: an unsigned release would be
	// refused by every server that updates itself.
	var key ed25519.PrivateKey
	if *sign {
		var err error
		if key, err = signingKey(os.Getenv("UPDATE_SIGNING_KEY")); err != nil {
			return err
		}
	}

	client, err := minio.New(*endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(id, secret, ""),
		Secure: !*insecure,
		Region: *region,
		// Path-style works with every S3-compatible server (MinIO, Garage,
		// Ceph) regardless of wildcard DNS for bucket subdomains.
		BucketLookup: minio.BucketLookupPath,
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	if ok, err := client.BucketExists(ctx, *bucket); err != nil {
		return fmt.Errorf("bucket %s: %w", *bucket, err)
	} else if !ok {
		return fmt.Errorf("bucket %s does not exist", *bucket)
	}

	m := manifest{Version: *version, Commit: *commit, Published: time.Now().UTC()}
	for _, f := range files {
		e, err := upload(ctx, client, *bucket, *prefix, f, *immutable)
		if err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
		fmt.Printf("uploaded %s -> s3://%s/%s (%d bytes, sha256 %s)\n", f, *bucket, e.Key, e.Size, e.SHA256)
		m.Files = append(m.Files, e)
	}

	body, _ := json.MarshalIndent(m, "", "  ")
	keys := []string{*prefix + "manifest.json"}
	if *latest != "" {
		keys = append(keys, *latest)
	}
	for _, k := range keys {
		if err := writeManifest(ctx, client, *bucket, k, body, key); err != nil {
			return err
		}
	}
	return nil
}

// signingKey reads the base64 Ed25519 seed (or full private key).
func signingKey(b64 string) (ed25519.PrivateKey, error) {
	if b64 == "" {
		return nil, errors.New("-sign: UPDATE_SIGNING_KEY is not set")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	switch {
	case err != nil:
		return nil, errors.New("-sign: UPDATE_SIGNING_KEY is not base64")
	case len(raw) == ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(raw), nil
	case len(raw) == ed25519.PrivateKeySize:
		return ed25519.PrivateKey(raw), nil
	}
	return nil, fmt.Errorf("-sign: UPDATE_SIGNING_KEY is %d bytes, expected a 32-byte seed", len(raw))
}

// writeManifest writes the manifest to key and, with a signing key, its
// signature to key + ".sig". Neither is cached: clients poll them.
func writeManifest(ctx context.Context, c *minio.Client, bucket, key string, body []byte, priv ed25519.PrivateKey) error {
	put := func(k, contentType string, data []byte) error {
		_, err := c.PutObject(ctx, bucket, k, strings.NewReader(string(data)), int64(len(data)),
			minio.PutObjectOptions{ContentType: contentType, CacheControl: "no-cache"})
		if err != nil {
			return fmt.Errorf("write %s: %w", k, err)
		}
		fmt.Printf("wrote s3://%s/%s\n", bucket, k)
		return nil
	}
	if err := put(key, "application/json", body); err != nil {
		return err
	}
	if priv == nil {
		return nil
	}
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, body)) + "\n"
	return put(key+".sig", "text/plain", []byte(sig))
}

func upload(ctx context.Context, c *minio.Client, bucket, prefix, file string, immutable bool) (fileEntry, error) {
	name := filepath.Base(file)
	e := fileEntry{Name: name, Key: prefix + name}
	sum, size, err := hashFile(file)
	if err != nil {
		return e, err
	}
	e.SHA256, e.Size = sum, size

	if immutable {
		st, err := c.StatObject(ctx, bucket, e.Key, minio.StatObjectOptions{})
		if err == nil {
			// Re-running the same release job is fine; replacing a
			// published file with different bytes is not.
			if st.UserMetadata["Sha256"] == sum {
				fmt.Printf("%s is already published with the same content\n", e.Key)
				return e, nil
			}
			return e, fmt.Errorf("s3://%s/%s already exists with different content; releases are immutable", bucket, e.Key)
		}
		if minio.ToErrorResponse(err).Code != "NoSuchKey" {
			return e, fmt.Errorf("check existing object: %w", err)
		}
	}

	ct := mime.TypeByExtension(path.Ext(name))
	if ct == "" {
		ct = "application/octet-stream"
	}
	info, err := c.FPutObject(ctx, bucket, e.Key, file, minio.PutObjectOptions{
		ContentType:        ct,
		ContentDisposition: fmt.Sprintf("attachment; filename=%q", name),
		UserMetadata:       map[string]string{"sha256": sum},
	})
	if err != nil {
		return e, err
	}
	if info.Size != size {
		return e, fmt.Errorf("uploaded %d bytes, expected %d", info.Size, size)
	}
	st, err := c.StatObject(ctx, bucket, e.Key, minio.StatObjectOptions{})
	if err != nil {
		return e, fmt.Errorf("verify upload: %w", err)
	}
	if st.Size != size {
		return e, fmt.Errorf("stored object is %d bytes, expected %d", st.Size, size)
	}
	return e, nil
}

func hashFile(name string) (string, int64, error) {
	f, err := os.Open(name)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
