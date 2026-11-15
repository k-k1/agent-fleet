package routepolicy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testStore(t *testing.T) (*Store, *time.Time) {
	t.Helper()
	now := time.Unix(1_800_000_000, 0)
	s := Open(filepath.Join(t.TempDir(), "route-policy.json"))
	s.now = func() time.Time { return now }
	return s, &now
}

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
	// A restart: the copy on disk is what is held.
	r := Open(s.path)
	r.now = s.now
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
	if _, err := s.Accept(Policy{Revision: 1, Allowed: []string{}}); err != nil {
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
		// A replayed older answer is not a confirmation.
		if _, err := s.Accept(Policy{Revision: 1, Allowed: []string{}}); err != nil {
			t.Fatal(err)
		}
		if err := s.Gate(); !errors.Is(err, ErrUnconfirmed) {
			t.Errorf("a replay confirmed revision %d", p.Revision)
		}
	}
	// The next good answer lifts it at once.
	if _, err := s.Accept(Policy{Revision: 3, CeilingRestricted: true, Allowed: []string{}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Gate(); err != nil {
		t.Errorf("after the CP answered again: %v", err)
	}
}

// A copy that does not parse is read as nothing held, and the first answer replaces it.
func TestUnreadableCopy(t *testing.T) {
	p := filepath.Join(t.TempDir(), "route-policy.json")
	if err := os.WriteFile(p, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := Open(p)
	if s.State().Held {
		t.Fatal("a broken copy reads as held")
	}
	if changed, err := s.Accept(Policy{Revision: 1, Allowed: []string{}}); !changed || err != nil {
		t.Fatalf("first answer over a broken copy: %v %v", changed, err)
	}
}

// The pull: the bearer is sent, the answer parsed, a malformed one refused rather than read as an
// empty policy, and a deployment without the bridge is off, not an error.
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
	t.Setenv("AF_CP_BASE_URL", "")
	t.Setenv(EnvToken, "")
	if _, err := Fetch(context.Background()); !errors.Is(err, ErrBridgeOff) {
		t.Fatalf("no bridge: %v", err)
	}
	t.Setenv("AF_CP_BASE_URL", srv.URL)
	t.Setenv("AF_CP_INTERNAL_URL", "")
	t.Setenv(EnvToken, "afc_tok")
	s, _ := testStore(t)
	body = `{"revision":7,"restricted":true,"ceilingRestricted":false,"allowed":["bedrock"]}`
	if err := s.Sync(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	if st := s.State(); st.Policy.Revision != 7 || !st.Policy.Allows("bedrock") || st.Policy.Allows("openai-api") {
		t.Fatalf("after a pull: %+v", st)
	}
	for _, bad := range []string{`{"restricted":false,"allowed":[]}`, `{"revision":8,"restricted":false}`, `<html>`, `{"revision":-1,"allowed":[]}`} {
		body = bad
		if err := s.Sync(context.Background(), "test"); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
	if !s.State().Policy.Restricted {
		t.Error("a malformed answer loosened the held policy")
	}
	t.Setenv(EnvToken, "afc_wrong")
	if err := s.Sync(context.Background(), "test"); err == nil {
		t.Error("an unauthorised answer was taken")
	}
}

// The start environment's policy is taken before the first pull.
func TestRunTakesTheStartPolicy(t *testing.T) {
	t.Setenv("AF_CP_BASE_URL", "")
	t.Setenv(EnvToken, "")
	t.Setenv(EnvPolicy, `{"revision":3,"restricted":true,"ceilingRestricted":true,"allowed":["bedrock-deployment"]}`)
	s, _ := testStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.Run(ctx) // returns at once: no bridge to pull from
	if st := s.State(); st.Policy.Revision != 3 || !st.Restricting() || st.Policy.Allows("anthropic-api") {
		t.Fatalf("start policy: %+v", st)
	}
	t.Setenv(EnvPolicy, `{"revision":2}`)
	if _, ok := FromEnv(); ok {
		t.Error("a start policy without its allowed list was taken")
	}
}
