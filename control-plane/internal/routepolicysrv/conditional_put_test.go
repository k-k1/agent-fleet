package routepolicysrv

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
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
			// Nothing changed: the same revision still saves.
			w = routeAdminDoIfMatch(mux, "PUT", c.path, c.who, `{"deny":["x-not-a-route"]}`, strconv.FormatInt(rev2, 10))
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
	if rows := auditRowsOf(t, st, tn.ID, "route_policy.tenant.put"); len(rows) != 3 {
		t.Errorf("tenant audit rows = %d, want 3 (two conditional saves and one unconditional)", len(rows))
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
		for _, bad := range []string{"abc", `"1`, "*", `W/"1"`, "-1", "1.5", "", "01", "1, 2", "99999999999999999999"} {
			w := routeAdminDoIfMatch(mux, "PUT", path.p, path.who, `{}`, bad)
			if w.Code != http.StatusBadRequest || errCodeOf(t, w.Body.String()) != "bad_request" {
				t.Errorf("%s If-Match %q: %d %s, want 400", path.p, bad, w.Code, w.Body)
			}
		}
	}
}

// Two saves of the same loaded revision, sent at once to the real handler over a real store: one
// lands, the other is a 409, and the stored rule is the winner's.
func TestRoutePolicyConcurrentSavesOnOneRevision(t *testing.T) {
	_, mux, tn := routePolicyFixture(t, "")
	path := "/api/admin/tenants/" + tn.Slug + "/route-policy"
	for round := 0; round < 5; round++ {
		g := routeAdminDo(mux, "GET", path, "boss-x", "")
		base := decodePolicy(t, g.Body.String()).Tenant.Revision
		var wg sync.WaitGroup
		codes := make([]int, 6)
		start := make(chan struct{})
		for i := range codes {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				for try := 0; try < 50; try++ {
					w := routeAdminDoIfMatch(mux, "PUT", path, "boss-x", `{"deny":["openai-api"]}`, strconv.FormatInt(base, 10))
					if w.Code != http.StatusInternalServerError { // engine lock contention: retry
						codes[i] = w.Code
						return
					}
				}
			}()
		}
		close(start)
		wg.Wait()
		ok, conflict := 0, 0
		for _, c := range codes {
			switch c {
			case 200:
				ok++
			case http.StatusConflict:
				conflict++
			}
		}
		if ok != 1 || conflict != len(codes)-1 {
			t.Fatalf("round %d: statuses %v, want one 200 and the rest 409", round, codes)
		}
	}
}
