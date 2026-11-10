package sessionx

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/watch"
)

func TestWatchRegistrarSendsOnFirstChangeRenewalAndRetriesFailures(t *testing.T) {
	var sent [][]string
	var fail bool
	set := []string{}
	r := &watchRegistrar{
		put: func(_ context.Context, s []string) error {
			if fail {
				return errors.New("down")
			}
			sent = append(sent, append([]string(nil), s...))
			return nil
		},
		subjects: func() []string { return set },
	}
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()

	r.sync(ctx, now)
	if len(sent) != 1 || len(sent[0]) != 0 {
		t.Fatalf("the first call must send even an empty set (it clears a previous run's rows): %v", sent)
	}
	r.sync(ctx, now.Add(time.Minute))
	if len(sent) != 1 {
		t.Fatalf("an unchanged set was sent again: %v", sent)
	}
	set = []string{"github:o/r#1"}
	r.sync(ctx, now.Add(2*time.Minute))
	if len(sent) != 2 || sent[1][0] != "github:o/r#1" {
		t.Fatalf("first subscription not registered: %v", sent)
	}
	set = []string{}
	r.sync(ctx, now.Add(3*time.Minute))
	if len(sent) != 3 || len(sent[2]) != 0 {
		t.Fatalf("the last subscription's end was not withdrawn: %v", sent)
	}
	r.sync(ctx, now.Add(3*time.Minute+watchRegisterRenew))
	if len(sent) != 4 {
		t.Fatalf("no renewal after %v: %v", watchRegisterRenew, sent)
	}
	// A failure is retried after a minute, not on every tick.
	fail, set = true, []string{"github:o/r#2"}
	at := now.Add(time.Hour)
	r.sync(ctx, at)
	fail = false
	r.sync(ctx, at.Add(10*time.Second))
	if len(sent) != 4 {
		t.Fatalf("retried before the retry delay: %v", sent)
	}
	r.sync(ctx, at.Add(watchRegisterRetry))
	if len(sent) != 5 || sent[4][0] != "github:o/r#2" {
		t.Fatalf("not retried: %v", sent)
	}
}

func TestWatchSubjectListKeepsOnlyReferencesTheControlPlaneAccepts(t *testing.T) {
	newWatchFixture(t)
	session.WriteMeta(session.Meta{Name: "a", Kind: session.KindClaude, Dir: t.TempDir()})
	e := watch.New(watchDiskStore{}, newWatchSink(), stubSource{})
	if _, err := e.Subscribe(context.Background(), watch.Request{Owner: "a", Source: "github", Ref: "o/r#1", Types: []string{"pr.merged"}}); err != nil {
		t.Fatal(err)
	}
	if got := watchSubjectList(e); len(got) != 1 || got[0] != "github:o/r#1" {
		t.Fatalf("list = %v", got)
	}
}

func TestHandleWatchPokeReadsAHeldSubjectSoonerAndRefusesBadBodies(t *testing.T) {
	newWatchFixture(t)
	session.WriteMeta(session.Meta{Name: "a", Kind: session.KindClaude, Dir: t.TempDir()})
	e := watch.New(watchDiskStore{}, newWatchSink(), stubSource{})
	useWatchEngine(t, e)
	if _, err := e.Subscribe(context.Background(), watch.Request{Owner: "a", Source: "github", Ref: "o/r#1", Types: []string{"pr.merged"}, Until: watch.UntilDeadline}); err != nil {
		t.Fatal(err)
	}
	e.Tick(context.Background())
	before := e.List("a")[0]
	if !before.NextPoll.After(before.LastPoll.Add(30 * time.Second)) {
		t.Fatalf("setup: next poll %v is not far from the last %v", before.NextPoll, before.LastPoll)
	}
	do := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/watch/poke", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		HandleWatchPoke(rec, r)
		return rec
	}
	if rec := do(`{"source":"github","subject":"github:o/other#9"}`); rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"held":false`) {
		t.Fatalf("unheld: %d %s", rec.Code, rec.Body)
	}
	if rec := do(`{"source":"github","subject":"github:o/r#1"}`); rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"held":true`) {
		t.Fatalf("held: %d %s", rec.Code, rec.Body)
	}
	after := e.List("a")[0]
	if !after.NextPoll.Before(before.NextPoll) {
		t.Fatalf("the poke did not bring the next read forward: %v -> %v", before.NextPoll, after.NextPoll)
	}
	for name, body := range map[string]string{
		"source mismatch":  `{"source":"bitbucket","subject":"github:o/r#1"}`,
		"header injection": `{"source":"github","subject":"github:o/r#1\nreply=none"}`,
		"not a subject":    `{"source":"github","subject":"x"}`,
		"garbage":          `{{`,
	} {
		if rec := do(body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
}

// echoActions resolves a reference to itself as the subject, so a test can hold any Actions
// subject, floors included.
type echoActions struct{ stubSource }

func (echoActions) Kind() string { return watch.ActionsKind }
func (echoActions) Types() []string {
	return []string{watch.EventRunCompleted}
}
func (echoActions) Resolve(_ context.Context, ref, _ string) (watch.Resolved, error) {
	return watch.Resolved{Subject: watch.SubjectID(ref)}, nil
}
func (echoActions) Poll(context.Context, watch.SubjectID, json.RawMessage, []string) ([]watch.Event, json.RawMessage, watch.Hint, error) {
	return nil, nil, watch.Hint{After: 10 * time.Minute}, nil
}

func actionsWatchEngine(t *testing.T, refs ...string) *watch.Engine {
	t.Helper()
	newWatchFixture(t)
	session.WriteMeta(session.Meta{Name: "a", Kind: session.KindClaude, Dir: t.TempDir()})
	e := watch.New(watchDiskStore{}, newWatchSink(), stubSource{}, echoActions{})
	useWatchEngine(t, e)
	for _, ref := range refs {
		if _, err := e.Subscribe(context.Background(), watch.Request{Owner: "a", Source: watch.ActionsKind, Ref: ref, Types: []string{watch.EventRunCompleted}, Until: watch.UntilDeadline}); err != nil {
			t.Fatal(err)
		}
	}
	e.Tick(context.Background())
	return e
}

const (
	wfOld = "github:o/r/actions/workflows/ci.yml/after/100-1700000000"
	wfNew = "github:o/r/actions/workflows/ci.yml/after/250-1700009999"
	wfBr  = "github:o/r/actions/workflows/ci.yml/after/250-1700009999/branch/main"
	wfOth = "github:o/r/actions/workflows/other.yml/after/1-2"
	runS  = "github:o/r/actions/runs/77/attempts/2"
)

func TestWatchSubjectListRegistersActionsKeysWithoutTheFloor(t *testing.T) {
	e := actionsWatchEngine(t, wfOld, wfNew, wfBr, wfOth, runS)
	want := []string{
		"github:o/r/actions/runs/77/attempts/2",
		"github:o/r/actions/workflows/ci.yml",
		"github:o/r/actions/workflows/ci.yml/branch/main",
		"github:o/r/actions/workflows/other.yml",
	}
	got := watchSubjectList(e)
	sortStrings(got)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("registered\n got %q\nwant %q", got, want)
	}
}

func sortStrings(s []string) {
	for i := range s {
		for j := i + 1; j < len(s); j++ {
			if s[j] < s[i] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
}

func pokeBody(t *testing.T, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/watch/poke", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	HandleWatchPoke(rec, r)
	return rec.Code, rec.Body.String()
}

func TestHandleWatchPokeBringsActionsWatchesForward(t *testing.T) {
	e := actionsWatchEngine(t, wfOld, wfNew, wfBr, wfOth, runS)
	next := func() map[string]time.Time {
		out := map[string]time.Time{}
		for _, s := range e.List("a") {
			out[s.Ref] = s.NextPoll
		}
		return out
	}
	before := next()
	if code, body := pokeBody(t, `{"source":"github","subject":"github:o/r/actions/workflows/ci.yml"}`); code != http.StatusAccepted || !strings.Contains(body, `"held":true`) {
		t.Fatalf("workflow key: %d %s", code, body)
	}
	after := next()
	// Every floor of ci.yml moves, the branch-only watch and the other workflow do not.
	for ref, moved := range map[string]bool{wfOld: true, wfNew: true, wfBr: false, wfOth: false, runS: false} {
		if got := after[ref].Before(before[ref]); got != moved {
			t.Errorf("%s: moved=%v, want %v", ref, got, moved)
		}
	}
	if code, body := pokeBody(t, `{"source":"github","subject":"github:o/r/actions/runs/77/attempts/2"}`); code != http.StatusAccepted || !strings.Contains(body, `"held":true`) {
		t.Fatalf("run key: %d %s", code, body)
	}
	if !next()[runS].Before(before[runS]) {
		t.Error("the run watch was not brought forward")
	}
	// A key nobody holds is accepted and held:false; it moves nothing.
	if code, body := pokeBody(t, `{"source":"github","subject":"github:o/r/actions/workflows/none.yml"}`); code != http.StatusAccepted || !strings.Contains(body, `"held":false`) {
		t.Fatalf("unheld key: %d %s", code, body)
	}
}

func TestHandleWatchPokeRefusesForgedActionsKeys(t *testing.T) {
	actionsWatchEngine(t, wfOld)
	for name, body := range map[string]string{
		"floor form from outside": `{"source":"github","subject":"` + wfOld + `"}`,
		"wrong provider":          `{"source":"bitbucket","subject":"github:o/r/actions/workflows/ci.yml"}`,
		"the engine's own source": `{"source":"github_actions","subject":"github:o/r/actions/workflows/ci.yml"}`,
		"newline":                 `{"source":"github","subject":"github:o/r/actions/runs/7\nreply=none"}`,
		"not a yaml file":         `{"source":"github","subject":"github:o/r/actions/workflows/ci.sh"}`,
	} {
		if code, _ := pokeBody(t, body); code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, code)
		}
	}
}
