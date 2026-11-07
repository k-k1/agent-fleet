package mcpsrv

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func TestURLHostChanged(t *testing.T) {
	same := [][2]string{
		{"https://mcp.example.com/a", "https://mcp.example.com/b"},
		{"https://MCP.example.com", "https://mcp.example.com:443/x"},
		{"http://mcp.example.com", "http://mcp.example.com:80"},
		{"https://mcp.example.com/a?x=1", "https://mcp.example.com/a?x=2"},
	}
	for _, c := range same {
		if urlHostChanged(c[0], c[1]) {
			t.Errorf("%s -> %s is not a host change", c[0], c[1])
		}
	}
	diff := [][2]string{
		{"https://mcp.example.com", "https://mcp2.example.com"},
		{"https://mcp.example.com", "https://mcp.example.com:8443"},
		{"https://mcp.example.com", "https://mcp.example.net"},
		{"http://mcp.example.com", "https://mcp.example.com"}, // 80 -> 443
	}
	for _, c := range diff {
		if !urlHostChanged(c[0], c[1]) {
			t.Errorf("%s -> %s is a host change", c[0], c[1])
		}
	}
}

// Header values entered for one host are not carried to another: changing the URL host with a
// masked value is refused (nothing is stored, nothing is audited as an upsert), while a change
// that keeps the host, or one that supplies the value again, goes through.
func TestAdminURLHostChangeRefusesMaskedHeaders(t *testing.T) {
	a, ctx := newMCPServerAPITest(t, true)
	tn := store.Tenant{ID: "tenant-a", Slug: "a"}
	cp := a.cp.(testCP)
	cp.admin = &tn
	a = NewServerAPI(cp)
	send := func(id, url, label string, headers map[string]string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(mcpServerBody{TenantSlug: "a", Name: "hosted", Label: label, URL: url, Headers: headers,
			Targets: mcpTargets{Session: true}, Enabled: true})
		method := http.MethodPost
		if id != "" {
			method = http.MethodPut
		}
		r := httptest.NewRequest(method, "/api/admin/mcp-servers", strings.NewReader(string(raw)))
		r.SetPathValue("id", id)
		w := httptest.NewRecorder()
		a.AdminUpsert(w, r)
		return w
	}
	w := send("", "https://mcp.example.com/v1", "one", map[string]string{"X-Key": "k-original", "Authorization": "Bearer tok-original"})
	if w.Code != 200 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var created mcpServerBody
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	stored := func() map[string]string {
		row, _, _ := a.store.GetMCPServer(ctx, tn.ID, created.ID)
		h, err := a.openHeaders(ctx, row.HeadersEnc, row.KeyRef)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	masked := map[string]string{"X-Key": MaskedValue, "Authorization": "Bearer ***"}

	// Other fields and a same-host URL keep the stored values.
	if w := send(created.ID, "https://mcp.example.com/v2", "two", masked); w.Code != 200 {
		t.Fatalf("same host: %d %s", w.Code, w.Body)
	}
	if h := stored(); h["X-Key"] != "k-original" || h["Authorization"] != "Bearer tok-original" {
		t.Fatalf("same-host edit lost the values: %v", h)
	}

	// A different host with masked values is refused, naming the headers and no values.
	w = send(created.ID, "https://other.example.net/v1", "two", masked)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), codeMCPHeaderReenter) ||
		!strings.Contains(w.Body.String(), "X-Key") || strings.Contains(w.Body.String(), "k-original") {
		t.Fatalf("host change with masked headers: %d %s", w.Code, w.Body)
	}
	if row, _, _ := a.store.GetMCPServer(ctx, tn.ID, created.ID); row.URL != "https://mcp.example.com/v2" || stored()["X-Key"] != "k-original" {
		t.Fatalf("a refused edit changed the row: %+v", row)
	}

	// The values typed again are accepted, and the audit line records both URLs.
	if w := send(created.ID, "https://other.example.net/v1", "two", map[string]string{"X-Key": "k-new", "Authorization": "Bearer tok-new"}); w.Code != 200 {
		t.Fatalf("host change with new values: %d %s", w.Code, w.Body)
	}
	if h := stored(); h["X-Key"] != "k-new" {
		t.Fatalf("new values not stored: %v", h)
	}
	logs, _ := a.store.ListAuditByTenant(ctx, tn.ID, 20)
	found := false
	for _, l := range logs {
		if l.Action == "mcp.upsert" && strings.Contains(l.Detail, "old_url=https://mcp.example.com/v2") &&
			strings.Contains(l.Detail, "url=https://other.example.net/v1") && strings.Contains(l.Detail, "host_changed=1") {
			found = true
		}
		if strings.Contains(l.Detail, "k-new") || strings.Contains(l.Detail, "k-original") {
			t.Fatalf("a header value reached the audit line: %s", l.Detail)
		}
	}
	if !found {
		t.Fatalf("no audit line with old and new URL: %+v", logs)
	}
}
