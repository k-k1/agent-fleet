package main

// Chat-bridge P3 (docs/log/37): the approval gate for destructive fleet-operator actions —
// the package-main half of internal/bridge/approval.go. Two cooperating processes share
// the filesystem here:
//
//   - The daemon runs runOperatorTurn (bridge_operator.go) for a Discord-driven operator
//     turn and ARMS a marker (bridge-operator-turn.json) around it. Console operator chat
//     (handleChatSend) never arms it, so those turns are never gated (a human is watching).
//   - The mcp-stdio SUBPROCESS spawned inside that turn runs the operator's tool calls. A
//     destructive handler calls bridgeApprovalGate before acting; when the marker says the
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
	"os"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/bridge"
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
var operatorTurnStore = fstore.JSON[operatorTurnMarker](paths.AgentConfigDir, "bridge-operator-turn", ".json")

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

// operatorTurnTimeout bounds a Discord-driven operator turn. It is deliberately longer than
// chatTimeout (Console chat) because such a turn may pause on a human approval — the window
// the bound user has to tap a phone button (bridgeApprovalTimeout) must fit inside it.
var operatorTurnTimeout = 6 * time.Minute

func armOperatorTurn(conv string) {
	if conv == "" {
		return
	}
	_ = operatorTurnStore.Write(operatorTurnKey(conv), operatorTurnMarker{
		Conv: conv, ExpiresAt: nowMs() + operatorTurnTimeout.Milliseconds(),
	})
}

// disarmOperatorTurn removes only THIS conv's marker, so a finishing turn can never
// erase the marker of another conversation's still-running unattended turn.
func disarmOperatorTurn(conv string) {
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
	return ok && m.Conv == conv && nowMs() < m.ExpiresAt
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
}

var bridgeApprovals = fstore.JSON[bridgeApprovalRec](paths.AgentConfigDir, "bridge-approvals", ".json")

// Tunables (vars so tests can shrink them). bridgeApprovalTimeout must stay under
// operatorTurnTimeout so the wait can never outlive the turn that spawned it.
var (
	bridgeApprovalTimeout = 4 * time.Minute
	bridgeApprovalPoll    = 2 * time.Second
	bridgeApprovalMaxAge  = 1 * time.Hour // orphan-record sweep horizon
)

var (
	errApprovalRejected      = errors.New("この操作は却下されました（実行していません）")
	errApprovalTimeout       = errors.New("承認がタイムアウトしました（実行していません）。必要ならもう一度依頼してください")
	errApprovalUndeliverable = errors.New("承認リクエストを Discord に送信できなかったため、この操作を中止しました")
)

// bridgeApprovalGate is called by a destructive write handler in the mcp-stdio subprocess
// BEFORE it acts. When this turn is a Discord-driven operator turn (operatorTurnArmed), it
// posts an approve/reject button into the operator thread and blocks until the bound user
// decides or it times out; otherwise it is an immediate no-op (Console-driven operator chat
// and every non-operator conversation proceed exactly as before). The returned error's
// message is surfaced to the LLM verbatim (mcpToolErr) so the operator reports the outcome.
func bridgeApprovalGate(op, summary string) error {
	if !operatorTurnArmed(mcpConvID) {
		return nil // attended (Console) or non-operator — no gate; unattended only
	}
	sweepStaleApprovals()
	id := randUUID()
	if err := bridgeApprovals.Write(id, bridgeApprovalRec{
		ID: id, Op: op, Summary: summary, Conv: mcpConvID, CreatedAt: nowMs(),
	}); err != nil {
		return errApprovalUndeliverable
	}
	if err := bridge.PostOperatorApproval(mcpConvID, approvalPrompt(op, summary), id); err != nil {
		bridgeApprovals.Remove(id)
		return errApprovalUndeliverable // fail closed — no channel to approve through
	}
	return waitApprovalDecision(id)
}

// waitApprovalDecision polls the handshake record until the daemon writes a decision or the
// window elapses, then removes the record. Read errors (a partial file during the daemon's
// write) are treated as "still pending" and retried on the next tick.
func waitApprovalDecision(id string) error {
	deadline := time.Now().Add(bridgeApprovalTimeout)
	for {
		time.Sleep(bridgeApprovalPoll)
		if cur, ok := bridgeApprovals.Read(id); ok {
			switch cur.Decision {
			case "approve":
				bridgeApprovals.Remove(id)
				return nil
			case "reject":
				bridgeApprovals.Remove(id)
				return errApprovalRejected
			}
		}
		if time.Now().After(deadline) {
			bridgeApprovals.Remove(id)
			return errApprovalTimeout
		}
	}
}

// bridgeApprovalDecision records the bound user's approve/reject on a pending approval
// (from routeInteraction via answerInteraction, kind "op") and returns the localized outcome
// line the receiver shows on the now-button-less message. The subprocess's gate loop is
// polling the same record and proceeds/aborts once it observes the decision. Stale or
// already-decided clicks are reported without changing the record.
func bridgeApprovalDecision(id, choice string, en bool) (string, error) {
	rec, ok := bridgeApprovals.Read(id)
	if !ok {
		return fb(en, "この承認は期限切れ、または処理済みです", "This approval has expired or was already handled"), nil
	}
	if rec.Decision != "" {
		return fb(en, "この承認はすでに処理済みです", "This approval was already handled"), nil
	}
	rec.Decision = choice
	if err := bridgeApprovals.Write(id, rec); err != nil {
		return "", err
	}
	if choice == "approve" {
		return fb(en, "✓ 承認しました（実行します）", "✓ Approved (running it)"), nil
	}
	return fb(en, "✓ 却下しました（実行しません）", "✓ Rejected (not running it)"), nil
}

// sweepStaleApprovals removes handshake records left behind by a subprocess that died
// mid-wait (its poll loop never reached the cleanup). Best-effort.
func sweepStaleApprovals() {
	entries, err := os.ReadDir(bridgeApprovals.Dir())
	if err != nil {
		return
	}
	cutoff := nowMs() - bridgeApprovalMaxAge.Milliseconds()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		if rec, ok := bridgeApprovals.Read(id); ok && rec.CreatedAt < cutoff {
			bridgeApprovals.Remove(id)
		}
	}
}

// --- the destructive-action descriptions surfaced on the approval button -----------------

// approvalPrompt is the message body shown above the approve/reject buttons (bridge scrubs
// it before posting). Locale follows the connection's notification language.
func approvalPrompt(op, summary string) string {
	en := bridgeAnswerEN()
	line := "**" + op + "**"
	if summary != "" {
		line += " — " + summary
	}
	return fb(en, "🔒 承認が必要な操作", "🔒 Approval required") + "\n" + line + "\n" +
		fb(en, "実行してよろしいですか？", "Run this?")
}

// approvalLabel maps a gated tool to its localized action label (the bold line on the
// button prompt). Locale follows the connection's notification language.
func approvalLabel(op string) string {
	en := bridgeAnswerEN()
	switch op {
	case "delete_session":
		return fb(en, "セッションを削除", "Delete session")
	case "delete_worktree":
		return fb(en, "worktree を削除", "Delete worktree")
	case "delete_branch":
		return fb(en, "ブランチを削除", "Delete branch")
	case "purge_cleanup_archive":
		return fb(en, "アーカイブを完全削除", "Purge archive")
	case "create_session_shell":
		return fb(en, "shell セッションを作成", "Create shell session")
	case "send_to_session_shell":
		return fb(en, "shell へコマンド送信", "Send command to shell")
	}
	return op
}

// shellCreateTarget / shellSendTarget build the summary detail for the two shell-execution
// gates (the raw command / prompt is the dangerous part, so it is echoed — truncated, and
// scrubbed by the bridge before posting).
func shellCreateTarget(dir, prompt string) string {
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

func shellSendTarget(name, prompt string) string {
	return name + " ← " + clampRunes(strings.TrimSpace(prompt), 200)
}

// sessionIsShell reports whether the named session runs the raw shell kind — send_to_session
// to it executes arbitrary commands, so it gates like create_session kind=shell.
func sessionIsShell(name string) bool {
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
