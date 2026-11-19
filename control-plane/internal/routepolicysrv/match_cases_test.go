package routepolicysrv

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/routepolicy"
)

// matchCase is one row of match-cases.json, which the Console's routePolicyCeiling.test.ts writes:
// a rule an administrator might type under an operator ceiling, and what "match the operator's
// setting" turns it into.
type matchCase struct {
	CeilingRaw string          `json:"ceilingRaw"`
	Ceiling    json.RawMessage `json:"ceiling"`
	Rule       json.RawMessage `json:"rule"`
	Matched    json.RawMessage `json:"matched"`
}

// normalized is a rule as the CP stores it, for comparing the Console's copy of a ceiling with
// what the CP parses from the same operator string.
func normalized(t *testing.T, raw []byte) string {
	t.Helper()
	var r routepolicy.Rule
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("rule %s: %v", raw, err)
	}
	b, err := json.Marshal(r.Normalize())
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestMatchCases holds the Console's "match the operator's setting" (#4376 item 5) to the CP: for
// every case, the rule as typed is refused under its ceiling with route_policy_exceeds_ceiling,
// on the deployment rule and on a tenant cap alike, and the matched rule is accepted on both.
func TestMatchCases(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(shotFixtureDir, "match-cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Cases []matchCase `json:"cases"`
	}
	if err := json.Unmarshal(b, &file); err != nil {
		t.Fatal(err)
	}
	if len(file.Cases) == 0 {
		t.Fatal("match-cases.json has no case")
	}
	for i, c := range file.Cases {
		_, mux, tn := routePolicyFixture(t, c.CeilingRaw)
		cur := currentCeiling()
		want, err := json.Marshal(cur.rule.Normalize())
		if err != nil {
			t.Fatal(err)
		}
		if got := normalized(t, c.Ceiling); got != string(want) {
			t.Fatalf("case %d: the Console's ceiling for %q is %s, the CP parses %s", i, c.CeilingRaw, got, want)
		}
		for _, path := range []string{"/api/admin/route-policy", "/api/admin/tenants/" + tn.Slug + "/route-policy/cap"} {
			w := routeAdminDo(mux, "PUT", path, "root-x", string(c.Rule))
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "route_policy_exceeds_ceiling") {
				t.Errorf("case %d %s: %s under %q: %d %s, want route_policy_exceeds_ceiling", i, path, c.Rule, c.CeilingRaw, w.Code, w.Body)
			}
			if w := routeAdminDo(mux, "PUT", path, "root-x", string(c.Matched)); w.Code != http.StatusOK {
				t.Errorf("case %d %s: matched %s under %q: %d %s, want 200", i, path, c.Matched, c.CeilingRaw, w.Code, w.Body)
			}
		}
	}
}

// TestEndpointCoverCases holds the Console's endpoint-pattern coverage check to the CP
// (endpoint-cases.json, written by routePolicyCeiling.test.ts): under a ceiling naming one
// pattern, a pattern the Console calls covered is accepted on the deployment rule and on a tenant
// cap, and one it calls uncovered is refused. The Console must never be more permissive than
// this, and where the CP refuses a pattern as malformed the Console must say "exceeds" too.
func TestEndpointCoverCases(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(shotFixtureDir, "endpoint-cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Cases []struct {
			Ceiling, Pattern string
			Covered          bool
		} `json:"cases"`
	}
	if err := json.Unmarshal(b, &file); err != nil {
		t.Fatal(err)
	}
	if len(file.Cases) == 0 {
		t.Fatal("endpoint-cases.json has no case")
	}
	byCeiling := map[string][]int{}
	for i, c := range file.Cases {
		byCeiling[c.Ceiling] = append(byCeiling[c.Ceiling], i)
	}
	for ceiling, idx := range byCeiling {
		_, mux, tn := routePolicyFixture(t, "endpoints="+ceiling)
		for _, i := range idx {
			c := file.Cases[i]
			body, _ := json.Marshal(map[string][]string{"endpoints": {c.Pattern}})
			for _, path := range []string{"/api/admin/route-policy", "/api/admin/tenants/" + tn.Slug + "/route-policy/cap"} {
				w := routeAdminDo(mux, "PUT", path, "root-x", string(body))
				if c.Covered && w.Code != http.StatusOK {
					t.Errorf("%s: %q under %q: Console says covered, CP answers %d %s", path, c.Pattern, ceiling, w.Code, w.Body)
				}
				if !c.Covered && w.Code == http.StatusOK && !mappedHost(c.Pattern) {
					t.Errorf("%s: %q under %q: Console says exceeds, CP accepts it", path, c.Pattern, ceiling)
				}
			}
		}
	}
}

// mappedHost: the CP unmaps an IPv4-mapped IPv6 host to its IPv4 address and so covers what the
// Console, on the safe side, calls uncovered.
func mappedHost(p string) bool { return strings.Contains(strings.ToLower(p), "::ffff:") }
