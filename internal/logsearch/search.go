// Package logsearch searches log files newest first: the current file
// read backwards, then lumberjack's rotated copies (plain or .gz), with a
// time and byte budget so one search cannot tie up the server, and a
// cursor to carry on where it stopped.
package logsearch

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Files returns a log file and its rotated copies, newest first. Rotated
// copies are named like lumberjack's: app-2006-01-02T15-04-05.000.log,
// optionally .gz.
func Files(current string) []string {
	dir, base := filepath.Split(current)
	ext := filepath.Ext(base)
	prefix := strings.TrimSuffix(base, ext) + "-"
	type backup struct {
		path string
		t    time.Time
	}
	var list []backup
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasPrefix(n, prefix) {
			continue
		}
		stamp := strings.TrimPrefix(n, prefix)
		switch {
		case strings.HasSuffix(stamp, ext+".gz"):
			stamp = strings.TrimSuffix(stamp, ext+".gz")
		case strings.HasSuffix(stamp, ext):
			stamp = strings.TrimSuffix(stamp, ext)
		default:
			continue
		}
		t, err := time.ParseInLocation("2006-01-02T15-04-05.000", stamp, time.Local)
		if err != nil {
			continue
		}
		list = append(list, backup{filepath.Join(dir, n), t})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].t.After(list[j].t) })
	out := []string{current}
	for _, b := range list {
		out = append(out, b.path)
	}
	return out
}

// Query is a search. Parse reads a line's time (zero if it has none) and
// the text Match is applied to (the message, without the line's prefix),
// and reports whether the line is wanted at all (a stream filter); nil
// keeps every line and matches the whole of it.
type Query struct {
	Match    func(text string) bool
	Parse    func(line string) (t time.Time, text string, keep bool)
	Since    time.Time // zero = no lower bound
	Until    time.Time // zero = no upper bound
	Limit    int
	Cursor   string
	Budget   time.Duration // wall time
	MaxBytes int64         // bytes read
}

// Line is a match.
type Line struct {
	Text string
	Time time.Time
	File string // base name of the file it is in
}

// Result: Truncated means the search stopped at its budget before the end
// of the logs (older matches may exist); Cursor continues the search after
// the last line returned, whether it stopped at the limit or the budget.
type Result struct {
	Lines     []Line
	Truncated bool
	Cursor    string
	Scanned   int64
}

// ErrBadCursor is returned for a cursor that is not one of ours.
var ErrBadCursor = errors.New("invalid cursor")

type cursor struct {
	File   string `json:"f"`
	Offset int64  `json:"o,omitempty"` // plain files: continue before this offset
	// .gz files: only the matches before this one, counted from the start
	// of the file, remain. Counting from the start (not the number already
	// returned, from the end) keeps what a page holds in memory to the
	// page itself, whatever a cursor says, and lets a page stop reading
	// there.
	Before int `json:"b,omitempty"`
}

func (c cursor) encode() string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(s string) (cursor, error) {
	var c cursor
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || json.Unmarshal(b, &c) != nil || c.File == "" || c.Offset < 0 || c.Before < 0 {
		return c, ErrBadCursor
	}
	return c, nil
}

type search struct {
	q        Query
	res      Result
	deadline time.Time
	done     bool // reached lines older than Since: nothing older can match
}

func (s *search) overBudget() bool {
	return time.Now().After(s.deadline) || (s.q.MaxBytes > 0 && s.res.Scanned >= s.q.MaxBytes)
}

// consider checks one line; it returns false to stop reading this file.
func (s *search) consider(text, file string) {
	var t time.Time
	match := text
	if s.q.Parse != nil {
		var keep bool
		if t, match, keep = s.q.Parse(text); !keep {
			return
		}
	}
	if !t.IsZero() {
		if !s.q.Since.IsZero() && t.Before(s.q.Since) {
			s.done = true
			return
		}
		if !s.q.Until.IsZero() && t.After(s.q.Until) {
			return
		}
	}
	if s.q.Match != nil && !s.q.Match(match) {
		return
	}
	s.res.Lines = append(s.res.Lines, Line{Text: text, Time: t, File: file})
}

// Search runs q over files (newest first, as Files returns them).
func Search(files []string, q Query) (Result, error) {
	if q.Limit <= 0 {
		q.Limit = 200
	}
	if q.Budget <= 0 {
		q.Budget = 3 * time.Second
	}
	s := &search{q: q, deadline: time.Now().Add(q.Budget)}
	s.res.Lines = []Line{}
	start, cur := 0, cursor{}
	if q.Cursor != "" {
		c, err := decodeCursor(q.Cursor)
		if err != nil {
			return s.res, err
		}
		start = -1
		for i, f := range files {
			if filepath.Base(f) == c.File {
				start, cur = i, c
			}
		}
		if start < 0 {
			return s.res, nil // rotated away and deleted since
		}
		// The current file was rotated since the cursor was made: what
		// it held is now the newest rotated copy.
		if start == 0 && len(files) > 1 {
			if st, err := os.Stat(files[0]); err == nil && st.Size() < c.Offset {
				start, cur.File = 1, filepath.Base(files[1])
			}
		}
	}
	for i := start; i < len(files); i++ {
		f := files[i]
		var next *cursor
		var err error
		if strings.HasSuffix(f, ".gz") {
			before := 0
			if i == start && cur.File != "" {
				before = cur.Before
			}
			next, err = s.gz(f, before, i == start)
		} else {
			off := int64(-1)
			if i == start && cur.File != "" && cur.Offset > 0 {
				off = cur.Offset
			}
			next, err = s.plain(f, off)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return s.res, err
		}
		if next != nil && !strings.HasSuffix(f, ".gz") && next.Offset == 0 {
			// Stopped exactly at the start of the file: go on with the next.
			if i+1 == len(files) {
				s.res.Truncated = false
				return s.res, nil
			}
			next = &cursor{File: filepath.Base(files[i+1])}
		}
		if next != nil {
			s.res.Cursor = next.encode()
			return s.res, nil
		}
		if s.done {
			return s.res, nil
		}
		if i+1 < len(files) && s.overBudget() {
			s.res.Truncated = true
			s.res.Cursor = cursor{File: filepath.Base(files[i+1])}.encode()
			return s.res, nil
		}
	}
	return s.res, nil
}

// plain reads a file backwards from end (-1 = its end). It returns a
// cursor when it stops before the start of the file.
func (s *search) plain(path string, end int64) (*cursor, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if end < 0 || end > st.Size() {
		end = st.Size()
	}
	name := filepath.Base(path)
	const chunk = 64 << 10
	pos := end      // bytes before pos are unread
	var tail []byte // the start of a line whose beginning is not read yet
	lineEnd := end  // offset just past the line in tail
	for {
		if len(s.res.Lines) >= s.q.Limit {
			return &cursor{File: name, Offset: lineEnd}, nil
		}
		if s.done {
			return nil, nil
		}
		if s.overBudget() {
			s.res.Truncated = true
			return &cursor{File: name, Offset: lineEnd}, nil
		}
		if pos == 0 {
			if len(tail) > 0 {
				s.consider(strings.TrimRight(string(tail), "\r"), name)
				tail = nil
				lineEnd = 0
				continue
			}
			return nil, nil
		}
		n := int64(chunk)
		if pos < n {
			n = pos
		}
		buf := make([]byte, n)
		if _, err := f.ReadAt(buf, pos-n); err != nil && err != io.EOF {
			return nil, err
		}
		pos -= n
		s.res.Scanned += n
		data := append(buf, tail...)
		// Complete lines are those after a newline; the first piece may
		// continue before pos.
		for {
			i := bytes.LastIndexByte(data, '\n')
			if i < 0 {
				break
			}
			line := data[i+1:]
			start := pos + int64(i) + 1
			if len(line) > 0 {
				s.consider(strings.TrimRight(string(line), "\r"), name)
			}
			data = data[:i]
			lineEnd = start - 1
			if len(s.res.Lines) >= s.q.Limit || s.done {
				// Lines before the newline at lineEnd remain.
				if s.done {
					return nil, nil
				}
				return &cursor{File: name, Offset: lineEnd}, nil
			}
		}
		if len(data) > maxLine {
			data = data[len(data)-maxLine:] // an absurdly long line is cut
		}
		tail = data
	}
}

// maxLine is the longest line a search keeps; the rest of an absurdly
// long line is cut.
const maxLine = 1 << 20

// gz scans a compressed file forwards (gzip cannot be read backwards),
// keeping the newest matches before the before'th (0 = all of them).
//
// The newest matches are at the end, so a page cannot return any of them
// before it has decompressed the file that far, and cannot resume a
// decompression another page started. When the budget runs out inside the
// file, the page ends there, truncated, without this file's lines, and
// the next page starts over at this file with a whole budget of its own.
// A page that starts at the file (first, true) already has that whole
// budget: it reads the file through whatever it costs, as nothing would
// ever read it otherwise (lumberjack keeps a rotated file to the log size
// setting).
func (s *search) gz(path string, before int, first bool) (*cursor, error) {
	name := filepath.Base(path)
	want := s.q.Limit - len(s.res.Lines)
	if want <= 0 {
		return &cursor{File: name, Before: before}, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	ring := make([]Line, want) // the newest matches: match n is at n % want
	n := 0                     // matches read, oldest first
	oldestTooOld := false
	sub := &search{q: s.q, deadline: s.deadline}
	sub.q.Since = time.Time{} // checked below: the file is in time order, oldest first
	br := bufio.NewReaderSize(zr, 64<<10)
	var long []byte // a line longer than br's buffer, so far
	for before <= 0 || n < before {
		if !first && s.overBudget() {
			s.res.Truncated = true
			return &cursor{File: name, Before: before}, nil
		}
		chunk, err := br.ReadSlice('\n')
		s.res.Scanned += int64(len(chunk))
		if errors.Is(err, bufio.ErrBufferFull) {
			long = append(long, chunk[:min(len(chunk), maxLine-len(long))]...)
			continue
		}
		line := chunk
		if long != nil {
			line = append(long, chunk[:min(len(chunk), maxLine-len(long))]...)
			long = nil
		}
		if len(line) > 0 {
			text := strings.TrimRight(string(line), "\r\n")
			sub.res.Lines = sub.res.Lines[:0]
			sub.consider(text, name)
			if len(sub.res.Lines) == 1 {
				l := sub.res.Lines[0]
				if !s.q.Since.IsZero() && !l.Time.IsZero() && l.Time.Before(s.q.Since) {
					oldestTooOld = true
				} else {
					ring[n%want] = l
					n++
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	returned := min(n, want)
	for k := 1; k <= returned; k++ {
		s.res.Lines = append(s.res.Lines, ring[(n-k)%want])
	}
	if rest := n - returned; rest > 0 {
		return &cursor{File: name, Before: rest}, nil
	}
	if oldestTooOld {
		s.done = true
	}
	return nil, nil
}
