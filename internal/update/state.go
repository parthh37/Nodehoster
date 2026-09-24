package update

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/parthh37/nodehoster/internal/config"
	"github.com/parthh37/nodehoster/internal/model"
)

// An installation spans three processes: the service that starts it, the
// updater (a copy of nodehoster.exe that runs setup) and the service that
// setup, or the updater after a failure, starts afterwards. They share
// files in the updates folder (data\updates, administrators only):
//
//	pending.json  written by the service before it starts the updater
//	result.json   written by the updater once setup has exited
//	last.json     the outcome, kept by the service that reports it
//
// The service that starts next reports the outcome: it runs the new
// version (success, even if the updater has not written result.json yet),
// or it does not, and result.json says why.
const (
	pendingFile = "pending.json"
	resultFile  = "result.json"
	lastFile    = "last.json"
	// HelperName is the updater's copy of nodehoster.exe: setup cannot
	// replace a program that is running.
	HelperName = "nodehoster-updater.exe"
)

// Dir is the updates folder under the data directory.
func Dir(data string) string { return filepath.Join(data, "updates") }

func ReadPending(dir string) (*model.UpdateResult, error) { return readResult(dir, pendingFile) }
func ReadResult(dir string) (*model.UpdateResult, error)  { return readResult(dir, resultFile) }
func ReadLast(dir string) (*model.UpdateResult, error)    { return readResult(dir, lastFile) }

func WritePending(dir string, r *model.UpdateResult) error { return writeResult(dir, pendingFile, r) }
func WriteResult(dir string, r *model.UpdateResult) error  { return writeResult(dir, resultFile, r) }
func WriteLast(dir string, r *model.UpdateResult) error    { return writeResult(dir, lastFile, r) }

// ClearInstall removes pending.json and result.json: the installation has
// been reported, or a new one is starting.
func ClearInstall(dir string) {
	os.Remove(filepath.Join(dir, pendingFile))
	os.Remove(filepath.Join(dir, resultFile))
}

func readResult(dir, name string) (*model.UpdateResult, error) {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r model.UpdateResult
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func writeResult(dir, name string, r *model.UpdateResult) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(filepath.Join(dir, name), b, 0o600)
}

// Outcome decides how the installation in pending ended, as seen by a
// service running version current: result is the updater's record, if it
// has written one. It returns nil when there was no installation.
func Outcome(pending, result *model.UpdateResult, current string) *model.UpdateResult {
	if pending == nil {
		return nil
	}
	out := *pending
	if result != nil && result.To == pending.To && result.StartedAt.Equal(pending.StartedAt) {
		out = *result
	}
	// The version that is running is the ground truth: setup may have
	// installed it and started it before the updater wrote anything.
	if cur, ok := ParseVersion(current); ok {
		if to, ok := ParseVersion(pending.To); ok && cur.Compare(to) == 0 {
			out.OK, out.Error = true, ""
			return &out
		}
	}
	out.OK = false
	if out.Error == "" {
		out.Error = "the update did not complete (setup was interrupted, or the computer restarted while it ran)"
	}
	return &out
}
