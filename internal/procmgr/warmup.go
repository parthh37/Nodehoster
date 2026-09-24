package procmgr

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

// warmup is one warm-up of a slot's instances before a swap.
type warmup struct {
	addrs   []string // the instances, host:port
	paths   []string
	accept  []model.StatusRange
	host    string // Host header: production's host name
	proto   string // X-Forwarded-Proto: production's binding's
	timeout time.Duration
	logf    func(format string, a ...any)
}

const (
	warmupRequestTimeout = 30 * time.Second
	warmupRetryDelay     = time.Second
)

// warmUp requests every path on every instance (instances in parallel,
// paths in order) until each answers an accepted status, retrying a
// second after any other answer or error, like Azure's swap warm-up. It
// fails when the timeout (for the whole warm-up) expires first, naming
// what did not answer and how it last answered.
func warmUp(ctx context.Context, client *http.Client, w warmup) error {
	ctx, cancel := context.WithTimeout(ctx, w.timeout)
	defer cancel()
	var logMu sync.Mutex // instances log from their own goroutines
	logf := w.logf
	w.logf = func(format string, a ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		logf(format, a...)
	}
	errs := make([]error, len(w.addrs))
	var wg sync.WaitGroup
	for i, addr := range w.addrs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, p := range w.paths {
				if err := warmPath(ctx, client, w, addr, p); err != nil {
					errs[i] = err
					return
				}
			}
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

func warmPath(ctx context.Context, client *http.Client, w warmup, addr, path string) error {
	start := time.Now()
	last := ""
	for attempt := 1; ; attempt++ {
		code, err := warmRequest(ctx, client, w, addr, path)
		switch {
		case err == nil && model.StatusAccepted(code, w.accept):
			w.logf("warm-up: %s%s answered %d after %s", addr, path, code, time.Since(start).Round(10*time.Millisecond))
			return nil
		case err == nil:
			last = fmt.Sprintf("HTTP %d", code)
		case ctx.Err() == nil:
			last = err.Error()
		}
		if attempt == 1 || attempt%10 == 0 {
			w.logf("warm-up: %s%s not ready yet (%s)", addr, path, last)
		}
		select {
		case <-ctx.Done():
			if last == "" {
				last = "no answer"
			}
			return fmt.Errorf("%s%s did not answer an accepted status within %s (last: %s)", addr, path, w.timeout, last)
		case <-time.After(warmupRetryDelay):
		}
	}
}

func warmRequest(ctx context.Context, client *http.Client, w warmup, addr, path string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, warmupRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+path, nil)
	if err != nil {
		return 0, err
	}
	if w.host != "" {
		req.Host = w.host
		req.Header.Set("X-Forwarded-Host", w.host)
	}
	if w.proto != "" {
		req.Header.Set("X-Forwarded-Proto", w.proto)
	}
	req.Header.Set("User-Agent", "NodeHoster-Warmup")
	req.Header.Set("X-NodeHoster-Warmup", "1")
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	return resp.StatusCode, nil
}
