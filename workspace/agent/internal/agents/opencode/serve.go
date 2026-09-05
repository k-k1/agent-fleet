package opencode

// The RuntimeSupervisor for opencode serve (docs/log/27 §3 / §7, P2). It starts and
// watches one shared `opencode serve` process (HTTP + SSE on loopback) and provides the
// runtime for managed sessions. Its only responsibility is the PROCESS LIFETIME — what a
// thread (an opencode session) is made to do belongs to driver.go's ThreadHandle.
//
// Measured (1.17.18, docs/log/27 §12.2):
//   - Auth: with OPENCODE_SERVER_PASSWORD unset there is none (the startup log carries an
//     unsecured warning). It listens only on loopback inside the container's network
//     namespace, so it runs unauthenticated on the same judgement as the codex app-server
//     (§9.1). A TUI attach passes through under the same condition.
//   - Provider keys are env-injected (auth.go's env()), so a key change requires a
//     restart: generation + drain (§7) IS the path that applies it.
//   - serve writes to SQLite (message/part) through the v1 flow, so the read layer
//     (transcript.go) can read the canonical record of a managed session untouched.
//
// Exit recording (docs/log/26, §10.2-2): the daemon is the supervisor's child process, so
// cmd.Wait()'s wait status is available directly. An unexpected death goes (a) to the log
// as generation history and (b) at thread level to every live managed session via
// status.PersistExit (sharing the existing session-exit store and reason enum); a session
// that recovers through reconcile is cleared by the baseline write.

import (
	"bufio"
	"context"
	"encoding/json"
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
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

const serveAddrEnv = "AF_OPENCODE_SERVE_ADDR"

// defaultServeAddr sits next to the codex app-server (:7798). It is loopback inside the
// container's network namespace, so the collision risk is limited to processes in this
// container.
const defaultServeAddr = "http://127.0.0.1:7799"

// serveClient is the short-timeout HTTP client for the control calls
// (create/abort/question/status). Only the blocking /message (running a turn, no time
// limit) uses a client of its own, in driver.go.
var serveClient = &http.Client{Timeout: 10 * time.Second}

// Supervisor owns the shared `opencode serve` daemon: idempotent start, health,
// generation counter, drain and restart (the RuntimeSupervisor of docs/log/27 §3).
type Supervisor struct {
	mu   sync.Mutex
	addr string
	gen  int       // runtime generation (§7); ++ every time Ensure starts a new process
	cmd  *exec.Cmd // this generation's child process (nil for an adopted one)
	up   bool
	// stopping marks a deliberate teardown (Restart/Shutdown) so the waiter doesn't
	// record it as a crash or trigger reconciliation.
	stopping bool
	// watching: one zero-demand watcher (agents.WatchIdle, idlestop.go) is running.
	watching bool
}

var supervisor = &Supervisor{}

// dependents is the total number of things that need the shared daemon.
//
// The TUI route of opencode does not use serve (buildProgram connects directly to its own
// SQLite store), so unlike codex only managed handles are counted. The OAuth device flow
// is the one exception: it is the only path that starts the daemon while still
// unconnected, so unless it counts as demand for its duration, the daemon gets folded up
// from under a login in progress.
//
// Counting REGISTERED rather than live handles is for the same reason as on the codex
// side: when the daemon dies runtimeLost clears alive, so counting live would make the
// very situation that needs recovery look like zero demand.
func dependents() int {
	handlesMu.Lock()
	n := len(handles)
	handlesMu.Unlock()
	if oauthBusy() {
		n++
	}
	return n
}

// idleGraceEnv / defaultIdleGrace: fold the daemon up after this much continuous zero
// demand; 0 disables the automatic stop. serve is a measured ~305 MB RSS, so there is no
// reason to let it sit there for as long as managed goes unused.
const idleGraceEnv = "AF_OPENCODE_SERVE_IDLE_SEC"
const defaultIdleGrace = 2 * time.Minute

// Serve returns the package-wide supervisor instance.
func Serve() *Supervisor { return supervisor }

func serveAddr() string {
	if v := os.Getenv(serveAddrEnv); v != "" {
		return v
	}
	return defaultServeAddr
}

// Disabled reports whether managed opencode is switched off for this workspace.
func (s *Supervisor) Disabled() bool { return os.Getenv("AF_OPENCODE_SERVE_DISABLE") == "1" }

// Generation returns the current runtime generation (§7).
func (s *Supervisor) Generation() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gen
}

// Addr returns the serve base URL (also when not yet ensured — callers only use it
// after a successful Ensure).
func (s *Supervisor) Addr() string { return serveAddr() }

// healthy is a cheap liveness probe (GET /global/health).
func healthy(addr string) bool {
	req, err := http.NewRequest("GET", addr+"/global/health", nil)
	if err != nil {
		return false
	}
	res, err := serveClient.Do(req)
	if err != nil {
		return false
	}
	defer res.Body.Close()
	return res.StatusCode < 500
}

// ErrNotConnected: opencode is not connected (no key, no Console login, no explicit
// free-tier choice), so the shared daemon is not started. serve would happily listen
// without looking at auth at all, but at a measured ~305 MB RSS there is no reason to keep
// it resident in a workspace that does not use it.
var ErrNotConnected = errors.New("opencode が未接続のため serve を起動しません")

// Ensure starts (or adopts) the shared serve daemon, idempotently. Returns the
// base URL and the runtime generation the caller should stamp on its handles.
func (s *Supervisor) Ensure() (string, int, error) { return s.ensure(false) }

// ensure is the body of Ensure. allowUnauthed is an escape hatch for the OAuth device
// flow only: that path logs in THROUGH the daemon's API, so refusing to start it for being
// unconnected makes logging in impossible forever (chicken and egg). Every other entry
// point must pass false.
func (s *Supervisor) ensure(allowUnauthed bool) (string, int, error) {
	if s.Disabled() {
		return "", 0, errors.New("opencode serve is disabled (AF_OPENCODE_SERVE_DISABLE=1)")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	addr := serveAddr()
	if s.up && healthy(addr) {
		return addr, s.gen, nil
	}
	// Adopt a serve already listening (a previous Agent process spawned it and we
	// restarted — same pattern as connectCodexAppServer's connect-first). No cmd:
	// exit recording degrades to SSE-disconnect detection for an adopted daemon.
	if healthy(addr) {
		s.gen++
		s.cmd = nil
		s.up = true
		s.stopping = false
		s.armIdleWatchLocked()
		go s.monitorEvents(addr, s.gen)
		log.Printf("opencode serve: adopted running daemon at %s (gen %d)", addr, s.gen)
		return addr, s.gen, nil
	}
	// Past this point a new process starts, i.e. new memory is paid for. Do not pay
	// while unconnected. (An adopt is memory already paid for, so it passed above.)
	if !allowUnauthed && !Connected() {
		return "", 0, ErrNotConnected
	}
	host, port, err := splitServeAddr(addr)
	if err != nil {
		return "", 0, err
	}
	cmd := exec.Command("opencode", "serve", "--hostname", host, "--port", port)
	// Provider keys are env-injected (auth.go) — the same set a TUI launch gets, so
	// managed turns authenticate identically. Re-keying requires a Restart (§7).
	cmd.Env = append(os.Environ(), env()...)
	cmd.Dir = paths.HomeDir()
	if err := cmd.Start(); err != nil {
		return "", 0, fmt.Errorf("opencode serve の起動に失敗しました: %w", err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
		if healthy(addr) {
			s.gen++
			s.cmd = cmd
			s.up = true
			s.stopping = false
			gen := s.gen
			s.armIdleWatchLocked()
			go s.waitDaemon(cmd, gen)
			go s.monitorEvents(addr, gen)
			log.Printf("opencode serve: started (gen %d, pid %d, %s)", gen, cmd.Process.Pid, addr)
			return addr, gen, nil
		}
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait() // reap: waitDaemon is only started on success — Kill alone leaves a zombie
	return "", 0, errors.New("opencode serve が時間内に起動しませんでした")
}

// armIdleWatchLocked starts the "fold up on zero demand" watcher, at most one at a time.
// The caller must hold s.mu.
func (s *Supervisor) armIdleWatchLocked() {
	if s.watching {
		return
	}
	s.watching = true
	go agents.WatchIdle("opencode serve", dependents, s.stopIfIdle,
		agents.IdleGrace(idleGraceEnv, defaultIdleGrace))
}

// stopIfIdle re-checks zero demand INSIDE THE LOCK before folding the daemon up. A Resume
// or an OAuth start landing between the watch loop's decision and the stop would pull the
// ground out from under something that is running, so this is the one place the race is
// closed. false = demand came back (keep watching).
func (s *Supervisor) stopIfIdle() bool {
	s.mu.Lock()
	if !s.up {
		s.watching = false
		s.mu.Unlock()
		return true // already down (daemon death etc.) — the watcher steps off
	}
	if dependents() > 0 {
		s.mu.Unlock()
		return false
	}
	addr := serveAddr()
	cmd := s.cmd
	s.stopping, s.up, s.watching = true, false, false
	s.mu.Unlock()

	if cmd == nil {
		// An adopted daemon has no process handle, so it cannot be signalled. Just
		// step off the watch; it will apply the next time we start an owned one.
		log.Printf("opencode serve: zero demand, but this daemon was adopted and cannot be stopped")
	} else {
		// Zero demand means no managed turn is running, so no drain is needed.
		stopProcess(cmd, addr)
		log.Printf("opencode serve: stopped (zero demand)")
	}
	s.mu.Lock()
	s.cmd = nil
	s.mu.Unlock()
	// stopping is deliberately NOT cleared, so a late SSE disconnect (monitorEvents) is
	// not misread as an unintended loss. The next successful Ensure resets it to false.
	return true
}

// splitServeAddr derives --hostname/--port from the configured http URL.
func splitServeAddr(addr string) (host, port string, err error) {
	rest, ok := strings.CutPrefix(addr, "http://")
	if !ok {
		return "", "", fmt.Errorf("unsupported %s (http://host:port only): %s", serveAddrEnv, addr)
	}
	host, port, ok = strings.Cut(strings.TrimSuffix(rest, "/"), ":")
	if !ok || host == "" || port == "" {
		return "", "", fmt.Errorf("unsupported %s (http://host:port only): %s", serveAddrEnv, addr)
	}
	return host, port, nil
}

// waitDaemon records WHY the owned daemon exited (§10.2-2: exit recording moved from the
// pane wrapper's record-exit into the supervisor) and kicks reconciliation for the
// surviving sessions.
func (s *Supervisor) waitDaemon(cmd *exec.Cmd, gen int) {
	err := cmd.Wait()
	s.mu.Lock()
	// `s.cmd != cmd` (same shape as codex): Restart clears stopping without changing
	// gen, so comparing gen alone can misrecord a deliberate stop as "died
	// unexpectedly".
	deliberate := s.stopping || s.cmd != cmd
	if s.cmd == cmd {
		s.up = false
		s.cmd = nil
	}
	s.mu.Unlock()
	if deliberate {
		return // Restart/Shutdown own this teardown; they log/rebuild themselves
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
	// Generation history (§9.5 operational metadata): in P2 the log IS the ledger.
	log.Printf("opencode serve: daemon died unexpectedly (gen %d, code %d, sig %d, oomCount ok=%v)", gen, code, sig, okOOM)
	// Thread-level record, on every managed session that was live. A session that
	// recovers is cleared by reconcile's baseline write (same as a tui restart).
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
	go reconcileAll("daemon death")
}

// Restart is the path that applies an auth or config change (§7): generation++ → drain →
// respawn → re-resume every handle. The daemon is shared, so the drain is a switchover
// window for every opencode managed session in the workspace at once (by design, §7).
func (s *Supervisor) Restart(reason string) {
	s.mu.Lock()
	if !s.up {
		s.mu.Unlock()
		return // not running — nothing to drain; next Ensure starts fresh
	}
	addr := serveAddr()
	cmd := s.cmd
	s.stopping = true
	s.mu.Unlock()

	log.Printf("opencode serve: restart requested (%s) — draining", reason)
	s.drain(addr)
	stopProcess(cmd, addr)
	s.mu.Lock()
	s.up = false
	s.cmd = nil
	s.stopping = false
	s.mu.Unlock()
	go reconcileAll("restart: " + reason)
}

// Shutdown drains and stops the daemon (graceful workspace stop, §10.2-8).
func (s *Supervisor) Shutdown() {
	s.mu.Lock()
	if !s.up {
		s.mu.Unlock()
		return
	}
	addr := serveAddr()
	cmd := s.cmd
	s.stopping = true
	s.up = false
	s.watching = false
	s.mu.Unlock()
	s.drain(addr)
	stopProcess(cmd, addr)
}

// drainTimeout bounds how long a drain waits for running turns to finish before
// aborting them (§7: wait for completion, interrupt on timeout).
func drainTimeout() time.Duration {
	if v := os.Getenv("AF_OPENCODE_DRAIN_TIMEOUT_SEC"); v != "" {
		if d, err := time.ParseDuration(v + "s"); err == nil && d > 0 {
			return d
		}
	}
	return 60 * time.Second
}

// drain waits for the busy managed sessions to go idle, then aborts the stragglers.
func (s *Supervisor) drain(addr string) {
	deadline := time.Now().Add(drainTimeout())
	for time.Now().Before(deadline) {
		if len(busyManaged(addr)) == 0 {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	for _, h := range busyManaged(addr) {
		log.Printf("opencode serve: drain timeout — aborting session %s", h.sessionID())
		abortSession(addr, h.sessionID(), h.dir)
	}
	// Give the aborts a moment to unwind the blocked turn goroutines.
	time.Sleep(2 * time.Second)
}

// busyManaged lists the opencode sessions that are (a) busy per the runtime and
// (b) owned by a managed handle — a TUI-attached user's own experiments outside AF
// sessions are not ours to wait on. Measured: /session/status is scoped to a project
// (directory), so it is queried per handle dir and the results combined.
func busyManaged(addr string) []*threadHandle {
	byDir := map[string][]*threadHandle{}
	for _, h := range liveHandles() {
		byDir[h.dir] = append(byDir[h.dir], h)
	}
	var out []*threadHandle
	for dir, hs := range byDir {
		res, err := serveClient.Get(dirQ(addr+"/session/status", dir))
		if err != nil {
			continue
		}
		var m map[string]struct {
			Type string `json:"type"`
		}
		err = json.NewDecoder(res.Body).Decode(&m)
		res.Body.Close()
		if err != nil {
			continue
		}
		for _, h := range hs {
			// Measured: /session/status omits idle, so being listed = busy/retry.
			if st, ok := m[h.sessionID()]; ok && st.Type != "idle" {
				out = append(out, h)
			}
		}
	}
	return out
}

func abortSession(addr, ses, dir string) {
	req, err := http.NewRequest("POST", dirQ(addr+"/session/"+ses+"/abort", dir), strings.NewReader("{}"))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if res, err := serveClient.Do(req); err == nil {
		res.Body.Close()
	}
}

// stopProcess terminates the owned daemon (SIGTERM → SIGKILL). For an adopted
// daemon (cmd == nil) there is no handle to signal — best-effort skip.
func stopProcess(cmd *exec.Cmd, addr string) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	// Poll health only (same shape as codex): cmd.ProcessState would data-race with
	// waitDaemon's cmd.Wait(), so it is left alone. Reaping is waitDaemon's job.
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if !healthy(addr) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
}

// --- SSE event monitor ------------------------------------------------------

// monitorEvents subscribes to the serve's cross-project SSE stream (GET /global/event)
// and dispatches to the thread handles. Measured: a bare /event is scoped to the project
// of serve's own cwd, so a question.asked from a session in another directory never
// arrives; /global/event wraps each event in {"payload": {...}} and delivers all projects.
// Unlike codex, no attach is needed. A disconnect while the generation is still current
// means the daemon is gone or wedged: verify via health and reconcile.
func (s *Supervisor) monitorEvents(addr string, gen int) {
	for {
		if s.Generation() != gen {
			return // superseded generation — its monitor exits quietly
		}
		err := s.streamEvents(addr, gen)
		if s.Generation() != gen {
			return
		}
		if !healthy(addr) {
			// Owned daemon death is recorded by waitDaemon; an ADOPTED daemon's death
			// is only visible here. Either way handles must fall to unknown (§6-1).
			s.mu.Lock()
			wasUp := s.up
			ownerless := s.cmd == nil
			if s.up && s.gen == gen {
				s.up = false
			}
			s.mu.Unlock()
			if wasUp && ownerless {
				log.Printf("opencode serve: adopted daemon lost (gen %d): %v", gen, err)
				for _, h := range liveHandles() {
					h.runtimeLost()
				}
				go reconcileAll("adopted daemon lost")
			}
			return
		}
		// Daemon alive, socket dropped (transient): resubscribe.
		time.Sleep(time.Second)
	}
}

// streamEvents reads one SSE connection until it breaks. Returns the read error.
func (s *Supervisor) streamEvents(addr string, gen int) error {
	req, err := http.NewRequest("GET", addr+"/global/event", nil)
	if err != nil {
		return err
	}
	// No timeout: SSE is a long-lived stream (heartbeats keep it warm).
	res, err := (&http.Client{}).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		if s.Generation() != gen {
			return nil
		}
		handleServeEvent([]byte(data))
	}
	return sc.Err()
}

// WithControlCtx builds a request context for short control calls.
func controlCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}
