package ipban

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

type harness struct {
	*Manager
	clock  *clock
	events []string
	saved  []model.Ban
	mu     sync.Mutex
}

func newHarness(t *testing.T, mutate ...func(*model.IPBanSettings)) *harness {
	t.Helper()
	h := &harness{clock: &clock{now: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)}}
	h.Manager = New(Options{
		Now: h.clock.Now,
		Save: func(b []model.Ban) error {
			h.mu.Lock()
			h.saved = b
			h.mu.Unlock()
			return nil
		},
		OnBan: func(b model.Ban, msg string) {
			h.mu.Lock()
			h.events = append(h.events, msg)
			h.mu.Unlock()
		},
	})
	s := model.DefaultIPBan()
	s.Enabled = true
	for _, m := range mutate {
		m(&s)
	}
	s.ApplyDefaults()
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	h.Apply(s, nil)
	return h
}

func ip(s string) net.IP { return net.ParseIP(s) }

func (h *harness) savedBans() []model.Ban {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.saved
}

func TestThresholdAndWindow(t *testing.T) {
	h := newHarness(t, func(s *model.IPBanSettings) { s.AuthFailures = model.BanRule{Threshold: 5, WindowSec: 60} })
	a := ip("203.0.113.7")

	// Four failures, then a quiet window: the old ones fade out.
	for range 4 {
		h.Record(a, AuthFailure)
	}
	if h.Banned(a) {
		t.Fatal("banned below the threshold")
	}
	h.clock.advance(61 * time.Second)
	h.Record(a, AuthFailure)
	if h.Banned(a) {
		t.Fatal("banned for failures spread over more than the window")
	}
	h.clock.advance(3 * time.Minute) // everything expired
	for i := range 5 {
		h.clock.advance(5 * time.Second)
		h.Record(a, AuthFailure)
		if got, want := h.Banned(a), i == 4; got != want {
			t.Fatalf("after %d failures within the window: banned = %v", i+1, got)
		}
	}
	// Other kinds have their own counters, other addresses their own.
	b := ip("203.0.113.8")
	for range 4 {
		h.Record(b, AuthFailure)
	}
	for range 3 {
		h.Record(b, RateLimited)
	}
	if h.Banned(b) {
		t.Fatal("counters of different kinds were added up")
	}

	list := h.List()
	if len(list) != 1 || list[0].Address != "203.0.113.7" || list[0].Strikes != 1 || list[0].ExpiresAt.Sub(h.clock.Now()) != 15*time.Minute {
		t.Fatalf("bans = %+v", list)
	}
	if !strings.Contains(list[0].Reason, "5 authentication failures within 1 minute") {
		t.Fatalf("reason %q", list[0].Reason)
	}
	// The ban expires by itself.
	h.clock.advance(15 * time.Minute)
	if h.Banned(a) || len(h.List()) != 0 {
		t.Fatal("ban did not expire")
	}
}

func TestRuleOff(t *testing.T) {
	h := newHarness(t, func(s *model.IPBanSettings) { s.NotFound.Threshold = 0 })
	for range 1000 {
		h.Record(ip("198.51.100.1"), NotFound)
	}
	if h.Banned(ip("198.51.100.1")) {
		t.Fatal("banned by a rule that is off")
	}
	h.Apply(model.DefaultIPBan(), nil) // banning itself off
	for range 100 {
		h.Record(ip("198.51.100.2"), AuthFailure)
	}
	if h.Trap(ip("198.51.100.2"), "/wp-login.php") || h.Banned(ip("198.51.100.2")) {
		t.Fatal("banned while banning is off")
	}
}

func TestEscalation(t *testing.T) {
	h := newHarness(t, func(s *model.IPBanSettings) { s.BanMinutes, s.MaxBanMinutes = 10, 35 })
	a := ip("192.0.2.50")
	for i, want := range []time.Duration{10 * time.Minute, 20 * time.Minute, 35 * time.Minute, 35 * time.Minute} {
		if !h.Trap(a, "/.env") {
			t.Fatalf("ban %d: trap did not fire", i+1)
		}
		b := h.List()[0]
		if b.Strikes != i+1 || b.ExpiresAt.Sub(h.clock.Now()) != want {
			t.Fatalf("ban %d: strikes %d, length %s, want %s", i+1, b.Strikes, b.ExpiresAt.Sub(h.clock.Now()), want)
		}
		h.clock.advance(want)
	}
	// A week after the last ban, the address starts over.
	h.clock.advance(historyFor + time.Minute)
	h.snapshot() // drops old history
	h.Trap(a, "/.env")
	if b := h.List()[0]; b.Strikes != 1 {
		t.Fatalf("strikes after a clean week = %d", b.Strikes)
	}
}

func TestAllowList(t *testing.T) {
	_, proxyNet, _ := net.ParseCIDR("10.1.0.0/16")
	h := newHarness(t, func(s *model.IPBanSettings) { s.AllowList = []string{"198.51.100.0/24"} })
	h.Apply(h.cfg, []*net.IPNet{proxyNet})
	for _, a := range []string{"127.0.0.1", "127.9.9.9", "::1", "198.51.100.77", "10.1.2.3", "::ffff:127.0.0.1"} {
		for range 200 {
			h.Record(ip(a), NotFound)
			h.Record(ip(a), AuthFailure)
		}
		if h.Trap(ip(a), "/wp-login.php") || h.Banned(ip(a)) {
			t.Errorf("%s was banned", a)
		}
	}
	for _, a := range []string{"127.0.0.1", "10.1.0.0/16", "198.51.100.5"} {
		if _, err := h.Ban(a, 10, "", "admin"); err == nil {
			t.Errorf("manual ban of always-allowed %s accepted", a)
		}
	}
	// A range containing an allowed address may be banned; the address
	// itself still gets through.
	if _, err := h.Ban("10.0.0.0/8", 10, "", "admin"); err != nil {
		t.Fatal(err)
	}
	if !h.Banned(ip("10.200.0.1")) || h.Banned(ip("10.1.0.9")) {
		t.Fatal("range ban vs trusted proxy")
	}
}

func TestIPv6Prefix(t *testing.T) {
	h := newHarness(t, func(s *model.IPBanSettings) { s.NotFound = model.BanRule{Threshold: 4, WindowSec: 60} })
	// A client rotating addresses within its /64 is counted as one.
	for i := range 4 {
		h.Record(ip(fmt.Sprintf("2001:db8:1:2::%x", i+1)), NotFound)
	}
	if !h.Banned(ip("2001:db8:1:2:ffff::1")) {
		t.Fatal("the /64 was not banned")
	}
	if h.Banned(ip("2001:db8:1:3::1")) {
		t.Fatal("the neighbouring /64 was banned")
	}
	if l := h.List(); len(l) != 1 || l[0].Address != "2001:db8:1:2::/64" {
		t.Fatalf("bans = %+v", l)
	}

	// With /56, the neighbour goes too; the existing /64 ban still holds.
	h.cfg.IPv6Prefix = 56
	h.Apply(h.cfg, nil)
	if !h.Banned(ip("2001:db8:1:2::9")) {
		t.Fatal("existing /64 ban lost when the prefix changed")
	}
	h.Trap(ip("2001:db8:1:3::1"), "/xmlrpc.php")
	if !h.Banned(ip("2001:db8:1:ff::1")) || h.Banned(ip("2001:db8:1:100::1")) {
		t.Fatal("/56 ban")
	}

	// IPv4 addresses are banned one by one.
	h.Trap(ip("192.0.2.1"), "/phpmyadmin/index.php")
	if !h.Banned(ip("192.0.2.1")) || h.Banned(ip("192.0.2.2")) {
		t.Fatal("IPv4 ban")
	}
}

func TestTrapPaths(t *testing.T) {
	h := newHarness(t)
	for _, p := range []string{"/", "/wp-login", "/index.php", "/blog/wp-login.php", "/envelope"} {
		if h.Trap(ip("192.0.2.9"), p) {
			t.Fatalf("%s treated as a trap", p)
		}
	}
	for i, p := range []string{"/wp-login.php", "/WP-LOGIN.PHP?x", "/.env.production", "/.git/config", "/phpMyAdmin/"} {
		a := ip(fmt.Sprintf("192.0.2.%d", 100+i))
		if !h.Trap(a, p) || !h.Banned(a) {
			t.Fatalf("%s did not ban", p)
		}
	}
}

func TestManualBanAndUnban(t *testing.T) {
	h := newHarness(t)
	for _, bad := range []string{"", "nonsense", "0.0.0.0/0", "10.0.0.0/4", "2001::/16"} {
		var ve *model.ValidationError
		if _, err := h.Ban(bad, 0, "", "admin"); !errors.As(err, &ve) {
			t.Errorf("Ban(%q) = %v", bad, err)
		}
	}
	b, err := h.Ban("203.0.113.0/24", 0, "abuse report", "admin")
	if err != nil || b.ExpiresAt != nil || !b.Manual || b.Address != "203.0.113.0/24" {
		t.Fatalf("ban = %+v, %v", b, err)
	}
	if _, err := h.Ban("2001:db8::5", 30, "", "admin"); err != nil {
		t.Fatal(err)
	}
	if !h.Banned(ip("203.0.113.200")) || !h.Banned(ip("2001:db8::5")) || h.Banned(ip("2001:db8::6")) {
		t.Fatal("manual bans not enforced")
	}
	h.clock.advance(24 * 365 * time.Hour)
	if !h.Banned(ip("203.0.113.1")) || h.Banned(ip("2001:db8::5")) {
		t.Fatal("permanent vs 30-minute ban")
	}

	// Unban by any address in the range.
	got, err := h.Unban("203.0.113.9")
	if err != nil || len(got) != 1 || got[0] != "203.0.113.0/24" || h.Banned(ip("203.0.113.9")) {
		t.Fatalf("Unban = %v, %v", got, err)
	}
	if _, err := h.Unban("203.0.113.9"); !errors.Is(err, ErrNotBanned) {
		t.Fatalf("second unban: %v", err)
	}

	// Unbanning forgives: the next automatic ban is a first one again.
	a := ip("2001:db8:5::1")
	h.Trap(a, "/.env")
	h.clock.advance(time.Hour)
	h.Trap(a, "/.env")
	if s := h.List()[0].Strikes; s != 2 {
		t.Fatalf("strikes %d", s)
	}
	if _, err := h.Unban("2001:db8:5::/64"); err != nil {
		t.Fatal(err)
	}
	h.Trap(a, "/.env")
	if s := h.List()[0].Strikes; s != 1 {
		t.Fatalf("strikes after an unban %d", s)
	}
	if len(h.events) == 0 || !strings.Contains(h.events[0], "banned by admin") {
		t.Fatalf("events %v", h.events)
	}
}

// TestPersistence: bans survive a restart through Save and Load, and
// expired ones keep the strike count for a week.
func TestPersistence(t *testing.T) {
	h := newHarness(t)
	h.Trap(ip("192.0.2.1"), "/.env")
	h.Ban("198.51.100.0/24", 0, "manual", "admin")
	h.Trap(ip("192.0.2.2"), "/.env")
	h.clock.advance(20 * time.Minute) // the automatic bans expire
	h.Trap(ip("192.0.2.3"), "/.env")
	if err := h.Flush(); err != nil {
		t.Fatal(err)
	}
	saved := h.savedBans()
	if len(saved) != 4 {
		t.Fatalf("saved %d bans: %+v", len(saved), saved)
	}

	h2 := newHarness(t)
	h2.clock.advance(h.clock.Now().Sub(h2.clock.Now()))
	h2.Load(saved)
	if !h2.Banned(ip("192.0.2.3")) || !h2.Banned(ip("198.51.100.7")) || h2.Banned(ip("192.0.2.1")) {
		t.Fatal("restored bans")
	}
	if len(h2.List()) != 2 {
		t.Fatalf("active after restore: %+v", h2.List())
	}
	h2.clock.advance(time.Second)
	h2.Trap(ip("192.0.2.1"), "/.env")
	if b := h2.List()[0]; b.Address != "192.0.2.1" || b.Strikes != 2 {
		t.Fatalf("escalation lost across the restart: %+v", b)
	}

	// Automatic bans are saved shortly after, without an explicit flush.
	h3 := newHarness(t)
	h3.Trap(ip("192.0.2.77"), "/.env")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if len(h3.savedBans()) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("automatic ban not saved")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestTrackedAddressesAreBounded(t *testing.T) {
	h := newHarness(t)
	for i := range maxTracked + 500 {
		h.Record(net.IPv4(10, byte(i>>16), byte(i>>8), byte(i)), AuthFailure)
	}
	if n := h.lru.Len(); n != maxTracked || len(h.tracked) != maxTracked {
		t.Fatalf("tracking %d addresses (map %d), cap %d", n, len(h.tracked), maxTracked)
	}
}

func TestBanEventsAreRateLimited(t *testing.T) {
	h := newHarness(t)
	for i := range 50 {
		h.Trap(net.IPv4(192, 0, 2, byte(i)), "/.env")
	}
	if len(h.events) != eventsPerMinute {
		t.Fatalf("%d events for 50 bans in a minute", len(h.events))
	}
	h.clock.advance(time.Minute)
	h.Trap(ip("198.51.100.1"), "/.env")
	if last := h.events[len(h.events)-1]; !strings.Contains(last, "40 more addresses were banned") {
		t.Fatalf("summary event %q", last)
	}
}

// TestAutomaticBansAreCapped: one IPv6 /48 is 65,536 /64s, each banned by
// a single trap-path request. Past the cap the automatic bans that expire
// soonest are lifted; manual bans never are.
func TestAutomaticBansAreCapped(t *testing.T) {
	h := newHarness(t)
	if _, err := h.Ban("198.51.100.1", 1, "", "admin"); err != nil { // expires before any automatic ban
		t.Fatal(err)
	}
	const n = maxAutoBans + 2000
	addr := func(i int) net.IP { return ip(fmt.Sprintf("2001:db8:1:%x::1", i)) }
	for i := range n {
		h.clock.advance(time.Millisecond)
		if !h.Trap(addr(i), "/.env") {
			t.Fatalf("ban %d refused", i)
		}
	}
	auto := 0
	for _, b := range h.List() {
		if !b.Manual {
			auto++
		}
	}
	if auto == 0 || auto > maxAutoBans {
		t.Fatalf("%d automatic bans in force, cap %d", auto, maxAutoBans)
	}
	if !h.Banned(ip("198.51.100.1")) {
		t.Fatal("a manual ban was lifted")
	}
	if !h.Banned(addr(n-1)) || h.Banned(addr(0)) {
		t.Fatal("not the soonest-expiring bans were lifted")
	}
	if err := h.Flush(); err != nil {
		t.Fatal(err)
	}
	if saved := h.savedBans(); len(saved) != auto+1 {
		t.Fatalf("saved %d bans, %d in force", len(saved), auto+1)
	}

	// Expired bans are kept for escalation, but not without bound either.
	for round := range 3 {
		h.clock.advance(time.Hour)
		for i := range n {
			h.Trap(ip(fmt.Sprintf("2001:db8:%x:%x::1", round+2, i)), "/.env")
		}
	}
	h.Manager.mu.RLock()
	total := len(h.bans)
	h.Manager.mu.RUnlock()
	if total > maxAutoBans+maxHistory+1 {
		t.Fatalf("%d bans remembered", total)
	}
}
