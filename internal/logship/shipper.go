package logship

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

// Sender delivers batches to one collector.
type Sender interface {
	Send(ctx context.Context, batch []Record) error
	Close() error
}

// permanent marks an error that retrying cannot fix (a 400 or 401 from the
// collector): the batch is dropped at once.
type permanent struct{ error }

func (p permanent) Unwrap() error { return p.error }

// Options tune the pipeline; zero values take the defaults.
type Options struct {
	QueueSize   int           // records waiting per target (10,000)
	BatchSize   int           // records per request (500)
	FlushEvery  time.Duration // a partial batch waits at most this long (1 s)
	RetryBase   time.Duration // first retry delay, doubling (500 ms)
	RetryMax    time.Duration // longest retry delay (30 s)
	MaxAttempts int           // a batch is dropped after this many failures (6)
	Timeout     time.Duration // one delivery (30 s)
}

func (o *Options) defaults() {
	if o.QueueSize <= 0 {
		o.QueueSize = 10000
	}
	if o.BatchSize <= 0 {
		o.BatchSize = 500
	}
	if o.FlushEvery <= 0 {
		o.FlushEvery = time.Second
	}
	if o.RetryBase <= 0 {
		o.RetryBase = 500 * time.Millisecond
	}
	if o.RetryMax <= 0 {
		o.RetryMax = 30 * time.Second
	}
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = 6
	}
	if o.Timeout <= 0 {
		o.Timeout = 30 * time.Second
	}
}

// Status is a target's delivery state, for the settings page.
type Status struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Type        string     `json:"type"`
	Enabled     bool       `json:"enabled"`
	Queued      int        `json:"queued"`
	Sent        uint64     `json:"sent"`
	Dropped     uint64     `json:"dropped"` // queue full: the oldest records were discarded
	Failed      uint64     `json:"failed"`  // given up on after the retries, or refused by the collector
	LastError   string     `json:"lastError,omitempty"`
	LastErrorAt *time.Time `json:"lastErrorAt,omitempty"`
	LastSuccess *time.Time `json:"lastSuccess,omitempty"`
}

// Shipper fans records out to the configured targets.
type Shipper struct {
	log      *slog.Logger // must not ship: see Tee
	siteName func(string) string
	hostname string
	opts     Options
	// NewSender builds a target's sender; tests replace it.
	NewSender func(t model.LogTarget, hostname string) (Sender, error)

	mu      sync.RWMutex
	workers []*worker
	wants   atomic.Uint32 // sources some enabled target takes
	minSrv  atomic.Int32  // lowest server-log level rank wanted
}

func New(log *slog.Logger, hostname string, siteName func(string) string, opts Options) *Shipper {
	opts.defaults()
	s := &Shipper{log: log, siteName: siteName, hostname: hostname, opts: opts, NewSender: NewSender}
	s.minSrv.Store(99)
	return s
}

func sourceBit(src string) uint32 {
	if i := slices.Index(model.LogSources, src); i >= 0 {
		return 1 << i
	}
	return 0
}

// Wants reports whether any target takes records of a source, so callers
// can skip building records nobody ships.
func (s *Shipper) Wants(source string) bool {
	return s != nil && s.wants.Load()&sourceBit(source) != 0
}

// WantsServerLevel reports whether a server-log record at level is shipped.
func (s *Shipper) WantsServerLevel(level string) bool {
	return s.Wants(model.LogSourceServer) && int32(model.LogLevelRank(level)) >= s.minSrv.Load()
}

// Configure applies targets (secrets in plain text). Unchanged targets keep
// their queue and counters; changed ones start over.
func (s *Shipper) Configure(targets []model.LogTarget) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old := map[string]*worker{}
	for _, w := range s.workers {
		old[w.cfg.ID] = w
	}
	var next []*worker
	var wants uint32
	minSrv := int32(99)
	for _, t := range targets {
		if !t.Enabled {
			continue
		}
		key, _ := json.Marshal(t)
		if w, ok := old[t.ID]; ok && w.key == string(key) {
			delete(old, t.ID)
			next = append(next, w)
		} else {
			sender, err := s.NewSender(t, s.hostname)
			if err != nil {
				s.log.Warn("log shipping target not started", "target", t.Name, "err", err)
				continue
			}
			w := newWorker(t, string(key), sender, s.log, s.opts)
			w.siteName = s.siteName
			next = append(next, w)
			go w.run()
		}
		for _, src := range t.Sources {
			wants |= sourceBit(src)
		}
		if slices.Contains(t.Sources, model.LogSourceServer) {
			minSrv = min(minSrv, int32(model.LogLevelRank(t.MinLevel)))
		}
	}
	for _, w := range old {
		go w.close(2 * time.Second)
	}
	s.workers = next
	s.wants.Store(wants)
	s.minSrv.Store(minSrv)
}

// Ship queues a record for every target that takes it. It never blocks.
func (s *Shipper) Ship(r Record) {
	if s == nil || s.wants.Load()&sourceBit(r.Source) == 0 {
		return
	}
	if r.Time.IsZero() {
		r.Time = time.Now()
	}
	// The site's name is looked up by the target's goroutine, not here:
	// callers may hold locks the lookup needs.
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, w := range s.workers {
		if w.accepts(&r) {
			w.enqueue(r)
		}
	}
}

// Status reports every enabled target's counters.
func (s *Shipper) Status() []Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []Status{}
	for _, w := range s.workers {
		out = append(out, w.status())
	}
	return out
}

// Test sends one record to a target now and reports the collector's answer.
func (s *Shipper) Test(ctx context.Context, t model.LogTarget) error {
	sender, err := s.NewSender(t, s.hostname)
	if err != nil {
		return err
	}
	defer sender.Close()
	return sender.Send(ctx, []Record{{
		Time: time.Now(), Source: model.LogSourceServer, Level: "info",
		Message: "Test message from NodeHoster log shipping (target " + t.Name + ")",
		Attrs:   map[string]string{"test": "true"},
	}})
}

// Close stops every target, giving each a moment to send what is queued.
func (s *Shipper) Close() {
	s.mu.Lock()
	ws := s.workers
	s.workers = nil
	s.wants.Store(0)
	s.mu.Unlock()
	var wg sync.WaitGroup
	for _, w := range ws {
		wg.Add(1)
		go func() { defer wg.Done(); w.close(3 * time.Second) }()
	}
	wg.Wait()
}

// ---- one target

type worker struct {
	cfg      model.LogTarget
	key      string
	sender   Sender
	log      *slog.Logger
	opts     Options
	sources  uint32
	sites    map[string]bool
	minLevel int
	siteName func(string) string

	mu            sync.Mutex
	q             []Record // ring buffer
	head, n       int
	sent, dropped uint64
	failed        uint64
	lastErr       string
	lastErrAt     time.Time
	lastOK        time.Time
	lastLogged    time.Time

	wake  chan struct{}
	stop  chan struct{}
	done  chan struct{}
	grace time.Duration
}

func newWorker(t model.LogTarget, key string, sender Sender, log *slog.Logger, opts Options) *worker {
	w := &worker{
		cfg: t, key: key, sender: sender, log: log, opts: opts, minLevel: model.LogLevelRank(t.MinLevel),
		q: make([]Record, opts.QueueSize), wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{}),
	}
	for _, src := range t.Sources {
		w.sources |= sourceBit(src)
	}
	if len(t.SiteIDs) > 0 {
		w.sites = map[string]bool{}
		for _, id := range t.SiteIDs {
			w.sites[id] = true
		}
	}
	return w
}

func (w *worker) accepts(r *Record) bool {
	if w.sources&sourceBit(r.Source) == 0 {
		return false
	}
	if r.Source == model.LogSourceServer && model.LogLevelRank(r.Level) < w.minLevel {
		return false
	}
	if w.sites != nil && r.SiteID != "" && !w.sites[r.SiteID] {
		return false
	}
	return true
}

func (w *worker) enqueue(r Record) {
	w.mu.Lock()
	if w.n == len(w.q) {
		// Full: the oldest record goes, so the newest (most useful when
		// something is going wrong) are kept.
		w.head = (w.head + 1) % len(w.q)
		w.n--
		w.dropped++
	}
	w.q[(w.head+w.n)%len(w.q)] = r
	w.n++
	full := w.n >= w.opts.BatchSize
	w.mu.Unlock()
	if full {
		select {
		case w.wake <- struct{}{}:
		default:
		}
	}
}

func (w *worker) take(max int) []Record {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := min(max, w.n)
	if n == 0 {
		return nil
	}
	out := make([]Record, n)
	for i := range n {
		j := (w.head + i) % len(w.q)
		out[i] = w.q[j]
		w.q[j] = Record{}
	}
	w.head = (w.head + n) % len(w.q)
	w.n -= n
	return out
}

func (w *worker) run() {
	defer close(w.done)
	t := time.NewTicker(w.opts.FlushEvery)
	defer t.Stop()
	for {
		select {
		case <-w.stop:
			w.drain()
			w.sender.Close()
			return
		case <-t.C:
		case <-w.wake:
		}
		for {
			batch := w.take(w.opts.BatchSize)
			if batch == nil || !w.deliver(batch) {
				break
			}
		}
	}
}

// names fills in site names.
func (w *worker) names(batch []Record) {
	if w.siteName == nil {
		return
	}
	for i := range batch {
		if batch[i].SiteID != "" && batch[i].SiteName == "" {
			batch[i].SiteName = w.siteName(batch[i].SiteID)
		}
	}
}

// drain sends what is queued once, without retries, within the grace time.
func (w *worker) drain() {
	deadline := time.Now().Add(w.grace)
	for time.Now().Before(deadline) {
		batch := w.take(w.opts.BatchSize)
		if batch == nil {
			return
		}
		w.names(batch)
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		err := w.sender.Send(ctx, batch)
		cancel()
		w.result(len(batch), err)
		if err != nil {
			return
		}
	}
}

// deliver sends a batch, retrying with exponential backoff. It returns
// false when the worker is stopping.
func (w *worker) deliver(batch []Record) bool {
	w.names(batch)
	delay := w.opts.RetryBase
	for attempt := 1; ; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), w.opts.Timeout)
		go func() {
			select {
			case <-w.stop:
				cancel()
			case <-ctx.Done():
			}
		}()
		err := w.sender.Send(ctx, batch)
		cancel()
		w.result(len(batch), err)
		if err == nil {
			return true
		}
		var perm permanent
		if errors.As(err, &perm) || attempt >= w.opts.MaxAttempts {
			w.mu.Lock()
			w.failed += uint64(len(batch))
			w.mu.Unlock()
			return true
		}
		select {
		case <-w.stop:
			return false
		case <-time.After(delay):
		}
		delay = min(delay*2, w.opts.RetryMax)
	}
}

func (w *worker) result(n int, err error) {
	w.mu.Lock()
	now := time.Now()
	if err == nil {
		w.sent += uint64(n)
		w.lastOK = now
		w.mu.Unlock()
		return
	}
	w.lastErr, w.lastErrAt = err.Error(), now
	// At most one line a minute per target in the server log: a collector
	// that is down must not flood it (and these lines are not shipped).
	logIt := now.Sub(w.lastLogged) > time.Minute
	if logIt {
		w.lastLogged = now
	}
	w.mu.Unlock()
	if logIt {
		w.log.Warn("log shipping failed", "target", w.cfg.Name, "err", err)
	}
}

func (w *worker) status() Status {
	w.mu.Lock()
	defer w.mu.Unlock()
	st := Status{ID: w.cfg.ID, Name: w.cfg.Name, Type: w.cfg.Type, Enabled: true, Queued: w.n,
		Sent: w.sent, Dropped: w.dropped, Failed: w.failed, LastError: w.lastErr}
	if !w.lastErrAt.IsZero() {
		t := w.lastErrAt
		st.LastErrorAt = &t
	}
	if !w.lastOK.IsZero() {
		t := w.lastOK
		st.LastSuccess = &t
	}
	return st
}

func (w *worker) close(grace time.Duration) {
	w.grace = grace
	select {
	case <-w.stop:
	default:
		close(w.stop)
	}
	select {
	case <-w.done:
	case <-time.After(grace + 5*time.Second):
	}
}

// httpStatusError is a collector's HTTP error.
func httpStatusError(code int, body string) error {
	err := fmt.Errorf("HTTP %d %s", code, body)
	if code >= 400 && code < 500 && code != 408 && code != 429 {
		return permanent{err}
	}
	return err
}
