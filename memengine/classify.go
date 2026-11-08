package memengine

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// Operation is the answer of a classification call.
type Operation string

const (
	OpAdd       Operation = "add"
	OpPatch     Operation = "patch"
	OpSupersede Operation = "supersede"
	OpMerge     Operation = "merge"
	OpNoop      Operation = "no-op"
)

// Operations lists the answers the schema allows.
var Operations = []Operation{OpAdd, OpPatch, OpSupersede, OpMerge, OpNoop}

// Escalates reports whether the small tier must hand this answer to the larger
// tier (or to the member as a plain observation): ADR 0130 decision 6 sends
// supersede and merge up, the cases Mem0's update router lost. A conservative
// rule, not a measured one; the evaluation reports both tiers on those cases.
func (o Operation) Escalates() bool { return o == OpSupersede || o == OpMerge }

// IsChange reports whether the operation would write to the store.
func (o Operation) IsChange() bool { return o != OpNoop }

// Snippet is what a classification call sees of one existing memory.
type Snippet struct {
	Name        string
	Description string
	Body        string
}

// MaxSnippets bounds how many memories of the topic one classification call
// sees; the call is meant to stay near 3k input tokens.
const MaxSnippets = 4

// SnippetRunes caps the body excerpt per memory.
const SnippetRunes = 600

// ClassifySchema is the fixed answer schema, shown to the model verbatim and
// enforced by ParseClassify.
const ClassifySchema = `{
  "operation": "add | patch | supersede | merge | no-op",
  "targets": ["names of existing memories the operation acts on"],
  "reason": "one sentence"
}`

// ClassifyRequest is one observation and the memories of its topic.
type ClassifyRequest struct {
	Observation string
	Memories    []Snippet
}

// ClassifyAnswer is the validated answer.
type ClassifyAnswer struct {
	Operation Operation `json:"operation"`
	Targets   []string  `json:"targets"`
	Reason    string    `json:"reason"`
}

// Prompt renders the request. The wording is part of what is measured: change
// it and the gold-set numbers no longer describe the engine.
func (r ClassifyRequest) Prompt() string {
	var b strings.Builder
	b.WriteString("You maintain a project's long-lived memory notes. Decide what one new observation should do to the existing notes of its topic.\n\n")
	b.WriteString("Answer with exactly one of:\n")
	b.WriteString("- add: the observation is a durable lesson no existing note holds.\n")
	b.WriteString("- patch: one existing note holds the topic and needs a small edit (a missing fact, a wrong line).\n")
	b.WriteString("- supersede: one existing note is mostly out of date and a replacement should take its place.\n")
	b.WriteString("- merge: two or more existing notes (or this observation and another) say the same thing and should become one.\n")
	b.WriteString("- no-op: nothing should change (the fact lives in the code or docs, is already recorded, is a one-off, or the note is already right).\n\n")
	b.WriteString("Reply with one JSON object and nothing else, in this schema:\n")
	b.WriteString(ClassifySchema)
	b.WriteString("\n`targets` is empty for add and no-op, one name for patch and supersede, and every note involved for merge.\n\n")
	b.WriteString("## Observation\n")
	b.WriteString(strings.TrimSpace(r.Observation))
	b.WriteString("\n\n## Existing notes of the topic\n")
	if len(r.Memories) == 0 {
		b.WriteString("(none)\n")
	}
	for _, m := range r.Memories {
		fmt.Fprintf(&b, "\n### %s\ndescription: %s\n%s\n", m.Name, m.Description, excerpt(m.Body, SnippetRunes))
	}
	return b.String()
}

func excerpt(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return strings.TrimSpace(s)
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n])) + " …"
}

// ParseClassify validates a model's text against the schema and the request:
// a known operation, targets that name memories the call was shown, and a
// target count that fits the operation.
func ParseClassify(text string, req ClassifyRequest) (ClassifyAnswer, error) {
	var a ClassifyAnswer
	if err := decodeStrict(text, &a); err != nil {
		return ClassifyAnswer{}, err
	}
	if !slices.Contains(Operations, a.Operation) {
		return ClassifyAnswer{}, invalid("operation %q is not one of the schema's", a.Operation)
	}
	shown := make(map[string]bool, len(req.Memories))
	for _, m := range req.Memories {
		shown[m.Name] = true
	}
	seen := map[string]bool{}
	for _, t := range a.Targets {
		if !shown[t] {
			return ClassifyAnswer{}, invalid("target %q is not a note this call was shown", t)
		}
		if seen[t] {
			return ClassifyAnswer{}, invalid("target %q is listed twice", t)
		}
		seen[t] = true
	}
	switch a.Operation {
	case OpAdd, OpNoop:
		if len(a.Targets) != 0 {
			return ClassifyAnswer{}, invalid("%s takes no targets", a.Operation)
		}
	case OpPatch, OpSupersede:
		if len(a.Targets) != 1 {
			return ClassifyAnswer{}, invalid("%s takes exactly one target, got %d", a.Operation, len(a.Targets))
		}
	case OpMerge:
		// An observation-only merge (the other half is not stored yet) has no
		// stored target, so any count up to what was shown is allowed.
	}
	return a, nil
}

// Classify runs one classification call. A non-nil error wrapping ErrInvalid
// means the answer was dropped; any other error is a failed call.
func Classify(ctx context.Context, m Model, req ClassifyRequest) (ClassifyAnswer, Reply, error) {
	rep, err := m.Call(ctx, req.Prompt())
	if err != nil {
		return ClassifyAnswer{}, rep, err
	}
	a, err := ParseClassify(rep.Text, req)
	return a, rep, err
}
