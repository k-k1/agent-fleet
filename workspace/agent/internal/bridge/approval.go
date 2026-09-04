package bridge

// Chat-bridge P3 (docs/log/37): approval gate for the fleet operator's destructive actions.
// When the built-in operator conversation is driven from chat (unattended — no Console
// human watching), a destructive tool posts an approve/reject button into the operator
// thread and blocks until the bound user decides — the SAME buttons + interaction
// round-trip as P2b (interact.go / slack_interact.go), just targeting an operator action
// rather than a session's pending prompt.
//
// This file is the SEND half (post the buttons). The decode lives in ParseCustomID
// (interact.go, kind "op"); the arm/wait/apply half lives in package main
// (bridge_approval.go), wired through the existing ReceiverDeps.Answer callback (a click
// with kind "op" is applied by answerInteraction → bridgeApprovalDecision).
//
// Provider-scoped (docs/log/37 Slack follow-up): the approval posts to whichever provider's
// operator store owns conv — the same conv→provider mapping PostOperatorReply uses.

import (
	"errors"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// errNoOperatorApprovalTarget: there is no operator thread / connection to request
// approval in. The gate treats this as fail-closed (the destructive action must not run
// unattended when there is no channel to approve it through).
var errNoOperatorApprovalTarget = errors.New("no operator thread to request approval in")

// approvalRow builds the Approve/Reject Discord action row for an approval request id, encoding
// the decision into each button's custom_id (af|op|approve|<id> / af|op|reject|<id>).
func approvalRow(id string, en bool) []any {
	return decisionRow(
		"op", id, "approve", label(en, "承認", "Approve"), buttonSuccess,
		"reject", label(en, "却下", "Reject"), buttonDanger)
}

// PostOperatorApproval posts an approve/reject prompt for a destructive operator action
// into the operator thread that owns conv. content is scrubbed of secrets before posting
// (the summary can echo a shell command / prompt). Returns errNoOperatorApprovalTarget
// when no operator thread / connection owns conv — the gate fails closed on that. The
// button language follows the connection's notification language.
func PostOperatorApproval(conv, content, id string) error {
	if ref, ok := discordOperator.state(); ok && ref.Conv == conv && ref.Thread != "" {
		s, err := secrets.Load()
		if err != nil || s.Discord == nil || s.Discord.Token == "" {
			return errNoOperatorApprovalTarget
		}
		en := s.Discord.Lang == "en"
		om := outMsg{content: ScrubSecrets(content), components: approvalRow(id, en)}
		_, err = discordPost(s.Discord.Token, ref.Thread, om)
		return err
	}
	if ref, ok := slackOperator.state(); ok && ref.Conv == conv && ref.Thread != "" {
		s, err := secrets.Load()
		if err != nil || s.Slack == nil || s.Slack.BotToken == "" {
			return errNoOperatorApprovalTarget
		}
		en := s.Slack.Lang == "en"
		_, err = slackPostMessage(s.Slack.BotToken, ref.Channel, ref.Thread,
			ScrubSecrets(content), slackApprovalBlocks(id, en))
		return err
	}
	return errNoOperatorApprovalTarget
}
