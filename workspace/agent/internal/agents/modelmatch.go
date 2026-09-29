package agents

import "strings"

// MatchModel returns the catalog rows a requested model name can mean: the one row whose id or
// label equals it, or else every row it names as a family ("terra" for gpt-5.6-terra). One row
// means it resolves; more means it is ambiguous. Launch resolves short names with it, and
// create_session's effort check must read the same row the launch will start, or it validates
// against one model and starts another.
func MatchModel(requested string, choices []ModelChoice) []ModelChoice {
	norm := func(s string) string {
		return strings.ToLower(strings.TrimSpace(s))
	}
	want := norm(requested)
	if want == "" {
		return nil
	}
	// An exact id/label match wins outright, even when it also happens to be a prefix of another
	// choice (e.g. "sakana/fugu" vs "sakana/fugu-ultra").
	for _, choice := range choices {
		if want == norm(choice.ID) || want == norm(choice.Label) {
			return []ModelChoice{choice}
		}
	}
	var matches []ModelChoice
	for _, choice := range choices {
		id, label := norm(choice.ID), norm(choice.Label)
		if strings.HasSuffix(id, "-"+want) || strings.HasSuffix(label, "-"+want) ||
			strings.HasPrefix(id, want+"-") || strings.HasPrefix(label, want+"-") {
			matches = append(matches, choice)
		}
	}
	return matches
}
