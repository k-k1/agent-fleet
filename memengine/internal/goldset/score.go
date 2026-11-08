package goldset

import (
	"fmt"
	"sort"
	"strings"

	"github.com/k-k1/memengine"
)

// ClassifyScore counts the classification answers for the cases the member
// labelled with one operation.
type ClassifyScore struct {
	Cases   int `json:"cases"`
	Correct int `json:"correct"`
	// Wrong is a change the member did not want: a change on a no-op case, or
	// the wrong kind of change.
	Wrong int `json:"wrong_changes"`
	// Missed is a no-op answer on a case that needed a change.
	Missed int `json:"missed_fixes"`
	// Invalid is an answer dropped by validation; Errors are failed calls.
	Invalid int `json:"invalid"`
	Errors  int `json:"errors"`
	// Escalated counts valid answers the small tier must hand up (supersede,
	// merge), whatever the label says.
	Escalated int `json:"escalated"`
}

// PatchScore counts the patch calls on cases labelled patch.
type PatchScore struct {
	Calls       int `json:"calls"`
	Applied     int `json:"applied"`
	Missed      int `json:"missed_fixes"` // empty answer
	Invalid     int `json:"invalid"`
	Errors      int `json:"errors"`
	WrongChange int `json:"wrong_changes"`
	// LostCases is the number of applied patches that lost at least one
	// must_survive item; LostItems / VerifiableItems is the item-level rate,
	// over the applied patches only.
	LostCases       int `json:"cases_losing_facts"`
	LostItems       int `json:"lost_items"`
	VerifiableItems int `json:"verifiable_items"`
	UnverifiedItems int `json:"unverifiable_items"`
	// NewMissing counts items whose new fact (one the reference patch brings)
	// is absent from an applied patch: a fix made only in part.
	NewMissing int `json:"new_facts_missing"`
}

// TierScore is one tier's scores and tokens.
type TierScore struct {
	Name     string                                `json:"name"`
	Classify map[memengine.Operation]ClassifyScore `json:"classify"`
	Total    ClassifyScore                         `json:"classify_total"`
	Patch    PatchScore                            `json:"patch"`
	// Tokens per call: all input tokens the command reported, and the same
	// minus the command's fixed overhead (Baseline), which is not the prompt.
	ClassifyCalls, PatchCalls int
	ClassifyInPerCall         float64 `json:"classify_input_per_call"`
	ClassifyPromptPerCall     float64 `json:"classify_prompt_per_call"`
	ClassifyOutPerCall        float64 `json:"classify_output_per_call"`
	PatchInPerCall            float64 `json:"patch_input_per_call"`
	PatchPromptPerCall        float64 `json:"patch_prompt_per_call"`
	PatchOutPerCall           float64 `json:"patch_output_per_call"`
	TotalInput, TotalOutput   int
	UsageKnown                bool `json:"usage_known"` // false when the tier's CLI reported no usage
	BaselineInput             int  `json:"baseline_input"`
}

// ScoreTier scores one tier's run.
func ScoreTier(r *TierRun) TierScore {
	ts := TierScore{Name: r.Name, Classify: map[memengine.Operation]ClassifyScore{}, BaselineInput: r.Baseline.TotalInput()}
	var cIn, cOut, pIn, pOut int
	for _, o := range r.Classify {
		s := ts.Classify[o.Label]
		s.Cases++
		switch o.Status {
		case StatusInvalid:
			s.Invalid++
		case StatusError:
			s.Errors++
		default:
			if o.Pred.Escalates() {
				s.Escalated++
			}
			switch {
			case o.Pred == o.Label:
				s.Correct++
			case o.Pred == memengine.OpNoop:
				s.Missed++
			default:
				s.Wrong++
			}
		}
		ts.Classify[o.Label] = s
		cIn += o.Usage.TotalInput()
		cOut += o.Usage.OutputTokens
		if o.Status != StatusError {
			ts.ClassifyCalls++
		}
	}
	// Recompute the total from the per-operation rows so the two never drift.
	ts.Total = ClassifyScore{}
	for _, s := range ts.Classify {
		ts.Total.Cases += s.Cases
		ts.Total.Correct += s.Correct
		ts.Total.Wrong += s.Wrong
		ts.Total.Missed += s.Missed
		ts.Total.Invalid += s.Invalid
		ts.Total.Errors += s.Errors
		ts.Total.Escalated += s.Escalated
	}
	for _, o := range r.Patch {
		p := &ts.Patch
		p.Calls++
		switch o.Status {
		case StatusApplied:
			p.Applied++
		case StatusEmpty:
			p.Missed++
		case StatusInvalid:
			p.Invalid++
		default:
			p.Errors++
		}
		if o.Status == StatusApplied || o.Status == StatusEmpty {
			if o.WrongChange {
				p.WrongChange++
			}
			if len(o.Lost) > 0 {
				p.LostCases++
			}
			p.LostItems += len(o.Lost)
			p.NewMissing += o.NewMissing
			p.VerifiableItems += o.Verifiable
			p.UnverifiedItems += o.Unverifiable
		}
		pIn += o.Usage.TotalInput()
		pOut += o.Usage.OutputTokens
		if o.Status != StatusError {
			ts.PatchCalls++
		}
	}
	ts.TotalInput, ts.TotalOutput = cIn+pIn, cOut+pOut
	ts.UsageKnown = ts.TotalInput+ts.TotalOutput > 0
	per := func(sum, n, sub int) float64 {
		if n == 0 {
			return 0
		}
		return float64(sum-n*sub) / float64(n)
	}
	b := ts.BaselineInput
	ts.ClassifyInPerCall, ts.ClassifyPromptPerCall = per(cIn, ts.ClassifyCalls, 0), per(cIn, ts.ClassifyCalls, b)
	ts.PatchInPerCall, ts.PatchPromptPerCall = per(pIn, ts.PatchCalls, 0), per(pIn, ts.PatchCalls, b)
	if ts.ClassifyCalls > 0 {
		ts.ClassifyOutPerCall = float64(cOut) / float64(ts.ClassifyCalls)
	}
	if ts.PatchCalls > 0 {
		ts.PatchOutPerCall = float64(pOut) / float64(ts.PatchCalls)
	}
	return ts
}

// Agreement is the share of cases where both tiers gave the same valid
// classification. It is a secondary measure: two tiers can agree and both be
// wrong.
func Agreement(a, b *TierRun) (same, both int) {
	pb := map[string]ClassifyOutcome{}
	for _, o := range b.Classify {
		pb[o.Case] = o
	}
	for _, o := range a.Classify {
		q, ok := pb[o.Case]
		if !ok || o.Status != StatusOK || q.Status != StatusOK {
			continue
		}
		both++
		if o.Pred == q.Pred {
			same++
		}
	}
	return same, both
}

// Projection is the ADR's monthly input-token estimate for given daily rates.
func Projection(ts TierScore, classifiesPerDay, patchesPerDay int, prompt bool) float64 {
	c, p := ts.ClassifyInPerCall, ts.PatchInPerCall
	if prompt {
		c, p = ts.ClassifyPromptPerCall, ts.PatchPromptPerCall
	}
	return 30 * (float64(classifiesPerDay)*c + float64(patchesPerDay)*p)
}

// Reference scores the member's own reference patches: a calibration for the
// fact check (a reference patch must lose no fact) and the ceiling any model
// could reach on it.
func Reference(s *Set) PatchScore {
	var run TierRun
	for _, c := range s.Cases {
		tgt, ok := s.PatchTarget(c)
		if !ok {
			continue
		}
		eds := toEdits(c.Edits)
		o := ScorePatch(c, tgt, eds)
		run.Patch = append(run.Patch, o)
	}
	return ScoreTier(&run).Patch
}

var opOrder = []memengine.Operation{memengine.OpAdd, memengine.OpPatch, memengine.OpSupersede, memengine.OpMerge, memengine.OpNoop}

// Markdown renders the scores. It holds counts and rates only: no case text,
// no memory name.
func Markdown(tiers []TierScore, ref PatchScore, agree [][3]int, notes []string) string {
	var b strings.Builder
	pct := func(n, d int) string {
		if d == 0 {
			return "-"
		}
		return fmt.Sprintf("%d/%d (%.0f%%)", n, d, 100*float64(n)/float64(d))
	}
	b.WriteString("# Gold-set evaluation\n\n")
	b.WriteString("## Classification, per labelled operation\n\n")
	b.WriteString("| tier | label | cases | correct | wrong changes | missed fixes | invalid | errors | escalated |\n|---|---|---|---|---|---|---|---|---|\n")
	for _, t := range tiers {
		for _, op := range opOrder {
			s, ok := t.Classify[op]
			if !ok {
				continue
			}
			fmt.Fprintf(&b, "| %s | %s | %d | %s | %d | %d | %d | %d | %d |\n", t.Name, op, s.Cases, pct(s.Correct, s.Cases), s.Wrong, s.Missed, s.Invalid, s.Errors, s.Escalated)
		}
		s := t.Total
		fmt.Fprintf(&b, "| %s | **all** | %d | %s | %d | %d | %d | %d | %d |\n", t.Name, s.Cases, pct(s.Correct, s.Cases), s.Wrong, s.Missed, s.Invalid, s.Errors, s.Escalated)
	}
	b.WriteString("\n## Patch calls (cases labelled patch)\n\n")
	b.WriteString("| tier | calls | applied | missed fixes (empty) | invalid | errors | wrong changes | patches losing a fact | lost items | new facts missing |\n|---|---|---|---|---|---|---|---|---|---|\n")
	row := func(name string, p PatchScore) {
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %d | %d | %d | %s | %d |\n", name, p.Calls, p.Applied, p.Missed, p.Invalid, p.Errors, p.WrongChange, p.LostCases, pct(p.LostItems, p.VerifiableItems), p.NewMissing)
	}
	row("reference (member's own patch)", ref)
	for _, t := range tiers {
		row(t.Name, t.Patch)
	}
	if len(tiers) > 0 {
		fmt.Fprintf(&b, "\nMust-survive items the fact check could not verify (no anchor): %d of %d across the reference patches.\n", ref.UnverifiedItems, ref.UnverifiedItems+ref.VerifiableItems)
	}
	b.WriteString("\n## Tokens per call and monthly projection\n\n")
	b.WriteString("Input counts every input token the command reported (cache reads and writes included); *prompt* subtracts the command's fixed overhead measured by a trivial call. Projection: 30 days x (5 classifications + 2 patches) a day, the ADR's low scenario.\n\n")
	b.WriteString("| tier | classify in | classify prompt | classify out | patch in | patch prompt | patch out | overhead | month in (total) | month in (prompt only) |\n|---|---|---|---|---|---|---|---|---|---|\n")
	for _, t := range tiers {
		if !t.UsageKnown {
			fmt.Fprintf(&b, "| %s | n/a | n/a | n/a | n/a | n/a | n/a | n/a | n/a | n/a |\n", t.Name)
			continue
		}
		fmt.Fprintf(&b, "| %s | %.0f | %.0f | %.0f | %.0f | %.0f | %.0f | %d | %.2f M | %.2f M |\n", t.Name,
			t.ClassifyInPerCall, t.ClassifyPromptPerCall, t.ClassifyOutPerCall, t.PatchInPerCall, t.PatchPromptPerCall, t.PatchOutPerCall,
			t.BaselineInput, Projection(t, 5, 2, false)/1e6, Projection(t, 5, 2, true)/1e6)
	}
	if len(agree) > 0 {
		b.WriteString("\n## Tier agreement (secondary)\n\n")
		for _, a := range agree {
			fmt.Fprintf(&b, "- tier 1 vs tier %d: %s\n", a[2]+1, pct(a[0], a[1]))
		}
	}
	sort.Strings(notes)
	for _, n := range notes {
		fmt.Fprintf(&b, "\n%s\n", n)
	}
	return b.String()
}
