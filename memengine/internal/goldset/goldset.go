// Package goldset loads the member-labelled gold set, runs a model tier over
// it, and scores the answers per operation (ADR 0130 decision 6).
//
// The gold set and the memory bodies it cites are private and live outside the
// repository; this package only reads them from paths it is given.
package goldset

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/k-k1/memengine"
)

// Label is the member's judgement of a case.
type Label struct {
	Operation memengine.Operation `json:"operation"`
	OK        string              `json:"ok"`
	Notes     string              `json:"notes"`
}

// RefEdit is an edit of the reference patch the case was proposed with. Target
// names the memory when the case cites more than one and the edit says so.
type RefEdit struct {
	Field  string `json:"field"`
	Old    string `json:"old"`
	New    string `json:"new"`
	Target string `json:"target"`
}

// MemRef is how a case cites a memory.
type MemRef struct {
	Name     string `json:"name"`
	Scope    string `json:"scope"`
	Revision int    `json:"revision"`
}

// Case is one line of the gold set. Only the fields the evaluation reads are
// declared; the proposal's own verdict (proposed_operation, reason,
// confidence) is deliberately not loaded: the member's label is the truth.
type Case struct {
	ID          string    `json:"id"`
	Observation string    `json:"observation"`
	Memories    []MemRef  `json:"memories"`
	Edits       []RefEdit `json:"edits"`
	MustSurvive []string  `json:"must_survive"`
	Label       Label     `json:"label"`
}

// Memory is a memory as read from the store when the inputs were built.
type Memory struct {
	Name             string `json:"name"`
	Scope            string `json:"scope"`
	Revision         int    `json:"revision"`
	CitedRevision    int    `json:"cited_revision"`
	RevisionMismatch bool   `json:"revision_mismatch"`
	Description      string `json:"description"`
	Body             string `json:"body"`
	Truncated        bool   `json:"truncated"`
}

// Input holds the memories a case cites, with the revision actually read.
type Input struct {
	ID       string   `json:"id"`
	Memories []Memory `json:"memories"`
}

// Set is the gold set joined with its inputs.
type Set struct {
	// GoldHash identifies the gold-set version: the first 12 hex digits of the
	// SHA-256 of cases.jsonl. A run made against another hash is not comparable.
	GoldHash string
	Cases    []Case
	Inputs   map[string]Input
}

// Split names a deterministic half of the gold set. The half a case belongs to
// is a function of its id alone (the low bit of the first byte of
// SHA-256(id)), so it survives additions to the set and cannot be chosen by
// looking at a score: prompt changes are iterated on SplitTune and judged on
// SplitHeldOut.
const (
	SplitAll     = "all"
	SplitTune    = "tune"
	SplitHeldOut = "heldout"
)

// SplitOf returns SplitTune or SplitHeldOut for a case id.
func SplitOf(id string) string {
	if sha256.Sum256([]byte(id))[0]&1 == 0 {
		return SplitTune
	}
	return SplitHeldOut
}

// ValidSplit reports whether name is one of the three accepted values.
func ValidSplit(name string) bool {
	return name == SplitAll || name == SplitTune || name == SplitHeldOut
}

// InSplit reports whether a case id belongs to the named split.
func InSplit(id, split string) bool { return split == "" || split == SplitAll || SplitOf(id) == split }

// Only keeps the cases of one split. GoldHash is unchanged: it identifies the
// whole file, not the half.
func (s *Set) Only(split string) {
	var kept []Case
	for _, c := range s.Cases {
		if InSplit(c.ID, split) {
			kept = append(kept, c)
		}
	}
	s.Cases = kept
}

// Only returns a copy of the run restricted to the cases of one split, so one
// full run can be reported per half. Baseline and provenance are kept.
func (r *TierRun) Only(split string) *TierRun {
	c := *r
	c.Classify, c.Patch = nil, nil
	for _, o := range r.Classify {
		if InSplit(o.Case, split) {
			c.Classify = append(c.Classify, o)
		}
	}
	for _, o := range r.Patch {
		if InSplit(o.Case, split) {
			c.Patch = append(c.Patch, o)
		}
	}
	return &c
}

func readJSONL[T any](path string) ([]T, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []T
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var v T
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		out = append(out, v)
	}
	return out, sc.Err()
}

// Load reads the gold set and the inputs file and checks that every case has
// its inputs and every cited memory was read.
func Load(casesPath, inputsPath string) (*Set, error) {
	cases, err := readJSONL[Case](casesPath)
	if err != nil {
		return nil, err
	}
	ins, err := readJSONL[Input](inputsPath)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(casesPath)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	s := &Set{GoldHash: hex.EncodeToString(sum[:])[:12], Cases: cases, Inputs: map[string]Input{}}
	for _, in := range ins {
		s.Inputs[in.ID] = in
	}
	for _, c := range cases {
		in, ok := s.Inputs[c.ID]
		if !ok {
			return nil, fmt.Errorf("case %s has no inputs", c.ID)
		}
		if len(in.Memories) != len(c.Memories) {
			return nil, fmt.Errorf("case %s cites %d memories, inputs hold %d", c.ID, len(c.Memories), len(in.Memories))
		}
	}
	return s, nil
}

// Snippets renders the memories of a case for a classification call.
func (s *Set) Snippets(c Case) []memengine.Snippet {
	var out []memengine.Snippet
	for _, m := range s.Inputs[c.ID].Memories {
		out = append(out, memengine.Snippet{Name: m.Name, Description: m.Description, Body: m.Body})
	}
	return out
}

// PatchTarget returns the memory a patch call for this case works on: the one
// a reference edit names, else the one in which the reference edits apply.
// ok is false for a case without reference edits or without such a memory.
func (s *Set) PatchTarget(c Case) (Memory, bool) {
	if c.Label.Operation != memengine.OpPatch || len(c.Edits) == 0 {
		return Memory{}, false
	}
	ms := s.Inputs[c.ID].Memories
	for _, e := range c.Edits {
		if e.Target != "" {
			for _, m := range ms {
				if m.Name == e.Target {
					return m, true
				}
			}
		}
	}
	for _, m := range ms {
		if refEditsApply(m, c.Edits) {
			return m, true
		}
	}
	return Memory{}, false
}

func refEditsApply(m Memory, edits []RefEdit) bool {
	_, _, err := memengine.ApplyEdits(m.Body, m.Description, toEdits(edits))
	return err == nil
}

func toEdits(rs []RefEdit) []memengine.Edit {
	out := make([]memengine.Edit, len(rs))
	for i := range rs {
		o, n := rs[i].Old, rs[i].New
		out[i] = memengine.Edit{Field: rs[i].Field, Old: &o, New: &n}
	}
	return out
}

// ConditionedTarget is the memory a gold-conditioned patch call works on for a
// case the member labelled supersede or merge: the first memory it cites.
// Such a call has no reference edits, so only its validity and the facts it
// keeps can be scored.
func (s *Set) ConditionedTarget(c Case) (Memory, bool) {
	if c.Label.Operation != memengine.OpSupersede && c.Label.Operation != memengine.OpMerge {
		return Memory{}, false
	}
	ms := s.Inputs[c.ID].Memories
	if len(ms) == 0 {
		return Memory{}, false
	}
	return ms[0], true
}
