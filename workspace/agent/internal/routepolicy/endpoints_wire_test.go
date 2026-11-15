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
