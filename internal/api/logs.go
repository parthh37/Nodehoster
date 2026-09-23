package api

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/core"
	"github.com/parthh37/nodehoster/internal/logsearch"
	"github.com/parthh37/nodehoster/internal/model"
)

// Log shipping (Settings → Log shipping) and log search.

func (a *API) logShippingStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.c.LogShippingStatus())
}

func (a *API) logShippingTest(w http.ResponseWriter, r *http.Request) {
	var in model.LogTarget
	if err := decode(r, &in); err != nil {
		a.fail(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := a.c.TestLogTarget(ctx, in); err != nil {
		var te *core.TargetError
		if errors.As(err, &te) {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		a.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// maxRegex bounds a search pattern. RE2 matches in linear time whatever
// the pattern, so the length is the only limit needed.
const maxRegex = 512

// SearchResult is a page of search results, newest first.
type SearchResult struct {
	Lines        []model.LogLine `json:"lines"`
	Truncated    bool            `json:"truncated"` // stopped at the time/byte budget; older lines may match
	Cursor       string          `json:"cursor,omitempty"`
	ScannedBytes int64           `json:"scannedBytes"`
}

// searchQuery reads the parameters every search takes: q (text,
// case-insensitive, or a regular expression with regex=1), since and
// until (RFC 3339, or a duration back from now such as 15m or 24h),
// limit and cursor.
func searchQuery(r *http.Request) (logsearch.Query, error) {
	v := r.URL.Query()
	q := logsearch.Query{Limit: intParam(r, "limit", 200, 1000), Cursor: v.Get("cursor"), Budget: 3 * time.Second, MaxBytes: 256 << 20}
	text := v.Get("q")
	if text != "" {
		if isTrue(v.Get("regex")) {
			if len(text) > maxRegex {
				return q, &model.ValidationError{Field: "q", Message: "the pattern is too long (512 characters at most)"}
			}
			re, err := regexp.Compile(text)
			if err != nil {
				return q, &model.ValidationError{Field: "q", Message: "not a valid regular expression: " + strings.TrimPrefix(err.Error(), "error parsing regexp: ")}
			}
			q.Match = re.MatchString
		} else {
			lower := strings.ToLower(text)
			q.Match = func(s string) bool { return strings.Contains(strings.ToLower(s), lower) }
		}
	}
	var err error
	if q.Since, err = timeParam(v.Get("since")); err != nil {
		return q, &model.ValidationError{Field: "since", Message: err.Error()}
	}
	if q.Until, err = timeParam(v.Get("until")); err != nil {
		return q, &model.ValidationError{Field: "until", Message: err.Error()}
	}
	return q, nil
}

func isTrue(s string) bool { return s == "1" || strings.EqualFold(s, "true") }

func timeParam(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return time.Now().Add(-d), nil
	}
	return time.Time{}, errors.New("use an RFC 3339 time or a duration such as 15m or 24h")
}

func (a *API) searchFail(w http.ResponseWriter, err error) {
	if errors.Is(err, logsearch.ErrBadCursor) {
		a.fail(w, &model.ValidationError{Field: "cursor", Message: "invalid cursor; search again"})
		return
	}
	a.fail(w, err)
}

// siteLogSearch searches a site's application or access log, including
// rotated files.
func (a *API) siteLogSearch(w http.ResponseWriter, r *http.Request) {
	s := a.site(w, r)
	if s == nil {
		return
	}
	q, err := searchQuery(r)
	if err != nil {
		a.fail(w, err)
		return
	}
	var path string
	src := r.URL.Query().Get("source")
	switch src {
	case "", "app":
		path = a.c.Procs.Logs(s.ID).Path()
		stream := r.URL.Query().Get("stream")
		if stream != "" && stream != "all" && stream != "stdout" && stream != "stderr" && stream != "system" {
			a.fail(w, &model.ValidationError{Field: "stream", Message: "stdout, stderr, system or all"})
			return
		}
		q.Parse = func(line string) (time.Time, string, bool) {
			l, ok := logsearch.ParseApp(line)
			if !ok {
				return time.Time{}, line, stream == "" || stream == "all"
			}
			return l.Time, l.Text, stream == "" || stream == "all" || l.Stream == stream
		}
	case "access":
		path = a.c.Proxy.AccessLogPath(s.ID)
		q.Parse = func(line string) (time.Time, string, bool) { return logsearch.AccessTime(line), line, true }
	default:
		a.fail(w, &model.ValidationError{Field: "source", Message: "app or access"})
		return
	}
	res, err := logsearch.Search(logsearch.Files(path), q)
	if err != nil {
		a.searchFail(w, err)
		return
	}
	out := SearchResult{Lines: make([]model.LogLine, 0, len(res.Lines)), Truncated: res.Truncated, Cursor: res.Cursor, ScannedBytes: res.Scanned}
	for _, l := range res.Lines {
		if src != "access" {
			if p, ok := logsearch.ParseApp(l.Text); ok {
				out.Lines = append(out.Lines, model.LogLine{Time: p.Time, Stream: p.Stream, Instance: p.Instance, Text: p.Text})
				continue
			}
			out.Lines = append(out.Lines, model.LogLine{Time: l.Time, Stream: "system", Instance: -1, Text: l.Text})
			continue
		}
		out.Lines = append(out.Lines, model.LogLine{Time: l.Time, Stream: "access", Instance: -1, Text: l.Text})
	}
	writeJSON(w, http.StatusOK, out)
}

// serverLogSearch searches NodeHoster's own log; level is the lowest
// level wanted (debug, info, warning, error).
func (a *API) serverLogSearch(w http.ResponseWriter, r *http.Request) {
	q, err := searchQuery(r)
	if err != nil {
		a.fail(w, err)
		return
	}
	min := 0
	if lv := r.URL.Query().Get("level"); lv != "" {
		if !slices.Contains(model.LogLevels, lv) {
			a.fail(w, &model.ValidationError{Field: "level", Message: "debug, info, warning or error"})
			return
		}
		min = model.LogLevelRank(lv)
	}
	q.Parse = func(line string) (time.Time, string, bool) {
		t, level := logsearch.ServerLine(line)
		return t, line, level == "" || model.LogLevelRank(level) >= min
	}
	res, err := logsearch.Search(logsearch.Files(filepath.Join(a.c.Paths.Logs, "nodehoster.log")), q)
	if err != nil {
		a.searchFail(w, err)
		return
	}
	out := SearchResult{Lines: make([]model.LogLine, 0, len(res.Lines)), Truncated: res.Truncated, Cursor: res.Cursor, ScannedBytes: res.Scanned}
	for _, l := range res.Lines {
		_, level := logsearch.ServerLine(l.Text)
		out.Lines = append(out.Lines, model.LogLine{Time: l.Time, Stream: level, Instance: -1, Text: l.Text})
	}
	writeJSON(w, http.StatusOK, out)
}
