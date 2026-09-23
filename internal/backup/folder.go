package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// folderTarget is a local folder or a network share (UNC path).
type folderTarget struct{ dir string }

func (t *folderTarget) Put(ctx context.Context, name, path string) error {
	if err := checkObjectName(name); err != nil {
		return err
	}
	if err := os.MkdirAll(t.dir, 0o750); err != nil {
		return t.explain(err)
	}
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close()
	// Written under a temporary name and renamed, so a half-copied
	// archive never looks like a backup (and is never picked by
	// retention or a restore).
	final := filepath.Join(t.dir, name)
	tmp := final + ".partial"
	dst, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return t.explain(err)
	}
	_, err = io.Copy(dst, readerCtx{ctx, src})
	if err == nil {
		err = dst.Sync()
	}
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, final)
	}
	if err != nil {
		os.Remove(tmp)
		return t.explain(err)
	}
	return nil
}

func (t *folderTarget) List(ctx context.Context) ([]Object, error) {
	entries, err := os.ReadDir(t.dir)
	if errors.Is(err, fs.ErrNotExist) && !isUNC(t.dir) {
		return []Object{}, nil // created by the first backup
	}
	if err != nil {
		return nil, t.explain(err)
	}
	out := []Object{}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Object{Name: e.Name(), Size: info.Size(), Modified: info.ModTime()})
	}
	return out, nil
}

func (t *folderTarget) Open(ctx context.Context, name string) (io.ReadCloser, error) {
	if err := checkObjectName(name); err != nil {
		return nil, err
	}
	f, err := os.Open(filepath.Join(t.dir, name))
	if err != nil {
		return nil, t.explain(err)
	}
	return f, nil
}

func (t *folderTarget) Delete(ctx context.Context, name string) error {
	if err := checkObjectName(name); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(t.dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return t.explain(err)
	}
	return nil
}

func (t *folderTarget) Close() error { return nil }

func isUNC(p string) bool { return strings.HasPrefix(p, `\\`) }

// explain turns the errors an administrator hits with shares into advice.
// The service runs as LocalSystem, which reaches other computers as the
// computer account, not as the administrator who set this up.
func (t *folderTarget) explain(err error) error {
	switch {
	case errors.Is(err, fs.ErrPermission):
		if isUNC(t.dir) || runtime.GOOS == "windows" {
			return fmt.Errorf("access to %s is denied. The NodeHoster service runs as LocalSystem, which reaches network shares as this computer's account (DOMAIN\\%s$): grant that account Modify permission on the share and on the folder: %w", t.dir, computerName(), err)
		}
		return fmt.Errorf("access to %s is denied; the NodeHoster service account needs write permission: %w", t.dir, err)
	case errors.Is(err, fs.ErrNotExist) && isUNC(t.dir):
		return fmt.Errorf("%s cannot be reached: check the server and share names, and that the share is reachable from this computer: %w", t.dir, err)
	}
	return err
}

func computerName() string {
	if n := os.Getenv("COMPUTERNAME"); n != "" {
		return n
	}
	h, _ := os.Hostname()
	return strings.ToUpper(strings.Split(h, ".")[0])
}

// readerCtx stops a copy when the context ends.
type readerCtx struct {
	ctx context.Context
	r   io.Reader
}

func (r readerCtx) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
