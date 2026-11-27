package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/pgtest"
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

func TestWorkspaceMCPPresetRefusesDeletedTenant(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		checkWorkspaceMCPPresetRefusesDeletedTenant(t, p3Store(t))
	})
	t.Run("postgres", func(t *testing.T) {
		url, ok := pgtest.Schema(t)
		if !ok {
			t.Skip("set AF_TEST_DATABASE_URL to run against Postgres")
		}
		st, err := store.OpenPostgres(url)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close() })
		if err := st.Migrate(t.Context()); err != nil {
			t.Fatal(err)
		}
		checkWorkspaceMCPPresetRefusesDeletedTenant(t, st)
	})
}

func checkWorkspaceMCPPresetRefusesDeletedTenant(t *testing.T, st *store.SQL) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	tenant, err := st.CreateTenant(ctx, "gone", "Gone")
	if err != nil {
		t.Fatal(err)
	}
	const admin = "root@example.com"
	mgr := p3Manager(t, st)
	mgr.authMode = "proxy"
	mgr.superAdmins = map[string]bool{admin: true}
	mgr.master32 = bytes.Repeat([]byte{42}, 32)
	entered, resume := make(chan struct{}), make(chan struct{})
	mgr.custodian = presetPauseCustodian{newLocalCustodian(mgr.master32), entered, resume}
	mux := http.NewServeMux()
	registerMCPServerRoutes(mux, config{mgr: mgr})

	body := `{"tenant_slug":"gone","client_id":"test-client","client_secret":"test-secret","services":["gmail","drive"],"bundle":"read_only"}`
	r := httptest.NewRequestWithContext(ctx, http.MethodPost, workspacePresetPath, strings.NewReader(body))
	r.Header.Set("X-Forwarded-Email", admin)
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		mux.ServeHTTP(w, r)
	}()
	select {
	case <-entered:
	case <-done:
		t.Fatalf("POST ended before sealing the client: %d %s", w.Code, w.Body)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := st.DeleteTenant(ctx, tenant.ID); err != nil {
		t.Fatal(err)
	}
	close(resume)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if w.Code != http.StatusNotFound || accessErrCode(w) != "no_tenant" {
		t.Fatalf("POST after tenant deletion: %d %s", w.Code, w.Body)
	}
	if rows, err := st.ListMCPServers(ctx, tenant.ID); err != nil || len(rows) != 0 {
		t.Fatalf("deleted tenant retained %d definitions: %v", len(rows), err)
	}
	if audit, err := st.ListAuditByTenant(ctx, tenant.ID, 20); err != nil || len(audit) != 0 {
		t.Fatalf("refused save left %d audit rows: %v", len(audit), err)
	}
}

// presetPauseCustodian holds the POST after tenant authorization and before its store write.
type presetPauseCustodian struct {
	KeyCustodian
	entered, resume chan struct{}
}

func (c presetPauseCustodian) Wrap(ctx context.Context, keyRef string, data []byte) (string, error) {
	close(c.entered)
	select {
	case <-c.resume:
		return c.KeyCustodian.Wrap(ctx, keyRef, data)
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
