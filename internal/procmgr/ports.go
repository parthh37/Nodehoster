package procmgr

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"strconv"
	"strings"
	"sync"
)

// portAllocator hands out loopback ports for instances from a configured
// range, the way iisnode hands each worker a named pipe.
type portAllocator struct {
	mu    sync.Mutex
	start int
	end   int
	used  map[int]bool
}

func newPortAllocator(start, end int) *portAllocator {
	return &portAllocator{start: start, end: end, used: map[int]bool{}}
}

func (p *portAllocator) setRange(start, end int) {
	p.mu.Lock()
	p.start, p.end = start, end
	p.mu.Unlock()
}

func (p *portAllocator) allocate() (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := p.end - p.start + 1
	if n <= 0 {
		return 0, errors.New("port range is empty")
	}
	off := rand.IntN(n)
	for i := 0; i < n; i++ {
		port := p.start + (off+i)%n
		if p.used[port] || !portFree(port) {
			continue
		}
		p.used[port] = true
		return port, nil
	}
	return 0, errors.New("no free port left in the configured range")
}

func (p *portAllocator) release(port int) {
	p.mu.Lock()
	delete(p.used, port)
	p.mu.Unlock()
}

// portFree reports whether the port can be bound on loopback right now. The
// answer is only a hint: the port is handed to the application, which binds
// it some time later, and any socket on the host can take it in between.
func portFree(port int) bool {
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	l.Close()
	return true
}

// portLostError is a start that failed because another process took the
// instance's port between allocation and the application's listen. It says
// nothing about the application, so spawn retries on another port instead
// of counting it as a crash.
type portLostError struct {
	port int
	err  error
}

func (e *portLostError) Error() string {
	return fmt.Sprintf("port %d was taken by another process: %v", e.port, e.err)
}

func (e *portLostError) Unwrap() error { return e.err }

// mentionsAddrInUse reports whether an output line is an application's
// report that listening on port failed: Node's and Bun's EADDRINUSE
// ("Error: listen EADDRINUSE: address already in use :::41000"), Python's
// and Kestrel's "address already in use" ("... on address ('127.0.0.1',
// 41000): address already in use", "Failed to bind to address
// http://127.0.0.1:41000: address already in use") and Windows' WSAEADDRINUSE
// text ("only one usage of each socket address"). The port must appear as a
// number of its own.
func mentionsAddrInUse(line, port string) bool {
	lower := strings.ToLower(line)
	if !strings.Contains(line, "EADDRINUSE") && !strings.Contains(lower, "address already in use") &&
		!strings.Contains(lower, "only one usage of each socket address") {
		return false
	}
	digit := func(c byte) bool { return c >= '0' && c <= '9' }
	for rest := line; ; {
		i := strings.Index(rest, port)
		if i < 0 {
			return false
		}
		end := i + len(port)
		if (i == 0 || !digit(rest[i-1])) && (end == len(rest) || !digit(rest[end])) {
			return true
		}
		rest = rest[end:]
	}
}

// ephemeralOverlap describes how the instance port range overlaps the range
// the OS takes local ports for outgoing connections from, or returns "" when
// they are disjoint or the OS range is unknown. Ports in the overlap can be
// taken by any connection the host makes.
func ephemeralOverlap(start, end int) string {
	lo, hi, ok := ephemeralRange()
	if !ok || end < lo || start > hi {
		return ""
	}
	return fmt.Sprintf("instance ports %d-%d overlap the ephemeral port range %d-%d (%s)", start, end, lo, hi, ephemeralSource)
}
