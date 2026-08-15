package opencode

// OpenCode の managed driver（docs/27 P2）— Driver 型（agents.Driver/ThreadHandle）の
// 初のフル実装。共有 `opencode serve` の HTTP＋SSE に turn 状態機械（§4）・
// Interaction（§5）・reconciliation（§6）をマッピングする。
//
// 実測（1.17.18、docs/27 §12.2）に基づく API 選定:
//   - turn 駆動は v1 の blocking POST /session/{id}/message（唯一 read 層の正本
//     message/part に書く駆動口）。goroutine で包んで非同期化する。
//     prompt_async は user message を書くだけで turn が始まらない（実測・不採用）。
//     v2 /api/session/{id}/prompt は別ストア（session_message）に書き read 層から
//     見えないため不採用。
//   - mid-turn steer の口は v1 に無い（v2 の delivery=steer は実行中 v1 turn に
//     注入されず独自 turn を始める — 実測）。Steer は driver 内キューで受け、実行中
//     turn の完走後に次 turn として投入する（§4 の queued 状態そのもの）。
//   - interrupt = POST /session/{id}/abort（blocking 呼び出しは 200 で部分結果を
//     返し、assistant message に completed が刻まれる — resume 安全）。
//   - question = question.asked/replied/rejected イベント＋GET /question＋
//     POST /question/{id}/reply {answers: [[label,…]]}（回答はラベル文字列）。
//   - 添付 = v1 file part {type:"file", mime, url:"file://…"}（実測で読解確認）。
//
// ClientMessageID（§4）の冪等化は driver の台帳（accept()）のみで行い、serve へは
// messageID を渡さない — 実測（1.17.18）: turn ループは message id の辞書順に依存し、
// 既存 id より小さいクライアント採番 id の turn は拾われず /message が返らない
// （prompt_async が不動に見えた真因も同じ）。
//
// serve の session/question/status/event 面はプロジェクト（directory）にスコープ
// される（実測）ので、セッションの dir を directory クエリで常に併送し、SSE は
// プロジェクト横断の /global/event を購読する。

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// turnClient runs the blocking /message calls: no timeout — a turn legitimately
// runs for minutes/hours. Interrupt (abort) or daemon death unblocks it.
var turnClient = &http.Client{}

// ledger は ClientMessageID の永続台帳（§9.5 のプロセス跨ぎ永続化 — §12.2-3 の
// 将来課題を P3 で解消。in-memory 台帳は Agent 再起動を跨ぐ再送に効かなかった）。
var ledger = agents.NewMsgLedger("opencode-msgledger")

// NewDriver returns the managed opencode Driver（driverOf が /turn・/respond から
// 引く）。read 層は agentImpl をそのまま埋め込んで温存する。
func NewDriver() agents.Driver { return managedDriver{} }

type managedDriver struct{ agentImpl }

// Capabilities（§3.1）。Steer は「実行中 turn への追撃を受け付ける」の意（driver 内
// キュー実装 — 上記ファイルコメント）。TUIAttach は opencode 固有の強み（serve へ
// `opencode attach` で無停止アタッチ、実測済み）。
func (managedDriver) Capabilities() agents.Capabilities {
	return agents.Capabilities{
		ProcessModel:  "shared-daemon",
		Steer:         true,
		Fork:          true,
		DynamicModel:  true,
		DynamicEffort: true,
		DynamicMode:   true,
		Questions:     true,
		TUIAttach:     true,
	}
}

// Resume returns the session's ThreadHandle, creating the runtime session when
// none exists yet（Driver IF: 無ければ新規 start）。§6 の reconciliation 共通手順を
// 兼ねる: runtime 確保 → session 解決 → snapshot 照合 → live 購読は supervisor が
// generation 単位で常設。
func (managedDriver) Resume(m session.Meta) (agents.ThreadHandle, error) {
	if m.Kind != session.KindOpencode {
		return nil, errors.New("opencode driver は opencode セッション専用です")
	}
	addr, gen, err := Serve().Ensure()
	if err != nil {
		return nil, err
	}
	ocSid := session.UUID(m.Dir, m.Name) // identity: the working copy, never the subdir
	cwd := m.CWD()                       // where the session runs (Dir, or a chosen subdir)
	handlesMu.Lock()
	h := handles[m.Name]
	if h == nil {
		h = &threadHandle{
			name:   m.Name,
			dir:    cwd,
			ocSid:  ocSid,
			events: make(chan agents.Event, 64),
		}
		handles[m.Name] = h
	}
	handlesMu.Unlock()

	h.resumeMu.Lock()
	defer h.resumeMu.Unlock()

	h.mu.Lock()
	if h.alive && h.gen == gen && h.ses != "" {
		h.mu.Unlock()
		return h, nil
	}
	// 起動時のモデル既定は meta から（動的変更は UpdateSettings が上書き、§9.4-3）。
	if h.settings.Model == "" {
		h.settings.Model = m.Model
	}
	if h.settings.Effort == "" {
		h.settings.Effort = m.Effort
	}
	if h.settings.Mode == "" {
		h.settings.Mode = m.Mode
	}
	ses := h.ses
	h.mu.Unlock()

	if ses == "" {
		ses = sids.Read(ocSid)
	}
	if ses != "" && !serveSessionExists(addr, ses, cwd) {
		ses = "" // pruned/imported-away conversation — start fresh
	}
	if ses == "" {
		if m.ForkFrom != "" {
			ses, err = serveForkSession(addr, m.ForkFrom, cwd, m.ForkAt)
		} else {
			ses, err = serveCreateSession(addr, cwd, session.Display(m))
		}
		if err != nil {
			return nil, err
		}
		// claude/codex と同じスロット単位の対応付け（read 層の activeSession が
		// これを最優先で引く — plugin 不在の managed では driver が書き手）。
		sids.Write(ocSid, ses)
	}

	h.mu.Lock()
	h.addr, h.gen, h.ses, h.alive = addr, gen, ses, true
	// daemon 死で pump が終了した後の残 queue を復活させる（§31 — opencode は
	// これが無いと恒久滞留）。
	if len(h.queue) > 0 && !h.pumping {
		h.pumping = true
		go h.pump()
	}
	h.mu.Unlock()

	h.reconcile()
	// exit recording の baseline（tui の startSessionTmux と同じ役割）: 前回の死亡
	// 記録をクリアし、以後の OOM 帰属をこのセッション期間に限定する。
	base, _ := status.OOMKillCount()
	status.PersistExit(m.Name, status.ExitInfo{OOMBase: base})
	return h, nil
}

// --- handle registry ---------------------------------------------------------

var handlesMu sync.Mutex
var handles = map[string]*threadHandle{}

// handleFor returns the live handle for a session name, or nil.
func handleFor(name string) *threadHandle {
	handlesMu.Lock()
	defer handlesMu.Unlock()
	return handles[name]
}

// liveHandles snapshots the currently-alive handles.
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

// DropHandle detaches a managed session from its runtime handle (stop/halt/
// archive): abort any running turn, clear the queue, forget the handle. The
// conversation stays in the SQLite store — a later Resume reattaches.
func DropHandle(name string) {
	handlesMu.Lock()
	h := handles[name]
	delete(handles, name)
	handlesMu.Unlock()
	if h == nil {
		return
	}
	h.mu.Lock()
	addr, ses, dir, running := h.addr, h.ses, h.dir, h.running
	h.alive = false
	h.queue = nil
	h.mu.Unlock()
	if running && ses != "" {
		abortSession(addr, ses, dir)
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

// AbortManaged interrupts every running managed turn — the managed counterpart of
// graceful shutdown's per-pane Ctrl-C（docs/27 §10.2-8）。ThreadHandle.Interrupt と
// 同じ経路なので、abort が landing すると turn goroutine が cancelled を刻み status
// ストアが idle に戻る（anySessionWorking の待ち条件が解ける）。
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

// ReconcileManaged re-attaches managed opencode sessions after an Agent boot,
// daemon restart or daemon death（§6）。対象は「停止扱いになっていない」managed メタ
// 全部（tui の tmux が Agent 再起動を生き延びるのと同じ体感にする）。失敗しても
// セッションは 停止中 として残り、ユーザーの 再開 クリック（/start）で再試行される。
func ReconcileManaged(reason string) {
	d := managedDriver{}
	for _, m := range session.ListMetas() {
		if m.Kind != session.KindOpencode || m.DriverKind() != session.DriverManaged || m.Archived {
			continue
		}
		if m.StoppedAt != "" && handleFor(m.Name) == nil {
			continue // deliberately stopped — resume only on user action
		}
		if _, err := d.Resume(m); err != nil {
			log.Printf("opencode managed: reconcile %s (%s): %v", m.Name, reason, err)
		}
	}
}

// reconcileAll is the supervisor-facing wrapper (serve.go の daemon 死・restart 後).
func reconcileAll(reason string) { ReconcileManaged(reason) }

// --- thread handle -----------------------------------------------------------

type threadHandle struct {
	name  string
	dir   string
	ocSid string

	mu sync.Mutex
	// resumeMu serializes Resume end-to-end (same §32 competition as codex: two
	// concurrent Resumes would create two native sessions and orphan one).
	resumeMu sync.Mutex

	addr     string
	gen      int
	ses      string
	alive    bool
	state    agents.TurnState
	running  bool // a turn goroutine is in flight (pump busy)
	pumping  bool
	queue    []agents.TurnInput
	settings agents.ThreadSettings
	inter    *agents.Interaction // pending question（waiting_interaction の中身）
	events   chan agents.Event
}

func (h *threadHandle) sessionID() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ses
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

func (h *threadHandle) currentState() agents.TurnState {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.state
}

// runtimeLost drops the handle to unknown（§6-1: 切断時の正直な状態）。The blocked
// turn call (if any) unblocks with a transport error and keeps unknown until
// reconcile resolves it.
func (h *threadHandle) runtimeLost() {
	h.mu.Lock()
	h.alive = false
	h.state = agents.TurnUnknown
	h.mu.Unlock()
	h.emit(agents.Event{Kind: "turn_state", TurnState: agents.TurnUnknown})
}

// reconcile は §6 の手順 3〜4: runtime の session 状態＋pending question を照合して
// turn 状態を確定する。busy=running / question=waiting_interaction / else completed。
func (h *threadHandle) reconcile() {
	h.mu.Lock()
	addr, ses, dir := h.addr, h.ses, h.dir
	h.mu.Unlock()
	st := agents.TurnCompleted
	if busy := serveSessionBusy(addr, ses, dir); busy {
		st = agents.TurnRunning
	}
	var inter *agents.Interaction
	for _, q := range serveListQuestions(addr, dir) {
		if q.SessionID == ses {
			inter = q.toInteraction()
			st = agents.TurnWaitingInteraction
			break
		}
	}
	h.mu.Lock()
	h.state = st
	h.inter = inter
	h.mu.Unlock()
}

// --- ThreadHandle interface ---------------------------------------------------

// Send starts a turn (turn/start 相当), queueing behind a running one.
func (h *threadHandle) Send(in agents.TurnInput) error { return h.accept(in) }

// Steer is an追撃 input to the running turn（§4 queued）。opencode v1 に mid-turn
// 注入の口が無いため（ファイルコメント）、意味論は「完走後に次 turn として投入」。
func (h *threadHandle) Steer(in agents.TurnInput) error { return h.accept(in) }

func (h *threadHandle) accept(in agents.TurnInput) error {
	if strings.TrimSpace(in.Prompt) == "" && len(in.Attachments) == 0 {
		return errors.New("empty prompt")
	}
	in.ClientMessageID = normalizeMsgID(in.ClientMessageID)
	h.mu.Lock()
	if !h.alive {
		h.mu.Unlock()
		return errors.New("runtime が停止しています（再開してください）")
	}
	if h.inter != nil {
		// 質問待ち中の自由文送信は誤答のもと（/input の question_pending ガードと
		// 同じ判断）— 構造化回答（Respond）へ誘導する。
		h.mu.Unlock()
		return errQuestionPending
	}
	if ledger.SeenOrRecord(h.name, in.ClientMessageID) {
		h.mu.Unlock()
		return nil // 再送 — 台帳（永続、プロセス跨ぎ）が冪等化（§4）
	}
	h.queue = append(h.queue, in)
	start := !h.pumping
	if start {
		h.pumping = true
	}
	if len(h.queue) > 0 && (h.running || len(h.queue) > 1) {
		h.state = agents.TurnQueued
	}
	h.mu.Unlock()
	if start {
		go h.pump()
	}
	return nil
}

// errQuestionPending is matched by the /turn handler to return the same
// question_pending wire error the tui route uses（P3 で kind 非依存の sentinel へ
// 一本化 — codex driver も同じ値を返す）。
var errQuestionPending error = agents.ErrQuestionPending

// ErrQuestionPending reports whether err is the "answer the question first" guard.
func ErrQuestionPending(err error) bool { return errors.Is(err, errQuestionPending) }

// pump processes the queue serially: wait for the runtime to be idle (a TUI-attached
// user may be running their own turn — serve 側の直列化に賭けず自前で待つ), run the
// blocking turn, repeat.
func (h *threadHandle) pump() {
	for {
		h.mu.Lock()
		if len(h.queue) == 0 || !h.alive {
			// accept と同じ lock 内で停止を確定する。空判定後〜defer の隙間を
			// 作ると、その間の入力が pumping=true を見て起動されず stranded になる。
			h.pumping = false
			h.mu.Unlock()
			return
		}
		in := h.queue[0]
		h.queue = h.queue[1:]
		addr, ses, dir := h.addr, h.ses, h.dir
		h.running = true
		h.mu.Unlock()

		// TUI 併用ガード: 他クライアントの turn が走っている間は待つ（最大 drain と
		// 同じ 60s、その後はそのまま投げる — serve は busy でも /message を直列に捌く）。
		waitIdle(addr, ses, dir, 60*time.Second)
		h.runTurn(in)

		h.mu.Lock()
		h.running = false
		h.mu.Unlock()
	}
}

// runTurn executes ONE blocking v1 /message turn and lands the terminal state.
// status ストア（hooks の代わり）も更新する — WireLive は db 由来の LiveState を
// 優先するが、フォールバックと anySessionWorking（graceful shutdown の待ち条件）が
// ここを読む。turn の終端で idle に戻さないと 進行中 に張り付く。
func (h *threadHandle) runTurn(in agents.TurnInput) {
	agents.MarkTurnStart(h.ocSid)
	// 終端の turn 状態で idle を刻む（＋完了なら docs/30 の報告を出す）。以下の return
	// 経路はすべて手前で setState 済みなので、defer 時点の state が turn の終端。
	// failure は失敗の理由（errors.go）— オペレーター報告とチャットブリッジ本文が
	// 「エラーで終わった」を言えるように終端まで運ぶ。
	failure := ""
	defer func() { agents.MarkTurnEndErr(h.ocSid, h.currentState(), failure) }()
	h.setState(agents.TurnStarting)
	h.mu.Lock()
	addr, ses := h.addr, h.ses
	st := h.settings
	h.mu.Unlock()

	// messageID は serve 採番に任せる（実測 1.17.18: turn ループは message id の
	// 辞書順に依存し、既存 id より小さいクライアント採番 id の turn は拾われず
	// /message が返らない — prompt_async が不動に見えた真因も同じ）。ClientMessageID
	// の冪等化は driver の台帳（accept()）だけで行う。
	body := map[string]any{"parts": buildParts(in)}
	if ag := agentForMode(st.Mode); ag != "" {
		body["agent"] = ag
	}
	if prov, model, ok := splitModel(st.Model); ok {
		body["model"] = map[string]string{"providerID": prov, "modelID": model}
	}
	if st.Effort != "" {
		body["variant"] = st.Effort
	}
	buf, err := json.Marshal(body)
	if err != nil {
		h.setState(agents.TurnFailed)
		return
	}
	h.setState(agents.TurnRunning)
	req, err := http.NewRequest("POST", dirQ(addr+"/session/"+url.PathEscape(ses)+"/message", h.dir), bytes.NewReader(buf))
	if err != nil {
		h.setState(agents.TurnFailed)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := turnClient.Do(req)
	if err != nil {
		// transport 断 = daemon 喪失か中断: 正直に unknown へ落とし §6 に委ねる。
		h.mu.Lock()
		interrupted := h.state == agents.TurnInterrupting
		h.mu.Unlock()
		if interrupted {
			h.setState(agents.TurnCancelled)
		} else {
			h.setState(agents.TurnUnknown)
		}
		return
	}
	defer res.Body.Close()
	// 200 でも失敗していることがある: opencode はプロバイダ側の失敗を HTTP ステータス
	// ではなく assistant message の error フィールドで返す（errors.go の実測）。status
	// だけ見ていた頃は残高切れ・認証エラーが「正常完了」として idle に戻り、転写にも
	// 何も残らなかった。
	turnErr, failed := decodeTurnError(res.Body)
	h.mu.Lock()
	interrupted := h.state == agents.TurnInterrupting
	h.inter = nil // turn が終わった＝質問はもう待っていない
	h.mu.Unlock()
	switch {
	case interrupted:
		h.setState(agents.TurnCancelled)
	case res.StatusCode >= 400:
		failure = fmt.Sprintf("[error] HTTP %d", res.StatusCode)
		log.Printf("opencode managed: turn failed name=%s status=%d", h.name, res.StatusCode)
		h.setState(agents.TurnFailed)
	case failed:
		failure = turnErr.summary()
		log.Printf("opencode managed: turn failed name=%s model=%s %s", h.name, st.Model, turnErr.summary())
		if turnErr.retryable() {
			h.setState(agents.TurnAborted)
		} else {
			h.setState(agents.TurnFailed)
		}
	default:
		h.setState(agents.TurnCompleted)
	}
}

// Interrupt aborts the running turn and clears the queued追撃（停止の意思表示は
// キューにも及ぶ — 完走後に古い追撃が勝手に走り出すのが最も驚く挙動）。
func (h *threadHandle) Interrupt() error {
	h.mu.Lock()
	addr, ses, dir := h.addr, h.ses, h.dir
	running := h.running
	h.queue = nil
	if running {
		h.state = agents.TurnInterrupting
	}
	h.mu.Unlock()
	if !running {
		return nil
	}
	h.emit(agents.Event{Kind: "turn_state", TurnState: agents.TurnInterrupting})
	return serveAbort(addr, ses, dir)
}

// UpdateSettings merges the dynamic thread settings（§9.4-3）。反映は次 turn の
// /message パラメータ（agent / model / variant）— v1 フローに thread 永続の設定
// 更新 RPC は無いので、driver が設定の持ち主になる。
func (h *threadHandle) UpdateSettings(s agents.ThreadSettings) error {
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

// Respond answers the pending Interaction（§5）: question 系のみ（3 者とも承認は
// bypass 運転）。answer は質問ごとの選択ラベル列へ変換して /question/{id}/reply、
// cancel/deny は /reject に落とす。
func (h *threadHandle) Respond(reply agents.InteractionReply) error {
	h.mu.Lock()
	inter := h.inter
	addr := h.addr
	h.mu.Unlock()
	if inter == nil || inter.ID != reply.ID {
		return fmt.Errorf("interaction %s は待機中ではありません", reply.ID)
	}
	switch reply.Decision {
	case agents.DecisionCancel, agents.DecisionDeny:
		if err := serveQuestionReject(addr, reply.ID, h.dir); err != nil {
			return err
		}
	case agents.DecisionAnswer, agents.DecisionAllow:
		answers, err := answersToLabels(inter.Questions, reply.Answers)
		if err != nil {
			return err
		}
		if err := serveQuestionReply(addr, reply.ID, h.dir, answers); err != nil {
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

// queuedPrompts surfaces the driver-held queue for the mirror's キュー済み badge
// （§10.2-10: ClientMessageID 台帳と turn 状態機械が正）。
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
// （readTranscript から呼ばれる）: pending question へ Interaction id を載せ（Console
// の /respond 経路の宛先）、driver 内キューを キュー済み へ合流する。tui セッション
// （handle 無し）には何もしない。
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
	// mode chip（§10.2-5）: db 由来の mode は「最後の turn の agent」で切替直後は
	// 古い。driver 設定（＝次 turn が使う値）があればそちらが真実。
	if modeSet != "" {
		td.Mode = modeSet
	}
}

// --- serve API helpers ---------------------------------------------------------

// dirQ appends the session's project directory to a serve URL. serve の
// session/question/status 面はプロジェクト（directory）にスコープされる（実測
// 1.17.18: serve の cwd と別ディレクトリのセッションは directory を付けないと
// /question・/session/status に載らない）。session 系 API は id 指定でも directory
// を付けて呼ぶのが安全。
func dirQ(base, dir string) string {
	if dir == "" {
		return base
	}
	sep := "?"
	if strings.ContainsRune(base, '?') {
		sep = "&"
	}
	return base + sep + "directory=" + url.QueryEscape(dir)
}

func serveSessionExists(addr, ses, dir string) bool {
	res, err := serveClient.Get(dirQ(addr+"/session/"+url.PathEscape(ses), dir))
	if err != nil {
		return false
	}
	defer res.Body.Close()
	return res.StatusCode == http.StatusOK
}

func serveSessionBusy(addr, ses, dir string) bool {
	res, err := serveClient.Get(dirQ(addr+"/session/status", dir))
	if err != nil {
		return false
	}
	defer res.Body.Close()
	var m map[string]struct {
		Type string `json:"type"`
	}
	if json.NewDecoder(res.Body).Decode(&m) != nil {
		return false
	}
	st, ok := m[ses] // idle なセッションは載らない（実測）
	return ok && st.Type != "idle"
}

func waitIdle(addr, ses, dir string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !serveSessionBusy(addr, ses, dir) {
			return
		}
		time.Sleep(2 * time.Second)
	}
}

func serveCreateSession(addr, dir, title string) (string, error) {
	body, _ := json.Marshal(map[string]string{"title": title})
	u := addr + "/session?directory=" + url.QueryEscape(dir)
	res, err := serveClient.Post(u, "application/json", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("opencode session の作成に失敗しました: %w", err)
	}
	defer res.Body.Close()
	var s struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(res.Body).Decode(&s); err != nil || s.ID == "" {
		return "", errors.New("opencode session の作成応答を解釈できません")
	}
	return s.ID, nil
}

// serveForkSession copies src into a NEW opencode session. at, when non-empty, is the
// message the copy stops BEFORE: opencode's fork loop breaks at the first message whose
// id sorts >= it, so the anchored turn and everything after it stay out of the fork
// (実測 1.18.14 — docs/55 §55.2). Empty at = the whole conversation, as before.
func serveForkSession(addr, src, dir, at string) (string, error) {
	body := "{}"
	if at != "" {
		b, err := json.Marshal(map[string]string{"messageID": at})
		if err != nil {
			return "", fmt.Errorf("opencode session の分岐点を組み立てられません: %w", err)
		}
		body = string(b)
	}
	res, err := serveClient.Post(dirQ(addr+"/session/"+url.PathEscape(src)+"/fork", dir), "application/json", strings.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("opencode session の分岐に失敗しました: %w", err)
	}
	defer res.Body.Close()
	// The anchor is client-supplied, so this is the one serve call that can be rejected
	// for what we asked rather than for how the daemon is doing. Say so instead of
	// letting it fall through to "応答を解釈できません".
	if res.StatusCode >= 400 {
		if at != "" {
			return "", fmt.Errorf("opencode が分岐点を受け付けませんでした (HTTP %d)", res.StatusCode)
		}
		return "", fmt.Errorf("opencode session の分岐に失敗しました (HTTP %d)", res.StatusCode)
	}
	var s struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(res.Body).Decode(&s); err != nil || s.ID == "" {
		return "", errors.New("opencode session の分岐応答を解釈できません")
	}
	return s.ID, nil
}

func serveAbort(addr, ses, dir string) error {
	res, err := serveClient.Post(dirQ(addr+"/session/"+url.PathEscape(ses)+"/abort", dir), "application/json", strings.NewReader("{}"))
	if err != nil {
		return err
	}
	res.Body.Close()
	return nil
}

// serveQuestion is the wire QuestionRequest（GET /question / question.asked event）。
type serveQuestion struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionID"`
	Questions []struct {
		Question string `json:"question"`
		Header   string `json:"header"`
		Options  []struct {
			Label       string `json:"label"`
			Description string `json:"description"`
		} `json:"options"`
		Multiple bool `json:"multiple"`
		Custom   bool `json:"custom"`
	} `json:"questions"`
}

func (q *serveQuestion) toInteraction() *agents.Interaction {
	inter := &agents.Interaction{ID: q.ID, Kind: "question"}
	for _, sq := range q.Questions {
		tq := transcript.Question{
			ID:          q.ID,
			Question:    sq.Question,
			Header:      sq.Header,
			MultiSelect: sq.Multiple,
		}
		for _, o := range sq.Options {
			tq.Options = append(tq.Options, transcript.Option{Label: o.Label, Description: o.Description})
		}
		inter.Questions = append(inter.Questions, tq)
	}
	return inter
}

func serveListQuestions(addr, dir string) []*serveQuestion {
	res, err := serveClient.Get(dirQ(addr+"/question", dir))
	if err != nil {
		return nil
	}
	defer res.Body.Close()
	var qs []*serveQuestion
	if json.NewDecoder(res.Body).Decode(&qs) != nil {
		return nil
	}
	return qs
}

func serveQuestionReply(addr, id, dir string, answers [][]string) error {
	body, _ := json.Marshal(map[string]any{"answers": answers})
	res, err := serveClient.Post(dirQ(addr+"/question/"+url.PathEscape(id)+"/reply", dir), "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return fmt.Errorf("質問への回答が拒否されました (HTTP %d)", res.StatusCode)
	}
	return nil
}

func serveQuestionReject(addr, id, dir string) error {
	res, err := serveClient.Post(dirQ(addr+"/question/"+url.PathEscape(id)+"/reject", dir), "application/json", strings.NewReader("{}"))
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return fmt.Errorf("質問の却下が拒否されました (HTTP %d)", res.StatusCode)
	}
	return nil
}

// --- pure mapping helpers（unit-tested） ---------------------------------------

// normalizeMsgID makes a ClientMessageID acceptable as opencode's v1 messageID
// (^msg 必須・実測)。空なら AF 採番（§4: 採番者は AF）。
func normalizeMsgID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		b := make([]byte, 12)
		_, _ = rand.Read(b)
		return "msg_af" + hex.EncodeToString(b)
	}
	if strings.HasPrefix(id, "msg") {
		return id
	}
	return "msg_af_" + id
}

// agentForMode maps the ThreadSettings.Mode vocabulary（TranscriptData.Mode と同語彙）
// onto opencode's agent name.
func agentForMode(mode string) string {
	switch mode {
	case "plan":
		return "plan"
	case "normal":
		return "build"
	}
	return ""
}

// splitModel parses the launch-model string "provider/model" (buildProgram が
// --model へ渡すのと同じ形) into a v1 model ref. 「/」を含まない値は provider が
// 特定できないので送らない（serve 既定に任せる）。
func splitModel(s string) (provider, model string, ok bool) {
	s = strings.TrimSpace(s)
	i := strings.IndexByte(s, '/')
	if i <= 0 || i == len(s)-1 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}

// buildParts assembles the v1 message parts: prompt text + one file part per
// attachment（§10.2-3: managed は tmux 貼付でなく API 添付）。
func buildParts(in agents.TurnInput) []map[string]any {
	var parts []map[string]any
	if t := strings.TrimSpace(in.Prompt); t != "" {
		parts = append(parts, map[string]any{"type": "text", "text": t})
	}
	for _, p := range in.Attachments {
		if strings.TrimSpace(p) == "" {
			continue
		}
		mt := mime.TypeByExtension(filepath.Ext(p))
		if mt == "" {
			mt = "application/octet-stream"
		}
		parts = append(parts, map[string]any{
			"type": "file", "mime": mt,
			"filename": filepath.Base(p),
			"url":      "file://" + p,
		})
	}
	return parts
}

// answersToLabels converts the structured reply（質問ごとの Text / Options index）
// into opencode's answers（質問ごとの選択ラベル列）。index は該当質問の選択肢へ、
// Text はそのまま 1 ラベルとして渡す（custom 回答）。
func answersToLabels(questions []transcript.Question, answers []agents.InteractionAnswer) ([][]string, error) {
	if len(answers) != len(questions) {
		return nil, fmt.Errorf("回答数が質問数と一致しません (%d != %d)", len(answers), len(questions))
	}
	out := make([][]string, len(answers))
	for i, a := range answers {
		var labels []string
		for _, oi := range a.Options {
			if oi < 0 || oi >= len(questions[i].Options) {
				return nil, fmt.Errorf("質問 %d の選択肢 %d は範囲外です", i+1, oi)
			}
			labels = append(labels, questions[i].Options[oi].Label)
		}
		if len(labels) == 0 && strings.TrimSpace(a.Text) != "" {
			labels = []string{strings.TrimSpace(a.Text)}
		}
		if len(labels) == 0 {
			return nil, fmt.Errorf("質問 %d に回答がありません", i+1)
		}
		out[i] = labels
	}
	return out, nil
}

// --- SSE dispatch ---------------------------------------------------------------

// handleServeEvent routes one SSE event to the owning handle（supervisor の
// monitorEvents から）。question 系が本命（§5）; permission.asked は bypass 運転の
// 保険として自動 allow（3 者とも承認は自動化済み、§5 — serve 既定は素通しを実測
// 済みだが、ユーザー config が ask を足しても managed セッションが黙って固まらない
// ようにする）。
func handleServeEvent(data []byte) {
	var ev struct {
		// /global/event は {"payload": {type, properties}} に包む（実測）。素の
		// /event 形（{type, properties} 直置き）も受ける — テスト・将来の互換用。
		Payload    json.RawMessage `json:"payload"`
		Type       string          `json:"type"`
		Properties json.RawMessage `json:"properties"`
	}
	if json.Unmarshal(data, &ev) != nil {
		return
	}
	if ev.Type == "" && len(ev.Payload) > 0 {
		inner := ev
		if json.Unmarshal(ev.Payload, &inner) != nil {
			return
		}
		ev.Type, ev.Properties = inner.Type, inner.Properties
	}
	switch ev.Type {
	case "question.asked":
		var q serveQuestion
		if json.Unmarshal(ev.Properties, &q) != nil || q.SessionID == "" {
			return
		}
		if h := handleBySes(q.SessionID); h != nil {
			inter := q.toInteraction()
			h.mu.Lock()
			h.inter = inter
			h.state = agents.TurnWaitingInteraction
			h.mu.Unlock()
			h.emit(agents.Event{Kind: "interaction", TurnState: agents.TurnWaitingInteraction, Interaction: inter})
		}
	case "question.replied", "question.rejected":
		var p struct {
			SessionID string `json:"sessionID"`
			RequestID string `json:"requestID"`
		}
		if json.Unmarshal(ev.Properties, &p) != nil {
			return
		}
		if h := handleBySes(p.SessionID); h != nil {
			// TUI アタッチ側で答えられたケースも含めて解消する（併用、§2）。
			h.mu.Lock()
			if h.inter != nil && h.inter.ID == p.RequestID {
				h.inter = nil
				if h.running {
					h.state = agents.TurnRunning
				}
			}
			h.mu.Unlock()
			h.emit(agents.Event{Kind: "turn_state", TurnState: agents.TurnRunning})
		}
	case "permission.asked":
		var p struct {
			ID        string `json:"id"`
			SessionID string `json:"sessionID"`
		}
		if json.Unmarshal(ev.Properties, &p) != nil || p.ID == "" {
			return
		}
		if h := handleBySes(p.SessionID); h != nil {
			h.mu.Lock()
			addr, dir := h.addr, h.dir
			h.mu.Unlock()
			body := strings.NewReader(`{"reply":"always"}`)
			if res, err := serveClient.Post(dirQ(addr+"/permission/"+url.PathEscape(p.ID)+"/reply", dir), "application/json", body); err == nil {
				res.Body.Close()
				log.Printf("opencode managed: auto-allowed permission %s (session %s)", p.ID, h.name)
			}
		}
	}
}

// handleBySes finds the live handle owning an opencode session id.
func handleBySes(ses string) *threadHandle {
	if ses == "" {
		return nil
	}
	for _, h := range liveHandles() {
		if h.sessionID() == ses {
			return h
		}
	}
	return nil
}
