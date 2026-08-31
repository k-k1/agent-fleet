package claude

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestPlanVerdict(t *testing.T) {
	approval := "User has approved your plan. You can now start coding. Start with updating your todo list if applicable\n\n" +
		"Your plan has been saved to: /var/lib/af/claude/plans/immutable-dazzling-babbage.md\n" +
		"You can refer back to it if needed during implementation.\n\n" +
		"## Approved Plan:\n# フロービルダー UI の移植\n\n- 前回の案は却下。途中で中止はしない。\n"
	for _, c := range []struct{ name, in, want string }{
		// 2026-08-31 の症状そのもの: 計画本文に却下語が入った承認結果。
		{"approval carrying a plan that says 却下", approval, PlanApproved},
		{"plain approval", "User approved the plan", PlanApproved},
		{"interrupt = reject", "[Request interrupted by user for tool use]", PlanRejected},
		{"keep planning", "not approved, keep planning", PlanRejected},
		// 読めない形＝ドリフトの署名。Console のバッジは「決定済み」に倒れる。
		{"unknown wording", "The plan step has concluded.", PlanUnknown},
		{"empty", "", PlanUnknown},
	} {
		if got := PlanVerdict(c.in); got != c.want {
			t.Errorf("%s: PlanVerdict = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestPlanVerdictKeywordsMatchConsole locks this Go copy to the Console's planDecision.ts.
//
// The badge the user sees is computed in the Console; this package only mirrors the rule
// so the Agent can notice (notePlanVerdict) when the CLI's wording drifts out from under
// both. Two copies of a heuristic silently diverging is exactly how the original bug
// stayed invisible, so the copy is asserted here rather than trusted to review.
func TestPlanVerdictKeywordsMatchConsole(t *testing.T) {
	path := filepath.Join("..", "..", "..", "..", "..", "console", "src", "features", "mirror", "planDecision.ts")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("planDecision.ts not available (%v)", err)
	}
	src := string(b)
	// Both heuristics are written as `return /<pattern>/i.test(outcomeHead(outcome));`
	// in source order: isApproved first, isRejected second.
	lit := regexp.MustCompile(`return /(.+)/i\.test\(outcomeHead\(outcome\)\);`)
	found := lit.FindAllStringSubmatch(src, -1)
	if len(found) != 2 {
		t.Fatalf("planDecision.ts: found %d keyword regexes, want 2 (isApproved, isRejected) — the Console rule was rewritten; re-check this mirror", len(found))
	}
	for i, want := range []string{
		strings.TrimPrefix(planApprovedRe.String(), "(?i)"),
		strings.TrimPrefix(planRejectedRe.String(), "(?i)"),
	} {
		if got := found[i][1]; got != want {
			t.Errorf("keyword list %d differs — Console: %q / Go: %q", i, got, want)
		}
	}
	// The embedded-plan cut must agree too: reading the plan body is what broke the badge.
	marker := regexp.MustCompile(`PLAN_BODY_MARKER = /(.+)/im;`).FindStringSubmatch(src)
	if marker == nil {
		t.Fatal("planDecision.ts: PLAN_BODY_MARKER not found — the embedded-plan cut moved")
	}
	if want := strings.TrimPrefix(planBodyMarker.String(), "(?im)"); marker[1] != want {
		t.Errorf("PLAN_BODY_MARKER differs — Console: %q / Go: %q", marker[1], want)
	}
}
