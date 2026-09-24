package core

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/events"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/update"
)

// updateCore is a core running version current that can update itself
// from a signed test feed offering offered; launches receives the setups
// it hands to the updater.
func updateCore(t *testing.T, current, offered string) (c *Core, launches chan string) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	setup := []byte("MZ setup " + offered)
	sum := sha256.Sum256(setup)
	name := "NodeHoster-" + offered + "-setup.exe"
	manifest, _ := json.Marshal(update.Manifest{Version: offered, Files: []update.File{
		{Name: name, Size: int64(len(setup)), SHA256: hex.EncodeToString(sum[:])},
	}})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest.json":
			w.Write(manifest)
		case "/latest.json.sig":
			w.Write([]byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, manifest))))
		case "/" + offered + "/" + name:
			w.Write(setup)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	c = openTestCore(t)
	t.Cleanup(c.Shutdown)
	c.UpdateFeed = &update.Feed{URL: srv.URL + "/latest.json", Keys: []ed25519.PublicKey{pub}, Client: srv.Client()}
	launches = make(chan string, 1)
	c.updates.version = current
	c.updates.supported = func() (bool, string) { return true, "" }
	c.updates.launch = func(dir, setup string) error {
		launches <- setup
		return nil
	}
	return c, launches
}

func eventsOfType(t *testing.T, c *Core, typ string) []model.Event {
	t.Helper()
	all, err := c.Store.ListEvents(context.Background(), "", 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []model.Event
	for _, e := range all {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

func TestUpdateCheckAnnouncesOnce(t *testing.T) {
	c, _ := updateCore(t, "1.1.0", "1.2.0")
	for range 2 {
		if err := c.CheckUpdate(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	st := c.UpdateStatus()
	if st.Available == nil || st.Available.Version != "1.2.0" || st.LastCheck == nil || st.LastError != "" {
		t.Fatalf("status = %+v", st)
	}
	if st.NextInstall != nil {
		t.Fatal("scheduled an install with automatic updates off")
	}
	if n := len(eventsOfType(t, c, events.UpdateAvailable)); n != 1 {
		t.Fatalf("%d update.available events, want 1", n)
	}
}

func TestUpdateUpToDate(t *testing.T) {
	c, _ := updateCore(t, "1.2.0", "1.2.0")
	if err := c.CheckUpdate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := c.UpdateStatus(); st.Available != nil {
		t.Fatalf("offered the running version: %+v", st.Available)
	}
	if err := c.InstallUpdate(); !errors.Is(err, ErrNoUpdate) {
		t.Fatalf("InstallUpdate = %v, want ErrNoUpdate", err)
	}
}

func TestUpdateInstallHandsVerifiedSetupToUpdater(t *testing.T) {
	c, launches := updateCore(t, "1.1.0", "1.2.0")
	if err := c.InstallUpdate(); err != nil {
		t.Fatal(err)
	}
	var setup string
	select {
	case setup = <-launches:
	case <-time.After(10 * time.Second):
		t.Fatal("the updater was not started")
	}
	if filepath.Dir(setup) != c.updatesDir() {
		t.Fatalf("setup %s is outside the updates folder", setup)
	}
	pending, err := update.ReadPending(c.updatesDir())
	if err != nil || pending == nil || pending.From != "1.1.0" || pending.To != "1.2.0" || pending.Trigger != "manual" {
		t.Fatalf("pending = %+v, %v", pending, err)
	}
	if st := c.UpdateStatus(); st.State != model.UpdateInstalling {
		t.Fatalf("state = %s", st.State)
	}
	if err := c.InstallUpdate(); !errors.Is(err, ErrUpdateBusy) {
		t.Fatalf("second install = %v, want ErrUpdateBusy", err)
	}
}

func TestUpdateReportedAfterRestart(t *testing.T) {
	// The new version started: success.
	c, _ := updateCore(t, "1.2.0", "1.2.0")
	dir := c.updatesDir()
	start := time.Now().UTC()
	if err := update.WritePending(dir, &model.UpdateResult{From: "1.1.0", To: "1.2.0", StartedAt: start}); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, update.HelperName), []byte("MZ"), 0o600)
	c.reportUpdate()
	if n := len(eventsOfType(t, c, events.UpdateInstalled)); n != 1 {
		t.Fatalf("%d update.installed events", n)
	}
	if p, _ := update.ReadPending(dir); p != nil {
		t.Fatal("pending.json was not cleared")
	}
	if _, err := os.Stat(filepath.Join(dir, update.HelperName)); err == nil {
		t.Fatal("the updater's copy was left behind")
	}
	if st := c.UpdateStatus(); st.LastResult == nil || !st.LastResult.OK {
		t.Fatalf("last result = %+v", st.LastResult)
	}
}

func TestFailedUpdateIsNotRetriedUnattended(t *testing.T) {
	c, _ := updateCore(t, "1.1.0", "1.2.0")
	dir := c.updatesDir()
	start := time.Now().UTC()
	p := &model.UpdateResult{From: "1.1.0", To: "1.2.0", StartedAt: start}
	update.WritePending(dir, p)
	r := *p
	r.ExitCode, r.Error = 4, "setup failed with exit code 4; see its log"
	update.WriteResult(dir, &r)
	c.reportUpdate()
	if ev := eventsOfType(t, c, events.UpdateFailed); len(ev) != 1 {
		t.Fatalf("update.failed events = %+v", ev)
	}

	if err := c.SetUpdateSettings(context.Background(), model.UpdateSettings{Auto: true, Time: "03:00"}); err != nil {
		t.Fatal(err)
	}
	if err := c.CheckUpdate(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := c.UpdateStatus()
	if st.Available == nil || !st.Available.Failed {
		t.Fatalf("available = %+v", st.Available)
	}
	if st.NextInstall != nil || c.eligibleUpdate() != nil {
		t.Fatal("a failed version is scheduled again")
	}
}

func TestUpdateSettingsValidated(t *testing.T) {
	c, _ := updateCore(t, "1.1.0", "1.2.0")
	var ve *model.ValidationError
	if err := c.SetUpdateSettings(context.Background(), model.UpdateSettings{Auto: true, Time: "25:00"}); !errors.As(err, &ve) || ve.Field != "updates.time" {
		t.Fatalf("bad time: %v", err)
	}
	if err := c.SetUpdateSettings(context.Background(), model.UpdateSettings{Auto: true, Time: "04:30", Weekdays: []int{6, 0}}); err != nil {
		t.Fatal(err)
	}
	u := c.Settings().Updates
	if !u.Auto || u.Time != "04:30" || len(u.Weekdays) != 2 || u.Weekdays[0] != 0 {
		t.Fatalf("saved %+v", u)
	}
	if err := c.CheckUpdate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := c.UpdateStatus(); st.NextInstall == nil {
		t.Fatal("no install scheduled with automatic updates on")
	}
}

func TestDevelopmentBuildsDoNotUpdate(t *testing.T) {
	c, _ := updateCore(t, "dev", "1.2.0")
	if err := c.CheckUpdate(context.Background()); !errors.Is(err, ErrUpdateUnsupported) {
		t.Fatalf("CheckUpdate = %v", err)
	}
	if st := c.UpdateStatus(); st.Supported || st.Reason == "" {
		t.Fatalf("status = %+v", st)
	}
}
