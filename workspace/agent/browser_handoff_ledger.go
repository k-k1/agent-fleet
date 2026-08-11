package main

// ブラウザ attach ハンドオフの配送台帳（docs/53-chromium-attach-view.md「完了通知」節）。
//
// request_browser_action で人間に操作を依頼したセッションへ、Console の完了/
// キャンセルボタンが押された結果を実際に届ける薄い機構。それまでは
// SetHandoffResult がメモリ上の状態を書き換えるだけで、依頼元セッションへは
// 何も伝わらなかった（get_browser_action_result を自発的に呼び直さない限り
// 結果を知る術がない）。
//
// browserAttachment 自体は Agent 再起動をまたいで残らない（メモリ上のみ）ので、
// 「再起動を挟んでも通知を失わない」が意味を持つのは
// resolveBrowserHandoff（結果確定）～deliverBrowserHandoff（配送完了）の短い窓
// だけ — その窓を跨いだクラッシュだけをこの台帳が救う。指示台帳
// （chat_report_ledger.go）の busy/idle settle 検出とは無関係な別物なので、
// あちらには載せない: session_peer.go と同じ理由で、指示台帳に載せると
// リコンサイラが「利用者の新指示」と誤認し早期 settle / 早期消費を起こす
// （ADR 0041 決定4と同型の事故）。配送そのものは既存の /sessions/{name}/input
// （agentSendToSession、停止中セッションの再起動込み）を再利用する。

import (
	"encoding/json"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/fstore"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// browserHandoffRow is one handoff round for one attachment. Rows are appended
// per session file, mirroring instr-ledger's append-only discipline (never
// overwrite another row when adding one). A row with Result == "" is still
// waiting for a human; once resolved it lives only until delivery succeeds —
// there is no compensation/reopen path here to audit a closed row against
// (unlike instr-ledger), so a delivered row is simply removed rather than kept
// in a "delivered" state.
type browserHandoffRow struct {
	ID           string `json:"id"`
	AttachmentID string `json:"attachmentId"`
	Message      string `json:"message,omitempty"`
	Result       string `json:"result,omitempty"` // "" until resolveBrowserHandoff
	ResolvedAt   string `json:"resolvedAt,omitempty"`
	CreatedAt    string `json:"createdAt"`
}

type browserHandoffLedger struct {
	Rows []browserHandoffRow `json:"rows"`
}

var browserHandoffLedgers = fstore.JSON[browserHandoffLedger](paths.AgentConfigDir, "browser-handoff-ledger", ".json")

// 台帳の read-modify-write はセッション単位で直列化する（chat_report_ledger.go の
// lockInstr と同じ理由: 書き手はこのプロセス内の複数ハンドラだけ）。
var (
	browserHandoffLocksMu sync.Mutex
	browserHandoffLocks   = map[string]*sync.Mutex{}
)

func lockBrowserHandoff(name string) func() {
	browserHandoffLocksMu.Lock()
	mu, ok := browserHandoffLocks[name]
	if !ok {
		mu = &sync.Mutex{}
		browserHandoffLocks[name] = mu
	}
	browserHandoffLocksMu.Unlock()
	mu.Lock()
	return mu.Unlock
}

func newBrowserHandoffID() string {
	return "bh-" + strings.ReplaceAll(randUUID(), "-", "")[:10]
}

// recordBrowserHandoffRequested persists a new pending row when a handoff is
// requested with a session to notify. A no-op when sessionName is empty/invalid
// (a handoff started without request_browser_action's session context, or a
// bare API call) — the handoff still works exactly as before, it just has
// nobody to deliver a result to.
func recordBrowserHandoffRequested(sessionName, attachmentID, message string) {
	if !session.ValidName(sessionName) || attachmentID == "" {
		return
	}
	row := browserHandoffRow{
		ID: newBrowserHandoffID(), AttachmentID: attachmentID, Message: truncateBrowserText(message, browserAttachmentMaxHandoff),
		CreatedAt: time.Now().Format(time.RFC3339),
	}
	unlock := lockBrowserHandoff(sessionName)
	defer unlock()
	l, _ := browserHandoffLedgers.Read(sessionName)
	l.Rows = append(l.Rows, row)
	_ = browserHandoffLedgers.Write(sessionName, l)
}

// resolveBrowserHandoff marks THIS attachment's still-open row (Result=="")
// resolved and returns it for delivery. ok=false when there is no row to
// resolve — no session was recorded for this handoff round, or (edge case) two
// results raced and a previous call already claimed it.
func resolveBrowserHandoff(sessionName, attachmentID, result string) (browserHandoffRow, bool) {
	if !session.ValidName(sessionName) || attachmentID == "" {
		return browserHandoffRow{}, false
	}
	unlock := lockBrowserHandoff(sessionName)
	defer unlock()
	l, _ := browserHandoffLedgers.Read(sessionName)
	for i := range l.Rows {
		if l.Rows[i].AttachmentID != attachmentID || l.Rows[i].Result != "" {
			continue
		}
		l.Rows[i].Result = result
		l.Rows[i].ResolvedAt = time.Now().Format(time.RFC3339)
		_ = browserHandoffLedgers.Write(sessionName, l)
		return l.Rows[i], true
	}
	return browserHandoffRow{}, false
}

// markBrowserHandoffDelivered removes a row after a SUCCESSFUL delivery
// (deliver-then-consume, same discipline as markInstrReported).
func markBrowserHandoffDelivered(sessionName, rowID string) {
	unlock := lockBrowserHandoff(sessionName)
	defer unlock()
	l, ok := browserHandoffLedgers.Read(sessionName)
	if !ok {
		return
	}
	var kept []browserHandoffRow
	for _, r := range l.Rows {
		if r.ID != rowID {
			kept = append(kept, r)
		}
	}
	if len(kept) == 0 {
		browserHandoffLedgers.Remove(sessionName)
		return
	}
	l.Rows = kept
	_ = browserHandoffLedgers.Write(sessionName, l)
}

// browserHandoffDeliveryText is what actually lands in the session's input —
// enough for the model to act without parsing prose, and an explicit pointer to
// the authoritative structured tool (get_browser_action_result) rather than
// asking it to trust this text as the result itself.
func browserHandoffDeliveryText(row browserHandoffRow) string {
	verdict := "完了(completed)"
	if row.Result == "cancelled" {
		verdict = "キャンセル(cancelled)"
	}
	message := row.Message
	if message == "" {
		message = "(メッセージなし)"
	}
	return "[agent-fleet] ブラウザ操作の依頼「" + message + "」に、利用者が「" + verdict +
		"」で応答しました。get_browser_action_result(attachment_id=" + row.AttachmentID + ") で結果を確認してください。"
}

// deliverBrowserHandoff injects the result into the session's live conversation
// via the same /sessions/{name}/input path peer messaging and scheduled
// resends use — it resumes a stopped session and blocks (internally, up to
// ~45s) until the turn provably started, so callers MUST run this off the
// request goroutine (SetHandoffResult does). Delivery failure just leaves the
// row for the next Agent start's sweep to retry; it does not retry itself,
// since a failure here is virtually always something that will not resolve
// within the same process lifetime (session gone, worktree dead).
func deliverBrowserHandoff(sessionName string, row browserHandoffRow) {
	body, _ := json.Marshal(map[string]any{"prompt": browserHandoffDeliveryText(row), "confirm": true})
	if _, _, err := agentSendToSession(sessionName, body); err != nil {
		log.Printf("browser handoff: deliver to %s failed, left for next Agent start's sweep: %v", sessionName, err)
		return
	}
	markBrowserHandoffDelivered(sessionName, row.ID)
}

// sweepUndeliveredBrowserHandoffs retries every resolved-but-undelivered row
// across all sessions — the recovery path for a crash between
// resolveBrowserHandoff persisting the result and deliverBrowserHandoff's
// agentSendToSession actually landing. Called once at Agent startup
// (main.go), the same place chat_report_ledger.go's own migration runs; a
// periodic tick is unnecessary because the only way a row survives here
// during a NORMAL run is that crash window — everything else delivers
// synchronously inside SetHandoffResult.
func sweepUndeliveredBrowserHandoffs() {
	ents, err := os.ReadDir(browserHandoffLedgers.Dir())
	if err != nil {
		return
	}
	n := 0
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		if !session.ValidName(name) {
			continue
		}
		l, ok := browserHandoffLedgers.Read(name)
		if !ok {
			continue
		}
		for _, row := range l.Rows {
			if row.Result == "" {
				continue // still waiting for a human, nothing to retry
			}
			n++
			go deliverBrowserHandoff(name, row)
		}
	}
	if n > 0 {
		log.Printf("browser handoff: retrying %d undelivered result(s) left over from a previous Agent run", n)
	}
}
