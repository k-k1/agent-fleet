package tmuxx

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Regression lock for the 2026-07-22 plan-approval bug.
//
// Symptom: the Console's Reject button drove claude's ExitPlanMode select menu by a fixed
// keystroke offset ("Down Down Down Enter", aiming at the 4th "Tell Claude what to
// change" row). On a SHORTER, wrapping menu those Downs wrapped back onto a "Yes" row,
// so Reject silently APPROVED the plan.
//
// Root cause is a CLI contract the frontend can't see: the ExitPlanMode menu's option
// count/order is version-dependent. This test pins the KNOWN shapes and asserts the
// invariants our (fixed) input strategy relies on:
//   - approve = "Enter" (accept the highlighted default) is safe iff the default is a Yes.
//   - reject must NOT navigate by position — on the real 2.1.212 menu there is no reject
//     row at all, so the only sound reject is an interrupt (Escape). See the frontend's
//     planDecision.ts.
//
// Like footer_corpus_test.go this is a LOCK, not a live drift detector: if a future CLI
// changes the menu, refresh the captures here (and the live tui_contract probe catches
// drift in-image). It fails loudly the moment a refreshed capture violates an invariant.

type planOption struct {
	n         int
	label     string
	isDefault bool // marked with ❯
}

var planMenuLineRe = regexp.MustCompile(`^(❯\s+)?([0-9]+)\.\s+(.*\S)\s*$`)

// parsePlanApprovalMenu extracts the numbered options from a captured ExitPlanMode
// approval modal, flagging the ❯-highlighted default.
func parsePlanApprovalMenu(capture string) []planOption {
	var opts []planOption
	for _, ln := range strings.Split(capture, "\n") {
		m := planMenuLineRe.FindStringSubmatch(strings.TrimSpace(ln))
		if m == nil {
			continue
		}
		opts = append(opts, planOption{
			n:         len(opts) + 1,
			label:     m[3],
			isDefault: m[1] != "",
		})
	}
	return opts
}

// isPlanApproval reports whether a menu label is an approval ("Yes, …") as opposed to a
// reject/refine row ("No, …" / "Tell Claude what to change").
func isPlanApproval(label string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(label)), "yes")
}

func loadPlanCapture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "footers", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

func TestPlanApprovalContract(t *testing.T) {
	// Two real, divergent captures. 4-option is the golden testdata file; 2-option is the
	// same 2.1.212 capture used in spinner_test.go's notAtPrompt set.
	fourOpt := loadPlanCapture(t, "modal_plan_approval.txt")
	twoOpt := "   Claude has written up a plan and is ready to execute. Would you like to\n" +
		"   proceed?\n\n   ❯ 1. Yes, and use auto mode\n     2. Yes, manually approve edits\n\n" +
		"   ctrl+g to edit in Vim ·"
	// 3-option: captured live from 2.1.251 by the plan approval probe
	// (workspace/agent/claude_plan_contract_test.go, 2026-08-31). Third distinct shape in
	// six weeks — the menu keeps moving, which is the whole reason approve must be
	// "Enter = the highlighted default" and reject must be an interrupt.
	threeOpt := "   Claude has written up a plan and is ready to execute. Would you like to proceed?\n\n" +
		"   ❯ 1. Yes, and switch to BYPASS PERMISSIONS (no further prompts) for this session\n" +
		"     2. Yes, manually approve edits\n     3. Tell Claude what to change\n" +
		"        shift+tab to approve with this feedback\n\n   ctrl+g to edit in Vim ·"

	for _, c := range []struct {
		name    string
		capture string
		wantN   int
	}{
		{"4-option (testdata golden)", fourOpt, 4},
		{"3-option (2.1.251, live capture)", threeOpt, 3},
		{"2-option (2.1.212)", twoOpt, 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			opts := parsePlanApprovalMenu(c.capture)
			if len(opts) != c.wantN {
				t.Fatalf("parsed %d options, want %d: %+v", len(opts), c.wantN, opts)
			}
			// Invariant guarding approve = "Enter": the highlighted default is an approval.
			if !opts[0].isDefault {
				t.Errorf("option 1 is not the highlighted default (❯): %+v", opts)
			}
			for _, o := range opts {
				if o.isDefault && !isPlanApproval(o.label) {
					t.Errorf("default option is not an approval — Enter would no longer approve: %q", o.label)
				}
			}
		})
	}

	// The killer fact behind "reject must interrupt": the real 2.1.212 menu has NO reject
	// row — every option is a "Yes". You literally cannot reject by selecting an option;
	// only Escape (interrupt) works. So a keystroke-navigation reject is unsound by design.
	two := parsePlanApprovalMenu(twoOpt)
	for _, o := range two {
		if !isPlanApproval(o.label) {
			t.Fatalf("test assumption broke: 2.1.212 menu unexpectedly has a reject row %q", o.label)
		}
	}

	// And demonstrate the exact bug: the old fixed reject (Down×3 + Enter) on the wrapping
	// 2-option menu lands back on a "Yes" — i.e. it approves. This is why the offset had to
	// go. (Down×3 from default index 0, wrapping: (0+3) % 2 = index 1 = "Yes, manually…".)
	sel := selectByDownOffset(two, 3)
	if !isPlanApproval(sel.label) {
		t.Fatalf("expected the old Down×3 reject to wrap onto a Yes on the 2-option menu, got %q", sel.label)
	}

	// The clincher: there is no single Down-offset that rejects on BOTH known menu shapes,
	// so no positional reject can ever be correct — interrupt is the only sound reject.
	four := parsePlanApprovalMenu(fourOpt)
	three := parsePlanApprovalMenu(threeOpt)
	for off := 0; off <= 8; off++ {
		if !isPlanApproval(selectByDownOffset(four, off).label) &&
			!isPlanApproval(selectByDownOffset(three, off).label) &&
			!isPlanApproval(selectByDownOffset(two, off).label) {
			t.Fatalf("Down×%d rejects on both menus — if the CLI ever aligns menu shapes, "+
				"revisit whether keystroke reject is viable; today reject must interrupt", off)
		}
	}
}

// selectByDownOffset models claude's Ink select: start on the ❯ default and press Down
// `off` times, wrapping at the end, then Enter selects the row under the cursor.
func selectByDownOffset(opts []planOption, off int) planOption {
	start := 0
	for i, o := range opts {
		if o.isDefault {
			start = i
			break
		}
	}
	return opts[(start+off)%len(opts)]
}
