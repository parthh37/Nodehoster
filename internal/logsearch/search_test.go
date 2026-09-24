package logsearch

import (
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

var base = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

// appLine is a line as procmgr.LogSink writes it; n is its minute.
func appLine(n int, stream, text string) string {
	return fmt.Sprintf("%s [0 %s] %s\n", base.Add(time.Duration(n)*time.Minute).Format("2006-01-02T15:04:05.000Z07:00"), stream, text)
}

// logs writes 300 lines, minute 0 to 299: the oldest 100 in a gzipped
// rotated file, the next 100 in a plain rotated file, the newest 100 in
// app.log. Every 10th line is on stderr.
func logs(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name string, from int, gz bool) {
		var b strings.Builder
		for i := from; i < from+100; i++ {
			stream := "stdout"
			if i%10 == 0 {
				stream = "stderr"
			}
			b.WriteString(appLine(i, stream, fmt.Sprintf("request %d done", i)))
		}
		p := filepath.Join(dir, name)
		if !gz {
			os.WriteFile(p, []byte(b.String()), 0o640)
			return
		}
		f, _ := os.Create(p)
		zw := gzip.NewWriter(f)
		zw.Write([]byte(b.String()))
		zw.Close()
		f.Close()
	}
	write("app-2026-03-01T01-40-00.000.log.gz", 0, true)
	write("app-2026-03-01T03-20-00.000.log", 100, false)
	write("app.log", 200, false)
	os.WriteFile(filepath.Join(dir, "app-notes.txt"), []byte("x"), 0o640)
	os.WriteFile(filepath.Join(dir, "access.log"), []byte("x"), 0o640)
	return filepath.Join(dir, "app.log")
}

func parse(stream string) func(string) (time.Time, string, bool) {
	return func(line string) (time.Time, string, bool) {
		l, ok := ParseApp(line)
		if !ok {
			return time.Time{}, line, false
		}
		return l.Time, l.Text, stream == "" || l.Stream == stream
	}
}

func minutes(res Result) []int {
	var out []int
	for _, l := range res.Lines {
		out = append(out, int(l.Time.Sub(base)/time.Minute))
	}
	return out
}

func TestFiles(t *testing.T) {
	t.Parallel()
	files := Files(logs(t))
	var names []string
	for _, f := range files {
		names = append(names, filepath.Base(f))
	}
	if strings.Join(names, " ") != "app.log app-2026-03-01T03-20-00.000.log app-2026-03-01T01-40-00.000.log.gz" {
		t.Errorf("Files = %v", names)
	}
}

func TestSearchPagesNewestFirstAcrossRotatedAndGzipFiles(t *testing.T) {
	t.Parallel()
	files := Files(logs(t))
	q := Query{Parse: parse(""), Limit: 70}
	var all []int
	for page := 0; page < 10; page++ {
		res, err := Search(files, q)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, minutes(res)...)
		if res.Truncated {
			t.Fatal("truncated without a budget problem")
		}
		if res.Cursor == "" {
			break
		}
		q.Cursor = res.Cursor
	}
	if len(all) != 300 {
		t.Fatalf("got %d lines", len(all))
	}
	for i, m := range all {
		if m != 299-i {
			t.Fatalf("line %d is minute %d, want %d (newest first, no gaps or repeats)", i, m, 299-i)
		}
	}
}

func TestSearchFilters(t *testing.T) {
	t.Parallel()
	files := Files(logs(t))
	lower := func(s string) func(string) bool {
		return func(x string) bool { return strings.Contains(strings.ToLower(x), strings.ToLower(s)) }
	}
	for _, tc := range []struct {
		name string
		q    Query
		want []int
	}{
		{"substring, case-insensitive", Query{Parse: parse(""), Match: lower("REQUEST 15")}, []int{159, 158, 157, 156, 155, 154, 153, 152, 151, 150, 15}},
		{"regex", Query{Parse: parse(""), Match: regexp.MustCompile(`^request (5|250) `).MatchString}, []int{250, 5}},
		{"stream", Query{Parse: parse("stderr"), Match: lower("request 2")}, []int{290, 280, 270, 260, 250, 240, 230, 220, 210, 200, 20}},
		{"time range", Query{Parse: parse(""), Since: base.Add(95 * time.Minute), Until: base.Add(105 * time.Minute)}, []int{105, 104, 103, 102, 101, 100, 99, 98, 97, 96, 95}},
		{"since inside the gz file", Query{Parse: parse("stderr"), Since: base.Add(75 * time.Minute), Until: base.Add(120 * time.Minute)}, []int{120, 110, 100, 90, 80}},
		{"limit", Query{Parse: parse(""), Limit: 3}, []int{299, 298, 297}},
	} {
		res, err := Search(files, tc.q)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := minutes(res); fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestSearchPagesInsideAGzipFile(t *testing.T) {
	t.Parallel()
	files := Files(logs(t))
	q := Query{Parse: parse(""), Match: func(s string) bool { return strings.HasPrefix(s, "request 1") || strings.HasPrefix(s, "request 2") }, Limit: 4, Since: base, Until: base.Add(29 * time.Minute)}
	var all []int
	for i := 0; i < 20; i++ {
		res, err := Search(files, q)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, minutes(res)...)
		if res.Cursor == "" {
			break
		}
		q.Cursor = res.Cursor
	}
	want := []int{29, 28, 27, 26, 25, 24, 23, 22, 21, 20, 19, 18, 17, 16, 15, 14, 13, 12, 11, 10, 2, 1}
	if fmt.Sprint(all) != fmt.Sprint(want) {
		t.Errorf("got %v\nwant %v", all, want)
	}
}

func TestSearchBudget(t *testing.T) {
	t.Parallel()
	files := Files(logs(t))
	// A byte budget smaller than one file: the search stops early, says
	// so, and the cursor carries on without losing or repeating lines.
	q := Query{Parse: parse(""), MaxBytes: 1500, Limit: 1000}
	var all []int
	truncated := 0
	for i := 0; i < 1000; i++ {
		res, err := Search(files, q)
		if err != nil {
			t.Fatal(err)
		}
		if res.Truncated {
			truncated++
		}
		all = append(all, minutes(res)...)
		if res.Cursor == "" {
			break
		}
		q.Cursor = res.Cursor
	}
	if truncated == 0 || len(all) != 300 || all[0] != 299 || all[299] != 0 {
		t.Errorf("truncated %d times, %d lines (%v…)", truncated, len(all), all[:min(5, len(all))])
	}
	for i, m := range all {
		if m != 299-i {
			t.Fatalf("line %d is minute %d", i, m)
		}
	}
	if _, err := Search(files, Query{Cursor: "garbage!"}); err != ErrBadCursor {
		t.Errorf("bad cursor: %v", err)
	}
}

// gzLogs writes app.log with lines 200 to 204 and two gzipped rotated
// files, lines 100 to 199 and 0 to 99, so that a page reading app.log
// goes on into a .gz file.
func gzLogs(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name string, from, to int, gz bool) {
		var b strings.Builder
		for i := from; i <= to; i++ {
			b.WriteString(appLine(i, "stdout", fmt.Sprintf("request %d done", i)))
		}
		p := filepath.Join(dir, name)
		if !gz {
			os.WriteFile(p, []byte(b.String()), 0o640)
			return
		}
		f, _ := os.Create(p)
		zw := gzip.NewWriter(f)
		zw.Write([]byte(b.String()))
		zw.Close()
		f.Close()
	}
	write("app-2026-03-01T01-40-00.000.log.gz", 0, 99, true)
	write("app-2026-03-01T03-20-00.000.log.gz", 100, 199, true)
	write("app.log", 200, 204, false)
	return filepath.Join(dir, "app.log")
}

// TestSearchBudgetInsideGzipFiles: the byte and time budgets stop a
// search inside a compressed file too, not only between files, and paging
// on from there loses and repeats nothing.
func TestSearchBudgetInsideGzipFiles(t *testing.T) {
	t.Parallel()
	files := Files(gzLogs(t))
	gz := filepath.Base(files[1])
	const lineLen = 60

	res, err := Search(files, Query{Parse: parse(""), MaxBytes: 1500, Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated || res.Scanned > 1500+lineLen || fmt.Sprint(minutes(res)) != "[204 203 202 201 200]" {
		t.Errorf("byte budget: truncated %v after %d bytes, lines %v", res.Truncated, res.Scanned, minutes(res))
	}
	if c, err := decodeCursor(res.Cursor); err != nil || c.File != gz || c.Before != 0 {
		t.Errorf("byte budget: cursor %+v %v, want the start of %s over", c, err, gz)
	}

	// A slow match: reading the whole file would take a second.
	slow := func(string) bool { time.Sleep(10 * time.Millisecond); return true }
	began := time.Now()
	res, err = Search(files, Query{Parse: parse(""), Match: slow, Budget: 150 * time.Millisecond, Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(began); !res.Truncated || took > 600*time.Millisecond || len(res.Lines) != 5 {
		t.Errorf("time budget: truncated %v after %v, %d lines", res.Truncated, took, len(res.Lines))
	}

	for _, limit := range []int{1000, 30, 7} {
		q := Query{Parse: parse(""), MaxBytes: 1500, Limit: limit}
		var all []int
		for page := 0; page < 100; page++ {
			res, err := Search(files, q)
			if err != nil {
				t.Fatal(err)
			}
			all = append(all, minutes(res)...)
			if res.Cursor == "" {
				break
			}
			q.Cursor = res.Cursor
		}
		if len(all) != 205 {
			t.Fatalf("limit %d: %d lines", limit, len(all))
		}
		for i, m := range all {
			if want := 204 - i; m != want {
				t.Fatalf("limit %d: line %d is minute %d, want %d (newest first, no gaps or repeats)", limit, i, m, want)
			}
		}
	}
}

// A huge line in a .gz file is cut to 1 MiB, as in a plain file, not
// read whole into memory.
func TestSearchLongGzipLine(t *testing.T) {
	t.Parallel()
	const maxLine = 1 << 20
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "app.log"), nil, 0o640)
	f, _ := os.Create(filepath.Join(dir, "app-2026-03-01T01-40-00.000.log.gz"))
	zw := gzip.NewWriter(f)
	zw.Write([]byte(appLine(1, "stdout", "before")))
	zw.Write([]byte(strings.Repeat("x", 3*maxLine) + "\n"))
	zw.Write([]byte(appLine(2, "stdout", "after")))
	zw.Close()
	f.Close()
	res, err := Search(Files(filepath.Join(dir, "app.log")), Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Lines) != 3 || len(res.Lines[1].Text) != maxLine || !strings.HasSuffix(res.Lines[0].Text, "after") || !strings.HasSuffix(res.Lines[2].Text, "before") {
		t.Errorf("got %d lines", len(res.Lines))
	}
}

func TestSearchFollowsRotation(t *testing.T) {
	t.Parallel()
	p := logs(t)
	files := Files(p)
	res, _ := Search(files, Query{Parse: parse(""), Limit: 10})
	// app.log rotates: its content moves to a new backup, app.log is
	// started again.
	dir := filepath.Dir(p)
	os.Rename(p, filepath.Join(dir, "app-2026-03-01T05-00-00.000.log"))
	os.WriteFile(p, []byte(appLine(300, "stdout", "request 300 done")), 0o640)
	res, err := Search(Files(p), Query{Parse: parse(""), Limit: 5, Cursor: res.Cursor})
	if err != nil {
		t.Fatal(err)
	}
	if got := minutes(res); fmt.Sprint(got) != "[289 288 287 286 285]" {
		t.Errorf("after rotation: %v", got)
	}
}

func TestParsers(t *testing.T) {
	t.Parallel()
	l, ok := ParseApp("2026-03-01T02:30:05.123+01:00 [2 stderr] Error: boom")
	if !ok || l.Instance != 2 || l.Stream != "stderr" || l.Text != "Error: boom" || l.Time.Hour() != 2 {
		t.Errorf("ParseApp = %+v %v", l, ok)
	}
	if l, ok := ParseApp("2026-03-01T02:30:05.123Z [-1 system]"); !ok || l.Instance != -1 || l.Text != "" {
		t.Errorf("ParseApp empty = %+v %v", l, ok)
	}
	if _, ok := ParseApp("  at Object.<anonymous>"); ok {
		t.Error("a continuation line parsed")
	}
	at := AccessTime(`10.0.0.1 - - [01/Mar/2026:02:30:05 +0000] "GET / HTTP/1.1" 200 5 "-" "curl" x 1.0ms`)
	if !at.Equal(time.Date(2026, 3, 1, 2, 30, 5, 0, time.UTC)) {
		t.Errorf("AccessTime = %v", at)
	}
	st, lvl := ServerLine(`time=2026-03-01T02:30:05.123+05:30 level=WARN msg="x level=ERROR"`)
	if lvl != "warning" || st.IsZero() {
		t.Errorf("ServerLine = %v %q", st, lvl)
	}
}

// A crafted cursor into a .gz file must not make a search allocate in
// proportion to a number it carries (a site viewer can send any cursor).
// Not parallel: it measures the process's allocations.
func TestSearchCraftedGzipCursor(t *testing.T) {
	files := Files(logs(t))
	gz := filepath.Base(files[2])
	for _, raw := range []string{
		`{"f":"` + gz + `","s":4194304}`,
		`{"f":"` + gz + `","b":1099511627776}`,
	} {
		cur := base64.RawURLEncoding.EncodeToString([]byte(raw))
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		res, err := Search(files, Query{Parse: parse(""), Limit: 3, Cursor: cur})
		runtime.ReadMemStats(&after)
		if err != nil && err != ErrBadCursor {
			t.Errorf("%s: %v", raw, err)
		}
		if n := after.TotalAlloc - before.TotalAlloc; n > 8<<20 {
			t.Errorf("%s: the search allocated %d MiB", raw, n>>20)
		}
		if err == nil && fmt.Sprint(minutes(res)) != "[99 98 97]" {
			t.Errorf("%s: %v", raw, minutes(res))
		}
	}
}
