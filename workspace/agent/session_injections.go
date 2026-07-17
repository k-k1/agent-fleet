package main

// docs/30 ②: prompts the fleet operator injects into a session (an af_write assistant's
// create_session initial_prompt / send_to_session) are typed into the TUI verbatim and
// land in the CLI transcript as ordinary user turns — indistinguishable from what the user
// typed themselves in the composer or the raw terminal. To let the mirror tell them apart,
// we remember the operator's injected prompt texts per session and, when serving the
// transcript, tag matching user turns Source="operator".

import (
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/fstore"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// turnSourceOperator marks a user turn that the operator injected (transcript.Turn.Source).
const turnSourceOperator = "operator"

// maxOperatorInjections caps the per-session record. Membership is all the tagging needs,
// so we keep the newest N distinct texts (a long-lived session steered many times stays
// bounded).
const maxOperatorInjections = 100

// operatorInjectionStore holds, per session, the distinct prompt texts the operator has
// injected. A SEPARATE file from Meta (same reasoning as the report link): several code
// paths touch session state and Meta is one clobber-prone blob.
var operatorInjectionStore = fstore.JSON[[]string](paths.AgentConfigDir, "session-injections", ".json")

// recordOperatorInjection remembers a prompt the operator injected into a session so the
// transcript can attribute the matching user turn. Called only on operator-originated input
// (report_to present). Deduped (a resend needn't duplicate) and capped (newest kept).
func recordOperatorInjection(name, text string) {
	text = strings.TrimSpace(text)
	if !session.ValidName(name) || text == "" {
		return
	}
	list, _ := operatorInjectionStore.Read(name)
	for _, t := range list {
		if t == text {
			return
		}
	}
	list = append(list, text)
	if len(list) > maxOperatorInjections {
		list = list[len(list)-maxOperatorInjections:]
	}
	_ = operatorInjectionStore.Write(name, list)
}

// operatorInjections returns the distinct prompt texts recorded for a session (nil when
// none). Used by tagOperatorTurns and tests.
func operatorInjections(name string) []string {
	list, _ := operatorInjectionStore.Read(name)
	return list
}

// tagOperatorTurns sets Source="operator" on each user turn whose text matches a recorded
// operator injection. A cheap no-op when nothing was injected (the common case: one file
// read, then return).
func tagOperatorTurns(name string, turns []transcript.Turn) {
	if len(turns) == 0 {
		return
	}
	list, ok := operatorInjectionStore.Read(name)
	if !ok || len(list) == 0 {
		return
	}
	set := make(map[string]struct{}, len(list))
	for _, t := range list {
		set[t] = struct{}{}
	}
	for i := range turns {
		if turns[i].Role != "user" {
			continue
		}
		if _, hit := set[strings.TrimSpace(turns[i].Text)]; hit {
			turns[i].Source = turnSourceOperator
		}
	}
}
