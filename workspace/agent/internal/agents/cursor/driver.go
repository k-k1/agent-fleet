package cursor

// cursor の managed driver（docs/log/40 Track A2）— per-session child 方式。セッション毎に
// `cursor-agent acp` を子プロセスとして抱え、session/new・session/load（クロスプロセス
// resume、実測）・session/prompt（blocking）・session/cancel・session/set_mode に turn
// 状態機械・Interaction・reconciliation をマッピングする。copilot の driver.go（docs/log/36）
// と同じ骨格で、cursor 固有の差分は 1 点だけ:
//
//   **cursor の ACP はローカル痕跡を一切書かない**（TUI/-p が書く JSONL 転写も、hooks も
//   ACP 経路では出ない — 履歴はサーバ側。docs/log/40 §プローブ）。copilot は全経路で
//   events.jsonl を書くので managed の転写もファイルから読めたが、cursor はそれが無い。
//   よって転写は driver が `session/update` 通知（agent_message_chunk / agent_thought_chunk
//   / tool_call / tool_call_update）から**メモリ上に構築**し、`session/load` の全量リプレイ
//   （user_message_chunk から再生 — 実測）で復元する。managedTranscript() が read 層
//   （transcript.go）へ供給する。停止中の managed セッションは handle が無い＝転写も無い
//   （resume で session/load リプレイが再構築する。ローカル正本が無い設計の帰結）。
//
// per-session child を選ぶ理由: ACP に per-session のモデル指定が無く（session/new 応答の
// models は列挙のみ）、子プロセス毎の `--model` フラグで固定するのが確実。権限
// （session/request_permission）は --force 運転では発生しない（実測: echo が確認なしで
// 実行された）が、plan 起動では --force を外すため到達しうる。「UI に出ないから発生
// しない」を信用せず（agy df996e4 の教訓）、常に Interaction(question) へ写像する。

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

// ledger は ClientMessageID の永続台帳（再送・reconnect の二重投入をプロセス跨ぎで冪等化）。
var ledger = agents.NewMsgLedger("cursor-msgledger")

// ACP のモード id は素の "agent"/"plan"/"ask"（実測 — copilot の URL 形式とは異なる）。
// AF 語彙 "plan"/"normal" と相互変換する。
func acpModeID(mode string) string {
	if mode == "plan" {
		return "plan"
	}
	return "agent"
}

func modeFromACP(id string) string {
	switch id {
	case "plan":
		return "plan"
	case "":
		return ""
	default: // agent / ask
		return "normal"
	}
}

// NewDriver returns the managed cursor Driver（driverOf が /turn・/respond から引く）。
// read 層は agentImpl をそのまま埋め込んで温存する。
func NewDriver() agents.Driver { return managedDriver{} }

type managedDriver struct{ agentImpl }

// Capabilities。Steer は driver 内キュー（ACP に mid-turn 注入の口が無い — copilot/
// opencode と同じ意味論）。DynamicModel は false: 子の起動フラグで固定（変更はセッション
// 再作成）。Mode は session/set_mode がネイティブ（実測: {} 応答＋current_mode_update 通知）。
func (managedDriver) Capabilities() agents.Capabilities {
	return agents.Capabilities{
		ProcessModel: "per-session-child",
		Steer:        true,
		DynamicMode:  true,
		Questions:    true,
	}
}

// Resume returns the session's ThreadHandle, spawning the child runtime and
// creating/loading the cursor session when needed（Driver IF: 無ければ新規 start。
// reconciliation の共通手順を兼ねる）。
func (managedDriver) Resume(m session.Meta) (agents.ThreadHandle, error) {
	if m.Kind != session.KindCursor {
		return nil, errors.New("cursor driver は cursor セッション専用です")
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
	if h.settings.Model == "" {
		h.settings.Model = m.Model
	}
	if h.settings.Mode == "" {
		h.settings.Mode = m.Mode
	}
	// 権限確認をスキップするか（docs/log/76）は meta と ui-prefs から毎 Resume 解決する。
	// ThreadSettings に載せないのは、あちらが「空 = 変更しない」の動的更新用で bool を
	// 3 値にできないから — 設定変更後の再 spawn でも、ここで解決し直した値が効く。
	h.bypass = agents.SkipPermissions(m)
	st := h.settings
	h.mu.Unlock()

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
// conversation stays on cursor's server — a later Resume re-spawns and
// session/load reattaches（実測: 履歴リプレイ＋文脈保持）。
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

// RemoveLedger drops the ClientMessageID ledger（/stop — スロットのアイデンティティごと
// 破棄する時だけ。halt/archive は再開があるので残す）。
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

// AbortManaged interrupts every running managed turn（graceful shutdown の per-pane
// Ctrl-C 相当）。
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

// Shutdown terminates every managed child（agent 終了時。会話正本は cursor 側のサーバに
// 残り、次回 boot の ReconcileManaged が session/load で再接続する）。
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

// ReconcileManaged re-attaches managed cursor sessions after an Agent boot or child
// death。対象は「停止扱いになっていない」managed メタ全部。失敗してもセッションは
// 停止中 として残り、ユーザーの 再開 クリックで再試行される。
func ReconcileManaged(reason string) {
	d := managedDriver{}
	for _, m := range session.ListMetas() {
		if m.Kind != session.KindCursor || m.DriverKind() != session.DriverManaged || m.Archived {
			continue
		}
		if m.StoppedAt != "" && handleFor(m.Name) == nil {
			continue // deliberately stopped — resume only on user action
		}
		if _, err := d.Resume(m); err != nil {
			log.Printf("cursor managed: reconcile %s (%s): %v", m.Name, reason, err)
		}
	}
}

// stopChild terminates a child process group: SIGTERM → 猶予後に SIGKILL。cursor は
// ターン後に worker-server 常駐プロセスを残す（実測 — docs/log/40）ので、プロセス「グループ」
// を落とすのが要点（spawn は Setpgid でグループを分けている）。reap は spawn 時の watch
// goroutine（cmd.Wait）が担う。
func stopChild(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	// プロセスグループ全体へ（-pid）。worker-server の取り残しを防ぐ。
	if syscall.Kill(-pid, syscall.SIGTERM) != nil {
		return // already gone
	}
	time.AfterFunc(3*time.Second, func() {
		// 子が回収済みなら pid（グループ）は再利用され得る — 生の -pid SIGKILL を
		// 無関係プロセスへ飛ばさないよう本体の生存を確認する（Wait 済みの Process への
		// Signal は ErrProcessDone を返す・レース安全）。
		if cmd.Process.Signal(syscall.Signal(0)) == nil {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
	})
}

// --- thread handle -----------------------------------------------------------

type threadHandle struct {
	name    string
	dir     string
	slotSid string

	spawnMu sync.Mutex // serializes spawns for this handle（並行 Resume の二重 spawn 防止・kiro A2-4 と同型）

	// bypass は「権限確認をスキップする」か（docs/log/76）。Resume が meta から解決して
	// 置く — spawn は meta を持たないので、ここに載せて渡す。plan は Resume 時点では
	// なく spawn の st.Mode で見る（稼働中のモード変更で再 spawn されるため）。
	bypass bool

	mu       sync.Mutex
	cmd      *exec.Cmd
	cl       *acpClient
	sid      string // cursor session UUID
	model    string // ACP currentModelId（モデルバッジ用・Auto は default[]）
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

	buf transcriptBuf // ACP session/update から構築する転写（別ロックで保護）
}

// spawn starts the child runtime, initializes ACP and loads/creates the cursor
// session. Caller must NOT hold h.mu.
// bypassNow reports the resolved「権限確認をスキップする」choice (docs/log/76). Resume writes
// it under h.mu; spawn runs without the lock, so read it through here.
func (h *threadHandle) bypassNow() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.bypass
}

func (h *threadHandle) spawn(st agents.ThreadSettings) error {
	// base: 背景自己更新の封殺（root option なので acp の前に必須 — 実測）＋ acp サブ
	// コマンド ＋ workspace trust スキップ（実測: --trust が無いと ACP でも trust
	// プロンプトで固まる）。plan では --force を外し、承認を Interaction として表面化。
	args := []string{disableAutoUpdateFlag, "acp", "--trust"}
	if h.bypassNow() && st.Mode != "plan" {
		args = append(args, "--force") // fleet 既定の bypass（"unless explicitly denied"）
	}
	if st.Model != "" && st.Model != "auto" {
		// per-session child のモデル固定。ACP に per-session の指定口が無いための起動フラグ。
		args = append(args, "--model", st.Model)
	}
	cmd := exec.Command(bin(), args...)
	cmd.Dir = h.dir
	// worker-server の取り残しをグループごと落とせるよう、専用プロセスグループにする。
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// 認証は ambient（~/.config/cursor/auth.json を CLI 自身が拾う — 実測: env 注入なしで完走）。
	// CI だけは外す（ci_env.go）。
	cmd.Env = EnvWithoutCI(os.Environ())
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
		return fmt.Errorf("cursor runtime を起動できません: %w", err)
	}
	cl := newACPClient(stdin, stdout)
	// クロージャで当該 cl を捕捉する: 初回 spawn 中は h.cl が未代入のまま readLoop が
	// 走り得るので、h.cl 参照だと未知メソッド応答で nil デリファレンス panic になる。
	cl.onRequest = func(id json.RawMessage, method string, params json.RawMessage) {
		h.onServerRequest(cl, id, method, params)
	}
	cl.onNotify = h.onNotify
	go h.watch(cmd, cl)

	if _, err := cl.call("initialize", map[string]any{
		"protocolVersion": 1, "clientCapabilities": map[string]any{},
	}, 30*time.Second); err != nil {
		stopChild(cmd)
		return fmt.Errorf("cursor runtime の initialize に失敗しました: %w", err)
	}

	sid := h.sid
	if sid == "" {
		sid = sids.Read(h.slotSid)
	}
	mode := ""
	modelID := ""
	if sid != "" {
		// クロスプロセス resume（実測: session/update リプレイで履歴＋文脈を復元）。
		// リプレイ通知が buf を再構築するので、その前に空にする（二重計上防止）。
		h.buf.reset()
		h.buf.setLoading(true)
		res, err := cl.call("session/load", map[string]any{
			"sessionId": sid, "cwd": h.dir, "mcpServers": []any{},
		}, 180*time.Second)
		h.buf.setLoading(false) // 最後の assistant ターンを flush
		if err != nil {
			// kiro A2-1 と同じ理屈: 一時失敗（timeout・lock 競合等）で session/new に
			// 落ちると、生きた会話を無言で切り離し slot の sid を空の新会話で上書き
			// してしまう。cursor はローカルストアが無く「会話が本当に消えた」を決定的に
			// 判定できないため、失敗は再試行可能なエラーとして返し停止中のままにする。
			h.buf.reset()
			stopChild(cmd)
			return fmt.Errorf("cursor セッションを読み込めませんでした（時間をおいて再開してください）: %w", err)
		} else {
			mode = currentModeOf(res)
			modelID = currentModelOf(res)
		}
	}
	if sid == "" {
		res, err := cl.call("session/new", map[string]any{
			"cwd": h.dir, "mcpServers": []any{},
		}, 60*time.Second)
		if err != nil {
			stopChild(cmd)
			return fmt.Errorf("cursor セッションを作成できません: %w", err)
		}
		var out struct {
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(res, &out) != nil || out.SessionID == "" {
			stopChild(cmd)
			return errors.New("cursor セッションの作成応答を解釈できません")
		}
		sid = out.SessionID
		sids.Write(h.slotSid, sid)
		mode = currentModeOf(res)
		modelID = currentModelOf(res)
	}

	h.mu.Lock()
	h.cmd, h.cl, h.sid, h.alive = cmd, cl, sid, true
	h.state = agents.TurnCompleted // 子は生まれたて — 走行中 turn は存在しない
	h.model = modelID              // ACP currentModelId（Auto は default[]）— モデルバッジ用
	h.inter, h.permID, h.permOpts = nil, nil, nil
	if m := modeFromACP(mode); m != "" {
		h.settings.Mode = m
	}
	wantMode := h.settings.Mode
	h.mu.Unlock()

	// meta の希望モードが runtime の現在モードと違えば再表明（resume 後の既定戻り対策・
	// best-effort）。
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

// currentModelOf extracts models.currentModelId from a session/new・load result
// （Auto は `default[]`・明示指定は bracket 形式で返る — 実測。docs/log/40 §モデル表示）。
func currentModelOf(res json.RawMessage) string {
	var out struct {
		Models struct {
			CurrentModelID string `json:"currentModelId"`
		} `json:"models"`
	}
	_ = json.Unmarshal(res, &out)
	return out.Models.CurrentModelID
}

// watch reaps the child and records its exit（per-session child なので daemon supervisor
// と違い帰属が正確）。SIGTERM 由来（DropHandle/Shutdown）は "stopped" になり Console は
// 通常の 停止中 を出す。
func (h *threadHandle) watch(cmd *exec.Cmd, cl *acpClient) {
	_ = cmd.Wait()
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

// emit pushes an event without ever blocking a state transition (drop on overflow).
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

// runtimeLost drops the handle to unknown（切断時の正直な状態）。
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

// Steer は driver 内キュー（ACP に mid-turn 注入の口が無い — 完走後に次 turn として投入）。
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
	// 再送の冪等化（台帳）は pump の実行開始時に行う — キュー投入前に永続記録すると、
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
			continue // 再送 — 台帳（永続、プロセス跨ぎ）が実行開始時に冪等化
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
// turn 境界の MarkTurnStart/End が status ストアと docs/log/30 の完了報告を駆動する。
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
	// ACP は live turn では user_message_chunk を出さない（実測）ので、ユーザーターンは
	// ここで転写へ確定させる（replay の user_message_chunk とは経路が分かれる）。
	h.buf.addUserTurn(in.Prompt)
	h.setState(agents.TurnRunning)
	res, err := cl.call("session/prompt", map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]any{{"type": "text", "text": in.Prompt}},
	}, 0) // no timeout — a turn runs as long as it runs
	h.buf.flushAsst() // turn 終端で開いた assistant ターンを確定（ACP に turn_ended 通知は無い）
	h.mu.Lock()
	interrupted := h.state == agents.TurnInterrupting
	h.inter, h.permID, h.permOpts = nil, nil, nil // turn が終わった＝待ちは無い
	h.mu.Unlock()
	if err != nil {
		if interrupted {
			h.setState(agents.TurnCancelled)
		} else {
			// transport 断 = 子の喪失: 正直に unknown へ落とす。
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

// Interrupt cancels the running turn and clears the queued 追撃。
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

// UpdateSettings applies dynamic settings. Mode は session/set_mode がネイティブ（実測）。
// Model は子の起動フラグ固定なので動的変更不可 — Capabilities が DynamicModel:false を
// 表明しており Console は UI を出さないが、防御的に明示エラーを返す。
func (h *threadHandle) UpdateSettings(s agents.ThreadSettings) error {
	if s.Model != "" || s.ClearModel || s.Effort != "" || s.ClearEffort {
		return errors.New("cursor はモデルの稼働中変更に未対応です（セッションを作り直してください）")
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

// Respond answers the pending Interaction — cursor では session/request_permission への
// 応答。answer/allow は選択肢 index を ACP の optionId へ変換、deny は reject 系 optionId、
// cancel は outcome:"cancelled"。
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

// findOption returns the first optionId containing the substring ("allow" / "reject" —
// ACP 語彙の allow_once / allow_always / reject_once)。
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

// onNotify accumulates the transcript from session/update on the readLoop goroutine.
// MUST be fast (no RPC, no h.mu) — it only appends to the transcript buffer (tmu)
// and, for current_mode_update, records the mode under h.mu.
func (h *threadHandle) onNotify(method string, params json.RawMessage) {
	if method != "session/update" {
		return
	}
	var p struct {
		Update struct {
			SessionUpdate string `json:"sessionUpdate"`
			Content       struct {
				Text string `json:"text"`
			} `json:"content"`
			// tool_call / tool_call_update
			ToolCallID string          `json:"toolCallId"`
			Title      string          `json:"title"`
			Status     string          `json:"status"`
			RawInput   json.RawMessage `json:"rawInput"`
			RawOutput  json.RawMessage `json:"rawOutput"`
			// ACP classifies a tool call itself: read | edit | delete | move | search |
			// execute | think | fetch | other, with the files it touches in `locations`.
			// That is what the changed-files list (docs/log/68) keys off here — the protocol's
			// own vocabulary, rather than the CLI's tool NAMES which are free to change.
			Kind      string `json:"kind"`
			Locations []struct {
				Path string `json:"path"`
			} `json:"locations"`
			// current_mode_update
			CurrentModeID string `json:"currentModeId"`
			// available_commands_update — CLI 広告のスキル/コマンド一覧
			// （builtin skill ＋ global ＋ project 全部入り。実測 2026-07-28:
			// {name:"simplify", description:"…(global)"} 形・先頭スラッシュ無し）。
			AvailableCommands []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"availableCommands"`
		} `json:"update"`
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	u := p.Update
	switch u.SessionUpdate {
	case "available_commands_update":
		// スキルピッカー（docs/log/50 v2）へ publish。map store 1 回なので readLoop を塞がない。
		cmds := make([]agents.AdvertisedCommand, 0, len(u.AvailableCommands))
		for _, c := range u.AvailableCommands {
			cmds = append(cmds, agents.AdvertisedCommand{
				Name:        strings.TrimPrefix(c.Name, "/"),
				Description: c.Description,
			})
		}
		agents.PublishCommands(h.name, cmds)
	case "user_message_chunk":
		h.buf.userChunk(u.Content.Text)
	case "agent_message_chunk":
		h.buf.agentChunk(u.Content.Text)
	case "agent_thought_chunk":
		h.buf.thoughtChunk(u.Content.Text)
	case "tool_call":
		file, verb, edits := "", "", []transcript.Edit(nil)
		switch u.Kind {
		case "edit", "move":
			verb = "edit"
		case "delete":
			verb = "delete"
		}
		if verb != "" {
			// `move` reports its DESTINATION last (source first), and for an edit there is
			// normally exactly one location; either way the last one is where the content
			// lives now, which is what a reader wants to open.
			if n := len(u.Locations); n > 0 {
				file = u.Locations[n-1].Path
			}
			// rawInput still carries the before/after, so the +/- counters work here too.
			// Read by SHAPE, not by name: `title` is a display string ("Write /tmp/x"),
			// and turning it back into a tool name would be exactly the string contract
			// the protocol's `kind` lets us avoid.
			f, es := editsFromInput(u.RawInput)
			if file == "" {
				file = f
			}
			edits = es
		}
		h.buf.toolCall(u.ToolCallID, u.Title, toolInfo(u.RawInput), file, verb, edits)
	case "tool_call_update":
		if out := toolOutput(u.RawOutput); out != "" {
			h.buf.toolOutput(u.ToolCallID, out)
		}
	case "current_mode_update":
		if m := modeFromACP(u.CurrentModeID); m != "" {
			h.mu.Lock()
			h.settings.Mode = m
			cur := h.settings
			h.mu.Unlock()
			h.emit(agents.Event{Kind: "settings", Settings: &cur})
		}
	}
}

// toolInfo extracts a short label from a tool_call rawInput（command が最も情報量が高い）。
func toolInfo(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var in struct {
		Command     string `json:"command"`
		Description string `json:"description"`
		Path        string `json:"path"`
		FilePath    string `json:"file_path"`
		TargetFile  string `json:"target_file"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return ""
	}
	for _, s := range []string{in.Command, in.Description, in.Path, in.FilePath, in.TargetFile} {
		if s != "" {
			return s
		}
	}
	return ""
}

// toolOutput renders a tool_call_update rawOutput（shell の exitCode/stdout/stderr — 実測）。
func toolOutput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var out struct {
		ExitCode *int   `json:"exitCode"`
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return ""
	}
	s := out.Stdout
	if out.Stderr != "" {
		if s != "" {
			s += "\n"
		}
		s += out.Stderr
	}
	if s == "" && out.ExitCode != nil && *out.ExitCode != 0 {
		s = fmt.Sprintf("(exit %d)", *out.ExitCode)
	}
	return s
}

// onServerRequest handles server-initiated requests on the readLoop goroutine —
// MUST NOT block: record the Interaction and return; the answer goes back later via
// Respond → cl.respond. --force 運転では発生しないが、plan 起動では到達しうる。
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
			ToolCallID string          `json:"toolCallId"`
			Title      string          `json:"title"`
			Kind       string          `json:"kind"`
			RawInput   json.RawMessage `json:"rawInput"`
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
	q := transcript.Question{Header: "許可", Question: req.ToolCall.Title}
	if cmd := toolInfo(req.ToolCall.RawInput); cmd != "" {
		q.Question += "\n`" + cmd + "`"
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

// pendingPermission は保留中の ACP `session/request_permission` を「何を訊かれて
// いたか」の 1 行へ畳む（docs/log/75 P5）。保留が無ければ ""。
func (h *threadHandle) pendingPermission() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.inter == nil {
		return ""
	}
	if len(h.inter.Questions) > 0 && strings.TrimSpace(h.inter.Questions[0].Question) != "" {
		return h.inter.Questions[0].Question
	}
	return h.inter.Prompt
}

// managedLiveState は managed ルートの live 状態（state.go の LiveState が読む）。
//
// なぜ要るか: ACP はローカル転写を書かないので JSONL 末尾分類は managed では常に空を
// 返し、**一覧のチップも reaper の分類材料も無いまま**だった。結果、承認待ちで固まった
// managed セッションは「状態不明」に落ち、tier1 が畳むことも tier2 が起こし続けることも
// 無い（docs/log/75 の分類でいう unknown）。turn 状態機械が唯一の情報源なので、そこから供給する。
//
// 承認待ちを **question** と名乗るのは、ミラーが描く許可カード（td.Pending）と語彙を
// 揃えるため — 一覧のバッジと本文のチップが食い違うと、どちらが本当か利用者に分からない
// （EffectiveModal の教訓と同じ）。持ち越しの Kind が permission なのは別の軸の話で、
// あちらは「再開後に何を配達できるか」を決めている。
func managedLiveState(m session.Meta) string {
	h := handleFor(m.Name)
	if h == nil {
		return "" // 停止中 / 未接続 — 状態については意見を持たない
	}
	switch h.currentState() {
	case agents.TurnWaitingInteraction:
		return "question"
	case agents.TurnQueued, agents.TurnStarting, agents.TurnRunning, agents.TurnInterrupting:
		return "working"
	}
	return "idle"
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

// managedTranscript builds the read-layer TranscriptData for a managed cursor session
// entirely from the driver's in-memory accumulator（transcript.go から呼ばれる）:
// ACP はローカル転写を書かないので、これが唯一のミラー源。停止中（handle 無し）は空。
func managedTranscript(m session.Meta) agents.TranscriptData {
	h := handleFor(m.Name)
	if h == nil {
		return agents.TranscriptData{}
	}
	td := agents.TranscriptData{Turns: h.buf.snapshot()}
	h.mu.Lock()
	inter := h.inter
	modeSet := h.settings.Mode
	// モデルバッジ: ユーザーが明示選択したモデル（settings.Model・ピッカーの dash 形式）を
	// 優先し、Auto/未指定なら ACP の currentModelId（default[]）を使う（docs/log/40 §モデル表示）。
	modelID := h.settings.Model
	if modelID == "" || modelID == "auto" {
		modelID = h.model
	}
	h.mu.Unlock()
	stampModel(td.Turns, displayModel(modelID))
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
	return td
}

// normalizeMsgID mirrors the other drivers' convention: empty → driver 採番。
func normalizeMsgID(id string) string {
	if id != "" {
		return id
	}
	if b, err := newChatID(); err == nil {
		return "af-" + b
	}
	return fmt.Sprintf("af-%d", time.Now().UnixNano())
}
