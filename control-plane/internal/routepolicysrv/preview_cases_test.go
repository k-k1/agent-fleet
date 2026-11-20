package routepolicysrv

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

// previewCapUnderCeiling restates what shotCeilingRestricted forbids (a cap may only narrow).
const previewCapUnderCeiling = `{"destinations":["af-engine","anthropic","aws-bedrock","cursor","github","google","kiro","meta","openai","opencode.ai","self-hosted"],"deny":["cursor-login"]}`

// previewScenes are the tenant-rule edits the Console's pre-save preview (routePolicyPreview.ts)
// is held to. Each case sets the layers up through the real PUT handlers, reads the tenant view the
// Console would have loaded, writes the edit through the real PUT, and records what the CP then
// says each owner can use. preview-cases.json is the table; the Console's test replays it.
var previewScenes = []struct {
	name, ceiling string
	puts          [][2]string // (path under /api/admin, body), written in order before the view is read
	edit          string      // the tenant rule written afterwards
}{
	{name: "open-classes-provided", edit: `{"classes":["provided"]}`},
	{name: "open-deny-route", edit: `{"deny":["openrouter-api"]}`},
	{name: "open-destinations-one", edit: `{"destinations":["anthropic"]}`},
	{name: "open-classes-none", edit: `{"classes":[]}`},
	{name: "open-except-only", edit: `{"except":["copilot-login"]}`},
	{name: "open-classes-with-except", edit: `{"classes":["member-key"],"except":["copilot-login"]}`},
	{name: "open-unapproved-host", edit: `{"classes":["member-key"],"endpoints":["gpu.partner.example"]}`},
	{name: "bedrockonly-widen-classes", ceiling: "destinations=aws-bedrock", edit: `{"classes":["member-cloud","self-hosted"]}`},
	{name: "cap-narrows-tenant-widens", puts: [][2]string{
		{"tenants/demo/route-policy/cap", `{"classes":["member-key","member-cloud"]}`},
	}, edit: `{"classes":["provided","member-key"]}`},
	{name: "restricted-drop-host", ceiling: shotCeilingRestricted, puts: [][2]string{
		{"tenants/demo/route-policy/cap", previewCapUnderCeiling},
		{"tenants/demo/route-policy", `{"classes":["member-key","self-hosted"],"endpoints":["llm.corp.example"]}`},
	}, edit: `{"classes":["member-key","self-hosted"],"endpoints":[]}`},
	{name: "restricted-keep-host-narrow-classes", ceiling: shotCeilingRestricted, puts: [][2]string{
		{"tenants/demo/route-policy/cap", previewCapUnderCeiling},
		{"tenants/demo/route-policy", `{"classes":["member-key","self-hosted"],"endpoints":["llm.corp.example"]}`},
	}, edit: `{"classes":["member-key"],"endpoints":["llm.corp.example"]}`},
	{name: "restricted-keep-host-drop-self-hosted", ceiling: shotCeilingRestricted, puts: [][2]string{
		{"tenants/demo/route-policy/cap", previewCapUnderCeiling},
		{"tenants/demo/route-policy", `{"classes":["member-key","self-hosted"],"endpoints":["llm.corp.example"]}`},
	}, edit: `{"classes":["member-key","self-hosted"],"deny":["custom-api"],"endpoints":["llm.corp.example"]}`},
	{name: "restricted-add-host", ceiling: shotCeilingRestricted, puts: [][2]string{
		{"tenants/demo/route-policy/cap", previewCapUnderCeiling},
	}, edit: `{"classes":["member-key","self-hosted"],"endpoints":["llm.corp.example"]}`},
	// Only the approved host keeps lcpp usable (af-engine-llm is denied): dropping it hides lcpp,
	// swapping it for one the CP has not judged leaves the Console unable to tell.
	{name: "host-only-lcpp-drop-host", puts: [][2]string{
		{"tenants/demo/route-policy", `{"classes":["member-key","self-hosted"],"deny":["af-engine-llm"],"endpoints":["llm.corp.example"]}`},
	}, edit: `{"classes":["member-key","self-hosted"],"deny":["af-engine-llm"],"endpoints":[]}`},
	{name: "host-only-lcpp-swap-host", puts: [][2]string{
		{"tenants/demo/route-policy", `{"classes":["member-key","self-hosted"],"deny":["af-engine-llm"],"endpoints":["llm.corp.example"]}`},
	}, edit: `{"classes":["member-key","self-hosted"],"deny":["af-engine-llm"],"endpoints":["llm2.corp.example"]}`},
	// A layer above that leaves self-hosted out stops the per-connection routes on an approved host.
	{name: "deployment-drops-self-hosted-keep-host", puts: [][2]string{
		{"tenants/demo/route-policy", `{"classes":["member-key","self-hosted"],"endpoints":["llm.corp.example"]}`},
		{"route-policy", `{"classes":["member-key","member-cloud","provided"]}`},
	}, edit: `{"classes":["member-key","self-hosted"],"deny":["anthropic-api"],"endpoints":["llm.corp.example"]}`},
	{name: "cap-denies-llamacpp-keep-host", puts: [][2]string{
		{"tenants/demo/route-policy", `{"classes":["member-key","self-hosted"],"deny":["af-engine-llm"],"endpoints":["llm.corp.example"]}`},
		{"tenants/demo/route-policy/cap", `{"deny":["member-llamacpp"]}`},
	}, edit: `{"classes":["member-key","self-hosted"],"deny":["af-engine-llm","anthropic-api"],"endpoints":["llm.corp.example"]}`},
	{name: "deployment-no-subscriptions", puts: [][2]string{
		{"route-policy", `{"classes":["member-key","member-cloud","provided","self-hosted"]}`},
	}, edit: `{"classes":["subscription","provided"]}`},
}

// TestPreviewCases keeps preview-cases.json equal to what the handlers answer now
// (-update-shot-fixtures rewrites it). The Console's routePolicyPreview.test.ts replays every
// case: the prediction for the edit must never allow a route the CP refuses.
func TestPreviewCases(t *testing.T) {
	out := map[string]json.RawMessage{}
	for _, sc := range previewScenes {
		t.Run(sc.name, func(t *testing.T) {
			st, mux, _ := routePolicyFixture(t, sc.ceiling)
			if _, err := st.CreateTenant(context.Background(), "demo", "Demo Team"); err != nil {
				t.Fatal(err)
			}
			put := func(path, body string) {
				t.Helper()
				if w := routeAdminDo(mux, "PUT", "/api/admin/"+path, "root-x", body); w.Code != 200 {
					t.Fatalf("PUT %s %s: %d %s", path, body, w.Code, w.Body)
				}
			}
			get := func() map[string]json.RawMessage {
				t.Helper()
				w := routeAdminDo(mux, "GET", "/api/admin/tenants/demo/route-policy", "root-x", "")
				if w.Code != 200 {
					t.Fatalf("GET: %d %s", w.Code, w.Body)
				}
				var m map[string]json.RawMessage
				if err := json.Unmarshal(shotTimestamp.ReplaceAll(w.Body.Bytes(), []byte(`"updatedAt":"2026-11-12T03:14:00Z"`)), &m); err != nil {
					t.Fatal(err)
				}
				return m
			}
			for _, p := range sc.puts {
				put(p[0], p[1])
			}
			before := get()
			// The vocabulary is the same in every case; the test takes it from policy-open.json.
			delete(before, "vocabulary")
			put("tenants/demo/route-policy", sc.edit)
			after := get()
			row, err := json.Marshal(map[string]any{
				"edit":   json.RawMessage(sc.edit),
				"before": before,
				"after":  map[string]json.RawMessage{"owners": after["owners"]},
			})
			if err != nil {
				t.Fatal(err)
			}
			out[sc.name] = row
		})
	}
	compareShotFixture(t, filepath.Join(shotFixtureDir, "preview-cases.json"), out)
}
