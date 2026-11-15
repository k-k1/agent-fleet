package routepolicy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
)

const memberLlamacpp = "member-llamacpp"

// approvedHostWire is a restricted member's answer with an approved self-hosted host, as the CP
// sends it (control-plane routepolicysrv.MemberPolicy).
func approvedHostWire(rev, issued int64) string {
	return fmt.Sprintf(`{"revision":%d,"restricted":true,"ceilingRestricted":true,"allowed":["anthropic-api"],`+
		`"endpoints":["llm.lab.example.net"],"endpointRoutes":["member-llamacpp"],"issuedAt":%d}`, rev, issued)
}

func approvesTheHost(t *testing.T, what string, p Policy) {
	t.Helper()
	if !slices.Equal(p.Endpoints, []string{"llm.lab.example.net"}) || !slices.Equal(p.EndpointRoutes, []string{memberLlamacpp}) {
		t.Errorf("%s: endpoints %v, endpoint routes %v", what, p.Endpoints, p.EndpointRoutes)
	}
	if !p.AllowsAt(memberLlamacpp, "llm.lab.example.net") || p.AllowsAt(memberLlamacpp, "llm.other.example.net") {
		t.Errorf("%s: the approved host is not judged as approved", what)
	}
}

// The approved self-hosted endpoints reach the Agent from the wire: parse keeps them, and so do a
// pull, the copy a restart reads back, and the start environment. Dropped anywhere on the way, an
// approved host would be refused (the sandbox's live M1 run found parse dropping them).
func TestApprovedEndpointsSurviveTheWire(t *testing.T) {
	p, err := parse([]byte(approvedHostWire(4, 1)))
	if err != nil {
		t.Fatal(err)
	}
	approvesTheHost(t, "parse", p)
	if p, err := parse([]byte(`{"revision":4,"restricted":true,"ceilingRestricted":false,"allowed":[]}`)); err != nil || p.Endpoints != nil || p.EndpointRoutes != nil {
		t.Errorf("an answer with no endpoints: %+v %v", p, err)
	}

	// A pull, then a restart reading the copy back.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, approvedHostWire(7, 1))
	}))
	t.Cleanup(srv.Close)
	resetToken := func() { tokenOnce, token = sync.Once{}, "" }
	t.Cleanup(resetToken)
	resetToken()
	t.Setenv("AF_CP_BASE_URL", srv.URL)
	t.Setenv("AF_CP_INTERNAL_URL", "")
	t.Setenv(EnvToken, "afc_tok")
	s, now := testStore(t)
	if err := s.Sync(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	approvesTheHost(t, "after a pull", s.State().Policy)
	approvesTheHost(t, "after a restart", reopen(s).State().Policy)

	// The start environment of a new container.
	s2, _ := testStore(t)
	t.Setenv(EnvPolicy, approvedHostWire(9, now.Unix()))
	s2.seedFromEnv()
	approvesTheHost(t, "from the start environment", s2.State().Policy)
}

// A copy written before parse kept the endpoints holds the current revision without them. An
// answer at that same revision, live or the start environment's, repairs it, and the repair
// survives a restart; an older answer (a replay) repairs nothing.
func TestAnEndpointLessCopyIsRepairedAtTheSameRevision(t *testing.T) {
	truncated, err := parse([]byte(`{"revision":7,"restricted":true,"ceilingRestricted":true,"allowed":["anthropic-api"]}`))
	if err != nil {
		t.Fatal(err)
	}
	full, err := parse([]byte(approvedHostWire(7, 1)))
	if err != nil {
		t.Fatal(err)
	}
	older, err := parse([]byte(approvedHostWire(6, 1)))
	if err != nil {
		t.Fatal(err)
	}
	for _, how := range []string{"live", "start environment"} {
		s, now := testStore(t)
		if _, err := s.Accept(truncated); err != nil {
			t.Fatal(err)
		}
		r := reopen(s)
		if r.State().Policy.AllowsAt(memberLlamacpp, "llm.lab.example.net") {
			t.Fatal("the premise: the old copy refuses the approved host")
		}
		if _, err := r.Accept(older); err != nil {
			t.Fatal(err)
		}
		if r.State().Policy.AllowsAt(memberLlamacpp, "llm.lab.example.net") {
			t.Errorf("%s: an older revision repaired the copy", how)
		}
		var changed bool
		if how == "live" {
			changed, err = r.Accept(full)
		} else {
			changed, err = r.Seed(full, now.Unix())
		}
		if err != nil || !changed {
			t.Fatalf("%s: the same-revision answer changed nothing (%v)", how, err)
		}
		approvesTheHost(t, how, r.State().Policy)
		approvesTheHost(t, how+", after a restart", reopen(r).State().Policy)
	}
	WaitListeners()
}
