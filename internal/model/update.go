package model

import (
	"slices"
	"time"
)

// UpdateSettings control how the server updates itself from the release
// feed, the way Windows Update's "automatic updates" setting does. The
// service checks for new releases whether or not Auto is on (and says so
// in the consoles and the event log); Auto installs them unattended at
// Time on Weekdays. Installing restarts the service: sites go offline for
// the few seconds it takes.
type UpdateSettings struct {
	Auto bool `json:"auto"`
	// Time is when automatic installs happen, "HH:MM" in the server's
	// local time; Weekdays restricts them to some days (0 = Sunday … 6 =
	// Saturday), empty meaning every day.
	Time     string `json:"time"`
	Weekdays []int  `json:"weekdays"`
}

// DefaultUpdates is off (setup asks), at 03:00 on any day.
func DefaultUpdates() UpdateSettings {
	return UpdateSettings{Time: "03:00", Weekdays: []int{}}
}

// ApplyDefaults fills what settings saved before automatic updates
// existed lack.
func (u *UpdateSettings) ApplyDefaults() {
	if u.Time == "" {
		u.Time = "03:00"
	}
	if u.Weekdays == nil {
		u.Weekdays = []int{}
	}
}

func (u *UpdateSettings) Validate() error {
	if !backupTimeRE.MatchString(u.Time) {
		return verr("updates.time", "use HH:MM, 24-hour, e.g. 03:00")
	}
	seen := map[int]bool{}
	for _, d := range u.Weekdays {
		if d < 0 || d > 6 || seen[d] {
			return verr("updates.weekdays", "weekdays are 0 (Sunday) to 6 (Saturday), each once")
		}
		seen[d] = true
	}
	slices.Sort(u.Weekdays)
	return nil
}

// Update states.
const (
	UpdateIdle        = "idle"
	UpdateChecking    = "checking"
	UpdateDownloading = "downloading"
	UpdateInstalling  = "installing" // setup is running; the service is about to stop
)

// UpdateRelease is a release offered by the feed.
type UpdateRelease struct {
	Version   string    `json:"version"`
	Published time.Time `json:"published"`
	Size      int64     `json:"size"`  // of the setup
	Notes     string    `json:"notes"` // release page, for people
	// Manual: the update policy (update.AutoInstall) leaves this release
	// to an administrator, even with automatic updates on.
	Manual bool `json:"manual"`
	// Failed: installing it failed before; it is not retried unattended.
	Failed bool `json:"failed"`
}

// UpdateStatus is what the consoles show.
type UpdateStatus struct {
	UpdateSettings
	Current   string         `json:"current"`
	State     string         `json:"state"`
	Supported bool           `json:"supported"`
	Reason    string         `json:"reason,omitempty"` // why not supported
	Available *UpdateRelease `json:"available,omitempty"`
	LastCheck *time.Time     `json:"lastCheck,omitempty"`
	LastError string         `json:"lastError,omitempty"`
	NextCheck *time.Time     `json:"nextCheck,omitempty"`
	// NextInstall is when Available will be installed automatically, if
	// it will be.
	NextInstall *time.Time `json:"nextInstall,omitempty"`
	// LastResult is the outcome of the previous installation.
	LastResult *UpdateResult `json:"lastResult,omitempty"`
}

// UpdateResult is how an installation ended, recorded by the updater and
// read by the service that starts afterwards.
type UpdateResult struct {
	From       string    `json:"from"`
	To         string    `json:"to"`
	Trigger    string    `json:"trigger"` // schedule | manual
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt,omitzero"`
	OK         bool      `json:"ok"`
	ExitCode   int       `json:"exitCode"`
	Error      string    `json:"error,omitempty"`
	Log        string    `json:"log,omitempty"` // setup's log file
}
