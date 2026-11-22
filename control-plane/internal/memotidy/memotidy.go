// Package memotidy validates what a tidy run's model may do to the messages of a queue (ADR 0137
// section 8): merge, split, move and keep. It is pure. It knows nothing of the store or the wire,
// and one violation refuses the whole reply, because a partial tidy would leave the person with a
// state nobody chose.
package memotidy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Bounds. All are provisional (ADR 0137 open question 1), and each lives here and nowhere else.
const (
	// MaxOperations is the most operations one reply may hold.
	MaxOperations = 80
	// MinMergeSources and MaxMergeSources bound the ids of a merge.
	MinMergeSources = 2
	MaxMergeSources = 10
	// MinSplitParts and MaxSplitParts bound the texts of a split.
	MinSplitParts = 2
	MaxSplitParts = 5
	// MaxTextBytes bounds one result text.
	MaxTextBytes = 16 << 10
	// GrowthSlackBytes is how much longer the results of a merge or a split may be than their sources.
	GrowthSlackBytes = 1 << 10
	// MaxInputItems bounds the messages one run reads.
	MaxInputItems = 40
	// MaxDestinations bounds the categories one run is offered.
	MaxDestinations = 100
	// DailyCap is the most automatic runs a queue reserves in 24 hours, every outcome counted.
	// Manual runs are never refused by it and are not counted.
	DailyCap = 10
	// EnvelopePrefix is what no result text may start with: it is the opening of the line the
	// Agent puts before a delivery, and a result that starts with it could pass for one.
	EnvelopePrefix = "[agent-fleet:"
)

// Operation names.
const (
	OpMerge = "merge"
	OpSplit = "split"
	OpMove  = "move"
	OpKeep  = "keep"
)

// Refusal codes. They end up in a run's reason, so they are short and stable.
const (
	CodeBadShape       = "bad_shape"
	CodeTooManyOps     = "too_many_operations"
	CodeUnknownID      = "unknown_id"
	CodeDuplicateID    = "duplicate_id"
	CodeMergeSources   = "merge_sources"
	CodeSplitParts     = "split_parts"
	CodeNotMergeable   = "not_mergeable"
	CodeMixedGroup     = "mixed_group"
	CodeBadText        = "bad_text"
	CodeTextTooLong    = "text_too_long"
	CodeEnvelope       = "text_envelope"
	CodeGrowth         = "text_growth"
	CodeBadDestination = "bad_destination"
	CodeMoveSelf       = "move_self"
	CodeDuplicateRef   = "duplicate_ref"
	CodeAlreadyMoved   = "already_moved"
	CodeKeepOnly       = "keep_only"
)

// Op is one operation, as the model writes it. Only the fields of its kind may be set.
type Op struct {
	Op    string   `json:"op"`
	IDs   []string `json:"ids,omitempty"`
	ID    string   `json:"id,omitempty"`
	Text  string   `json:"text,omitempty"`
	Texts []string `json:"texts,omitempty"`
	To    string   `json:"to,omitempty"`
}

// Group is what messages must share to be merged: the provenance of their origin (a derived
// message carries its sources' provenance), the producer and the producer origin.
type Group struct {
	Provenance, Producer, ProducerOrigin string
	// Recipe is the encoded run recipe the message resolves to under its queue's settings, so two
	// messages that would run differently are never merged (ADR 0137, addendum 2026-11-23).
	Recipe string
}

// Item is one message the run read.
type Item struct {
	ID    string
	Group Group
	// Mergeable is a plain text note: no file, no attachment, no reference.
	Mergeable bool
	// Bytes is the length of its text.
	Bytes int
	// Moved is a message a run already moved: no run moves it again.
	Moved bool
	// KeepOnly is a message that has or is a prerequisite of another (ADR 0137, addendum
	// 2026-11-19): a run may only keep it, never merge, split or move it.
	KeepOnly bool
	// Ref is the identity of its reference (memoref.Ident), empty for none.
	Ref string
}

// Destination is a category a message may be moved to, as the model was shown it.
type Destination struct {
	ID string
	// OpenRefs is the set of reference identities of the open messages already there.
	OpenRefs map[string]bool
}

// Request is everything Validate decides from.
type Request struct {
	// Self is the category id of the queue being tidied.
	Self         string
	Items        []Item
	Destinations []Destination
	Ops          []Op
}

// Refusal is why a reply was refused. Code is for the run's reason, Msg for a log.
type Refusal struct {
	Code, Msg string
}

func (r *Refusal) Error() string { return r.Code + ": " + r.Msg }

func refuse(code, format string, a ...any) *Refusal {
	return &Refusal{Code: code, Msg: fmt.Sprintf(format, a...)}
}

// Parse reads the operations of a reply strictly: unknown fields and anything after the value are
// refused, and so is an operation kind that is not one of the four.
func Parse(raw []byte) ([]Op, *Refusal) {
	var in struct {
		Operations []Op `json:"operations"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return nil, refuse(CodeBadShape, "not a reply of the agreed shape")
	}
	if dec.More() {
		return nil, refuse(CodeBadShape, "text after the reply")
	}
	if in.Operations == nil {
		return nil, refuse(CodeBadShape, "operations is missing")
	}
	return in.Operations, nil
}

// Validate accepts or refuses a whole reply. An input id that no operation names is kept, so an
// empty list of operations is valid and changes nothing.
func Validate(req Request) *Refusal {
	if len(req.Ops) > MaxOperations {
		return refuse(CodeTooManyOps, "%d operations, at most %d", len(req.Ops), MaxOperations)
	}
	items := make(map[string]Item, len(req.Items))
	for _, it := range req.Items {
		items[it.ID] = it
	}
	dests := make(map[string]Destination, len(req.Destinations))
	for _, d := range req.Destinations {
		dests[d.ID] = d
	}
	named := map[string]bool{}
	use := func(id string) *Refusal {
		if _, ok := items[id]; !ok {
			return refuse(CodeUnknownID, "an id that was not in the input")
		}
		if named[id] {
			return refuse(CodeDuplicateID, "an id in two operations")
		}
		named[id] = true
		return nil
	}
	// moved tracks the references this reply already puts into each destination, so two moves of
	// one reference into one queue are refused as well.
	moved := map[string]map[string]bool{}
	for _, op := range req.Ops {
		if r := checkShape(op); r != nil {
			return r
		}
		switch op.Op {
		case OpMerge:
			if len(op.IDs) < MinMergeSources || len(op.IDs) > MaxMergeSources {
				return refuse(CodeMergeSources, "a merge has %d to %d sources", MinMergeSources, MaxMergeSources)
			}
			total := 0
			var group Group
			for i, id := range op.IDs {
				if r := use(id); r != nil {
					return r
				}
				it := items[id]
				if it.KeepOnly {
					return refuse(CodeKeepOnly, "a source has or is a prerequisite")
				}
				if !it.Mergeable {
					return refuse(CodeNotMergeable, "a source is not a plain text note")
				}
				if i == 0 {
					group = it.Group
				} else if it.Group != group {
					return refuse(CodeMixedGroup, "sources of different provenance or producer")
				}
				total += it.Bytes
			}
			text, r := checkText(op.Text)
			if r != nil {
				return r
			}
			if len(text) > total+GrowthSlackBytes {
				return refuse(CodeGrowth, "a merge is longer than its sources together plus %d bytes", GrowthSlackBytes)
			}
		case OpSplit:
			if len(op.Texts) < MinSplitParts || len(op.Texts) > MaxSplitParts {
				return refuse(CodeSplitParts, "a split has %d to %d parts", MinSplitParts, MaxSplitParts)
			}
			if r := use(op.ID); r != nil {
				return r
			}
			src := items[op.ID]
			if src.KeepOnly {
				return refuse(CodeKeepOnly, "the source has or is a prerequisite")
			}
			if !src.Mergeable {
				return refuse(CodeNotMergeable, "the source is not a plain text note")
			}
			total := 0
			for _, t := range op.Texts {
				text, r := checkText(t)
				if r != nil {
					return r
				}
				total += len(text)
			}
			if total > src.Bytes+GrowthSlackBytes {
				return refuse(CodeGrowth, "the parts of a split are longer than their source plus %d bytes", GrowthSlackBytes)
			}
		case OpMove:
			if r := use(op.ID); r != nil {
				return r
			}
			it := items[op.ID]
			if op.To == req.Self {
				return refuse(CodeMoveSelf, "a move to the queue itself")
			}
			d, ok := dests[op.To]
			if !ok {
				return refuse(CodeBadDestination, "a destination that was not offered")
			}
			if it.Moved {
				return refuse(CodeAlreadyMoved, "a message a run already moved")
			}
			if it.KeepOnly {
				return refuse(CodeKeepOnly, "a message that has or is a prerequisite")
			}
			if it.Ref != "" {
				if d.OpenRefs[it.Ref] || moved[d.ID][it.Ref] {
					return refuse(CodeDuplicateRef, "the destination already holds an open message with this reference")
				}
				if moved[d.ID] == nil {
					moved[d.ID] = map[string]bool{}
				}
				moved[d.ID][it.Ref] = true
			}
		case OpKeep:
			if r := use(op.ID); r != nil {
				return r
			}
		}
	}
	return nil
}

// checkShape refuses an operation that sets a field its kind does not have, or has no kind.
func checkShape(op Op) *Refusal {
	bad := func() *Refusal { return refuse(CodeBadShape, "an operation with fields of another kind") }
	switch op.Op {
	case OpMerge:
		if op.ID != "" || op.Texts != nil || op.To != "" {
			return bad()
		}
	case OpSplit:
		if op.IDs != nil || op.Text != "" || op.To != "" {
			return bad()
		}
	case OpMove:
		if op.IDs != nil || op.Text != "" || op.Texts != nil || op.To == "" {
			return bad()
		}
	case OpKeep:
		if op.IDs != nil || op.Text != "" || op.Texts != nil || op.To != "" {
			return bad()
		}
	default:
		return refuse(CodeBadShape, "an unknown operation")
	}
	return nil
}

// CleanText is the text a message is stored with: trimmed, as a message the member writes is.
func CleanText(s string) string { return strings.TrimSpace(s) }

// checkText checks one result text and returns it as it is stored.
func checkText(s string) (string, *Refusal) {
	t := CleanText(s)
	switch {
	case t == "":
		return "", refuse(CodeBadText, "an empty text")
	case !utf8.ValidString(t) || strings.ContainsRune(t, 0):
		return "", refuse(CodeBadText, "a text that is not valid UTF-8 or holds a NUL")
	case len(t) > MaxTextBytes:
		return "", refuse(CodeTextTooLong, "a text over %d bytes", MaxTextBytes)
	case strings.HasPrefix(t, EnvelopePrefix):
		return "", refuse(CodeEnvelope, "a text that starts with %q", EnvelopePrefix)
	}
	return t, nil
}
