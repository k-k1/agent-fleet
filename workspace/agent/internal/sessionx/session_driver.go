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

	// flip → resume: restart the same conversation on the new driver. On failure, put Driver
	// back and return the session stopped: the conversation itself is untouched, so the user
	// can resume on the old driver.
	prev := m.Driver
	if target == session.DriverManaged {
		m.Driver = session.DriverManaged
	} else {
		m.Driver = "" // tui persists as "" (the convention that keeps existing meta byte-identical)
	}
	if target == session.DriverManaged {
		d, _ := driverOf(m)
		if _, err := mcpx.StartManagedSession(d, m); err != nil {
			m.Driver = prev
			session.WriteMeta(m)
			writeRuntimeErr(w, err)
			return
		}
	} else {
		if err := startSessionTmux(m, false); err != nil {
			m.Driver = prev
			session.WriteMeta(m)
			httpx.WriteErr(w, http.StatusInternalServerError, "tmux_failed", err.Error())
			return
		}
	}
	m.StoppedAt = ""
	// The stop/relaunch above takes seconds — re-merge the on-disk lock so this
	// write-back can't roll back a lock the user flipped meanwhile (lost update).
	m = WriteSessionMetaKeepingLock(m)
	httpx.WriteJSON(w, http.StatusOK, wireSession(m, true))
}
