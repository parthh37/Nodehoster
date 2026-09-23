package tasks

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// runLog is a run's output: written to a file of limited size and
// streamed live to subscribers (the console's log viewer).
type runLog struct {
	mu        sync.Mutex
	f         *os.File
	path      string
	max       int64
	written   int64
	truncated bool
	subs      map[chan string]struct{}
	done      chan struct{}
}

func openRunLog(path string, max int64) (*runLog, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return nil, err
	}
	return &runLog{f: f, path: path, max: max, subs: map[chan string]struct{}{}, done: make(chan struct{})}, nil
}

// Write keeps the first max bytes of output. A task stuck printing in a
// loop must not fill the disk; the start of the output is usually what
// explains a failure, and the end is marked as cut.
func (l *runLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.truncated {
		return len(p), nil
	}
	b := p
	if room := l.max - l.written; int64(len(b)) > room {
		b = b[:max(room, 0)]
		l.truncated = true
	}
	l.emit(b)
	if l.truncated {
		l.emit([]byte(fmt.Sprintf("\n[%s] output truncated: a run's log is limited to %d MB\n", stamp(), l.max>>20)))
	}
	return len(p), nil
}

func (l *runLog) emit(b []byte) {
	if len(b) == 0 {
		return
	}
	n, _ := l.f.Write(b)
	l.written += int64(n)
	s := string(b)
	for ch := range l.subs {
		select {
		case ch <- s:
		default: // a slow viewer misses output rather than stalling the task
		}
	}
}

// printf writes a line of NodeHoster's own, always (even past the limit).
func (l *runLog) printf(format string, a ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.emit([]byte(fmt.Sprintf("[%s] %s\n", stamp(), fmt.Sprintf(format, a...))))
}

func stamp() string { return time.Now().Format("15:04:05") }

// subscribe adds a live subscriber and returns how many bytes of the file
// were written before it: those are not sent to it, everything after is.
func (l *runLog) subscribe() (ch chan string, offset int64, cancel func()) {
	ch = make(chan string, 256)
	l.mu.Lock()
	l.subs[ch] = struct{}{}
	offset = l.written
	l.mu.Unlock()
	return ch, offset, func() {
		l.mu.Lock()
		delete(l.subs, ch)
		l.mu.Unlock()
	}
}

// readPrefix reads the first n bytes of a file. The file only grows, so
// they do not change while it is being written to.
func readPrefix(path string, n int64) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	buf := make([]byte, n)
	m, _ := io.ReadFull(f, buf)
	return buf[:m]
}

func (l *runLog) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.f.Close()
	close(l.done)
}
