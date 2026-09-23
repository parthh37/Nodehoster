package procmgr

import (
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// fileWatcher recycles a site when its files change, like iisnode's
// watchedFiles. Changes are debounced so a deployment copying hundreds of
// files causes one recycle, not hundreds.
type fileWatcher struct {
	w    *fsnotify.Watcher
	stop chan struct{}
	once sync.Once
}

const maxWatchedDirs = 2000

func (a *App) startWatcher() {
	site := a.config()
	if !site.Node.WatchFiles {
		return
	}
	root := a.workDir(site)
	ignore := site.Node.WatchIgnore
	w, err := fsnotify.NewWatcher()
	if err != nil {
		a.logs.System("file watcher unavailable: %v", err)
		return
	}
	count := 0
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if p != root && slices.Contains(ignore, d.Name()) {
			return filepath.SkipDir
		}
		if count >= maxWatchedDirs {
			return filepath.SkipAll
		}
		if w.Add(p) == nil {
			count++
		}
		return nil
	})
	fw := &fileWatcher{w: w, stop: make(chan struct{})}
	a.mu.Lock()
	a.watcher = fw
	a.mu.Unlock()
	a.logs.System("watching %d folder(s) for changes", count)

	go func() {
		var timer *time.Timer
		fire := make(chan struct{}, 1)
		for {
			select {
			case <-fw.stop:
				if timer != nil {
					timer.Stop()
				}
				return
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				rel, _ := filepath.Rel(root, ev.Name)
				if ignored(rel, ignore) {
					continue
				}
				if ev.Has(fsnotify.Create) {
					// New folders need a watch of their own.
					w.Add(ev.Name)
				}
				if timer != nil {
					timer.Stop()
				}
				timer = time.AfterFunc(2*time.Second, func() {
					select {
					case fire <- struct{}{}:
					default:
					}
				})
			case <-fire:
				go a.recycle("files changed")
			case <-w.Errors:
			}
		}
	}()
}

func ignored(rel string, ignore []string) bool {
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if slices.Contains(ignore, part) {
			return true
		}
	}
	return strings.HasSuffix(rel, ".log")
}

func (a *App) stopWatcher() {
	a.mu.Lock()
	fw := a.watcher
	a.watcher = nil
	a.mu.Unlock()
	if fw != nil {
		fw.once.Do(func() {
			close(fw.stop)
			fw.w.Close()
		})
	}
}
