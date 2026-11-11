package memconsol

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memengine"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memoryx"
)

// Models are the two roles the engine calls (decision 6): small for every operation, large for
// the escalations (supersede, merge). There is no fallback between them: a call that fails fails
// the run (memengine.Pipeline's rule, kept here because the run needs each call's usage).
type Models struct {
	Small, Large memengine.Model
}

// Turns tells the run whether a session turn has started since it began. It is asked before every
// model call, never during one: the call in flight finishes, the rest is deferred.
type Turns interface {
	StartedSince(t time.Time) bool
}

// Run statuses.
const (
	RunOK     = "ok"     // every selected topic was handled
	RunFailed = "failed" // a model or runner error ended it; nothing was queued from the failed call
	RunCut    = "cut"    // a turn started, or the budget ran out, and the rest was deferred
	RunIdle   = "idle"   // nothing was ripe
)

// Report is what one run did, kept under Dir/runs and returned by the status route. It carries no
// memory text and no model output.
type Report struct {
	ID       string    `json:"id"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`
	Status   string    `json:"status"`
	// Error names the class of a failure ("model call failed"), never the runner's output.
	Error   string   `json:"error,omitempty"`
	Ripe    int      `json:"ripe"`
	Handled []string `json:"handled"` // topic ids that were looked at
	Queued  []string `json:"queued"`  // proposal ids
	Delayed []Delay  `json:"delayed"`
	// TooLarge are the ripe topics no run can take (CauseTooLarge): not delayed, and not retried
	// until the memory or the budget changes. Each carries its smallest estimate.
	TooLarge []Delay `json:"tooLarge"`
	Calls    int     `json:"calls"`
	Dropped  int     `json:"dropped"` // engine answers that failed validation
	// DroppedReasons says why observations were dropped, one short fixed phrase each (never model
	// output or memory text); at most maxDroppedReasons are kept and the rest are only counted in
	// DroppedReasonsMore. Dropped counts answers, so the two need not match.
	DroppedReasons     []DroppedReason   `json:"droppedReasons,omitempty"`
	DroppedReasonsMore int               `json:"droppedReasonsMore,omitempty"`
	Rejected           int               `json:"rejected"` // items the adapter's own checks refused
	Skipped            int               `json:"skipped"`  // observations left out by the secret scan
	Tokens             int               `json:"tokens"`   // measured when the runner reported usage, else estimated
	Estimated          bool              `json:"estimated,omitempty"`
	Models             map[string]string `json:"models,omitempty"`
	// Manual is set on a run the member started; All on one that took every waiting topic.
	Manual bool `json:"manual,omitempty"`
	All    bool `json:"all,omitempty"`
}

// DroppedReason is why one observation was dropped. Reason is always one of the fixed phrases of
// this file (dropReason*, problemReason): the model's wording, a refused memory's text and the
// validators' own messages can all carry secret material and never reach the record.
type DroppedReason struct {
	Observation string `json:"observation"`
	Reason      string `json:"reason"`
}

// Bounds on the dropped-reason record, so one bad run cannot grow the stored report.
const (
	maxDroppedReasons   = 20
	maxDroppedReasonLen = 120
)

// The reasons a drop carries besides a validation problem's.
const (
	dropReasonClassify   = "the classification answer failed validation"
	dropReasonPatch      = "the patch answer failed validation"
	dropReasonCompose    = "the compose answer failed validation; kept as advice"
	dropReasonDescEdit   = "the patch edits the description, which a patch cannot apply"
	dropReasonSuperseded = "the memory is superseded by a replace of the same proposal"
)

// problemReason is the fixed phrase for a refused item, by the validator's problem kind. The
// problem's own message is not used: it can quote a name or the store's schema error.
func problemReason(op string, pr *Problem) string {
	var what string
	switch pr.Kind {
	case ProbStale:
		what = "the memory changed or is gone since the engine read it"
	case ProbForgotten:
		what = "the name was forgotten or existed before (no revival)"
	case ProbExists:
		what = "a memory by that name exists"
	case ProbSecret:
		what = "the text failed the secret scan"
	default:
		what = "the memory failed the schema or link check"
	}
	return op + " refused: " + what
}

// RunInput is everything a run needs; the trigger builds it, tests build it with fakes.
type RunInput struct {
	Models Models
	Roles  map[string]string // role → tier, recorded on proposals
	Turns  Turns
	// Enabled is the member's switch, asked before every model call: once it is off no further
	// call starts. nil means on (tests).
	Enabled func() bool
	Config  Config
	Now     time.Time
	Sleeper func() time.Time // the clock after the start; nil means time.Now
	// Manual marks a member-started run. Its Turns is nil (other sessions' turns do not defer it);
	// every other guard (switch, budget, validation, approval-only publishing) is unchanged.
	Manual bool
	// All takes every waiting topic, ripe or not (a topic with a proposal pending stays out).
	// Honoured only together with Manual.
	All bool
	// Progress is told how many selected topics are done out of how many; nil in tests.
	Progress func(done, total int)
	// Refs looks up the state of the issues and PRs that notes and observations name, for the
	// classifier's prompt. nil means nothing is looked up.
	Refs *RefChecker
}

var errCut = errors.New("cut")

// Run is one consolidation pass. It never writes the memory store: its only writes are the
// proposal queue, the state file and the report under Dir.
func Run(ctx context.Context, in RunInput) (Report, error) {
	clock := in.Sleeper
	if clock == nil {
		clock = time.Now
	}
	rep := Report{ID: newID(), Started: in.Now, Status: RunIdle, Handled: []string{}, Queued: []string{}, Delayed: []Delay{}, TooLarge: []Delay{}, Models: in.Roles,
		Manual: in.Manual, All: in.Manual && in.All}
	progress := func(done, total int) {
		if in.Progress != nil {
			in.Progress(done, total)
		}
	}
	spent := 0
	refs := in.Refs.Begin()
	finish := func(err error) (Report, error) {
		rep.Tokens = spent
		rep.Finished = clock()
		if werr := writeReport(rep); werr != nil && err == nil {
			err = werr
		}
		return rep, err
	}

	if _, err := SweepExpired(in.Now); err != nil {
		return finish(err)
	}
	pending, err := pendingTopics(in.Now)
	if err != nil {
		return finish(err)
	}
	inbox, err := Gather(in.Now)
	if err != nil {
		return finish(err)
	}
	rep.Skipped = inbox.Skipped

	stateMu.Lock()
	st, err := loadState()
	if err == nil {
		err = errors.Join(func() error {
			obs := Reconcile(st, inbox, in.Now)
			inbox.Observations = obs
			return st.save()
		}())
	}
	stateMu.Unlock()
	if err != nil {
		return finish(err)
	}

	topics := BuildTopics(inbox.Observations, inbox.Memories)
	var ripe []RipeTopic
	for _, t := range topics {
		open := t.OpenObservations(st)
		if !Ripe(open, pending[t.ID], in.Config, in.Now) && !(rep.All && !pending[t.ID] && len(open) > 0) {
			continue
		}
		r := PlanTopic(t, open, in.Config)
		if r.TooLarge {
			rep.TooLarge = append(rep.TooLarge, tooLargeDelay(r))
		}
		ripe = append(ripe, r)
	}
	rep.Ripe = len(ripe)
	run, delayed := Select(fitting(ripe), in.Config.TokenBudget)
	progress(0, len(run))
	rep.Delayed = append(rep.Delayed, delayed...)
	if len(run) == 0 {
		if len(delayed) > 0 {
			rep.Status = RunCut
		}
		return finish(nil)
	}
	rep.Status = RunOK

	for k, r := range run {
		progress(k, len(run))
		if err := ctx.Err(); err != nil {
			rep.Status = RunCut
			rep.Delayed = append(rep.Delayed, delaysOf(run[k:], CauseInterrupted, "the Agent is shutting down")...)
			return finish(nil)
		}
		w := &worker{in: in, rep: &rep, spent: &spent, topic: r.Topic, refs: refs}
		perr := w.topic1(ctx, st, r)
		rep.Handled = append(rep.Handled, r.Topic.ID)
		switch {
		case perr == nil:
		case errors.Is(perr, errCut):
			rep.Status = RunCut
			rep.Delayed = append(rep.Delayed, delaysOf(run[k:], w.cutCause, w.cutDetail)...)
			rep.Handled = rep.Handled[:len(rep.Handled)-1]
			return finish(nil)
		default:
			// A model or runner error (a retired model included) ends the run. Nothing was queued
			// from the failed call, and the run does not go on to try the next topic with a
			// model that is not answering.
			rep.Status = RunFailed
			rep.Error = "a model call failed: the run was stopped and nothing was queued from it"
			rep.Delayed = append(rep.Delayed, delaysOf(run[k:], CauseRunFailed, "")...)
			rep.Handled = rep.Handled[:len(rep.Handled)-1]
			return finish(nil)
		}
	}
	progress(len(run), len(run))
	return finish(nil)
}

// fitting drops the topics marked TooLarge: they are reported, never selected or delayed.
func fitting(rs []RipeTopic) []RipeTopic {
	out := make([]RipeTopic, 0, len(rs))
	for _, r := range rs {
		if !r.TooLarge {
			out = append(out, r)
		}
	}
	return out
}

func tooLargeDelay(r RipeTopic) Delay {
	return Delay{Topic: r.Topic.ID, Memory: r.Topic.Memory, Scope: ScopeKey(r.Topic.Scope, r.Topic.ProjectID), Cause: CauseTooLarge, Estimate: r.Estimate,
		Detail: "even one observation with one note costs more than the whole per-run token budget"}
}

func delaysOf(rs []RipeTopic, cause, detail string) []Delay {
	var out []Delay
	for _, r := range rs {
		out = append(out, Delay{Topic: r.Topic.ID, Memory: r.Topic.Memory, Scope: ScopeKey(r.Topic.Scope, r.Topic.ProjectID), Cause: cause, Detail: detail, Estimate: r.Estimate})
	}
	return out
}

type worker struct {
	in    RunInput
	rep   *Report
	spent *int
	topic Topic
	refs  *RefRun

	cutCause, cutDetail string
}

// noteDrop records why an observation was dropped, in the run record (capped) and the Agent log.
// The reason must be one of the fixed phrases; it is cut to maxDroppedReasonLen regardless.
func (w *worker) noteDrop(o Observation, reason string) {
	if r := []rune(reason); len(r) > maxDroppedReasonLen {
		reason = string(r[:maxDroppedReasonLen-1]) + "…"
	}
	log.Printf("memory consolidation: dropped observation %s: %s", o.ID, reason)
	if len(w.rep.DroppedReasons) >= maxDroppedReasons {
		w.rep.DroppedReasonsMore++
		return
	}
	w.rep.DroppedReasons = append(w.rep.DroppedReasons, DroppedReason{Observation: o.ID, Reason: reason})
}

// gate is asked before every model call: a turn that started since the run began, or a spent
// budget, defers the rest. The idle check itself belongs to the trigger (Decide); only a
// member-started manual run reaches Run without it. This is the second half of decision 6 ("when
// a turn starts during a run, the run finishes the model call in flight and defers the rest").
func (w *worker) gate(estimate int) error {
	if w.in.Enabled != nil && !w.in.Enabled() {
		w.cutCause, w.cutDetail = CauseInterrupted, "the member switched consolidation off during the run"
		return errCut
	}
	if w.in.Turns != nil && w.in.Turns.StartedSince(w.in.Now) {
		w.cutCause, w.cutDetail = CauseInterrupted, "a session started a turn during the run"
		return errCut
	}
	if *w.spent+estimate > w.in.Config.TokenBudget {
		w.cutCause, w.cutDetail = CauseBudget, "the per-run token budget would be exceeded"
		return errCut
	}
	return nil
}

func (w *worker) account(rep memengine.Reply, estimate int) {
	w.rep.Calls++
	if rep.UsageKnown {
		*w.spent += rep.Usage.TotalInput() + rep.Usage.OutputTokens
		return
	}
	w.rep.Estimated = true
	*w.spent += estimate
}

func (w *worker) model(role memengine.Role) memengine.Model {
	if role == memengine.RoleLarge {
		return w.in.Models.Large
	}
	return w.in.Models.Small
}

func (w *worker) call(ctx context.Context, role memengine.Role, estimate int, f func(ctx context.Context, m memengine.Model) (memengine.Reply, error)) error {
	if err := w.gate(estimate); err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, w.in.Config.CallTimeout)
	defer cancel()
	rep, err := f(cctx, w.model(role))
	w.account(rep, estimate)
	return err
}

// classified is one observation and the engine's final answer about it.
type classified struct {
	obs  Observation
	ans  memengine.ClassifyAnswer
	role memengine.Role
}

// topic1 handles one topic: classify each taken observation (small, escalating supersede and
// merge to large), then make the patch call for the patch answers, and queue what validates. A
// model or runner error returns as it is and queues nothing for the topic; an invalid answer is
// dropped and counted, never retried.
func (w *worker) topic1(ctx context.Context, st *State, r RipeTopic) error {
	t := r.Topic
	var answers []classified
	dropped := map[string]bool{}
	for _, o := range r.Take {
		req := classifyRequest(t, o, r.Fit)
		req.Evidence = w.refs.evidenceFor(ctx, t, o, memoryTexts(t, req))
		est := estimateTokens(req.Prompt()) + 200
		var ans memengine.ClassifyAnswer
		role := memengine.RoleSmall
		err := w.call(ctx, memengine.RoleSmall, est, func(ctx context.Context, m memengine.Model) (memengine.Reply, error) {
			a, rep, err := memengine.Classify(ctx, m, req)
			ans = a
			return rep, err
		})
		if err == nil && ans.Operation.Escalates() {
			role = memengine.RoleLarge
			err = w.call(ctx, memengine.RoleLarge, est, func(ctx context.Context, m memengine.Model) (memengine.Reply, error) {
				a, rep, err := memengine.Classify(ctx, m, req)
				ans = a
				return rep, err
			})
		}
		if err != nil {
			if memengine.IsInvalid(err) {
				w.rep.Dropped++
				dropped[o.ID] = true
				w.noteDrop(o, dropReasonClassify)
				continue
			}
			return err
		}
		answers = append(answers, classified{o, ans, role})
	}

	var items []Item
	covered := map[string]string{} // observation id -> verdict
	var used []Observation
	patchObs := map[string][]classified{}
	// claim keeps one proposal from holding two items that cannot both be applied: a memory is
	// superseded by one replace only, and a name is created once. The later item is advice.
	claim := newClaims()
	for _, c := range answers {
		switch c.ans.Operation {
		case memengine.OpNoop:
			covered[c.obs.ID] = "no-op"
		case memengine.OpPatch:
			patchObs[c.ans.Targets[0]] = append(patchObs[c.ans.Targets[0]], c)
		case memengine.OpRetire:
			it, ok := w.retireItem(t, c)
			switch {
			case !ok:
				items = append(items, noteItem(c, c.ans.Targets...))
			case claim.sources[it.Name]:
				// Another item of this proposal already takes the memory out of use; this
				// observation is covered by it.
			default:
				claim.sources[it.Name] = true
				items = append(items, it)
			}
			used = append(used, c.obs)
		case memengine.OpAdd, memengine.OpSupersede, memengine.OpMerge:
			it, pr, ok := w.addItem(t, c)
			if !ok && c.ans.Operation == memengine.OpAdd && c.obs.Candidate != nil {
				// The memory an exported proposal carried failed a check (no revival, a name that
				// exists, the schema, a secret): it is refused, not turned into advice.
				dropped[c.obs.ID] = true
				w.noteDrop(c.obs, problemReason("the exported memory", pr))
				continue
			}
			if !ok {
				var err error
				if it, ok, err = w.composeItem(ctx, t, c); err != nil {
					return err
				}
			}
			if ok && !claim.take(it) {
				ok = false
			}
			if !ok {
				it = noteItem(c, c.ans.Targets...)
			}
			items = append(items, it)
			used = append(used, c.obs)
		}
	}
	targets := make([]string, 0, len(patchObs))
	for n := range patchObs {
		targets = append(targets, n)
	}
	sort.Strings(targets)
	for _, name := range targets {
		cs := patchObs[name]
		m, ok := findMem(t.Memories, name)
		if !ok {
			continue
		}
		if claim.sources[name] {
			// The memory is superseded by a replace of this proposal; a patch of it could not apply
			// after that, and the replace already carries the observation.
			for _, c := range cs {
				dropped[c.obs.ID] = true
				w.noteDrop(c.obs, dropReasonSuperseded)
			}
			continue
		}
		var note []string
		for _, c := range cs {
			note = append(note, observationText(c.obs))
		}
		preq := memengine.PatchRequest{Observation: strings.Join(note, "\n\n- "), Name: m.Name, Description: m.Description, Body: m.Body}
		if memoryTextProblem(m.Body+m.Description) || memoryTextProblem(preq.Observation) {
			continue
		}
		est := estimateTokens(preq.Prompt()) + 300
		var pans memengine.PatchAnswer
		err := w.call(ctx, memengine.RoleSmall, est, func(ctx context.Context, mod memengine.Model) (memengine.Reply, error) {
			a, rep, err := memengine.Patch(ctx, mod, preq)
			pans = a
			return rep, err
		})
		if err != nil {
			if memengine.IsInvalid(err) {
				w.rep.Dropped++
				for _, c := range cs {
					dropped[c.obs.ID] = true
					w.noteDrop(c.obs, dropReasonPatch)
				}
				continue
			}
			return err
		}
		if len(pans.Edits) == 0 {
			for _, c := range cs {
				covered[c.obs.ID] = "no-op"
			}
			continue
		}
		it := Item{Op: OpPatch, Name: m.Name, Revision: m.Revision, Reason: cs[0].ans.Reason, Role: string(memengine.RoleSmall)}
		for _, e := range pans.Edits {
			if e.Field == "description" {
				// A description edit is not a body patch; the store's patch path takes the body only
				// from here, so such an answer is dropped rather than half applied.
				it.Edits = nil
				break
			}
			it.Edits = append(it.Edits, Edit{Old: *e.Old, New: *e.New})
		}
		if len(it.Edits) == 0 {
			w.rep.Dropped++
			for _, c := range cs {
				dropped[c.obs.ID] = true
				w.noteDrop(c.obs, dropReasonDescEdit)
			}
			continue
		}
		if pr := validateItem(t.Scope, t.ProjectID, it); pr != nil {
			w.rep.Rejected++
			for _, c := range cs {
				dropped[c.obs.ID] = true
				w.noteDrop(c.obs, problemReason("the patch", pr))
			}
			continue
		}
		items = append(items, it)
		for _, c := range cs {
			used = append(used, c.obs)
		}
	}

	w.commit(t, items, used, covered, dropped)
	return nil
}

func memoryTextProblem(s string) bool { return reviewTextProblem(s) != "" }

// claims tracks what the items of one proposal already use.
type claims struct {
	sources map[string]bool // memories a replace supersedes
	names   map[string]bool // memories a create or replace writes
}

func newClaims() *claims { return &claims{sources: map[string]bool{}, names: map[string]bool{}} }

// take registers the item's memories, or says it clashes with an earlier item and registers nothing.
func (c *claims) take(it Item) bool {
	if c.names[it.Name] {
		return false
	}
	for _, s := range it.Sources {
		if c.sources[s.Name] {
			return false
		}
	}
	c.names[it.Name] = true
	for _, s := range it.Sources {
		c.sources[s.Name] = true
	}
	return true
}

// composeItem asks the large tier to write the memory a supersede, a merge or an add stands for,
// then holds it to the same gate as any other item (validateItem, no revival included). Its text
// is a model's, so a bad answer is advice instead, never a write: ok=false with a nil error. Only
// a failed or deferred call returns an error.
func (w *worker) composeItem(ctx context.Context, t Topic, c classified) (Item, bool, error) {
	op := c.ans.Operation
	if op == memengine.OpAdd && len(c.ans.Targets) != 0 || op == memengine.OpSupersede && len(c.ans.Targets) != 1 || op == memengine.OpMerge && len(c.ans.Targets) < 2 {
		return Item{}, false, nil
	}
	req := memengine.ComposeRequest{Operation: op, Observation: observationText(c.obs)}
	var srcs []Source
	for _, name := range c.ans.Targets {
		m, ok := findMem(t.Memories, name)
		if !ok || memoryTextProblem(m.Body+m.Description) {
			return Item{}, false, nil
		}
		req.Sources = append(req.Sources, memengine.Note{Name: m.Name, Description: m.Description, Body: m.Body})
		srcs = append(srcs, Source{Name: m.Name, Revision: m.Revision})
	}
	if memoryTextProblem(req.Observation) {
		return Item{}, false, nil
	}
	est := estimateTokens(req.Prompt()) + 1500
	var ans memengine.ComposeAnswer
	err := w.call(ctx, memengine.RoleLarge, est, func(ctx context.Context, m memengine.Model) (memengine.Reply, error) {
		a, rep, err := memengine.Compose(ctx, m, req)
		ans = a
		return rep, err
	})
	if err != nil {
		if memengine.IsInvalid(err) {
			w.rep.Dropped++
			w.noteDrop(c.obs, dropReasonCompose)
			return Item{}, false, nil
		}
		return Item{}, false, err
	}
	it := Item{Op: OpCreate, Name: ans.Name, Description: ans.Description, Type: ans.Type, Body: ans.Body,
		Reason: c.ans.Reason, Role: string(memengine.RoleLarge)}
	if len(srcs) > 0 {
		it.Op, it.Sources = OpReplace, srcs
	}
	if pr := validateItem(t.Scope, t.ProjectID, it); pr != nil {
		w.rep.Rejected++
		return Item{}, false, nil
	}
	return it, true, nil
}

// addItem turns an add into a create when an exported 0117 proposal carried the memory; the text is
// then the member's own export, not a model's. An add with no candidate is written by composeItem.
// The candidate is still untrusted and goes through validateItem like anything else.
func (w *worker) addItem(t Topic, c classified) (Item, *Problem, bool) {
	if c.obs.Candidate == nil || c.ans.Operation != memengine.OpAdd {
		return Item{}, nil, false
	}
	cd := c.obs.Candidate
	it := Item{Op: OpCreate, Name: cd.Name, Description: cd.Description, Type: cd.Type, Body: cd.Body,
		Reason: c.ans.Reason, Role: string(c.role)}
	if pr := validateItem(t.Scope, t.ProjectID, it); pr != nil {
		w.rep.Rejected++
		return Item{}, pr, false
	}
	return it, nil, true
}

// retireItem turns a retire answer into an item. The model's text is not trusted: the reason is
// reduced to one line, and the item passes validateItem (the memory is visible, unpinned and at the
// revision the engine read) like any other. ok=false makes it advice instead, never a write.
func (w *worker) retireItem(t Topic, c classified) (Item, bool) {
	if len(c.ans.Targets) != 1 {
		return Item{}, false
	}
	m, ok := findMem(t.Memories, c.ans.Targets[0])
	if !ok {
		return Item{}, false
	}
	it := Item{Op: OpRetire, Name: m.Name, Revision: m.Revision, Reason: retireReason(c.ans.Reason), Role: string(c.role)}
	if pr := validateItem(t.Scope, t.ProjectID, it); pr != nil {
		w.rep.Rejected++
		return Item{}, false
	}
	return it, true
}

// maxRetireReason is the longest one-line reason a retire carries (the store's reason note limit).
const maxRetireReason = 300

// retireReason reduces the model's sentence to one line the store accepts, and says something
// plain when the model gave none.
func retireReason(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return "obsolete: the subject of this note is finished"
	}
	if r := []rune(s); len(r) > maxRetireReason {
		s = string(r[:maxRetireReason-1]) + "…"
	}
	return s
}

// memoryTexts is the full text of the notes a classification call shows, by name: the references a
// note names are read from all of it, not from the excerpt the prompt carries.
func memoryTexts(t Topic, req memengine.ClassifyRequest) map[string]string {
	out := map[string]string{}
	for _, sn := range req.Memories {
		if m, ok := findMem(t.Memories, sn.Name); ok {
			out[m.Name] = m.Description + "\n" + m.Body
		}
	}
	return out
}

func noteItem(c classified, targets ...string) Item {
	it := Item{Op: OpNote, Name: c.obs.Memory, Advice: string(c.ans.Operation), Reason: c.ans.Reason, Role: string(c.role)}
	for _, tg := range targets {
		if tg != "" {
			it.Targets = append(it.Targets, tg)
		}
	}
	if len(it.Targets) == 1 {
		it.Name = it.Targets[0] // a supersede is a change to one target, the old memory
	}
	return it
}

// subjectOf is the memory a proposal is about: the one name every item changes, otherwise the
// topic's. An exported create that joined another memory's topic is about the created memory, not
// the one it was grouped with.
func subjectOf(t Topic, items []Item) string {
	if len(items) == 0 {
		return t.Memory
	}
	for _, it := range items[1:] {
		if it.Name != items[0].Name {
			return t.Memory
		}
	}
	return items[0].Name
}

// commit queues the topic's proposal and records coverage, under the state lock so a concurrent
// Retry or approval sees one consistent file.
func (w *worker) commit(t Topic, items []Item, used []Observation, covered map[string]string, dropped map[string]bool) {
	stateMu.Lock()
	defer stateMu.Unlock()
	cur, err := loadState()
	if err != nil {
		return
	}
	now := w.in.Now
	cover := func(o Observation, proposal, verdict string) {
		s := cur.Observations[o.ID]
		if s == nil {
			s = &ObsState{FirstSeen: o.FirstSeen, LastSeen: now}
			cur.Observations[o.ID] = s
		}
		rev, _ := memoryRev(t, o.Memory)
		s.Covered = &Coverage{Proposal: proposal, Revision: rev, At: now, Verdict: verdict}
	}
	if len(items) > 0 {
		p := Proposal{ID: newID(), TopicID: t.ID, RunID: w.rep.ID, Scope: t.Scope, ProjectID: t.ProjectID, Memory: subjectOf(t, items),
			Status: StatusPending, Created: now, Expires: now.Add(w.in.Config.ProposalTTL), Items: items,
			TargetRevisions: map[string]int{}, Models: w.in.Roles}
		for _, m := range t.Memories {
			p.TargetRevisions[m.Name] = m.Revision
		}
		sort.Slice(used, func(i, j int) bool { return used[i].ID < used[j].ID })
		seen := map[string]bool{}
		for _, o := range used {
			if seen[o.ID] {
				continue
			}
			seen[o.ID] = true
			p.ObservationIDs = append(p.ObservationIDs, o.ID)
			p.Evidence = append(p.Evidence, Evidence{ID: o.ID, Source: o.Source, Memory: o.Memory, Rule: o.Rule, Note: o.Note})
		}
		if err := saveProposal(p); err != nil {
			return // nothing queued, nothing covered: the topic stays ripe for the next run
		}
		w.rep.Queued = append(w.rep.Queued, p.ID)
		for _, o := range used {
			cover(o, p.ID, "proposed")
		}
	}
	for _, o := range t.Observations {
		if v, ok := covered[o.ID]; ok {
			cover(o, "", v)
		}
		if dropped[o.ID] {
			s := cur.Observations[o.ID]
			if s == nil {
				s = &ObsState{FirstSeen: o.FirstSeen, LastSeen: now}
				cur.Observations[o.ID] = s
			}
			s.Dropped++
			s.DroppedRev, _ = memoryRev(t, o.Memory)
		}
	}
	_ = cur.save()
}

func writeReport(rep Report) error {
	dir := filepath.Join(Dir(), "runs")
	if err := writeJSONAtomic(filepath.Join(dir, rep.ID+".json"), rep); err != nil {
		return err
	}
	ents, err := os.ReadDir(dir)
	if err != nil || len(ents) <= keepReports {
		return err
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		names = append(names, e.Name())
	}
	// Reports are named by random ids, so age comes from the modification time.
	sort.Slice(names, func(i, j int) bool {
		a, _ := os.Stat(filepath.Join(dir, names[i]))
		b, _ := os.Stat(filepath.Join(dir, names[j]))
		if a == nil || b == nil {
			return names[i] < names[j]
		}
		return a.ModTime().Before(b.ModTime())
	})
	for _, n := range names[:len(names)-keepReports] {
		_ = os.Remove(filepath.Join(dir, n))
	}
	return nil
}

const keepReports = 30

// LastReport is the newest run report, if any.
func LastReport() (Report, bool) {
	dir := filepath.Join(Dir(), "runs")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return Report{}, false
	}
	var best Report
	found := false
	for _, e := range ents {
		var r Report
		if b, err := os.ReadFile(filepath.Join(dir, e.Name())); err == nil && jsonUnmarshal(b, &r) == nil {
			if !found || r.Started.After(best.Started) {
				best, found = r, true
			}
		}
	}
	return best, found
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

var reviewTextProblem = memoryx.ReviewTextProblem
