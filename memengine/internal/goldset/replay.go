package goldset

import (
	"context"
	"fmt"

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

// AnswerLine is one line of an answers file.
type AnswerLine struct {
	CaseID string `json:"case_id"`
	Call   string `json:"call"`
	Output string `json:"output"`
	// Usage is optional; replay answers usually carry none.
	Usage *memengine.Usage `json:"usage,omitempty"`
}

// Prompts lists every call a replay answers. A patch prompt depends on the
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
