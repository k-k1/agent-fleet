package muse

import (
	"errors"
	"io"
	"log"
	"os/exec"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// ledger makes a resend idempotent: the same ClientMessageID never starts two turns.
var ledger = agents.NewMsgLedger("muse-msgledger")

// NewDriver returns the muse managed driver for the driver registry.
func NewDriver() agents.Driver { return managedDriver{} }

type managedDriver struct{ agentImpl }

// Capabilities.
//
// ProcessModel is per-session-child: MSP can host several sessions per process, but the
// posture a host fixes for its lifetime includes --trust-workspace, and trust is a decision
// about a WORKING COPY. A shared host would trust the first session's repository on behalf of
// every repository loaded after it, which is not a trade to make for 73 MiB idle
// (ADR 0095 decision 3).
//
// Steer, DynamicModel and DynamicEffort are true because the wire really carries them
// (turn/steer, session/setModel, session/setReasoningEffort) and UpdateSettings below calls
// them. DynamicMode is false: Mode is AF's plan mode, and MSP has no method that sets it —
// session/setApprovalMode changes the approval posture, which decision 5 already maps to the
// launch-time permission choice. Reading one wire method as two different AF axes is how a
// capability table starts lying.
//
// Permissions is true, and muse is the first kind to declare it. Decision 13 calls it the one
// genuinely new capability, and the condition for claiming it is the same one docs/log/76 sets
// for Caps.PermissionChoice: a pending approval can actually be ANSWERED from the Console. It
// can — the driver raises an approval-kind Interaction, the read layer sends it out as
// `pendingApproval`, and the mirror's ApprovalCard answers it allow/deny through /respond. A
// session blocked on a tool is the one state where an unanswerable dialog reads to the member
// as a frozen session.
//
// Questions is separately true and is a DIFFERENT channel: userInput/requested maps onto AF's
// existing question interaction field for field. Fork stays false until the fork path is built.
func (managedDriver) Capabilities() agents.Capabilities {
	return agents.Capabilities{
		ProcessModel:  "per-session-child",
		Steer:         true,
		DynamicModel:  true,
		DynamicEffort: true,
		Questions:     true,
		Permissions:   true,
	}
}

// Resume returns the session's ThreadHandle, spawning `muse serve` and starting or reloading
// the muse session when needed.
func (managedDriver) Resume(m session.Meta) (agents.ThreadHandle, error) {
	if m.Kind != session.KindMuse {
		return nil, errors.New("muse driver は muse セッション専用です")
	}
	if !session.DirExists(m.Dir) {
		return nil, agents.DirGoneErr(m.Dir)
	}
	if !Installed() {
		return nil, errors.New("Muse Code がインストールされていません（workspace-agent install-muse）")
	}
	// Fail-close, and here rather than in StartManagedSession: Resume is reached directly from
	// the turn, answer, carried-session and bridge paths and from the boot-time
	// ReconcileManaged, so a restart or a dead child would otherwise start an unclamped host —
	// eight subagents, workflows and the bundled foreign readers all enabled. Unlike MCP
	// materialisation, which logs and launches anyway by design, this refuses the start.
	if err := EnsureClamps(); err != nil {
		return nil, err
	}

	handlesMu.Lock()
	h := handles[m.Name]
	if h == nil {
		h = &threadHandle{
			name:    m.Name,
			dir:     m.CWD(),
			slotSid: slotSid(m),
			events:  make(chan agents.Event, 64),
			state:   agents.TurnUnknown,
		}
		handles[m.Name] = h
	}
	handlesMu.Unlock()

	h.mu.Lock()
	alive := h.alive && h.cl != nil
	h.mu.Unlock()
	if alive {
		return h, nil
	}

	// Serialize spawns per handle: boot's ReconcileManaged and a /turn arriving right after it
	// both call Resume, and an unserialized check-then-spawn starts two hosts for one session.
	h.spawnMu.Lock()
	defer h.spawnMu.Unlock()
	h.mu.Lock()
	if h.alive && h.cl != nil {
		h.mu.Unlock()
		return h, nil
	}
	if h.settings.Model == "" {
		h.settings.Model = m.Model
	}
	if h.settings.Effort == "" {
		h.settings.Effort = m.Effort
	}
	// Whether to skip the permission prompt is resolved from meta and ui-prefs on every
	// Resume, not carried in ThreadSettings: "empty means unchanged" cannot make a bool
	// three-valued, so a re-spawn after a settings change still uses the value resolved here.
	h.bypass = agents.SkipPermissions(m)
	st := h.settings
	h.mu.Unlock()

	if err := h.spawn(st); err != nil {
		return nil, err
	}

	base, _ := status.OOMKillCount()
	status.PersistExit(m.Name, status.ExitInfo{OOMBase: base})
	return h, nil
}

// --- handle registry ---------------------------------------------------------

var handlesMu sync.Mutex
var handles = map[string]*threadHandle{}

func handleFor(name string) *threadHandle {
	handlesMu.Lock()
	defer handlesMu.Unlock()
	return handles[name]
}

func liveHandles() []*threadHandle {
	handlesMu.Lock()
	defer handlesMu.Unlock()
	var out []*threadHandle
	for _, h := range handles {
		h.mu.Lock()
		alive := h.alive
		h.mu.Unlock()
		if alive {
			out = append(out, h)
		}
	}
	return out
}

// DropHandle detaches a managed session from its host (stop / halt / archive / recreate):
// interrupt any running turn, close the child's stdin so it exits on its own terms, forget
// the handle. The conversation stays in muse's own session.jsonl, and a later Resume reloads
// it with session/resume.
func DropHandle(name string) {
	handlesMu.Lock()
	h := handles[name]
	delete(handles, name)
	handlesMu.Unlock()
	if h == nil {
		return
	}
	h.mu.Lock()
	h.alive = false
	h.queue = nil
	cmd, stdin, running := h.cmd, h.stdin, h.running
	h.mu.Unlock()
	if running {
		_ = h.Interrupt()
	}
	stopChild(cmd, stdin)
}

// RemoveLedger drops the ClientMessageID ledger (/stop only — halt and archive keep it
// because they can be resumed).
func RemoveLedger(name string) { ledger.Remove(name) }

// ManagedAlive reports whether the session has a live host.
func ManagedAlive(name string) bool {
	h := handleFor(name)
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.alive
}

// ManagedBusy reports a turn running or queued — the refusal condition for an exclusive
// driver switch, and the wait condition for graceful shutdown.
func ManagedBusy(name string) bool {
	h := handleFor(name)
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.running || len(h.queue) > 0
}

// AbortManaged interrupts every running managed turn (the equivalent of the per-pane Ctrl-C
// in graceful shutdown).
func AbortManaged() {
	for _, h := range liveHandles() {
		h.mu.Lock()
		running := h.running
		h.mu.Unlock()
		if running {
			_ = h.Interrupt()
		}
	}
}

// Shutdown terminates every host on agent exit. The conversation of record is muse's own
// session.jsonl, so the next boot's ReconcileManaged reattaches with session/resume.
func Shutdown() {
	handlesMu.Lock()
	type child struct {
		cmd   *exec.Cmd
		stdin io.Closer
	}
	var kids []child
	for _, h := range handles {
		h.mu.Lock()
		h.alive = false
		kids = append(kids, child{h.cmd, h.stdin})
		h.mu.Unlock()
	}
	handlesMu.Unlock()
	for _, c := range kids {
		stopChild(c.cmd, c.stdin)
	}
}

// ReconcileManaged re-attaches managed muse sessions after an Agent boot or a child death.
// It is mandatory for a per-session-child kind: without it a restart leaves every session
// showing as live with no host behind it. On failure the session stays stopped and the
// member's resume click retries.
func ReconcileManaged(reason string) {
	d := managedDriver{}
	for _, m := range session.ListMetas() {
		if m.Kind != session.KindMuse || m.DriverKind() != session.DriverManaged || m.Archived {
			continue
		}
		if m.StoppedAt != "" && handleFor(m.Name) == nil {
			continue // deliberately stopped — resume only on user action
		}
		if ManagedAlive(m.Name) {
			continue
		}
		if _, err := d.Resume(m); err != nil {
			log.Printf("muse: reconcile (%s) %s: %v", reason, m.Name, err)
		}
	}
}

// stopChild closes stdin and waits briefly for the host to exit on its own before killing it.
// The graceful path matters: muse holds a flock on its settings file while it writes, and a
// SIGKILL mid-write is how a lock outlives the process that took it.
func stopChild(cmd *exec.Cmd, stdin io.Closer) {
	if stdin != nil {
		_ = stdin.Close()
	}
	if cmd == nil || cmd.Process == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		_, _ = cmd.Process.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
	}
}
