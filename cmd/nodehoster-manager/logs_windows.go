package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
	"github.com/tailscale/walk"
)

// logFollower shows a site's recent output and then streams new lines.
// Lines arrive on network goroutines and are appended in batches, so a
// chatty application cannot flood the UI thread. Each follow has its own
// buffer: goroutines of a previous site, still winding down, write only to
// theirs, which nobody reads any more.
//
// The view can be paused (lines keep arriving and show on resume, so
// reading does not fight the scrolling) and filtered to lines containing
// some text.
type logFollower struct {
	m      *manager
	site   string
	view   *walk.TextEdit
	cancel context.CancelFunc
	lines  []string // everything received, up to maxLogLines
	paused bool
	filter string // lower-cased; "" shows every line
}

type logBuffer struct {
	mu      sync.Mutex
	pending []string
}

func (b *logBuffer) push(l model.LogLine) {
	prefix := l.Time.Local().Format("15:04:05")
	if l.Instance >= 0 {
		prefix += fmt.Sprintf(" #%d", l.Instance)
	}
	if l.Stream == "stderr" || l.Stream == "system" {
		prefix += " " + l.Stream
	}
	b.mu.Lock()
	b.pending = append(b.pending, prefix+"  "+strings.TrimRight(l.Text, "\r\n"))
	b.mu.Unlock()
}

func (b *logBuffer) take() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	batch := b.pending
	b.pending = nil
	return batch
}

const maxLogLines = 3000

func (f *logFollower) follow(site string, view *walk.TextEdit, state *walk.Label) {
	if f.cancel != nil && f.site == site {
		return
	}
	f.stop()
	f.site, f.view = site, view
	f.clear()
	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel
	buf := &logBuffer{}
	state.SetText("● Loading…")
	state.SetTextColor(colorMuted)
	setState := func(s string, c walk.Color) {
		f.m.mw.Synchronize(func() {
			if ctx.Err() == nil {
				state.SetText(s)
				state.SetTextColor(c)
			}
		})
	}

	base := "/api/sites/" + url.PathEscape(site) + "/logs"
	go func() {
		var recent []model.LogLine
		if err := f.m.cl.Get(ctx, base+"?lines=500", &recent); err == nil {
			for _, l := range recent {
				buf.push(l)
			}
		}
		for ctx.Err() == nil {
			setState("● Live", colorOK)
			f.m.cl.Stream(ctx, base+"/stream", func(event string, data []byte) {
				var l model.LogLine
				if event == "log" && json.Unmarshal(data, &l) == nil {
					buf.push(l)
				}
			})
			if ctx.Err() == nil {
				setState("● Disconnected; reconnecting…", colorWarning)
				select {
				case <-ctx.Done():
				case <-time.After(3 * time.Second):
				}
			}
		}
	}()
	go func() {
		tick := time.NewTicker(250 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if batch := buf.take(); len(batch) > 0 {
					f.m.mw.Synchronize(func() {
						if ctx.Err() == nil {
							f.appendLines(batch)
						}
					})
				}
			}
		}
	}()
}

func (f *logFollower) keep(line string) bool {
	return f.filter == "" || strings.Contains(strings.ToLower(line), f.filter)
}

func (f *logFollower) shown() []string {
	if f.filter == "" {
		return f.lines
	}
	var out []string
	for _, l := range f.lines {
		if f.keep(l) {
			out = append(out, l)
		}
	}
	return out
}

func (f *logFollower) appendLines(batch []string) {
	f.lines = append(f.lines, batch...)
	if len(f.lines) > maxLogLines {
		f.lines = f.lines[len(f.lines)-maxLogLines*2/3:]
		if !f.paused {
			f.render()
		}
		return
	}
	if f.paused || f.view == nil {
		return
	}
	var add []string
	for _, l := range batch {
		if f.keep(l) {
			add = append(add, l)
		}
	}
	if len(add) > 0 {
		f.view.AppendText(strings.Join(add, "\r\n") + "\r\n")
	}
}

// render redraws the view from the lines kept.
func (f *logFollower) render() {
	if f.view == nil {
		return
	}
	lines := f.shown()
	if len(lines) == 0 {
		f.view.SetText("")
		return
	}
	f.view.SetText(strings.Join(lines, "\r\n") + "\r\n")
	f.view.SendMessage(0x00B7, 0, 0) // EM_SCROLLCARET: to the end
}

func (f *logFollower) setPaused(p bool) {
	f.paused = p
	if !p {
		f.render()
	}
}

func (f *logFollower) setFilter(text string) {
	f.filter = strings.ToLower(strings.TrimSpace(text))
	f.render()
}

// text is what the view shows, for copying.
func (f *logFollower) text() string { return strings.Join(f.shown(), "\r\n") }

// saveAs writes the lines shown to a file.
func (f *logFollower) saveAs() {
	dlg := walk.FileDialog{
		Title:    "Save the log",
		Filter:   "Log files (*.log)|*.log|Text files (*.txt)|*.txt",
		FilePath: "site-" + f.site + "-" + time.Now().Format("20060102-150405") + ".log",
	}
	if ok, _ := dlg.ShowSave(f.m.mw); !ok {
		return
	}
	path := dlg.FilePath
	if !strings.Contains(path[strings.LastIndexAny(path, `\/`)+1:], ".") {
		path += ".log"
	}
	if err := os.WriteFile(path, []byte(f.text()+"\r\n"), 0o644); err != nil {
		f.m.errorBox("Save the log", err)
		return
	}
	f.m.flashStatus("Saved the log to "+path, false)
}

func (f *logFollower) clear() {
	f.lines = nil
	if f.view != nil {
		f.view.SetText("")
	}
}

func (f *logFollower) stop() {
	if f.cancel != nil {
		f.cancel()
		f.cancel = nil
	}
	f.site = ""
}
