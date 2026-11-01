package opencode

// The RuntimeSupervisor for opencode serve (docs/log/27 §3 / §7, P2). It starts and
// watches one shared `opencode serve` process (HTTP + SSE on loopback) and provides the
// runtime for managed sessions. Its only responsibility is the PROCESS LIFETIME — what a
// thread (an opencode session) is made to do belongs to driver.go's ThreadHandle.
//
// Measured (1.17.18, docs/log/27 §12.2):
//   - Auth: every request carries Basic auth (user `opencode`). A new daemon starts with a
//     random per-generation OPENCODE_SERVER_PASSWORD that both majors read (measured on
//     1.19.0 and 2.0.28: 401 without it, SSE included); serveapi.go's transport adds it to
//     every client, and the file it is kept in lets a restarted Agent adopt the daemon. On v1
//     the TUI route never attaches to this daemon (buildProgram opens its own store). On v2
//     Terminal sessions do (shared.go): their TUIs read the password from the pane
//     environment, so a v2 daemon keeps ONE password across generations, because a TUI whose
//     server restarted reconnects only with the password it started with. The password is not
//     a boundary against the agent itself: the serve process environment and the file are
//     readable by the same uid.
//   - Major version: the pin is v1, but a probe also recognises v2 and the driver then
//     routes to /api/* (serveapi.go holds the table and the measured differences).
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
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// serveStartTimeout bounds the wait for a spawned daemon to answer its probe.
var serveStartTimeout = 20 * time.Second

const serveAddrEnv = "AF_OPENCODE_SERVE_ADDR"

// sharedServeEnv is set in the environment of a daemon af started for shared Terminal sessions.
const sharedServeEnv = "AF_OPENCODE_SHARED_SERVE"

// defaultServeAddr sits next to the codex app-server (:7798). It is loopback inside the
// container's network namespace, so the collision risk is limited to processes in this
// container.
const defaultServeAddr = "http://127.0.0.1:7799"

// serveClient is the short-timeout HTTP client for the control calls
// (create/abort/question/status). Only the blocking /message (running a turn, no time
// limit) uses a client of its own, in driver.go.
var serveClient = &http.Client{Timeout: 10 * time.Second, Transport: authTransport{}}

// Supervisor owns the shared `opencode serve` daemon: idempotent start, health,
// generation counter, drain and restart (the RuntimeSupervisor of docs/log/27 §3).
type Supervisor struct {
	mu  sync.Mutex
	gen int       // runtime generation (§7); ++ every time Ensure starts a new process
	cmd *exec.Cmd // this generation's child process (nil for an adopted one)
	up  bool
	// stopping marks a deliberate teardown (Restart/Shutdown) so the waiter doesn't
	// record it as a crash or trigger reconciliation.
	stopping bool
	// askedStop is the exact process this supervisor asked to end (stopIfIdle / Restart /
	// Shutdown). Process identity is the only honest test of "was this death deliberate":
	// deriving it from s.cmd files every crash as an orderly stop, because a daemon that
	// dies on its own is replaced by the next Ensure — the Console polls Resume — before
	// the waiter can take the lock. Measured: 727 daemon generations in one workspace log,
	// not one exit record among them.
	askedStop *exec.Cmd
	// watching: one zero-demand watcher (agents.WatchIdle, idlestop.go) is running.
	watching bool
}

var supervisor = &Supervisor{}

// dependents is the total number of things that need the shared daemon.
//
// On v1 the TUI route does not use serve (buildProgram connects directly to its own SQLite
// store), so only managed handles are counted. On v2 every live Terminal pane is a dependent
// as well (sharedTerminals): its TUI is attached to this daemon, and the idle stop must not
// pull it out from under it. The OAuth device flow is the one more exception: it is the only
// path that starts the daemon while still unconnected, so unless it counts as demand for its
// duration, the daemon gets folded up from under a login in progress.
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
	return n + sharedTerminals()
}

// idleGraceEnv / defaultIdleGrace: fold the daemon up after this much continuous zero
// demand; 0 disables the automatic stop. serve is a measured ~305 MB RSS, so there is no
// reason to let it sit there for as long as managed goes unused.
const idleGraceEnv = "AF_OPENCODE_SERVE_IDLE_SEC"
const defaultIdleGrace = 2 * time.Minute

// --- lifecycle ledger --------------------------------------------------------
//
// §9.5 makes the log the generation ledger, but in a workspace that log is the container's
// stdout, which nothing inside the container can read — not the Console, not the MCP
// tools, not a shell. A daemon that keeps being replaced kills whatever turn it lands on,
// so the transitions are kept here too and ride out on GET /connections next to
// last_limit; otherwise that failure is only diagnosable from outside the container.

const lifecycleKeep = 16

// LifecycleEvent is one daemon transition: what happened, when, and who asked for it.
type LifecycleEvent struct {
	At     string `json:"at"`            // RFC3339
	Event  string `json:"event"`         // started | adopted | stopped | restart | died
	Gen    int    `json:"gen,omitempty"` // the runtime generation it refers to
	Reason string `json:"reason,omitempty"`
}

var (
	lifecycleMu sync.Mutex
	lifecycle   []LifecycleEvent
)

func recordLifecycle(event string, gen int, reason string) {
	lifecycleMu.Lock()
	defer lifecycleMu.Unlock()
	lifecycle = append(lifecycle, LifecycleEvent{
		At: time.Now().Format(time.RFC3339), Event: event, Gen: gen, Reason: reason,
	})
	if len(lifecycle) > lifecycleKeep {
		lifecycle = lifecycle[len(lifecycle)-lifecycleKeep:]
	}
}

// Lifecycle returns the recent daemon transitions, oldest first.
func Lifecycle() []LifecycleEvent {
	lifecycleMu.Lock()
	defer lifecycleMu.Unlock()
	return append([]LifecycleEvent(nil), lifecycle...)
}

// --- pending restart ----------------------------------------------------------
//
// Provider keys arrive as environment (cmd.Env) and the engine provider block as a config
// file, so `opencode serve` reads both once, at start: only a new process picks a change
// up. Applying one on our own initiative costs whoever is mid-turn — the drain kills a
// running turn after drainTimeout — and the change is rarely urgent, so the choice of
// moment belongs to the person who made it. The change is recorded here and the Console
// offers the restart (Settings → Agents).

// pendingReasonsKeep bounds the list shown in the Console; the count is what matters, not
// every entry.
const pendingReasonsKeep = 8

// PendingRestart is the set of changes waiting for a new serve process.
type PendingRestart struct {
	Reasons []string `json:"reasons"`
	Since   string   `json:"since"` // RFC3339, when the first one landed
}

var (
	pendingMu      sync.Mutex
	pendingRestart PendingRestart
)

// NotePendingRestart records a change only a new serve process will pick up.
//
// It is deliberately a no-op while no daemon is running: there is nothing stale to replace,
// and the next start reads the new value anyway. Saying "restart to apply" about a daemon
// that does not exist is the kind of false alarm that teaches people to ignore the notice.
func NotePendingRestart(reason string) {
	if !supervisor.Running() {
		return
	}
	pendingMu.Lock()
	defer pendingMu.Unlock()
	if pendingRestart.Since == "" {
		pendingRestart.Since = time.Now().Format(time.RFC3339)
	}
	for _, r := range pendingRestart.Reasons {
		if r == reason {
			return // the same setting toggled twice is still one pending change
		}
	}
	pendingRestart.Reasons = append(pendingRestart.Reasons, reason)
	if len(pendingRestart.Reasons) > pendingReasonsKeep {
		pendingRestart.Reasons = pendingRestart.Reasons[len(pendingRestart.Reasons)-pendingReasonsKeep:]
	}
}

// PendingRestartInfo returns the changes waiting to be applied, and whether there are any.
func PendingRestartInfo() (PendingRestart, bool) {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	if len(pendingRestart.Reasons) == 0 {
		return PendingRestart{}, false
	}
	out := PendingRestart{Since: pendingRestart.Since, Reasons: append([]string(nil), pendingRestart.Reasons...)}
	return out, true
}

// ClearPendingRestart forgets the pending changes — they have been applied, or the daemon
// they were stale against is gone.
func ClearPendingRestart() {
	pendingMu.Lock()
	pendingRestart = PendingRestart{}
	pendingMu.Unlock()
}

// Running reports whether a daemon is up, which decides whether a setting change is stale
// (NotePendingRestart) or will simply be read at the next start.
func (s *Supervisor) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.up
}

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

// healthy is a cheap liveness probe of either major (serveapi.go): a JSON answer that names
// the server, not a status code, because the other major's paths answer 200 with HTML.
func healthy(addr string) bool { return probeServe(addr) }

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
	// One reading for this start: the binary and the migration check both come from it.
	spec := CurrentLaunch()
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
		recordLifecycle("adopted", s.gen, addr)
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
	// Something already answers 401 on the port: a daemon whose password we lost. Spawning
	// would only collide, and rotating the password would make the loss permanent.
	if serveRejectsAuth(addr) {
		return "", 0, fmt.Errorf("opencode serve が %s で認証を要求しています（パスワードが一致しません）。起動済みのデーモンを止めてください", addr)
	}
	// The candidate is only adopted for the probe below; the previous password comes back
	// unless the new daemon proves it holds this one, so a failed start (or an Ensure that
	// raced a slow-but-alive daemon) cannot lock the Agent out of it.
	prevPassword := servePassword()
	// Decided once: the password policy and the environment flag must agree, and a second read
	// could see a Terminal slot come or go in between.
	sharedWanted := sharedTUI() || sharedTerminals() > 0
	password, err := nextServePassword(prevPassword, sharedWanted)
	if err != nil {
		return "", 0, fmt.Errorf("opencode serve のパスワードを用意できません: %w", err)
	}
	useServePassword(password)
	started := false
	defer func() {
		if !started {
			useServePassword(prevPassword)
		}
	}()
	// A database still holding 1.x data is migrated before the daemon opens it (migrate.go).
	if err := PrepareStart(spec); err != nil {
		return "", 0, err
	}
	cmd := exec.Command(spec.Bin, "serve", "--hostname", host, "--port", port)
	// Provider keys are env-injected (auth.go) — the same set a TUI launch gets, so
	// managed turns authenticate identically. Re-keying requires a Restart (§7). The
	// auth variables come last: a duplicate key keeps its final value.
	cmd.Env = append(append(withoutEnv(os.Environ(), "AF_SESSION_SID", "AF_SESSION_NAME"), spec.Env()...),
		"OPENCODE_SERVER_USERNAME="+serveUser, "OPENCODE_SERVER_PASSWORD="+password)
	if sharedWanted {
		// Tells the plugins this is af's shared server: no AF_SESSION_SID here, sessions are told
		// apart by metadata, and shell commands wait for their environment (agent-fleet-shared.js).
		cmd.Env = append(cmd.Env, sharedServeEnv+"=1")
	}
	cmd.Dir = paths.HomeDir()
	tail, err := agents.StartWithStderrTail(cmd)
	if err != nil {
		return "", 0, fmt.Errorf("opencode serve の起動に失敗しました: %w", err)
	}
	defer tail.Settle() // after any failure snapshot; see StderrTail.Release
	deadline := time.Now().Add(serveStartTimeout)
	for time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
		if healthy(addr) {
			started = true
			if err := persistServePassword(password); err != nil {
				log.Printf("opencode serve: password not saved, a restarted Agent cannot adopt this daemon: %v", err)
			}
			// Environments go back BEFORE the daemon counts as up: the Console polls Resume and
			// the reconnecting TUIs submit as soon as they can.
			restoreTerminalEnvs(addr)
			s.gen++
			s.cmd = cmd
			s.up = true
			s.stopping = false
			gen := s.gen
			s.armIdleWatchLocked()
			go s.waitDaemon(cmd, tail, gen)
			go s.monitorEvents(addr, gen)
			log.Printf("opencode serve: started (gen %d, pid %d, %s)", gen, cmd.Process.Pid, addr)
			recordLifecycle("started", gen, fmt.Sprintf("pid %d", cmd.Process.Pid))
			return addr, gen, nil
		}
	}
	err = tail.Wrap(errors.New("opencode serve が時間内に起動しませんでした")) // before the kill adds noise
	_ = cmd.Process.Kill()
	_ = cmd.Wait() // reap: waitDaemon is only started on success — Kill alone leaves a zombie
	tail.Release()
	return "", 0, err
}

// armIdleWatchLocked starts the "fold up on zero demand" watcher, at most one at a time.
// The caller must hold s.mu.
func (s *Supervisor) armIdleWatchLocked() {
	if s.watching {
		return
	}
	s.watching = true
	idleWatch.start(func(quit <-chan struct{}) {
		agents.WatchIdleUntil(quit, "opencode serve", dependents, s.stopIfIdle,
			agents.IdleGrace(idleGraceEnv, defaultIdleGrace))
	})
}

// idleWatch tracks the zero-demand watchers so a test can end them (stop) before it swaps a
// global they read, such as terminalSlots; production never calls stop.
var idleWatch idleWatchers

type idleWatchers struct {
	mu   sync.Mutex
	quit chan struct{}
	wg   sync.WaitGroup
}

func (w *idleWatchers) start(run func(quit <-chan struct{})) {
	w.mu.Lock()
	if w.quit == nil {
		w.quit = make(chan struct{})
	}
	quit := w.quit
	w.wg.Add(1)
	w.mu.Unlock()
	go func() {
		defer w.wg.Done()
		run(quit)
	}()
}

// stop ends every watcher started so far and waits for them to return.
func (w *idleWatchers) stop() {
	w.mu.Lock()
	if w.quit != nil {
		close(w.quit)
		w.quit = nil
	}
	w.mu.Unlock()
	w.wg.Wait()
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
	gen := s.gen // read under the lock: the ledger write below runs without it
	s.stopping, s.up, s.watching = true, false, false
	s.askedStop = cmd
	s.mu.Unlock()

	if cmd == nil {
		// An adopted daemon has no process handle, so it cannot be signalled. Just
		// step off the watch; it will apply the next time we start an owned one.
		log.Printf("opencode serve: zero demand, but this daemon was adopted and cannot be stopped")
	} else {
		// Zero demand means no managed turn is running, so no drain is needed.
		stopProcess(cmd, addr)
		log.Printf("opencode serve: stopped (zero demand)")
		recordLifecycle("stopped", gen, "zero demand")
	}
	s.mu.Lock()
	s.cmd = nil
	s.mu.Unlock()
	// stopping is deliberately NOT cleared, so a late SSE disconnect (monitorEvents) is
	// not misread as an unintended loss. The next successful Ensure resets it to false.
	return true
}

// nextServePassword is the password a daemon about to start gets. A daemon that Terminal TUIs
// attach to keeps its password across generations: a TUI whose server went away retries forever
// and reconnects only with the password it started with (measured on 2.0.28). Every other daemon
// gets a fresh one.
func nextServePassword(prev string, shared bool) (string, error) {
	if shared && prev != "" {
		return prev, nil
	}
	return genServePassword()
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
func (s *Supervisor) waitDaemon(cmd *exec.Cmd, tail *agents.StderrTail, gen int) {
	err := cmd.Wait()
	tail.Release()
	s.mu.Lock()
	// Deliberate means WE asked THIS process to end. Comparing gen alone would misread a
	// Restart (it clears stopping without changing gen), and comparing s.cmd would call
	// every crash deliberate the moment a concurrent Ensure replaced it — see askedStop.
	deliberate := s.askedStop == cmd
	if s.askedStop == cmd {
		s.askedStop = nil
	}
	if s.cmd == cmd {
		s.up = false
		s.cmd = nil
	}
	s.mu.Unlock()
	if deliberate {
		return // Restart/Shutdown own this teardown; they log/rebuild themselves
	}
	// sig comes from the wait status alone: a daemon that exits normally with 143 on its own
	// is a crash, and reading the signal back out of the exit code would file it as stopped.
	code, sig := 0, 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				sig = int(ws.Signal())
				code = 128 + sig
			} else {
				code = ee.ExitCode()
			}
		} else {
			code = -1
		}
	}
	oomNow, okOOM := status.OOMKillCount()
	// Generation history (§9.5 operational metadata): in P2 the log IS the ledger.
	log.Printf("opencode serve: daemon died unexpectedly (gen %d, code %d, sig %d, oomCount ok=%v)", gen, code, sig, okOOM)
	recordLifecycle("died", gen, fmt.Sprintf("code %d, sig %d", code, sig))
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
	goReconcile("daemon death")
}

// Restart is the path that applies an auth or config change (§7): generation++ → drain →
// respawn → re-resume every handle. The daemon is shared, so the drain is a switchover
// window for every opencode managed session in the workspace at once (by design, §7).
// replaced reports whether the daemon process was really replaced. An adopted daemon has no
// process handle to signal, so the restart cannot apply a new environment; the caller must
// say so rather than report success, because the whole point of a restart is the change it
// carries.
func (s *Supervisor) Restart(reason string) (replaced bool) {
	s.mu.Lock()
	if !s.up {
		s.mu.Unlock()
		// Not running: nothing to replace and nothing stale — the next Ensure reads the
		// new configuration on its own.
		ClearPendingRestart()
		return true
	}
	addr := serveAddr()
	cmd := s.cmd
	s.stopping = true
	s.askedStop = cmd
	s.mu.Unlock()

	log.Printf("opencode serve: restart requested (%s) — draining", reason)
	recordLifecycle("restart", s.Generation(), reason)
	s.drain(addr)
	if cmd == nil {
		// Adopted: no process handle to signal, so nothing was replaced and the old
		// environment is still in force. Leaving s.up alone matters — clearing it would send
		// the next Ensure to adopt the very same daemon again, gaining nothing.
		s.mu.Lock()
		s.stopping = false
		s.mu.Unlock()
		return false
	}
	stopProcess(cmd, addr)
	s.finishTeardown(cmd)
	goReconcile("restart: " + reason)
	ClearPendingRestart()
	return true
}

// finishTeardown drops the supervisor's view of cmd — but only while cmd is still the
// process it describes.
//
// Restart deliberately holds no lock across drain and stopProcess, both of which take
// seconds, and Ensure runs under that lock with the Console calling Resume about once a
// second. So a replacement can legitimately be installed while the old process is being
// signalled. Clearing s.cmd unconditionally then orphans that live daemon: it keeps serving
// while the supervisor believes it owns nothing, which costs the idle stop, exit recording,
// and any further restart — including the button that started this.
func (s *Supervisor) finishTeardown(cmd *exec.Cmd) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopping = false
	if s.cmd != cmd {
		return // somebody already installed a replacement; it is theirs, not ours to forget
	}
	s.up = false
	s.cmd = nil
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
	s.askedStop = cmd
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

// drain waits for the busy sessions to go idle, then aborts the stragglers: managed handles and,
// on v2, the live Terminal slots, whose turns run on this same daemon.
func (s *Supervisor) drain(addr string) {
	deadline := time.Now().Add(drainTimeout())
	for time.Now().Before(deadline) {
		if len(busyManaged(addr)) == 0 && len(busyTerminals(addr)) == 0 {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	for _, h := range busyManaged(addr) {
		log.Printf("opencode serve: drain timeout — aborting session %s", h.sessionID())
		// Mark it ours BEFORE the abort lands, or runTurn wakes up to an abort it cannot
		// account for and reports the turn as killed by something unknown.
		h.mu.Lock()
		h.abortAsked = true
		h.mu.Unlock()
		abortSession(addr, h.sessionID())
	}
	for _, ses := range busyTerminals(addr) {
		log.Printf("opencode serve: drain timeout — aborting terminal session %s", ses)
		abortSession(addr, ses)
	}
	// Give the aborts a moment to unwind the blocked turn goroutines.
	time.Sleep(2 * time.Second)
}

// busyTerminals lists the opencode session ids of the live Terminal slots that are running a
// turn. The v2 active list is global, so one read covers every slot.
func busyTerminals(addr string) []string {
	slots := terminalSlots()
	if len(slots) == 0 {
		return nil
	}
	m, err := activeSessions(addr)
	if err != nil {
		return nil
	}
	var out []string
	for _, slot := range slots {
		if ses := sids.Read(session.UUID(slot.Dir, slot.Name)); ses != "" {
			if st, ok := m[ses]; ok && st != "idle" {
				out = append(out, ses)
			}
		}
	}
	return out
}

// busyManaged lists the opencode sessions that are (a) busy per the runtime and
// (b) owned by a managed handle — a TUI-attached user's own experiments outside AF
// sessions are not ours to wait on.
func busyManaged(addr string) []*threadHandle {
	hs := liveHandles()
	if len(hs) == 0 {
		return nil
	}
	m, err := activeSessions(addr)
	if err != nil {
		return nil
	}
	var out []*threadHandle
	for _, h := range hs {
		// Measured: the active list omits idle, so being listed = busy/retry.
		if st, ok := m[h.sessionID()]; ok && st != "idle" {
			out = append(out, h)
		}
	}
	return out
}

func abortSession(addr, ses string) { _ = serveAbort(addr, ses) }

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

// monitorEvents subscribes to the serve's cross-project SSE stream (GET /global/event, /api/event on v2)
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
				goReconcile("adopted daemon lost")
			}
			return
		}
		// Daemon alive, socket dropped (transient): resubscribe.
		time.Sleep(time.Second)
	}
}

// sseClient has no timeout, and carries the same auth as every other call.
var sseClient = &http.Client{Transport: authTransport{}}

// streamEvents reads one SSE connection until it breaks. Returns the read error.
func (s *Supervisor) streamEvents(addr string, gen int) error {
	// Whatever ends this stream (EOF, a failed or refused reconnect, a hang) leaves a window
	// in which the daemon can start a tool nobody hears about, so the in-flight sets stop being
	// trusted the moment the stream is gone, not when the next one connects. A superseded
	// generation's monitor must not touch the handles of the current one.
	defer func() {
		if s.Generation() == gen {
			for _, h := range liveHandles() {
				h.tools.Unknown()
			}
		}
	}()
	req, err := http.NewRequest("GET", eventURL(addr), nil)
	if err != nil {
		return err
	}
	// No timeout: SSE is a long-lived stream (heartbeats keep it warm).
	res, err := sseClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("event stream: HTTP %d", res.StatusCode) // a 401 must not read as a clean EOF
	}
	// Events are not replayed across a reconnect, so a tool call announced during the gap is
	// invisible: the in-flight sets are unknown until a turn boundary shows up on this stream.
	for _, h := range liveHandles() {
		h.tools.Unknown()
	}
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

// withoutEnv returns environ without the named variables.
func withoutEnv(environ []string, names ...string) []string {
	out := make([]string, 0, len(environ))
next:
	for _, kv := range environ {
		for _, n := range names {
			if strings.HasPrefix(kv, n+"=") {
				continue next
			}
		}
		out = append(out, kv)
	}
	return out
}
