package procmgr

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
	"gopkg.in/natefinch/lumberjack.v2"
)

const ringSize = 2000

// LogSink collects a site's stdout/stderr: it appends to a rotating file,
// keeps the most recent lines in memory for the UI, and fans lines out to
// live subscribers (the log tail view).
type LogSink struct {
	mu   sync.Mutex
	file *lumberjack.Logger
	ring []model.LogLine
	next int
	full bool
	subs map[chan model.LogLine]struct{}

	onWrite func(model.LogLine) // set at creation, called outside mu
}

func NewLogSink(path string, maxSizeMB, maxFiles, maxAgeDays int) *LogSink {
	os.MkdirAll(filepath.Dir(path), 0o750)
	return &LogSink{
		file: &lumberjack.Logger{
			Filename:   path,
			MaxSize:    maxSizeMB,
			MaxBackups: maxFiles,
			MaxAge:     maxAgeDays,
			LocalTime:  true,
		},
		ring: make([]model.LogLine, ringSize),
		subs: map[chan model.LogLine]struct{}{},
	}
}

func (s *LogSink) Write(l model.LogLine) {
	if s.onWrite != nil {
		defer s.onWrite(l)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprintf(s.file, "%s [%d %s] %s\n", l.Time.Format("2006-01-02T15:04:05.000Z07:00"), l.Instance, l.Stream, l.Text)
	s.ring[s.next] = l
	s.next = (s.next + 1) % ringSize
	if s.next == 0 {
		s.full = true
	}
	for ch := range s.subs {
		select {
		case ch <- l:
		default:
		}
	}
}

func (s *LogSink) System(format string, a ...any) {
	s.Write(model.LogLine{Time: time.Now(), Stream: "system", Instance: -1, Text: fmt.Sprintf(format, a...)})
}

// Recent returns up to n of the latest lines, oldest first.
func (s *LogSink) Recent(n int) []model.LogLine {
	s.mu.Lock()
	defer s.mu.Unlock()
	var all []model.LogLine
	if s.full {
		all = append(append(all, s.ring[s.next:]...), s.ring[:s.next]...)
	} else {
		all = append(all, s.ring[:s.next]...)
	}
	if n > 0 && len(all) > n {
		all = all[len(all)-n:]
	}
	return all
}

func (s *LogSink) Subscribe() (<-chan model.LogLine, func()) {
	ch := make(chan model.LogLine, 256)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}
}

func (s *LogSink) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ring = make([]model.LogLine, ringSize)
	s.next, s.full = 0, false
	if err := s.file.Close(); err != nil {
		return err
	}
	return os.Truncate(s.file.Filename, 0)
}

func (s *LogSink) Path() string { return s.file.Filename }

func (s *LogSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.file.Close()
}
