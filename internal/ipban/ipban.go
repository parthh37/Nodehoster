// Package ipban bans client addresses that misbehave, like fail2ban or IIS
// Dynamic IP Restrictions: failed sign-ins, runs of 404s, rate-limit
// rejections and requests for trap paths count against the client's
// address, and past a threshold within a window every site refuses it for
// a while, longer each time it comes back.
package ipban

import (
	"container/list"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

// Kind is what happened.
type Kind int

const (
	AuthFailure Kind = iota
	NotFound
	RateLimited
	kinds
)

func (k Kind) String() string {
	return [...]string{"authentication failures", "requests for missing pages", "rate limit rejections"}[k]
}

const (
	// maxTracked bounds the counters: beyond it the addresses seen least
	// recently are forgotten (a flood of one-off addresses cannot exhaust
	// memory; each costs about 200 bytes).
	maxTracked = 100_000
	// historyFor keeps an expired ban so that a repeat offender's next ban
	// is longer.
	historyFor = 7 * 24 * time.Hour
	maxHistory = 20_000
	// eventsPerMinute limits security.banned events (and webhook calls)
	// during an attack by many addresses; the rest are summarized.
	eventsPerMinute = 10
	saveDelay       = 2 * time.Second
)

// Options connect the manager to the rest of the server.
type Options struct {
	Log *slog.Logger
	// Save persists the bans (active ones, and recent ones for escalation).
	Save func([]model.Ban) error
	// OnBan reports a new ban, for the security.banned event.
	OnBan func(b model.Ban, message string)
	Now   func() time.Time
}

// Manager counts offences and holds the bans. Its methods are safe for
// concurrent use and cheap when banning is off.
type Manager struct {
	opts Options

	mu      sync.RWMutex
	cfg     model.IPBanSettings
	allow   []*net.IPNet
	traps   []string
	bans    map[string]*model.Ban // by address, active and recent
	ranges  []rangeBan            // bans not found by key (manual ranges, other prefixes)
	tracked map[string]*list.Element
	lru     *list.List // of *tracker, front = most recent

	saveMu  sync.Mutex
	pending bool

	evMu       sync.Mutex
	evWindow   time.Time
	evSent     int
	evDropped  int
	evDroppedA string
}

type rangeBan struct {
	net *net.IPNet
	ban *model.Ban
}

type tracker struct {
	key      string
	counters [kinds]counter
}

// counter estimates events in a sliding window from two fixed windows
// (the previous one weighted by how much of it still overlaps), so that
// each address costs a few words however many requests it makes.
type counter struct {
	start     time.Time
	prev, cur float64
}

func (c *counter) add(now time.Time, window time.Duration) float64 {
	if c.start.IsZero() {
		c.start = now
	}
	elapsed := now.Sub(c.start)
	switch {
	case elapsed >= 2*window:
		c.prev, c.cur, c.start, elapsed = 0, 0, now, 0
	case elapsed >= window:
		c.prev, c.cur = c.cur, 0
		c.start = c.start.Add(window)
		elapsed -= window
	}
	c.cur++
	return c.prev*(1-float64(elapsed)/float64(window)) + c.cur
}

// New creates a manager with banning off until Apply.
func New(opts Options) *Manager {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	m := &Manager{opts: opts, bans: map[string]*model.Ban{}, tracked: map[string]*list.Element{}, lru: list.New()}
	m.Apply(model.IPBanSettings{}, nil)
	return m
}

var loopback = []*net.IPNet{
	{IP: net.IPv4(127, 0, 0, 0).To4(), Mask: net.CIDRMask(8, 32)},
	{IP: net.IPv6loopback, Mask: net.CIDRMask(128, 128)},
}

// Apply takes new settings. trusted are the trusted proxies, which are
// never banned themselves.
func (m *Manager) Apply(s model.IPBanSettings, trusted []*net.IPNet) {
	allow := slices.Clone(loopback)
	allow = append(allow, trusted...)
	for _, c := range s.AllowList {
		if n, err := model.ParseCIDROrIP(c); err == nil {
			allow = append(allow, n)
		}
	}
	var traps []string
	for _, p := range s.TrapPaths {
		if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
			traps = append(traps, p)
		}
	}
	if s.IPv6Prefix <= 0 {
		s.IPv6Prefix = 64
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg, m.allow, m.traps = s, allow, traps
	m.reindexLocked()
}

// Enabled reports whether automatic banning is on (bans in force are
// enforced either way, so that turning it off does not lift manual ones).
func (m *Manager) Enabled() bool {
	if m == nil {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg.Enabled
}

// key is how an address is counted and banned: IPv4 addresses on their
// own, IPv6 addresses by their prefix.
func (m *Manager) keyLocked(ip net.IP) string {
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	if m.cfg.IPv6Prefix >= 128 {
		return ip.String()
	}
	n := &net.IPNet{IP: ip.Mask(net.CIDRMask(m.cfg.IPv6Prefix, 128)), Mask: net.CIDRMask(m.cfg.IPv6Prefix, 128)}
	return n.String()
}

func (m *Manager) allowedLocked(ip net.IP) bool {
	for _, n := range m.allow {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// Banned reports whether an address is banned now.
func (m *Manager) Banned(ip net.IP) bool {
	if m == nil || ip == nil {
		return false
	}
	now := m.opts.Now()
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.bans) == 0 || m.allowedLocked(ip) {
		return false
	}
	if b := m.bans[m.keyLocked(ip)]; b != nil && b.Active(now) {
		return true
	}
	for _, r := range m.ranges {
		if r.net.Contains(ip) && r.ban.Active(now) {
			return true
		}
	}
	return false
}

// Record counts an event against an address and bans it when a rule's
// threshold is reached.
func (m *Manager) Record(ip net.IP, kind Kind) {
	if m == nil || ip == nil {
		return
	}
	m.mu.Lock()
	rule := [...]model.BanRule{m.cfg.AuthFailures, m.cfg.NotFound, m.cfg.RateLimited}[kind]
	if !m.cfg.Enabled || rule.Threshold <= 0 || m.allowedLocked(ip) {
		m.mu.Unlock()
		return
	}
	now := m.opts.Now()
	key := m.keyLocked(ip)
	if b := m.bans[key]; b != nil && b.Active(now) {
		m.mu.Unlock()
		return // already banned (e.g. requests that were in flight)
	}
	var t *tracker
	if el, ok := m.tracked[key]; ok {
		m.lru.MoveToFront(el)
		t = el.Value.(*tracker)
	} else {
		t = &tracker{key: key}
		m.tracked[key] = m.lru.PushFront(t)
		for m.lru.Len() > maxTracked {
			old := m.lru.Remove(m.lru.Back()).(*tracker)
			delete(m.tracked, old.key)
		}
	}
	window := time.Duration(rule.WindowSec) * time.Second
	if t.counters[kind].add(now, window) < float64(rule.Threshold) {
		m.mu.Unlock()
		return
	}
	b, msg := m.banLocked(key, fmt.Sprintf("%d %s within %s", rule.Threshold, kind, formatDuration(window)))
	m.mu.Unlock()
	m.banned(b, msg)
}

// Trap bans an address that asked for a trap path, and reports whether it
// did.
func (m *Manager) Trap(ip net.IP, path string) bool {
	if m == nil || ip == nil {
		return false
	}
	// Every request asks, so the common case only reads.
	m.mu.RLock()
	trap := ""
	if m.cfg.Enabled {
		lp := strings.ToLower(path)
		for _, t := range m.traps {
			if strings.HasPrefix(lp, t) {
				trap = t
				break
			}
		}
	}
	m.mu.RUnlock()
	if trap == "" {
		return false
	}
	m.mu.Lock()
	if !m.cfg.Enabled || m.allowedLocked(ip) {
		m.mu.Unlock()
		return false
	}
	key := m.keyLocked(ip)
	if b := m.bans[key]; b != nil && b.Active(m.opts.Now()) {
		m.mu.Unlock()
		return true
	}
	b, msg := m.banLocked(key, "requested the trap path "+trap)
	m.mu.Unlock()
	m.banned(b, msg)
	return true
}

// banLocked bans key automatically, for longer the more often it was
// banned recently.
func (m *Manager) banLocked(key, reason string) (model.Ban, string) {
	now := m.opts.Now()
	strikes := 1
	if old := m.bans[key]; old != nil && !old.Manual {
		strikes = old.Strikes + 1
	}
	d := time.Duration(m.cfg.BanMinutes) * time.Minute
	limit := time.Duration(m.cfg.MaxBanMinutes) * time.Minute
	d = time.Duration(math.Min(float64(d)*math.Pow(2, float64(strikes-1)), float64(limit)))
	exp := now.Add(d)
	b := &model.Ban{Address: key, Reason: reason, Strikes: strikes, CreatedAt: now, ExpiresAt: &exp}
	m.bans[key] = b
	if el, ok := m.tracked[key]; ok {
		m.lru.Remove(el)
		delete(m.tracked, key)
	}
	msg := fmt.Sprintf("%s banned for %s: %s", key, formatDuration(d), reason)
	if strikes > 1 {
		msg += fmt.Sprintf(" (ban %d)", strikes)
	}
	return *b, msg
}

// banned reports a new automatic ban and saves.
func (m *Manager) banned(b model.Ban, msg string) {
	m.opts.Log.Warn("address banned", "address", b.Address, "reason", b.Reason, "strikes", b.Strikes)
	m.event(b, msg)
	m.saveSoon()
}

// event raises security.banned at most eventsPerMinute times a minute; the
// next one after a quiet spell mentions how many were not reported.
func (m *Manager) event(b model.Ban, msg string) {
	if m.opts.OnBan == nil {
		return
	}
	now := m.opts.Now()
	m.evMu.Lock()
	if now.Sub(m.evWindow) >= time.Minute {
		if m.evDropped > 0 {
			msg += fmt.Sprintf("; %d more addresses were banned in the last minute (%s, …)", m.evDropped, m.evDroppedA)
		}
		m.evWindow, m.evSent, m.evDropped = now, 0, 0
	}
	if m.evSent >= eventsPerMinute {
		if m.evDropped == 0 {
			m.evDroppedA = b.Address
		}
		m.evDropped++
		m.evMu.Unlock()
		return
	}
	m.evSent++
	m.evMu.Unlock()
	m.opts.OnBan(b, msg)
}

// ErrNotBanned is returned by Unban for an address that is not banned.
var ErrNotBanned = errors.New("this address is not banned")

// Ban bans an address or range by hand, for minutes (0 = until removed).
func (m *Manager) Ban(address string, minutes int, reason, by string) (model.Ban, error) {
	address = strings.TrimSpace(address)
	n, err := model.ParseCIDROrIP(address)
	if err != nil {
		return model.Ban{}, &model.ValidationError{Field: "address", Message: err.Error()}
	}
	ones, bits := n.Mask.Size()
	if (bits == 32 && ones < 8) || (bits == 128 && ones < 32) {
		return model.Ban{}, &model.ValidationError{Field: "address", Message: "the range is too wide (at most a /8 for IPv4, a /32 for IPv6)"}
	}
	if minutes < 0 || minutes > 525600 {
		return model.Ban{}, &model.ValidationError{Field: "minutes", Message: "must be between 0 (until removed) and 525600 (a year)"}
	}
	now := m.opts.Now()
	m.mu.Lock()
	for _, a := range m.allow {
		if aOnes, aBits := a.Mask.Size(); aBits == bits && aOnes <= ones && a.Contains(n.IP) {
			m.mu.Unlock()
			return model.Ban{}, &model.ValidationError{Field: "address", Message: fmt.Sprintf("%s is always allowed (loopback, a trusted proxy or the allow list)", a)}
		}
	}
	key := n.String()
	if ones == bits {
		key = n.IP.String()
	} else if bits == 128 && ones == m.cfg.IPv6Prefix {
		key = n.String() // the same key an automatic ban of that prefix uses
	}
	if reason = strings.TrimSpace(reason); reason == "" {
		reason = "banned manually"
	}
	b := &model.Ban{Address: key, Reason: reason, Manual: true, CreatedAt: now, CreatedBy: by}
	if minutes > 0 {
		exp := now.Add(time.Duration(minutes) * time.Minute)
		b.ExpiresAt = &exp
	}
	m.bans[key] = b
	m.reindexLocked()
	out := *b
	m.mu.Unlock()
	msg := fmt.Sprintf("%s banned by %s: %s", key, by, reason)
	if b.ExpiresAt != nil {
		msg = fmt.Sprintf("%s banned by %s for %s: %s", key, by, formatDuration(time.Duration(minutes)*time.Minute), reason)
	}
	m.event(out, msg)
	return out, m.save()
}

// Unban lifts the bans matching an address: the ban of that exact address
// or range, or every ban containing that IP address (an IPv6 prefix, a
// manual range). The address's history is forgotten too, so a later ban
// starts short again. It returns the addresses unbanned.
func (m *Manager) Unban(address string) ([]string, error) {
	address = strings.TrimSpace(address)
	now := m.opts.Now()
	m.mu.Lock()
	var removed []string
	ip := net.ParseIP(address)
	var canon string
	if n, err := model.ParseCIDROrIP(address); err == nil {
		canon = n.String()
	}
	for key, b := range m.bans {
		match := key == address || (canon != "" && banNet(b) != nil && banNet(b).String() == canon)
		if !match && ip != nil {
			if n := banNet(b); n != nil && n.Contains(ip) {
				match = true
			}
		}
		if match {
			if b.Active(now) {
				removed = append(removed, key)
			}
			delete(m.bans, key)
			if el, ok := m.tracked[key]; ok {
				m.lru.Remove(el)
				delete(m.tracked, key)
			}
		}
	}
	m.reindexLocked()
	m.mu.Unlock()
	if len(removed) == 0 {
		return nil, ErrNotBanned
	}
	slices.Sort(removed)
	return removed, m.save()
}

func banNet(b *model.Ban) *net.IPNet {
	n, err := model.ParseCIDROrIP(b.Address)
	if err != nil {
		return nil
	}
	return n
}

// List returns the bans in force, newest first.
func (m *Manager) List() []model.Ban {
	now := m.opts.Now()
	m.mu.RLock()
	out := []model.Ban{}
	for _, b := range m.bans {
		if b.Active(now) {
			out = append(out, *b)
		}
	}
	m.mu.RUnlock()
	slices.SortFunc(out, func(a, b model.Ban) int { return b.CreatedAt.Compare(a.CreatedAt) })
	return out
}

// Load restores saved bans, at startup.
func (m *Manager) Load(bans []model.Ban) {
	now := m.opts.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range bans {
		b := bans[i]
		if b.ExpiresAt != nil && now.Sub(*b.ExpiresAt) > historyFor {
			continue
		}
		m.bans[b.Address] = &b
	}
	m.reindexLocked()
}

// reindexLocked rebuilds the list of range bans, which Banned checks by
// containment. A ban keyed the way keyLocked keys its addresses (an IPv4
// address, an IPv6 prefix of the configured length) is found by key.
func (m *Manager) reindexLocked() {
	m.ranges = m.ranges[:0]
	for key, b := range m.bans {
		if n := banNet(b); n != nil && key != m.keyLocked(n.IP) {
			m.ranges = append(m.ranges, rangeBan{net: n, ban: b})
		}
	}
}

// snapshot is what is saved: bans in force and recent ones, the most
// recent first when there are too many.
func (m *Manager) snapshot() []model.Ban {
	now := m.opts.Now()
	m.mu.Lock()
	out := make([]model.Ban, 0, len(m.bans))
	for key, b := range m.bans {
		if b.ExpiresAt != nil && now.Sub(*b.ExpiresAt) > historyFor {
			delete(m.bans, key)
			continue
		}
		out = append(out, *b)
	}
	m.mu.Unlock()
	slices.SortFunc(out, func(a, b model.Ban) int { return b.CreatedAt.Compare(a.CreatedAt) })
	if len(out) > maxHistory {
		kept := out[:0]
		for _, b := range out {
			if b.Active(now) || len(kept) < maxHistory {
				kept = append(kept, b)
			}
		}
		out = kept
	}
	return out
}

func (m *Manager) save() error {
	if m.opts.Save == nil {
		return nil
	}
	return m.opts.Save(m.snapshot())
}

// saveSoon saves shortly, once for a burst of bans.
func (m *Manager) saveSoon() {
	m.saveMu.Lock()
	defer m.saveMu.Unlock()
	if m.pending || m.opts.Save == nil {
		return
	}
	m.pending = true
	time.AfterFunc(saveDelay, func() {
		m.saveMu.Lock()
		m.pending = false
		m.saveMu.Unlock()
		if err := m.save(); err != nil {
			m.opts.Log.Error("save bans", "err", err)
		}
	})
}

// Flush saves now, at shutdown.
func (m *Manager) Flush() error {
	if m == nil {
		return nil
	}
	return m.save()
}

func formatDuration(d time.Duration) string {
	unit := func(n int64, name string) string {
		if n == 1 {
			return "1 " + name
		}
		return fmt.Sprintf("%d %ss", n, name)
	}
	switch {
	case d >= 24*time.Hour && d%(24*time.Hour) == 0:
		return unit(int64(d/(24*time.Hour)), "day")
	case d >= time.Hour && d%time.Hour == 0:
		return unit(int64(d/time.Hour), "hour")
	case d >= time.Minute && d%time.Minute == 0:
		return unit(int64(d/time.Minute), "minute")
	default:
		return unit(int64(d/time.Second), "second")
	}
}
