package logsearch

import (
	"strconv"
	"strings"
	"time"
)

// AppLine is a line of a site's app.log, as procmgr.LogSink writes it:
// "2006-01-02T15:04:05.000Z07:00 [<instance> <stream>] <text>".
type AppLine struct {
	Time     time.Time
	Instance int
	Stream   string
	Text     string
}

func ParseApp(line string) (AppLine, bool) {
	ts, rest, ok := strings.Cut(line, " [")
	if !ok {
		return AppLine{}, false
	}
	t, err := time.Parse("2006-01-02T15:04:05.000Z07:00", ts)
	if err != nil {
		return AppLine{}, false
	}
	head, text, ok := strings.Cut(rest, "] ")
	if !ok {
		head, ok = strings.CutSuffix(rest, "]")
		if !ok {
			return AppLine{}, false
		}
	}
	inst, stream, ok := strings.Cut(head, " ")
	if !ok {
		return AppLine{}, false
	}
	n, err := strconv.Atoi(inst)
	if err != nil {
		return AppLine{}, false
	}
	return AppLine{Time: t, Instance: n, Stream: stream, Text: text}, true
}

// AccessTime reads the time of an access log line (combined format:
// `ip - user [02/Jan/2006:15:04:05 -0700] "GET …"`).
func AccessTime(line string) time.Time {
	i := strings.IndexByte(line, '[')
	if i < 0 {
		return time.Time{}
	}
	j := strings.IndexByte(line[i:], ']')
	if j < 0 {
		return time.Time{}
	}
	t, _ := time.Parse("02/Jan/2006:15:04:05 -0700", line[i+1:i+j])
	return t
}

// ServerLine reads the time and level of a server log line (slog's text
// format: `time=… level=INFO msg=…`). The level is lowercase: debug,
// info, warning, error.
func ServerLine(line string) (time.Time, string) {
	var t time.Time
	if v, ok := field(line, "time="); ok {
		t, _ = time.Parse(time.RFC3339Nano, v)
	}
	level := ""
	if v, ok := field(line, "level="); ok {
		switch {
		case strings.HasPrefix(v, "ERROR"):
			level = "error"
		case strings.HasPrefix(v, "WARN"):
			level = "warning"
		case strings.HasPrefix(v, "INFO"):
			level = "info"
		case strings.HasPrefix(v, "DEBUG"):
			level = "debug"
		}
	}
	return t, level
}

func field(line, key string) (string, bool) {
	i := strings.Index(line, key)
	if i < 0 || (i > 0 && line[i-1] != ' ') {
		return "", false
	}
	v := line[i+len(key):]
	if j := strings.IndexByte(v, ' '); j >= 0 {
		v = v[:j]
	}
	return v, true
}
