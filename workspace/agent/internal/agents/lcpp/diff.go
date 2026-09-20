package lcpp

// diff.go answers the one question the driver must get right per store.go's own
// AppendMessage doc comment: given the harness.Message slice already persisted (before) and
// harness.Run's Result.Messages (after), which entries of after are actually NEW and need
// AppendMessage?
//
// The naive answer — "everything past len(before)" — is wrong the moment a compaction fires
// inside the Run call: loop.go's compactPreservingLastRoundTrip only ever INSERTS one new
// system boundary message into the existing sequence (see its own doc comment — toSummarize
// and the preserved round trip are full[:lastAssistant] and full[lastAssistant:] verbatim,
// never reordered or dropped), so after has the same before-content, plus insertions, not a
// simple prefix relationship. before is therefore always a subsequence (in order, never
// reordered) of after, and newMessagesSince walks both in lockstep: whenever the next
// candidate in after equals the next unconsumed entry of before, that entry is already on
// disk and is skipped; everything else is new and is handed back for AppendMessage, which
// itself sorts out user/continuation/assistant/tool/system-note by role and content — this
// function only decides NEW vs ALREADY-PERSISTED, never the record Kind.
//
// This is a value-equality diff, so two coincidentally identical consecutive messages could in
// principle be matched out of order; harness.Message content (a model's own reply text, a
// tool's own output, a unique ToolCall.ID) makes an exact collision vanishingly unlikely in
// practice, and the failure mode if one ever happens is a duplicate mirror row, not a broken
// send (SendMessages only reads from the last compaction boundary onward).

import "github.com/k-k1/agent-fleet/workspace/agent/internal/harness"

// newMessagesSince returns the entries of after that are not already accounted for by before,
// in after's own order.
func newMessagesSince(before, after []harness.Message) []harness.Message {
	i := 0
	var out []harness.Message
	for _, m := range after {
		if i < len(before) && messagesEqual(before[i], m) {
			i++
			continue
		}
		out = append(out, m)
	}
	return out
}

func messagesEqual(a, b harness.Message) bool {
	if a.Role != b.Role || a.Content != b.Content || a.Reasoning != b.Reasoning || a.ToolCallID != b.ToolCallID {
		return false
	}
	if len(a.ToolCalls) != len(b.ToolCalls) {
		return false
	}
	for i := range a.ToolCalls {
		if a.ToolCalls[i] != b.ToolCalls[i] {
			return false
		}
	}
	return true
}
