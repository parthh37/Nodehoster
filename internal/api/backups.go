package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/parthh37/nodehoster/internal/core"
	"github.com/parthh37/nodehoster/internal/model"
)

// Scheduled backups (Settings → Backups). The configuration download and
// restore are in misc.go.

// maxRestoreUpload bounds an uploaded backup; archives with shared folders
// can be large.
const maxRestoreUpload = 8 << 30

func (a *API) backupStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.c.BackupStatus(r.Context()))
}

func (a *API) backupRun(w http.ResponseWriter, r *http.Request) {
	if err := a.c.StartBackup(); err != nil {
		a.backupFail(w, err)
		return
	}
	a.audit(r, "backup.run", "server", "")
	writeJSON(w, http.StatusAccepted, a.c.BackupStatus(r.Context()))
}

func (a *API) backupTest(w http.ResponseWriter, r *http.Request) {
	var in model.BackupDestination
	if err := decode(r, &in); err != nil {
		a.fail(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	res, err := a.c.TestBackupDestination(ctx, in)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (a *API) backupFiles(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	list, err := a.c.BackupFiles(ctx, chi.URLParam(r, "dest"))
	if err != nil {
		a.backupFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (a *API) backupRestoreFrom(w http.ResponseWriter, r *http.Request) {
	var in struct {
		File       string `json:"file"`
		Passphrase string `json:"passphrase"`
	}
	if err := decode(r, &in); err != nil {
		a.fail(w, err)
		return
	}
	res, err := a.c.RestoreFromDestination(r.Context(), chi.URLParam(r, "dest"), in.File, in.Passphrase)
	if err != nil {
		a.backupFail(w, err)
		return
	}
	a.audit(r, "backup.restore", "server", in.File)
	writeJSON(w, http.StatusOK, res)
}

func (a *API) backupSharedSizes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.c.SharedSizes(r.Context()))
}

// backupFail is fail, with a running backup or restore as a conflict.
func (a *API) backupFail(w http.ResponseWriter, err error) {
	if errors.Is(err, core.ErrBackupBusy) {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	a.fail(w, err)
}

// receiveBackup stores an uploaded backup in a temporary file: the "file"
// part of a multipart form (with an optional "passphrase" part), or the
// raw request body (the Manager's way), passphrase in X-Backup-Passphrase.
func (a *API) receiveBackup(w http.ResponseWriter, r *http.Request) (path, passphrase string, err error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRestoreUpload)
	f, err := os.CreateTemp(a.c.Paths.Tmp, "upload-*")
	if err != nil {
		return "", "", err
	}
	defer func() {
		f.Close()
		if err != nil {
			os.Remove(f.Name())
		}
	}()
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if ct != "multipart/form-data" {
		if _, err = io.Copy(f, r.Body); err != nil {
			return "", "", fmt.Errorf("upload: %w", err)
		}
		return f.Name(), r.Header.Get("X-Backup-Passphrase"), nil
	}
	mr, err := r.MultipartReader()
	if err != nil {
		return "", "", err
	}
	got := false
	for {
		p, perr := mr.NextPart()
		if perr == io.EOF {
			break
		}
		if perr != nil {
			return "", "", fmt.Errorf("upload: %w", perr)
		}
		switch p.FormName() {
		case "file":
			if _, err = io.Copy(f, p); err != nil {
				return "", "", fmt.Errorf("upload: %w", err)
			}
			got = true
		case "passphrase":
			b, _ := io.ReadAll(io.LimitReader(p, 4096))
			passphrase = strings.TrimRight(string(b), "\r\n")
		}
		p.Close()
	}
	if !got {
		return "", "", fmt.Errorf("upload a backup file")
	}
	return f.Name(), passphrase, nil
}
