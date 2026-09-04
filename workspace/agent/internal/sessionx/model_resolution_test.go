package sessionx

import (
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

func TestResolveLiveModelAcceptsShortUniqueFamilyName(t *testing.T) {
	choices := []agents.ModelChoice{
		{ID: "gpt-5.6-terra", Label: "GPT-5.6-Terra"},
		{ID: "gpt-5.6-luna", Label: "GPT-5.6-Luna"},
	}
	got, err := resolveLiveModel("terra", choices)
	if err != nil || got != "gpt-5.6-terra" {
		t.Fatalf("resolveLiveModel(terra) = %q, %v", got, err)
	}
}

func TestResolveLiveModelRejectsUnavailableNameBeforeLaunch(t *testing.T) {
	_, err := resolveLiveModel("not-a-model", []agents.ModelChoice{{ID: "gpt-5.6-terra"}})
	if err == nil || !strings.Contains(err.Error(), "gpt-5.6-terra") {
		t.Fatalf("resolveLiveModel error = %v, want available model in message", err)
	}
}

func TestResolveLiveModelRejectsAmbiguousVersionPrefix(t *testing.T) {
	_, err := resolveLiveModel("gpt-5.6", []agents.ModelChoice{
		{ID: "gpt-5.6-terra"}, {ID: "gpt-5.6-luna"},
	})
	if err == nil || !strings.Contains(err.Error(), "曖昧") || !strings.Contains(err.Error(), "gpt-5.6-terra") {
		t.Fatalf("resolveLiveModel ambiguous error = %v", err)
	}
}

// A requested id that exactly matches one choice must win even when it is also a
// prefix of another (sakana/fugu vs sakana/fugu-ultra vs sakana/fugu-ultra-20260615):
// launching "sakana/fugu" failed with a false "ambiguous" error before this was fixed,
// because the prefix-match pass ran unconditionally and picked up the longer ids too.
func TestResolveLiveModelPrefersExactMatchOverPrefixCollision(t *testing.T) {
	choices := []agents.ModelChoice{
		{ID: "sakana/fugu"}, {ID: "sakana/fugu-ultra"}, {ID: "sakana/fugu-ultra-20260615"},
	}
	got, err := resolveLiveModel("sakana/fugu", choices)
	if err != nil || got != "sakana/fugu" {
		t.Fatalf("resolveLiveModel(sakana/fugu) = %q, %v", got, err)
	}
	got, err = resolveLiveModel("sakana/fugu-ultra", choices)
	if err != nil || got != "sakana/fugu-ultra" {
		t.Fatalf("resolveLiveModel(sakana/fugu-ultra) = %q, %v", got, err)
	}
}

// The rejection message lands in a Console toast and a phone notification, so it must
// stay readable when the live catalog is large. Seen in practice: a launch that named the
// retired opencode-go/ox-alpha-free listed all ~60 available ids, and the reason was no
// longer readable in the notification (once models.dev marks status=deprecated the id also
// disappears from `opencode models`). Name only the nearest candidates and count the rest.
func TestResolveLiveModelUnavailableNamesOnlyNearestIDs(t *testing.T) {
	var choices []agents.ModelChoice
	for _, id := range []string{
		"opencode/big-pickle", "opencode/claude-opus-5", "opencode/gpt-5.6-terra",
		"opencode/hy3-free", "opencode/kimi-k2.6", "opencode/nemotron-3-ultra-free",
		"opencode-go/glm-5.3", "opencode-go/glm-5.3-flash", "opencode-go/kimi-k3",
		"opencode-go/qwen3.8-max", "opencode-go/gpt-5.6-luna", "opencode-go/hy3",
	} {
		choices = append(choices, agents.ModelChoice{ID: id, Label: id})
	}
	_, err := resolveLiveModel("opencode-go/ox-alpha-free", choices)
	if err == nil {
		t.Fatal("resolveLiveModel accepted a retired model id")
	}
	msg := err.Error()
	// The nearest candidates must come from the same billing route (opencode-go/…). Filling
	// the 5 slots with unrelated Zen ids makes them useless as suggestions.
	if !strings.Contains(msg, "opencode-go/") {
		t.Fatalf("message names no opencode-go candidate: %s", msg)
	}
	// Never dump the whole list: what was cut off is reported as a count.
	if strings.Contains(msg, "opencode/big-pickle") {
		t.Fatalf("message dumped the whole catalog: %s", msg)
	}
	if !strings.Contains(msg, "ほか 7 件") {
		t.Fatalf("message does not report the dropped count: %s", msg)
	}
	if n := strings.Count(msg, ", "); n > modelSuggestLimit {
		t.Fatalf("message lists %d separators, want at most %d: %s", n, modelSuggestLimit, msg)
	}
}

// An ambiguous request lists what to pick from, so it keeps more candidates than an
// unknown model does.
func TestResolveLiveModelAmbiguousKeepsMoreCandidates(t *testing.T) {
	var choices []agents.ModelChoice
	for _, suffix := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l"} {
		choices = append(choices, agents.ModelChoice{ID: "gpt-5.6-" + suffix})
	}
	_, err := resolveLiveModel("gpt-5.6", choices)
	if err == nil || !strings.Contains(err.Error(), "曖昧") {
		t.Fatalf("resolveLiveModel(gpt-5.6) = %v, want ambiguous", err)
	}
	if !strings.Contains(err.Error(), "ほか 2 件") {
		t.Fatalf("ambiguous message should keep %d then count the rest: %s", modelAmbiguousLimit, err.Error())
	}
}

// A retired model needs different wording from a typo: told only "not available" when the
// id and the billing route are both right, the user suspects a misspelling and retries the
// same request.
func TestRetiredModelErrorSaysWhy(t *testing.T) {
	choices := []agents.ModelChoice{
		{ID: "opencode-go/glm-5.3"}, {ID: "opencode-go/kimi-k3"}, {ID: "opencode/big-pickle"},
	}
	msg := retiredModelError("opencode-go/ox-alpha-free", choices)
	if !strings.Contains(msg, "提供終了") {
		t.Fatalf("retired message does not say the model is gone: %s", msg)
	}
	if strings.Contains(msg, "は利用できません") {
		t.Fatalf("retired message reads like a typo rejection: %s", msg)
	}
	if !strings.Contains(msg, "opencode-go/glm-5.3") {
		t.Fatalf("retired message offers no replacement: %s", msg)
	}
}
