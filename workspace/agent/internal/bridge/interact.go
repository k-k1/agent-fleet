package bridge

// P2b (docs/log/37): button-drive a session's pending AskUserQuestion / permission /
// plan approval from Discord Message Components, so the fleet can be answered
// without the Console (composes with P2a reply-inject and the full-text bridge).
// This file is the SEND half — turning a pending payload into action-row buttons
// and encoding each button's answer into its custom_id. The receive half
// (INTERACTION_CREATE → answer application) lives in receiver.go + package main.
//
// Interactions arrive over the SAME Gateway as P2a (no public endpoint needed),
// so buttons ride the Receive opt-in and channel mode (a bound user + a thread to
// route back through). Multi-select and over-budget forms are left as plain text
// (no buttons) — answered from the Console — rather than mis-rendered.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
)

// Discord component/interaction constants.
const (
	compActionRow   = 1
	compButton      = 2
	buttonPrimary   = 1
	buttonSuccess   = 3 // green — allow / approve
	buttonDanger    = 4 // red — deny / reject
	buttonLabelCap  = 80
	buttonsPerRow   = 5
	maxRows         = 5
	maxOptionButton = buttonsPerRow * maxRows // 25 — Discord's per-message ceiling
)

// custom_id scheme: pipe-joined, "af" tagged, well under Discord's 100-char cap.
//
//	af|q|<sid>|<qi>|<oi>|<fp>   AskUserQuestion option (qi/oi = question/option index)
//	af|p|<allow|deny>|<sid>     tool-permission decision
//	af|pl|<approve|reject>|<sid>  plan approval decision
//	af|op|<approve|reject>|<id>   operator destructive-action approval (P3, approval.go)
//
// The fingerprint (fp) on question buttons is a short digest of the questions
// payload; the answer path rejects a click whose fp no longer matches the current
// pending question (the form changed / was already answered).
const customIDPrefix = "af"

// qkQuestion is the slice of claude's tool_input.questions we render (labels +
// whether multiple picks are allowed). Mirrors console questionKeys.ts QKQuestion
// and internal/transcript.Question, kept local so bridge stays self-contained.
type qkQuestion struct {
	Question    string `json:"question"`
	Header      string `json:"header"`
	MultiSelect bool   `json:"multiSelect"`
	Options     []struct {
		Label string `json:"label"`
	} `json:"options"`
}

// outMsg is one message to post: text content and/or an interactive component set.
// Discord allows either alone, so a buttons-only message (no content) is valid.
type outMsg struct {
	content    string
	components []any // Discord action rows; nil = plain message
}

// QuestionFingerprint is a short stable digest of the raw questions payload, used
// to detect a stale button click (the pending question changed since we posted).
func QuestionFingerprint(raw json.RawMessage) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])[:6]
}

// interactive reports whether this connection can round-trip button clicks: it
// needs the Receive gateway up (INTERACTION_CREATE) and channel mode (a bound
// user + thread to answer within). DM mode is out of scope (P2a parity).
func (d *discordProvider) interactive() bool {
	return d.creds.Receive && d.creds.ChannelID != ""
}

// buttonMessages renders the interactive follow-up messages for an attention
// event, or nil when the kind isn't buttonable (or the form can't be rendered as
// single-select buttons — the caller then keeps the plain-text notification).
func (d *discordProvider) buttonMessages(m Message) []outMsg {
	en := d.creds.Lang == "en"
	switch m.Kind {
	case "permission-request":
		return []outMsg{{components: decisionRow(
			"p", m.SessionName, "allow", label(en, "許可", "Allow"), buttonSuccess,
			"deny", label(en, "拒否", "Deny"), buttonDanger)}}
	case "plan-approval":
		return []outMsg{{components: decisionRow(
			"pl", m.SessionName, "approve", label(en, "承認", "Approve"), buttonSuccess,
			"reject", label(en, "却下", "Reject"), buttonDanger)}}
	case "question":
		return questionMessages(m.SessionName, m.Questions, en)
	}
	return nil
}

// decisionRow builds a single action row of two decision buttons (allow/deny,
// approve/reject). kind is the custom_id tag ("p" / "pl").
func decisionRow(kind, sid, a, aLabel string, aStyle int, b, bLabel string, bStyle int) []any {
	return []any{map[string]any{
		"type": compActionRow,
		"components": []any{
			button(customID(kind, a, sid), aLabel, aStyle),
			button(customID(kind, b, sid), bLabel, bStyle),
		},
	}}
}

// questionMessages renders one message per question (uniform for single- and
// multi-question forms), each carrying that question's option buttons. Returns
// nil (→ plain-text fallback) if ANY question is multi-select or exceeds the
// button budget — those need the Console, and a partial rendering would mislead.
func questionMessages(sid string, raw json.RawMessage, en bool) []outMsg {
	var qs []qkQuestion
	if err := json.Unmarshal(raw, &qs); err != nil || len(qs) == 0 {
		return nil
	}
	for _, q := range qs {
		if q.MultiSelect || len(q.Options) == 0 || len(q.Options) > maxOptionButton {
			return nil // out of P2b scope — keep the plain notification
		}
	}
	fp := QuestionFingerprint(raw)
	var msgs []outMsg
	for qi, q := range qs {
		var rows []any
		var row []any
		for oi, opt := range q.Options {
			if len(row) == buttonsPerRow {
				rows = append(rows, map[string]any{"type": compActionRow, "components": row})
				row = nil
			}
			cid := customID("q", sid, strconv.Itoa(qi), strconv.Itoa(oi), fp)
			row = append(row, button(cid, truncate(opt.Label, buttonLabelCap), buttonPrimary))
		}
		if len(row) > 0 {
			rows = append(rows, map[string]any{"type": compActionRow, "components": row})
		}
		msgs = append(msgs, outMsg{content: questionHeading(q, qi, len(qs), en), components: rows})
	}
	return msgs
}

// questionHeading is the message content above a question's buttons — the header
// (if any) and the question text, prefixed with an index for multi-question forms
// so the buttons that follow are unambiguous.
func questionHeading(q qkQuestion, qi, total int, en bool) string {
	var b strings.Builder
	if total > 1 {
		b.WriteString("[" + strconv.Itoa(qi+1) + "/" + strconv.Itoa(total) + "] ")
	}
	if q.Header != "" {
		b.WriteString(q.Header)
		if q.Question != "" {
			b.WriteString(label(en, "：", " — "))
		}
	}
	b.WriteString(q.Question)
	return truncate(strings.TrimSpace(b.String()), 1900)
}

func button(customID, label string, style int) map[string]any {
	return map[string]any{"type": compButton, "style": style, "label": label, "custom_id": customID}
}

// customID joins the parts with the scheme separator.
func customID(parts ...string) string {
	return customIDPrefix + "|" + strings.Join(parts, "|")
}

func label(en bool, ja, ens string) string {
	if en {
		return ens
	}
	return ja
}

// ParsedInteraction is a decoded button custom_id (the answer intent).
type ParsedInteraction struct {
	Kind     string // "q" | "p" | "pl" | "op"
	Session  string
	QI, OI   int    // question/option index (Kind == "q")
	Fp       string // questions fingerprint (Kind == "q")
	Choice   string // "allow"/"deny" (p), "approve"/"reject" (pl / op)
	Approval string // operator approval id (Kind == "op")
}

// ParseCustomID decodes a button custom_id back into an answer intent, or (nil,
// false) if it isn't one of ours / is malformed.
func ParseCustomID(s string) (ParsedInteraction, bool) {
	parts := strings.Split(s, "|")
	if len(parts) < 3 || parts[0] != customIDPrefix {
		return ParsedInteraction{}, false
	}
	switch parts[1] {
	case "q":
		if len(parts) != 6 {
			return ParsedInteraction{}, false
		}
		qi, err1 := strconv.Atoi(parts[3])
		oi, err2 := strconv.Atoi(parts[4])
		if err1 != nil || err2 != nil || qi < 0 || oi < 0 {
			return ParsedInteraction{}, false
		}
		return ParsedInteraction{Kind: "q", Session: parts[2], QI: qi, OI: oi, Fp: parts[5]}, true
	case "p", "pl":
		if len(parts) != 4 {
			return ParsedInteraction{}, false
		}
		return ParsedInteraction{Kind: parts[1], Session: parts[3], Choice: parts[2]}, true
	case "op":
		// af|op|<approve|reject>|<id> — an operator destructive-action approval (P3).
		// There is no session; the id keys the bridge-approvals handshake record.
		if len(parts) != 4 || (parts[2] != "approve" && parts[2] != "reject") || parts[3] == "" {
			return ParsedInteraction{}, false
		}
		return ParsedInteraction{Kind: "op", Choice: parts[2], Approval: parts[3]}, true
	}
	return ParsedInteraction{}, false
}
