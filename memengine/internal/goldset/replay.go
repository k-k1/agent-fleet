package goldset

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/k-k1/memengine"
)

// PromptLine is one line of prompts.jsonl: a call exactly as the one-shot
// runner would send it.
type PromptLine struct {
	CaseID string `json:"case_id"`
	Call   string `json:"call"` // "classify" or "patch"
	Prompt string `json:"prompt"`
	Schema string `json:"schema"`
	// GoldConditioned marks a patch call that exists because the gold label is
	// supersede or merge, not because a classifier chose patch.
	GoldConditioned bool `json:"gold_conditioned,omitempty"`
}

// AnswerText is a model's answer in an answers file: a JSON string (the usual
// case, possibly fenced or wrapped in prose) or a JSON object, which is taken
// as the answer itself. Either way it is validated like a live answer.
type AnswerText string

// UnmarshalJSON accepts a string or any other JSON value.
func (a *AnswerText) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*a = AnswerText(s)
		return nil
	}
	if bytes.Equal(b, []byte("null")) {
		*a = ""
		return nil
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, b); err != nil {
		return err
	}
	*a = AnswerText(buf.String())
	return nil
}

// AnswerLine is one line of an answers file.
type AnswerLine struct {
	CaseID string     `json:"case_id"`
	Call   string     `json:"call"`
	Output AnswerText `json:"output"`
	// Usage is optional; replay answers usually carry none.
	Usage *memengine.Usage `json:"usage,omitempty"`
}

// ClassifyPrompts is the classify-only file: every classification call and
// nothing else. A patch line exists only for cases whose gold label is patch,
// supersede or merge, so a session that saw one next to a classify line would
// read the label off it; the classify calls must be answered completely, in a
// fresh session, before the patch file is handed over.
func ClassifyPrompts(s *Set) []PromptLine {
	var out []PromptLine
	for _, p := range Prompts(s) {
		if p.Call == "classify" {
			out = append(out, p)
		}
	}
	return out
}

// PatchPrompts is the patch file.
func PatchPrompts(s *Set) []PromptLine {
	var out []PromptLine
	for _, p := range Prompts(s) {
		if p.Call == "patch" {
			out = append(out, p)
		}
	}
	return out
}

// Prompts lists every call a replay answers, classify and patch interleaved by
// case. It is not a file to hand to a session (see ClassifyPrompts). A patch prompt depends on the
// classify answer in a live pipeline, so it is exported for every case whose
// gold label is patch, supersede or merge (gold-conditioned).
func Prompts(s *Set) []PromptLine {
	var out []PromptLine
	for _, c := range s.Cases {
		out = append(out, PromptLine{CaseID: c.ID, Call: "classify",
			Prompt: memengine.ClassifyRequest{Observation: c.Observation, Memories: s.Snippets(c)}.Prompt(), Schema: memengine.ClassifySchema})
		if t, ok := s.PatchTarget(c); ok {
			out = append(out, patchLine(c, t, false))
		} else if t, ok := s.ConditionedTarget(c); ok {
			out = append(out, patchLine(c, t, true))
		}
	}
	return out
}

func patchLine(c Case, t Memory, cond bool) PromptLine {
	return PromptLine{CaseID: c.ID, Call: "patch", GoldConditioned: cond, Schema: memengine.PatchSchema,
		Prompt: memengine.PatchRequest{Observation: c.Observation, Name: t.Name, Description: t.Description, Body: t.Body}.Prompt()}
}

// Replay is a Model that answers from a recorded file, keyed by the exact
// prompt text; a prompt with no recorded answer is a failed call, not an
// invalid output.
type Replay struct {
	answers map[string]AnswerLine
}

// NewReplay joins an answers file with the prompts of the set.
func NewReplay(s *Set, answers []AnswerLine) *Replay {
	byKey := map[string]AnswerLine{}
	for _, a := range answers {
		byKey[a.CaseID+"\x00"+a.Call] = a
	}
	r := &Replay{answers: map[string]AnswerLine{}}
	for _, p := range Prompts(s) {
		if a, ok := byKey[p.CaseID+"\x00"+p.Call]; ok {
			r.answers[p.Prompt] = a
		}
	}
	return r
}

// Call implements memengine.Model.
func (r *Replay) Call(_ context.Context, prompt string) (memengine.Reply, error) {
	a, ok := r.answers[prompt]
	if !ok {
		return memengine.Reply{}, fmt.Errorf("no recorded answer for this call")
	}
	rep, err := memengine.ParseReply([]byte(a.Output))
	if a.Usage != nil {
		rep.Usage, rep.UsageKnown = *a.Usage, true
	}
	return rep, err
}

// LoadAnswers reads an answers file.
func LoadAnswers(path string) ([]AnswerLine, error) { return readJSONL[AnswerLine](path) }

// AnswerSource describes one answers file.
type AnswerSource struct {
	File         string `json:"file"`
	Hash         string `json:"hash"` // first 12 hex digits of the SHA-256 of the file
	ClassifyOnly bool   `json:"classify_only"`
	Classify     int    `json:"classify_answers"`
	Patch        int    `json:"patch_answers"`
}

// LoadAnswerFiles reads several answers files and describes each one.
func LoadAnswerFiles(paths []string) ([]AnswerLine, []AnswerSource, error) {
	var all []AnswerLine
	var srcs []AnswerSource
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, nil, err
		}
		ls, err := LoadAnswers(p)
		if err != nil {
			return nil, nil, err
		}
		sum := sha256.Sum256(raw)
		src := AnswerSource{File: filepath.Base(p), Hash: hex.EncodeToString(sum[:])[:12]}
		for _, l := range ls {
			if l.Call == "classify" {
				src.Classify++
			} else {
				src.Patch++
			}
		}
		src.ClassifyOnly = src.Classify > 0 && src.Patch == 0
		all = append(all, ls...)
		srcs = append(srcs, src)
	}
	return all, srcs, nil
}

// ClassifyValid reports whether a replay tier's classification scores can be
// trusted: every file that supplied a classify answer held classify calls only,
// so the gold label could not be read off a patch line.
func ClassifyValid(srcs []AnswerSource) bool {
	n := 0
	for _, s := range srcs {
		if s.Classify == 0 {
			continue
		}
		n++
		if !s.ClassifyOnly {
			return false
		}
	}
	return n > 0
}
