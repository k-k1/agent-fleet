package browserx

// The delivery ledger for a browser attach handoff (docs/log/53-chromium-attach-view.md
// §completion notification).
//
// A thin mechanism that actually delivers the outcome of the Console's complete / cancel
// button to the session that asked a human to operate the browser through
// request_browser_action. Without it, SetHandoffResult only rewrites in-memory state and
// nothing reaches the requesting session, which then has no way to learn the result unless it
// calls get_browser_action_result again of its own accord.
//
// A browserAttachment does not itself survive an Agent restart (it is memory-only), so "the
// notification is not lost across a restart" only means anything for the short window between
// ResolveBrowserHandoff (the result is decided) and DeliverBrowserHandoff (delivery
// completes); a crash inside that window is all this ledger rescues. It is a different thing
// from the instruction ledger (chat_report_ledger.go) and its busy/idle settle detection, and
// must not be filed there: for the same reason as session_peer.go, a row in the instruction
// ledger is mistaken by the reconciler for a new instruction from the user and causes an
// early settle / early consume (the same accident as ADR 0041 decision 4). Delivery itself
// reuses the existing /sessions/{name}/input (agentSendToSession, including restarting a
// stopped session).

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

// BrowserHandoffRow is one handoff round for one attachment. Rows are appended
// per session file, mirroring instr-ledger's append-only discipline (never
// overwrite another row when adding one). A row with Result == "" is still
// waiting for a human; once resolved it lives only until delivery succeeds —
// there is no compensation/reopen path here to audit a closed row against
// (unlike instr-ledger), so a delivered row is simply removed rather than kept
// in a "delivered" state.
type BrowserHandoffRow struct {
	ID           string `json:"id"`
	AttachmentID string `json:"attachmentId"`
	Message      string `json:"message,omitempty"`
	Result       string `json:"result,omitempty"` // "" until ResolveBrowserHandoff
	ResolvedAt   string `json:"resolvedAt,omitempty"`
	CreatedAt    string `json:"createdAt"`
}

type browserHandoffLedger struct {
	Rows []BrowserHandoffRow `json:"rows"`
}

var BrowserHandoffLedgers = fstore.JSON[browserHandoffLedger](paths.AgentConfigDir, "browser-handoff-ledger", ".json")

// The ledger's read-modify-write is serialized per session (the same reason as lockInstr in
// chat_report_ledger.go: the only writers are several handlers inside this process).
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

// RecordBrowserHandoffRequested persists a new pending row when a handoff is
// requested with a session to notify. A no-op when sessionName is empty/invalid
// (a handoff started without request_browser_action's session context, or a
// bare API call) — the handoff still works exactly as before, it just has
// nobody to deliver a result to.
func RecordBrowserHandoffRequested(sessionName, attachmentID, message string) {
	if !session.ValidName(sessionName) || attachmentID == "" {
		return
	}
	row := BrowserHandoffRow{
		ID: newBrowserHandoffID(), AttachmentID: attachmentID, Message: truncateBrowserText(message, browserAttachmentMaxHandoff),
		CreatedAt: time.Now().Format(time.RFC3339),
	}
	unlock := lockBrowserHandoff(sessionName)
	defer unlock()
	l, _ := BrowserHandoffLedgers.Read(sessionName)
	l.Rows = append(l.Rows, row)
	_ = BrowserHandoffLedgers.Write(sessionName, l)
}

// ResolveBrowserHandoff marks THIS attachment's still-open row (Result=="")
// resolved and returns it for delivery. ok=false when there is no row to
// resolve — no session was recorded for this handoff round, or (edge case) two
// results raced and a previous call already claimed it.
func ResolveBrowserHandoff(sessionName, attachmentID, result string) (BrowserHandoffRow, bool) {
	if !session.ValidName(sessionName) || attachmentID == "" {
		return BrowserHandoffRow{}, false
	}
	unlock := lockBrowserHandoff(sessionName)
	defer unlock()
	l, _ := BrowserHandoffLedgers.Read(sessionName)
	for i := range l.Rows {
		if l.Rows[i].AttachmentID != attachmentID || l.Rows[i].Result != "" {
			continue
		}
		l.Rows[i].Result = result
		l.Rows[i].ResolvedAt = time.Now().Format(time.RFC3339)
		_ = BrowserHandoffLedgers.Write(sessionName, l)
		return l.Rows[i], true
	}
	return BrowserHandoffRow{}, false
}

// MarkBrowserHandoffDelivered removes a row after a SUCCESSFUL delivery
// (deliver-then-consume, same discipline as markInstrReported).
func MarkBrowserHandoffDelivered(sessionName, rowID string) {
	unlock := lockBrowserHandoff(sessionName)
	defer unlock()
	l, ok := BrowserHandoffLedgers.Read(sessionName)
	if !ok {
		return
	}
	var kept []BrowserHandoffRow
	for _, r := range l.Rows {
		if r.ID != rowID {
			kept = append(kept, r)
		}
	}
	if len(kept) == 0 {
		BrowserHandoffLedgers.Remove(sessionName)
		return
	}
	l.Rows = kept
	_ = BrowserHandoffLedgers.Write(sessionName, l)
}

// BrowserHandoffDeliveryText is what actually lands in the session's input —
// enough for the model to act without parsing prose, and an explicit pointer to
// the authoritative structured tool (get_browser_action_result) rather than
// asking it to trust this text as the result itself.
func BrowserHandoffDeliveryText(row BrowserHandoffRow) string {
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

// DeliverBrowserHandoff injects the result into the session's live conversation
// via the same /sessions/{name}/input path peer messaging and scheduled
// resends use — it resumes a stopped session and blocks (internally, up to
// ~45s) until the turn provably started, so callers MUST run this off the
// request goroutine (SetHandoffResult does). Delivery failure just leaves the
// row for the next Agent start's sweep to retry; it does not retry itself,
// since a failure here is virtually always something that will not resolve
// within the same process lifetime (session gone, worktree dead).
func DeliverBrowserHandoff(sessionName string, row BrowserHandoffRow) {
	body, _ := json.Marshal(map[string]any{"prompt": BrowserHandoffDeliveryText(row), "confirm": true})
	if _, _, err := agentSendToSession(sessionName, body); err != nil {
		log.Printf("browser handoff: deliver to %s failed, left for next Agent start's sweep: %v", sessionName, err)
		return
	}
	MarkBrowserHandoffDelivered(sessionName, row.ID)
}

// SweepUndeliveredBrowserHandoffs retries every resolved-but-undelivered row
// across all sessions — the recovery path for a crash between
// ResolveBrowserHandoff persisting the result and DeliverBrowserHandoff's
// agentSendToSession actually landing. Called once at Agent startup
// (main.go), the same place chat_report_ledger.go's own migration runs; a
// periodic tick is unnecessary because the only way a row survives here
// during a NORMAL run is that crash window — everything else delivers
// synchronously inside SetHandoffResult.
func SweepUndeliveredBrowserHandoffs() {
	ents, err := os.ReadDir(BrowserHandoffLedgers.Dir())
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
		l, ok := BrowserHandoffLedgers.Read(name)
		if !ok {
			continue
		}
		for _, row := range l.Rows {
			if row.Result == "" {
				continue // still waiting for a human, nothing to retry
			}
			n++
			go DeliverBrowserHandoff(name, row)
		}
	}
	if n > 0 {
		log.Printf("browser handoff: retrying %d undelivered result(s) left over from a previous Agent run", n)
	}
}
