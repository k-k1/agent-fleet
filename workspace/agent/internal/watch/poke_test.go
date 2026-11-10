package watch

import (
	"context"
	"testing"
	"time"
)

func pokeEngine(t *testing.T, hint Hint) (*Engine, *fakeSource, *clock) {
	t.Helper()
	src := &fakeSource{hint: hint}
	e, _, c := newTestEngine(src, &fakeSink{answer: Ready})
	if _, err := e.Subscribe(context.Background(), Request{Owner: "a", Source: "fake", Ref: "o/r#1", Types: []string{"pr.merged"}, Until: UntilDeadline}); err != nil {
		t.Fatal(err)
	}
	e.Tick(context.Background()) // the first read
	return e, src, c
}

func TestPokeBringsTheNextReadForwardAndNeverDeliversByItself(t *testing.T) {
	e, src, c := pokeEngine(t, Hint{After: 10 * time.Minute})
	c.add(time.Minute)
	e.Tick(context.Background())
	if src.polls != 1 {
		t.Fatalf("polled early without a poke: %d", src.polls)
	}
	if !e.Poke("fake", "fake:o/r#1") {
		t.Fatal("a held subject was reported as not held")
	}
	c.add(pokeMinGap)
	e.Tick(context.Background())
	if src.polls != 2 {
		t.Fatalf("the poke did not cause a read: %d polls", src.polls)
	}
}

func TestPokeForAnUnheldSubjectOrSourceDoesNothing(t *testing.T) {
	e, src, c := pokeEngine(t, Hint{After: 10 * time.Minute})
	if e.Poke("fake", "fake:o/other#9") || e.Poke("github", "fake:o/r#1") {
		t.Fatal("poked a subject nobody holds")
	}
	c.add(9 * time.Minute) // before the 10 minute hint is due
	e.Tick(context.Background())
	if src.polls != 1 {
		t.Fatalf("an unheld poke caused a read: %d", src.polls)
	}
}

func TestPokeFloodCostsOneReadPerGap(t *testing.T) {
	e, src, c := pokeEngine(t, Hint{After: 10 * time.Minute})
	for i := 0; i < 500; i++ {
		e.Poke("fake", "fake:o/r#1")
		e.Tick(context.Background())
	}
	if src.polls != 1 {
		t.Fatalf("pokes within the gap of the last read caused %d extra reads", src.polls-1)
	}
	c.add(pokeMinGap)
	for i := 0; i < 500; i++ {
		e.Poke("fake", "fake:o/r#1")
		e.Tick(context.Background())
	}
	if src.polls != 2 {
		t.Fatalf("a flood after the gap caused %d reads, want 1", src.polls-1)
	}
}

func TestPokeKeepsBackoff(t *testing.T) {
	e, src, c := pokeEngine(t, Hint{After: 10 * time.Minute})
	src.err = context.DeadlineExceeded
	c.add(11 * time.Minute)
	e.Tick(context.Background()) // fails: backoff
	n := src.polls
	e.Poke("fake", "fake:o/r#1")
	c.add(pokeMinGap)
	e.Tick(context.Background())
	if src.polls != n {
		t.Fatal("a poke cut a failing subject's backoff short")
	}
}

func TestSubjectsListsLiveWatchesOnce(t *testing.T) {
	e, _, _ := pokeEngine(t, Hint{After: time.Minute})
	if _, err := e.Subscribe(context.Background(), Request{Owner: "b", Source: "fake", Ref: "o/r#1", Types: []string{"pr.merged"}}); err != nil {
		t.Fatal(err)
	}
	if got := e.Subjects(); len(got) != 1 || got[0].Subject != "fake:o/r#1" || got[0].Source != "fake" {
		t.Fatalf("subjects = %v", got)
	}
	e.Unsubscribe("a", e.List("a")[0].ID)
	if len(e.Subjects()) != 1 {
		t.Fatal("the subject must stay while a second watch holds it")
	}
	e.Unsubscribe("b", e.List("b")[0].ID)
	if got := e.Subjects(); len(got) != 0 {
		t.Fatalf("subjects after the last watch ended: %v", got)
	}
}

func TestActionsPokeKeyDropsTheFloorOnly(t *testing.T) {
	for in, want := range map[SubjectID]string{
		"github:o/r/actions/runs/5":                                   "github:o/r/actions/runs/5",
		"github:o/r/actions/runs/5/attempts/2":                        "github:o/r/actions/runs/5/attempts/2",
		"github:o/r/actions/workflows/ci.yml/after/10-20":             "github:o/r/actions/workflows/ci.yml",
		"github:o/r/actions/workflows/Ci.yaml/after/10-20/branch/a/b": "github:o/r/actions/workflows/Ci.yaml/branch/a/b",
	} {
		got, ok := ActionsPokeKey(in)
		if !ok || got != want {
			t.Errorf("ActionsPokeKey(%q) = %q, %v; want %q", in, got, ok, want)
		}
		if !IsActionsPokeKey(got) {
			t.Errorf("the key %q of %q is not accepted by IsActionsPokeKey", got, in)
		}
	}
	for _, in := range []SubjectID{"github:o/r#1", "github:o/r/actions/workflows/ci.yml", "x"} {
		if k, ok := ActionsPokeKey(in); ok {
			t.Errorf("ActionsPokeKey(%q) = %q", in, k)
		}
	}
	if IsActionsPokeKey("github:o/r/actions/workflows/ci.yml/after/1-2") {
		t.Error("the floor form passes as a key")
	}
}

func TestPokeMatchingPokesOnlyMatchesOfTheSource(t *testing.T) {
	e, src, c := pokeEngine(t, Hint{After: 10 * time.Minute})
	if e.PokeMatching("fake", func(SubjectID) bool { return false }) || e.PokeMatching("other", func(SubjectID) bool { return true }) {
		t.Fatal("reported a subject held that nothing matched")
	}
	c.add(pokeMinGap)
	e.Tick(context.Background())
	if src.polls != 1 {
		t.Fatalf("a poke that matched nothing caused a read: %d", src.polls)
	}
	if !e.PokeMatching("fake", func(s SubjectID) bool { return s == "fake:o/r#1" }) {
		t.Fatal("matching subject not held")
	}
	e.Tick(context.Background())
	if src.polls != 2 {
		t.Fatalf("the matching poke caused no read: %d", src.polls)
	}
}

func TestIsActionsPokeKeyIsAsStrictAsTheControlPlane(t *testing.T) {
	for k, want := range map[string]bool{
		"github:o/my_repo/actions/runs/5":                     true,
		"github:O/r/actions/runs/5":                           false,
		"github:o/r/actions/workflows/ci.yml/branch/a..b":     false,
		"github:o/r/actions/workflows/ci.yml/branch/-x":       false,
		"github:o/r/actions/workflows/ci.yml/branch/x/":       false,
		"github:o/r/actions/workflows/ci.yml/branch/feat/x-1": true,
	} {
		if IsActionsPokeKey(k) != want {
			t.Errorf("IsActionsPokeKey(%q) = %v", k, !want)
		}
	}
}
