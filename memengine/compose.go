package memengine

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// ComposeSchema is the fixed answer schema of a compose call, shown to the model verbatim.
const ComposeSchema = `{
  "name": "new note name: lower-case words joined by hyphens, not the name of any note shown",
  "description": "one line saying what the note holds and when to read it",
  "type": "user | feedback | project | reference",
  "body": "the whole text of the new note"
}`

// Bounds of a composed note. A composed note is a replacement or a first draft, so it is bounded
// by what the store accepts for a memory, not by the size of an edit.
const (
	MaxComposeBody        = 16 << 10
	MaxComposeDescription = 300
	MaxComposeSources     = 8
)

var composeNameRe = regexp.MustCompile(`^[a-z0-9_][a-z0-9._-]{0,63}$`)

// Note is one existing note shown to a compose call, in full: the replacement has to keep every fact
// of the notes it replaces, so an excerpt would lose some.
type Note struct {
	Name        string
	Description string
	Body        string
}

// ComposeRequest is an observation and the notes the new note takes the place of. Sources is empty
// for an add, one note for a supersede, two or more for a merge.
type ComposeRequest struct {
	Operation   Operation
	Observation string
	Sources     []Note
}

// ComposeAnswer is the decoded new note.
type ComposeAnswer struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Type        string `json:"type"`
	Body        string `json:"body"`
}

// Prompt renders the request. It is the only call of the engine that asks the model for a whole
// note instead of an edit: a supersede, a merge and an add have no note to edit.
func (r ComposeRequest) Prompt() string {
	var b strings.Builder
	b.WriteString("You maintain a project's long-lived memory notes. Write one new note.\n\n")
	switch r.Operation {
	case OpSupersede:
		b.WriteString("The shown note is mostly out of date. Write the note that takes its place: keep every fact of it that is still right, and change what the observation shows is wrong.\n")
	case OpMerge:
		b.WriteString("The shown notes say the same thing in several places. Write one note that replaces all of them: keep every distinct fact of each, say each fact once, and apply what the observation adds.\n")
	default:
		b.WriteString("No shown note holds the lesson in the observation. Write a note that records it as a durable, reusable rule, procedure or pitfall.\n")
	}
	b.WriteString("\nRules:\n")
	b.WriteString("- Use only facts that are in the observation or the shown notes. Do not invent paths, commands, names or numbers.\n")
	b.WriteString("- Copy a `[[link]]` only if the shown notes already contain it.\n")
	b.WriteString("- The name is new: lower-case words joined by hyphens, different from every shown note's name.\n")
	b.WriteString("- Never include a secret, a token, a password or a private host name.\n\n")
	b.WriteString("Reply with one JSON object and nothing else, in this schema:\n")
	b.WriteString(ComposeSchema)
	b.WriteString("\n\n## Observation\n")
	b.WriteString(strings.TrimSpace(r.Observation))
	b.WriteString("\n\n## Notes to replace\n")
	if len(r.Sources) == 0 {
		b.WriteString("(none)\n")
	}
	for _, n := range r.Sources {
		fmt.Fprintf(&b, "\n### %s\ndescription: %s\n%s\n", n.Name, n.Description, n.Body)
	}
	return b.String()
}

// ParseCompose decodes a model's text and validates it against the request: every field present,
// a name of the store's form that no shown note has, a one-line description, a body within bounds.
// Whether the name is free in the store, the type is one the store takes and the text is clean is
// the adapter's to check against the store itself.
func ParseCompose(text string, req ComposeRequest) (ComposeAnswer, error) {
	var a ComposeAnswer
	if err := decodeStrict(text, &a); err != nil {
		return ComposeAnswer{}, err
	}
	a.Name, a.Description, a.Type = strings.TrimSpace(a.Name), strings.TrimSpace(a.Description), strings.TrimSpace(a.Type)
	switch {
	case len(req.Sources) > MaxComposeSources:
		return ComposeAnswer{}, invalid("more than %d notes to replace", MaxComposeSources)
	case !composeNameRe.MatchString(a.Name):
		return ComposeAnswer{}, invalid("the name is not a note name")
	case a.Description == "" || strings.ContainsAny(a.Description, "\r\n") || utf8.RuneCountInString(a.Description) > MaxComposeDescription:
		return ComposeAnswer{}, invalid("the description is empty, not one line, or too long")
	case a.Type == "" || len(a.Type) > 32:
		return ComposeAnswer{}, invalid("the type is missing")
	case strings.TrimSpace(a.Body) == "" || len(a.Body) > MaxComposeBody:
		return ComposeAnswer{}, invalid("the body is empty or too large")
	}
	for _, n := range req.Sources {
		if n.Name == a.Name {
			return ComposeAnswer{}, invalid("the new note reuses the name of a note it replaces")
		}
	}
	return a, nil
}

// Compose runs one compose call. A non-nil error wrapping ErrInvalid means the answer was dropped;
// any other error is a failed call.
func Compose(ctx context.Context, m Model, req ComposeRequest) (ComposeAnswer, Reply, error) {
	rep, err := m.Call(ctx, req.Prompt())
	if err != nil {
		return ComposeAnswer{}, rep, err
	}
	a, err := ParseCompose(rep.Text, req)
	return a, rep, err
}
