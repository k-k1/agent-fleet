package sessionx

// docs/log/27 P3: 既存セッションのドライバ排他切替（POST /sessions/{name}/driver）。
// codex の「CLI ルート常設・双方向切替（tui ⇄ managed）」（§2）の実装で、opencode
// にも同じ形で効く（opencode は排他不要だが、切替の意味論 — 旧ドライバを止めて
// 新ドライバで同じ会話を再開 — は共通）。
//
// 切替は必ず stop → drain → resume を経由する（§2）。drain は最小形: 実行中 /
// キュー済みの turn がある間は 409 busy_switch で拒否し、「idle まで待つ（または
// 停止ボタンで interrupt する）」のはユーザーの明示操作に倒す — 切替クリックの
// 裏で走行中 turn を勝手に abort するのが最も驚く挙動だから。
//
// 会話の同一性は per-slot sid ストアが担保する: managed 化は同じ thread id を
// thread/resume し（server 作成 thread の TUI resume と逆方向も実測済み、§12.3）、
// tui 化は BuildLaunch の従来 resume（codex resume <id> --remote / opencode
// --session <id>）に乗る。

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
	Driver string `json:"driver"` // "tui"（"" も可）| "managed"
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

	// drain 条件: 実行中 turn（またはキュー）を巻き込まない。tui は status ストアの
	// working（hooks 由来）、managed は handle の running/queue で判定する。
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

	// stop: 旧ドライバ側を落とす（単一 writer 排他、§2 — 同一 thread への二重
	// writer 状態を作らない）。
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

	// flip → resume: 新ドライバで同じ会話を再開する。失敗したら Driver を戻して
	// 停止中のまま返す（会話は正本に無傷 — ユーザーは 再開 で旧ドライバに戻れる）。
	prev := m.Driver
	if target == session.DriverManaged {
		m.Driver = session.DriverManaged
	} else {
		m.Driver = "" // tui は "" で永続化（既存メタとバイト同一の規約）
	}
	if target == session.DriverManaged {
		d, _ := driverOf(m)
		if _, err := mcpx.StartManagedSession(d, m); err != nil {
			m.Driver = prev
			session.WriteMeta(m)
			httpx.WriteErr(w, http.StatusBadGateway, "runtime_failed", err.Error())
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
