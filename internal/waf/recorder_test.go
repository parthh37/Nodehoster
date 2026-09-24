package waf

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

func TestRecorderSavesInBatches(t *testing.T) {
	var mu sync.Mutex
	var saved []model.WAFEvent
	var batches int
	rec := NewRecorder(RecorderOptions{Save: func(_ context.Context, evs []model.WAFEvent) error {
		mu.Lock()
		defer mu.Unlock()
		saved = append(saved, evs...) // copies: the batch is reused
		batches++
		return nil
	}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { rec.Run(ctx); close(done) }()
	for i := 0; i < 300; i++ {
		rec.Record(model.WAFEvent{ID: "e", Action: model.WAFActionDetected})
	}
	cancel()
	<-done
	mu.Lock()
	defer mu.Unlock()
	if len(saved) != 300 || batches < 2 {
		t.Errorf("saved %d events in %d batches", len(saved), batches)
	}
	if rec.Dropped() != 0 {
		t.Errorf("dropped %d", rec.Dropped())
	}
}

func TestRecorderDropsBeyondRate(t *testing.T) {
	rec := NewRecorder(RecorderOptions{}) // Run never started: the queue fills
	for i := 0; i < 3*queueSize; i++ {
		rec.Record(model.WAFEvent{Action: model.WAFActionDetected})
	}
	if rec.Dropped() < int64(3*queueSize-2*eventsPerSecond)-10 {
		t.Errorf("dropped %d", rec.Dropped())
	}
}

func TestRecorderNotableRateLimited(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	var msgs []string
	rec := NewRecorder(RecorderOptions{
		Now:     func() time.Time { return now },
		OnBlock: func(_ model.WAFEvent, msg string) { msgs = append(msgs, msg) },
	})
	ev := model.WAFEvent{ID: "abc", Action: model.WAFActionBlocked, Method: "GET", Path: "/login", ClientIP: "203.0.113.7", Score: 5,
		Matches: []model.WAFMatch{{RuleID: 942100, Message: "SQL injection", In: model.WAFInArg, Name: "user"}}}
	for i := 0; i < 25; i++ {
		rec.Record(ev)
	}
	rec.Record(model.WAFEvent{Action: model.WAFActionDetected}) // detections are not notable
	if len(msgs) != notablePerMinute {
		t.Fatalf("%d notable events", len(msgs))
	}
	if want := "Blocked GET /login from 203.0.113.7: SQL injection (rule 942100) in argument user, score 5 [request abc]"; msgs[0] != want {
		t.Errorf("message %q", msgs[0])
	}
	now = now.Add(time.Minute)
	rec.Record(ev)
	if len(msgs) != notablePerMinute+1 || !strings.Contains(msgs[len(msgs)-1], "15 more requests were blocked") {
		t.Errorf("summary: %q", msgs[len(msgs)-1])
	}
}

func TestCounters(t *testing.T) {
	rec := NewRecorder(RecorderOptions{})
	c := rec.Counters("s1")
	if rec.Counters("s1") != c {
		t.Fatal("counters not kept")
	}
	c.Inspected.Add(3)
	c.Blocked.Add(1)
	c.Matched(Result{Matches: []model.WAFMatch{{Category: model.WAFSQLi}, {Category: model.WAFXSS}, {Category: model.WAFSQLi}}})
	s := rec.Snapshot()["s1"]
	if s.Inspected != 3 || s.Blocked != 1 || s.Matches[model.WAFSQLi] != 2 || s.Matches[model.WAFXSS] != 1 || s.Matches[model.WAFRCE] != 0 {
		t.Errorf("snapshot %+v", s)
	}
	rec.Forget("s1")
	if _, ok := rec.Snapshot()["s1"]; ok {
		t.Error("forgotten site still counted")
	}

	var nilRec *Recorder
	nilRec.Record(model.WAFEvent{})
	nilRec.Counters("x").Matched(Result{})
	if nilRec.Dropped() != 0 || len(nilRec.Snapshot()) != 0 {
		t.Error("nil recorder")
	}
}

func TestNewEventSanitizes(t *testing.T) {
	r := browser("GET", "/a%0aFAKE%20LOG%20LINE?secret=token123", "", "")
	r.Header.Set("User-Agent", strings.Repeat("x", 2000))
	ev := NewEvent(r, "id1", "site", "203.0.113.7", model.WAFActionBlocked, Result{Score: 5, Threshold: 5, Paranoia: 1}, time.Now())
	if ev.Path != `/a\nFAKE LOG LINE` {
		t.Errorf("path %q", ev.Path)
	}
	if len(ev.UserAgent) > 520 || !strings.HasSuffix(ev.UserAgent, "…") {
		t.Errorf("user agent %d bytes", len(ev.UserAgent))
	}
	if strings.Contains(ev.Path+ev.Host, "token123") {
		t.Error("query string recorded")
	}
	if id := NewRequestID(); len(id) != 16 || id == NewRequestID() {
		t.Errorf("request ID %q", id)
	}
}
