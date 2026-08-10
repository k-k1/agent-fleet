package copilot

// copilot の managed driver（docs/36 Track A2）— per-session child 方式。
// セッション毎に `copilot --acp`（Agent Client Protocol、stdio JSON-RPC）を子
// プロセスとして抱え、session/new・session/load（クロスプロセス resume、実測）・
// session/prompt（blocking）・session/cancel・session/set_mode に turn 状態機械
// （§4）・Interaction（§5）・reconciliation（§6）をマッピングする。
//
// per-session child を選ぶ理由（docs/36 契約）: ACP に per-session のモデル指定が
// 無く（configOptions は mode/allow_all のみ — 実測）、子プロセス毎の --model /
// --effort フラグで固定するのが唯一の確実な経路。メモリは TUI pane と同等で、
// exit/OOM 記録は子の cmd.Wait() で per-session に正確化される。
//
// 権限（session/request_permission）は --allow-all 運転では発生しないが、plan
// モード起動では allow-all を外すため到達しうる。「UI に出ないから発生しない」を
// 信用せず（agy 3aaebf6 の教訓）、常に Interaction(question) へ写像して Console の
// 質問カード（/respond）で答えさせる。

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// ledger は ClientMessageID の永続台帳（§9.5 — 再送・reconnect の二重投入を
// プロセス跨ぎで冪等化）。
var ledger = agents.NewMsgLedger("copilot-msgledger")

// ACP session-mode ids（v1.0.73 実測）。AF 語彙 "plan"/"normal" と相互変換する。
const (
	acpModeAgent     = "https://agentclientprotocol.com/protocol/session-modes#agent"
	acpModePlan      = "https://agentclientprotocol.com/protocol/session-modes#plan"
	acpModeAutopilot = "https://agentclientprotocol.com/protocol/session-modes#autopilot"
)

func acpModeID(mode string) string {
	if mode == "plan" {
		return acpModePlan
	}
	return acpModeAgent
}

func modeFromACP(id string) string {
	switch id {
	case acpModePlan:
		return "plan"
	case "":
		return ""
	default:
		return "normal"
	}
}

// NewDriver returns the managed copilot Driver（driverOf が /turn・/respond から
// 引く）。read 層は agentImpl をそのまま埋め込んで温存する。
func NewDriver() agents.Driver { return managedDriver{} }

type managedDriver struct{ agentImpl }

// Capabilities（§3.1・docs/36 契約）。Steer は driver 内キュー（ACP に mid-turn
// 注入の口が無い — opencode と同じ意味論）。DynamicModel/Effort は false: 子の
// 起動フラグで固定（変更はセッション再作成）。Mode は session/set_mode がネイティブ。
func (managedDriver) Capabilities() agents.Capabilities {
	return agents.Capabilities{
		ProcessModel: "per-session-child",
		Steer:        true,
		DynamicMode:  true,
		Questions:    true,
	}
}

// Resume returns the session's ThreadHandle, spawning the child runtime and
// creating/loading the copilot session when needed（Driver IF: 無ければ新規
// start。§6 の reconciliation 共通手順を兼ねる）。
func (managedDriver) Resume(m session.Meta) (agents.ThreadHandle, error) {
	if m.Kind != session.KindCopilot {
		return nil, errors.New("copilot driver は copilot セッション専用です")
	}
	if !session.DirExists(m.Dir) {
		return nil, agents.DirGoneErr(m.Dir)
	}
	slotSid := session.UUID(m.Dir, m.Name) // identity: the working copy, never the subdir
	handlesMu.Lock()
	h := handles[m.Name]
	if h == nil {
		h = &threadHandle{
			name:    m.Name,
			dir:     m.CWD(), // Dir, or the subdir chosen at launch
			slotSid: slotSid,
			events:  make(chan agents.Event, 64),
		}
		handles[m.Name] = h
	}
	handlesMu.Unlock()

	// spawn を handle 単位で直列化する（kiro A2-4 と同型）: boot の ReconcileManaged と
	// 直後の /turn が並行に Resume すると check-then-spawn が非直列で二重 spawn し、
	// 先発の子プロセスが孤児化する。ロック取得後に liveness を再確認する。
	h.spawnMu.Lock()
	defer h.spawnMu.Unlock()

	h.mu.Lock()
	if h.alive && h.cl != nil && !h.cl.dead() {
		h.mu.Unlock()
		return h, nil
	}
	// 起動時の設定既定は meta から（mode の動的変更は UpdateSettings が上書き）。
	if h.settings.Model == "" {
		h.settings.Model = m.Model
	}
	if h.settings.Effort == "" {
		h.settings.Effort = m.Effort
	}
	if h.settings.Mode == "" {
		h.settings.Mode = m.Mode
	}
	st := h.settings
	h.mu.Unlock()

	// First Resume of a forked slot (docs/55): mint this slot's session id and build its
	// session-state directory from the source before spawning, so the spawn below takes
	// the ordinary session/load path. Without this the slot has no sid, spawn falls to
	// session/new, and the branch quietly opens as an empty conversation.
	if m.ForkFrom != "" && sids.Read(slotSid) == "" {
		sid, err := newSessionID()
		if err != nil {
			return nil, fmt.Errorf("セッション ID を採番できません: %w", err)
		}
		if err := MaterializeForkAt(m.ForkFrom, sid, m.ForkAt); err != nil {
			return nil, fmt.Errorf("分岐を作成できませんでした: %w", err)
		}
		sids.Write(slotSid, sid)
	}

	if err := h.spawn(st); err != nil {
		return nil, err
	}

	// exit recording の baseline（tui の startSessionTmux と同じ役割）。
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

// DropHandle detaches a managed session from its runtime (stop/halt/archive):
// interrupt any running turn, terminate the child, forget the handle. The
// conversation stays in $COPILOT_HOME/session-state — a later Resume re-spawns
// and session/load reattaches（実測: 履歴リプレイ＋文脈保持）。
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
	cmd, cl, sid, running := h.cmd, h.cl, h.sid, h.running
	h.mu.Unlock()
	if running && cl != nil && sid != "" {
		_ = cl.notifyPeer("session/cancel", map[string]any{"sessionId": sid})
	}
	stopChild(cmd)
}

// RemoveLedger drops the ClientMessageID ledger（/stop — スロットの
// アイデンティティごと破棄する時だけ。halt/archive は再開があるので残す）。
func RemoveLedger(name string) { ledger.Remove(name) }

// ManagedAlive reports whether the session has a live runtime handle.
func ManagedAlive(name string) bool {
	h := handleFor(name)
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.alive
}

// ManagedBusy reports a turn is running or queued (graceful shutdown の待ち条件).
func ManagedBusy(name string) bool {
	h := handleFor(name)
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.running || len(h.queue) > 0
}

// AbortManaged interrupts every running managed turn（graceful shutdown の
// per-pane Ctrl-C 相当）。
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

// Shutdown terminates every managed child（agent 終了時。会話正本は copilot 側の
// ストアに残り、次回 boot の ReconcileManaged が再接続する）。
func Shutdown() {
	handlesMu.Lock()
	var cmds []*exec.Cmd
	for _, h := range handles {
		h.mu.Lock()
		h.alive = false
		cmds = append(cmds, h.cmd)
		h.mu.Unlock()
	}
	handlesMu.Unlock()
	for _, c := range cmds {
		stopChild(c)
	}
}

// ReconcileManaged re-attaches managed copilot sessions after an Agent boot or
// child death（§6）。対象は「停止扱いになっていない」managed メタ全部。失敗しても
// セッションは 停止中 として残り、ユーザーの 再開 クリックで再試行される。
func ReconcileManaged(reason string) {
	d := managedDriver{}
	for _, m := range session.ListMetas() {
		if m.Kind != session.KindCopilot || m.DriverKind() != session.DriverManaged || m.Archived {
			continue
		}
		if m.StoppedAt != "" && handleFor(m.Name) == nil {
			continue // deliberately stopped — resume only on user action
		}
		if _, err := d.Resume(m); err != nil {
			log.Printf("copilot managed: reconcile %s (%s): %v", m.Name, reason, err)
		}
	}
}

// stopChild terminates a child process: SIGTERM（copilot は graceful に
// session.shutdown を刻む）→ 猶予後に SIGKILL。reap は spawn 時の watch
// goroutine（cmd.Wait）が担う — 独自 spawn 経路の全数 reap（dev/04 §4.3）。
func stopChild(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	p := cmd.Process
	if p.Signal(syscall.SIGTERM) != nil {
		return // already gone
	}
	time.AfterFunc(3*time.Second, func() { _ = p.Kill() })
}

// --- thread handle -----------------------------------------------------------

type threadHandle struct {
	name    string
	dir     string
	slotSid string

	spawnMu sync.Mutex // serializes spawns for this handle（並行 Resume の二重 spawn 防止・kiro A2-4 と同型）

	mu       sync.Mutex
	cmd      *exec.Cmd
	cl       *acpClient
	sid      string // copilot session UUID
	alive    bool
	state    agents.TurnState
	running  bool
	pumping  bool
	queue    []agents.TurnInput
	settings agents.ThreadSettings
	inter    *agents.Interaction
	permID   json.RawMessage // pending session/request_permission の JSON-RPC id
	permOpts []string        // Interaction の選択肢 index → ACP optionId
	events   chan agents.Event
}

// spawn starts the child runtime, initializes ACP and loads/creates the copilot
// session. Caller must NOT hold h.mu.
func (h *threadHandle) spawn(st agents.ThreadSettings) error {
	args := []string{"--acp", "--no-remote", "--no-remote-export"}
	if st.Mode != "plan" {
		// fleet 既定の bypass。plan 起動では外す（TUI と同じ判断 — 承認は
		// Interaction として表面化させる）。
		args = append(args, "--allow-all")
	}
	concreteModel := st.Model != "" && st.Model != "auto"
	if concreteModel {
		args = append(args, "--model", st.Model)
	}
	// Auto (copilot's default / the only Free model) rejects --effort ("Model \"auto\"
	// does not support reasoning effort configuration") — only pass it with an explicit
	// non-auto model, else the child errors on startup.
	if st.Effort != "" && concreteModel {
		args = append(args, "--effort", st.Effort)
	}
	bin := os.Getenv("AGENT_COPILOT_BIN")
	if bin == "" {
		bin = "copilot"
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = h.dir
	env := append(os.Environ(), "COPILOT_AUTO_UPDATE=false")
	if tok := Token(); tok != "" {
		// gh 透過認証のトークンを明示注入（ambient フォールバックは実測で動くが
		// 公式未文書 — 子プロセスの env は決定的にできるのでこちらを正とする）。
		env = append(env, "COPILOT_GITHUB_TOKEN="+tok)
	}
	cmd.Env = env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("copilot runtime を起動できません: %w", err)
	}
	cl := newACPClient(stdin, stdout)
	// クロージャで当該 cl を捕捉する: 初回 spawn 中は h.cl が未代入のまま readLoop が
	// 走り得るので、h.cl 参照だと未知メソッド応答で nil デリファレンス panic になる。
	cl.onRequest = func(id json.RawMessage, method string, params json.RawMessage) {
		h.onServerRequest(cl, id, method, params)
	}
	go h.watch(cmd, cl)

	if _, err := cl.call("initialize", map[string]any{
		"protocolVersion": 1, "clientCapabilities": map[string]any{},
	}, 30*time.Second); err != nil {
		stopChild(cmd)
		return fmt.Errorf("copilot runtime の initialize に失敗しました: %w", err)
	}

	sid := h.sid
	if sid == "" {
		sid = sids.Read(h.slotSid)
	}
	mode := ""
	if sid != "" {
		// クロスプロセス resume（実測: 履歴リプレイ＋文脈保持）。リプレイは会話長に
		// 比例するので余裕を持つ。
		res, err := cl.call("session/load", map[string]any{
			"sessionId": sid, "cwd": h.dir, "mcpServers": []any{},
		}, 180*time.Second)
		if err != nil {
			// session/new へ落ちてよいのは sid のローカルストア（session-state/<sid>）が
			// 実際に消えている＝会話が削除済みのときだけ（kiro A2-1 と同型）。ストア健在の
			// 一時失敗で new すると生きた会話を無言で切り離し sid を上書きしてしまう。
			if _, statErr := os.Stat(sessionStateDir(sid)); statErr != nil {
				log.Printf("copilot managed: session/load %s: store gone (%v) — 新規セッションで再開", h.name, err)
				sid = ""
			} else {
				stopChild(cmd)
				return fmt.Errorf("copilot セッションを読み込めませんでした（時間をおいて再開してください）: %w", err)
			}
		} else {
			mode = currentModeOf(res)
		}
	}
	if sid == "" {
		res, err := cl.call("session/new", map[string]any{
			"cwd": h.dir, "mcpServers": []any{},
		}, 60*time.Second)
		if err != nil {
			stopChild(cmd)
			return fmt.Errorf("copilot セッションを作成できません: %w", err)
		}
		var out struct {
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(res, &out) != nil || out.SessionID == "" {
			stopChild(cmd)
			return errors.New("copilot セッションの作成応答を解釈できません")
		}
		sid = out.SessionID
		sids.Write(h.slotSid, sid)
		mode = currentModeOf(res)
	}

	h.mu.Lock()
	h.cmd, h.cl, h.sid, h.alive = cmd, cl, sid, true
	h.state = agents.TurnCompleted // 子は生まれたて — 走行中 turn は存在しない
	h.inter, h.permID, h.permOpts = nil, nil, nil
	if m := modeFromACP(mode); m != "" {
		h.settings.Mode = m
	}
	wantMode := h.settings.Mode
	h.mu.Unlock()

	// meta の希望モードが runtime の現在モードと違えば再表明（resume 後の既定戻り
	// 対策 — codex の approvalPolicy 再表明と同じ理屈。best-effort）。
	if wantMode != "" && wantMode != modeFromACP(mode) {
		_, _ = cl.call("session/set_mode", map[string]any{
			"sessionId": sid, "modeId": acpModeID(wantMode),
		}, 15*time.Second)
	}
	return nil
}

// currentModeOf extracts modes.currentModeId from a session/new・load result.
func currentModeOf(res json.RawMessage) string {
	var out struct {
		Modes struct {
			CurrentModeID string `json:"currentModeId"`
		} `json:"modes"`
	}
	_ = json.Unmarshal(res, &out)
	return out.Modes.CurrentModeID
}

// watch reaps the child and records its exit（record-exit の managed 対応 —
// per-session child なので daemon supervisor と違い帰属が正確）。SIGTERM 由来
// （DropHandle/Shutdown）は "stopped" になり Console は通常の 停止中 を出す。
func (h *threadHandle) watch(cmd *exec.Cmd, cl *acpClient) {
	err := cmd.Wait()
	_ = err
	code, sig := 0, 0
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok {
		if ws.Signaled() {
			sig = int(ws.Signal())
			code = 128 + sig
		} else {
			code = ws.ExitStatus()
		}
	}
	oom := false
	base := uint64(0)
	if prev, ok := status.ReadExit(h.name); ok {
		base = prev.OOMBase
	}
	if cur, ok := status.OOMKillCount(); ok && cur > base {
		oom = true
	}
	status.PersistExit(h.name, status.ExitInfo{
		Reason: status.ExitReasonFor(code, sig, oom),
		Code:   code, Signal: sig,
		At:      time.Now().Format(time.RFC3339),
		OOMBase: base,
	})
	cl.markClosed()
	h.mu.Lock()
	stale := h.cl != cl // 既に新しい子へ差し替わっている（respawn 後の旧 watch）
	h.mu.Unlock()
	if !stale {
		h.runtimeLost()
	}
}

// emit pushes an event without ever blocking a state transition (drop on
// overflow — events are advisory; the source of truth is Snapshot + events.jsonl).
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

func (h *threadHandle) currentState() agents.TurnState {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.state
}

// runtimeLost drops the handle to unknown（§6-1: 切断時の正直な状態）。
func (h *threadHandle) runtimeLost() {
	h.mu.Lock()
	h.alive = false
	h.state = agents.TurnUnknown
	h.inter, h.permID, h.permOpts = nil, nil, nil
	h.mu.Unlock()
	h.emit(agents.Event{Kind: "turn_state", TurnState: agents.TurnUnknown})
}

// --- ThreadHandle interface ---------------------------------------------------

func (h *threadHandle) Send(in agents.TurnInput) error { return h.accept(in) }

// Steer は driver 内キュー（ACP に mid-turn 注入の口が無い — 完走後に次 turn
// として投入、opencode と同じ意味論）。
func (h *threadHandle) Steer(in agents.TurnInput) error { return h.accept(in) }

func (h *threadHandle) accept(in agents.TurnInput) error {
	if strings.TrimSpace(in.Prompt) == "" {
		return errors.New("empty prompt")
	}
	in.ClientMessageID = normalizeMsgID(in.ClientMessageID)
	h.mu.Lock()
	if !h.alive {
		h.mu.Unlock()
		return errors.New("runtime が停止しています（再開してください）")
	}
	if h.inter != nil {
		h.mu.Unlock()
		return agents.ErrQuestionPending
	}
	// 再送の冪等化（台帳・§4）は pump の実行開始時に行う — キュー投入前に永続記録すると、
	// クラッシュでキューが失われた後の再送が「既知」として無言破棄される。
	h.queue = append(h.queue, in)
	start := !h.pumping
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

// pump processes the queue serially（子は排他なので waitIdle は不要）。
func (h *threadHandle) pump() {
	for {
		h.mu.Lock()
		if len(h.queue) == 0 || !h.alive {
			h.pumping = false
			h.mu.Unlock()
			return
		}
		in := h.queue[0]
		h.queue = h.queue[1:]
		if ledger.SeenOrRecord(h.name, in.ClientMessageID) {
			h.mu.Unlock()
			continue // 再送 — 台帳（永続、プロセス跨ぎ）が実行開始時に冪等化（§4）
		}
		h.running = true
		h.mu.Unlock()

		h.runTurn(in)

		h.mu.Lock()
		h.running = false
		h.mu.Unlock()
	}
}

// runTurn executes ONE blocking session/prompt and lands the terminal state.
// turn 境界の MarkTurnStart/End が status ストアと docs/30 の完了報告を駆動する
// （notify seam — 0c80377/451ff8b の教訓）。
func (h *threadHandle) runTurn(in agents.TurnInput) {
	agents.MarkTurnStart(h.slotSid)
	defer func() { agents.MarkTurnEnd(h.slotSid, h.currentState()) }()
	h.setState(agents.TurnStarting)
	h.mu.Lock()
	cl, sid := h.cl, h.sid
	h.mu.Unlock()
	if cl == nil || sid == "" {
		h.setState(agents.TurnFailed)
		return
	}
	h.setState(agents.TurnRunning)
	res, err := cl.call("session/prompt", map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]any{{"type": "text", "text": in.Prompt}},
	}, 0) // no timeout — a turn runs as long as it runs
	h.mu.Lock()
	interrupted := h.state == agents.TurnInterrupting
	h.inter, h.permID, h.permOpts = nil, nil, nil // turn が終わった＝待ちは無い
	h.mu.Unlock()
	if err != nil {
		if interrupted {
			h.setState(agents.TurnCancelled)
		} else {
			// transport 断 = 子の喪失: 正直に unknown へ落とし §6 に委ねる。
			h.setState(agents.TurnUnknown)
		}
		return
	}
	var out struct {
		StopReason string `json:"stopReason"`
	}
	_ = json.Unmarshal(res, &out)
	switch {
	case interrupted || out.StopReason == "cancelled":
		h.setState(agents.TurnCancelled)
	case out.StopReason == "refusal":
		h.setState(agents.TurnFailed)
	default: // end_turn / max_tokens / …
		h.setState(agents.TurnCompleted)
	}
}

// Interrupt cancels the running turn and clears the queued 追撃（停止の意思表示は
// キューにも及ぶ）。
func (h *threadHandle) Interrupt() error {
	h.mu.Lock()
	cl, sid := h.cl, h.sid
	running := h.running
	h.queue = nil
	if running {
		h.state = agents.TurnInterrupting
	}
	h.mu.Unlock()
	if !running || cl == nil {
		return nil
	}
	h.emit(agents.Event{Kind: "turn_state", TurnState: agents.TurnInterrupting})
	return cl.notifyPeer("session/cancel", map[string]any{"sessionId": sid})
}

// UpdateSettings applies dynamic settings. Mode は session/set_mode がネイティブ
// （実測）。Model/Effort は子の起動フラグ固定なので動的変更不可 — Capabilities が
// DynamicModel/Effort:false を表明しており Console は UI を出さないが、防御的に
// 明示エラーを返す。
func (h *threadHandle) UpdateSettings(s agents.ThreadSettings) error {
	if s.Model != "" || s.ClearModel || s.Effort != "" || s.ClearEffort {
		return errors.New("copilot はモデル/effort の稼働中変更に未対応です（セッションを作り直してください）")
	}
	if s.Mode == "" {
		return nil
	}
	h.mu.Lock()
	cl, sid := h.cl, h.sid
	h.mu.Unlock()
	if cl == nil {
		return errors.New("runtime が停止しています")
	}
	if _, err := cl.call("session/set_mode", map[string]any{
		"sessionId": sid, "modeId": acpModeID(s.Mode),
	}, 15*time.Second); err != nil {
		return err
	}
	h.mu.Lock()
	h.settings.Mode = s.Mode
	cur := h.settings
	h.mu.Unlock()
	h.emit(agents.Event{Kind: "settings", Settings: &cur})
	return nil
}

// Respond answers the pending Interaction（§5）— copilot では
// session/request_permission への応答。answer/allow は選択肢 index を ACP の
// optionId へ変換、deny は reject 系 optionId、cancel は outcome:"cancelled"。
func (h *threadHandle) Respond(reply agents.InteractionReply) error {
	h.mu.Lock()
	inter, permID, permOpts, cl := h.inter, h.permID, h.permOpts, h.cl
	h.mu.Unlock()
	if inter == nil || inter.ID != reply.ID || cl == nil {
		return fmt.Errorf("interaction %s は待機中ではありません", reply.ID)
	}
	var result map[string]any
	switch reply.Decision {
	case agents.DecisionCancel:
		result = map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}
	case agents.DecisionDeny:
		opt := findOption(permOpts, "reject")
		if opt == "" {
			result = map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}
		} else {
			result = map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": opt}}
		}
	case agents.DecisionAllow, agents.DecisionAnswer:
		opt := ""
		if len(reply.Answers) > 0 && len(reply.Answers[0].Options) > 0 {
			i := reply.Answers[0].Options[0]
			if i < 0 || i >= len(permOpts) {
				return fmt.Errorf("選択肢 %d は範囲外です", i)
			}
			opt = permOpts[i]
		} else {
			opt = findOption(permOpts, "allow")
		}
		if opt == "" {
			return errors.New("承認の選択肢を解決できません")
		}
		result = map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": opt}}
	default:
		return fmt.Errorf("unsupported decision: %s", reply.Decision)
	}
	if err := cl.respond(permID, result); err != nil {
		return err
	}
	h.mu.Lock()
	h.inter, h.permID, h.permOpts = nil, nil, nil
	running := h.running
	if running {
		h.state = agents.TurnRunning
	}
	h.mu.Unlock()
	if running {
		// turn が走っていない時に偽の「実行中」を購読側へ流さない。
		h.emit(agents.Event{Kind: "turn_state", TurnState: agents.TurnRunning})
	}
	return nil
}

// findOption returns the first optionId containing the substring ("allow" /
// "reject" — 実測の語彙: allow_once / allow_always / reject_once)。
func findOption(opts []string, sub string) string {
	for _, o := range opts {
		if strings.Contains(o, sub) {
			return o
		}
	}
	return ""
}

func (h *threadHandle) Events() <-chan agents.Event { return h.events }

func (h *threadHandle) Snapshot() (agents.ThreadSnapshot, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return agents.ThreadSnapshot{
		TurnState:   h.state,
		Interaction: h.inter,
		Settings:    h.settings,
	}, nil
}

// onServerRequest handles server-initiated requests on the readLoop goroutine —
// MUST NOT block: record the Interaction and return; the answer goes back later
// via Respond → cl.respond.
func (h *threadHandle) onServerRequest(cl *acpClient, id json.RawMessage, method string, params json.RawMessage) {
	if method != "session/request_permission" {
		// 未知のサーバー発リクエストは応答しないと turn が固まる — エラーで返す。
		_ = cl.write(map[string]any{
			"jsonrpc": "2.0", "id": id,
			"error": map[string]any{"code": -32601, "message": "unsupported request: " + method},
		})
		return
	}
	var req struct {
		ToolCall struct {
			ToolCallID string `json:"toolCallId"`
			Title      string `json:"title"`
			Kind       string `json:"kind"`
			RawInput   struct {
				Command string `json:"command"`
			} `json:"rawInput"`
		} `json:"toolCall"`
		Options []struct {
			OptionID string `json:"optionId"`
			Name     string `json:"name"`
			Kind     string `json:"kind"`
		} `json:"options"`
	}
	if json.Unmarshal(params, &req) != nil {
		return
	}
	q := transcript.Question{
		Header:   "許可",
		Question: req.ToolCall.Title,
	}
	if req.ToolCall.RawInput.Command != "" {
		q.Question += "\n`" + req.ToolCall.RawInput.Command + "`"
	}
	var optIDs []string
	for _, o := range req.Options {
		q.Options = append(q.Options, transcript.Option{Label: o.Name})
		optIDs = append(optIDs, o.OptionID)
	}
	interID := req.ToolCall.ToolCallID
	if interID == "" {
		interID = "perm-" + strings.TrimSpace(string(id))
	}
	q.ID = interID
	inter := &agents.Interaction{ID: interID, Kind: "question", Prompt: req.ToolCall.Title,
		Questions: []transcript.Question{q}}
	h.mu.Lock()
	h.inter, h.permID, h.permOpts = inter, id, optIDs
	h.state = agents.TurnWaitingInteraction
	h.mu.Unlock()
	h.emit(agents.Event{Kind: "interaction", TurnState: agents.TurnWaitingInteraction, Interaction: inter})
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

// managedEnrich folds the driver-side state into the read layer's TranscriptData
// （transcript.go から呼ばれる）: pending permission へ Interaction id を載せ、
// driver 内キューを キュー済み へ、driver 設定の mode を chip へ合流する。tui
// セッション（handle 無し）には何もしない。
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
		td.Mode = modeSet
	}
}

// normalizeMsgID mirrors the other drivers' convention: empty → driver 採番。
func normalizeMsgID(id string) string {
	if id != "" {
		return id
	}
	b, err := newSessionID()
	if err != nil {
		return fmt.Sprintf("af-%d", time.Now().UnixNano())
	}
	return "af-" + b
}
