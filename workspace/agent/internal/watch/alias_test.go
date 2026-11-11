package watch

import (
	"context"
	"testing"
)

func TestCanonPatterns(t *testing.T) {
	al := (&GitHubActions{}).TypeAliases()
	for in, want := range map[string]string{
		"github.workflow_run.completed": "ci.run.completed",
		"github.workflow_run.*":         "ci.run.*",
		"ci.run.failed":                 "ci.run.failed",
		"github.workflow_runner.x":      "github.workflow_runner.x", // a prefix is a whole name part
		"pr.merged":                     "pr.merged",
	} {
		if got, _ := canonPattern(in, al); got != want {
			t.Errorf("%s -> %s, want %s", in, got, want)
		}
	}
	got := canonPatterns([]string{"github.workflow_run.completed", "ci.run.completed", "ci.run.failed"}, al)
	if len(got) != 2 || got[0] != "ci.run.completed" || got[1] != "ci.run.failed" {
		t.Errorf("both spellings of one type were kept: %v", got)
	}
}

// A subscription that names both spellings is told one run once, not twice.
func TestBothSpellingsOfATypeAreToldOnce(t *testing.T) {
	f := newFakeGitHub()
	f.bodies["/repos/o/r/actions/runs/55"] = run(55, "completed", "success", "2026-11-10T01:00:00Z", 1)
	sink := &fakeSink{answer: Ready}
	e, _, _ := newTestEngine(newTestActions(t, f), sink)
	ctx := context.Background()
	sub, err := e.Subscribe(ctx, Request{Owner: "s", Source: ActionsKind, Ref: "o/r:run/55", Until: UntilDeadline,
		Types: []string{"github.workflow_run.completed", "ci.run.completed", "github.workflow_run.*"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(sub.Types) != 2 { // ci.run.completed and ci.run.*
		t.Fatalf("types %v", sub.Types)
	}
	e.Tick(ctx)
	e.Tick(ctx)
	if len(sink.told) != 1 || sink.told[0] != "s:ci.run.completed:55:1" {
		t.Fatalf("told %v", sink.told)
	}
}

// State written before the rename holds the old type names. After a restart the run that was
// already told under the old name must not be told again, and what was pending is still told.
func TestStateStoredUnderTheOldNamesKeepsItsWatchesQuiet(t *testing.T) {
	f := newFakeGitHub()
	f.bodies["/repos/o/r/actions/runs/55"] = run(55, "completed", "failure", "2026-11-10T01:00:00Z", 1)
	f.bodies["/repos/o/r/actions/runs/55/attempts/1/jobs?per_page=100"] = map[string]any{"jobs": []any{}}
	a := newTestActions(t, f)
	st := &memStore{}
	e := New(st, &fakeSink{answer: Ready}, a)
	e.load()
	e.state.Subs = []Subscription{{
		ID: "old", Owner: "s", Source: ActionsKind, Subject: "github:o/r/actions/runs/55", Ref: "o/r:run/55",
		Types: []string{"github.workflow_run.*"}, Until: UntilDeadline, Created: e.now(), Expires: e.now().Add(DefaultLifetime),
		Told:    map[string]string{"github.workflow_run.completed": "55:1"},
		Pending: []Event{{Type: "github.workflow_run.failed", Version: "55:1", Summary: "x", Subject: "github:o/r/actions/runs/55", Source: ActionsKind}},
	}}
	e.state.Subjects["github:o/r/actions/runs/55"] = SubjectState{}
	e.save()

	sink := &fakeSink{answer: Ready}
	e2 := New(st, sink, a)
	ctx := context.Background()
	e2.Tick(ctx)
	e2.Tick(ctx)
	// completed was told before (no repeat); failed was pending and is delivered exactly once.
	if len(sink.told) != 1 || sink.told[0] != "s:ci.run.failed:55:1" {
		t.Fatalf("told %v", sink.told)
	}
	got := e2.state.Subs[0]
	if got.Types[0] != "ci.run.*" || got.Told["ci.run.completed"] != "55:1" {
		t.Fatalf("not migrated: %+v", got)
	}
	if _, left := got.Told["github.workflow_run.completed"]; left {
		t.Fatalf("old key left: %v", got.Told)
	}
}

// When state holds both spellings of a pending type, the newer (new-name) event is the one kept.
func TestMigrationKeepsTheNewNamePendingEvent(t *testing.T) {
	e := New(&memStore{}, &fakeSink{}, &GitHubActions{})
	e.load()
	e.state.Subs = []Subscription{{ID: "x", Source: ActionsKind, Types: []string{"ci.run.*"},
		Told:    map[string]string{"github.workflow_run.completed": "old", "ci.run.completed": "new"},
		Pending: []Event{{Type: "github.workflow_run.completed", Version: "1:1"}, {Type: "ci.run.completed", Version: "2:1"}}}}
	e.migrateAliasedLocked()
	s := e.state.Subs[0]
	if len(s.Pending) != 1 || s.Pending[0].Version != "2:1" || s.Told["ci.run.completed"] != "new" || len(s.Told) != 1 {
		t.Fatalf("%+v", s)
	}
}
