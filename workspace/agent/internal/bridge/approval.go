package bridge

// Chat-bridge P3 (docs/37): approval gate for the fleet operator's destructive actions.
// When the built-in operator conversation is driven from Discord (unattended — no Console
// human watching), a destructive tool (delete_session / delete_worktree / delete_branch /
// purge_cleanup_archive / a shell create-or-send) posts an approve/reject button into the
// operator thread and blocks until the bound user decides — the SAME Message Components +
// INTERACTION_CREATE round-trip as P2b (interact.go), just targeting an operator action
// rather than a session's pending prompt.
//
// This file is the SEND half (post the buttons). The decode lives in ParseCustomID
// (interact.go, kind "op"); the arm/wait/apply half lives in package main
// (bridge_approval.go), wired through the existing ReceiverDeps.Answer callback (a click
// with kind "op" is applied by answerInteraction → bridgeApprovalDecision).

import (
	"errors"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// errNoOperatorApprovalTarget: there is no operator thread / connection to request
// approval in. The gate treats this as fail-closed (the destructive action must not run
// unattended when there is no channel to approve it through).
var errNoOperatorApprovalTarget = errors.New("no operator thread to request approval in")

// approvalRow builds the 承認/却下 action row for an approval request id, encoding the
// decision into each button's custom_id (af|op|approve|<id> / af|op|reject|<id>).
func approvalRow(id string, en bool) []any {
	return decisionRow(
		"op", id, "approve", label(en, "承認", "Approve"), buttonSuccess,
		"reject", label(en, "却下", "Reject"), buttonDanger)
}

// PostOperatorApproval posts an approve/reject prompt for a destructive operator action
// into the operator thread. content is scrubbed of secrets before posting (the summary can
// echo a shell command / prompt). Returns errNoOperatorApprovalTarget when no operator
// thread or Discord connection exists — the gate fails closed on that. The button language
// follows the connection's notification language (契約: same locale as notifications).
func PostOperatorApproval(content, id string) error {
	ref, ok := OperatorState()
	if !ok || ref.Thread == "" {
		return errNoOperatorApprovalTarget
	}
	s, err := secrets.Load()
	if err != nil || s.Discord == nil || s.Discord.Token == "" {
		return errNoOperatorApprovalTarget
	}
	en := s.Discord.Lang == "en"
	om := outMsg{content: ScrubSecrets(content), components: approvalRow(id, en)}
	_, err = discordPost(s.Discord.Token, ref.Thread, om)
	return err
}
