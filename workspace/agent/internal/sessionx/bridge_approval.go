package sessionx

// Chat-bridge P3 (docs/log/37): the approval gate for destructive fleet-operator actions —
// the package-main half of internal/bridge/approval.go. Two cooperating processes share
// the filesystem here:
//
//   - The daemon runs runOperatorTurn (bridge_operator.go) for a Discord-driven operator
//     turn and ARMS a marker (bridge-operator-turn.json) around it. Console operator chat
//     (handleChatSend) never arms it, so those turns are never gated (a human is watching).
//   - The mcp-stdio SUBPROCESS spawned inside that turn runs the operator's tool calls. A
//     destructive handler calls BridgeApprovalGate before acting; when the marker says the
//     turn is unattended, it posts an approve/reject button (bridge.PostOperatorApproval)
//     and blocks polling a handshake record (bridge-approvals/<id>.json) until decided.
//   - A button click arrives at the daemon's Gateway → routeInteraction → answerInteraction
//     (kind "op") → bridgeApprovalDecision writes the decision into the same record, which
//     the subprocess's poll loop then observes.
//
// The marker + record cross the process boundary as small files (the same fstore pattern
// as bridge-answers / bridge-threads / bridge-operator). Only Discord-driven turns gate;
// the gate is fail-closed (no channel to approve through ⇒ the action does not run).

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/bridge"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/fstore"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// --- origin marker: only a Discord-driven operator turn arms the gate --------------------

// operatorTurnMarker records that a Discord-driven operator turn is in progress for Conv,
// until ExpiresAt (a self-clearing TTL in case the turn's process dies without disarming).
type operatorTurnMarker struct {
	Conv      string `json:"conv"`
	ExpiresAt int64  `json:"expiresAt"`
}

// operatorTurnStore holds one active-turn marker PER conversation (key = conv). The daemon
// writes it around runOperatorTurn; the mcp-stdio subprocess reads it to learn its turn is
// unattended and must gate destructive actions. A single shared key would let two unattended
// turns (Discord/Slack/scheduled assistant, different convs) overwrite each other's marker —
// the earlier turn's defer would then erase the later turn's marker and its destructive
// tools would run ungated (fail-open).
var operatorTurnStore = fstore.JSON[operatorTurnMarker](paths.AgentStateDir, "bridge-operator-turn", ".json")

// operatorTurnKey derives the per-conv marker filename key (conv IDs are slug-safe; any
// other byte is normalized defensively so the key is always a plain filename).
func operatorTurnKey(conv string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		}
		return '-'
	}, conv)
}

// OperatorTurnTimeout bounds a Discord-driven operator turn. It is deliberately longer than
// chatTimeout (Console chat) because such a turn may pause on a human approval — the window
// the bound user has to tap a phone button (bridgeApprovalTimeout) must fit inside it.
var OperatorTurnTimeout = 6 * time.Minute

func ArmOperatorTurn(conv string) {
	if conv == "" {
		return
	}
	_ = operatorTurnStore.Write(operatorTurnKey(conv), operatorTurnMarker{
		Conv: conv, ExpiresAt: chatx.NowMs() + OperatorTurnTimeout.Milliseconds(),
	})
}

// DisarmOperatorTurn removes only THIS conv's marker, so a finishing turn can never
// erase the marker of another conversation's still-running unattended turn.
func DisarmOperatorTurn(conv string) {
	if conv == "" {
		return
	}
	operatorTurnStore.Remove(operatorTurnKey(conv))
}

// operatorTurnArmed reports whether the current turn is THE Discord-driven operator turn for
// conv (own marker present, matching conv, unexpired). Read by the mcp-stdio subprocess to
// decide whether a destructive tool must gate.
func operatorTurnArmed(conv string) bool {
	if conv == "" {
		return false
	}
	m, ok := operatorTurnStore.Read(operatorTurnKey(conv))
	return ok && m.Conv == conv && chatx.NowMs() < m.ExpiresAt
}

// --- approval handshake (subprocess writes+polls the record; daemon writes the decision) ---

// bridgeApprovalRec is the cross-process handshake record for one pending destructive action.
type bridgeApprovalRec struct {
	ID        string `json:"id"`
	Op        string `json:"op"`      // localized action label (for the daemon-side audit only)
	Summary   string `json:"summary"` // localized target detail
	Conv      string `json:"conv"`
	Decision  string `json:"decision"` // "" (pending) | "approve" | "reject"
	CreatedAt int64  `json:"createdAt"`
	// Deadline (unix ms) is when the waiter gives up. It lets the daemon refuse a late
	// click even when the waiter could not take the lock and left the record in place.
	Deadline int64 `json:"deadline,omitempty"`
}

var bridgeApprovals = fstore.JSON[bridgeApprovalRec](paths.AgentStateDir, "bridge-approvals", ".json")

// Tunables (vars so tests can shrink them). bridgeApprovalTimeout must stay under
// OperatorTurnTimeout so the wait can never outlive the turn that spawned it.
var (
	bridgeApprovalTimeout = 4 * time.Minute
	bridgeApprovalPoll    = 2 * time.Second
	bridgeApprovalMaxAge  = 1 * time.Hour // orphan-record sweep horizon

	bridgeApprovalLockWait = 2 * time.Second // bound on waiting for the handshake lock
)

// lockApprovals is a var so tests can inject an acquisition failure. Neither the flock nor
// the in-process lock is reentrant, so a test seam must not take it from inside the section.
var lockApprovals = lockApprovalsFile

// approvalNowMs is the clock the handshake compares against Deadline; a var so a test can
// stand in for "the daemon stalled across the deadline" without sleeping.
var approvalNowMs = chatx.NowMs

// bridgeApprovalClaims holds one claim file per approval: the single arbiter of who
// resolved it. Whoever creates <id>.claim first wins, and the content names the winner
// (claimTaken: the waiter consumed a decision; claimExpired: the waiter gave up;
// claimConfirmed: the daemon claimed the approval BEFORE it wrote the decision, so a decision
// is visible only once the claim exists). Creation is an atomic
// hard link, so it holds with no flock — the lock can be unavailable, or unable to exclude a counterpart in
// another process (the non-unix lock is process-local), and the arbiter still serializes the
// two processes.
var bridgeApprovalClaims = fstore.Strings(paths.AgentStateDir, "bridge-approvals", ".claim")

const (
	claimTaken     = "taken"
	claimExpired   = "expired"
	claimConfirmed = "confirmed"
)

// bridgeApprovalConfirmWait bounds how long a waiter that lost its expiry claim to the
// daemon's "confirmed" waits for the decision the daemon is about to write (the claim comes
// first, the write microseconds later). A var so tests can shrink it.
var bridgeApprovalConfirmWait = 2 * time.Second

// claimApprovalFn is the arbiter every caller goes through; a var so a test can make one
// side's claim fail (a filesystem without hard links, a transient I/O error).
var claimApprovalFn = claimApproval

// claimHonored reports whether a claim's winner means the waiter may act on the decision:
// its own "taken", or the daemon's "confirmed" (the user was already told the outcome).
func claimHonored(winner string) bool { return winner == claimTaken || winner == claimConfirmed }

// claimApprovalRetry rides out a transient I/O failure of the arbiter (not a lost race,
// which is a result): the waiter's claim sits on the most common path, an on-time approval.
func claimApprovalRetry(id, who string) (string, error) {
	var winner string
	var err error
	for i := 0; i < 3; i++ {
		if winner, err = claimApprovalFn(id, who); err == nil {
			return winner, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return "", err
}

// readClaimRetry reads the claim file, retrying while it is absent (the daemon may still be
// retrying its own claim) within the same bound as claimApprovalRetry.
func readClaimRetry(id string) (string, bool) {
	for i := 0; i < 3; i++ {
		if b, ok := bridgeApprovalClaims.Read(id); ok {
			return b, true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return "", false
}

// claimExpiry is the waiter's deadline claim. It reports whether the daemon had already
// confirmed, in which case the waiter must act on the decision (awaitConfirmedDecision).
// An unusable arbiter falls back to reading the claim; unknown means timeout.
func claimExpiry(id string) (confirmed bool) {
	w, err := claimApprovalRetry(id, claimExpired)
	if err != nil {
		w, _ = readClaimRetry(id)
	}
	return w == claimConfirmed
}

// awaitConfirmedDecision waits (bounded) for the decision record the confirming daemon
// writes right after its claim, then applies it. The daemon only tells the user "Approved"
// after writing; if the write never lands the user was told nothing and this is a timeout.
func awaitConfirmedDecision(id string) error {
	until := time.Now().Add(bridgeApprovalConfirmWait)
	for {
		if cur, ok := bridgeApprovals.Read(id); ok {
			if decided, err := approvalOutcome(id, cur); decided {
				return err
			}
		}
		if time.Now().After(until) {
			bridgeApprovals.Remove(id)
			return errApprovalTimeout
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// claimApproval tries to resolve id as who and returns the winner's name. A returned error
// means the outcome is unknown; callers fail closed.
func claimApproval(id, who string) (winner string, err error) {
	staging := filepath.Join(bridgeApprovalClaims.Dir(), fstore.StagingSubdir)
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(staging, id+".*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	_, werr := tmp.WriteString(who)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return "", werr
	}
	path := bridgeApprovalClaims.Path(id)
	// Link, not rename: it fails when the claim exists, and the content is complete the
	// moment the name appears (an O_EXCL create would expose an empty file to the loser).
	lerr := os.Link(tmp.Name(), path)
	if lerr == nil {
		return who, nil
	}
	if !errors.Is(lerr, fs.ErrExist) {
		return "", lerr
	}
	b, rerr := os.ReadFile(path)
	if rerr != nil || len(b) == 0 {
		return "", errors.New("approval claim unreadable")
	}
	return string(b), nil
}

var (
	errApprovalRejected      = errors.New("この操作は却下されました（実行していません）")
	errApprovalTimeout       = errors.New("承認がタイムアウトしました（実行していません）。必要ならもう一度依頼してください")
	errApprovalUnconfirmed   = errors.New("承認は届きましたが確定できなかったため、この操作を実行していません。必要ならもう一度依頼してください")
	errApprovalUndeliverable = errors.New("承認リクエストを Discord に送信できなかったため、この操作を中止しました")
)

// BridgeApprovalGate is called by a destructive write handler in the mcp-stdio subprocess
// BEFORE it acts. When this turn is a Discord-driven operator turn (operatorTurnArmed), it
// posts an approve/reject button into the operator thread and blocks until the bound user
// decides or it times out; otherwise it is an immediate no-op (Console-driven operator chat
// and every non-operator conversation proceed exactly as before). The returned error's
// message is surfaced to the LLM verbatim (mcpToolErr) so the operator reports the outcome.
func BridgeApprovalGate(op, summary string) error {
	if !operatorTurnArmed(mcpConvID()) {
		return nil // attended (Console) or non-operator — no gate; unattended only
	}
	sweepStaleApprovals()
	id := chatx.RandUUID()
	if err := bridgeApprovals.Write(id, bridgeApprovalRec{
		ID: id, Op: op, Summary: summary, Conv: mcpConvID(), CreatedAt: chatx.NowMs(),
		Deadline: chatx.NowMs() + bridgeApprovalTimeout.Milliseconds(),
	}); err != nil {
		return errApprovalUndeliverable
	}
	if err := bridge.PostOperatorApproval(mcpConvID(), approvalPrompt(op, summary), id); err != nil {
		bridgeApprovals.Remove(id)
		return errApprovalUndeliverable // fail closed — no channel to approve through
	}
	return waitApprovalDecision(id)
}

// bridgeApprovalBetweenReadAndClaim is a test seam: it runs inside bridgeApprovalDecision's
// critical section, after the pending and deadline checks and before the daemon claims, so a
// test can land the waiter's deadline step exactly there (the daemon then loses the claim).
// It must not take lockApprovals (not reentrant).
var bridgeApprovalBetweenReadAndClaim func(id string)

// bridgeApprovalAfterWrite is a test seam: it runs after the daemon has claimed and written
// the decision, before it replies. The gap between the claim and the write is not seamed;
// tests reach it by acting inside the claim function (claimApprovalFn).
// It must not take lockApprovals (not reentrant).
var bridgeApprovalAfterWrite func(id string)

// waitApprovalDecision polls the handshake record until the daemon writes a decision or the
// window elapses, then removes the record. Read errors (a partial file during the daemon's
// write) are treated as "still pending" and retried on the next tick.
func waitApprovalDecision(id string) error {
	deadline := time.Now().Add(bridgeApprovalTimeout)
	for {
		time.Sleep(bridgeApprovalPoll)
		if cur, ok := bridgeApprovals.Read(id); ok {
			if decided, err := approvalOutcome(id, cur); decided {
				return err
			}
		}
		if time.Now().After(deadline) {
			return expireApproval(id)
		}
	}
}

// approvalOutcome maps a decided record to the gate's result and removes the record. The
// waiter first claims the decision (claimTaken): a daemon that wrote it after the deadline
// withdraws it through the same arbiter, so the gate acts on a decision only when the user
// was told it would. Losing the claim, or being unable to make it, is a timeout — the
// action does not run.
func approvalOutcome(id string, cur bridgeApprovalRec) (decided bool, err error) {
	if cur.Decision != "approve" && cur.Decision != "reject" {
		return false, nil
	}
	winner, cerr := claimApprovalRetry(id, claimTaken)
	if cerr != nil {
		// Not a lost race: the waiter's own claim failed. The daemon claims before it
		// writes the decision, so it has confirmed (and told the user "Approved") or is
		// about to; reading the claim needs no link. Ride out the daemon's own retries.
		if b, ok := readClaimRetry(id); ok {
			winner, cerr = b, nil // confirmed: act; expired: a plain timeout
		}
	}
	bridgeApprovals.Remove(id)
	if cerr != nil {
		// Unknown outcome. The daemon either did not confirm (it then told the user "not
		// confirmed") or its confirmation cannot be read: report "arrived, not confirmed".
		return true, errApprovalUnconfirmed
	}
	if !claimHonored(winner) {
		return true, errApprovalTimeout
	}
	if cur.Decision == "approve" {
		return true, nil
	}
	return true, errApprovalRejected
}

// expireApproval is the waiter's deadline step. Under the lock shared with
// bridgeApprovalDecision it re-reads the record: a decision that landed first is honored
// (the user was told "running it"), otherwise the record is removed so a later click finds
// nothing and reports "expired". Without the lock, a click between the daemon's read and
// write re-created the removed record and the user was told "Approved" for an action that
// never ran.
func expireApproval(id string) error {
	unlock, err := lockApprovals()
	if err != nil {
		// Never remove without the lock: that is the race. Leave the record (its Deadline
		// makes the daemon refuse any later click; the sweep removes it) and take a last
		// look so a decision already written is not discarded.
		if cur, ok := bridgeApprovals.Read(id); ok {
			if decided, derr := approvalOutcome(id, cur); decided {
				return derr
			}
		}
		// The expiry claim decides against a daemon that is mid-click: it either wins
		// (the daemon then reports "expired") or loses to "confirmed" (act on that).
		if claimExpiry(id) {
			return awaitConfirmedDecision(id)
		}
		return errApprovalTimeout
	}
	defer unlock()
	if cur, ok := bridgeApprovals.Read(id); ok {
		if decided, err := approvalOutcome(id, cur); decided {
			return err
		}
	}
	// A lock that excludes the daemon (unix flock) means it cannot be mid-click here; the
	// process-local non-unix lock does not exclude another process, so the claim decides.
	if claimExpiry(id) {
		return awaitConfirmedDecision(id)
	}
	bridgeApprovals.Remove(id)
	return errApprovalTimeout
}

// bridgeApprovalDecision records the bound user's approve/reject on a pending approval
// (from routeInteraction via answerInteraction, kind "op") and returns the localized outcome
// line the receiver shows on the now-button-less message. The subprocess's gate loop is
// polling the same record and proceeds/aborts once it observes the decision. Stale or
// already-decided clicks are reported without changing the record. The read-check-write runs
// under the lock shared with expireApproval, so a record the waiter has removed is never
// re-created and a click after the deadline is reported as expired.
//
// The lock alone cannot cover a waiter that failed to take it, or a counterpart in another
// process that a process-local lock cannot exclude, so the claim file arbitrates, and the
// daemon claims BEFORE it writes the decision:
// "confirmed" wins only against a waiter that has not claimed "expired" yet, and a waiter
// sees the decision only once the claim exists. The decision is therefore never visible
// without the daemon having claimed it, so a daemon whose claim fails (bounded 3x50 ms)
// publishes nothing, tells the user "not confirmed", and no waiter can act on it. A waiter
// that lost to "confirmed" waits (bounded) for the write that follows. No wait on the waiter
// is involved, so the request path stays bounded.
func bridgeApprovalDecision(id, choice string, en bool) (string, error) {
	unlock, err := lockApprovals()
	if err != nil {
		return "", err
	}
	defer unlock()
	rec, ok := bridgeApprovals.Read(id)
	if !ok {
		return Fb(en, "この承認は期限切れ、または処理済みです", "This approval has expired or was already handled"), nil
	}
	if rec.Decision != "" {
		return Fb(en, "この承認はすでに処理済みです", "This approval was already handled"), nil
	}
	expired := Fb(en, "この承認は期限切れ、または処理済みです", "This approval has expired or was already handled")
	if rec.Deadline > 0 && approvalNowMs() >= rec.Deadline {
		return expired, nil // the waiter has given up (or is about to)
	}
	if h := bridgeApprovalBetweenReadAndClaim; h != nil {
		h(id)
	}
	winner, cerr := claimApprovalRetry(id, claimConfirmed)
	if cerr != nil {
		bridgeApprovals.Remove(id)
		return Fb(en, "⚠ 承認を受け付けましたが確定できませんでした（実行しません）。もう一度依頼してください",
			"⚠ Received, but it could not be confirmed (not running it). Ask again if needed"), errors.New("approval outcome not confirmed")
	}
	if winner != claimConfirmed {
		bridgeApprovals.Remove(id) // the waiter gave up first
		return expired, nil
	}
	rec.Decision = choice
	if err := bridgeApprovals.Write(id, rec); err != nil {
		return "", err
	}
	if h := bridgeApprovalAfterWrite; h != nil {
		h(id)
	}
	if choice == "approve" {
		return Fb(en, "✓ 承認しました（実行します）", "✓ Approved (running it)"), nil
	}
	return Fb(en, "✓ 却下しました（実行しません）", "✓ Rejected (not running it)"), nil
}

// sweepStaleApprovals removes handshake records left behind by a subprocess that died
// mid-wait (its poll loop never reached the cleanup). Best-effort.
func sweepStaleApprovals() {
	cutoff := chatx.NowMs() - bridgeApprovalMaxAge.Milliseconds()
	entries, _ := os.ReadDir(bridgeApprovals.Dir()) // absent dir: nothing to sweep here
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		if rec, ok := bridgeApprovals.Read(id); ok && rec.CreatedAt < cutoff {
			bridgeApprovals.Remove(id)
		}
	}
	// Claim files carry no timestamp; the file's mtime is when the approval was resolved.
	// They must outlive any gate still deciding (a waiter's wait is minutes, the claim
	// wait seconds), which the horizon far exceeds.
	claims, _ := os.ReadDir(bridgeApprovalClaims.Dir())
	for _, e := range claims {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".claim") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".claim")
		if mt, ok := bridgeApprovalClaims.ModTime(id); ok && mt.UnixMilli() < cutoff {
			bridgeApprovalClaims.Remove(id)
		}
	}
}

// --- the destructive-action descriptions surfaced on the approval button -----------------

// approvalPrompt is the message body shown above the approve/reject buttons (bridge scrubs
// it before posting). Locale follows the connection's notification language.
func approvalPrompt(op, summary string) string {
	en := BridgeAnswerEN()
	line := "**" + op + "**"
	if summary != "" {
		line += " — " + summary
	}
	return Fb(en, "🔒 承認が必要な操作", "🔒 Approval required") + "\n" + line + "\n" +
		Fb(en, "実行してよろしいですか？", "Run this?")
}

// ApprovalLabel maps a gated tool to its localized action label (the bold line on the
// button prompt). Locale follows the connection's notification language.
func ApprovalLabel(op string) string {
	en := BridgeAnswerEN()
	switch op {
	case "delete_session":
		return Fb(en, "セッションを削除", "Delete session")
	case "delete_worktree":
		return Fb(en, "worktree を削除", "Delete worktree")
	case "delete_branch":
		return Fb(en, "ブランチを削除", "Delete branch")
	case "purge_cleanup_archive":
		return Fb(en, "アーカイブを完全削除", "Purge archive")
	case "create_session_shell":
		return Fb(en, "shell セッションを作成", "Create shell session")
	case "send_to_session_shell":
		return Fb(en, "shell へコマンド送信", "Send command to shell")
	}
	return op
}

// ShellCreateTarget / ShellSendTarget build the summary detail for the two shell-execution
// gates (the raw command / prompt is the dangerous part, so it is echoed — truncated, and
// scrubbed by the bridge before posting).
func ShellCreateTarget(dir, prompt string) string {
	s := ""
	if dir != "" {
		s = dir
	}
	if p := strings.TrimSpace(prompt); p != "" {
		if s != "" {
			s += " ← "
		}
		s += clampRunes(p, 200)
	}
	return s
}

func ShellSendTarget(name, prompt string) string {
	return name + " ← " + clampRunes(strings.TrimSpace(prompt), 200)
}

// SessionIsShell reports whether the named session runs the raw shell kind — send_to_session
// to it executes arbitrary commands, so it gates like create_session kind=shell.
func SessionIsShell(name string) bool {
	if !session.ValidName(name) {
		return false
	}
	m, ok := session.ReadMeta(name)
	return ok && m.Kind == session.KindShell
}

// clampRunes truncates s to at most n runes, appending an ellipsis when it cut anything.
func clampRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
