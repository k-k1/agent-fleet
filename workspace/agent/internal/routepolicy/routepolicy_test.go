package routepolicy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testStore(t *testing.T) (*Store, *time.Time) {
	t.Helper()
	return storeAt(t, filepath.Join(t.TempDir(), "route-policy.json"))
}

func storeAt(t *testing.T, path string) (*Store, *time.Time) {
	t.Helper()
	now := time.Unix(1_800_000_000, 0)
	s := Open(path)
	s.now = func() time.Time { return now }
	return s, &now
}

func reopen(s *Store) *Store {
	r := Open(s.path)
	r.now = s.now
	return r
}

var (
	open1   = Policy{Revision: 1, Restricted: false, Allowed: []string{}}
	strict2 = Policy{Revision: 2, Restricted: true, Allowed: []string{"anthropic-api"}}
)

// Only a strictly higher revision replaces the held policy; the same revision confirms it; a lower
// one, replayed or out of order, changes nothing, its freshness included. Across a restart the
// held policy and its freshness come back from disk, so an older answer still cannot win.
func TestAcceptOnlyHigherRevisions(t *testing.T) {
	s, now := testStore(t)
	strict := Policy{Revision: 5, Restricted: true, Allowed: []string{"anthropic-api"}}
	if changed, err := s.Accept(strict); err != nil || !changed {
		t.Fatalf("first answer: %v %v", changed, err)
	}
	*now = now.Add(time.Minute)
	loose := Policy{Revision: 4, Allowed: []string{}}
	if changed, _ := s.Accept(loose); changed || s.State().Policy.Revision != 5 || s.State().LastOK.Unix() != now.Add(-time.Minute).Unix() {
		t.Fatalf("a replayed older revision was taken: %+v", s.State())
	}
	if changed, _ := s.Accept(Policy{Revision: 5, Allowed: []string{}}); changed || !s.State().Policy.Restricted || s.State().LastOK.Unix() != now.Unix() {
		t.Fatalf("the same revision must confirm, not replace: %+v", s.State())
	}
	if s.Allows("openai-api") || !s.Allows("anthropic-api") {
		t.Error("decision under the held policy")
	}
	r := reopen(s)
	if st := r.State(); !st.Held || st.Policy.Revision != 5 || !st.Policy.Restricted || st.LastOK.Unix() != now.Unix() {
		t.Fatalf("after a restart: %+v", st)
	}
	if changed, _ := r.Accept(loose); changed || !r.State().Policy.Restricted {
		t.Fatalf("after a restart an older answer replaced the held one: %+v", r.State())
	}
	if changed, _ := r.Accept(Policy{Revision: 6, Allowed: []string{}}); !changed || r.State().Policy.Restricted {
		t.Fatalf("a higher revision that loosens must be taken: %+v", r.State())
	}
}

// New work is refused once a restricting policy (a restricted member, or any ceiling) has gone
// unconfirmed past 15 minutes, and never for a member nobody restricts or before anything is held.
func TestFreshness(t *testing.T) {
	s, now := testStore(t)
	if err := s.Gate(); err != nil {
		t.Fatalf("nothing held: %v", err)
	}
	if _, err := s.Accept(open1); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(2 * time.Hour)
	if err := s.Gate(); err != nil {
		t.Errorf("an unrestricted member blocked by an unreachable CP: %v", err)
	}
	for _, p := range []Policy{{Revision: 2, Restricted: true, Allowed: []string{}}, {Revision: 3, CeilingRestricted: true, Allowed: []string{}}} {
		if _, err := s.Accept(p); err != nil {
			t.Fatal(err)
		}
		*now = now.Add(FreshnessLimit)
		if err := s.Gate(); err != nil {
			t.Errorf("revision %d at exactly the limit: %v", p.Revision, err)
		}
		*now = now.Add(time.Second)
		if err := s.Gate(); !errors.Is(err, ErrUnconfirmed) {
			t.Errorf("revision %d past the limit: %v, want %v", p.Revision, err, ErrUnconfirmed)
		}
		if _, err := s.Accept(open1); err != nil {
			t.Fatal(err)
		}
		if err := s.Gate(); !errors.Is(err, ErrUnconfirmed) {
			t.Errorf("a replay confirmed revision %d", p.Revision)
		}
	}
	if _, err := s.Accept(Policy{Revision: 3, CeilingRestricted: true, Allowed: []string{}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Gate(); err != nil {
		t.Errorf("after the CP answered again: %v", err)
	}
}

// An old start environment re-read by a restarted Agent confirms nothing: freshness comes from the
// CP answering now, the start environment counting only up to when the CP issued it.
func TestSeedConfirmsOnlyUpToIssue(t *testing.T) {
	s, now := testStore(t)
	issued := now.Unix()
	if _, err := s.Seed(Policy{Revision: 5, Restricted: true, Allowed: []string{}}, issued); err != nil {
		t.Fatal(err)
	}
	if err := s.Gate(); err != nil {
		t.Fatalf("a fresh start environment: %v", err)
	}
	*now = now.Add(FreshnessLimit + time.Minute)
	// The Agent restarts in the same container: same environment, CP unreachable.
	r := reopen(s)
	t.Setenv(EnvPolicy, fmt.Sprintf(`{"revision":5,"restricted":true,"ceilingRestricted":false,"allowed":[],"issuedAt":%d}`, issued))
	r.seedFromEnv()
	if err := r.Gate(); !errors.Is(err, ErrUnconfirmed) {
		t.Errorf("an old start environment confirmed an expired policy: %v", err)
	}
	// A start environment issued now (a new container) does confirm it.
	t.Setenv(EnvPolicy, fmt.Sprintf(`{"revision":5,"restricted":true,"ceilingRestricted":false,"allowed":[],"issuedAt":%d}`, now.Unix()))
	r.seedFromEnv()
	if err := r.Gate(); err != nil {
		t.Errorf("a start environment issued now: %v", err)
	}
}

// The copy is written behind a floor. A restart that finds the copy unreadable, or older than a
// restricting floor, holds a placeholder that allows nothing until the CP answers; with no
// restricting floor it holds nothing and blocks nothing.
func TestLostCopyFailsClosedOnlyWhenRestricting(t *testing.T) {
	for _, tc := range []struct {
		name     string
		policy   Policy
		damage   func(path string)
		closed   bool
		heldWant bool
	}{
		{"restricting, copy corrupted", strict2, func(p string) { _ = os.WriteFile(p, []byte("{"), 0o600) }, true, true},
		{"restricting, copy removed", strict2, func(p string) { _ = os.Remove(p) }, true, true},
		{"restricting, copy partial", strict2, func(p string) {
			_ = os.WriteFile(p, []byte(`{"policy":{"revision":2,"allowed":[]},"held":true,"lastOk":1}`), 0o600)
		}, true, true},
		{"restricting, floor unreadable", strict2, func(p string) { _ = os.WriteFile(floorPath(p), []byte("{"), 0o600); _ = os.Remove(p) }, true, true},
		{"unrestricted, copy corrupted", open1, func(p string) { _ = os.WriteFile(p, []byte("{"), 0o600) }, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := testStore(t)
			if _, err := s.Accept(tc.policy); err != nil {
				t.Fatal(err)
			}
			tc.damage(s.path)
			r := reopen(s)
			st := r.State()
			if st.Held != tc.heldWant || st.Placeholder != tc.closed {
				t.Fatalf("after the damage: %+v", st)
			}
			if tc.closed && (r.Allows("anthropic-api") || r.Gate() == nil) {
				t.Errorf("a placeholder must allow nothing and stay unconfirmed: %+v", st)
			}
			if !tc.closed && (!r.Allows("anthropic-api") || r.Gate() != nil) {
				t.Errorf("nothing restricted it, nothing may be blocked: %+v", st)
			}
			if tc.closed {
				// The next answer at the floor's revision replaces the placeholder.
				if changed, err := r.Accept(tc.policy); !changed || err != nil || r.State().Placeholder {
					t.Errorf("the CP's answer did not replace the placeholder: %v %v %+v", changed, err, r.State())
				}
			}
		})
	}
	// No floor and no copy: a first boot holds nothing.
	if st := Open(filepath.Join(t.TempDir(), "none.json")).State(); st.Held {
		t.Errorf("a first boot holds %+v", st)
	}
}

// A copy that fails to land behind a written floor is caught at the next start; the floor, the
// copy's bytes, its name and its directory are synced in that order.
func TestDurableWriteAndRestartOrdering(t *testing.T) {
	s, _ := testStore(t)
	if _, err := s.Accept(open1); err != nil {
		t.Fatal(err)
	}
	oldF, oldD, oldR := syncFile, syncDir, rename
	t.Cleanup(func() { syncFile, syncDir, rename = oldF, oldD, oldR })
	var events []string
	name := func(p string) string {
		switch b := filepath.Base(p); {
		case strings.Contains(b, ".floor"):
			return "floor"
		default:
			return "copy"
		}
	}
	syncFile = func(f *os.File) error { events = append(events, "sync:"+name(f.Name())); return oldF(f) }
	syncDir = func(d string) error { events = append(events, "syncdir"); return oldD(d) }
	rename = func(a, b string) error { events = append(events, "rename:"+name(b)); return oldR(a, b) }
	if _, err := s.Accept(strict2); err != nil {
		t.Fatal(err)
	}
	want := "sync:floor rename:floor syncdir sync:copy rename:copy syncdir"
	if got := strings.Join(events, " "); got != want {
		t.Errorf("order = %s, want %s", got, want)
	}
	// The copy's rename fails after the floor landed: the stricter rule is enforced now, and a
	// restart does not fall back to the unrestricted copy.
	syncFile, syncDir, rename = oldF, oldD, oldR
	strict3 := Policy{Revision: 3, Restricted: true, Allowed: []string{}}
	s2, _ := testStore(t)
	if _, err := s2.Accept(open1); err != nil {
		t.Fatal(err)
	}
	rename = func(a, b string) error {
		if name(b) == "copy" {
			return errors.New("injected")
		}
		return oldR(a, b)
	}
	if _, err := s2.Accept(strict3); err == nil {
		t.Fatal("a failed copy write was reported as recorded")
	}
	if s2.Allows("anthropic-api") {
		t.Error("the stricter policy is not enforced in memory after a failed write")
	}
	rename = oldR
	if r := reopen(s2); !r.State().Placeholder || r.Allows("openai-api") {
		t.Errorf("after a restart the older unrestricted copy came back: %+v", r.State())
	}
	// A sync failure of the copy is a failure too.
	syncFile = func(f *os.File) error {
		if name(f.Name()) == "copy" {
			return errors.New("injected")
		}
		return oldF(f)
	}
	s3, _ := testStore(t)
	if _, err := s3.Accept(strict2); err == nil {
		t.Error("an unsynced copy was reported as recorded")
	}
	syncFile = oldF
	if r := reopen(s3); !r.State().Placeholder {
		t.Errorf("after an unsynced copy and a restart: %+v", r.State())
	}
}

// Every field of an answer is required: a missing or null flag is not "unrestricted".
func TestPartialPolicyIsRejected(t *testing.T) {
	for _, bad := range []string{
		`{"revision":9,"allowed":[]}`,
		`{"revision":9,"restricted":null,"ceilingRestricted":false,"allowed":[]}`,
		`{"revision":9,"restricted":false,"allowed":[]}`,
		`{"restricted":false,"ceilingRestricted":false,"allowed":[]}`,
		`{"revision":9,"restricted":false,"ceilingRestricted":false}`,
		`{"revision":-1,"restricted":false,"ceilingRestricted":false,"allowed":[]}`,
		`<html>`,
	} {
		if _, err := parse([]byte(bad)); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
	if p, err := parse([]byte(`{"revision":9,"restricted":true,"ceilingRestricted":false,"allowed":["bedrock"],"issuedAt":1}`)); err != nil ||
		!p.Restricted || p.Revision != 9 {
		t.Errorf("a complete answer: %+v %v", p, err)
	}
}

// The pull: the bearer is sent, the answer parsed, a malformed one refused, a deployment without
// the bridge is off, and the token has left the environment children inherit.
func TestFetchAndSync(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != bridgePath || r.Header.Get("Authorization") != "Bearer afc_tok" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	resetToken := func() { tokenOnce, token = sync.Once{}, "" }
	t.Cleanup(resetToken)
	resetToken()
	t.Setenv("AF_CP_BASE_URL", "")
	t.Setenv(EnvToken, "")
	if _, err := Fetch(context.Background()); !errors.Is(err, ErrBridgeOff) {
		t.Fatalf("no bridge: %v", err)
	}
	resetToken()
	t.Setenv("AF_CP_BASE_URL", srv.URL)
	t.Setenv("AF_CP_INTERNAL_URL", "")
	t.Setenv(EnvToken, "afc_tok")
	s, _ := testStore(t)
	body = `{"revision":7,"restricted":true,"ceilingRestricted":false,"allowed":["bedrock"],"issuedAt":1}`
	if err := s.Sync(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	if _, set := os.LookupEnv(EnvToken); set {
		t.Error("the bridge token is still in the environment children inherit")
	}
	if st := s.State(); st.Policy.Revision != 7 || !st.Policy.Allows("bedrock") || st.Policy.Allows("openai-api") {
		t.Fatalf("after a pull: %+v", st)
	}
	for _, bad := range []string{`{"revision":8,"allowed":[]}`, `<html>`} {
		body = bad
		if err := s.Sync(context.Background(), "test"); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
	if !s.State().Policy.Restricted {
		t.Error("a malformed answer loosened the held policy")
	}
}

// The start environment: a policy is seeded, the unknown marker holds a placeholder, an incomplete
// one is ignored.
func TestSeedFromEnv(t *testing.T) {
	s, now := testStore(t)
	t.Setenv(EnvPolicy, fmt.Sprintf(`{"revision":3,"restricted":true,"ceilingRestricted":true,"allowed":["bedrock-deployment"],"issuedAt":%d}`, now.Unix()))
	s.seedFromEnv()
	if st := s.State(); st.Policy.Revision != 3 || !st.Restricting() || st.Policy.Allows("anthropic-api") || s.Gate() != nil {
		t.Fatalf("start policy: %+v", st)
	}
	u, _ := testStore(t)
	t.Setenv(EnvPolicy, `{"unknown":true}`)
	u.seedFromEnv()
	if st := u.State(); !st.Placeholder || u.Allows("bedrock-deployment") || u.Gate() == nil {
		t.Errorf("unknown start: %+v", st)
	}
	n, _ := testStore(t)
	for _, bad := range []string{`{"revision":2,"restricted":true,"ceilingRestricted":true,"allowed":[]}`, `{"revision":2}`} {
		t.Setenv(EnvPolicy, bad)
		n.seedFromEnv()
		if n.State().Held {
			t.Errorf("an incomplete start policy %s was taken", bad)
		}
	}
}

// The schedule: a healthy Agent pulls within 20 to 30 s, a failing one backs off from 5 s to two
// minutes, both jittered.
func TestNextDelay(t *testing.T) {
	for _, r := range []float64{0, 0.5, 0.999} {
		if d := nextDelay(0, r); d < healthyMin || d > healthyMax {
			t.Errorf("healthy delay %v with rnd %v", d, r)
		}
	}
	prevMax := time.Duration(0)
	for f := 1; f <= 10; f++ {
		lo, hi := nextDelay(f, 0), nextDelay(f, 0.999)
		if lo < backoffMin/2 || hi > backoffMax || lo > hi {
			t.Errorf("failure %d: %v..%v", f, lo, hi)
		}
		if hi < prevMax {
			t.Errorf("failure %d: backoff shrank to %v from %v", f, hi, prevMax)
		}
		prevMax = hi
	}
	if nextDelay(10, 0.999) <= nextDelay(1, 0.999) {
		t.Error("no backoff growth")
	}
}

// Run stops as soon as its context ends, wherever it is waiting.
func TestRunStopsOnCancel(t *testing.T) {
	t.Setenv("AF_CP_BASE_URL", "http://127.0.0.1:1")
	tokenOnce, token = sync.Once{}, "afc_tok"
	tokenOnce.Do(func() {})
	t.Cleanup(func() { tokenOnce, token = sync.Once{}, "" })
	s, _ := testStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	never := make(chan time.Time)
	go func() {
		s.run(ctx, func() float64 { return 0 }, func(time.Duration) <-chan time.Time { return never })
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop on cancel")
	}
}
