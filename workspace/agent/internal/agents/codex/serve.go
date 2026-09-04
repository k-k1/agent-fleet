package codex

// The RuntimeSupervisor of the codex app-server (docs/log/27 §3, §7, P3). It starts and
// watches one shared `codex app-server` process (WS JSON-RPC, loopback :7798) and provides
// the runtime and the single writer connection (appClient) for managed sessions. Its
// responsibility is the life of the process and the connection only — what a thread is made
// to do belongs to driver.go's ThreadHandle.
//
// The connection is kept apart from P1's observation (codexObserver in package main,
// read-only): observation stays as it is, for compaction detection and rate limits of TUI
// (CLI route) sessions, and managed writes go through this supervisor's writer connection
// alone (single-writer exclusivity, §2).
//
// Measured (0.144.4, docs/log/27 §12.3):
//   - the listen port serves HTTP /healthz and /readyz alongside WS — use those for the
//     health check (the same shape as opencode's /global/health).
//   - a thread-scoped notification is delivered only to the connection that loaded that
//     thread (§12.1-1). The writer connection always receives them for the threads it
//     started or resumed.
//   - after a daemon SIGKILL, restart → thread/resume finds the turn that was running
//     committed to the rollout as status=interrupted, and the thread continues as it is
//     (§12.3).
//
// Exit recording (docs/log/26, the supervisor move of §10.2-2): the daemon is a child of the
// supervisor, so cmd.Wait() gives its wait status directly. An unexpected death is recorded
// with status.PersistExit on every live managed session, and a session whose recovery
// (reconcile) succeeds has it cleared by the baseline write (same shape as opencode's
// serve.go).

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// appServerAddrEnv is the existing contract buildProgram (TUI --remote) and main's
// observation read: a value present means the shared app-server is usable. The supervisor
// exports it when a start succeeds.
const appServerAddrEnv = "AF_CODEX_APP_SERVER_ADDR"
const defaultAppServerAddr = "ws://127.0.0.1:7798"

// Supervisor owns the shared codex app-server daemon and the managed writer
// connection: idempotent start, health, generation counter, drain and restart
// (the RuntimeSupervisor of docs/log/27 §3).
type Supervisor struct {
	mu     sync.Mutex
	gen    int        // runtime generation (§7); ++ on every new process / new writer connection
	cmd    *exec.Cmd  // this generation's child process (nil for an adopted existing process)
	client *appClient // managed writer connection (one per generation)
	up     bool
	// stopping marks a deliberate teardown (Restart/Shutdown) so the waiter doesn't
	// record it as a crash or trigger reconciliation.
	stopping bool
	// watching: one zero-demand watcher (agents.WatchIdle, idlestop.go) is running.
	watching bool
}

var supervisor = &Supervisor{}

// TUIDependents is the seam returning how many TUI-route codex sessions use the shared
// daemon as their backend. This package holds neither the session ledger nor tmux, so
// package main swaps it in at startup (default 0 = look at managed sessions only).
//
// Undercounting here lets the zero-demand decision pull the backend out from under live TUI
// sessions: buildProgram bakes in `codex --remote <addr>`, so a TUI whose daemon disappeared
// stops together with its conversation. Write this on the safe side, counting generously.
var TUIDependents = func() int { return 0 }

// dependents is the total number of things needing the shared daemon (managed handles + TUI).
//
// The managed side counts REGISTERED handles rather than liveHandles: when the daemon dies
// runtimeLost clears alive on every handle, so counting live ones would look like "zero
// demand right after the death" and stop waking it back up exactly when recovery is due. A
// handle disappears only through DropHandle (stop, halt, archive), i.e. when the user folded
// that session up.
func dependents() int {
	handlesMu.Lock()
	n := len(handles)
	handlesMu.Unlock()
	return n + TUIDependents()
}

// idleGraceEnv / defaultIdleGrace: fold the daemon up after zero demand lasts this long.
// 0 disables the automatic stop. A cold start is 217 ms (measured), so the stop-and-resume
// round trip is cheap.
const idleGraceEnv = "AF_CODEX_APP_SERVER_IDLE_SEC"
const defaultIdleGrace = 2 * time.Minute

// Serve returns the package-wide supervisor instance.
func Serve() *Supervisor { return supervisor }

// Disabled reports whether the shared app-server is switched off (same env the
// pre-P3 startCodexAppServer honored).
func (s *Supervisor) Disabled() bool { return os.Getenv("AF_CODEX_APP_SERVER_DISABLE") == "1" }

// operatorAddr records once the listen address the operator specified through env. The same
// env is also written on a successful Ensure as the mark "a usable daemon lives here" (the
// existing contract buildProgram's --remote and the observation read), so the value from
// before the mark is kept here: removing the mark on an automatic stop must not lose the
// operator's setting as well.
var (
	configuredOnce sync.Once
	configuredAddr string
)

func operatorAddr() string {
	configuredOnce.Do(func() { configuredAddr = os.Getenv(appServerAddrEnv) })
	return configuredAddr
}

// Addr returns the app-server ws address (live mark → operator setting → default).
func (s *Supervisor) Addr() string {
	if v := os.Getenv(appServerAddrEnv); v != "" {
		return v
	}
	if v := operatorAddr(); v != "" {
		return v
	}
	return defaultAppServerAddr
}

// Generation returns the current runtime generation (§7).
func (s *Supervisor) Generation() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gen
}

// healthy probes the daemon's /healthz (the ws listen port doubles as HTTP —
// measured 0.144.4). Unix-socket listeners fall back to a ws dial probe.
func healthy(addr string) bool {
	if rest, ok := strings.CutPrefix(addr, "ws://"); ok {
		c := &http.Client{Timeout: 2 * time.Second}
		res, err := c.Get("http://" + strings.TrimSuffix(rest, "/") + "/healthz")
		if err != nil {
			return false
		}
		res.Body.Close()
		return res.StatusCode < 500
	}
	conn, err := dialAppServer(addr)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// ErrNotLoggedIn: codex is not logged in, so the shared daemon is not started. The
// app-server itself would listen without looking at the login state at all (staying resident
// at about 110 MB), and in a workspace that never authenticated that residency is pure waste.
var ErrNotLoggedIn = errors.New("codex にログインしていないため app-server を起動しません")

// Ensure starts (or adopts) the shared daemon and the managed writer connection,
// idempotently. Returns the writer client and the generation the caller should
// stamp on its handles.
//
// A daemon is newly started only when codex is logged in and something needs it (the start
// conditions are the addendum to docs/log/27 §7). Bringing it up unconditionally at startup
// is not worth it: 110 MB resident against a 217 ms cold start.
func (s *Supervisor) Ensure() (*appClient, int, error) {
	if s.Disabled() {
		return nil, 0, errors.New("codex app-server is disabled (AF_CODEX_APP_SERVER_DISABLE=1)")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	addr := s.Addr()
	if s.up && s.client != nil && s.client.alive() {
		return s.client, s.gen, nil
	}
	// Daemon already listening (started by a previous Agent process, or the writer
	// socket alone dropped): adopt it with a fresh writer connection. cmd==nil for a
	// daemon we didn't spawn — exit recording degrades to socket-loss detection.
	if !healthy(addr) {
		// Not logged in: do not start one. Adopting a daemon that already listens costs no
		// new memory, so let that through (codex itself rejects an unauthenticated turn —
		// this gate is about startup cost, not about authorization).
		if !loggedIn() {
			return nil, 0, ErrNotLoggedIn
		}
		if err := s.startDaemonLocked(addr); err != nil {
			return nil, 0, err
		}
	}
	cl, err := newAppClient(addr)
	if err != nil {
		return nil, 0, fmt.Errorf("codex app-server writer 接続に失敗しました: %w", err)
	}
	s.gen++
	s.client = cl
	s.up = true
	s.stopping = false
	gen := s.gen
	cl.onClosed = func() { s.writerLost(gen) }
	go cl.readLoop()
	// Keep the existing contract buildProgram (TUI --remote) and main's observation use.
	_ = os.Setenv(appServerAddrEnv, addr)
	s.armIdleWatchLocked()
	log.Printf("codex app-server: writer connected (gen %d, %s)", gen, addr)
	// Tell the observation side (the read-only observer in package main) that it can connect
	// now, so a wake-on-demand after an automatic stop skips a reconnect wait of up to 60s.
	go DaemonUp()
	return cl, gen, nil
}

// AdoptIfRunning is the entry point at Agent startup: it adopts only a daemon that already
// listens (started by a previous Agent process and left behind without a graceful shutdown).
// With none there it does nothing — starting is left to demand (managed Resume, TUI
// BuildLaunch). An unconditional Ensure here would make a workspace that never uses codex
// pay 110 MB just for booting.
func (s *Supervisor) AdoptIfRunning() {
	if s.Disabled() || !healthy(s.Addr()) {
		return
	}
	if _, _, err := s.Ensure(); err != nil {
		log.Printf("codex app-server: could not adopt the running daemon: %v", err)
	}
}

// armIdleWatchLocked starts the "fold up on zero demand" watcher, at most one at a time.
// The caller must hold s.mu.
func (s *Supervisor) armIdleWatchLocked() {
	if s.watching {
		return
	}
	s.watching = true
	go agents.WatchIdle("codex app-server", dependents, s.stopIfIdle,
		agents.IdleGrace(idleGraceEnv, defaultIdleGrace))
}

// stopIfIdle folds the daemon up only after re-checking zero demand under the lock. A Resume
// or BuildLaunch between the watch loop's decision and the stop would pull the backend out
// from under a live session, so this one place closes the race. false = demand had returned
// (keep watching).
func (s *Supervisor) stopIfIdle() bool {
	s.mu.Lock()
	if !s.up && s.cmd == nil {
		s.watching = false
		s.mu.Unlock()
		return true // already down (daemon death and the like) — step out of the watch
	}
	if dependents() > 0 {
		s.mu.Unlock()
		return false
	}
	cmd, cl := s.cmd, s.client
	s.stopping, s.up, s.client, s.watching = true, false, nil, false
	s.mu.Unlock()

	if cl != nil {
		cl.close()
	}
	if cmd == nil {
		// An adopted daemon has no process handle, so no signal can be sent. Fold up the
		// writer connection only and step out of the watch (it takes effect the next time we
		// start one we own).
		log.Printf("codex app-server: zero demand, but an adopted daemon cannot be stopped")
	} else {
		// Zero demand means no managed turn is running, so no drain is needed.
		stopProcess(cmd, s.Addr())
		log.Printf("codex app-server: stopped (zero demand)")
	}
	s.mu.Lock()
	s.cmd = nil
	s.mu.Unlock()
	// Do not clear stopping: if writerLost, which cl.close() triggers asynchronously, read
	// this as an unintended disconnect, retryEnsure would bring back the daemon we just
	// folded up. The next successful Ensure resets it to false (the only restart point).
	//
	// The TUI bakes in --remote whenever the env is present. Leaving the address of a dead
	// daemon behind would make the next launch grab a dead backend, so remove the mark and
	// fall back to a direct start.
	_ = os.Unsetenv(appServerAddrEnv)
	return true
}

// DaemonUp is the seam announcing that the daemon has become usable. The read-only observer
// in package main swaps it in to cut short its reconnect wait.
var DaemonUp = func() {}

// startDaemonLocked spawns the daemon and waits for readiness. Caller holds mu.
func (s *Supervisor) startDaemonLocked(addr string) error {
	if strings.HasPrefix(addr, "unix://") {
		_ = os.Remove(strings.TrimPrefix(addr, "unix://")) // stale socket
	}
	cmd := exec.Command("codex", "app-server", "--listen", addr)
	if err := cmd.Start(); err != nil {
		_ = os.Unsetenv(appServerAddrEnv) // future TUI launches fall back to direct
		return fmt.Errorf("codex app-server の起動に失敗しました: %w", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		if healthy(addr) {
			s.cmd = cmd
			go s.waitDaemon(cmd, s.gen+1) // Ensure increments gen right after
			log.Printf("codex app-server: started (pid %d, %s)", cmd.Process.Pid, addr)
			return nil
		}
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait() // reap: waitDaemon is only started on success — Kill alone leaves a zombie
	_ = os.Unsetenv(appServerAddrEnv)
	return errors.New("codex app-server が時間内に起動しませんでした")
}

// waitDaemon records WHY the owned daemon exited (§10.2-2: the supervisor move of the pane
// wrapper's record-exit) and kicks reconciliation for the surviving sessions.
func (s *Supervisor) waitDaemon(cmd *exec.Cmd, gen int) {
	err := cmd.Wait()
	s.mu.Lock()
	deliberate := s.stopping || s.cmd != cmd
	if s.cmd == cmd {
		s.up = false
		s.cmd = nil
	}
	s.mu.Unlock()
	if deliberate {
		return // Restart/Shutdown own this teardown
	}
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				code = 128 + int(ws.Signal())
			} else {
				code = ee.ExitCode()
			}
		} else {
			code = -1
		}
	}
	sig := 0
	if code >= 128 {
		sig = code - 128
	}
	oomNow, okOOM := status.OOMKillCount()
	// Generation history (the operational metadata of §9.5): like opencode, P3 uses the log
	// as its ledger.
	log.Printf("codex app-server: daemon died unexpectedly (gen %d, code %d, sig %d, oomCount ok=%v)", gen, code, sig, okOOM)
	ClearCompacting()
	for _, h := range liveHandles() {
		base, _ := status.ReadExit(h.name)
		oom := okOOM && oomNow > base.OOMBase
		status.PersistExit(h.name, status.ExitInfo{
			Reason: status.ExitReasonFor(code, sig, oom),
			Code:   code, Signal: sig,
			At:      time.Now().Format(time.RFC3339),
			OOMBase: base.OOMBase,
		})
		h.runtimeLost()
	}
	// The daemon is also the backend of the CLI route (TUI --remote): even with zero managed
	// sessions, restart it while live TUI sessions exist (reconcileAll never reaches Ensure
	// without managed metadata). With nobody waiting, do not restart — the same call as the
	// fold-up-on-zero-demand policy (stopIfIdle), reclaiming 110 MB the moment it dies.
	// On failure startDaemonLocked drops the env and later TUI launches fall back to a
	// direct start.
	if dependents() > 0 {
		go s.retryEnsure("daemon death")
	} else {
		_ = os.Unsetenv(appServerAddrEnv)
	}
	go reconcileAll("daemon death")
}

// retryEnsure brings the shared daemon back after an unexpected death, once per
// death with a short settle (a brake on instant-death loops — the next retry comes from the
// next Resume).
func (s *Supervisor) retryEnsure(reason string) {
	time.Sleep(3 * time.Second)
	if _, _, err := s.Ensure(); err != nil {
		log.Printf("codex app-server: restart after %s failed: %v", reason, err)
	}
}

// writerLost handles the managed writer socket dropping while its generation is
// still current: daemon dead (owned death is recorded by waitDaemon; an adopted
// one only shows up here) or a transient socket loss. Either way handles fall to
// unknown and reconciliation redials (§6-1).
func (s *Supervisor) writerLost(gen int) {
	s.mu.Lock()
	if s.gen != gen || s.stopping {
		s.mu.Unlock()
		return // superseded or deliberate teardown
	}
	ownerless := s.cmd == nil
	s.up = false
	s.mu.Unlock()
	log.Printf("codex app-server: writer connection lost (gen %d, adopted=%v)", gen, ownerless)
	for _, h := range liveHandles() {
		h.runtimeLost()
	}
	if ownerless {
		// The death of an adopted daemon is visible only here — for an owned one waitDaemon
		// owns the restart. For a mere socket blip, Ensure re-adopts the healthy daemon.
		go s.retryEnsure("writer loss")
	}
	go reconcileAll("writer connection lost")
}

// Restart is the path that applies auth and configuration changes (§7): drain → stop the old
// process → reconcile brings up the new generation. Being a shared daemon, the drain is a
// switch-over window for every codex session in the workspace (by design, §7 — the CLI
// route's TUI backend rides on the same daemon).
func (s *Supervisor) Restart(reason string) {
	s.mu.Lock()
	if !s.up && s.cmd == nil {
		s.mu.Unlock()
		return // not running — nothing to drain; next Ensure starts fresh
	}
	cmd := s.cmd
	cl := s.client
	s.stopping = true
	s.mu.Unlock()

	log.Printf("codex app-server: restart requested (%s) — draining", reason)
	s.drain()
	if cl != nil {
		cl.close()
	}
	stopProcess(cmd, s.Addr())
	s.mu.Lock()
	s.up = false
	s.cmd = nil
	s.client = nil
	s.stopping = false
	s.mu.Unlock()
	go reconcileAll("restart: " + reason)
}

// Shutdown drains and stops the daemon (graceful workspace stop, §10.2-8).
// Assumes shutdown.go has already delivered C-c to the tmux (CLI route) panes.
func (s *Supervisor) Shutdown() {
	s.mu.Lock()
	if !s.up && s.cmd == nil {
		s.mu.Unlock()
		return
	}
	cmd := s.cmd
	cl := s.client
	s.stopping = true
	s.up = false
	s.watching = false
	s.mu.Unlock()
	s.drain()
	if cl != nil {
		cl.close()
	}
	stopProcess(cmd, s.Addr())
	_ = os.Unsetenv(appServerAddrEnv)
}

// drainTimeout bounds how long a drain waits for running managed turns to finish
// before interrupting them (§7: wait for completion, interrupt on timeout).
func drainTimeout() time.Duration {
	if v := os.Getenv("AF_CODEX_DRAIN_TIMEOUT_SEC"); v != "" {
		if d, err := time.ParseDuration(v + "s"); err == nil && d > 0 {
			return d
		}
	}
	return 60 * time.Second
}

// drain waits for busy managed handles to go idle, then interrupts stragglers.
func (s *Supervisor) drain() {
	deadline := time.Now().Add(drainTimeout())
	for time.Now().Before(deadline) {
		if len(busyManaged()) == 0 {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	for _, h := range busyManaged() {
		log.Printf("codex app-server: drain timeout — interrupting session %s", h.name)
		_ = h.Interrupt()
	}
	// Give the interrupts a moment to land their turn/completed.
	time.Sleep(2 * time.Second)
}

// busyManaged lists handles with a turn in flight (queued input is not lost — the queue
// lives inside the handle and the pump after reconcile submits the rest).
func busyManaged() []*threadHandle {
	var out []*threadHandle
	for _, h := range liveHandles() {
		h.mu.Lock()
		busy := h.running
		h.mu.Unlock()
		if busy {
			out = append(out, h)
		}
	}
	return out
}

// stopProcess terminates the owned daemon (SIGTERM → SIGKILL). For an adopted
// daemon (cmd == nil) there is no handle to signal — best-effort skip.
func stopProcess(cmd *exec.Cmd, addr string) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if !healthy(addr) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
}
