// Package codex は codex CLI 種別の縦割りパッケージ（docs/23 残① Wave E）。
// Agent 実装・起動コマンド組み立て・rollout JSONL transcript 読み出し・auth/usage
// の Connections ハンドラ・rtk ブロック適用を package main から移設した。
// 挙動・ワイヤ・ディスクは main 時代とバイト同一を維持すること。
package codex

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// sids maps our deterministic slot sid to codex's own session id: written by the
// session-status hook (RememberSid — codex has no --session-id flag to pin), read
// for `codex resume <id>`.
var sids = agents.NewSidStore("codex-sid")

// compacting is keyed by Codex's native thread id and fed by app-server
// contextCompaction item lifecycle events (see package main/codex_appserver.go).
var compacting sync.Map

func SetCompacting(threadID string, active bool) {
	if threadID == "" {
		return
	}
	if active {
		compacting.Store(threadID, true)
	} else {
		compacting.Delete(threadID)
	}
}

func ClearCompacting() { compacting.Range(func(k, _ any) bool { compacting.Delete(k); return true }) }

func IsCompactingThread(threadID string) bool {
	_, ok := compacting.Load(threadID)
	return ok
}

func isCompacting(m session.Meta) bool {
	threadID := sids.Read(session.UUID(m.Dir, m.Name))
	return IsCompactingThread(threadID)
}

// RememberSid records the slot sid → codex session id mapping. Called from the
// session-status hook entrypoint in package main when codex's hook JSON carries
// its own session_id.
func RememberSid(slotSid, codexID string) { sids.Write(slotSid, codexID) }

// New returns the codex Agent implementation for the kind registry.
func New() agents.Agent { return agentImpl{} }

// agentImpl — codex 種別の Agent 実装（docs/23 P1残: CLI 縦割りファイル分割）
type agentImpl struct{}

func (agentImpl) Kind() string { return session.KindCodex }

// CanTranscript lights up the Console chat mirror for codex; its turns come from the
// rollout JSONL via Transcript() (readTranscript), windowed by the generic /messages
// handler. CanFork: the conversation forks via `codex fork <id>` (ForkSource /
// BuildLaunch). No label (codex has no --name). CanForkAt: the fork can also be pinned to
// a past turn via `thread/fork`'s lastTurnId (docs/55) — app-server only, so the handler
// refuses a point fork for a CLI-route session (`codex fork <id>` has no such argument).
func (agentImpl) Caps() agents.Caps {
	return agents.Caps{CanTranscript: true, CanFork: true, CanForkAt: true}
}

// ForkSource resolves this session's codex conversation id as the fork source —
// the hook-captured per-slot id, provided its rollout actually exists on disk.
func (agentImpl) ForkSource(m session.Meta) (string, error) {
	id := sids.Read(session.UUID(m.Dir, m.Name))
	if id == "" || rolloutPath(id) == "" {
		return "", errors.New("分岐できる会話がまだありません")
	}
	return id, nil
}

// ResolveForkAt translates a mirror anchor into codex's lastTurnId.
//
// This is the one kind where the anchor is NOT what gets sent. The Console's meaning is
// exclusive ("branch before this turn"); `thread/fork`'s lastTurnId is **inclusive**
// ("fork through this turn"), so the answer is the turn BEFORE the anchor. Passing the
// anchor itself would carry the very prompt the user wanted to retake — and the branch
// would look correct in the mirror, just one exchange too long.
//
// Branching before the FIRST turn has no representable lastTurnId: an empty value means
// "the whole conversation" to codex, which is the opposite. Refuse instead of sending it.
func (agentImpl) ResolveForkAt(m session.Meta, anchor string) (string, error) {
	if anchor == "" {
		return "", errors.New("分岐点が指定されていません")
	}
	// 分岐点を渡せる口は app-server 側にしかない（`codex fork <id>` には無い）。
	if m.DriverKind() != session.DriverManaged {
		return "", fmt.Errorf("%w: codex の発言時点からの分岐は managed のセッションでのみ利用できます",
			agents.ErrForkAtRoute)
	}
	id := sids.Read(session.UUID(m.Dir, m.Name))
	path := rolloutPath(id)
	if id == "" || path == "" {
		return "", errors.New("分岐できる会話がまだありません")
	}
	lines, err := rolloutLines(path)
	if err != nil {
		return "", errors.New("codex の会話ログを読めません")
	}
	ids := rolloutTurnIDs(lines)
	for i, tid := range ids {
		if tid != anchor {
			continue
		}
		if i == 0 {
			return "", errors.New("最初のやり取りからは分岐できません（新しいセッションを作ってください）")
		}
		return ids[i-1], nil
	}
	return "", errors.New("指定された分岐点がこの会話に見つかりません")
}

func (agentImpl) Transcript(m session.Meta) (agents.TranscriptData, bool) {
	return readTranscript(m)
}

func (agentImpl) BuildLaunch(m session.Meta, _ agents.LaunchOpts) (agents.LaunchPlan, error) {
	// codex resumes (or starts) in its real project dir; refuse if it's gone.
	if !session.DirExists(m.Dir) {
		return agents.LaunchPlan{}, agents.DirGoneErr(m.Dir)
	}
	// Pre-accept codex's per-dir trust gate so a freshly cloned repo doesn't stall at
	// the "Do you trust this directory?" prompt (the bypass flags don't cover it).
	ensureFolderTrusted(m.Dir)
	// Auth is codex's own ~/.codex/auth.json (codex login, written via the Connections
	// flow), so no token is injected. State + per-slot resume are wired purely through
	// codex hooks injected on the command line (-c), keyed by our deterministic slot
	// sid — see buildProgram.
	cxSid := session.UUID(m.Dir, m.Name)
	// First launch of a forked slot: no own captured session yet — fork the source
	// conversation (`codex fork <id>`). The injected hooks record the fork's own id
	// on the first prompt, so later launches resume it and ForkFrom is ignored.
	forkFrom := ""
	if sids.Read(cxSid) == "" {
		forkFrom = m.ForkFrom
	}
	// `codex fork <id>` takes no fork point — only the app-server's thread/fork does
	// (docs/55 §55.5). Refuse rather than launch a CLI fork that would quietly copy the
	// WHOLE conversation when the user asked for a point. The handler gates on managed
	// first; this is the second line.
	if forkFrom != "" && m.ForkAt != "" {
		return agents.LaunchPlan{}, errors.New("発言時点からの分岐は managed のセッションでのみ利用できます")
	}
	return agents.LaunchPlan{Program: buildProgram(m.Model, m.Effort, cxSid, sids.Read(cxSid), forkFrom), Cwd: m.CWD()}, nil
}

func (agentImpl) WireLive(m session.Meta, alive bool) agents.LiveInfo {
	// State comes from codex's -c-injected status hooks keyed by our sid (the status
	// store; no idle-heal, no background-busy). Resumable unless the working dir is gone.
	// managed（docs/27 P3）はhooks不在の代わりに driver が同じ status ストアへ turn
	// 境界（turn/started・turn/completed 通知）を書く — 読み側はほぼ共通で済む。
	li := agents.LiveInfo{Resumable: true}
	if alive {
		sid := session.UUID(m.Dir, m.Name)
		st, hasStatus := status.Read(sid)
		li.State = "idle"
		if hasStatus {
			li.State = st.State
		}
		// A missed Stop hook otherwise leaves Codex on 進行中 forever even after its
		// TUI has returned to the composer. Heal it from the rollout's independent
		// task_complete event, but only when it belongs to this working interval.
		// （managed はイベント駆動なので原則不要だが、無害な保険として共用する。）
		if li.State == "working" {
			workingSince, _ := time.Parse(time.RFC3339, st.TS)
			if rolloutCompletedAfter(m, workingSince) {
				li.State = "idle"
			}
		}
		if isCompacting(m) {
			li.State = "compacting"
		}
		if m.DriverKind() == session.DriverManaged {
			// managed の質問は handle の Interaction が正（server request の再配送で
			// 回復する、§12.3）— rollout tail probe より正確で安い。
			if h := handleFor(m.Name); h != nil && h.hasQuestion() {
				li.State = "question"
			}
			// 利用上限で turn が失敗した managed セッションは「入力待ち」に見えるが、
			// 再送しても同じ結果なので blocked バッジを表示する（Claude の上限メニューと
			// 同じ扱い）。turnError は次の turn 開始時にクリアされるので永遠に貼り付かない。
			if li.State == "idle" && IsRateLimited(m.Name) {
				li.State = agents.StateBlocked
			}
		} else if li.State == "working" && HasPendingQuestion(m) {
			// The hooks report only working/idle — a request_user_input dialog keeps
			// the turn "working" forever. Probe the rollout tail so the sessions list
			// shows 質問あり (and notifies) like claude; only while working, to keep
			// the probe off the common idle path.
			li.State = "question"
		}
	} else if !session.DirExists(m.Dir) {
		li.Resumable = false
	}
	return li
}

func (agentImpl) ClearResume(sid string) { sids.Remove(sid) }

// IsRateLimited reports whether a managed codex session's last turn failed with a
// usage-limit error. The shared live-state helper in package main uses this to
// surface "blocked" in the sessions list.
func IsRateLimited(name string) bool {
	h := handleFor(name)
	if h == nil {
		return false
	}
	ce := h.turnError()
	return ce != nil && ce.label == "usageLimitExceeded"
}
