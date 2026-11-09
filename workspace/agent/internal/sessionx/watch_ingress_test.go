package sessionx

import (
	"context"
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
