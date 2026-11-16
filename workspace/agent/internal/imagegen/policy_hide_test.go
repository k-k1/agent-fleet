package imagegen

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/routepolicy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
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
	// The order the Console lists as the member's providers hides the same ones.
	rec := httptest.NewRecorder()
	HandleStatus(rec, httptest.NewRequest(http.MethodGet, "/imagegen/status?session=slot01", nil))
	var got statusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Order, []string{"codex"}) {
		t.Errorf("under the policy the order is %v, want only codex", got.Order)
	}
}

// A generation is judged at the point each provider would run (ADR 0138 D12): a provider on a
// forbidden route is not called and leaves no ledger row, and the order moves to an allowed one;
// with none allowed the request fails with the policy's reason, nothing having run.
func TestRunSkipsProvidersThePolicyForbids(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_USAGE_DIR", filepath.Join(home, "usage"))
	var calls []string
	withStubProvider(t,
		stubProvider{id: ProviderCodex, calls: &calls, res: Result{Provider: ProviderCodex,
			Images: []Image{{Bytes: tinyPNG(t, 4, 4), MIME: "image/png"}}}},
		stubProvider{id: "sd", calls: &calls, res: Result{Provider: "sd",
			Images: []Image{{Bytes: tinyPNG(t, 4, 4), MIME: "image/png"}}}},
	)
	oldLaunch := providerLaunch
	providerLaunch = func(p Provider) routes.Launch {
		if p.ID() == ProviderCodex {
			return routes.Launch{Route: routes.RouteChatGPTSubscription}
		}
		return routes.Launch{Route: routes.RouteAFEngineImage}
	}
	t.Cleanup(func() { providerLaunch = oldLaunch })
	s := routepolicy.Open(filepath.Join(t.TempDir(), "rp.json"), "")
	if _, err := s.Accept(routepolicy.Policy{Revision: 2, Restricted: true, Allowed: []string{routes.RouteAFEngineImage}}); err != nil {
		t.Fatal(err)
	}
	old := routepolicy.Active
	routepolicy.Active = func() *routepolicy.Store { return s }
	t.Cleanup(func() { routepolicy.Active = old })

	out, err := Run(context.Background(), Job{Session: "slot01", SID: "sid-1", Request: Request{Op: OpGenerate, Prompt: "a cat"}})
	if err != nil || out.Provider != "sd" {
		t.Fatalf("run = %+v %v, want sd", out, err)
	}
	if !reflect.DeepEqual(calls, []string{"sd"}) {
		t.Errorf("calls = %v, want the forbidden codex never called", calls)
	}
	if rows := usagex.ReadRows(); len(rows) != 1 || rows[0].Kind == "codex" {
		t.Errorf("ledger rows = %+v, want only the sd row", rows)
	}
	calls = nil
	if _, err := Run(context.Background(), Job{Session: "slot01", SID: "sid-1", Pref: ProviderCodex,
		Request: Request{Op: OpGenerate, Prompt: "a cat"}}); !errors.Is(err, ErrRoutePolicy) || len(calls) != 0 {
		t.Errorf("an explicit forbidden provider: %v (calls %v)", err, calls)
	}
}
