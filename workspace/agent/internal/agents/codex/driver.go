package codex

// Codex の managed driver（docs/27 P3）— Driver 型（agents.Driver/ThreadHandle）の
// 第 2 実装（初出は P2 の opencode）。共有 app-server の WS JSON-RPC に turn 状態
// 機械（§4）・Interaction（§5）・reconciliation（§6）をマッピングする。
//
// 実測（0.144.4、docs/27 §12.3）に基づく API 選定:
//   - turn 駆動は `turn/start`。応答は turn id を即返し（status=inProgress）、完走は
//     `turn/completed` 通知（status: completed | interrupted | failed）で届く。
//   - `turn/steer` は expectedTurnId 前提の真の mid-turn 注入（実測: 実行中 turn の
//     同一 turn 内にユーザーメッセージが合流し以後の応答に反映される）— opencode の
//     「完走後キュー投入」と違い、codex の Steer はネイティブに落ちる。実行中 turn が
//     無いときだけキューへ縮退する。
//   - interrupt = `turn/interrupt {threadId, turnId}` → turn/completed(interrupted)。
//   - 設定変更 = `thread/settings/update`（experimentalApi 必須、§12.1-4）。モデル /
//     effort は素のフィールド、モード（plan/normal）は collaborationMode
//     {mode: plan|default, settings:{model, reasoning_effort}} に畳む。
//   - 質問 = server-initiated request `item/tool/requestUserInput`（thread 単位の
//     config `features.default_mode_request_user_input=true` を thread/start・resume
//     の両方で要求 — 既定では "unavailable in this mode" になる、実測）。応答は
//     JSON-RPC response {answers: {qid: {answers: [label…]}}}。**未応答のまま接続が
//     切れても、再接続後の thread/resume で同じ要求が新接続へ再配送される**（実測）
//     — 質問待ちの reconciliation はこれで自動成立する。
//   - thread/resume 後のスレッドは approvalPolicy / sandboxPolicy が config 既定へ
//     落ちていることがある（実測: dangerFullAccess で作った thread が resume 後
//     readOnly に見えた）。resume 直後に thread/settings/update で never＋
//     dangerFullAccess を再表明する（bypass 運転の維持、§5）。
//
// ClientMessageID（§4）の冪等化は kind 共通の永続台帳（agents.MsgLedger — §9.5 の
// プロセス跨ぎ永続化、P2 持ち越し課題の解消）で行い、codex へは clientUserMessageId
// としても併送する（item の clientId に往復記録される — 実測。診断と将来の照合用）。

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// ledger は ClientMessageID の永続台帳（プロセス跨ぎの再送冪等化、§9.5）。
var ledger = agents.NewMsgLedger("codex-msgledger")

// NewDriver returns the managed codex Driver（driverOf が /turn・/respond から引く）。
// read 層は agentImpl をそのまま埋め込んで温存する。
func NewDriver() agents.Driver { return managedDriver{} }

type managedDriver struct{ agentImpl }

// Capabilities（§3.1）。Steer は turn/steer へのネイティブ接続。TUIAttach は false —
// codex の CLI ルートは排他切替（stop→drain→resume、§2）であって併用ではない。
func (managedDriver) Capabilities() agents.Capabilities {
	return agents.Capabilities{
		ProcessModel:  "shared-daemon",
		Steer:         true,
		Fork:          true,
		DynamicModel:  true,
		DynamicEffort: true,
		DynamicMode:   true,
		Questions:     true,
	}
}

// threadFeatures は thread/start・thread/resume に毎回渡す thread 単位 config。
// request_user_input ツールは app-server スレッドでは既定無効（実測: "unavailable in
// this mode"）。**TUI が自前で有効化するという当初の前提は誤りだった**（実測 0.144.3 /
// 0.144.5: TUI も Default mode では同じく拒否する — Plan mode だけが自動で有効）。CLI
// ルートは buildProgram が同じ feature を -c で渡して対称にしている。
var threadFeatures = map[string]any{"default_mode_request_user_input": true}

// threadConfig is the per-thread config: the features above, plus the MCP servers
// scoped to THIS session（docs/27 §9.3.1）。
//
// A managed session's MCP child is spawned by the one shared app-server, so the
// process environment cannot carry a per-session AF_SESSION_NAME the way tmux does
// for the TUI route. The thread config is the only channel that varies per session,
// and it was measured to reach the spawned child — so the session name is stamped
// there and `propose_session_handoff` stops guessing from cwd.
//
// Since a thread-local map REPLACES the global config rather than merging with it,
// mcpreg emits the WHOLE effective set. When it has nothing to say — registry
// unreadable, or no servers for codex — the key is omitted entirely so the thread
// inherits config.toml. Omitting is the safe failure: sending `{}` would deny every
// server, and losing the user's MCP servers is a far worse outcome than losing the
// session name (which degrades to the cwd fallback that shipped before this).
func threadConfig(slot string) map[string]any {
	cfg := map[string]any{"features": threadFeatures}
	if servers, ok := threadMCPServers(slot); ok {
		cfg["mcp_servers"] = servers
	}
	return cfg
}

// sessionMCPDefs is a seam: the real registry reads the user's encrypted store, which
// a unit test must not touch.
var sessionMCPDefs = func() ([]mcpreg.ServerDef, error) { return mcpreg.ForSession(session.KindCodex) }

func threadMCPServers(slot string) (map[string]any, bool) {
	if slot == "" {
		return nil, false
	}
	defs, err := sessionMCPDefs()
	if err != nil {
		// Inherit config.toml rather than fail the thread: an unreadable registry
		// must not stop a session from starting.
		log.Printf("codex managed: MCP レジストリを読めないため thread 単位設定を省略します (%s): %v", slot, err)
		return nil, false
	}
	return mcpreg.CodexThreadServers(defs, mcpreg.CodexThreadOpts{SessionName: slot})
}

// bypassPolicies は AF の無人運転ポリシー（TUI ルートの --dangerously-bypass-… と
// 同じ意味、コンテナがサンドボックス）。
func bypassPolicies() map[string]any {
	return map[string]any{
		"approvalPolicy": "never",
		"sandboxPolicy":  map[string]any{"type": "dangerFullAccess"},
	}
}

// Resume returns the session's ThreadHandle, creating the runtime thread when
// none exists yet（Driver IF: 無ければ新規 start）。§6 の reconciliation 共通手順を
// 兼ねる: runtime＋writer 接続の確保 → thread 解決（resume/fork/start）→ ポリシー
// 再表明 → snapshot 反映。live 購読は writer 接続が generation 単位で常設。
func (managedDriver) Resume(m session.Meta) (agents.ThreadHandle, error) {
	if m.Kind != session.KindCodex {
		return nil, errors.New("codex driver は codex セッション専用です")
	}
	cl, gen, err := Serve().Ensure()
	if err != nil {
		return nil, err
	}
	slotSid := session.UUID(m.Dir, m.Name) // identity: the working copy, never the subdir
	cwd := m.CWD()                         // where the thread runs (Dir, or a chosen subdir)
	handlesMu.Lock()
	h := handles[m.Name]
	if h == nil {
		h = &threadHandle{
			name:    m.Name,
			dir:     cwd,
			slotSid: slotSid,
			events:  make(chan agents.Event, 64),
		}
		handles[m.Name] = h
	}
	handlesMu.Unlock()

	h.resumeMu.Lock()
	defer h.resumeMu.Unlock()

	h.mu.Lock()
	if h.alive && h.gen == gen && h.tid != "" {
		h.mu.Unlock()
		return h, nil
	}
	if h.settings.Model == "" {
		h.settings.Model = m.Model // 起動時既定（動的変更は UpdateSettings が上書き）
	}
	if h.settings.Effort == "" {
		h.settings.Effort = m.Effort
	}
	if h.settings.Mode == "" {
		h.settings.Mode = m.Mode
	}
	tid := h.tid
	h.mu.Unlock()

	if tid == "" {
		tid = sids.Read(slotSid)
	}
	var st threadSnapshotWire
	if tid != "" {
		st, err = threadResume(cl, tid, cwd, m.Name)
		if err != nil {
			// 初回 turn 前に daemon が再起動したスレッドは rollout が無く resume
			// できない（§12.1-5）— 会話はまだ空なので新規 start で置き直す。それ以外
			// のエラーで新 thread を鋳直すと sids が付け替わり会話が見えなくなるので
			// 正直に失敗させる。
			if !strings.Contains(err.Error(), "no rollout found") {
				return nil, err
			}
			tid = ""
		}
	}
	if tid == "" {
		if m.ForkFrom != "" {
			st, err = threadFork(cl, m.ForkFrom, cwd, m.ForkAt, m.Name)
		} else {
			st, err = threadStart(cl, cwd, firstNonEmpty(h.settings.Model, m.Model), m.Name)
		}
		if err != nil {
			return nil, err
		}
		tid = st.threadID
		// TUI ルートの hook 捕捉（RememberSid）と同じスロット対応付け — read 層の
		// rolloutPath(sids.Read) と排他切替（cli⇄managed の同一会話維持）が乗る。
		sids.Write(slotSid, tid)
	}
	// resume 後はポリシーが config 既定へ落ちていることがある（実測）— 再表明する。
	// 失敗しても turn は動く（readOnly 側に倒れるだけ）ので致命にはしない。
	if _, err := cl.call("thread/settings/update", mergeMaps(map[string]any{"threadId": tid}, bypassPolicies()), 10*time.Second); err != nil {
		log.Printf("codex managed: policy re-assert %s: %v", m.Name, err)
	}

	h.mu.Lock()
	h.client, h.gen, h.tid, h.alive = cl, gen, tid, true
	if st.model != "" {
		h.curModel = st.model
		if h.settings.Model == "" {
			h.settings.Model = st.model // GET /settings に実効値を返す
		}
	}
	// reconcile（§6-4）: resume が返した server 側状態を正とする。実行中 turn が
	// 残っていれば（前 Agent プロセスが投げたもの）その turnId を引き継ぎ、
	// turn/completed 通知で確定する。質問待ちなら要求の再配送が直後に届く（実測）。
	switch {
	case st.active && st.waitingInput:
		h.state = agents.TurnWaitingInteraction
	case st.active:
		h.state = agents.TurnRunning
	default:
		h.state = agents.TurnCompleted
	}
	h.running = st.active
	h.turnID = st.inProgressTurn
	if !st.waitingInput {
		h.inter = nil
	}
	h.mu.Unlock()

	// thread/start は model しか受け取らず、effort / collaboration mode は
	// thread/settings/update の管轄。作成 UI で選んだ値と Meta に永続化した動的設定を、
	// 初回 turn より前（および daemon 再接続・fork 後）に native thread へ再適用する。
	// ここを best-effort にすると UI 上の選択と実際の推論設定が黙って食い違うため、
	// ユーザー指定がある場合の失敗は Resume の失敗として返す。
	if m.Model != "" || m.Effort != "" || m.Mode != "" {
		if err := h.UpdateSettings(agents.ThreadSettings{Model: m.Model, Effort: m.Effort, Mode: m.Mode}); err != nil {
			h.mu.Lock()
			h.alive = false // 次回 Resume で設定適用を再試行する
			h.mu.Unlock()
			return nil, fmt.Errorf("codex thread の実行設定を反映できませんでした: %w", err)
		}
	}

	// daemon 死で pump が終了した後も queue には送信済み入力が残り得る（§31）。
	// Resume がここで再起動しないと、投入済みプロンプトは滞留したままになる。
	h.mu.Lock()
	if len(h.queue) > 0 && !h.pumping && !h.running {
		h.pumping = true
		go h.pump()
	}
	h.mu.Unlock()

	// exit recording の baseline（opencode driver / tui startSessionTmux と同じ役割）。
	base, _ := status.OOMKillCount()
	status.PersistExit(m.Name, status.ExitInfo{OOMBase: base})
	return h, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func mergeMaps(dst map[string]any, src map[string]any) map[string]any {
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// --- thread RPC helpers --------------------------------------------------------

// threadSnapshotWire is what Resume needs back from thread/start・resume・fork.
type threadSnapshotWire struct {
	threadID       string
	model          string
	active         bool
	waitingInput   bool
	inProgressTurn string
}

func parseThreadResult(res json.RawMessage) (threadSnapshotWire, error) {
	var p struct {
		Model  string `json:"model"`
		Thread struct {
			ID     string `json:"id"`
			Status struct {
				Type        string   `json:"type"`
				ActiveFlags []string `json:"activeFlags"`
			} `json:"status"`
			Turns []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"turns"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(res, &p); err != nil || p.Thread.ID == "" {
		return threadSnapshotWire{}, errors.New("thread 応答を解釈できません")
	}
	st := threadSnapshotWire{threadID: p.Thread.ID, model: p.Model}
	st.active = p.Thread.Status.Type == "active"
	for _, f := range p.Thread.Status.ActiveFlags {
		if f == "waitingOnUserInput" {
			st.waitingInput = true
		}
	}
	for _, t := range p.Thread.Turns {
		if t.Status == "inProgress" {
			st.inProgressTurn = t.ID
		}
	}
	return st, nil
}

func threadStart(cl *appClient, cwd, model, slot string) (threadSnapshotWire, error) {
	params := mergeMaps(map[string]any{"cwd": cwd, "config": threadConfig(slot)}, bypassPolicies())
	if model != "" {
		params["model"] = model
	}
	res, err := cl.call("thread/start", params, 30*time.Second)
	if err != nil {
		return threadSnapshotWire{}, fmt.Errorf("codex thread の作成に失敗しました: %w", err)
	}
	return parseThreadResult(res)
}

func threadResume(cl *appClient, tid, cwd, slot string) (threadSnapshotWire, error) {
	params := map[string]any{"threadId": tid, "cwd": cwd, "config": threadConfig(slot)}
	res, err := cl.call("thread/resume", params, 30*time.Second)
	if err != nil {
		return threadSnapshotWire{}, err
	}
	return parseThreadResult(res)
}

// threadFork copies src into a NEW thread. lastTurnId, when non-empty, is the last turn
// the fork keeps — **inclusive**: codex omits every turn after it (docs/55 §55.2). The
// translation from the Console's exclusive anchor happened in ResolveForkAt; by the time
// it reaches here it is already "the turn to fork through". Empty = the whole thread.
func threadFork(cl *appClient, src, cwd, lastTurnID, slot string) (threadSnapshotWire, error) {
	params := mergeMaps(map[string]any{"threadId": src, "cwd": cwd, "config": threadConfig(slot)}, bypassPolicies())
	if lastTurnID != "" {
		params["lastTurnId"] = lastTurnID
	}
	res, err := cl.call("thread/fork", params, 30*time.Second)
	if err != nil {
		if lastTurnID != "" {
			// codex refuses a turn that is still in progress; that is about the anchor we
			// sent, not about the daemon, so don't report it as a generic fork failure.
			return threadSnapshotWire{}, fmt.Errorf("codex が分岐点を受け付けませんでした: %w", err)
		}
		return threadSnapshotWire{}, fmt.Errorf("codex thread の分岐に失敗しました: %w", err)
	}
	return parseThreadResult(res)
}

// --- handle registry ---------------------------------------------------------

var handlesMu sync.Mutex
var handles = map[string]*threadHandle{}

func handleFor(name string) *threadHandle {
	handlesMu.Lock()
	defer handlesMu.Unlock()
	return handles[name]
}

// handleByTid finds the live handle owning a codex thread id.
func handleByTid(tid string) *threadHandle {
	if tid == "" {
		return nil
	}
	handlesMu.Lock()
	defer handlesMu.Unlock()
	for _, h := range handles {
		h.mu.Lock()
		match := h.tid == tid
		h.mu.Unlock()
		if match {
			return h
		}
	}
	return nil
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

// DropHandle detaches a managed session from its runtime handle (stop / halt /
// archive / 排他切替): interrupt any running turn, unsubscribe the writer
// connection from the thread, forget the handle. 会話の正本（rollout）は残り、
// 後の Resume（または TUI の `codex resume`）が再接続する。
func DropHandle(name string) {
	handlesMu.Lock()
	h := handles[name]
	delete(handles, name)
	handlesMu.Unlock()
	if h == nil {
		return
	}
	h.mu.Lock()
	cl, tid, turnID, running := h.client, h.tid, h.turnID, h.running
	h.alive = false
	h.queue = nil
	h.mu.Unlock()
	if cl == nil {
		return
	}
	if running && turnID != "" {
		_, _ = cl.call("turn/interrupt", map[string]any{"threadId": tid, "turnId": turnID}, 10*time.Second)
	}
	if tid != "" {
		// 観測しない接続に thread を載せたままにしない（daemon メモリ・通知の節約）。
		_, _ = cl.call("thread/unsubscribe", map[string]any{"threadId": tid}, 10*time.Second)
	}
}

// RemoveLedger drops a session's ClientMessageID ledger（/stop — スロットの
// アイデンティティごと破棄する時だけ。halt/archive は再開があるので残す）。
func RemoveLedger(name string) { ledger.Remove(name) }

// ManagedAlive reports whether the session has a live runtime handle — the
// managed counterpart of tmuxx.HasSession for the sessions list.
func ManagedAlive(name string) bool {
	h := handleFor(name)
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.alive
}

// ManagedBusy reports a turn is running or queued (graceful shutdown・排他切替の
// 待ち/拒否条件).
func ManagedBusy(name string) bool {
	h := handleFor(name)
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.running || h.turnID != "" || len(h.queue) > 0
}

// AbortManaged interrupts every running managed turn（graceful shutdown の
// per-pane Ctrl-C 相当、§10.2-8）。
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

// ReconcileManaged re-attaches managed codex sessions after an Agent boot,
// daemon restart or writer loss（§6）。対象は「停止扱いになっていない」managed メタ
// 全部。失敗してもセッションは 停止中 として残り、再開クリック（/start）で再試行。
func ReconcileManaged(reason string) {
	d := managedDriver{}
	for _, m := range session.ListMetas() {
		if m.Kind != session.KindCodex || m.DriverKind() != session.DriverManaged || m.Archived {
			continue
		}
		if m.StoppedAt != "" && handleFor(m.Name) == nil {
			continue // deliberately stopped — resume only on user action
		}
		if _, err := d.Resume(m); err != nil {
			log.Printf("codex managed: reconcile %s (%s): %v", m.Name, reason, err)
		}
	}
}

// reconcileAll is the supervisor-facing wrapper (serve.go の daemon 死・restart 後).
func reconcileAll(reason string) { ReconcileManaged(reason) }

// --- thread handle -----------------------------------------------------------

type threadHandle struct {
	name    string
	dir     string
	slotSid string

	// resumeMu serializes Resume end-to-end: its check-then-act (no tid → start a
	// new native thread + sids.Write) spans network calls, and two concurrent
	// Resumes (waitDaemon + writerLost reconcile, or a user /start) would mint two
	// threads and orphan one (§32).
	resumeMu sync.Mutex

	mu       sync.Mutex
	client   *appClient
	gen      int
	tid      string
	alive    bool
	state    agents.TurnState
	running  bool // runtime に active turn がある（pump 駆動または resume で引継ぎ）
	pumping  bool
	turnID   string // the active turn (turn/steer・turn/interrupt の宛先)
	turnEnd  chan agents.TurnState
	queue    []agents.TurnInput
	settings agents.ThreadSettings
	curModel string              // 直近の実効モデル（settings/updated・thread 応答から。モード切替の collaborationMode.settings.model に必須）
	inter    *agents.Interaction // pending question（waiting_interaction の中身）
	interQ   []string            // 質問 id 列（応答 map のキー、Interaction.Questions と同順）
	interReq json.RawMessage     // server request の JSON-RPC id（interClient 接続スコープ）
	interCl  *appClient
	events   chan agents.Event
	lastErr  *codexError // 直近ターンの失敗詳細（errors.go）。次ターン開始で clearLastError
	// されるまで managedEnrich が末尾へ合成 error ターンとして出す。
}

// setLastError / clearLastError / turnError manage the failure detail managedEnrich
// injects as a synthetic transcript turn (§10.2-5). In-memory only — matches h.inter's
// lifetime, since neither survives a process restart and both are re-derived by
// reconciliation instead of persisted.
func (h *threadHandle) setLastError(e codexError) {
	h.mu.Lock()
	ce := e
	h.lastErr = &ce
	h.mu.Unlock()
}

func (h *threadHandle) clearLastError() {
	h.mu.Lock()
	h.lastErr = nil
	h.mu.Unlock()
}

func (h *threadHandle) turnError() *codexError {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lastErr
}

// emit pushes an event without ever blocking a state transition (drop on overflow —
// events are advisory; the source of truth is Snapshot + the native store, §6).
func (h *threadHandle) emit(e agents.Event) {
	select {
	case h.events <- e:
	default:
	}
}

func (h *threadHandle) setState(st agents.TurnState) {
	h.mu.Lock()
	h.state = st
	h.mu.Unlock()
	h.emit(agents.Event{Kind: "turn_state", TurnState: st})
}

// runtimeLost drops the handle to unknown（§6-1: 切断時の正直な状態）。The pump's
// wait (if any) unblocks via turnEnd and keeps unknown until reconcile resolves it.
func (h *threadHandle) runtimeLost() {
	h.mu.Lock()
	h.alive = false
	h.state = agents.TurnUnknown
	end := h.turnEnd
	h.mu.Unlock()
	if end != nil {
		select {
		case end <- agents.TurnUnknown:
		default:
		}
	}
	h.emit(agents.Event{Kind: "turn_state", TurnState: agents.TurnUnknown})
}

// --- ThreadHandle interface ---------------------------------------------------

// Send starts a turn (turn/start), queueing behind a running one.
func (h *threadHandle) Send(in agents.TurnInput) error { return h.accept(in) }

// Steer injects a follow-up into the RUNNING turn via native turn/steer（実測:
// expectedTurnId 前提・同一 turn へ合流）。実行中 turn が無い（完走直後の競合等）
// ときはキュー（次 turn として投入）へ縮退する。
func (h *threadHandle) Steer(in agents.TurnInput) error {
	if strings.TrimSpace(in.Prompt) == "" && len(in.Attachments) == 0 {
		return errors.New("empty prompt")
	}
	in.ClientMessageID = agents.NormalizeMsgID(in.ClientMessageID)
	h.mu.Lock()
	if !h.alive {
		h.mu.Unlock()
		return errors.New("runtime が停止しています（再開してください）")
	}
	if h.inter != nil {
		h.mu.Unlock()
		return agents.ErrQuestionPending
	}
	cl, tid, turnID, running := h.client, h.tid, h.turnID, h.running
	h.mu.Unlock()
	if !running || turnID == "" {
		return h.accept(in)
	}
	if ledger.SeenOrRecord(h.name, in.ClientMessageID) {
		return nil // 再送 — 台帳が冪等化（§4）
	}
	_, err := cl.call("turn/steer", map[string]any{
		"threadId":            tid,
		"expectedTurnId":      turnID,
		"input":               buildInput(in),
		"clientUserMessageId": in.ClientMessageID,
	}, 15*time.Second)
	if err != nil {
		// turn がちょうど終わった等 — 意図（追撃入力）は次 turn として残す。
		h.mu.Lock()
		h.queue = append(h.queue, in)
		start := !h.pumping
		if start {
			h.pumping = true
		}
		h.mu.Unlock()
		if start {
			go h.pump()
		}
	}
	return nil
}

func (h *threadHandle) accept(in agents.TurnInput) error {
	if strings.TrimSpace(in.Prompt) == "" && len(in.Attachments) == 0 {
		return errors.New("empty prompt")
	}
	in.ClientMessageID = agents.NormalizeMsgID(in.ClientMessageID)
	h.mu.Lock()
	if !h.alive {
		h.mu.Unlock()
		return errors.New("runtime が停止しています（再開してください）")
	}
	if h.inter != nil {
		// 質問待ち中の自由文送信は誤答のもと（/input の question_pending ガードと
		// 同じ判断）— 構造化回答（Respond）へ誘導する。
		h.mu.Unlock()
		return agents.ErrQuestionPending
	}
	if ledger.SeenOrRecord(h.name, in.ClientMessageID) {
		h.mu.Unlock()
		return nil // 再送 — 台帳が冪等化（§4）
	}
	h.queue = append(h.queue, in)
	// Resume が引き継いだ外部実行中 turn には pump goroutine がない。その場合は
	// turn/completed dispatcher がキューを起こすまで待つ（二重 turn/start 防止）。
	start := !h.pumping && !h.running
	if start {
		h.pumping = true
	}
	if h.running || len(h.queue) > 1 {
		h.state = agents.TurnQueued
	}
	h.mu.Unlock()
	if start {
		go h.pump()
	}
	return nil
}

// pump processes the queue serially: run one turn/start, wait for its
// turn/completed (dispatcher が turnEnd へ届ける), repeat.
func (h *threadHandle) pump() {
	for {
		h.mu.Lock()
		if len(h.queue) == 0 || !h.alive || h.running {
			// accept と同じ lock 内で停止を確定する。空判定後〜defer の隙間を
			// 作ると、その間の入力が pumping=true を見て起動されず stranded になる。
			h.pumping = false
			h.mu.Unlock()
			return
		}
		in := h.queue[0]
		h.queue = h.queue[1:]
		h.running = true
		gen := h.gen
		h.mu.Unlock()

		h.runTurn(in, gen)

		h.mu.Lock()
		if h.gen == gen {
			h.running = false
		}
		h.mu.Unlock()
	}
}

// runTurn executes ONE turn: turn/start → wait for the dispatcher-delivered
// terminal state. status ストア（hooks の代わり — WireLive のフォールバックと
// anySessionWorking が読む）は dispatcher（turn/started・turn/completed）が正で、
// ここでは楽観の working だけ先に刻む。
func (h *threadHandle) runTurn(in agents.TurnInput, gen int) {
	agents.MarkTurnStart(h.slotSid)
	h.clearLastError() // 新しいターンが始まる＝前ターンの合成 error は役目を終える
	h.setState(agents.TurnStarting)
	h.mu.Lock()
	cl, tid := h.client, h.tid
	end := make(chan agents.TurnState, 1)
	h.turnEnd = end
	h.mu.Unlock()

	res, err := cl.call("turn/start", map[string]any{
		"threadId":            tid,
		"input":               buildInput(in),
		"clientUserMessageId": in.ClientMessageID,
	}, 30*time.Second)
	if err != nil {
		h.mu.Lock()
		sameGen := h.gen == gen
		if h.turnEnd == end {
			h.turnEnd = nil
		}
		alive := h.alive
		h.mu.Unlock()
		if !sameGen {
			return // reconciliation が新世代の snapshot を既に置いた
		}
		st := agents.TurnUnknown // 切断 — §6 に委ねる（報告はしない）
		failure := ""
		if alive {
			log.Printf("codex managed: turn/start %s: %v", h.name, err)
			st = agents.TurnFailed
			// turn/start が JSON-RPC エラーで即拒否されるケース(実測: 利用上限を使い切った
			// 状態での送信)は turn すら作られず rollout に何も書かれない — ユーザーの発言
			// すら記録されない。理由を拾えないと通知/報告は空文字のまま「失敗しました」
			// としか言えず、ミラーの反映待ちechoも一致対象が永遠に現れず消えない
			// （pendingEcho.echoLanded）。ここで合成した error ターンが managedEnrich 経由で
			// その代わりを果たす。
			if ce, ok := codexErrorFromRPC(err); ok {
				failure = ce.summary()
				h.setLastError(ce)
			} else {
				failure = "[error] " + err.Error()
			}
		}
		agents.MarkTurnEndErr(h.slotSid, st, failure)
		h.setState(st)
		return
	}
	var tr struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	_ = json.Unmarshal(res, &tr)
	h.mu.Lock()
	if h.gen != gen {
		if h.turnEnd == end {
			h.turnEnd = nil
		}
		h.mu.Unlock()
		return // 旧世代の response で新世代 handle を上書きしない
	}
	h.turnID = tr.Turn.ID
	h.mu.Unlock()
	h.setState(agents.TurnRunning)

	final := <-end // turn/completed（dispatcher）か runtimeLost が必ず届ける
	h.mu.Lock()
	sameGen := h.gen == gen
	if h.turnEnd == end {
		h.turnEnd = nil
	}
	if sameGen {
		h.turnID = ""
		h.inter = nil // turn が終わった＝質問はもう待っていない
	}
	h.mu.Unlock()
	if sameGen {
		h.setState(final)
	}
}

// Interrupt aborts the running turn and clears the queued追撃（停止の意思表示は
// キューにも及ぶ）。
func (h *threadHandle) Interrupt() error {
	h.mu.Lock()
	cl, tid, turnID := h.client, h.tid, h.turnID
	running := h.running || turnID != "" // turnID only: agent 再起動後に引き継いだ実行中 turn
	h.queue = nil
	if running {
		h.state = agents.TurnInterrupting
	}
	h.mu.Unlock()
	if !running || turnID == "" {
		return nil
	}
	h.emit(agents.Event{Kind: "turn_state", TurnState: agents.TurnInterrupting})
	_, err := cl.call("turn/interrupt", map[string]any{"threadId": tid, "turnId": turnID}, 15*time.Second)
	return err
}

// UpdateSettings applies dynamic thread settings（§9.4-3 — 稼働中セッションの
// モデル/effort/モード変更）via thread/settings/update.
func (h *threadHandle) UpdateSettings(s agents.ThreadSettings) error {
	h.mu.Lock()
	cl, tid := h.client, h.tid
	next := h.settings
	if s.ClearModel {
		next.Model = ""
	} else if s.Model != "" {
		next.Model = s.Model
	}
	if s.ClearEffort {
		next.Effort = ""
	} else if s.Effort != "" {
		next.Effort = s.Effort
	}
	if s.Mode != "" {
		next.Mode = s.Mode
	}
	model := firstNonEmpty(next.Model, h.curModel)
	alive := h.alive
	h.mu.Unlock()
	if !alive {
		return errors.New("runtime が停止しています（再開してください）")
	}
	params := map[string]any{"threadId": tid}
	if s.ClearModel {
		params["model"] = nil
	} else if s.Model != "" {
		params["model"] = s.Model
	}
	if s.ClearEffort {
		params["effort"] = nil
	} else if s.Effort != "" {
		params["effort"] = s.Effort
	}
	if s.Mode != "" {
		// モードは collaborationMode プリセット（ModeKind: plan | default）。settings.
		// model は必須なので実効モデルを併送する（reasoning_effort は null 可）。
		kind := "default"
		if s.Mode == "plan" {
			kind = "plan"
		}
		if model == "" {
			return errors.New("モデルが未確定のためモードを切り替えられません")
		}
		cm := map[string]any{"mode": kind, "settings": map[string]any{"model": model}}
		if e := next.Effort; e != "" {
			cm["settings"].(map[string]any)["reasoning_effort"] = e
		}
		params["collaborationMode"] = cm
	}
	if _, err := cl.call("thread/settings/update", params, 15*time.Second); err != nil {
		return err
	}
	// RPC 成功後にだけローカル snapshot を進める。失敗時に mode chip が実 runtime
	// より先行したまま残らないようにする。
	h.mu.Lock()
	if s.ClearModel {
		h.settings.Model = ""
	} else if s.Model != "" {
		h.settings.Model = s.Model
	}
	if s.ClearEffort {
		h.settings.Effort = ""
	} else if s.Effort != "" {
		h.settings.Effort = s.Effort
	}
	if s.Mode != "" {
		h.settings.Mode = s.Mode
	}
	cur := h.settings
	h.mu.Unlock()
	h.emit(agents.Event{Kind: "settings", Settings: &cur})
	return nil
}

// Respond answers the pending Interaction（§5）: question 系のみ。answer/allow は
// server request への JSON-RPC response（answers[qid] = ラベル列）、cancel/deny は
// 「答えずに turn を止める」＝ turn/interrupt に落とす（codex に reject の口は無い）。
func (h *threadHandle) Respond(reply agents.InteractionReply) error {
	h.mu.Lock()
	inter, qids, reqID, cl := h.inter, h.interQ, h.interReq, h.interCl
	h.mu.Unlock()
	if inter == nil || inter.ID != reply.ID {
		return fmt.Errorf("interaction %s は待機中ではありません", reply.ID)
	}
	switch reply.Decision {
	case agents.DecisionCancel, agents.DecisionDeny:
		return h.Interrupt()
	case agents.DecisionAnswer, agents.DecisionAllow:
		if len(reply.Answers) != len(inter.Questions) {
			return fmt.Errorf("回答数が質問数と一致しません (%d != %d)", len(reply.Answers), len(inter.Questions))
		}
		answers := map[string]any{}
		for i, a := range reply.Answers {
			var labels []string
			for _, oi := range a.Options {
				if oi < 0 || oi >= len(inter.Questions[i].Options) {
					return fmt.Errorf("質問 %d の選択肢 %d は範囲外です", i+1, oi)
				}
				labels = append(labels, inter.Questions[i].Options[oi].Label)
			}
			if len(labels) == 0 && strings.TrimSpace(a.Text) != "" {
				labels = []string{strings.TrimSpace(a.Text)}
			}
			if len(labels) == 0 {
				return fmt.Errorf("質問 %d に回答がありません", i+1)
			}
			answers[qids[i]] = map[string]any{"answers": labels}
		}
		if cl == nil || !cl.alive() {
			return errors.New("質問の要求元接続が失われています（再開で再配送されます）")
		}
		if err := cl.respond(reqID, map[string]any{"answers": answers}); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported decision: %s", reply.Decision)
	}
	h.mu.Lock()
	h.inter = nil
	if h.running {
		h.state = agents.TurnRunning
	}
	h.mu.Unlock()
	h.emit(agents.Event{Kind: "turn_state", TurnState: agents.TurnRunning})
	return nil
}

func (h *threadHandle) Events() <-chan agents.Event { return h.events }

// Snapshot（§6-3）: reconciliation 用の現在地。
func (h *threadHandle) Snapshot() (agents.ThreadSnapshot, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return agents.ThreadSnapshot{
		TurnState:   h.state,
		Interaction: h.inter,
		Settings:    h.settings,
	}, nil
}

// queuedPrompts surfaces the driver-held queue for the mirror's キュー済み badge.
func (h *threadHandle) queuedPrompts() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, in := range h.queue {
		if t := strings.TrimSpace(in.Prompt); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// hasQuestion reports a pending Interaction (WireLive の question 射影が読む —
// managed は rollout tail probe より handle が正確で安い).
func (h *threadHandle) hasQuestion() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.inter != nil
}

// PendingInteraction returns the marshaled []transcript.Question a managed codex
// session is currently blocked on, read straight from the live handle WITHOUT
// resuming the runtime (a notification-poll caller must stay cheap). ok=false when no
// live handle / no pending question. Used to enrich the codex-question notification
// with P2b option buttons (docs/37 managed ボタン化). The bytes are the SAME shape
// bridge_answer fingerprints from Snapshot at answer time (identical []transcript.
// Question marshaled the same way), so the send-side fingerprint matches the
// answer-side one for an unchanged question.
func PendingInteraction(name string) (json.RawMessage, bool) {
	h := handleFor(name)
	if h == nil {
		return nil, false
	}
	h.mu.Lock()
	inter := h.inter
	h.mu.Unlock()
	if inter == nil || len(inter.Questions) == 0 {
		return nil, false
	}
	b, err := json.Marshal(inter.Questions)
	if err != nil {
		return nil, false
	}
	return b, true
}

// managedEnrich folds the driver-side state into the read layer's TranscriptData
// （readTranscript から呼ばれる — opencode と同型）: pending question へ Interaction
// id を載せ、driver 内キューを キュー済み へ合流し、モード chip の即時性を上げる。
func managedEnrich(m session.Meta, td *agents.TranscriptData) {
	if m.DriverKind() != session.DriverManaged {
		return
	}
	h := handleFor(m.Name)
	if h == nil {
		return
	}
	h.mu.Lock()
	inter := h.inter
	modeSet := h.settings.Mode
	h.mu.Unlock()
	if inter != nil {
		qs := make([]transcript.Question, len(inter.Questions))
		copy(qs, inter.Questions)
		for i := range qs {
			qs[i].ID = inter.ID
		}
		td.Pending = qs
	}
	td.Queued = append(td.Queued, h.queuedPrompts()...)
	if modeSet != "" {
		td.Mode = modeSet // 切替直後の rollout はまだ古い — driver 設定が次 turn の真実
	}
	// A turn rejected at turn/start (errors.go — usage limit exhausted, in the field) never
	// creates a Turn, so nothing — not even the user's own prompt — reaches the rollout.
	// Without this, the mirror's optimistic echo has no real turn to reconcile against and
	// sits at 反映待ち forever (pendingEcho.echoLanded requires a landed turn). Appending a
	// synthetic trailing turn gives both the echo (any post-cutoff error turn resolves it,
	// console/src/features/mirror/pendingEcho.ts) and the Console (ErrorBlock, same Kind
	// opencode's errors.go targets) something to show. Cleared by clearLastError the moment
	// the next turn starts (runTurn), so it never lingers past a successful retry.
	if ce := h.turnError(); ce != nil {
		idx := 0
		if n := len(td.Turns); n > 0 {
			idx = td.Turns[n-1].Idx + 1
		}
		td.Turns = append(td.Turns, transcript.Turn{
			Role: "assistant", Parts: []transcript.Part{ce.part()}, Text: ce.summary(),
			Idx: idx, TS: time.Now().Format(time.RFC3339),
		})
	}
}

// buildInput assembles the turn/start・turn/steer input items: prompt text +
// attachments（§10.2-3: managed は tmux 貼付でなく API 添付）。画像は localImage
// item、その他のファイルはパスをテキストで併記する（codex は TUI でもパス言及で
// view_image / 読解が発火する — imagePaste の既存経路と同じ扱い）。
func buildInput(in agents.TurnInput) []map[string]any {
	var items []map[string]any
	text := strings.TrimSpace(in.Prompt)
	var nonImage []string
	for _, p := range in.Attachments {
		if strings.TrimSpace(p) == "" {
			continue
		}
		switch strings.ToLower(filepath.Ext(p)) {
		case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp":
			items = append(items, map[string]any{"type": "localImage", "path": p})
		default:
			nonImage = append(nonImage, p)
		}
	}
	if len(nonImage) > 0 {
		if text != "" {
			text += "\n\n"
		}
		text += "添付ファイル:\n" + strings.Join(nonImage, "\n")
	}
	if text != "" {
		// input の先頭に text を置く（items の順序は「画像 → テキスト」でなく
		// 「テキスト → 画像」が TUI の添付と同じ並び）。
		items = append([]map[string]any{{"type": "text", "text": text}}, items...)
	}
	return items
}

// --- notification / server-request dispatch ------------------------------------

// userInputRequest is the wire params of item/tool/requestUserInput（実測 §12.3:
// questions[{id, header, question, isOther, options[{label, description}]}]）.
type userInputRequest struct {
	ThreadID  string `json:"threadId"`
	TurnID    string `json:"turnId"`
	ItemID    string `json:"itemId"`
	Questions []struct {
		ID       string `json:"id"`
		Header   string `json:"header"`
		Question string `json:"question"`
		IsOther  bool   `json:"isOther"`
		Options  []struct {
			Label       string `json:"label"`
			Description string `json:"description"`
		} `json:"options"`
	} `json:"questions"`
}

// deliverUserInputRequest lands a server question on its handle（appclient.go の
// handleServerRequest から）。ItemID を Interaction id にする — 接続を跨いだ再配送
// でも安定（実測）で、Console の /respond がこの id で答える。
func deliverUserInputRequest(c *appClient, reqID json.RawMessage, p userInputRequest) {
	h := handleByTid(p.ThreadID)
	if h == nil {
		log.Printf("codex managed: requestUserInput for unknown thread %s", p.ThreadID)
		return
	}
	inter := &agents.Interaction{ID: p.ItemID, Kind: "question"}
	var qids []string
	for _, q := range p.Questions {
		tq := transcript.Question{ID: p.ItemID, Question: q.Question, Header: q.Header}
		for _, o := range q.Options {
			tq.Options = append(tq.Options, transcript.Option{Label: o.Label, Description: o.Description})
		}
		inter.Questions = append(inter.Questions, tq)
		qids = append(qids, q.ID)
	}
	h.mu.Lock()
	h.inter = inter
	h.interQ = qids
	h.interReq = append(json.RawMessage(nil), reqID...)
	h.interCl = c
	h.state = agents.TurnWaitingInteraction
	h.mu.Unlock()
	h.emit(agents.Event{Kind: "interaction", TurnState: agents.TurnWaitingInteraction, Interaction: inter})
}

// dispatchNotification routes a server notification to the owning handle
// （appclient.go の readLoop から）。turn 状態機械（§4）の正はここ — pump は
// turnEnd 経由で終端を受け取る。
func dispatchNotification(msg rpcMsg) {
	switch msg.Method {
	case "turn/started":
		var p struct {
			ThreadID string `json:"threadId"`
			Turn     struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		if json.Unmarshal(msg.Params, &p) != nil {
			return
		}
		if h := handleByTid(p.ThreadID); h != nil {
			h.mu.Lock()
			h.turnID = p.Turn.ID
			h.mu.Unlock()
			agents.MarkTurnStart(h.slotSid)
		}
	case "turn/completed":
		var p struct {
			ThreadID string `json:"threadId"`
			Turn     struct {
				ID     string          `json:"id"`
				Status string          `json:"status"`
				Error  json.RawMessage `json:"error"` // TurnError; populated only when Status=="failed"
			} `json:"turn"`
		}
		if json.Unmarshal(msg.Params, &p) != nil {
			return
		}
		if h := handleByTid(p.ThreadID); h != nil {
			st := agents.TurnCompleted
			failure := ""
			switch p.Turn.Status {
			case "failed":
				st = agents.TurnFailed
				if ce, ok := decodeCodexError(p.Turn.Error); ok {
					failure = ce.summary()
					h.setLastError(ce)
				}
			case "interrupted":
				st = agents.TurnCancelled
			}
			// 別 turn の完了で実行中 turn を誤終了させない（遅延配送・多重接続）。
			// 両方の id が分かっていて食い違う時だけ弾く — turn/started を見ていない
			// 引き継ぎ turn（h.turnID == ""）は従来どおり終端として扱う。
			h.mu.Lock()
			mismatch := h.turnID != "" && p.Turn.ID != "" && h.turnID != p.Turn.ID
			h.mu.Unlock()
			if mismatch {
				return
			}
			agents.MarkTurnEndErr(h.slotSid, st, failure)
			h.mu.Lock()
			end := h.turnEnd
			if h.turnID == p.Turn.ID {
				h.turnID = ""
			}
			h.inter = nil
			pumpDriven := end != nil
			startQueued := false
			if !pumpDriven {
				h.running = false
				startQueued = len(h.queue) > 0 && !h.pumping && h.alive
				if startQueued {
					h.pumping = true
				}
			}
			h.mu.Unlock()
			if pumpDriven {
				select {
				case end <- st:
				default:
				}
			} else {
				// Agent 再起動で引き継いだ turn 等、pump が待っていない完走。
				h.setState(st)
			}
			if startQueued {
				go h.pump()
			}
		}
	case "thread/settings/updated":
		var p struct {
			ThreadID       string `json:"threadId"`
			ThreadSettings struct {
				Model             string `json:"model"`
				Effort            string `json:"effort"`
				CollaborationMode struct {
					Mode string `json:"mode"`
				} `json:"collaborationMode"`
			} `json:"threadSettings"`
		}
		if json.Unmarshal(msg.Params, &p) != nil {
			return
		}
		if h := handleByTid(p.ThreadID); h != nil {
			h.mu.Lock()
			if p.ThreadSettings.Model != "" {
				h.curModel = p.ThreadSettings.Model
				h.settings.Model = p.ThreadSettings.Model
			}
			if p.ThreadSettings.Effort != "" {
				h.settings.Effort = p.ThreadSettings.Effort
			}
			if p.ThreadSettings.CollaborationMode.Mode != "" {
				h.settings.Mode = p.ThreadSettings.CollaborationMode.Mode
				if h.settings.Mode == "default" {
					h.settings.Mode = "normal"
				}
			}
			cur := h.settings
			h.mu.Unlock()
			h.emit(agents.Event{Kind: "settings", Settings: &cur})
		}
	case "serverRequest/resolved":
		// 質問が（別接続・自動解決などで）解消された。id は接続スコープなので
		// thread 単位で見る — 待機中の Interaction を閉じ、turn 続行へ戻す。
		var p struct {
			ThreadID string `json:"threadId"`
		}
		if json.Unmarshal(msg.Params, &p) != nil {
			return
		}
		if h := handleByTid(p.ThreadID); h != nil {
			h.mu.Lock()
			had := h.inter != nil
			h.inter = nil
			if had && h.running {
				h.state = agents.TurnRunning
			}
			h.mu.Unlock()
			if had {
				h.emit(agents.Event{Kind: "turn_state", TurnState: agents.TurnRunning})
			}
		}
	case "item/started", "item/completed":
		// 圧縮検知（P1 の observer と同じ射影 — writer 接続でも受けて冗長化する）。
		var p struct {
			ThreadID string `json:"threadId"`
			Item     struct {
				Type string `json:"type"`
			} `json:"item"`
		}
		if json.Unmarshal(msg.Params, &p) == nil && p.ThreadID != "" && p.Item.Type == "contextCompaction" {
			SetCompacting(p.ThreadID, msg.Method == "item/started")
		}
	}
}
