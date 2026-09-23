package mail

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/parthh37/nodehoster/internal/config"
	"github.com/parthh37/nodehoster/internal/model"
)

// ErrNotFound is returned for a message that is not in the queue.
var ErrNotFound = errors.New("no such message")

// queue is the spool on disk, like the IIS 6 Queue and Badmail folders:
// every message is <id>.eml (the content) and <id>.json (its envelope and
// delivery state) in queue/ while it is being delivered, and in failed/
// once it is undeliverable. The content is written before the message is
// acknowledged, so an accepted message survives a crash or reboot.
type queue struct {
	dir string
	mu  sync.Mutex
	msg map[string]*entry
}

type entry struct {
	model.MailMessage
	busy bool // an attempt is running; never persisted
}

func openQueue(dir string) (*queue, error) {
	q := &queue{dir: dir, msg: map[string]*entry{}}
	for _, d := range []string{q.path("queue"), q.path("failed"), q.path("pickup")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return nil, err
		}
	}
	for _, sub := range []string{"queue", "failed"} {
		files, _ := filepath.Glob(filepath.Join(q.path(sub), "*.json"))
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			var m model.MailMessage
			if json.Unmarshal(data, &m) != nil || m.ID == "" {
				continue
			}
			if _, err := os.Stat(q.emlPath(&m)); err != nil {
				continue // metadata without content: nothing to send
			}
			if m.State == model.MailSending {
				m.State = model.MailQueued // interrupted by a restart
			}
			q.msg[m.ID] = &entry{MailMessage: m}
		}
	}
	return q, nil
}

func (q *queue) path(sub string) string { return filepath.Join(q.dir, sub) }

func (q *queue) sub(m *model.MailMessage) string {
	if m.State == model.MailFailed {
		return "failed"
	}
	return "queue"
}

func (q *queue) emlPath(m *model.MailMessage) string {
	return filepath.Join(q.path(q.sub(m)), m.ID+".eml")
}

func (q *queue) jsonPath(m *model.MailMessage) string {
	return filepath.Join(q.path(q.sub(m)), m.ID+".json")
}

// add stores a new message and its content.
func (q *queue) add(m model.MailMessage, data []byte) error {
	if err := config.WriteFileAtomic(q.emlPath(&m), data, 0o640); err != nil {
		return err
	}
	if err := q.writeMeta(&m); err != nil {
		os.Remove(q.emlPath(&m))
		return err
	}
	q.mu.Lock()
	q.msg[m.ID] = &entry{MailMessage: m}
	q.mu.Unlock()
	return nil
}

func (q *queue) writeMeta(m *model.MailMessage) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(q.jsonPath(m), data, 0o640)
}

// content reads a message's content.
func (q *queue) content(id string) ([]byte, error) {
	q.mu.Lock()
	e, ok := q.msg[id]
	var p string
	if ok {
		p = q.emlPath(&e.MailMessage)
	}
	q.mu.Unlock()
	if !ok {
		return nil, ErrNotFound
	}
	return os.ReadFile(p)
}

// claim marks the messages due for an attempt as busy and returns
// snapshots of them.
func (q *queue) claim(now time.Time, max int) []model.MailMessage {
	q.mu.Lock()
	defer q.mu.Unlock()
	var due []*entry
	for _, e := range q.msg {
		if e.State == model.MailQueued && !e.busy && (e.NextAttempt == nil || !e.NextAttempt.After(now)) {
			due = append(due, e)
		}
	}
	slices.SortFunc(due, func(a, b *entry) int { return a.ReceivedAt.Compare(b.ReceivedAt) })
	if len(due) > max {
		due = due[:max]
	}
	out := make([]model.MailMessage, len(due))
	for i, e := range due {
		e.busy = true
		e.State = model.MailSending
		out[i] = clone(e.MailMessage)
	}
	return out
}

// finish records the outcome of an attempt. A message whose recipients
// have all been delivered is removed; one with failed recipients and
// none pending moves to failed/.
func (q *queue) finish(m model.MailMessage) (failed bool, err error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	e, ok := q.msg[m.ID]
	if !ok {
		return false, nil // deleted while it was being sent
	}
	e.busy = false
	// The worker keeps reading its copy after this returns (to report
	// failures), so the queue must not share its slices.
	m = clone(m)
	pending, bad := 0, 0
	for _, r := range m.Recipients {
		switch r.State {
		case model.RcptPending:
			pending++
		case model.RcptFailed:
			bad++
		}
	}
	switch {
	case pending > 0:
		m.State = model.MailQueued
		e.MailMessage = m
		return false, q.writeMeta(&e.MailMessage)
	case bad == 0:
		delete(q.msg, m.ID)
		os.Remove(q.jsonPath(&e.MailMessage))
		return false, os.Remove(q.emlPath(&e.MailMessage))
	default:
		oldEml, oldJSON := q.emlPath(&e.MailMessage), q.jsonPath(&e.MailMessage)
		m.State, m.NextAttempt = model.MailFailed, nil
		e.MailMessage = m
		if err := os.Rename(oldEml, q.emlPath(&e.MailMessage)); err != nil {
			return true, err
		}
		os.Remove(oldJSON)
		return true, q.writeMeta(&e.MailMessage)
	}
}

// retry makes a queued message due now. A failed one goes back to the
// queue with its failed recipients pending again.
func (q *queue) retry(id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	e, ok := q.msg[id]
	if !ok {
		return ErrNotFound
	}
	if e.busy {
		return nil
	}
	if e.State == model.MailFailed {
		oldEml, oldJSON := q.emlPath(&e.MailMessage), q.jsonPath(&e.MailMessage)
		e.State = model.MailQueued
		e.ReceivedAt = time.Now() // a fresh expiry period
		for i := range e.Recipients {
			if e.Recipients[i].State == model.RcptFailed {
				e.Recipients[i].State, e.Recipients[i].Error = model.RcptPending, ""
			}
		}
		if err := os.Rename(oldEml, q.emlPath(&e.MailMessage)); err != nil {
			e.State = model.MailFailed
			return err
		}
		os.Remove(oldJSON)
	}
	e.NextAttempt = nil
	return q.writeMeta(&e.MailMessage)
}

func (q *queue) retryAll() {
	q.mu.Lock()
	var ids []string
	for id, e := range q.msg {
		if e.State == model.MailQueued {
			ids = append(ids, id)
		}
	}
	q.mu.Unlock()
	for _, id := range ids {
		q.retry(id)
	}
}

func (q *queue) remove(id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	e, ok := q.msg[id]
	if !ok {
		return ErrNotFound
	}
	delete(q.msg, id)
	os.Remove(q.jsonPath(&e.MailMessage))
	return os.Remove(q.emlPath(&e.MailMessage))
}

// purge deletes failed messages older than keep.
func (q *queue) purge(keep time.Duration) {
	q.mu.Lock()
	var old []string
	for id, e := range q.msg {
		last := e.ReceivedAt
		if e.LastAttempt != nil {
			last = *e.LastAttempt
		}
		if e.State == model.MailFailed && time.Since(last) > keep {
			old = append(old, id)
		}
	}
	q.mu.Unlock()
	for _, id := range old {
		q.remove(id)
	}
}

// list returns the messages in a state ("" = all), newest first.
func (q *queue) list(state string) []model.MailMessage {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := []model.MailMessage{}
	for _, e := range q.msg {
		st := e.State
		if st == model.MailSending {
			st = model.MailQueued
		}
		if state == "" || st == state {
			out = append(out, clone(e.MailMessage))
		}
	}
	slices.SortFunc(out, func(a, b model.MailMessage) int {
		if c := b.ReceivedAt.Compare(a.ReceivedAt); c != 0 {
			return c
		}
		return strings.Compare(b.ID, a.ID)
	})
	return out
}

func (q *queue) get(id string) (model.MailMessage, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	e, ok := q.msg[id]
	if !ok {
		return model.MailMessage{}, false
	}
	return clone(e.MailMessage), true
}

func (q *queue) counts() (queued, failed int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, e := range q.msg {
		if e.State == model.MailFailed {
			failed++
		} else {
			queued++
		}
	}
	return
}

func clone(m model.MailMessage) model.MailMessage {
	m.Recipients = slices.Clone(m.Recipients)
	return m
}

// retryDelay is how long to wait before the next attempt at a message
// that has failed `attempts` times so far (attempts >= 1).
//
// TODO(user): the retry schedule. IIS 6 retries after 10, 10, 10 and then
// every 15 minutes until the message expires (Settings → ExpireHours, 48h
// by default). Mail providers greylist first attempts for a few minutes,
// and a receiving server that is down for maintenance may be down for
// hours. Replace the fixed interval below with the policy you want.
func retryDelay(attempts int) time.Duration {
	return 15 * time.Minute
}
