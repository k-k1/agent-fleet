package memreview

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memoryx"
)

// The operations a reply may carry. "skill" operations arrive with phase P4.
const (
	OpCreate = "create"
	OpUpdate = "update"
	OpForget = "forget"
)

// Op is one validated proposal. BaseRevision is the revision of the target in the input the
// helper was shown (0 for a create); applying it later must carry that revision, so a memory
// edited since the review turns the proposal stale instead of being overwritten.
type Op struct {
	Op           string `json:"op"`
	Scope        string `json:"scope"`
	Name         string `json:"name"`
	Type         string `json:"type,omitempty"`
	Description  string `json:"description,omitempty"`
	Body         string `json:"body,omitempty"`
	Reason       string `json:"reason"`
	Evidence     []int  `json:"evidence"`
	BaseRevision int    `json:"baseRevision,omitempty"`
}

// ReplyError is why a reply was dropped. Detail names a field or a rule and never carries a value
// of the reply, so the error can be logged, stored in a run record and shown.
type ReplyError struct {
	Kind   string // ReplyMalformed, ReplyOversize, ReplySecret
	Detail string
}

// The kinds of ReplyError.
const (
	ReplyMalformed = "malformed"
	ReplyOversize  = "oversize"
	ReplySecret    = "secret"
)

func (e *ReplyError) Error() string { return "review reply dropped (" + e.Kind + "): " + e.Detail }

func malformed(format string, args ...any) error {
	return &ReplyError{Kind: ReplyMalformed, Detail: fmt.Sprintf(format, args...)}
}

// maxEvidence and maxReasonChars bound the small fields.
const (
	maxEvidence    = 10
	maxReasonChars = 300
)

// ParseReply validates the helper's raw reply against the input it was built from (ADR 0117
// decision 4). A reply that does not meet the contract is dropped whole, as an error; nothing
// is repaired and nothing is salvaged from a partly good reply.
//
// Checked: the size caps; strict JSON of the documented shape with no unknown field; at most
// MaxOps operations; per operation the store's own limits for the memory (names, description,
// type, body), the text cap, evidence inside the window, a create that does not collide with any
// memory the session could see, and an update or forget of a memory that was shown. Then every
// string of every field is secret-scanned, the same rule as the input's. Two operations on one
// target collapse to the last.
func ParseReply(raw string, in Input) ([]Op, error) {
	if len(raw) > ReplyBytes {
		return nil, &ReplyError{Kind: ReplyOversize, Detail: fmt.Sprintf("reply is larger than %d bytes", ReplyBytes)}
	}
	var env struct {
		Ops *[]rawOp `json:"ops"`
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(stripFence(raw))))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&env); err != nil {
		return nil, malformed("not the documented JSON object (%s)", jsonProblem(err))
	}
	// More() only peeks for a value; the stream must be at its end, whatever follows.
	if _, err := dec.Token(); err != io.EOF {
		return nil, malformed("text after the JSON object")
	}
	if env.Ops == nil {
		return nil, malformed("no ops field")
	}
	if len(*env.Ops) > MaxOps {
		return nil, &ReplyError{Kind: ReplyOversize, Detail: fmt.Sprintf("more than %d operations", MaxOps)}
	}
	var ops []Op
	for i, r := range *env.Ops {
		op, err := r.validate(i, in)
		if err != nil {
			return nil, err
		}
		ops = append(ops, op)
	}
	return collapse(ops), nil
}

// rawOp is an operation as decoded.
type rawOp struct {
	Op          string `json:"op"`
	Scope       string `json:"scope"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Body        string `json:"body"`
	Reason      string `json:"reason"`
	Evidence    []int  `json:"evidence"`
}

func (r rawOp) validate(i int, in Input) (Op, error) {
	at := func(field string) string { return fmt.Sprintf("op %d: %s", i+1, field) }
	op := Op{
		Op: strings.TrimSpace(r.Op), Scope: strings.TrimSpace(r.Scope), Name: strings.TrimSpace(r.Name),
		Type: strings.TrimSpace(r.Type), Description: strings.TrimSpace(r.Description),
		Body: strings.TrimSpace(r.Body), Reason: strings.TrimSpace(r.Reason), Evidence: r.Evidence,
	}
	if op.Op != OpCreate && op.Op != OpUpdate && op.Op != OpForget {
		return Op{}, malformed("%s", at("op must be create, update or forget"))
	}
	if op.Scope != "project" && op.Scope != "user" {
		return Op{}, malformed("%s", at("scope must be project or user"))
	}
	if !memoryx.ValidReviewName(op.Name) {
		return Op{}, malformed("%s", at("name is not a valid memory name"))
	}
	if op.Reason == "" || strings.ContainsAny(op.Reason, "\r\n") || utf8.RuneCountInString(op.Reason) > maxReasonChars {
		return Op{}, malformed("%s", at(fmt.Sprintf("reason must be one line of at most %d characters", maxReasonChars)))
	}
	if n := len(op.Evidence); n == 0 || n > maxEvidence {
		return Op{}, malformed("%s", at(fmt.Sprintf("evidence must cite 1 to %d turns", maxEvidence)))
	}
	for _, e := range op.Evidence {
		if !in.Numbers[e] {
			return Op{}, malformed("%s", at("evidence cites a turn the excerpt does not show"))
		}
	}
	key := MemoryKey(op.Scope, op.Name)
	rev, known := in.Known[key]
	switch op.Op {
	case OpCreate:
		if known {
			return Op{}, malformed("%s", at("create over a memory that exists"))
		}
	case OpUpdate:
		if !known || !in.Full[key] {
			return Op{}, malformed("%s", at("the target's whole text was not shown, so it cannot be replaced"))
		}
		op.BaseRevision = rev
	case OpForget:
		if !known || !in.Listed[key] {
			return Op{}, malformed("%s", at("the target was not among the memories shown"))
		}
		op.BaseRevision = rev
	}
	if op.Op == OpForget {
		if op.Type != "" || op.Description != "" || op.Body != "" {
			return Op{}, malformed("%s", at("forget carries no memory text"))
		}
	} else {
		if len(op.Body) > OpTextBytes {
			return Op{}, &ReplyError{Kind: ReplyOversize, Detail: at(fmt.Sprintf("body is larger than %d bytes", OpTextBytes))}
		}
		if err := memoryx.ValidateReviewMemory(op.Scope, op.Name, op.Description, op.Type, nil, op.Body); err != nil {
			return Op{}, malformed("%s", at(err.Error()))
		}
	}
	// Every field, not only the body (ADR 0117 decision 2): reason and evidence reach the
	// pending queue and the Console as well.
	for _, f := range []struct{ name, val string }{{"op", op.Op}, {"scope", op.Scope}, {"name", op.Name},
		{"type", op.Type}, {"description", op.Description}, {"body", op.Body}, {"reason", op.Reason}} {
		if p := memoryx.ReviewTextProblem(f.val); p != "" {
			return Op{}, &ReplyError{Kind: ReplySecret, Detail: at(f.name + " matches the secret rule " + p)}
		}
	}
	return op, nil
}

// collapse keeps the last of several operations on one target, in the order the survivors had.
func collapse(ops []Op) []Op {
	last := map[string]int{}
	for i, o := range ops {
		last[MemoryKey(o.Scope, o.Name)] = i
	}
	var out []Op
	for i, o := range ops {
		if last[MemoryKey(o.Scope, o.Name)] == i {
			out = append(out, o)
		}
	}
	return out
}

// stripFence removes one surrounding Markdown code fence, which models add unasked. Nothing else
// is tolerated.
func stripFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") || !strings.HasSuffix(s, "```") || len(s) < 6 {
		return s
	}
	s = strings.TrimSuffix(strings.TrimPrefix(s, "```"), "```")
	if nl := strings.IndexByte(s, '\n'); nl >= 0 && !strings.ContainsAny(s[:nl], "{[") {
		s = s[nl+1:] // the language tag line
	}
	return strings.TrimSpace(s)
}

// jsonProblem describes a decode error without quoting the reply: the standard errors can carry
// a fragment of the input (an unknown field's name, an offending value).
func jsonProblem(err error) string {
	var syn *json.SyntaxError
	var typ *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syn):
		return fmt.Sprintf("syntax error at byte %d", syn.Offset)
	case errors.As(err, &typ):
		return "wrong type for field " + typ.Field
	case strings.HasPrefix(err.Error(), "json: unknown field"):
		return "unknown field"
	}
	return "invalid JSON"
}
