package ipban

import (
	"strings"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

// TestWAFBlocksBan: requests the web application firewall blocked count
// towards a ban under their own rule.
func TestWAFBlocksBan(t *testing.T) {
	h := newHarness(t) // default rule: 5 blocks within 60 s
	a := ip("198.51.100.9")
	for i := 0; i < 4; i++ {
		h.Record(a, WAFBlocked)
		h.clock.advance(5 * time.Second)
	}
	if h.Banned(a) {
		t.Fatal("banned after 4 blocks")
	}
	h.Record(a, WAFBlocked)
	if !h.Banned(a) {
		t.Fatal("not banned after 5 blocks")
	}
	if len(h.events) != 1 || !strings.Contains(h.events[0], "5 requests blocked by the web application firewall within 1 minute") {
		t.Errorf("events %q", h.events)
	}

	// Threshold 0 turns the rule off; the other rules are unaffected.
	h = newHarness(t, func(s *model.IPBanSettings) { s.WAFBlocks = model.BanRule{Threshold: 0, WindowSec: 60} })
	for i := 0; i < 50; i++ {
		h.Record(a, WAFBlocked)
	}
	if h.Banned(a) {
		t.Error("banned with the rule off")
	}
}
