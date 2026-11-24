package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/providedroutes"
	"github.com/k-k1/agent-fleet/control-plane/internal/routegateway"
	"github.com/k-k1/agent-fleet/control-plane/internal/routeprice"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

var updateProvidedShots = flag.Bool("update-shot-fixtures", false,
	"rewrite console/scripts/shots/model-access/provided.json from the real handlers")

// shotMonth and shotStamp pin what changes from run to run: the admission month follows the
// clock, and the source's updated_at the moment it was written.
const (
	shotMonth = "2026-11"
	shotStamp = "2026-11-12T03:14:00Z"
)

var (
	shotMonthRe = regexp.MustCompile(`"month":"\d{4}-\d{2}"`)
	shotStampRe = regexp.MustCompile(`"updated_at":"[^"]*"`)
)

// TestShotFixturesProvided keeps the screenshot harness's provided-route answers equal to what
// the handlers answer, in the three states its scenes draw: no source registered, a source the
// tenant is not granted, and a grant with members, budgets and spend. -update-shot-fixtures
// rewrites the file. The spend is real: calls admitted and settled through the store, one left
// in flight so the reservation shows.
func TestShotFixturesProvided(t *testing.T) {
	ctx := context.Background()
	st, err := openMigratedSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	mgr := p3Manager(t, st)
	mgr.master32 = []byte(strings.Repeat("r", 32))
	if _, err := st.UpsertIdentity(ctx, "root@x", "root-x", "super_admin"); err != nil {
		t.Fatal(err)
	}
	tn, err := st.CreateTenant(ctx, "demo", "Demo Team")
	if err != nil {
		t.Fatal(err)
	}
	people := []string{"aoi-tanaka", "ren-sato", "mio-kubo", "haru-ito", "yui-kato", "sora-abe"}
	seats := map[string]string{}
	for i, key := range people {
		role := "member"
		if i == 0 {
			role = "tenant_admin"
		}
		id, err := st.UpsertIdentity(ctx, key+"@example.com", key, "")
		if err != nil {
			t.Fatal(err)
		}
		m, err := st.EnsureMembership(ctx, id.ID, tn.ID, role)
		if err != nil {
			t.Fatal(err)
		}
		seats[key] = m.ID
	}
	restoreAuthExemptions(t)
	prev := activeProvidedRouteGateway.Load()
	t.Cleanup(func() { activeProvidedRouteGateway.Store(prev) })
	g := routegateway.New(providedRouteDeps(mgr, nil, routegateway.Config{MaxInFlight: 4}))
	mux := http.NewServeMux()
	providedRouteRoutes(mux, mgr, g)

	base := "/api/admin/tenants/demo/provided-routes/deployment"
	do := func(method, path, body string) []byte {
		t.Helper()
		w := routeAdminDo(mux, method, path, "root@x", body)
		if w.Code != 200 {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body)
		}
		return bytes.TrimSpace(w.Body.Bytes())
	}
	out := map[string][]byte{}
	snap := func(state string) {
		out[state+"/source"] = do("GET", "/api/admin/provided-routes/deployment", "")
		out[state+"/tenant/demo"] = do("GET", base, "")
	}

	snap("nosource")
	do("PUT", "/api/admin/provided-routes/deployment", `{"name":"Shared Bedrock","role_arn":"arn:aws:iam::123456789012:role/af-provided-bedrock",`+
		`"region":"us-east-1","models":["us.anthropic.claude-opus-5-5","us.anthropic.claude-sonnet-5","us.anthropic.claude-haiku-5-5"]}`)
	snap("notgranted")

	do("PUT", base+"/grant", `{"granted":true,"ceiling_micro":500000000}`)
	do("PUT", base+"/policy", `{"members_only":true,"member_default_micro":50000000}`)
	for _, m := range []struct {
		key  string
		body string
	}{
		{"aoi-tanaka", `"granted":true`},
		{"ren-sato", `"granted":true,"budget_micro":120000000`},
		{"mio-kubo", `"granted":true`},
		{"yui-kato", `"granted":true,"budget_micro":0`},
	} {
		do("PUT", base+"/members", `{"membership_id":"`+seats[m.key]+`",`+m.body+`}`)
	}
	src, ok, err := g.DeploymentSource(ctx)
	if err != nil || !ok {
		t.Fatalf("source: %v %v", ok, err)
	}
	// Settled spend for three members, and one call of aoi-tanaka's still in flight.
	n := 0
	call := func(key string, reserved, final int64) {
		t.Helper()
		n++
		c := store.ProvidedRouteCall{CallID: fmt.Sprintf("shot-%d", n), MembershipID: seats[key], TenantID: tn.ID,
			Source: providedroutes.SourceDeployment, Binding: src.Binding(), Model: "us.anthropic.claude-opus-5-5",
			API: "invoke-with-response-stream", PriceVersion: routeprice.Version(), ReservedMicro: reserved, CPInstance: "cp-shot"}
		ceiling := int64(500_000_000)
		a, err := st.AdmitProvidedRouteCall(ctx, c, store.ProvidedRouteLimits{MemberBudgetMicro: 200_000_000, TenantCeilingMicro: &ceiling})
		if err != nil || !a.Admitted {
			t.Fatalf("admit %s: %+v %v", key, a, err)
		}
		if final >= 0 {
			if _, err := st.SettleProvidedRouteCall(ctx, c.CallID, final); err != nil {
				t.Fatal(err)
			}
		}
	}
	call("aoi-tanaka", 9_000_000, 41_200_000)
	call("ren-sato", 9_000_000, 96_050_000)
	call("mio-kubo", 9_000_000, 49_900_000)
	call("aoi-tanaka", 8_800_022, -1)
	snap("granted")

	// Ids are random per run: name them by role instead.
	repl := []string{src.ID, "src-deployment"}
	for i, key := range people {
		repl = append(repl, seats[key], fmt.Sprintf("seat-%d", i+1))
	}
	r := strings.NewReplacer(repl...)
	for k, v := range out {
		v = shotMonthRe.ReplaceAll(v, []byte(`"month":"`+shotMonth+`"`))
		v = shotStampRe.ReplaceAll(v, []byte(`"updated_at":"`+shotStamp+`"`))
		out[k] = []byte(r.Replace(string(v)))
	}
	compareProvidedShots(t, filepath.Join("..", "console", "scripts", "shots", "model-access", "provided.json"), out)
}

// compareProvidedShots writes or compares one answer per line, in key order.
func compareProvidedShots(t *testing.T, path string, m map[string][]byte) {
	t.Helper()
	keys := slices.Sorted(maps.Keys(m))
	got := []byte("{\n")
	for i, k := range keys {
		got = fmt.Appendf(got, "%q: %s", k, m[k])
		if i < len(keys)-1 {
			got = append(got, ',')
		}
		got = append(got, '\n')
	}
	got = append(got, "}\n"...)
	if *updateProvidedShots {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update-shot-fixtures)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from the handlers' answer; run go test -run TestShotFixturesProvided -update-shot-fixtures . and commit the diff", path)
	}
}
