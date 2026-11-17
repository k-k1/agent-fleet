package memotidy

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxReply bounds what is parsed: a bigger answer is not a tidy proposal.
const maxReply = 256 << 10

// Operation is one entry of the reply, in the wire shape the Control Plane validates.
type Operation struct {
	Op    string   `json:"op"`
	IDs   []string `json:"ids,omitempty"`
	ID    string   `json:"id,omitempty"`
	Text  string   `json:"text,omitempty"`
	Texts []string `json:"texts,omitempty"`
	To    string   `json:"to,omitempty"`
}

// rawOp has a pointer for every field so that a field the op does not take can be told from an
// empty one.
type rawOp struct {
	Op    *string   `json:"op"`
	IDs   *[]string `json:"ids"`
	ID    *string   `json:"id"`
	Text  *string   `json:"text"`
	Texts *[]string `json:"texts"`
	To    *string   `json:"to"`
}

type rawReply struct {
	Operations *[]rawOp `json:"operations"`
}

// errMalformed carries no text from the reply: it is logged and answered as the code alone.
var errMalformed = errors.New("the reply is not exactly one operations object of the agreed shape")

// parseReply accepts one JSON object, optionally inside one surrounding code fence, with no
// unknown key and nothing after it, and checks its shape against the limits. Whatever the model
// wrote beyond that is refused whole; no part of the reply is repaired or kept.
func parseReply(reply string, l Limits) ([]Operation, error) {
	if len(reply) > maxReply || !utf8.ValidString(reply) {
		return nil, errMalformed
	}
	body := unfence(strings.TrimSpace(reply))
	dec := json.NewDecoder(strings.NewReader(body))
	dec.DisallowUnknownFields()
	var raw rawReply
	if err := dec.Decode(&raw); err != nil || raw.Operations == nil {
		return nil, errMalformed
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errMalformed
	}
	if len(*raw.Operations) > l.Operations {
		return nil, errMalformed
	}
	ops := make([]Operation, 0, len(*raw.Operations))
	for _, r := range *raw.Operations {
		op, ok := r.operation(l)
		if !ok {
			return nil, errMalformed
		}
		ops = append(ops, op)
	}
	return ops, nil
}

// unfence removes one code fence around s when s is exactly a fenced block, with an optional
// language word on the opening line. Any other text is returned as it is and fails the parse.
func unfence(s string) string {
	if !strings.HasPrefix(s, "```") || !strings.HasSuffix(s, "```") || len(s) < 6 {
		return s
	}
	inner := s[3 : len(s)-3]
	first, rest, found := strings.Cut(inner, "\n")
	if !found {
		return s
	}
	if w := strings.TrimSpace(first); len(w) > 12 || strings.ContainsFunc(w, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		return s
	}
	return strings.TrimSpace(rest)
}

// operation checks one entry: the fields its op takes are present and in bounds, and no other.
func (r rawOp) operation(l Limits) (Operation, bool) {
	if r.Op == nil {
		return Operation{}, false
	}
	switch *r.Op {
	case "merge":
		if r.IDs == nil || r.Text == nil || set(r.ID) || set(r.Texts) || set(r.To) {
			return Operation{}, false
		}
		if n := len(*r.IDs); n < 2 || n > l.MergeSources || !idsOK(*r.IDs) || !textInBounds(*r.Text, l) {
			return Operation{}, false
		}
		return Operation{Op: "merge", IDs: *r.IDs, Text: *r.Text}, true
	case "split":
		if r.ID == nil || r.Texts == nil || set(r.IDs) || set(r.Text) || set(r.To) {
			return Operation{}, false
		}
		if n := len(*r.Texts); !idOK(*r.ID) || n < 2 || n > l.SplitParts {
			return Operation{}, false
		}
		for _, t := range *r.Texts {
			if !textInBounds(t, l) {
				return Operation{}, false
			}
		}
		return Operation{Op: "split", ID: *r.ID, Texts: *r.Texts}, true
	case "move":
		if r.ID == nil || r.To == nil || set(r.IDs) || set(r.Text) || set(r.Texts) {
			return Operation{}, false
		}
		if !idOK(*r.ID) || !idOK(*r.To) {
			return Operation{}, false
		}
		return Operation{Op: "move", ID: *r.ID, To: *r.To}, true
	case "keep":
		if r.ID == nil || set(r.IDs) || set(r.Text) || set(r.Texts) || set(r.To) {
			return Operation{}, false
		}
		if !idOK(*r.ID) {
			return Operation{}, false
		}
		return Operation{Op: "keep", ID: *r.ID}, true
	}
	return Operation{}, false
}

func set[T any](p *T) bool { return p != nil }

func idsOK(ids []string) bool {
	for _, id := range ids {
		if !idOK(id) {
			return false
		}
	}
	return true
}

// textInBounds is the shape of a derived text: 1 byte to the limit. The prefix and the rest of
// the rules are the Control Plane's to judge.
func textInBounds(s string, l Limits) bool {
	return len(s) >= 1 && len(s) <= l.TextBytes
}
