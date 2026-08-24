package kiro

// kiro の managed driver（docs/43 Track A2）— per-session child 方式。セッション毎に
// `kiro-cli acp` を子プロセスとして抱え、session/new・session/load（クロスプロセス
// resume、実測）・session/prompt（blocking）・session/cancel に turn 状態機械・
// Interaction・reconciliation をマッピングする。cursor / copilot の driver.go（docs/40,36）
// と同じ骨格で、kiro 固有の差分は 2 点:
//
//  1. **`.lock` によるクロスプロセス排他**（cursor に無い）。kiro のセッションは
//     `~/.kiro/sessions/cli/<sid>.lock`（pid 入り）で 1 プロセス占有され、session/load は
//     旧所有プロセスが生きている間は「Session is active in another process (PID …)」で
//     拒否する（実測）。よって停止は **stdin を閉じて EOF で正規終了**させ（kiro-cli acp は
//     exit 0＋.lock を除去する — 実測）、resume 時の session/load は lock 競合を数回リトライ
//     して旧所有者の消滅を待つ（stopChild / spawn の retry）。
//  2. **ACP 転写がローカルにも persist される**（cursor は書かない）。kiro の acp は
//     v2 JSONL（~/.kiro/sessions/cli/<sid>.jsonl・TUI と共用）へ全ターンを追記するので、
//     停止して handle が無いときは transcript.go の fileTranscript がそれを読む
//     （managedTranscript のフォールバック）。生きた handle の間は cursor 同様
//     session/update 通知からメモリ構築（mirror.go transcriptBuf）してライブ配信する。
//
// per-session child を選ぶ理由: モデル/effort/agent-engine を子プロセスの起動フラグで固定
// するのが確実（ACP に per-session のモデル指定口が無い）。権限（session/request_permission）は
// --trust-all-tools 運転では発生しないが、plan 起動では --trust-all-tools を外すため到達
// しうる。「UI に出ないから発生しない」を信用せず（agy 3aaebf6 の教訓）、常に
// Interaction(question) へ写像する。
//
// ライブ使用量（`_kiro.dev/metadata` の contextUsagePercentage / meteringUsage）は cursor で
// 不可能だった経路として T0 で確認済みだが、コンテキストバー表示は registry contextBar／
// get_session_usage 側の配線（A2 のファイルスコープ外・Track C で A2 送りとした部分）が要る
// ため v1 A2 では UI へ配線しない。ここでは onNotify でこの通知を黙って捨てる（将来の Track で
// 拾える seam として明示）。

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
var ledger = agents.NewMsgLedger("kiro-msgledger")

// ACP のモード id は kiro の agent 名（kiro_default / kiro_planner / kiro_guide）。AF 語彙
// "plan"/"normal" と相互変換する（plan=planner・それ以外=default。guide は normal 扱い）。
func acpModeID(mode string) string {
	if mode == "plan" {
		return "kiro_planner"
	}
	return "kiro_default"
}

func modeFromACP(id string) string {
	switch id {
	case "kiro_planner":
		return "plan"
	case "":
		return ""
	default: // kiro_default / kiro_guide
		return "normal"
	}
}

// NewDriver returns the managed kiro Driver（driverOf が /turn・/respond から引く）。
// read 層は agentImpl をそのまま埋め込んで温存する。
func NewDriver() agents.Driver { return managedDriver{} }

type managedDriver struct{ agentImpl }

// Capabilities。Steer は driver 内キュー（ACP に mid-turn 注入の口が無い — cursor/copilot/
// opencode と同じ意味論）。Dynamic* はすべて false: モデル/effort/モードは子の起動フラグで
// 固定（変更はセッション再作成）——registry も planMode/effort を出さない（3 モード循環で
// クリーンな二値でない・カタログに effort メタ無し）ので、Console は動的 UI を出さない。
// Questions は plan 起動（--trust-all-tools を外す）で session/request_permission を拾うため true。
func (managedDriver) Capabilities() agents.Capabilities {
	return agents.Capabilities{
		ProcessModel: "per-session-child",
		Steer:        true,
		Questions:    true,
	}
}

// Resume returns the session's ThreadHandle, spawning the child runtime and
// creating/loading the kiro session when needed（Driver IF: 無ければ新規 start。
// reconciliation の共通手順を兼ねる）。
func (managedDriver) Resume(m session.Meta) (agents.ThreadHandle, error) {
	if m.Kind != session.KindKiro {
		return nil, errors.New("kiro driver は kiro セッション専用です")
	}
	if !session.DirExists(m.Dir) {
		return nil, agents.DirGoneErr(m.Dir)
	}
	ensureSettings()                       // 冪等: autoupdate off ＋ --trust-all の危険モード確認ダイアログ抑止
	slotSid := session.UUID(m.Dir, m.Name) // identity: the working copy, never the subdir
	handlesMu.Lock()
	h := handles[m.Name]
	if h == nil {
		h = &threadHandle{
			name:      m.Name,
			dir:       m.CWD(), // Dir, or the subdir chosen at launch
			slotSid:   slotSid,
			createdAt: slotCreatedAt(m), // discoverSid のフェンス（read 層 resolveSid と同じ）
			events:    make(chan agents.Event, 64),
		}
		handles[m.Name] = h
	}
	handlesMu.Unlock()

	h.mu.Lock()
	alive := h.alive && h.cl != nil && !h.cl.dead()
	h.mu.Unlock()
	if alive {
		return h, nil
	}

	// spawn を handle 単位で直列化する（A2-4）: boot の ReconcileManaged と直後の /turn が
	// 並行に Resume すると check-then-spawn が非直列で二重 spawn し、後発の子が先発の .lock に
	// 弾かれて枯渇→session/new へ直行しかねない。spawnMu を取ってから liveness を再確認し、
	// 先発が既に立てていればその handle を再利用する（二度目の spawn はしない）。
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
	if h.settings.Effort == "" {
		h.settings.Effort = m.Effort
	}
	if h.settings.Mode == "" {
		h.settings.Mode = m.Mode
	}
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
// interrupt any running turn, gracefully terminate the child (stdin EOF → kiro exits
// and releases its .lock), forget the handle. The conversation stays on disk (v2 JSONL);
// a later Resume re-spawns and session/load reattaches（実測: 履歴リプレイ＋文脈保持）。
func DropHandle(name string) { dropHandle(name, 0) }

// DropHandleWait is DropHandle that additionally waits (bounded) for the child to
// actually exit. Used by the managed→TUI driver switch (A2-2): the TUI relaunch does a
// `--resume-id` on the same session, and kiro's per-sid .lock rejects that (or mints a
// new sid = split-brain) while the managed child still holds it. Waiting for the graceful
// stdin-EOF exit (which releases the .lock) closes that race. Best-effort: on timeout we
// proceed anyway (the TUI launch will surface any residual lock error itself).
func DropHandleWait(name string, wait time.Duration) { dropHandle(name, wait) }

func dropHandle(name string, wait time.Duration) {
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
	cmd, cl, stdin, sid, running, exited := h.cmd, h.cl, h.stdin, h.sid, h.running, h.exited
	h.mu.Unlock()
	if running && cl != nil && sid != "" {
		_ = cl.notifyPeer("session/cancel", map[string]any{"sessionId": sid})
	}
	stopChild(cmd, stdin)
	if wait > 0 && exited != nil {
		select {
		case <-exited:
		case <-time.After(wait):
		}
	}
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

// ManagedContext returns the live context fill for a managed kiro session (Track D —
// docs/43 §10): the latest `_kiro.dev/metadata` contextUsagePercentage (0–100), the
// running model's context window in tokens, the accumulated meteringUsage credits, and
// the model id. ok=false when there's no live handle or no metadata has arrived yet —
// so TUI sessions and pre-first-turn managed sessions show no context bar. Cheap
// (in-memory read); safe to call from the /sessions/usage aggregation.
func ManagedContext(name string) (pct float64, window int, credits float64, model string, ok bool) {
	h := handleFor(name)
	if h == nil {
		return 0, 0, 0, "", false
	}
	h.mu.Lock()
	alive := h.alive
	model = h.model
	h.mu.Unlock()
	if !alive {
		return 0, 0, 0, "", false
	}
	h.usageMu.Lock()
	defer h.usageMu.Unlock()
	if !h.hasUsage {
		return 0, 0, 0, "", false
	}
	window = h.ctxWindow
	if window <= 0 {
		window = kiroDefaultWindow
	}
	return h.ctxPct, window, h.credits, model, true
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

// Shutdown terminates every managed child（agent 終了時。会話正本は v2 JSONL に残り、
// 次回 boot の ReconcileManaged が session/load で再接続する）。stdin を閉じて .lock を
// 綺麗に手放させるのが要点（次 boot の resume が lock 競合しない）。
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

// ReconcileManaged re-attaches managed kiro sessions after an Agent boot or child
// death。対象は「停止扱いになっていない」managed メタ全部。失敗してもセッションは
// 停止中 として残り、ユーザーの 再開 クリックで再試行される。
func ReconcileManaged(reason string) {
	d := managedDriver{}
	for _, m := range session.ListMetas() {
		if m.Kind != session.KindKiro || m.DriverKind() != session.DriverManaged || m.Archived {
			continue
		}
		if m.StoppedAt != "" && handleFor(m.Name) == nil {
			continue // deliberately stopped — resume only on user action
		}
		if _, err := d.Resume(m); err != nil {
			log.Printf("kiro managed: reconcile %s (%s): %v", m.Name, reason, err)
		}
	}
}

// stopChild terminates a child gracefully-first: close stdin (EOF) so kiro-cli acp exits
// 0 and removes its .lock（実測 — これで後続 session/load の「active in another process」
// を避ける）。EOF を無視して残った場合の安全網としてプロセスグループへ SIGTERM→SIGKILL。
// reap は spawn 時の watch goroutine（cmd.Wait）が担う。
func stopChild(cmd *exec.Cmd, stdin io.Closer) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if stdin != nil {
		_ = stdin.Close() // graceful: EOF → exit 0 ＋ .lock 除去
	}
	pid := cmd.Process.Pid
	// 安全網: EOF で落ちなければグループごと（-pid）落とす。Setpgid でグループを分けて
	// いるので子が抱える補助プロセスも掃ける。子が回収済みなら pid（グループ）は再利用
	// され得るため、生の -pid シグナルの前に本体の生存を確認する（Wait 済みの Process
	// への Signal は ErrProcessDone を返す・レース安全）。
	time.AfterFunc(4*time.Second, func() {
		if cmd.Process.Signal(syscall.Signal(0)) != nil {
			return // already reaped — グループへは送らない
		}
		if syscall.Kill(-pid, syscall.SIGTERM) == nil {
			time.AfterFunc(3*time.Second, func() {
				if cmd.Process.Signal(syscall.Signal(0)) == nil {
					_ = syscall.Kill(-pid, syscall.SIGKILL)
				}
			})
		}
	})
}

// --- thread handle -----------------------------------------------------------

type threadHandle struct {
	name      string
	dir       string
	slotSid   string
	createdAt time.Time // slot 作成時刻（discoverSid のフェンス・前身セッション誤採用防止）

	spawnMu sync.Mutex // serializes spawns for this handle（並行 Resume の二重 spawn 防止・A2-4）

	mu       sync.Mutex
	cmd      *exec.Cmd
	stdin    io.WriteCloser // kept so stop can EOF-close it (graceful .lock release)
	exited   chan struct{}  // closed by watch when the current child exits（切替の有界待ち・A2-2）
	cl       *acpClient
	sid      string // kiro session UUID（CLI 採番）
	model    string // ACP currentModelId（モデルバッジ用・auto は "auto"）
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

	// ライブ使用量（Track D — docs/43 §10）。`_kiro.dev/metadata` 通知が運ぶ
	// contextUsagePercentage（最新値）と meteringUsage（累積 credit）を保持する。
	// onNotify（readLoop goroutine）が更新するので h.mu とは別ロックにして turn 配管と
	// 競合させない。読み手は ManagedContext（context.go / session_usage.go 経由でミラーの
	// ContextBar と get_session_usage に配線）。
	usageMu   sync.Mutex
	ctxWindow int     // 現在モデルの context window（tokens・spawn 時に ModelWindow で確定）
	ctxPct    float64 // 最新の contextUsagePercentage（0–100）
	credits   float64 // meteringUsage の累積（この handle の生存中・in-memory）
	hasUsage  bool    // metadata を一度でも受けたか（未受信は context 非表示）
}

// spawn starts the child runtime, initializes ACP and loads/creates the kiro session.
// Caller must NOT hold h.mu.
func (h *threadHandle) spawn(st agents.ThreadSettings) error {
	// acp サブコマンド ＋ v2 エンジン明示ピン（read 正本の v2 JSONL を書くエンジン。既定は
	// v2 だが将来 v3 へ振れないよう固定）＋ fleet 既定の bypass（--trust-all-tools）。plan では
	// --trust-all-tools を外し、承認を Interaction として表面化させる。
	args := []string{"acp", "--agent-engine", "v2"}
	if st.Mode != "plan" {
		args = append(args, "--trust-all-tools")
	}
	if st.Model != "" && st.Model != "auto" {
		// per-session child のモデル固定（ACP に per-session の指定口が無い）。
		args = append(args, "--model", st.Model)
	}
	if st.Effort != "" {
		args = append(args, "--effort", st.Effort)
	}
	cmd := exec.Command(bin(), args...)
	cmd.Dir = h.dir
	// 補助プロセスもグループごと落とせるよう専用プロセスグループにする。
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// 認証は ambient（~/.local/share/kiro-cli/data.sqlite3 を CLI 自身が拾う — 実測: env
	// 注入なしで完走）。ACP は未認証なら stderr「You are not logged in」で即終了（fail-fast）。
	cmd.Env = os.Environ()
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
		return fmt.Errorf("kiro runtime を起動できません: %w", err)
	}
	cl := newACPClient(stdin, stdout)
	// クロージャで当該 cl を捕捉する: 初回 spawn 中は h.cl が未代入のまま readLoop が
	// 走り得るので、h.cl 参照だと未知メソッド応答で nil デリファレンス panic になる。
	cl.onRequest = func(id json.RawMessage, method string, params json.RawMessage) {
		h.onServerRequest(cl, id, method, params)
	}
	cl.onNotify = h.onNotify
	exited := make(chan struct{})
	go h.watch(cmd, cl, exited)

	if _, err := cl.call("initialize", map[string]any{
		"protocolVersion": 1, "clientCapabilities": map[string]any{},
	}, 30*time.Second); err != nil {
		stopChild(cmd, stdin)
		return fmt.Errorf("kiro runtime の initialize に失敗しました: %w", err)
	}

	sid := h.sid
	if sid == "" {
		sid = sids.Read(h.slotSid)
	}
	// sid キャッシュが空でも、この cwd に既存の kiro セッションがあれば拾う（read 層
	// resolveSid と同じ discover＝cwd+mtime）。Terminal→managed 切替で TUI 側の sid が
	// sidstore に未キャッシュのまま切り替わると、ここが無いと無言で新規会話を切ってしまう
	// （A2-3）。**フェンス（slot 作成時刻）必須**: recreate は同一 dir に新スラグを切るため、
	// フェンス無しの discover は前身の旧セッション .json を拾い、A2-1 の「ストア健在なら
	// load 成功」ロジックで旧会話へ無言継続してしまう（managed 経路での A-1 再発）。
	// worktree で dir が分かれる前提の同一 cwd 制約も resolveSid と同じ。
	if sid == "" {
		if d := discoverSid(h.dir, h.createdAt); d != "" {
			sid = d
			sids.Write(h.slotSid, d)
		}
	}
	mode := ""
	modelID := ""
	if sid != "" {
		// クロスプロセス resume（実測: session/update リプレイで履歴＋文脈を復元）。旧所有
		// プロセスがまだ .lock を握っていると「active in another process」で弾かれるので、
		// stopChild が旧子を落とす猶予ぶんだけ数回リトライして待つ。
		res, lerr := h.loadWithLockRetry(cl, sid)
		if lerr != nil {
			// session/new へ落ちてよいのは、その sid のストア（<sid>.json）が実際に消えている
			// ＝会話が削除済みのときだけ。ストアが健在なままの load 失敗（別プロセスが .lock を
			// 握っている／文言ドリフトで lock と判定できなかった／一時失敗）で session/new すると、
			// **生きた会話を切り離し slot の sid を新セッションで上書きしてしまう**（A2-1）。よって
			// ストア健在時はエラーを返し、セッションは停止中のまま（再開クリックで再試行可能）に
			// する。判定はディスク上のストア存在（決定的）で行い、脆い文字列マッチには依存しない。
			if _, statErr := os.Stat(sessionJSONPath(sid)); statErr != nil {
				log.Printf("kiro managed: session/load %s: store gone (%v) — 新規セッションで再開", h.name, lerr)
				sid = ""
				h.buf.reset()
			} else {
				// 部分リプレイを buf に残さない — 残すと完全なファイル転写より優先表示される。
				h.buf.reset()
				stopChild(cmd, stdin)
				return fmt.Errorf("kiro セッションを読み込めませんでした（別プロセスが占有中の可能性・時間をおいて再開してください）: %w", lerr)
			}
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
			stopChild(cmd, stdin)
			return fmt.Errorf("kiro セッションを作成できません: %w", err)
		}
		var out struct {
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(res, &out) != nil || out.SessionID == "" {
			stopChild(cmd, stdin)
			return errors.New("kiro セッションの作成応答を解釈できません")
		}
		sid = out.SessionID
		sids.Write(h.slotSid, sid) // read 層（resolveSid）と共有する CLI 採番 sid
		mode = currentModeOf(res)
		modelID = currentModelOf(res)
	}

	h.mu.Lock()
	h.cmd, h.stdin, h.cl, h.sid, h.alive = cmd, stdin, cl, sid, true
	h.exited = exited              // この子の終了シグナル（DropHandleWait の有界待ち用）
	h.state = agents.TurnCompleted // 子は生まれたて — 走行中 turn は存在しない
	h.model = modelID
	h.inter, h.permID, h.permOpts = nil, nil, nil
	if m := modeFromACP(mode); m != "" {
		h.settings.Mode = m
	}
	wantMode := h.settings.Mode
	h.mu.Unlock()

	// Track D: この子のモデルの context window を確定（pct→token 変換の分母）。metadata の
	// pct は window を運ばないのでカタログ（--list-models）から引く。resume/再spawn では
	// pct/credits を持ち越さず ctxWindow だけ更新（credits は in-memory な生存中カウント）。
	if win := ModelWindow(modelID); win > 0 {
		h.usageMu.Lock()
		h.ctxWindow = win
		h.usageMu.Unlock()
	}

	// meta の希望モードが runtime の現在モードと違えば再表明（resume 後の既定戻り対策・
	// best-effort）。plan 起動時は kiro_planner へ。
	if wantMode != "" && wantMode != modeFromACP(mode) {
		_, _ = cl.call("session/set_mode", map[string]any{
			"sessionId": sid, "modeId": acpModeID(wantMode),
		}, 15*time.Second)
	}
	return nil
}

// loadWithLockRetry calls session/load, retrying while the prior owner still holds the
// session's .lock（「active in another process」）. Non-lock errors return immediately so
// spawn can fall back to a fresh session/new. Each attempt resets the replay buffer so a
// partial replay before an error isn't double-counted.
// lockRetryAttempts / lockRetryDelay bound the .lock wait（~6s 既定）。テストが縮められる
// よう var にする。
var (
	lockRetryAttempts = 10
	lockRetryDelay    = 600 * time.Millisecond
)

func (h *threadHandle) loadWithLockRetry(cl *acpClient, sid string) (json.RawMessage, error) {
	var lastErr error
	for attempt := 0; attempt < lockRetryAttempts; attempt++ {
		h.buf.reset()
		h.buf.setLoading(true)
		res, err := cl.call("session/load", map[string]any{
			"sessionId": sid, "cwd": h.dir, "mcpServers": []any{},
		}, 180*time.Second)
		h.buf.setLoading(false) // 最後の assistant ターンを flush
		if err == nil {
			return res, nil
		}
		lastErr = err
		if !isLockBusy(err) {
			return nil, err
		}
		h.buf.reset()
		time.Sleep(lockRetryDelay) // 旧所有プロセスが消えて .lock が解放されるのを待つ
	}
	return nil, lastErr
}

// isLockBusy reports the "Session is active in another process" refusal (kiro's per-sid
// .lock is still held by a just-killed prior owner — retry until it exits). Requires the
// JSON-RPC error code (-32603) AND the message so an unrelated error can't be misread as a
// lock-busy. Only gates the RETRY decision — the session/new-vs-fail choice in spawn is made
// from the on-disk store, not this string, so a message drift stays fail-safe (A2-1).
func isLockBusy(err error) bool {
	var re *rpcError
	if !errors.As(err, &re) {
		return false
	}
	return re.Code == -32603 && strings.Contains(strings.ToLower(re.Error()), "active in another process")
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

// currentModelOf extracts models.currentModelId from a session/new・load result（実測:
// auto は "auto"・named は素の id）。
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
// と違い帰属が正確）。stdin EOF 由来（DropHandle/Shutdown）は exit 0＝"stopped" 相当で
// Console は通常の 停止中 を出す。
func (h *threadHandle) watch(cmd *exec.Cmd, cl *acpClient, exited chan struct{}) {
	defer close(exited) // 切替の DropHandleWait を解放（この子固有のチャネル）
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
// turn 境界の MarkTurnStart/End が status ストアと docs/30 の完了報告を駆動する。
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

// UpdateSettings applies dynamic settings. kiro はモデル/effort/モードをすべて子の起動
// フラグで固定する（Capabilities が Dynamic* すべて false を表明・Console は UI を出さない）
// ので、防御的に明示エラーを返す（セッションを作り直して反映）。
func (h *threadHandle) UpdateSettings(s agents.ThreadSettings) error {
	if s.Model != "" || s.ClearModel || s.Effort != "" || s.ClearEffort || s.Mode != "" {
		return errors.New("kiro は稼働中の設定変更に未対応です（セッションを作り直してください）")
	}
	return nil
}

// Respond answers the pending Interaction — kiro では session/request_permission への
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
// MUST be fast (no RPC, no h.mu) — it only appends to the transcript buffer and, for
// current_mode_update, records the mode under h.mu. kiro の `_kiro.dev/*` 通知
// （metadata / subagent / commands / retry_warning）はここで受けるが v1 A2 は使わない
// （ライブ使用量の UI 配線は将来 Track — driver.go 冒頭参照）。
func (h *threadHandle) onNotify(method string, params json.RawMessage) {
	if method == "_kiro.dev/metadata" {
		h.onMetadata(params) // Track D: ライブ context% / credits
		return
	}
	if method != "session/update" {
		return // その他の _kiro.dev/*（subagent / commands / retry_warning）は使わない
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
			// current_mode_update
			CurrentModeID string `json:"currentModeId"`
		} `json:"update"`
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	u := p.Update
	switch u.SessionUpdate {
	case "user_message_chunk":
		h.buf.userChunk(u.Content.Text)
	case "agent_message_chunk":
		h.buf.agentChunk(u.Content.Text)
	case "agent_thought_chunk":
		h.buf.thoughtChunk(u.Content.Text)
	case "tool_call":
		h.buf.toolCall(u.ToolCallID, u.Title, toolInfo(u.RawInput))
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

// onMetadata folds a `_kiro.dev/metadata` notification into the handle's live usage
// (Track D). Called on the readLoop goroutine — must be fast (a single lock, no RPC).
// The percentage is the CURRENT context fill (it can shrink after compaction, so we
// keep the latest, not a max); credits accumulate per turn（実測: value は当該ターンの
// 消費・ターン終了時のみ付く）。A metadata notification may carry only one of the two
// (実測: percentage 単独 / percentage+credits) — a nil field leaves the prior value.
func (h *threadHandle) onMetadata(params json.RawMessage) {
	var p struct {
		ContextUsagePercentage *float64 `json:"contextUsagePercentage"`
		MeteringUsage          []struct {
			Value float64 `json:"value"`
			Unit  string  `json:"unit"`
		} `json:"meteringUsage"`
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	h.usageMu.Lock()
	defer h.usageMu.Unlock()
	if p.ContextUsagePercentage != nil {
		h.ctxPct = *p.ContextUsagePercentage
		h.hasUsage = true
	}
	for _, mu := range p.MeteringUsage {
		if mu.Unit == "credit" {
			h.credits += mu.Value
			h.hasUsage = true
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
		Purpose     string `json:"__tool_use_purpose"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return ""
	}
	for _, s := range []string{in.Command, in.Description, in.Path, in.FilePath, in.Purpose} {
		if s != "" {
			return s
		}
	}
	return ""
}

// toolOutput renders a tool_call_update rawOutput（shell の exit_status/stdout/stderr。
// kiro の v2 JSONL toolResult と同じ形と、ACP 標準の exitCode/stdout/stderr の両方を拾う）。
func toolOutput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var out struct {
		ExitCode   *int   `json:"exitCode"`
		ExitStatus string `json:"exit_status"`
		Stdout     string `json:"stdout"`
		Stderr     string `json:"stderr"`
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
	if s == "" {
		if out.ExitCode != nil && *out.ExitCode != 0 {
			s = fmt.Sprintf("(exit %d)", *out.ExitCode)
		} else if out.ExitStatus != "" && !strings.HasSuffix(out.ExitStatus, "0") {
			s = "(" + out.ExitStatus + ")"
		}
	}
	return s
}

// onServerRequest handles server-initiated requests on the readLoop goroutine —
// MUST NOT block: record the Interaction and return; the answer goes back later via
// Respond → cl.respond. --trust-all-tools 運転では発生しないが、plan 起動では到達しうる。
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
// いたか」の 1 行へ畳む（docs/75 P5）。保留が無ければ ""。
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
// なぜ要るか: managed にはペインが無いので TUI 文字列契約は常に空を返し、**一覧の
// チップも reaper の分類材料も無いまま**だった。結果、承認待ちで固まった managed
// セッションは「状態不明」に落ち、tier1 が畳むことも無い（docs/75 の unknown）。
// turn 状態機械が唯一の情報源なので、そこから供給する（cursor と同型）。
//
// 承認待ちを **question** と名乗るのは、ミラーが描く許可カード（td.Pending）および
// TUI ルートの分類（classifyPane の "requires approval" → question）と語彙を揃える
// ため。持ち越しの Kind が permission なのは別の軸（再開後に何を配達できるか）。
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

// managedTranscript builds the read-layer TranscriptData for a managed kiro session
// （transcript.go の DriverManaged 分岐から呼ばれる）。生きた handle があれば driver が
// session/update から組んだメモリ転写（ライブストリーミング込み）を返す。handle が無い
// （停止/未起動）ときは kiro が persist した v2 JSONL を fileTranscript が読む（cursor は
// ローカル転写が無いので停止中は空だったが、kiro は persist するので停止中でも履歴を出せる）。
func managedTranscript(m session.Meta) agents.TranscriptData {
	h := handleFor(m.Name)
	if h == nil || h.buf.empty() {
		return fileTranscript(m)
	}
	td := agents.TranscriptData{Turns: h.buf.snapshot()}
	h.mu.Lock()
	inter := h.inter
	modeSet := h.settings.Mode
	// モデルバッジ: ユーザーが明示選択したモデル（settings.Model）を優先し、auto/未指定なら
	// ACP の currentModelId を使う。
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
	return fmt.Sprintf("af-%d", time.Now().UnixNano())
}
