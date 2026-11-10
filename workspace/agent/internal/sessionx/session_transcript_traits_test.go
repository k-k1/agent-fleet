package sessionx

import "testing"

// oldTranscriptTraits is a copy of the kind branches session_transcript.go
// carried before the table.
func oldTranscriptTraits(kind string) (own, hasTerminal bool, normal string, planInMeta bool) {
	own = kind == "claude"
	hasTerminal = kind == "codex"
	normal = "Default"
	if kind == "opencode" {
		normal = "Build"
	}
	planInMeta = kind == "codex"
	return
}

func TestTranscriptTraitsMatchOldSwitch(t *testing.T) {
	kinds := append(RegisteredKinds(), "", "unknown")
	for _, k := range kinds {
		own, term, normal, plan := oldTranscriptTraits(k)
		tr := transcriptTraitsOf(k)
		if tr.ownMessagesPath != own || (tr.terminalState != nil) != term ||
			normalModeLabelOf(k) != normal || tr.planInMeta != plan {
			t.Errorf("kind %q: table (%v,%v,%q,%v) != old switch (%v,%v,%q,%v)", k,
				tr.ownMessagesPath, tr.terminalState != nil, normalModeLabelOf(k), tr.planInMeta,
				own, term, normal, plan)
		}
	}
}
