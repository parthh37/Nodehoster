package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/parthh37/nodehoster/internal/backup"
	"github.com/parthh37/nodehoster/internal/config"
	"github.com/parthh37/nodehoster/internal/events"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/update"
)

// Automatic updates: the service reads the signed release feed a few
// minutes after it starts and every few hours, and announces a newer
// release once. With automatic updates on, it installs it at the
// scheduled time; an administrator can install it at any time. Installing
// downloads the setup (verified against the signed manifest) and hands it
// to the updater, which stops this service.

const (
	updateFirstCheck = 2 * time.Minute
	updateCheckEvery = 6 * time.Hour
	updateRetryAfter = 30 * time.Minute // after a failed check
)

var (
	ErrUpdateBusy        = errors.New("an update is already being checked or installed")
	ErrNoUpdate          = errors.New("NodeHoster is up to date")
	ErrUpdateUnsupported = errors.New("this installation cannot update itself")
)

type updateState struct {
	mu        sync.Mutex
	state     string
	available *update.Release
	lastCheck time.Time
	lastError string
	nextCheck time.Time
	last      *model.UpdateResult // the previous installation
	announced string              // the version update.available was sent for

	// Seams for tests; nil means the real thing.
	version   string // config.Version
	supported func() (bool, string)
	launch    func(dir, setup string) error // update.Launch
}

func (u *updateState) current() string {
	if u.version != "" {
		return u.version
	}
	return config.Version
}

func (c *Core) updatesDir() string { return update.Dir(c.Paths.Data) }

// updateSupport says whether (and why not) this server updates itself.
func (c *Core) updateSupport() (bool, string) {
	if v := c.updates.current(); !validVersion(v) {
		return false, "development builds are not updated (version " + v + ")"
	}
	if c.updates.supported != nil {
		return c.updates.supported()
	}
	return update.Supported()
}

func validVersion(v string) bool {
	_, ok := update.ParseVersion(v)
	return ok
}

// reportUpdate tells how the previous installation ended, once, from the
// service that started after it; see update.Outcome.
func (c *Core) reportUpdate() {
	dir := c.updatesDir()
	pending, err := update.ReadPending(dir)
	if err != nil {
		c.Log.Warn("read the pending update", "err", err)
	}
	result, _ := update.ReadResult(dir)
	if out := update.Outcome(pending, result, c.updates.current()); out != nil {
		if out.OK {
			c.Bus.Info(events.UpdateInstalled, "", "NodeHoster was updated from %s to %s", out.From, out.To)
		} else {
			c.Bus.Error(events.UpdateFailed, "", "Updating NodeHoster from %s to %s failed: %s", out.From, out.To, out.Error)
		}
		if err := update.WriteLast(dir, out); err != nil {
			c.Log.Warn("record the update", "err", err)
		}
		update.ClearInstall(dir)
	}
	last, _ := update.ReadLast(dir)
	c.updates.mu.Lock()
	c.updates.last = last
	c.updates.state = model.UpdateIdle
	c.updates.mu.Unlock()

	// The updater and setup it leaves behind (either may still be
	// running: then they stay until next time).
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if n := e.Name(); strings.EqualFold(filepath.Ext(n), ".exe") || strings.HasPrefix(n, ".download-") {
			os.Remove(filepath.Join(dir, n))
		}
	}
}

func (c *Core) updateLoop(ctx context.Context) {
	if ok, _ := c.updateSupport(); !ok {
		return
	}
	c.updates.mu.Lock()
	c.updates.nextCheck = time.Now().Add(updateFirstCheck)
	c.updates.mu.Unlock()

	// Installs happen at the scheduled time, like backups.
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	ticks := make(chan time.Time)
	sched := backup.Scheduler{
		Now:  time.Now,
		Tick: ticks,
		Schedule: func() (bool, string, []int) {
			u := c.Settings().Updates
			return u.Auto, u.Time, u.Weekdays
		},
		Run: func(ctx context.Context) {
			if err := c.CheckUpdate(ctx); err != nil {
				return
			}
			if rel := c.eligibleUpdate(); rel != nil {
				if err := c.installUpdate(ctx, rel, "schedule"); err != nil {
					c.Log.Warn("automatic update", "err", err)
				}
			}
		},
	}
	go sched.Loop(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			c.updates.mu.Lock()
			due := !now.Before(c.updates.nextCheck)
			c.updates.mu.Unlock()
			if due {
				_ = c.CheckUpdate(ctx)
			}
			select {
			case ticks <- now:
			case <-ctx.Done():
				return
			}
		}
	}
}

// CheckUpdate reads the release feed now.
func (c *Core) CheckUpdate(ctx context.Context) error {
	if ok, why := c.updateSupport(); !ok {
		return fmt.Errorf("%w: %s", ErrUpdateUnsupported, why)
	}
	if !c.beginUpdate(model.UpdateChecking) {
		return ErrUpdateBusy
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	rel, err := c.UpdateFeed.Latest(ctx)
	cancel()
	now := time.Now()

	c.updates.mu.Lock()
	c.updates.state = model.UpdateIdle
	c.updates.lastCheck = now
	if err != nil {
		c.updates.lastError = err.Error()
		c.updates.nextCheck = now.Add(updateRetryAfter)
		c.updates.mu.Unlock()
		c.Log.Warn("check for updates", "err", err)
		return err
	}
	c.updates.lastError = ""
	c.updates.nextCheck = now.Add(updateCheckEvery)
	c.updates.available = nil
	cur, _ := update.ParseVersion(c.updates.current())
	var announce string
	if rel.Version.Compare(cur) > 0 {
		c.updates.available = rel
		if c.updates.announced != rel.Version.String() {
			c.updates.announced = rel.Version.String()
			announce = "Install it from Settings → Updates or NodeHoster Manager."
			if c.eligibleLocked(rel) {
				announce = "It will be installed automatically at " + c.Settings().Updates.Time + "."
			}
		}
	}
	c.updates.mu.Unlock()
	if announce != "" {
		c.Bus.Info(events.UpdateAvailable, "", "NodeHoster %s is available (this server runs %s). %s", rel.Version, c.updates.current(), announce)
	}
	return nil
}

// eligibleUpdate is the available release if it may install itself now.
func (c *Core) eligibleUpdate() *update.Release {
	c.updates.mu.Lock()
	defer c.updates.mu.Unlock()
	if rel := c.updates.available; rel != nil && c.eligibleLocked(rel) {
		return rel
	}
	return nil
}

func (c *Core) eligibleLocked(rel *update.Release) bool {
	cur, ok := update.ParseVersion(c.updates.current())
	return ok && cur.IsRelease() && rel.Version.IsRelease() && c.Settings().Updates.Auto &&
		!c.failedLocked(rel) && update.AutoInstall(cur, rel.Version)
}

// failedLocked: installing rel failed last time. It is not retried
// unattended; an administrator can.
func (c *Core) failedLocked(rel *update.Release) bool {
	l := c.updates.last
	return l != nil && !l.OK && l.To == rel.Version.String()
}

func (c *Core) beginUpdate(state string) bool {
	c.updates.mu.Lock()
	defer c.updates.mu.Unlock()
	if c.updates.state != model.UpdateIdle && c.updates.state != "" {
		return false
	}
	c.updates.state = state
	return true
}

func (c *Core) setUpdateState(state string) {
	c.updates.mu.Lock()
	c.updates.state = state
	c.updates.mu.Unlock()
}

// InstallUpdate installs the newest release now, in the background (the
// API's "Install now"): checking the feed first, whatever the policy.
func (c *Core) InstallUpdate() error {
	if ok, why := c.updateSupport(); !ok {
		return fmt.Errorf("%w: %s", ErrUpdateUnsupported, why)
	}
	ctx := c.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if err := c.CheckUpdate(ctx); err != nil {
		return err
	}
	c.updates.mu.Lock()
	rel, busy := c.updates.available, c.updates.state != model.UpdateIdle
	c.updates.mu.Unlock()
	switch {
	case busy:
		return ErrUpdateBusy
	case rel == nil:
		return ErrNoUpdate
	}
	go func() {
		if err := c.installUpdate(ctx, rel, "manual"); err != nil {
			c.Log.Error("install update", "err", err)
		}
	}()
	return nil
}

// installUpdate downloads rel and starts the updater. On success the
// service is stopped by setup shortly afterwards.
func (c *Core) installUpdate(ctx context.Context, rel *update.Release, trigger string) error {
	if !c.beginUpdate(model.UpdateDownloading) {
		return ErrUpdateBusy
	}
	to := rel.Version.String()
	fail := func(err error) error {
		c.updates.mu.Lock()
		c.updates.state = model.UpdateIdle
		c.updates.lastError = err.Error()
		c.updates.mu.Unlock()
		if ctx.Err() == nil { // not the service stopping
			c.Bus.Error(events.UpdateFailed, "", "Updating NodeHoster to %s failed: %v", to, err)
		}
		return err
	}
	dir := c.updatesDir()
	setup, err := c.UpdateFeed.Download(ctx, rel, dir)
	if err != nil {
		return fail(err)
	}
	pending := &model.UpdateResult{
		From: c.updates.current(), To: to, Trigger: trigger, StartedAt: time.Now().UTC(),
		Log: filepath.Join(c.Paths.Logs, "update-"+to+".log"),
	}
	update.ClearInstall(dir)
	if err := update.WritePending(dir, pending); err != nil {
		return fail(err)
	}
	c.setUpdateState(model.UpdateInstalling)
	c.Bus.Info(events.UpdateInstalling, "", "Installing NodeHoster %s (%s): the service restarts and sites are offline for a few seconds", to, trigger)
	launch := update.Launch
	if c.updates.launch != nil {
		launch = c.updates.launch
	}
	if err := launch(dir, setup); err != nil {
		update.ClearInstall(dir)
		return fail(err)
	}
	return nil
}

// UpdateStatus is the updater's state for the consoles.
func (c *Core) UpdateStatus() model.UpdateStatus {
	u := c.Settings().Updates
	ok, why := c.updateSupport()
	st := model.UpdateStatus{UpdateSettings: u, Current: c.updates.current(), Supported: ok, Reason: why}
	c.updates.mu.Lock()
	defer c.updates.mu.Unlock()
	st.State = c.updates.state
	if st.State == "" {
		st.State = model.UpdateIdle
	}
	st.LastError = c.updates.lastError
	st.LastResult = c.updates.last
	if !c.updates.lastCheck.IsZero() {
		t := c.updates.lastCheck
		st.LastCheck = &t
	}
	if ok && !c.updates.nextCheck.IsZero() {
		t := c.updates.nextCheck
		st.NextCheck = &t
	}
	if rel := c.updates.available; rel != nil {
		cur, _ := update.ParseVersion(c.updates.current())
		st.Available = &model.UpdateRelease{
			Version: rel.Version.String(), Published: rel.Published, Size: rel.Setup.Size,
			Notes:  update.ReleaseNotes(rel.Version.String()),
			Manual: !update.AutoInstall(cur, rel.Version), Failed: c.failedLocked(rel),
		}
		if c.eligibleLocked(rel) {
			if next, ok := backup.Next(u.Time, u.Weekdays, time.Now()); ok {
				st.NextInstall = &next
			}
		}
	}
	return st
}

// SetUpdateSettings changes the updates section alone (the Manager, the
// command line and setup), through the same validation as the rest.
func (c *Core) SetUpdateSettings(ctx context.Context, u model.UpdateSettings) error {
	s := c.MaskedSettings() // UpdateSettings takes masked secrets back
	s.Updates = u
	_, err := c.UpdateSettings(ctx, s)
	return err
}
