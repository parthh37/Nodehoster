package procmgr

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

// TaskProcess is one run of a scheduled task: a Node.js process tree with
// the site's runtime, environment, identity and limits, in a Job Object of
// its own so a timeout or a cancel ends the whole tree.
type TaskProcess struct {
	m     *Manager
	cmd   *exec.Cmd
	os    *osProc
	pid   int
	token string

	exited   chan struct{}
	exitCode int

	mu        sync.Mutex
	agentConn net.Conn
	released  bool // the job handle is closed
}

// StartTask starts a task of a node or worker site in the site's active
// release. Its output goes to out (stdout and stderr interleaved; out is
// never written by two goroutines at once).
func (m *Manager) StartTask(site *model.Site, task model.ScheduledTask, runID string, out io.Writer) (*TaskProcess, error) {
	if !site.RunsNode() || site.Node == nil {
		return nil, ErrNotNode
	}
	n := site.Node
	dir := site.ResolveRoot(m.opts.SitesDir, n.AppRoot)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("application folder %q does not exist", dir)
	}
	rt, err := m.resolveNode(site)
	if err != nil {
		return nil, err
	}
	token := newToken()
	cmd, env, err := m.nodeCommand(site, rt, dir, task.Script, task.NpmScript, task.Args, token)
	if err != nil {
		return nil, err
	}
	if err := m.setSecretEnv(env, site, n.Env, task.Name); err != nil {
		return nil, err
	}
	for _, e := range task.Env {
		if e.From != nil {
			continue
		}
		v := e.Value
		if e.Secret {
			v = m.opts.Unseal(v)
		}
		env.set(e.Name, v)
	}
	if err := m.setSecretEnv(env, site, task.Env, task.Name); err != nil {
		return nil, err
	}
	env.set("NODEHOSTER_TASK", task.Name)
	env.set("NODEHOSTER_TASK_RUN", runID)
	cmd.Env = env.list()
	cmd.Stdout, cmd.Stderr = out, out
	cmd.WaitDelay = 5 * time.Second

	cleanup, err := prepare(cmd, n.RunAs, m.opts.Unseal(n.RunAs.Password), filepath.Join(m.opts.SitesDir, site.ID))
	defer cleanup()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", rt.Exe, err)
	}
	osp, err := afterStart(cmd.Process.Pid, n.Limits)
	if err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return nil, err
	}
	p := &TaskProcess{m: m, cmd: cmd, os: osp, pid: cmd.Process.Pid, token: token, exited: make(chan struct{})}
	m.agent.register(token, p)
	go func() {
		err := cmd.Wait()
		code := 0
		if cmd.ProcessState != nil {
			code = cmd.ProcessState.ExitCode()
		} else if err != nil {
			code = -1
		}
		m.agent.unregister(token)
		// Whatever the task left running in its job goes with it: a task
		// is over when its main process is.
		p.mu.Lock()
		p.os.kill(p.pid)
		p.os.release()
		p.released = true
		p.mu.Unlock()
		p.exitCode = code
		close(p.exited)
	}()
	return p, nil
}

func (p *TaskProcess) PID() int { return p.pid }

// Done is closed when the process has exited; ExitCode is valid then.
func (p *TaskProcess) Done() <-chan struct{} { return p.exited }

func (p *TaskProcess) ExitCode() int {
	<-p.exited
	return p.exitCode
}

// Stop asks the task to stop gracefully, through the agent (or SIGTERM on
// Unix), and kills the process tree if it has not exited within grace.
func (p *TaskProcess) Stop(grace time.Duration) {
	select {
	case <-p.exited:
		return
	default:
	}
	asked := p.requestShutdown(grace)
	if !asked {
		asked = p.os.signalStop(p.pid) == nil
	}
	if asked {
		select {
		case <-p.exited:
			return
		case <-time.After(grace):
		}
	}
	p.Kill()
}

// Kill ends the whole process tree at once.
func (p *TaskProcess) Kill() {
	p.mu.Lock()
	if !p.released {
		p.os.kill(p.pid)
	}
	p.mu.Unlock()
	select {
	case <-p.exited:
	case <-time.After(10 * time.Second):
	}
}

func (p *TaskProcess) requestShutdown(timeout time.Duration) bool {
	p.mu.Lock()
	c := p.agentConn
	p.mu.Unlock()
	if c == nil {
		return false
	}
	b, _ := json.Marshal(map[string]any{"type": "shutdown", "timeoutMs": timeout.Milliseconds()})
	c.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_, err := c.Write(append(b, '\n'))
	return err == nil
}

func (p *TaskProcess) attachAgent(c net.Conn, _ agentMsg) {
	p.mu.Lock()
	p.agentConn = c
	p.mu.Unlock()
}

func (p *TaskProcess) detachAgent(c net.Conn) {
	p.mu.Lock()
	if p.agentConn == c {
		p.agentConn = nil
	}
	p.mu.Unlock()
}

func (p *TaskProcess) agentMessage(agentMsg) {}
