package procmgr

import (
	"errors"
	"math/rand/v2"
	"net"
	"strconv"
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

func portFree(port int) bool {
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	l.Close()
	return true
}
