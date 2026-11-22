package chatx

import (
	"context"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// Every chat backend declares at least one catalogued route, and nothing else does.
func TestChatBackendRoutes(t *testing.T) {
	if got, want := slices.Sorted(maps.Keys(chatBackendRoutes)), chatBackendKinds(); !slices.Equal(got, want) {
		t.Fatalf("chatBackendRoutes kinds = %v, chatBackends kinds = %v", got, want)
	}
	for k, rs := range ChatBackendRoutes() {
		if len(rs) == 0 {
			t.Errorf("chat backend %q declares no route", k)
		}
		for _, id := range rs {
			if _, ok := routes.Lookup(id); !ok {
				t.Errorf("chat backend %q declares %q, which is not in routes.Catalogue", k, id)
			}
		}
	}
}

// Every chat backend's calls are stamped with what they run on: a resolver per kind, except lcpp,
// whose call records what the engine seam answered.
func TestEveryChatBackendResolvesItsLaunch(t *testing.T) {
	for _, k := range chatBackendKinds() {
		if _, ok := chatLaunches[k]; ok == (k == lcppKind) {
			t.Errorf("chat backend %q: resolver present = %v", k, ok)
		}
	}
	for k := range chatLaunches {
		if backend(k) == nil {
			t.Errorf("resolver for %q, which has no chat backend", k)
		}
	}
}

// fakeOneShot is codex's backend with a one-shot that records that it ran, and the route and
// binding the call carried when it did.
type fakeOneShot struct {
	codexChat
	ran   *bool
	route *string
}

func (f fakeOneShot) runOneShot(_ context.Context, call *usagex.Call, _ oneShotRun) (string, error) {
	*f.ran, *f.route = true, call.Route
	call.Kind, call.OK = session.KindCodex, true
	return "ok", nil
}

// A one-shot dispatched through oneShotRunOn runs, and is recorded, while its route resolver
// hangs: the observation costs the row its route, never the call (ADR 0138 M0).
func TestOneShotIsNotHeldByAHungLaunchResolver(t *testing.T) {
	writeResolvePrefs(t, `{}`)
	t.Setenv("AF_USAGE_DIR", t.TempDir())
	t.Setenv("AF_USAGE_RECORD", "")
	release := make(chan struct{})
	oldBudget, oldResolver, oldBackend := chatObserveBudget, chatLaunches[session.KindCodex], chatBackends[session.KindCodex]
	chatObserveBudget = 50 * time.Millisecond
	chatLaunches[session.KindCodex] = func(string) routes.Launch { <-release; return routes.Launch{Route: "late"} }
	ran, route := false, "unset"
	chatBackends[session.KindCodex] = fakeOneShot{ran: &ran, route: &route}
	t.Cleanup(func() {
		close(release)
		chatObserveBudget, chatLaunches[session.KindCodex], chatBackends[session.KindCodex] = oldBudget, oldResolver, oldBackend
	})
	start := time.Now()
	out, _, _, err := oneShotRunOn(context.Background(), session.KindCodex, "title.session", OneShotShort, "", "p", "")
	if err != nil || out != "ok" || !ran {
		t.Fatalf("one-shot = %q %v ran=%v", out, err, ran)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Errorf("the one-shot waited %v on a hung resolver", el)
	}
	if route != "" {
		t.Errorf("the call carried route %q, want unresolved", route)
	}
	rows := usagex.ReadRows()
	if len(rows) != 1 || rows[0].Route != "" || rows[0].Connection != "" {
		t.Errorf("ledger = %+v, want one unresolved row", rows)
	}
}

// An opencode one-shot whose settings name no model runs the recommendation, and its route is
// resolved on that model, not on the empty one oneShotRunOn started with.
func TestOpencodeOneShotResolvesTheModelItRuns(t *testing.T) {
	writeResolvePrefs(t, `{}`)
	t.Setenv("AF_TITLE_MODEL_OPENCODE", "anthropic/claude-haiku-x")
	var asked []string
	old := opencodePinRun
	opencodePinRun = func(model string) opencode.RunPin {
		asked = append(asked, model)
		return opencode.RunPin{Launch: routes.Launch{Route: "resolved-on:" + model}}
	}
	t.Cleanup(func() { opencodePinRun = old })
	var call usagex.Call
	_, m, err := opencodeOneShotModel(&call, "", false, session.KindOpencode, OneShotShort)
	if err != nil || m == "" || call.ModelReq != m || call.Route != "resolved-on:"+m {
		t.Fatalf("model %q, call = %+v; want the route resolved on the model passed", m, call)
	}
	if len(asked) != 1 || asked[0] != m {
		t.Errorf("resolved on %v, want only %q", asked, m)
	}
}
