package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

const workspacePresetPath = "/api/admin/mcp-servers/google-workspace"

func TestWorkspaceMCPPresetRouteTenantAuthorization(t *testing.T) {
	t.Parallel()
	st, mgr, sales, _, member := networkFixture(t)
	ctx := context.Background()
	other, err := st.CreateTenant(ctx, "other", "Other")
	if err != nil {
		t.Fatal(err)
	}
	const admin = "boss@acme.co.jp"
	mux := http.NewServeMux()
	registerMCPServerRoutes(mux, config{mgr: mgr})
	post := func(email, selected, target string) *httptest.ResponseRecorder {
		body := `{"tenant_slug":"` + target + `","client_id":"test-client","client_secret":"test-secret","services":["gmail","drive"],"bundle":"read_only"}`
		return postMCPPresetTest(mux, workspacePresetPath, email, selected, body)
	}
	assertTenant := func(tenant store.Tenant, count int) {
		t.Helper()
		rows, err := st.ListMCPServers(ctx, tenant.ID)
		if err != nil || len(rows) != count {
			t.Fatalf("tenant %s: %d definitions, want %d: %v", tenant.Slug, len(rows), count, err)
		}
		audit, err := st.ListAuditByTenant(ctx, tenant.ID, 20)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, row := range audit {
			if row.Action == "mcp.upsert" {
				n++
				if row.TenantID != tenant.ID || row.ActorKind != "admin" || row.ActorID == "" || strings.Contains(row.Detail, "test-secret") {
					t.Fatalf("incorrect MCP upsert audit row (tenant %s, actor kind %s)", row.TenantID, row.ActorKind)
				}
			}
		}
		if n != count {
			t.Fatalf("tenant %s: %d audited upserts, want %d", tenant.Slug, n, count)
		}
	}

	if w := post(admin, sales.Slug, other.Slug); w.Code != http.StatusForbidden {
		t.Fatalf("admin targeting a different tenant: %d %s", w.Code, w.Body)
	}
	if w := post(member.Email, sales.Slug, sales.Slug); w.Code != http.StatusForbidden {
		t.Fatalf("member targeting their own tenant: %d %s", w.Code, w.Body)
	}
	assertTenant(sales, 0)
	assertTenant(other, 0)

	if w := post(admin, other.Slug, sales.Slug); w.Code != http.StatusCreated {
		t.Fatalf("admin targeting their own tenant: %d %s", w.Code, w.Body)
	}
	assertTenant(sales, 2)
	assertTenant(other, 0)
}

func TestWorkspaceMCPPresetRouteConflictsWithManualCreate(t *testing.T) {
	t.Parallel()
	st, mgr, tenant, _, _ := networkFixture(t)
	mux := http.NewServeMux()
	registerMCPServerRoutes(mux, config{mgr: mgr})
	const admin = "boss@acme.co.jp"
	const manualBody = `{"name":"Google-DRIVE","url":"https://mcp.example.com/mcp","enabled":true}`
	const presetBody = `{"client_id":"test-client","client_secret":"test-secret","services":["gmail","drive"],"bundle":"read_only"}`
	start := make(chan struct{})
	manualResult := make(chan *httptest.ResponseRecorder, 1)
	presetResult := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		<-start
		manualResult <- postMCPPresetTest(mux, "/api/admin/mcp-servers", admin, tenant.Slug, manualBody)
	}()
	go func() {
		<-start
		presetResult <- postMCPPresetTest(mux, workspacePresetPath, admin, tenant.Slug, presetBody)
	}()
	close(start)
	manual, preset := <-manualResult, <-presetResult
	rows, err := st.ListMCPServers(context.Background(), tenant.ID)
	if err != nil {
		t.Fatal(err)
	}
	switch {
	case manual.Code == http.StatusOK && preset.Code == http.StatusConflict:
		if len(rows) != 1 || rows[0].Name != "Google-DRIVE" || accessErrCode(preset) != "mcp_name_taken" {
			t.Fatalf("manual create won: %d rows, conflict %s", len(rows), preset.Body)
		}
	case manual.Code == http.StatusConflict && preset.Code == http.StatusCreated:
		if len(rows) != 2 || rows[0].Name != "google-drive" || rows[1].Name != "google-gmail" || accessErrCode(manual) != "mcp_name_taken" {
			t.Fatalf("preset create won: %d rows, conflict %s", len(rows), manual.Body)
		}
	default:
		t.Fatalf("manual create %d %s, preset create %d %s", manual.Code, manual.Body, preset.Code, preset.Body)
	}
}

func postMCPPresetTest(mux *http.ServeMux, path, email, selected, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("X-Forwarded-Email", email)
	r.Header.Set("X-AF-Tenant", selected)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}
