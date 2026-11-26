package sessionx

// docs/log/27 P3: exclusive driver switching for an existing session
// (POST /sessions/{name}/driver). This implements codex's "the CLI route is always available,
// switching goes both ways (tui ⇄ managed)" (§2) and works the same way for opencode (which
// needs no exclusion, but shares the semantics: stop the old driver and resume the same
// conversation on the new one).
//
// A switch always goes stop → drain → resume (§2). The drain is minimal: while a turn is
// running or queued the request is refused with 409 busy_switch, leaving "wait for idle (or
// interrupt with the stop button)" to the user, because silently aborting a running turn
// behind a switch click is the most surprising behaviour available.
//
// Conversation identity is carried by the per-slot sid store: going managed does a
// thread/resume on the same thread id, and going tui rides BuildLaunch's usual resume
// (codex resume <id> / opencode --session <id>). §12.3 measured the codex TUI opening a
// server-created thread through `--remote`; the direct launch reads the same rollout under
// $CODEX_HOME but has not been measured on that path.

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agentreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/fleetgraph"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

type driverReq struct {
	Driver string `json:"driver"` // "tui" ("" accepted too) | "managed"
}

func HandleSessionDriver(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	// A switch of driver stops the old runtime and relaunches: no pane in between.
	defer markLaunching(name)()
	lease, ok := enterSessionGate(w, r, name)
	if !ok {
		return
	}
	defer lease.Release()
	var req driverReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_body", "invalid JSON body")
		return
	}
	target := strings.TrimSpace(req.Driver)
	switch target {
	case "", session.DriverTUI:
		target = session.DriverTUI
	case session.DriverManaged:
	default:
		httpx.WriteErr(w, http.StatusBadRequest, "bad_driver", "unknown driver: "+req.Driver)
		return
	}
	m, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	if m.DriverKind() == target {
		httpx.WriteJSON(w, http.StatusOK, wireSession(m, tmuxx.HasSession(session.TmuxName(name)) || ManagedAlive(m)))
		return
	}
	// A route that holds only under one execution method (agents.RouteDriverChecker) refuses the
	// switch before anything is stopped.
	if ref := checkRouteDriver(m.Kind, m.Route, target); ref != nil {
		httpx.WriteErr(w, ref.Status, ref.Code, ref.Message)
		return
	}
	if target == session.DriverManaged {
		if _, ok := driverOf(m); !ok {
			httpx.WriteErr(w, http.StatusBadRequest, "driver_unsupported",
				msgDriverUnsupported)
			return
		}
	} else if AgentOf(m.Kind).Caps().ManagedOnly {
		// The symmetric refusal (ADR 0093 decision 2): a kind with no Terminal(CLI) route at
		// all can never be switched TO tui, unlike the managed-unsupported case above where
		// the kind might grow a driver later.
		httpx.WriteErr(w, http.StatusBadRequest, "driver_unsupported",
			msgNoTerminalRoute)
		return
	}

	// codex goes to Terminal only from a stopped session: the TUI cannot open a thread the
	// shared app-server still has loaded, and it unloads one only about a minute after the stop
	// (codex/release.go). Refusing here keeps a prompt from being sent into either side
	// meanwhile.
	// The claim comes first: a Resume that finishes before it shows up as alive below, and one
	// in flight makes the claim fail.
	if m.DriverKind() == session.DriverManaged && target == session.DriverTUI && terminalSwitchNeedsStopped(m.Kind) {
		end, ok := beginCodexTerminalSwitch(name)
		if !ok {
			httpx.WriteErr(w, http.StatusConflict, errCodeDriverSwitching, errDriverSwitching.Error())
			return
		}
		defer end()
		if switchSourceAlive(m) {
			httpx.WriteErr(w, http.StatusConflict, errCodeCodexStopFirst,
				msgCodexStopFirst)
			return
		}
	}

	// Drain condition: never take a running (or queued) turn with us. For tui that is the
	// status store's working state (from hooks), for managed the handle's running/queue.
	sid := session.UUID(m.Dir, name)
	if m.DriverKind() == session.DriverTUI {
		if st, ok := status.Read(sid); ok && st.State == "working" {
			httpx.WriteErr(w, http.StatusConflict, "busy_switch",
				msgBusySwitch)
			return
		}
	} else if managedBusy(m) {
		httpx.WriteErr(w, http.StatusConflict, "busy_switch",
			msgBusySwitch)
		return
	}

	// The relaunch below starts the session again on the other driver: a person's start. Held
	// across it, and refused on a session a retire has marked (resume it first), so no relaunch
	// leaves a marked session running.
	releaseRelaunch, rerr := holdPersonRelaunch(name)
	if rerr != nil {
		httpx.WriteErr(w, http.StatusConflict, "session_retired", rerr.Error())
		return
	}
	defer releaseRelaunch()
	// The relaunch below starts the CLI: refuse it with the reason, before the old runtime is
	// stopped, its held input dropped or its status cleared, when that CLI failed to install.
	if ref := startRefusalOf(m); ref != nil {
		httpx.WriteErr(w, ref.Status, ref.Code, ref.Message)
		return
	}
	// The launch the target driver would refuse is refused now, not after the old runtime is
	// stopped and its held input dropped.
	if g, ok := agentreg.Lookup(NormalizeKind(m.Kind)); ok {
		if lg, ok := g.(agents.LaunchGuard); ok {
			mt := m
			if target == session.DriverManaged {
				mt.Driver = session.DriverManaged
			} else {
				mt.Driver = ""
			}
			if err := lg.LaunchRefusal(mt); err != nil {
				httpx.WriteErr(w, http.StatusConflict, "launch_refused", err.Error())
				return
			}
		}
	}
	// Before the stop, so a refusal leaves the session as it was: from the stop to the relaunch
	// the session runs nowhere, which a delete must not take for "stopped" (reserveLaunch).
	releaseLaunch, lref := reserveLaunch(m, launchExisting)
	if lref != nil {
		httpx.WriteErr(w, lref.Status, lref.Code, lref.Message)
		return
	}
	defer releaseLaunch()
	// stop: bring the old driver down (single-writer exclusion, §2 — never leave two
	// writers on one thread). The old runtime's route token goes with it; the relaunch mints
	// its own.
	endRouteTokens(name)
	if tn := session.TmuxName(name); tmuxx.HasSession(tn) {
		disconnectRemoteControl(name, m)
		if out, err := tmuxx.Cmd("kill-session", "-t", session.ExactTarget(tn)).CombinedOutput(); err != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, "tmux_failed", string(out))
			return
		}
	}
	// Stop the old managed runtime. A kind whose lock guards the session cross-process (kiro)
	// waits for it in dropForTerminalSwitch on a managed→TUI switch, so the TUI's relaunch below
	// does not race it into an error or a split-brain new sid. Other kinds / directions drop
	// asynchronously.
	if !(m.DriverKind() == session.DriverManaged && target == session.DriverTUI && dropForTerminalSwitch(m.Kind, name)) {
		dropManagedRuntime(m)
	}
	if target == session.DriverTUI {
		// Held inputs are delivered by a Managed start, and Terminal has no queue to put them
		// in: typing them into the CLI as it boots would race its own startup.
		agents.DropHeld(name, agents.DropTerminal)
	}
	status.Remove(sid)

	// flip → resume: restart the same conversation on the new driver. The new Driver lives only
	// in m until the launch succeeds, so a failure leaves the meta on disk on the old driver and
	// returns the session stopped: the conversation itself is untouched, so the user can resume
	// on the old driver. (Writing m back on failure would only roll back what changed meanwhile.)
	if target == session.DriverManaged {
		m.Driver = session.DriverManaged
	} else {
		m.Driver = "" // tui persists as "" (the convention that keeps existing meta byte-identical)
	}
	if target == session.DriverManaged {
		// Unguarded: the meta on disk still says Terminal until this launch succeeds, which the
		// guard would read as a stale caller (switch_guard.go).
		d := managedDrivers[m.Kind]
		if _, err := mcpx.StartManagedSession(d, m); err != nil {
			writeRuntimeErr(w, err)
			return
		}
	} else {
		if err := startSessionTmux(m, false); err != nil {
			if !writeCodexReleasingErr(w, err) {
				writeTmuxLaunchErr(w, err)
			}
			return
		}
	}
	// The stop/relaunch above takes seconds, so the switch is written onto the meta as it is
	// now: m written back would roll back a lock set meanwhile (issue #950).
	wasStopped := false
	cur, ok := UpdateSessionMeta(name, func(cur *session.Meta) bool {
		// Judged on disk too: a list poll during the switch may have stamped the stop (and
		// recorded the death) that this revive answers.
		wasStopped = cur.StoppedAt != ""
		cur.Driver = m.Driver
		cur.StoppedAt = ""
		return true
	})
	if !ok {
		// Deleted during the relaunch: the delete halted whatever was running then, and the
		// runtime started above has no meta to belong to. Stop it rather than answer 200 for a
		// session that is gone.
		if target == session.DriverManaged {
			dropManagedRuntime(m)
		} else {
			_ = tmuxx.Cmd("kill-session", "-t", session.ExactTarget(session.TmuxName(name))).Run()
		}
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	m = cur
	if wasStopped {
		fleetgraph.RecordRevive(name) // write site ③: only when the slot really was stopped
	}
	httpx.WriteJSON(w, http.StatusOK, wireSession(m, true))
}

// errCodeCodexReleasing is the retry-shortly refusal while codex's shared app-server still holds
// the conversation; the Console renders it from its catalogue (and the switch toast keys on it).
const errCodeCodexReleasing = "codex_releasing"

// errCodeCodexStopFirst refuses a codex Managed-to-Terminal switch while the session runs.
const errCodeCodexStopFirst = "codex_stop_first"

// switchSourceAlive is ManagedAlive; a variable so tests can stand in a live runtime.
var switchSourceAlive = ManagedAlive

// writeCodexReleasingErr answers a launch refused for a codex switch: the shared app-server
// still holds the conversation (codex.ErrThreadReleasing), or a switch to Terminal holds the
// session (errDriverSwitching). Both are 409, retry shortly. It reports whether err was one.
func writeCodexReleasingErr(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, codex.ErrThreadReleasing):
		httpx.WriteErr(w, http.StatusConflict, errCodeCodexReleasing, err.Error())
	case errors.Is(err, claude.ErrTranscriptLost):
		httpx.WriteErr(w, http.StatusConflict, "transcript_lost", err.Error())
	case errors.Is(err, claude.ErrTranscriptRestore):
		httpx.WriteErr(w, http.StatusConflict, errCodeTranscriptRestoreFailed, err.Error())
	case errors.Is(err, errDriverSwitching):
		httpx.WriteErr(w, http.StatusConflict, errCodeDriverSwitching, err.Error())
	default:
		return false
	}
	return true
}

// terminalSwitchNeedsStopped reports whether a Managed -> Terminal switch of the kind is
// refused unless the session is stopped.
func terminalSwitchNeedsStopped(kind string) bool {
	if a, ok := agentreg.Lookup(kind); ok {
		if g, ok := a.(agents.TerminalSwitchGuard); ok {
			return g.SwitchNeedsStopped()
		}
	}
	return false
}

// dropForTerminalSwitch lets the kind stop its Managed runtime and wait out its own lock;
// false means it has no such wait and the caller drops the runtime as usual.
func dropForTerminalSwitch(kind, name string) bool {
	if a, ok := agentreg.Lookup(kind); ok {
		if g, ok := a.(agents.TerminalSwitchGuard); ok {
			return g.DropForTerminalSwitch(name)
		}
	}
	return false
}
