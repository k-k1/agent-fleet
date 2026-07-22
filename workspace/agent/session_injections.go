package main

// Injected prompts that did not come from the user's own keyboard land in the CLI
// transcript as ordinary user turns — indistinguishable from what the user typed in
// the composer or the raw terminal. Two sources inject this way:
//   - the fleet operator (docs/30 ②): an af_write assistant's create_session
//     initial_prompt / send_to_session, tagged Source="operator".
//   - the chat bridge (docs/37 P2a): a Discord (later Slack) thread reply routed back
//     into the session, tagged Source="discord" / "slack".
// To let the mirror tell them apart from self-typed input, we remember each injected
// prompt's text AND its origin per session and, when serving the transcript, tag the
// matching user turn with that origin (transcript.Turn.Source → a Console badge).

import (
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/fstore"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// Turn origins recorded here (transcript.Turn.Source values). "" = the user's own input.
const (
	turnSourceOperator = "operator" // fleet operator injected (docs/30 ②)
	turnSourceDiscord  = "discord"  // chat bridge — Discord thread reply (docs/37 P2a)
	turnSourceSlack    = "slack"    // chat bridge — Slack (P2 follow-up)
)

// maxOperatorInjections caps the per-session record. Membership is all the tagging needs,
// so we keep the newest N distinct texts (a long-lived session steered many times stays
// bounded).
const maxOperatorInjections = 100

// injectedPrompt is one remembered injection: the prompt text (the tagging key) plus the
// origin to stamp onto the matching user turn.
type injectedPrompt struct {
	Text   string `json:"text"`
	Source string `json:"source"`
}

// injectionStore holds, per session, the distinct prompt texts injected into it and their
// origins. A SEPARATE file from Meta (same reasoning as the report link): several code
// paths touch session state and Meta is one clobber-prone blob. (Format note: this replaced
// an earlier []string operator-only store — an old file simply fails to decode into the new
// shape and is treated as empty, which only drops badges on in-flight sessions at upgrade.)
var injectionStore = fstore.JSON[[]injectedPrompt](paths.AgentConfigDir, "session-injections", ".json")

// recordInjection remembers a prompt injected into a session, tagged with its origin, so the
// transcript can attribute the matching user turn. Deduped by text (a resend needn't
// duplicate; a re-injection from a different source updates the origin) and capped (newest
// kept).
func recordInjection(name, text, source string) {
	text = strings.TrimSpace(text)
	if !session.ValidName(name) || text == "" {
		return
	}
	list, _ := injectionStore.Read(name)
	for i, e := range list {
		if e.Text == text {
			list[i].Source = source // latest origin wins for the same text
			_ = injectionStore.Write(name, list)
			return
		}
	}
	list = append(list, injectedPrompt{Text: text, Source: source})
	if len(list) > maxOperatorInjections {
		list = list[len(list)-maxOperatorInjections:]
	}
	_ = injectionStore.Write(name, list)
}

// recordOperatorInjection is the operator-origin convenience wrapper (docs/30 ②), kept so
// the several operator-injection call sites read unchanged.
func recordOperatorInjection(name, text string) { recordInjection(name, text, turnSourceOperator) }

// operatorInjections returns the distinct prompt texts recorded for a session (nil when
// none). Used by tests.
func operatorInjections(name string) []string {
	list, _ := injectionStore.Read(name)
	out := make([]string, 0, len(list))
	for _, e := range list {
		out = append(out, e.Text)
	}
	return out
}

// tagInjectedTurns stamps each user turn whose text matches a recorded injection with that
// injection's origin (transcript.Turn.Source). A cheap no-op when nothing was injected (the
// common case: one file read, then return).
func tagInjectedTurns(name string, turns []transcript.Turn) {
	if len(turns) == 0 {
		return
	}
	list, ok := injectionStore.Read(name)
	if !ok || len(list) == 0 {
		return
	}
	bySource := make(map[string]string, len(list))
	for _, e := range list {
		bySource[e.Text] = e.Source
	}
	for i := range turns {
		if turns[i].Role != "user" {
			continue
		}
		if src, hit := bySource[strings.TrimSpace(turns[i].Text)]; hit {
			turns[i].Source = src
		}
	}
}
