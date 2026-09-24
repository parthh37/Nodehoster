package model

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// DeploymentSlot is a second copy of a node or worker site that runs its
// own release on its own instances, like an Azure App Service deployment
// slot: deploy to it, test it on its own bindings (staging.example.com),
// then swap it into production. Production is the site itself and not a
// slot of this list.
//
// A slot runs with production's configuration except for its slot
// settings: its own variables (Env, which override production's and are
// never swapped), production's variables marked SlotSetting (which stay in
// production), its instance count and its bindings (Binding.Slot). Before
// a swap the slot's instances are restarted with production's settings
// and warmed up, so that production traffic moves onto processes that are
// already running with the configuration they will keep.
type DeploymentSlot struct {
	Name string `json:"name"` // "staging": lower-case letters, digits and '-'
	// Env are the slot's own variables: added to production's variables
	// that are not slot settings, replacing any of the same name.
	Env []EnvVar `json:"env,omitempty"`
	// Instances run in this slot; 0 = as many as production. While a
	// swap warms the slot up it runs production's count.
	Instances int          `json:"instances,omitempty"`
	AutoSwap  bool         `json:"autoSwap"` // swap into production after a successful deployment to this slot
	Warmup    WarmupConfig `json:"warmup"`
	// ActiveRelease is the deployment this slot runs, managed by
	// NodeHoster like Site.ActiveRelease. Empty = nothing deployed yet: the
	// slot is not started.
	ActiveRelease string `json:"activeRelease,omitempty"`
}

// WarmupConfig is how a slot's instances are warmed up before a swap,
// like Azure's applicationInitialization: every path is requested on every
// instance until it answers an accepted status or the timeout expires.
type WarmupConfig struct {
	Paths      []string `json:"paths"`      // default "/"
	Statuses   string   `json:"statuses"`   // accepted statuses, e.g. "200-399" (the default) or "200-299,401"
	TimeoutSec int      `json:"timeoutSec"` // for all instances and paths together; default 120
}

const (
	// ProductionSlot names the site itself where a slot name is expected.
	ProductionSlot = "production"
	// MaxSlots is how many slots a site may have besides production.
	MaxSlots = 4

	DefaultWarmupTimeoutSec = 120
	MaxWarmupTimeoutSec     = 1800
	DefaultWarmupStatuses   = "200-399"
)

var slotNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)

// slotKeySep joins a site ID and a slot name. Site IDs are UUIDs and slot
// names cannot contain it.
const slotKeySep = "@"

// SlotKey identifies a slot's processes and traffic: the site ID for
// production, "<site ID>@<slot>" for another slot.
func SlotKey(siteID, slot string) string {
	if slot == "" || slot == ProductionSlot {
		return siteID
	}
	return siteID + slotKeySep + slot
}

// SplitSlotKey is the inverse of SlotKey; slot is "" for production.
func SplitSlotKey(key string) (siteID, slot string) {
	siteID, slot, _ = strings.Cut(key, slotKeySep)
	return siteID, slot
}

// NormalizeSlot maps "production" to "" (the site itself).
func NormalizeSlot(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == ProductionSlot {
		return ""
	}
	return name
}

// FindSlot returns the named slot, nil if the site has none by that name.
func (s *Site) FindSlot(name string) *DeploymentSlot {
	for i := range s.Slots {
		if s.Slots[i].Name == name {
			return &s.Slots[i]
		}
	}
	return nil
}

// ReleaseIn is the release a slot runs ("" = production's).
func (s *Site) ReleaseIn(slot string) string {
	if slot == "" {
		return s.ActiveRelease
	}
	if sl := s.FindSlot(slot); sl != nil {
		return sl.ActiveRelease
	}
	return ""
}

// SlotReleases are the releases the site's slots run, which deployments
// must not prune.
func (s *Site) SlotReleases() []string {
	var out []string
	for _, sl := range s.Slots {
		if sl.ActiveRelease != "" {
			out = append(out, sl.ActiveRelease)
		}
	}
	return out
}

// SlotBindings are the bindings that route to a slot ("" = production).
func (s *Site) SlotBindings(slot string) []Binding {
	out := []Binding{}
	for _, b := range s.Bindings {
		if b.Slot == slot {
			out = append(out, b)
		}
	}
	return out
}

// HasSlotBindings reports whether any binding routes to a slot rather than
// to production.
func (s *Site) HasSlotBindings() bool {
	for _, b := range s.Bindings {
		if b.Slot != "" {
			return true
		}
	}
	return false
}

// SlotSite derives the configuration a slot's instances run with and its
// deployments are made with: production's, with the slot's release,
// variables and instance count. Bindings are left as they are (all of the
// site's): the process manager only asks whether the site serves HTTP,
// which must not differ between slots. It returns nil when the site has
// no such slot. The result shares no mutable state with s.
func SlotSite(s *Site, slot string) *Site {
	sl := s.FindSlot(slot)
	if sl == nil || s.Node == nil {
		return nil
	}
	out := *s
	n := *s.Node
	out.Node = &n
	out.Slot = slot
	out.ActiveRelease = sl.ActiveRelease
	out.Node.Env = SlotEnv(s.Node.Env, sl.Env)
	if sl.Instances > 0 {
		out.Node.Instances = sl.Instances
	}
	out.Slots, out.Tasks = nil, nil // tasks run in production only
	return &out
}

// SwapSite is the configuration a slot's instances are restarted with
// before a swap: exactly production's, running the slot's release. After
// the swap production is given that same configuration, so the warm
// instances carry on without a restart.
func SwapSite(s *Site, slot string) *Site {
	out := *s
	out.ActiveRelease = s.ReleaseIn(slot)
	return &out
}

// SlotEnv merges a slot's variables into production's: production's
// variables that are slot settings are left out, the slot's replace those
// of the same name and the rest are appended in order.
func SlotEnv(prod, slot []EnvVar) []EnvVar {
	out := make([]EnvVar, 0, len(prod)+len(slot))
	for _, e := range prod {
		if !e.SlotSetting {
			out = append(out, e)
		}
	}
	for _, e := range slot {
		e.SlotSetting = false
		if i := slices.IndexFunc(out, func(o EnvVar) bool { return o.Name == e.Name }); i >= 0 {
			out[i] = e
		} else {
			out = append(out, e)
		}
	}
	return out
}

// StatusRange is an inclusive range of HTTP statuses.
type StatusRange struct{ From, To int }

// ParseStatusRanges reads "200-399" or "200-299,401,404".
func ParseStatusRanges(s string) ([]StatusRange, error) {
	var out []StatusRange
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		lo, hi, isRange := strings.Cut(part, "-")
		from, err1 := strconv.Atoi(strings.TrimSpace(lo))
		to := from
		var err2 error
		if isRange {
			to, err2 = strconv.Atoi(strings.TrimSpace(hi))
		}
		if err1 != nil || err2 != nil || from < 100 || to > 599 || from > to {
			return nil, fmt.Errorf("%q is not an HTTP status or a range of them (100-599)", part)
		}
		out = append(out, StatusRange{from, to})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("list at least one status, e.g. 200-399")
	}
	return out, nil
}

// StatusAccepted reports whether code falls in one of the ranges.
func StatusAccepted(code int, ranges []StatusRange) bool {
	for _, r := range ranges {
		if code >= r.From && code <= r.To {
			return true
		}
	}
	return false
}

func (w *WarmupConfig) applyDefaults() {
	if len(w.Paths) == 0 {
		w.Paths = []string{"/"}
	}
	for i := range w.Paths {
		w.Paths[i] = strings.TrimSpace(w.Paths[i])
	}
	if strings.TrimSpace(w.Statuses) == "" {
		w.Statuses = DefaultWarmupStatuses
	}
	if w.TimeoutSec <= 0 {
		w.TimeoutSec = DefaultWarmupTimeoutSec
	}
}

// applySlotDefaults normalizes slot names and the slot of each binding.
func (s *Site) applySlotDefaults() {
	for i := range s.Slots {
		sl := &s.Slots[i]
		sl.Name = strings.ToLower(strings.TrimSpace(sl.Name))
		sl.Warmup.applyDefaults()
	}
	for i := range s.Bindings {
		s.Bindings[i].Slot = NormalizeSlot(s.Bindings[i].Slot)
	}
}

// validateSlots checks the slots and what they need of the site: a node
// or worker site with automatic ports (two slots cannot share one fixed
// port), bindings that name an existing slot, sane warm-up settings.
func (s *Site) validateSlots() error {
	for i, b := range s.Bindings {
		if b.Slot != "" && s.FindSlot(b.Slot) == nil {
			return verr(fmt.Sprintf("bindings[%d].slot", i), "there is no deployment slot %q", b.Slot)
		}
	}
	if len(s.Slots) == 0 {
		return nil
	}
	if !s.RunsNode() {
		return verr("slots", "deployment slots need a Node.js application or background worker site")
	}
	if len(s.Slots) > MaxSlots {
		return verr("slots", "at most %d deployment slots besides production", MaxSlots)
	}
	if s.Node.PortMode == "fixed" {
		return verr("node.portMode", "deployment slots need automatic ports: two slots cannot listen on one fixed port")
	}
	seen := map[string]bool{}
	for i, sl := range s.Slots {
		f := fmt.Sprintf("slots[%d]", i)
		if sl.Name == ProductionSlot || !slotNameRe.MatchString(sl.Name) {
			return verr(f+".name", "use 1-32 lower-case letters, digits or '-', other than \"production\"")
		}
		if seen[sl.Name] {
			return verr(f+".name", "another slot is called %q", sl.Name)
		}
		seen[sl.Name] = true
		if sl.Instances < 0 || sl.Instances > 64 {
			return verr(f+".instances", "must be between 0 (as many as production) and 64")
		}
		names := map[string]bool{}
		for j, e := range sl.Env {
			if !envNameRe.MatchString(e.Name) {
				return verr(fmt.Sprintf("%s.env[%d].name", f, j), "%q is not a valid variable name", e.Name)
			}
			if names[e.Name] {
				return verr(fmt.Sprintf("%s.env[%d].name", f, j), "%s is set twice", e.Name)
			}
			names[e.Name] = true
		}
		w := sl.Warmup
		if len(w.Paths) > 10 {
			return verr(f+".warmup.paths", "at most 10 warm-up paths")
		}
		for j, p := range w.Paths {
			if !strings.HasPrefix(p, "/") || strings.ContainsAny(p, " \t\r\n") {
				return verr(fmt.Sprintf("%s.warmup.paths[%d]", f, j), "must be a path starting with /")
			}
		}
		if _, err := ParseStatusRanges(w.Statuses); err != nil {
			return verr(f+".warmup.statuses", "%v", err)
		}
		if w.TimeoutSec < 5 || w.TimeoutSec > MaxWarmupTimeoutSec {
			return verr(f+".warmup.timeoutSec", "must be between 5 and %d seconds", MaxWarmupTimeoutSec)
		}
	}
	return nil
}

// ---- status, swaps

// SlotStatus is one slot (production included) as the API shows it.
type SlotStatus struct {
	Name     string     `json:"name"` // "production" or the slot's name
	Release  string     `json:"release,omitempty"`
	Status   SiteStatus `json:"status"`
	Bindings []Binding  `json:"bindings"`
	AutoSwap bool       `json:"autoSwap,omitempty"`
}

// SlotsView is GET /sites/{id}/slots: every slot, production first, and
// the swap in progress or the last one.
type SlotsView struct {
	Slots    []SlotStatus  `json:"slots"`
	Swap     *SwapProgress `json:"swap,omitempty"`
	LastSwap *SwapResult   `json:"lastSwap,omitempty"`
}

// Phases of a swap.
const (
	SwapPreparing = "preparing" // the slot's instances restart with production's settings
	SwapWarming   = "warming"   // warm-up requests
	SwapSwapping  = "swapping"  // production traffic moves
)

// SwapProgress is a swap in progress.
type SwapProgress struct {
	Slot      string    `json:"slot"`
	Phase     string    `json:"phase"`
	Message   string    `json:"message,omitempty"`
	User      string    `json:"user,omitempty"`
	Auto      bool      `json:"auto,omitempty"` // started by auto-swap after a deployment
	StartedAt time.Time `json:"startedAt"`
}

// SwapResult is how the last swap of a site ended. It is kept in memory:
// the event log and the audit log have the history.
type SwapResult struct {
	Slot       string    `json:"slot"`
	Succeeded  bool      `json:"succeeded"`
	Message    string    `json:"message"`
	User       string    `json:"user,omitempty"`
	Auto       bool      `json:"auto,omitempty"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
	// Releases after the swap (before it, when it failed).
	ProductionRelease string `json:"productionRelease,omitempty"`
	SlotRelease       string `json:"slotRelease,omitempty"`
}

// SwapPreview is what a swap would do, for the confirmation dialog.
type SwapPreview struct {
	Slot              string       `json:"slot"`
	ProductionRelease string       `json:"productionRelease"` // now in production; goes to the slot
	SlotRelease       string       `json:"slotRelease"`       // now in the slot; goes to production
	Warmup            WarmupConfig `json:"warmup"`
	// Changes lists what happens, in order, in plain words.
	Changes []string `json:"changes"`
	// Warnings are reasons to think twice; Blockers make the swap refuse.
	Warnings []string `json:"warnings"`
	Blockers []string `json:"blockers"`
}
