package goldset

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/k-k1/memengine"
)

// Threshold is the bar for one operation. A zero MaxInvalid means none is
// allowed; to allow more, say so in the file.
type Threshold struct {
	MaxWrongChanges int     `json:"max_wrong_changes"`
	MaxInvalid      int     `json:"max_invalid"`
	MinFixRate      float64 `json:"min_fix_rate"`
	// MaxLostFacts applies to the patch contract only.
	MaxLostFacts int `json:"max_lost_facts,omitempty"`
}

// Bar is the data the scorer judges against: one threshold per operation, and
// which operations each role must pass. "patch-call" is the patch contract
// (the {old,new} edits), the others are classification, by gold label.
type Bar struct {
	Operations map[string]Threshold        `json:"operations"`
	Roles      map[memengine.Role][]string `json:"roles"`
}

// LoadBar reads the thresholds file.
func LoadBar(path string) (Bar, error) {
	var b Bar
	raw, err := os.ReadFile(path)
	if err != nil {
		return b, err
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		return b, fmt.Errorf("%s: %w", path, err)
	}
	for role, ops := range b.Roles {
		for _, op := range ops {
			if _, ok := b.Operations[op]; !ok {
				return b, fmt.Errorf("%s: role %s needs operation %q, which has no threshold", path, role, op)
			}
		}
	}
	return b, nil
}

// Verdict is one tier's result against one operation.
type Verdict struct {
	Pass   bool
	Reason string // what failed, empty on a pass
}

// Judge checks one tier against every operation in the bar.
func (b Bar) Judge(ts TierScore) map[string]Verdict {
	out := map[string]Verdict{}
	for op, th := range b.Operations {
		if op == "patch-call" {
			p := ts.Patch
			out[op] = th.check(p.WrongChange, p.Invalid+p.Errors, p.LostItems, rate(p.Applied, p.Calls))
			continue
		}
		s, ok := ts.Classify[memengine.Operation(op)]
		if !ok {
			out[op] = Verdict{Reason: "no cases"}
			continue
		}
		out[op] = th.check(s.Wrong, s.Invalid+s.Errors, 0, rate(s.Correct, s.Cases))
	}
	return out
}

func rate(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) / float64(d)
}

func (t Threshold) check(wrong, invalid, lost int, fix float64) Verdict {
	var why []string
	if wrong > t.MaxWrongChanges {
		why = append(why, fmt.Sprintf("wrong changes %d > %d", wrong, t.MaxWrongChanges))
	}
	if invalid > t.MaxInvalid {
		why = append(why, fmt.Sprintf("invalid or failed %d > %d", invalid, t.MaxInvalid))
	}
	if lost > t.MaxLostFacts {
		why = append(why, fmt.Sprintf("lost facts %d > %d", lost, t.MaxLostFacts))
	}
	if fix < t.MinFixRate {
		why = append(why, fmt.Sprintf("fix rate %.0f%% < %.0f%%", 100*fix, 100*t.MinFixRate))
	}
	return Verdict{Pass: len(why) == 0, Reason: strings.Join(why, "; ")}
}

// BarMarkdown renders pass/fail per candidate per operation and suggests, for
// each role, the cheapest candidate that passes every operation of the role.
// Only runs scored against the current gold set take part; a replay candidate
// is marked, since a session saw earlier cases. cost maps a tier name to its
// relative cost.
func BarMarkdown(b Bar, tiers []TierScore, goldHash string, cost func(string) float64) string {
	var ops []string
	for op := range b.Operations {
		ops = append(ops, op)
	}
	sort.Strings(ops)
	var sb strings.Builder
	sb.WriteString("\n## Bar per operation (thresholds are data: the bar file)\n\n")
	sb.WriteString("| operation | max wrong changes | max invalid/failed | min fix rate | max lost facts |\n|---|---|---|---|---|\n")
	for _, op := range ops {
		t := b.Operations[op]
		fmt.Fprintf(&sb, "| %s | %d | %d | %.0f%% | %d |\n", op, t.MaxWrongChanges, t.MaxInvalid, 100*t.MinFixRate, t.MaxLostFacts)
	}
	sb.WriteString("\n| candidate |")
	for _, op := range ops {
		sb.WriteString(" " + op + " |")
	}
	sb.WriteString("\n|---|" + strings.Repeat("---|", len(ops)) + "\n")
	verdicts := map[string]map[string]Verdict{}
	var stale []string
	for _, t := range tiers {
		if t.GoldHash != goldHash {
			stale = append(stale, t.Name)
			continue
		}
		v := b.Judge(t)
		verdicts[t.Name] = v
		name := t.Name
		if t.Mode == "replay" {
			name += " (replay)"
		}
		fmt.Fprintf(&sb, "| %s |", name)
		for _, op := range ops {
			if v[op].Pass {
				sb.WriteString(" pass |")
			} else {
				sb.WriteString(" FAIL: " + v[op].Reason + " |")
			}
		}
		sb.WriteString("\n")
	}
	if len(stale) > 0 {
		fmt.Fprintf(&sb, "\nNot judged, scored against another gold-set version than %s: %s.\n", goldHash, strings.Join(stale, ", "))
	}
	sb.WriteString("\n### Suggested mapping (cheapest passing candidate per role)\n\n")
	for _, role := range memengine.Roles() {
		need := b.Roles[role]
		if len(need) == 0 {
			continue
		}
		best, bestCost := "", 0.0
		var names []string
		for n := range verdicts {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			ok := true
			for _, op := range need {
				ok = ok && verdicts[n][op].Pass
			}
			if c := cost(n); ok && (best == "" || c < bestCost) {
				best, bestCost = n, c
			}
		}
		if best == "" {
			fmt.Fprintf(&sb, "- %s (%s): no candidate passes; keep the current mapping or lower the bar knowingly.\n", role, strings.Join(need, ", "))
			continue
		}
		note := ""
		for _, t := range tiers {
			if t.Name == best && t.Mode == "replay" {
				note = " (replay result: confirm with a live run before mapping)"
			}
		}
		fmt.Fprintf(&sb, "- %s (%s): **%s**%s\n", role, strings.Join(need, ", "), best, note)
	}
	return sb.String()
}
