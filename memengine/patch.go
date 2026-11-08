package memengine

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Edit replaces one exact occurrence of Old with New. It is the form of
// ADR 0130 decision 4: an edit, never a whole body. Field is "body" (the
// default) or "description".
type Edit struct {
	Field string  `json:"field,omitempty"`
	Old   *string `json:"old"`
	New   *string `json:"new"`
}

// PatchSchema is the fixed answer schema, shown to the model verbatim.
const PatchSchema = `{
  "edits": [
    {"field": "body | description (default body)", "old": "exact text to replace, copied from the note, appears once", "new": "replacement text, may be empty to delete"}
  ]
}`

// MaxEdits bounds one answer. A patch call fixes one place or a handful.
const MaxEdits = 12

// PatchRequest is one note and the observation to fold into it.
type PatchRequest struct {
	Observation string
	Name        string
	Description string
	Body        string
}

// PatchAnswer is the decoded answer. An empty list is a valid answer: the
// model found nothing to change.
type PatchAnswer struct {
	Edits []Edit `json:"edits"`
}

// Prompt renders the request.
func (r PatchRequest) Prompt() string {
	var b strings.Builder
	b.WriteString("You maintain a project's long-lived memory notes. Fold one observation into one existing note as the smallest set of exact text edits.\n\n")
	b.WriteString("Rules:\n")
	b.WriteString("- Never rewrite the note. Return edits only; every other character stays as it is.\n")
	b.WriteString("- `old` must be copied exactly from the note and appear in it exactly once; edits must not overlap.\n")
	b.WriteString("- To add text, anchor on a nearby line: put that line in `old` and repeat it in `new` with the addition.\n")
	b.WriteString("- Keep every fact the note already states unless the observation shows it is wrong.\n")
	b.WriteString("- If the note needs no change, return an empty list.\n\n")
	b.WriteString("Reply with one JSON object and nothing else, in this schema:\n")
	b.WriteString(PatchSchema)
	b.WriteString("\n\n## Observation\n")
	b.WriteString(strings.TrimSpace(r.Observation))
	fmt.Fprintf(&b, "\n\n## Note %s\n### description\n%s\n### body\n%s\n", r.Name, r.Description, r.Body)
	return b.String()
}

// ParsePatch decodes a model's text and validates it against the note.
func ParsePatch(text string, req PatchRequest) (PatchAnswer, error) {
	var a PatchAnswer
	if err := decodeStrict(text, &a); err != nil {
		return PatchAnswer{}, err
	}
	if a.Edits == nil {
		return PatchAnswer{}, invalid("missing \"edits\"")
	}
	if _, _, err := ApplyEdits(req.Body, req.Description, a.Edits); err != nil {
		return PatchAnswer{}, err
	}
	return a, nil
}

// Patch runs one patch call. The answer is validated against the given text; an
// error wrapping ErrInvalid means it was dropped.
func Patch(ctx context.Context, m Model, req PatchRequest) (PatchAnswer, Reply, error) {
	rep, err := m.Call(ctx, req.Prompt())
	if err != nil {
		return PatchAnswer{}, rep, err
	}
	a, err := ParsePatch(rep.Text, req)
	return a, rep, err
}

// ApplyEdits is the deterministic validation of decision 6: every edit is
// complete, each old text occurs exactly once in its field (an old that
// overlaps itself, "aa" in "aaa", counts as more than once, as the store's
// memory_patch does), edits do not overlap, and all are matched against the
// same original and applied together. It returns the new body and description.
func ApplyEdits(body, description string, edits []Edit) (string, string, error) {
	if len(edits) > MaxEdits {
		return "", "", invalid("%d edits, at most %d", len(edits), MaxEdits)
	}
	type span struct{ lo, hi, idx int }
	spans := map[string][]span{}
	texts := map[string]string{"body": body, "description": description}
	for i, e := range edits {
		f := e.Field
		if f == "" {
			f = "body"
		}
		text, ok := texts[f]
		if !ok {
			return "", "", invalid("edit %d: field %q is not body or description", i, e.Field)
		}
		if e.Old == nil || e.New == nil {
			return "", "", invalid("edit %d: old and new are both required", i)
		}
		if *e.Old == "" {
			return "", "", invalid("edit %d: old is empty", i)
		}
		if *e.Old == *e.New {
			return "", "", invalid("edit %d: old equals new", i)
		}
		switch n := countOverlapping(text, *e.Old); n {
		case 1:
		case 0:
			return "", "", invalid("edit %d: old does not occur in the %s", i, f)
		default:
			return "", "", invalid("edit %d: old occurs %d times in the %s", i, n, f)
		}
		lo := strings.Index(text, *e.Old)
		spans[f] = append(spans[f], span{lo, lo + len(*e.Old), i})
	}
	out := map[string]string{"body": body, "description": description}
	for f, ss := range spans {
		sort.Slice(ss, func(a, b int) bool { return ss[a].lo < ss[b].lo })
		for k := 1; k < len(ss); k++ {
			if ss[k].lo < ss[k-1].hi {
				return "", "", invalid("edits %d and %d overlap in the %s", ss[k-1].idx, ss[k].idx, f)
			}
		}
		var b strings.Builder
		prev := 0
		for _, s := range ss {
			b.WriteString(texts[f][prev:s.lo])
			b.WriteString(*edits[s.idx].New)
			prev = s.hi
		}
		b.WriteString(texts[f][prev:])
		out[f] = b.String()
	}
	return out["body"], out["description"], nil
}

func countOverlapping(s, sub string) int {
	n := 0
	for i := 0; ; {
		j := strings.Index(s[i:], sub)
		if j < 0 {
			return n
		}
		n++
		i += j + 1
	}
}
