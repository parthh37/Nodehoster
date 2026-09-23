package procmgr

import (
	"bufio"
	"encoding/json"
	"log/slog"
	"net"
	"sync"
	"time"
)

// agentServer accepts connections from the in-process agent and routes them
// to the instance that owns the token.
type agentServer struct {
	log  *slog.Logger
	l    net.Listener
	path string

	mu    sync.Mutex
	byTok map[string]agentPeer
}

// agentPeer is the process an agent connection belongs to: an instance of
// a site or a scheduled task run.
type agentPeer interface {
	attachAgent(c net.Conn, hello agentMsg)
	detachAgent(c net.Conn)
	agentMessage(m agentMsg)
}

type agentMsg struct {
	Type      string  `json:"type"`
	Token     string  `json:"token,omitempty"`
	PID       int     `json:"pid,omitempty"`
	Node      string  `json:"node,omitempty"`
	Port      int     `json:"port,omitempty"`
	HeapUsed  uint64  `json:"heapUsed,omitempty"`
	HeapTotal uint64  `json:"heapTotal,omitempty"`
	RSS       uint64  `json:"rss,omitempty"`
	Lag       float64 `json:"lag,omitempty"`
}

func newAgentServer(dir string, log *slog.Logger) (*agentServer, error) {
	l, path, err := listenAgent(dir)
	if err != nil {
		return nil, err
	}
	s := &agentServer{log: log, l: l, path: path, byTok: map[string]agentPeer{}}
	go s.accept()
	return s, nil
}

func (s *agentServer) register(token string, inst agentPeer) {
	s.mu.Lock()
	s.byTok[token] = inst
	s.mu.Unlock()
}

func (s *agentServer) unregister(token string) {
	s.mu.Lock()
	delete(s.byTok, token)
	s.mu.Unlock()
}

func (s *agentServer) close() { s.l.Close() }

func (s *agentServer) accept() {
	for {
		c, err := s.l.Accept()
		if err != nil {
			return
		}
		go s.serve(c)
	}
}

func (s *agentServer) serve(c net.Conn) {
	defer c.Close()
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 4096), 64<<10)

	// The first message must be a hello carrying a known token.
	c.SetReadDeadline(time.Now().Add(10 * time.Second))
	if !sc.Scan() {
		return
	}
	var hello agentMsg
	if json.Unmarshal(sc.Bytes(), &hello) != nil || hello.Type != "hello" {
		return
	}
	s.mu.Lock()
	inst := s.byTok[hello.Token]
	s.mu.Unlock()
	if inst == nil {
		return
	}
	c.SetReadDeadline(time.Time{})
	inst.attachAgent(c, hello)
	defer inst.detachAgent(c)

	for sc.Scan() {
		var m agentMsg
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		inst.agentMessage(m)
	}
}
