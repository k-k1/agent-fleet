package opencode

// Private servers for Managed sessions on an explicit route (ADR 0128 decision 5, #4004).
//
// A Managed session normally runs on af's one shared `opencode serve`, which cannot carry a
// route: opencode holds a provider's credential in the server's environment, and the route
// gateway serves a token only to a process in the owning session's tree (routegw/peer.go), which
// the shared server is in no session's. So a Managed session with a route selection gets a server
// of its own, the same isolation a Terminal pane gets with --standalone (buildRouteLaunch): the
// route's bearer, region and inline config, none of the workspace's provider keys or engine
// token, and the session's own AF_SESSION_NAME / AF_WORK_DIR for its shell (the shared-server
// plugin that PUTs them is off here, as on every server that is not the shared one).
//
// The server's process is registered as the session's runtime root (agents.RegisterRuntimeRoots),
// so its model calls and the tools it runs are the session's, and nothing else is.
//
// It lives exactly as long as the session's handle: started by Resume, stopped by DropHandle and
// by Shutdown, replaced when it stops answering. It is started from a goroutine pinned to its OS
// thread with Pdeathsig, so an Agent that dies takes it along: its route token lived only in the
// dead Agent's gateway, and a server left behind would hold memory and serve nobody. Unlike the
// shared server it is never adopted for the same reason.

import (
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// routeServe is one session's private server.
type routeServe struct {
	name     string
	addr     string
	password string
	gen      int
	cmd      *exec.Cmd
	// done is closed when the process has exited and been reaped.
	done chan struct{}
	// up: the server answered its probe. stopping: af asked it to end, so its exit is no crash.
	// Both are guarded by routeServes.mu.
	up, stopping bool
}

var routeServes = struct {
	mu     sync.Mutex
	byName map[string]*routeServe
	gen    int
	// locks serializes the start and stop of one session's server; a start waits seconds for the
	// probe, and other sessions must not wait with it.
	locks map[string]*sync.Mutex
}{byName: map[string]*routeServe{}, locks: map[string]*sync.Mutex{}}

func init() {
	agents.RegisterRuntimeRoots(routeServeRoots)
}

// routeServeLock is the per-session lock of start and stop.
func routeServeLock(name string) *sync.Mutex {
	routeServes.mu.Lock()
	defer routeServes.mu.Unlock()
	l := routeServes.locks[name]
	if l == nil {
		l = &sync.Mutex{}
		routeServes.locks[name] = l
	}
	return l
}

// routeServeOf is the session's registered server, nil when none.
func routeServeOf(name string) *routeServe {
	routeServes.mu.Lock()
	defer routeServes.mu.Unlock()
	return routeServes.byName[name]
}

// routeServeRoots is the session's server process while it is up: the gateway's caller root.
func routeServeRoots(name string) []int {
	routeServes.mu.Lock()
	defer routeServes.mu.Unlock()
	rs := routeServes.byName[name]
	if rs == nil || !rs.up || rs.stopping || rs.cmd == nil || rs.cmd.Process == nil {
		return nil
	}
	return []int{rs.cmd.Process.Pid}
}

// routeServeByHost finds the server listening on host ("127.0.0.1:<port>"), for the password
// authTransport adds.
func routeServeByHost(host string) *routeServe {
	routeServes.mu.Lock()
	defer routeServes.mu.Unlock()
	for _, rs := range routeServes.byName {
		if strings.TrimPrefix(rs.addr, "http://") == host {
			return rs
		}
	}
	return nil
}

// routeServeAddr is the address of the session's server while it is up.
func routeServeAddr(name string) (string, bool) {
	routeServes.mu.Lock()
	defer routeServes.mu.Unlock()
	rs := routeServes.byName[name]
	if rs == nil || !rs.up || rs.stopping {
		return "", false
	}
	return rs.addr, true
}

// routeServeGen is the generation of the server on addr, -1 when none is up there: a turn that
// ran on a server that has since gone or been replaced must not read the next one's state as its
// own (sendTurn).
func routeServeGen(addr string) int {
	routeServes.mu.Lock()
	defer routeServes.mu.Unlock()
	for _, rs := range routeServes.byName {
		if rs.addr == addr && rs.up && !rs.stopping {
			return rs.gen
		}
	}
	return -1
}

// serveGenOf is the generation of whichever server addr names.
func serveGenOf(addr string) int {
	if addr == serveAddr() {
		return Serve().Generation()
	}
	return routeServeGen(addr)
}

// ensureRouteServe returns the address and generation of m's private server, starting one when
// none answers.
func ensureRouteServe(m session.Meta) (string, int, error) {
	l := routeServeLock(m.Name)
	l.Lock()
	defer l.Unlock()
	if rs := routeServeOf(m.Name); rs != nil {
		routeServes.mu.Lock()
		up := rs.up && !rs.stopping
		routeServes.mu.Unlock()
		if up && healthy(rs.addr) {
			return rs.addr, rs.gen, nil
		}
		stopRouteServeLocked(rs, "not answering")
	}
	return startRouteServe(m)
}

// routeServeStartTimeout bounds the wait for a new private server's probe; a var for tests.
var routeServeStartTimeout = 20 * time.Second

// startRouteServe starts m's private server. The caller holds the session's lock.
func startRouteServe(m session.Meta) (string, int, error) {
	if m.Route == nil {
		return "", 0, errors.New("opencode: a private server is only for a session with an explicit route")
	}
	// The route's own checks (gate, model, Managed admitted) and the bearer for this incarnation;
	// minting it ends any earlier token of the session.
	plan, err := agentImpl{}.ApplyRoute(m, *m.Route)
	if err != nil {
		return "", 0, err
	}
	if ref := configConflict(append(globalConfigFiles(), projectConfigFiles(m)...)); ref != nil {
		return "", 0, ref
	}
	spec := CurrentLaunch()
	if err := PrepareStart(spec); err != nil {
		return "", 0, err
	}
	port, err := freeLoopbackPort()
	if err != nil {
		return "", 0, fmt.Errorf("opencode: no loopback port for the session's server: %w", err)
	}
	password, err := genServePassword()
	if err != nil {
		return "", 0, fmt.Errorf("opencode: cannot prepare the session server's password: %w", err)
	}
	rs := &routeServe{name: m.Name, addr: "http://127.0.0.1:" + port, password: password, done: make(chan struct{})}
	cmd := exec.Command(spec.Bin, "serve", "--hostname", "127.0.0.1", "--port", port)
	cmd.Env = routeServeEnv(os.Environ(), m.Name, plan, password)
	cmd.Dir = paths.HomeDir()
	rs.cmd = cmd
	// Registered before the start: the probe below goes through authTransport, which finds the
	// password by the server's address.
	routeServes.mu.Lock()
	routeServes.byName[m.Name] = rs
	routeServes.mu.Unlock()
	tail, err := startPinned(rs)
	if err != nil {
		routeServes.mu.Lock()
		if routeServes.byName[m.Name] == rs {
			delete(routeServes.byName, m.Name)
		}
		routeServes.mu.Unlock()
		return "", 0, fmt.Errorf("opencode: failed to start the session's server: %w", err)
	}
	defer tail.Settle()
	deadline := time.Now().Add(routeServeStartTimeout)
	for time.Now().Before(deadline) {
		select {
		case <-rs.done:
			deadline = time.Now() // exited before answering; the error below carries its stderr
			continue
		case <-time.After(200 * time.Millisecond):
		}
		if healthy(rs.addr) {
			routeServes.mu.Lock()
			routeServes.gen++
			rs.gen, rs.up = routeServes.gen, true
			routeServes.mu.Unlock()
			go monitorRouteEvents(rs)
			log.Printf("opencode: session %s: route server started (gen %d, pid %d, %s)", m.Name, rs.gen, cmd.Process.Pid, rs.addr)
			return rs.addr, rs.gen, nil
		}
	}
	err = tail.Wrap(errors.New("opencode: the session's server did not start in time"))
	stopRouteServeLocked(rs, "start failed")
	return "", 0, err
}

// startPinned starts rs's process from a goroutine that holds its OS thread until the process
// exits. Pdeathsig fires when the thread that started the child ends, so the thread must not be
// handed back to the scheduler while the server runs; when the Agent dies, every such thread
// ends and the server gets SIGTERM.
func startPinned(rs *routeServe) (*agents.StderrTail, error) {
	type result struct {
		tail *agents.StderrTail
		err  error
	}
	started := make(chan result, 1)
	go func() {
		runtime.LockOSThread()
		rs.cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
		tail, err := agents.StartWithStderrTail(rs.cmd)
		started <- result{tail, err}
		if err != nil {
			close(rs.done)
			return // the locked thread ends with this goroutine; nothing was started on it
		}
		waitRouteServe(rs, tail)
	}()
	r := <-started
	return r.tail, r.err
}

// waitRouteServe reaps rs and, when nobody asked it to end, records the death on the session and
// lets reconcile start a replacement (the same path as the shared server's death).
func waitRouteServe(rs *routeServe, tail *agents.StderrTail) {
	err := rs.cmd.Wait()
	tail.Release()
	close(rs.done)
	routeServes.mu.Lock()
	deliberate, wasUp := rs.stopping, rs.up
	rs.up = false
	if routeServes.byName[rs.name] == rs {
		delete(routeServes.byName, rs.name)
	}
	routeServes.mu.Unlock()
	if deliberate || !wasUp {
		return
	}
	code, sig := exitOf(err)
	log.Printf("opencode: session %s: route server died unexpectedly (gen %d, code %d, sig %d)", rs.name, rs.gen, code, sig)
	h := handleFor(rs.name)
	if h == nil {
		return
	}
	h.mu.Lock()
	ours := h.addr == rs.addr
	h.mu.Unlock()
	if !ours {
		return
	}
	oomNow, okOOM := status.OOMKillCount()
	base, _ := status.ReadExit(rs.name)
	status.PersistExit(rs.name, status.ExitInfo{
		Reason: status.ExitReasonFor(code, sig, okOOM && oomNow > base.OOMBase),
		Code:   code, Signal: sig,
		At:      time.Now().Format(time.RFC3339),
		OOMBase: base.OOMBase,
	})
	h.runtimeLost()
	goReconcile("route server death: " + rs.name)
}

// exitOf reads a wait error as an exit code and the signal that ended the process (0 for none).
func exitOf(err error) (code, sig int) {
	if err == nil {
		return 0, 0
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return -1, 0
	}
	if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal()), int(ws.Signal())
	}
	return ee.ExitCode(), 0
}

// stopRouteServe ends the session's private server, if it has one.
func stopRouteServe(name, reason string) {
	l := routeServeLock(name)
	l.Lock()
	defer l.Unlock()
	if rs := routeServeOf(name); rs != nil {
		stopRouteServeLocked(rs, reason)
	}
}

// routeServeStopGrace is how long a server gets after SIGTERM before SIGKILL; a var for tests.
var routeServeStopGrace = 6 * time.Second

// stopRouteServeLocked ends rs and waits until it is reaped. The caller holds the session's lock.
// No drain: a server of one session is stopped only when that session's handle is going away.
func stopRouteServeLocked(rs *routeServe, reason string) {
	routeServes.mu.Lock()
	rs.stopping = true
	if routeServes.byName[rs.name] == rs {
		delete(routeServes.byName, rs.name)
	}
	routeServes.mu.Unlock()
	if rs.cmd == nil || rs.cmd.Process == nil {
		return
	}
	_ = rs.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-rs.done:
	case <-time.After(routeServeStopGrace):
		_ = rs.cmd.Process.Kill()
		<-rs.done
	}
	log.Printf("opencode: session %s: route server stopped (%s)", rs.name, reason)
}

// stopAllRouteServes ends every private server (the workspace's graceful stop).
func stopAllRouteServes() {
	routeServes.mu.Lock()
	names := make([]string, 0, len(routeServes.byName))
	for n := range routeServes.byName {
		names = append(names, n)
	}
	routeServes.mu.Unlock()
	for _, n := range names {
		stopRouteServe(n, "shutdown")
	}
}

// routeServeEnv is a private server's environment: the Agent's own without the variables that
// would widen or replace the route's config, the session's identity for its shell and MCP
// children, the route's bearer and region, the inline config, and the server's credentials.
// Nothing of keyEnv (provider keys, the engine token) and no AF_OPENCODE_SHARED_SERVE: this
// server is the session's, as a --standalone pane's is.
func routeServeEnv(base []string, name string, plan agents.RoutePlan, password string) []string {
	drop := append([]string{"AF_SESSION_SID", "AF_SESSION_NAME", "AF_WORK_DIR", sharedServeEnv,
		"OPENCODE_SERVER_USERNAME", "OPENCODE_SERVER_PASSWORD"}, plan.Unset...)
	// The names keyEnv gives the shared server, should the Agent's own environment carry one.
	for _, kv := range keyEnv() {
		if k, _, ok := strings.Cut(kv, "="); ok {
			drop = append(drop, k)
		}
	}
	env := withoutEnv(base, drop...)
	env = agents.WithSessionEnv(env, name)
	return mergeCommandEnv(env, append(append(append([]string(nil), plan.Env...), plan.Settings...),
		"OPENCODE_SERVER_USERNAME="+serveUser, "OPENCODE_SERVER_PASSWORD="+password))
}

// freeLoopbackPort asks the kernel for a free loopback port. Another process can take it before
// the server binds; the server then fails to start and the probe, which needs this server's
// password, cannot mistake the other one for it.
func freeLoopbackPort() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer l.Close()
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port), nil
}

// monitorRouteEvents is monitorEvents for one private server: its events reach the handles the
// same way (handleServeEvent finds the handle by its opencode session), and a dropped stream makes
// the session's in-flight tools unknown until a turn boundary shows up again.
func monitorRouteEvents(rs *routeServe) {
	for {
		select {
		case <-rs.done:
			return
		default:
		}
		err := streamRouteEvents(rs)
		select {
		case <-rs.done:
			return
		default:
		}
		if !healthy(rs.addr) {
			// The process is still there but does not answer: the next Resume replaces it
			// (ensureRouteServe), and the waiter records a real death.
			log.Printf("opencode: session %s: route server stopped answering: %v", rs.name, err)
			if h := handleFor(rs.name); h != nil {
				h.mu.Lock()
				ours := h.addr == rs.addr
				h.mu.Unlock()
				if ours {
					h.runtimeLost()
					goReconcile("route server not answering: " + rs.name)
				}
			}
			return
		}
		time.Sleep(time.Second)
	}
}

func streamRouteEvents(rs *routeServe) error {
	unknown := func() {
		if h := handleFor(rs.name); h != nil {
			h.tools.Unknown()
		}
	}
	defer unknown()
	res, err := sseClient.Get(eventURL(rs.addr))
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("event stream: HTTP %d", res.StatusCode)
	}
	unknown()
	return readServeEvents(res.Body, func() bool {
		select {
		case <-rs.done:
			return false
		default:
			return true
		}
	})
}
