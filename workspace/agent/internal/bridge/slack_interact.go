package bridge

// Slack Block Kit button rendering (docs/log/37 Slack 追随 = P2b/P3 parity): the Slack twin of
// interact.go's Message-Components rendering. It emits the SAME custom_id strings (interact.go
// customID / ParseCustomID are provider-neutral), carried in each button's action_id + value,
// so the answer path (bridge_answer.go) is shared. Interactions arrive over the same Socket
// Mode WSS as inbound replies (slack_socket.go) — no public endpoint, mirroring Discord's
// INTERACTION_CREATE-over-Gateway.

import (
	"encoding/json"
	"strconv"
)

// slackButtonsPerActions is Slack's cap on elements in one actions block. A question with
// more options than this falls back to plain text (answered from the Console), like Discord.
const slackButtonsPerActions = 25

func slackButton(cid, text, style string) map[string]any {
	b := map[string]any{
		"type":      "button",
		"text":      map[string]any{"type": "plain_text", "text": truncate(text, 74), "emoji": true},
		"action_id": cid,
		"value":     cid,
	}
	if style != "" {
		b["style"] = style // "primary" (green) | "danger" (red); "" = default
	}
	return b
}

func slackSection(text string) map[string]any {
	return map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": text}}
}

func slackActions(elements []any) map[string]any {
	return map[string]any{"type": "actions", "elements": elements}
}

// interactive reports whether this Slack connection can round-trip button clicks: Receive
// (the Socket Mode WSS delivering the click) + channel mode (a thread to answer within).
func (sp *slackProvider) interactive() bool {
	return sp.creds.Receive && sp.creds.ChannelID != ""
}

// buttonMessages renders the interactive follow-up messages for an attention event as Block
// Kit, or nil when the kind isn't buttonable (or a form can't render as single-select
// buttons — the caller then keeps the plain-text notification).
func (sp *slackProvider) buttonMessages(m Message) []slackMsg {
	en := sp.creds.Lang == "en"
	switch m.Kind {
	case "permission-request":
		return []slackMsg{{blocks: slackDecisionBlocks("p", m.SessionName,
			"allow", label(en, "許可", "Allow"), "primary",
			"deny", label(en, "拒否", "Deny"), "danger")}}
	case "plan-approval":
		return []slackMsg{{blocks: slackDecisionBlocks("pl", m.SessionName,
			"approve", label(en, "承認", "Approve"), "primary",
			"reject", label(en, "却下", "Reject"), "danger")}}
	case "question":
		return slackQuestionMessages(m.SessionName, m.Questions, en)
	}
	return nil
}

// slackDecisionBlocks builds a single actions block of two decision buttons (allow/deny,
// approve/reject). kind is the custom_id tag ("p" / "pl" / "op").
func slackDecisionBlocks(kind, sid, a, aLabel, aStyle, b, bLabel, bStyle string) []any {
	return []any{slackActions([]any{
		slackButton(customID(kind, a, sid), aLabel, aStyle),
		slackButton(customID(kind, b, sid), bLabel, bStyle),
	})}
}

// slackApprovalBlocks builds the 承認/却下 blocks for a P3 operator destructive-action
// approval id (af|op|approve|<id> / af|op|reject|<id>).
func slackApprovalBlocks(id string, en bool) []any {
	return slackDecisionBlocks("op", id,
		"approve", label(en, "承認", "Approve"), "primary",
		"reject", label(en, "却下", "Reject"), "danger")
}

// slackQuestionMessages renders one message per question (a heading section + an option-button
// actions block). Returns nil (→ plain-text fallback) if ANY question is multi-select or
// over the button budget — those need the Console, and a partial render would mislead.
func slackQuestionMessages(sid string, raw json.RawMessage, en bool) []slackMsg {
	var qs []qkQuestion
	if err := json.Unmarshal(raw, &qs); err != nil || len(qs) == 0 {
		return nil
	}
	for _, q := range qs {
		if q.MultiSelect || len(q.Options) == 0 || len(q.Options) > slackButtonsPerActions {
			return nil
		}
	}
	fp := QuestionFingerprint(raw)
	var msgs []slackMsg
	for qi, q := range qs {
		heading := questionHeading(q, qi, len(qs), en)
		var elements []any
		for oi, opt := range q.Options {
			cid := customID("q", sid, strconv.Itoa(qi), strconv.Itoa(oi), fp)
			elements = append(elements, slackButton(cid, opt.Label, ""))
		}
		msgs = append(msgs, slackMsg{text: heading, blocks: []any{slackSection(heading), slackActions(elements)}})
	}
	return msgs
}
