package procmgr

import (
	"bytes"
	"encoding/json"
	"net"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
	"github.com/shirou/gopsutil/v4/process"
)

// Backend is what the reverse proxy sees of an instance: an address and two
// counters. The proxy increments Active around every request, which is what
// lets a recycle drain an instance before stopping it.
type Backend struct {
	Addr     string
	Active   atomic.Int64
	Requests atomic.Int64
}

// Instance is one running process of a site.
type Instance struct {
	app   *App
	index int
	port  int
	token string

	cmd       *exec.Cmd
	os        *osProc
	pid       int
	startedAt time.Time
	backend   *Backend

	exited   chan struct{}
	exitCode int
	// addrInUse is set when the application reported that its port was
	// already in use: it never owned the port, whatever answered on it.
	addrInUse *atomic.Bool

	mu          sync.Mutex
	state       string // starting | ready | unhealthy | stopping | exited
	healthy     bool
	healthFails int
	lastHealth  time.Time
	cpu         float64
	mem         uint64
	procs       map[int32]*process.Process

	agentConn net.Conn
	agentNode string
	heapUsed  uint64
	heapTotal uint64
	lag       float64
	listening bool
}

func (i *Instance) setState(s string) {
	i.mu.Lock()
	i.state = s
	i.mu.Unlock()
}

func (i *Instance) getState() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.state
}

func (i *Instance) isExited() bool {
	select {
	case <-i.exited:
		return true
	default:
		return false
	}
}

func (i *Instance) attachAgent(c net.Conn, hello agentMsg) {
	i.mu.Lock()
	i.agentConn = c
	i.agentNode = hello.Node
	i.mu.Unlock()
}

func (i *Instance) detachAgent(c net.Conn) {
	i.mu.Lock()
	if i.agentConn == c {
		i.agentConn = nil
	}
	i.mu.Unlock()
}

func (i *Instance) agentMessage(m agentMsg) {
	i.mu.Lock()
	defer i.mu.Unlock()
	switch m.Type {
	case "stats":
		i.heapUsed, i.heapTotal, i.lag = m.HeapUsed, m.HeapTotal, m.Lag
	case "listening":
		i.listening = true
	}
}

// requestShutdown asks the agent to stop the application gracefully.
// It reports whether an agent was there to ask.
func (i *Instance) requestShutdown(timeout time.Duration) bool {
	i.mu.Lock()
	c := i.agentConn
	i.mu.Unlock()
	if c == nil {
		return false
	}
	b, _ := json.Marshal(map[string]any{"type": "shutdown", "timeoutMs": timeout.Milliseconds()})
	c.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_, err := c.Write(append(b, '\n'))
	return err == nil
}

// stop shuts the process down: gracefully through the agent (or SIGTERM on
// Unix) and, after the timeout, by killing the whole process tree.
func (i *Instance) stop(timeout time.Duration) {
	if i.isExited() {
		return
	}
	i.setState("stopping")
	asked := i.requestShutdown(timeout)
	if !asked {
		asked = i.os.signalStop(i.pid) == nil
	}
	if asked {
		select {
		case <-i.exited:
			return
		case <-time.After(timeout):
			i.app.logs.System("instance %d (pid %d) did not exit within %s; terminating", i.index, i.pid, timeout)
		}
	}
	i.os.kill(i.pid)
	select {
	case <-i.exited:
	case <-time.After(10 * time.Second):
		i.app.logs.System("instance %d (pid %d) still running after kill", i.index, i.pid)
	}
}

func (i *Instance) status(restarts int, lastExit *exitInfo) model.InstanceStatus {
	i.mu.Lock()
	defer i.mu.Unlock()
	st := model.InstanceStatus{
		Index:          i.index,
		PID:            i.pid,
		Port:           i.port,
		State:          i.state,
		Healthy:        i.healthy,
		Restarts:       restarts,
		CPUPercent:     i.cpu,
		MemoryBytes:    i.mem,
		HeapUsedBytes:  i.heapUsed,
		HeapTotalBytes: i.heapTotal,
		EventLoopLagMs: i.lag,
		NodeVersion:    i.agentNode,
	}
	t := i.startedAt
	st.StartedAt = &t
	if i.backend != nil {
		st.Requests = i.backend.Requests.Load()
		st.ActiveConns = i.backend.Active.Load()
	}
	if lastExit != nil {
		code, at := lastExit.code, lastExit.at
		st.LastExitCode, st.LastExitAt = &code, &at
	}
	return st
}

// sample refreshes CPU and memory for the instance's whole process tree.
func (i *Instance) sample() {
	if i.isExited() {
		return
	}
	root, err := process.NewProcess(int32(i.pid))
	if err != nil {
		return
	}
	tree := []*process.Process{root}
	for q := 0; q < len(tree) && q < 256; q++ {
		kids, _ := tree[q].Children()
		tree = append(tree, kids...)
	}
	i.mu.Lock()
	if i.procs == nil {
		i.procs = map[int32]*process.Process{}
	}
	seen := map[int32]bool{}
	var cpu float64
	var mem uint64
	for _, p := range tree {
		seen[p.Pid] = true
		tracked, ok := i.procs[p.Pid]
		if !ok {
			tracked = p
			i.procs[p.Pid] = p
		}
		i.mu.Unlock()
		c, _ := tracked.Percent(0) // since the previous sample
		m, _ := tracked.MemoryInfo()
		i.mu.Lock()
		cpu += c
		if m != nil {
			mem += m.RSS
		}
	}
	for pid := range i.procs {
		if !seen[pid] {
			delete(i.procs, pid)
		}
	}
	i.cpu, i.mem = cpu, mem
	i.mu.Unlock()
}

// lineWriter turns a child's output stream into log lines. It also watches
// for Node's report that the instance's port was already in use.
type lineWriter struct {
	sink      *LogSink
	stream    string
	instance  int
	port      string
	addrInUse *atomic.Bool
	buf       bytes.Buffer
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf.Write(p)
	for {
		b := w.buf.Bytes()
		idx := bytes.IndexByte(b, '\n')
		if idx < 0 {
			if w.buf.Len() > 16<<10 { // flush pathological lines in chunks
				w.emit(string(b))
				w.buf.Reset()
			}
			return len(p), nil
		}
		w.emit(string(bytes.TrimRight(b[:idx], "\r")))
		w.buf.Next(idx + 1)
	}
}

func (w *lineWriter) flush() {
	if w.buf.Len() > 0 {
		w.emit(w.buf.String())
		w.buf.Reset()
	}
}

func (w *lineWriter) emit(s string) {
	if w.addrInUse != nil && mentionsAddrInUse(s, w.port) {
		w.addrInUse.Store(true)
	}
	w.sink.Write(model.LogLine{Time: time.Now(), Stream: w.stream, Instance: w.instance, Text: s})
}
