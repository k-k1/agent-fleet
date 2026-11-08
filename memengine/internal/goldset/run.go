package goldset

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/k-k1/memengine"
)

// Status values of an outcome.
const (
	StatusOK      = "ok"      // classification answered and valid
	StatusApplied = "applied" // patch valid and non-empty
	StatusEmpty   = "empty"   // patch valid, no edits: the model changed nothing
	StatusInvalid = "invalid" // answer dropped by deterministic validation
	StatusError   = "error"   // the call itself failed (not counted as an invalid output)
)

// ClassifyOutcome is the result of one classification call.
type ClassifyOutcome struct {
	Case    string              `json:"case"`
	Label   memengine.Operation `json:"label"`
	Pred    memengine.Operation `json:"pred,omitempty"`
	Targets []string            `json:"targets,omitempty"`
	Status  string              `json:"status"`
	Detail  string              `json:"detail,omitempty"`
	Usage   memengine.Usage     `json:"usage"`
	Raw     string              `json:"raw,omitempty"`
}

// PatchOutcome is the result of one patch call.
type PatchOutcome struct {
	Case         string           `json:"case"`
	Target       string           `json:"target"`
	Conditioned  bool             `json:"gold_conditioned,omitempty"` // a patch call on a supersede/merge case: no reference edits
	NG           bool             `json:"ng,omitempty"`               // the member marked the proposal NG; see Label.Notes
	Status       string           `json:"status"`
	Detail       string           `json:"detail,omitempty"`
	Edits        []memengine.Edit `json:"edits,omitempty"`
	Lost         []string         `json:"lost,omitempty"`
	NewMissing   int              `json:"new_missing,omitempty"`
	Verifiable   int              `json:"verifiable"`
	Unverifiable int              `json:"unverifiable"`
	WrongChange  bool             `json:"wrong_change,omitempty"`
	Usage        memengine.Usage  `json:"usage"`
	Raw          string           `json:"raw,omitempty"`
}

// TierRun is everything one tier produced.
type TierRun struct {
	Name string `json:"name"`
	// Mode is "live" (a one-shot command per call) or "replay" (answers made
	// elsewhere, e.g. by an agent session that saw earlier cases, so its scores
	// are not fully comparable).
	Mode string `json:"mode"`
	// GoldConditioned: patch calls were also made for supersede/merge cases,
	// with the gold label deciding that a patch call happens.
	GoldConditioned bool              `json:"gold_conditioned,omitempty"`
	Baseline        memengine.Usage   `json:"baseline"` // a trivial call: the command's fixed overhead
	Classify        []ClassifyOutcome `json:"classify"`
	Patch           []PatchOutcome    `json:"patch"`
}

func statusOf(err error) string {
	if errors.Is(err, memengine.ErrInvalid) {
		return StatusInvalid
	}
	return StatusError
}

// ClassifyCase runs one classification call on a case.
func ClassifyCase(ctx context.Context, m memengine.Model, s *Set, c Case) ClassifyOutcome {
	req := memengine.ClassifyRequest{Observation: c.Observation, Memories: s.Snippets(c)}
	ans, rep, err := memengine.Classify(ctx, m, req)
	o := ClassifyOutcome{Case: c.ID, Label: c.Label.Operation, Usage: rep.Usage, Raw: string(rep.Raw)}
	if err != nil {
		o.Status, o.Detail = statusOf(err), err.Error()
		return o
	}
	o.Status, o.Pred, o.Targets = StatusOK, ans.Operation, ans.Targets
	return o
}

// PatchCase runs one patch call on a case labelled patch. ok is false when the
// case has no resolvable target memory.
func PatchCase(ctx context.Context, m memengine.Model, s *Set, c Case, conditioned bool) (PatchOutcome, bool) {
	tgt, ok := s.PatchTarget(c)
	if conditioned {
		tgt, ok = s.ConditionedTarget(c)
	}
	if !ok {
		return PatchOutcome{}, false
	}
	req := memengine.PatchRequest{Observation: c.Observation, Name: tgt.Name, Description: tgt.Description, Body: tgt.Body}
	ans, rep, err := memengine.Patch(ctx, m, req)
	if err != nil {
		o := PatchOutcome{Case: c.ID, Target: tgt.Name, NG: c.Label.OK == "NG", Usage: rep.Usage, Raw: string(rep.Raw)}
		o.Status, o.Detail = statusOf(err), err.Error()
		o.Conditioned = conditioned
		return o, true
	}
	o := ScorePatch(c, tgt, ans.Edits)
	o.Conditioned = conditioned
	o.Usage, o.Raw = rep.Usage, string(rep.Raw)
	return o, true
}

// ScorePatch scores a validated answer against the case: empty answers are
// missed fixes, must_survive facts are checked on the patched text, and an
// edit that deletes or rewrites text the reference patch left alone is a wrong
// change.
func ScorePatch(c Case, tgt Memory, edits []memengine.Edit) PatchOutcome {
	o := PatchOutcome{Case: c.ID, Target: tgt.Name, NG: c.Label.OK == "NG", Edits: edits}
	if len(edits) == 0 {
		o.Status = StatusEmpty
	} else {
		o.Status = StatusApplied
	}
	body, desc, err := memengine.ApplyEdits(tgt.Body, tgt.Description, edits)
	if err != nil { // the caller validated already; this keeps the scorer total
		o.Status, o.Detail = StatusInvalid, err.Error()
		return o
	}
	refBody, refDesc, _ := memengine.ApplyEdits(tgt.Body, tgt.Description, toEdits(c.Edits))
	fc := CheckFacts(c.MustSurvive, body+"\n"+desc, tgt.Body+"\n"+tgt.Description, refBody+"\n"+refDesc)
	o.Lost, o.NewMissing, o.Verifiable, o.Unverifiable = fc.Lost, fc.NewMissing, fc.Verifiable, fc.Unverifiable
	// Without reference edits there is nothing to compare an edit with.
	o.WrongChange = len(c.Edits) > 0 && offTargetDestructive(tgt, c.Edits, edits)
	return o
}

// offTargetDestructive reports an edit that removes or rewrites text no
// reference edit touches. Adding text next to an unrelated line (new contains
// old) is not destructive and is judged by the member, not by this scorer.
func offTargetDestructive(tgt Memory, ref []RefEdit, got []memengine.Edit) bool {
	type span struct{ lo, hi int }
	refSpans := map[string][]span{}
	for _, r := range ref {
		f := fieldOf(r.Field)
		if i := strings.Index(textOf(tgt, f), r.Old); i >= 0 {
			refSpans[f] = append(refSpans[f], span{i, i + len(r.Old)})
		}
	}
	for _, e := range got {
		f := fieldOf(e.Field)
		i := strings.Index(textOf(tgt, f), *e.Old)
		if i < 0 || strings.Contains(*e.New, *e.Old) {
			continue
		}
		hit := false
		for _, s := range refSpans[f] {
			if i < s.hi && s.lo < i+len(*e.Old) {
				hit = true
			}
		}
		if !hit {
			return true
		}
	}
	return false
}

func fieldOf(f string) string {
	if f == "" {
		return "body"
	}
	return f
}

func textOf(m Memory, field string) string {
	if field == "description" {
		return m.Description
	}
	return m.Body
}

// Run runs both calls of one tier over the whole set with a bounded number of
// calls in flight (each model command is a heavy process on a shared host).
// Calls whose key is in done are skipped, so an interrupted run resumes
// without paying twice.
func Run(ctx context.Context, name string, m memengine.Model, s *Set, workers int, prev *TierRun, progress func(string)) *TierRun {
	return RunWith(ctx, name, m, s, workers, prev, false, progress)
}

// RunWith is Run with the choice of gold-conditioned patch calls: also one for
// every case labelled supersede or merge.
func RunWith(ctx context.Context, name string, m memengine.Model, s *Set, workers int, prev *TierRun, conditioned bool, progress func(string)) *TierRun {
	if workers < 1 {
		workers = 1
	}
	run := &TierRun{Name: name, Mode: "live", GoldConditioned: conditioned}
	have := map[string]bool{}
	if prev != nil {
		run.Baseline = prev.Baseline
		for _, o := range prev.Classify {
			if o.Status != StatusError {
				run.Classify = append(run.Classify, o)
				have["c:"+o.Case] = true
			}
		}
		for _, o := range prev.Patch {
			if o.Status != StatusError {
				run.Patch = append(run.Patch, o)
				have["p:"+o.Case+":"+o.Target+fmt.Sprint(o.Conditioned)] = true
			}
		}
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, workers)
	spawn := func(f func()) {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			f()
		}()
	}
	for _, c := range s.Cases {
		c := c
		if !have["c:"+c.ID] {
			spawn(func() {
				o := ClassifyCase(ctx, m, s, c)
				mu.Lock()
				run.Classify = append(run.Classify, o)
				mu.Unlock()
				if progress != nil {
					progress(name + " classify " + c.ID + " " + o.Status)
				}
			})
		}
		for _, cond := range []bool{false, true} {
			cond := cond
			tgt, ok := s.PatchTarget(c)
			if cond {
				if !conditioned {
					continue
				}
				tgt, ok = s.ConditionedTarget(c)
			}
			if !ok || have["p:"+c.ID+":"+tgt.Name+fmt.Sprint(cond)] {
				continue
			}
			spawn(func() {
				o, _ := PatchCase(ctx, m, s, c, cond)
				mu.Lock()
				run.Patch = append(run.Patch, o)
				mu.Unlock()
				if progress != nil {
					progress(name + " patch " + c.ID + " " + o.Status)
				}
			})
		}
	}
	wg.Wait()
	return run
}
