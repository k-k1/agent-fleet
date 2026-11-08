package goldset

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/memengine"
)

// A synthetic gold set: nothing here is from the private set.
func synth() *Set {
	mem := func(name, desc, body string) Memory {
		return Memory{Name: name, Scope: "project", Revision: 1, CitedRevision: 1, Description: desc, Body: body}
	}
	s := &Set{Inputs: map[string]Input{}}
	add := func(c Case, ms ...Memory) {
		for _, m := range ms {
			c.Memories = append(c.Memories, MemRef{Name: m.Name, Scope: m.Scope, Revision: m.Revision})
		}
		s.Cases = append(s.Cases, c)
		s.Inputs[c.ID] = Input{ID: c.ID, Memories: ms}
	}
	retry := mem("retry-rule", "How to retry flaky jobs", "Retry a flaky job with `gh run rerun --failed`.\nNever raise the timeout.\nSee issue #952 for the root cause.\n")
	add(Case{
		ID: "P1", Observation: "The trunk branch is main now, not develop.", Label: Label{Operation: memengine.OpPatch, OK: "OK"},
		Edits:       []RefEdit{{Field: "body", Old: "Never raise the timeout.\n", New: "Never raise the timeout.\nThe trunk is main.\n"}},
		MustSurvive: []string{"retry with `gh run rerun --failed`", "see #952", "the trunk is main"},
	}, retry)
	add(Case{ID: "A1", Observation: "A new durable lesson.", Label: Label{Operation: memengine.OpAdd, OK: "OK"}})
	add(Case{ID: "N1", Observation: "Already recorded elsewhere.", Label: Label{Operation: memengine.OpNoop, OK: "OK"}}, retry)
	return s
}

func TestAnchors(t *testing.T) {
	got := strings.Join(Anchors("retry with `gh run rerun --failed`, see #952 and a.b/c-d.go."), "|")
	for _, want := range []string{"gh run rerun --failed", "#952", "a.b/c-d.go", "retry", "with"} {
		if !strings.Contains(got, want) {
			t.Errorf("anchor %q missing from %s", want, got)
		}
	}
	if a := Anchors("ab 12 は"); len(a) != 0 {
		t.Errorf("too-short tokens are not anchors: %v", a)
	}
}

func TestCheckFactsSeparatesLostFromMissingNew(t *testing.T) {
	orig := "keep ALPHA-1 here\nand BETA-2 too\n"
	ref := orig + "new GAMMA-3 line\n"
	items := []string{"ALPHA-1 stays", "BETA-2 stays", "GAMMA-3 arrives", "nothing checkable があ"}
	fc := CheckFacts(items, "keep ALPHA-1 here\n", orig, ref)
	if len(fc.Lost) != 1 || fc.Lost[0] != "BETA-2 stays" {
		t.Errorf("lost %v", fc.Lost)
	}
	if fc.NewMissing != 1 || fc.Verifiable != 3 || fc.Unverifiable != 1 {
		t.Errorf("%+v", fc)
	}
	if fc := CheckFacts(items, ref, orig, ref); len(fc.Lost) != 0 || fc.NewMissing != 0 {
		t.Errorf("the reference text must pass its own check: %+v", fc)
	}
}

func TestReferenceLosesNothing(t *testing.T) {
	ref := Reference(synth())
	if ref.Calls != 1 || ref.Applied != 1 || ref.LostItems != 0 || ref.NewMissing != 0 || ref.WrongChange != 0 {
		t.Errorf("%+v", ref)
	}
}

func TestScorePatchCountsWhatItShould(t *testing.T) {
	s := synth()
	c := s.Cases[0]
	tgt, ok := s.PatchTarget(c)
	if !ok {
		t.Fatal("no target")
	}
	str := func(v string) *string { return &v }
	// Drops the rerun advice, an original fact, while adding the new line.
	lose := ScorePatch(c, tgt, []memengine.Edit{
		{Old: str("Retry a flaky job with `gh run rerun --failed`.\n"), New: str("")},
		{Old: str("Never raise the timeout.\n"), New: str("Never raise the timeout.\nThe trunk is main.\n")},
	})
	if len(lose.Lost) != 1 || !strings.Contains(lose.Lost[0], "rerun") {
		t.Errorf("lost %v", lose.Lost)
	}
	if !lose.WrongChange {
		t.Error("deleting a line no reference edit touches is a wrong change")
	}
	// Adds the fact without touching anything else: clean.
	clean := ScorePatch(c, tgt, []memengine.Edit{{Old: str("Never raise the timeout.\n"), New: str("Never raise the timeout.\nThe trunk is main.\n")}})
	if len(clean.Lost) != 0 || clean.WrongChange || clean.NewMissing != 0 || clean.Status != StatusApplied {
		t.Errorf("%+v", clean)
	}
	// Anchoring the addition on an unrelated line is not destructive.
	anchored := ScorePatch(c, tgt, []memengine.Edit{{Old: str("See issue #952 for the root cause.\n"), New: str("See issue #952 for the root cause.\nThe trunk is main.\n")}})
	if anchored.WrongChange {
		t.Error("an addition next to another line is not a wrong change")
	}
	// An empty answer is a missed fix, and the new fact is absent.
	empty := ScorePatch(c, tgt, nil)
	if empty.Status != StatusEmpty || empty.NewMissing != 1 || len(empty.Lost) != 0 {
		t.Errorf("%+v", empty)
	}
}

// funcModel answers by looking at the prompt, like a scripted model command.
type funcModel func(prompt string) (memengine.Reply, error)

func (f funcModel) Call(_ context.Context, p string) (memengine.Reply, error) { return f(p) }

func reply(text string, in, out int) memengine.Reply {
	return memengine.Reply{Text: text, Usage: memengine.Usage{InputTokens: in, OutputTokens: out}, Raw: []byte(text)}
}

// The negative control: a model that always says no-op must be seen to fail
// every case that needs a change, and must not be rewarded on patch cases.
func TestNoOpModelScoresBadly(t *testing.T) {
	s := synth()
	noop := funcModel(func(p string) (memengine.Reply, error) {
		if strings.Contains(p, "Fold one observation") {
			return reply(`{"edits":[]}`, 100, 5), nil
		}
		return reply(`{"operation":"no-op","targets":[],"reason":"nothing"}`, 100, 5), nil
	})
	ts := ScoreTier(Run(context.Background(), "noop", noop, s, 2, nil, nil))
	if got := ts.Classify[memengine.OpPatch]; got.Cases != 1 || got.Missed != 1 || got.Correct != 0 {
		t.Errorf("patch label: %+v", got)
	}
	if got := ts.Classify[memengine.OpAdd]; got.Missed != 1 {
		t.Errorf("add label: %+v", got)
	}
	if got := ts.Classify[memengine.OpNoop]; got.Correct != 1 {
		t.Errorf("no-op label: %+v", got)
	}
	if ts.Patch.Calls != 1 || ts.Patch.Missed != 1 || ts.Patch.Applied != 0 || ts.Patch.NewMissing != 1 {
		t.Errorf("patch calls must all be missed fixes: %+v", ts.Patch)
	}
	if ts.Total.Missed != 2 || ts.Total.Wrong != 0 {
		t.Errorf("total %+v", ts.Total)
	}
}

func TestPerfectAndBrokenModels(t *testing.T) {
	s := synth()
	var perfect funcModel = func(p string) (memengine.Reply, error) {
		switch {
		case strings.Contains(p, "Fold one observation"):
			return reply(`{"edits":[{"old":"Never raise the timeout.\n","new":"Never raise the timeout.\nThe trunk is main.\n"}]}`, 200, 30), nil
		case strings.Contains(p, "trunk branch is main"):
			return reply(`{"operation":"patch","targets":["retry-rule"],"reason":"r"}`, 150, 20), nil
		case strings.Contains(p, "new durable lesson"):
			return reply(`{"operation":"add","targets":[],"reason":"r"}`, 150, 20), nil
		}
		return reply(`{"operation":"no-op","targets":[],"reason":"r"}`, 150, 20), nil
	}
	pr := Run(context.Background(), "perfect", perfect, s, 1, nil, nil)
	ps := ScoreTier(pr)
	if ps.Total.Correct != 3 || ps.Total.Wrong+ps.Total.Missed+ps.Total.Invalid != 0 {
		t.Errorf("perfect: %+v", ps.Total)
	}
	if ps.Patch.Applied != 1 || ps.Patch.LostItems != 0 || ps.Patch.NewMissing != 0 {
		t.Errorf("perfect patch: %+v", ps.Patch)
	}
	if ps.ClassifyCalls != 3 || ps.PatchCalls != 1 || ps.ClassifyInPerCall != 150 || ps.PatchInPerCall != 200 {
		t.Errorf("tokens: %+v", ps)
	}

	// Garbage is an invalid output; a failed call is an error and not an invalid output.
	var broken funcModel = func(p string) (memengine.Reply, error) {
		if strings.Contains(p, "Fold one observation") {
			return reply(`{"edits":[{"old":"text not in the note","new":"x"}]}`, 1, 1), nil
		}
		if strings.Contains(p, "new durable lesson") {
			return memengine.Reply{}, os.ErrDeadlineExceeded
		}
		return reply(`add!`, 1, 1), nil
	}
	bs := ScoreTier(Run(context.Background(), "broken", broken, s, 1, nil, nil))
	if bs.Total.Invalid != 2 || bs.Total.Errors != 1 || bs.Total.Correct != 0 {
		t.Errorf("broken: %+v", bs.Total)
	}
	if bs.Patch.Invalid != 1 || bs.Patch.Applied != 0 {
		t.Errorf("broken patch: %+v", bs.Patch)
	}
	same, both := Agreement(pr, Run(context.Background(), "p2", perfect, s, 1, nil, nil))
	if same != 3 || both != 3 {
		t.Errorf("agreement %d/%d", same, both)
	}
}

func TestRunResumesWithoutCallingAgain(t *testing.T) {
	s := synth()
	n := 0
	m := funcModel(func(p string) (memengine.Reply, error) {
		n++
		return reply(`{"operation":"no-op","targets":[],"reason":"r"}`, 1, 1), nil
	})
	first := Run(context.Background(), "t", m, s, 1, nil, nil)
	calls := n
	second := Run(context.Background(), "t", m, s, 1, first, nil)
	if n != calls || len(second.Classify) != 3 {
		t.Errorf("resume called the model again: %d -> %d", calls, n)
	}
}

func TestMarkdownCarriesNoCaseText(t *testing.T) {
	s := synth()
	m := funcModel(func(string) (memengine.Reply, error) {
		return reply(`{"operation":"no-op","targets":[],"reason":"SECRET-REASON"}`, 10, 1), nil
	})
	r := Run(context.Background(), "small", m, s, 1, nil, nil)
	md := Markdown([]TierScore{ScoreTier(r)}, Reference(s), nil, nil)
	for _, leak := range []string{"retry-rule", "trunk", "SECRET-REASON", "P1", "N1"} {
		if strings.Contains(md, leak) {
			t.Errorf("report leaks %q", leak)
		}
	}
	if !strings.Contains(md, "| small | patch |") {
		t.Errorf("report lacks the per-operation row:\n%s", md)
	}
}

func TestLoadJoinsAndChecks(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, v ...any) string {
		var b strings.Builder
		for _, x := range v {
			j, _ := json.Marshal(x)
			b.Write(j)
			b.WriteByte('\n')
		}
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	c := map[string]any{"id": "X", "observation": "o", "memories": []any{map[string]any{"name": "m", "scope": "project", "revision": 1}}, "label": map[string]any{"operation": "no-op", "ok": "OK"}, "proposed_operation": "patch"}
	good := write("in.jsonl", map[string]any{"id": "X", "memories": []any{map[string]any{"name": "m", "body": "b"}}})
	cases := write("c.jsonl", c)
	set, err := Load(cases, good)
	if err != nil || len(set.Cases) != 1 || set.Cases[0].Label.Operation != memengine.OpNoop {
		t.Fatalf("%v %v", set, err)
	}
	if _, err := Load(cases, write("none.jsonl", map[string]any{"id": "Y", "memories": []any{}})); err == nil {
		t.Error("a case without inputs must be refused")
	}
	if _, err := Load(cases, write("short.jsonl", map[string]any{"id": "X", "memories": []any{}})); err == nil {
		t.Error("inputs with fewer memories than cited must be refused")
	}
}

// Tiers are columns keyed by name: a later run adds one, and a tier whose CLI
// reports no usage shows n/a rather than a zero.
func TestReportKeyedByTierAndUsageUnknown(t *testing.T) {
	s := synth()
	known := funcModel(func(string) (memengine.Reply, error) {
		return reply(`{"operation":"no-op","targets":[],"reason":"r"}`, 100, 5), nil
	})
	unknown := funcModel(func(string) (memengine.Reply, error) {
		return memengine.Reply{Text: `{"operation":"no-op","targets":[],"reason":"r"}`}, nil
	})
	a := ScoreTier(Run(context.Background(), "claude:haiku", known, s, 1, nil, nil))
	b := ScoreTier(Run(context.Background(), "codex:x", unknown, s, 1, nil, nil))
	if !a.UsageKnown || b.UsageKnown {
		t.Fatalf("usage known: %v %v", a.UsageKnown, b.UsageKnown)
	}
	md := Markdown([]TierScore{a, b}, Reference(s), nil, nil)
	if !strings.Contains(md, "| codex:x | n/a |") || !strings.Contains(md, "| claude:haiku | patch |") || !strings.Contains(md, "| codex:x | patch |") {
		t.Errorf("report:\n%s", md)
	}
}

func synthWithSupersede() *Set {
	s := synth()
	m := Memory{Name: "old-rule", Scope: "project", Revision: 1, CitedRevision: 1, Description: "d", Body: "Use the old branch.\nKeep the audit log.\n"}
	c := Case{ID: "S1", Observation: "The old branch is gone.", Label: Label{Operation: memengine.OpSupersede, OK: "OK"},
		Memories: []MemRef{{Name: m.Name, Scope: m.Scope, Revision: 1}}, MustSurvive: []string{"keep the audit log"}}
	s.Cases = append(s.Cases, c)
	s.Inputs["S1"] = Input{ID: "S1", Memories: []Memory{m}}
	return s
}

func TestPromptsAreGoldConditioned(t *testing.T) {
	s := synthWithSupersede()
	var got []string
	for _, p := range Prompts(s) {
		got = append(got, p.CaseID+"/"+p.Call+map[bool]string{true: "*"}[p.GoldConditioned])
		if p.Prompt == "" || p.Schema == "" {
			t.Errorf("%s/%s lacks prompt or schema", p.CaseID, p.Call)
		}
		if !strings.Contains(p.Prompt, "Reply with one JSON object and nothing else") {
			t.Errorf("%s/%s does not tell the model to answer with one JSON object", p.CaseID, p.Call)
		}
	}
	want := "P1/classify P1/patch A1/classify N1/classify S1/classify S1/patch*"
	if strings.Join(got, " ") != want {
		t.Errorf("got %v, want %s", got, want)
	}
}

func TestReplayScoresLikeALiveRun(t *testing.T) {
	s := synthWithSupersede()
	answer := func(p string) string {
		switch {
		case strings.Contains(p, "Fold one observation") && strings.Contains(p, "old branch"):
			return `{"edits":[{"old":"Use the old branch.\n","new":"Use main.\n"}]}`
		case strings.Contains(p, "Fold one observation"):
			return `{"edits":[{"old":"Never raise the timeout.\n","new":"Never raise the timeout.\nThe trunk is main.\n"}]}`
		case strings.Contains(p, "old branch is gone"):
			return `{"operation":"supersede","targets":["old-rule"],"reason":"r"}`
		case strings.Contains(p, "trunk branch is main"):
			return `{"operation":"patch","targets":["retry-rule"],"reason":"r"}`
		case strings.Contains(p, "new durable lesson"):
			return `{"operation":"add","targets":[],"reason":"r"}`
		}
		return `{"operation":"no-op","targets":[],"reason":"r"}`
	}
	var lines []AnswerLine
	for _, p := range Prompts(s) {
		lines = append(lines, AnswerLine{CaseID: p.CaseID, Call: p.Call, Output: AnswerText(answer(p.Prompt))})
	}
	rr := RunWith(context.Background(), "agy:x", NewReplay(s, lines), s, 1, nil, true, nil)
	rr.Mode = "replay"
	rr.AnswerFiles = []AnswerSource{{File: "c.jsonl", ClassifyOnly: true, Classify: 4}, {File: "p.jsonl", Patch: 2}}
	rs := ScoreTier(rr)
	live := ScoreTier(RunWith(context.Background(), "live", funcModel(func(p string) (memengine.Reply, error) { return reply(answer(p), 1, 1), nil }), s, 1, nil, true, nil))
	if rs.Total != live.Total || rs.Patch != live.Patch {
		t.Errorf("replay %+v / %+v differs from live %+v / %+v", rs.Total, rs.Patch, live.Total, live.Patch)
	}
	if rs.Total.Wrong+rs.Total.Missed+rs.Total.Invalid+rs.Total.Errors != 0 {
		t.Errorf("%+v", rs.Total)
	}
	if rs.UsageKnown || rs.Mode != "replay" || rs.PatchConditioned.Calls != 1 || rs.PatchConditioned.Applied != 1 || rs.PatchConditioned.LostItems != 0 {
		t.Errorf("replay tier: %+v", rs)
	}
	if rs.Patch.Calls != 1 {
		t.Errorf("conditioned calls must not enter the patch-label row: %+v", rs.Patch)
	}
	md := Markdown([]TierScore{live, rs}, Reference(s), nil, nil)
	if !strings.Contains(md, "replay (answered as a session") || !strings.Contains(md, "Gold-conditioned patch calls") {
		t.Errorf("report does not record the mode or the conditioning:\n%s", md)
	}
	// An unanswered call is a failed call, and a garbled one an invalid output.
	partial := append([]AnswerLine{}, lines[:1]...)
	partial = append(partial, AnswerLine{CaseID: "A1", Call: "classify", Output: "I would add it."})
	ps := ScoreTier(RunWith(context.Background(), "p", NewReplay(s, partial), s, 1, nil, true, nil))
	if ps.Total.Errors != 2 || ps.Total.Invalid != 1 {
		t.Errorf("partial: %+v", ps.Total)
	}
}

// The classify file must not carry the label: patch lines exist only for
// patch/supersede/merge cases, so they live in their own file.
func TestClassifyFileHasNoPatchLines(t *testing.T) {
	s := synthWithSupersede()
	cl, pa := ClassifyPrompts(s), PatchPrompts(s)
	if len(cl) != len(s.Cases) {
		t.Errorf("%d classify lines for %d cases", len(cl), len(s.Cases))
	}
	for _, l := range cl {
		if l.Call != "classify" || l.GoldConditioned {
			t.Errorf("the classify file holds %+v", l.CaseID+"/"+l.Call)
		}
	}
	if len(pa) != 2 {
		t.Errorf("%d patch lines", len(pa))
	}
	for _, l := range pa {
		if l.Call != "patch" {
			t.Errorf("the patch file holds a %s line", l.Call)
		}
	}
}

func TestAnswerOutputMayBeAStringOrAnObject(t *testing.T) {
	var ls []AnswerLine
	in := `[{"case_id":"A","call":"classify","output":"{\"operation\":\"add\"}"},
	      {"case_id":"B","call":"classify","output":{"operation":"add","targets":[ ],"reason":"r"}},
	      {"case_id":"C","call":"patch","output":{"edits":[]}},
	      {"case_id":"D","call":"classify","output":null}]`
	if err := json.Unmarshal([]byte(in), &ls); err != nil {
		t.Fatal(err)
	}
	if string(ls[0].Output) != `{"operation":"add"}` || string(ls[1].Output) != `{"operation":"add","targets":[],"reason":"r"}` || string(ls[2].Output) != `{"edits":[]}` || ls[3].Output != "" {
		t.Errorf("%v", ls)
	}
	// An object answer is validated like any other: replay, then score.
	s := synthWithSupersede()
	rep := NewReplay(s, []AnswerLine{{CaseID: "A1", Call: "classify", Output: ls[1].Output}})
	o := ClassifyCase(context.Background(), rep, s, s.Cases[1])
	if o.Status != StatusOK || o.Pred != memengine.OpAdd {
		t.Errorf("%+v", o)
	}
}

func TestReplayClassificationIsValidOnlyFromAClassifyOnlyFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, ls ...AnswerLine) string {
		var b strings.Builder
		for _, l := range ls {
			j, _ := json.Marshal(l)
			b.Write(j)
			b.WriteByte('\n')
		}
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte(b.String()), 0o644)
		return p
	}
	cl := AnswerLine{CaseID: "A1", Call: "classify", Output: "{}"}
	pa := AnswerLine{CaseID: "P1", Call: "patch", Output: "{}"}
	_, split, err := LoadAnswerFiles([]string{write("c.jsonl", cl), write("p.jsonl", pa)})
	if err != nil || !ClassifyValid(split) || split[0].Hash == "" || split[0].File != "c.jsonl" {
		t.Fatalf("%+v %v", split, err)
	}
	_, mixed, _ := LoadAnswerFiles([]string{write("m.jsonl", cl, pa)})
	if ClassifyValid(mixed) {
		t.Error("a file mixing classify and patch calls must not validate the classification")
	}
	if ClassifyValid(nil) || ClassifyValid([]AnswerSource{{File: "p", Patch: 1}}) {
		t.Error("no classify answers, no validity")
	}

	s := synth()
	r := RunWith(context.Background(), "agy-session:x", NewReplay(s, nil), s, 1, nil, true, nil)
	r.Mode, r.AnswerFiles = "replay", mixed
	ts := ScoreTier(r)
	if ts.ClassifyValid {
		t.Fatal("the mixed source must make classification invalid")
	}
	md := Markdown([]TierScore{ts}, Reference(s), nil, nil)
	if !strings.Contains(md, "classification scores NOT valid") {
		t.Errorf("the report must say so:\n%s", md)
	}
	if v := (Bar{Operations: map[string]Threshold{"add": {}}}).Judge(ts)["add"]; v.Pass || !strings.Contains(v.Reason, "not valid") {
		t.Errorf("a replay tier with unvalidated classification must not pass: %+v", v)
	}
	r.AnswerFiles = split
	if !ScoreTier(r).ClassifyValid || !strings.Contains(Markdown([]TierScore{ScoreTier(r)}, Reference(s), nil, nil), "classification scores valid, answered from a classify-only file") {
		t.Error("a classify-only source validates the classification")
	}
}
