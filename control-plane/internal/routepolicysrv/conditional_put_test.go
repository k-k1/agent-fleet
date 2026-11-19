package routepolicysrv

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// routeAdminDoIfMatch is routeAdminDo with an If-Match header (none when ifMatch is "-").
func routeAdminDoIfMatch(mux *http.ServeMux, method, path, who, body, ifMatch string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("X-Who", who)
	if ifMatch != "-" {
		r.Header.Set("If-Match", ifMatch)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func layerRevision(t *testing.T, body string) int64 {
	t.Helper()
	var l policyLayerWire
	if err := json.Unmarshal([]byte(body), &l); err != nil {
		t.Fatalf("%s: %v", body, err)
	}
	return l.Revision
}

func errCodeOf(t *testing.T, body string) string {
	t.Helper()
	var e errorBody
	if err := json.Unmarshal([]byte(body), &e); err != nil {
		t.Fatalf("%s: %v", body, err)
	}
	return e.Error.Code
}

// Each of the three PUTs is conditional on the layer's own revision as the GET shows it: a write
// at the revision it loaded lands and answers the new one (which the next save sends, so a second
// save does not conflict with the first), a write at an older one is a 409 that changes nothing,
// and a missing header stays the unconditional write older callers make.
func TestRoutePolicyPutsAreConditional(t *testing.T) {
	st, mux, tn := routePolicyFixture(t, "")
	for _, c := range []struct{ name, path, who string }{
		{"deployment", "/api/admin/route-policy", "root-x"},
		{"cap", "/api/admin/tenants/" + tn.Slug + "/route-policy/cap", "root-x"},
		{"tenant", "/api/admin/tenants/" + tn.Slug + "/route-policy", "boss-x"},
	} {
		t.Run(c.name, func(t *testing.T) {
			// Never written: revision 0.
			w := routeAdminDoIfMatch(mux, "PUT", c.path, c.who, `{"deny":["openai-api"]}`, `"0"`)
			if w.Code != 200 {
				t.Fatalf("first save at revision 0: %d %s", w.Code, w.Body)
			}
			rev1 := layerRevision(t, w.Body.String())
			// Save again with the revision the response carried.
			w = routeAdminDoIfMatch(mux, "PUT", c.path, c.who, `{"deny":["openai-api","anthropic-api"]}`, `"`+strconv.FormatInt(rev1, 10)+`"`)
			if w.Code != 200 {
				t.Fatalf("second save at the revision the first answered: %d %s", w.Code, w.Body)
			}
			rev2 := layerRevision(t, w.Body.String())
			if rev2 <= rev1 {
				t.Fatalf("revision %d then %d", rev1, rev2)
			}
			// Another editor still on rev1 (bare number form).
			w = routeAdminDoIfMatch(mux, "PUT", c.path, c.who, `{}`, strconv.FormatInt(rev1, 10))
			if w.Code != http.StatusConflict || errCodeOf(t, w.Body.String()) != codeRoutePolicyStale {
				t.Fatalf("stale save: %d %s, want 409 %s", w.Code, w.Body, codeRoutePolicyStale)
			}
			// The refusal changed nothing and took no revision: rev2 still saves, as the very next one.
			w = routeAdminDoIfMatch(mux, "PUT", c.path, c.who, `{"deny":["anthropic-api"]}`, strconv.FormatInt(rev2, 10))
			if w.Code != 200 || layerRevision(t, w.Body.String()) != rev2+1 {
				t.Fatalf("save at the current revision after a 409: %d %s, want 200 at revision %d", w.Code, w.Body, rev2+1)
			}
			rev3 := rev2 + 1
			// An invalid rule at the current revision is its own 400, not a 409.
			w = routeAdminDoIfMatch(mux, "PUT", c.path, c.who, `{"deny":["x-not-a-route"]}`, strconv.FormatInt(rev3, 10))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("an invalid rule at the current revision is still its own 400: %d %s", w.Code, w.Body)
			}
			// No header: unconditional, as before.
			if w = routeAdminDoIfMatch(mux, "PUT", c.path, c.who, `{}`, "-"); w.Code != 200 {
				t.Fatalf("no If-Match: %d %s", w.Code, w.Body)
			}
		})
	}
	// A refused write recorded nothing: one audit row per landed write.
	if rows := auditRowsOf(t, st, tn.ID, "route_policy.tenant.put"); len(rows) != 4 {
		t.Errorf("tenant audit rows = %d, want 4 (three conditional saves and one unconditional)", len(rows))
	}
}

// A malformed precondition is a 400, never read as no precondition; it is checked before any
// write and before the body.
func TestRoutePolicyPutRefusesMalformedIfMatch(t *testing.T) {
	_, mux, tn := routePolicyFixture(t, "")
	for _, path := range []struct{ p, who string }{
		{"/api/admin/route-policy", "root-x"},
		{"/api/admin/tenants/" + tn.Slug + "/route-policy/cap", "root-x"},
		{"/api/admin/tenants/" + tn.Slug + "/route-policy", "boss-x"},
	} {
		for _, bad := range []string{"abc", `"1`, "*", `W/"1"`, "-1", "1.5", "", "01", "1, 2", "99999999999999999999", `1"`, `""`, "+1", "-0"} {
			w := routeAdminDoIfMatch(mux, "PUT", path.p, path.who, `{}`, bad)
			if w.Code != http.StatusBadRequest || errCodeOf(t, w.Body.String()) != "bad_request" {
				t.Errorf("%s If-Match %q: %d %s, want 400", path.p, bad, w.Code, w.Body)
			}
		}
	}
}

// A repeated If-Match header is as malformed as an unparsable one.
func TestRoutePolicyPutRefusesRepeatedIfMatch(t *testing.T) {
	_, mux, _ := routePolicyFixture(t, "")
	r := httptest.NewRequest("PUT", "/api/admin/route-policy", strings.NewReader(`{}`))
	r.Header.Set("X-Who", "root-x")
	r.Header.Add("If-Match", `"0"`)
	r.Header.Add("If-Match", `"0"`)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest || errCodeOf(t, w.Body.String()) != "bad_request" {
		t.Fatalf("two If-Match headers: %d %s, want 400", w.Code, w.Body)
	}
}

// Saves of the same loaded revision, each with its own rule, sent at once to the real handler over
// a real store: one lands at base+1, the rest are 409, and the stored rule is the winner's. No
// retry: any other status is a failure.
func TestRoutePolicyConcurrentSavesOnOneRevision(t *testing.T) {
	st, mux, tn := routePolicyFixture(t, "")
	path := "/api/admin/tenants/" + tn.Slug + "/route-policy"
	routes := []string{"openai-api", "anthropic-api", "gemini-api", "openrouter-api", "copilot-login", "anthropic-subscription"}
	for round := 0; round < 5; round++ {
		g := routeAdminDo(mux, "GET", path, "boss-x", "")
		base := decodePolicy(t, g.Body.String()).Tenant.Revision
		var wg sync.WaitGroup
		codes := make([]int, len(routes))
		start := make(chan struct{})
		for i, id := range routes {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				codes[i] = routeAdminDoIfMatch(mux, "PUT", path, "boss-x", `{"deny":["`+id+`"]}`, strconv.FormatInt(base, 10)).Code
			}()
		}
		close(start)
		wg.Wait()
		winner := -1
		for i, c := range codes {
			switch c {
			case 200:
				if winner >= 0 {
					t.Fatalf("round %d: two saves landed: %v", round, codes)
				}
				winner = i
			case http.StatusConflict:
			default:
				t.Fatalf("round %d: statuses %v, want one 200 and the rest 409", round, codes)
			}
		}
		if winner < 0 {
			t.Fatalf("round %d: no save landed: %v", round, codes)
		}
		row, err := st.GetRoutePolicy(context.Background(), store.RoutePolicyTenant, tn.ID)
		if err != nil || row.Revision != base+1 || !strings.Contains(row.Rule, routes[winner]) {
			t.Fatalf("round %d: stored %+v (%v), want the winner's rule %q at revision %d", round, row, err, routes[winner], base+1)
		}
	}
}
