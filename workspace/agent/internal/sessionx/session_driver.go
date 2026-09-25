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
// thread/resume on the same thread id (the other direction, a TUI resume of a server-created
// thread, is measured too, §12.3), and going tui rides BuildLaunch's usual resume
// (codex resume <id> --remote / opencode --session <id>).

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/kiro"
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
	if target == session.DriverManaged {
		if _, ok := driverOf(m); !ok {
			httpx.WriteErr(w, http.StatusBadRequest, "driver_unsupported",
				"managed ドライバはこの kind ではまだ利用できません")
			return
		}
	} else if AgentOf(m.Kind).Caps().ManagedOnly {
		// The symmetric refusal (ADR 0093 decision 2): a kind with no Terminal(CLI) route at
		// all can never be switched TO tui, unlike the managed-unsupported case above where
		// the kind might grow a driver later.
		httpx.WriteErr(w, http.StatusBadRequest, "driver_unsupported",
			"この kind には Terminal(CLI) 実行方式がありません（managed 専用です）")
		return
	}

	// Drain condition: never take a running (or queued) turn with us. For tui that is the
	// status store's working state (from hooks), for managed the handle's running/queue.
	sid := session.UUID(m.Dir, name)
	if m.DriverKind() == session.DriverTUI {
		if st, ok := status.Read(sid); ok && st.State == "working" {
			httpx.WriteErr(w, http.StatusConflict, "busy_switch",
				"実行中のターンがあります。完了を待つか停止してから切り替えてください")
			return
		}
	} else if managedBusy(m) {
		httpx.WriteErr(w, http.StatusConflict, "busy_switch",
			"実行中のターンがあります。完了を待つか停止してから切り替えてください")
		return
	}

	// stop: bring the old driver down (single-writer exclusion, §2 — never leave two
	// writers on one thread).
	if tn := session.TmuxName(name); tmuxx.HasSession(tn) {
		disconnectRemoteControl(name, m)
		if out, err := tmuxx.Cmd("kill-session", "-t", session.ExactTarget(tn)).CombinedOutput(); err != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, "tmux_failed", string(out))
			return
		}
	}
	// Stop the old managed runtime. For a managed→TUI switch of kiro, whose per-sid `.lock`
	// guards the session cross-process, wait bounded for the child to exit + release the lock
	// so the TUI's `--resume-id` relaunch below doesn't race it into an error or a split-brain
	// new sid (A2-2). Other kinds / directions don't gate on a lock, so drop asynchronously.
	if m.DriverKind() == session.DriverManaged && target == session.DriverTUI && m.Kind == session.KindKiro {
		kiro.DropHandleWait(name, 5*time.Second)
	} else {
		dropManagedRuntime(m)
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
		d, _ := driverOf(m)
		if _, err := mcpx.StartManagedSession(d, m); err != nil {
			writeRuntimeErr(w, err)
			return
		}
	} else {
		if err := startSessionTmux(m, false); err != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, "tmux_failed", err.Error())
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
