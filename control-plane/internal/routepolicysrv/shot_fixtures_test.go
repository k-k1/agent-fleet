package routepolicysrv

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
)

var updateShotFixtures = flag.Bool("update-shot-fixtures", false,
	"rewrite console/scripts/shots/model-access/policy-*.json from the real handlers")

// shotFixtureDir is where the screenshot harness reads the Model access pages' answers from.
const shotFixtureDir = "../../../console/scripts/shots/model-access"

// A ceiling that forbids two destinations restricts self-hosted endpoints to one domain.
const shotCeilingRestricted = "destinations=-openrouter,-sakana;endpoints=*.corp.example"

// shotPolicyScenes are the rule states the Model access scenes draw. Each is written through the
// real PUT handlers, so a rule the CP would refuse cannot become a picture, and read back through
// the real GETs, so the pictures follow the wire.
var shotPolicyScenes = []struct {
	name, ceiling string
	// puts are (path under /api/admin, body) written in order as the super_admin.
	puts [][2]string
}{
	{name: "open"},
	{name: "restricted", ceiling: shotCeilingRestricted, puts: [][2]string{
		// A cap under a ceiling has to restate what the ceiling forbids (ADR 0138 §8 M1).
		{"tenants/demo/route-policy/cap", `{"destinations":["af-engine","anthropic","aws-bedrock","cursor","github","google","kiro","meta","openai","opencode.ai","self-hosted"],"deny":["cursor-login"]}`},
		{"tenants/demo/route-policy", `{"classes":["member-key","member-cloud","provided","self-hosted"],"except":["copilot-login"],"endpoints":["llm.corp.example"]}`},
	}},
	{name: "bedrockonly", ceiling: "destinations=aws-bedrock"},
	{name: "nosubs", puts: [][2]string{
		{"route-policy", `{"classes":["member-key","member-cloud","provided","self-hosted"]}`},
		{"tenants/demo/route-policy", `{"deny":["openrouter-api"],"endpoints":["gpu.partner.example","llm.corp.example"]}`},
	}},
}

// shotTimestamp pins the layers' updatedAt, the one value that changes from run to run.
var shotTimestamp = regexp.MustCompile(`"updatedAt":"[^"]*"`)

// TestShotFixturesPolicy keeps the harness's route-policy answers equal to what the handlers
// answer now; -update-shot-fixtures rewrites them. A wire change shows up here as a diff to
// commit with the change, never as a picture drawn from a shape the CP no longer sends.
func TestShotFixturesPolicy(t *testing.T) {
	for _, sc := range shotPolicyScenes {
		t.Run(sc.name, func(t *testing.T) {
			st, mux, _ := routePolicyFixture(t, sc.ceiling)
			if _, err := st.CreateTenant(context.Background(), "demo", "Demo Team"); err != nil {
				t.Fatal(err)
			}
			for _, p := range sc.puts {
				if w := routeAdminDo(mux, "PUT", "/api/admin/"+p[0], "root-x", p[1]); w.Code != 200 {
					t.Fatalf("PUT %s: %d %s", p[0], w.Code, w.Body)
				}
			}
			get := func(path string) json.RawMessage {
				w := routeAdminDo(mux, "GET", "/api/admin/"+path, "root-x", "")
				if w.Code != 200 {
					t.Fatalf("GET %s: %d %s", path, w.Code, w.Body)
				}
				return shotTimestamp.ReplaceAll(bytes.TrimSpace(w.Body.Bytes()), []byte(`"updatedAt":"2026-11-12T03:14:00Z"`))
			}
			out := map[string]json.RawMessage{
				"deployment":  get("route-policy"),
				"tenant/demo": get("tenants/demo/route-policy"),
				"tenant/acme": get("tenants/acme/route-policy"),
			}
			compareShotFixture(t, filepath.Join(shotFixtureDir, "policy-"+sc.name+".json"), out)
		})
	}
}

// compareShotFixture writes or compares one answer per line: the files are generated, and a
// diff then names the answer that changed.
func compareShotFixture(t *testing.T, path string, m map[string]json.RawMessage) {
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
	if *updateShotFixtures {
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
		t.Errorf("%s differs from the handlers' answer; run go test -run TestShotFixturesPolicy -update-shot-fixtures ./internal/routepolicysrv/ (from control-plane) and commit the diff", path)
	}
}
