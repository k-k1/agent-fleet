package imagegen

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/routepolicy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// The status lists only the providers whose call would run on a route the credential policy
// allows (ADR 0138 D10a); one whose route cannot be told is not listed under a restriction. With
// no restriction every ready provider is listed, as before.
func TestStatusHidesProvidersThePolicyForbids(t *testing.T) {
	withImagegenSession(t, session.KindClaude,
		stubProvider{id: "agy", caps: capsOf([]Op{OpGenerate})},
		stubProvider{id: "codex", caps: capsOf([]Op{OpGenerate})},
		stubProvider{id: "mystery", caps: capsOf([]Op{OpGenerate})},
	)
	oldLaunch := providerLaunch
	providerLaunch = func(p Provider) routes.Launch {
		switch p.ID() {
		case "agy":
			return routes.Launch{Route: routes.RouteAgyLogin}
		case "codex":
			return routes.Launch{Route: routes.RouteOpenAIAPI}
		}
		return routes.Launch{}
	}
	t.Cleanup(func() { providerLaunch = oldLaunch })
	ids := func() []string {
		rec := httptest.NewRecorder()
		HandleStatus(rec, httptest.NewRequest(http.MethodGet, "/imagegen/status?session=slot01", nil))
		var got statusResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, p := range got.Providers {
			out = append(out, p.ID)
		}
		if len(out) > 0 && got.Provider != out[0] {
			t.Errorf("the effective provider %q is not the first listed %v", got.Provider, out)
		}
		return out
	}
	if got := ids(); len(got) != 3 {
		t.Fatalf("control: without a policy %v", got)
	}
	s := routepolicy.Open(filepath.Join(t.TempDir(), "rp.json"), "")
	if _, err := s.Accept(routepolicy.Policy{Revision: 2, Restricted: true, Allowed: []string{routes.RouteOpenAIAPI}}); err != nil {
		t.Fatal(err)
	}
	old := routepolicy.Active
	routepolicy.Active = func() *routepolicy.Store { return s }
	t.Cleanup(func() { routepolicy.Active = old })
	if got := ids(); len(got) != 1 || got[0] != "codex" {
		t.Errorf("under the policy the providers are %v, want only codex on its API key", got)
	}
}
