package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// The defaults must not touch any size the standard configurations can use today.
func TestDefaultCeilingsLeaveTheStandardSizesAlone(t *testing.T) {
	for _, p := range []struct{ cpu, disk int }{{1024, 0}, {2048, 40}, {4096, 80}, {8192, 160}, {16384, 200}} {
		if p.cpu > defaultMaxWorkspaceCPUUnits || p.disk > defaultMaxWorkspaceDiskGB {
			t.Errorf("preset cpu=%d disk=%d is above the default ceilings", p.cpu, p.disk)
		}
	}
}

func TestResolveWorkspaceSizeClampsToTheDeploymentCeilings(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tn, _ := st.EnsureDefaultTenant(ctx)
	ident, _ := st.UpsertIdentity(ctx, "a@x.com", "a-x-com", "")
	mem, _ := st.EnsureMembership(ctx, ident.ID, tn.ID, "member")
	ws := store.Workspace{MembershipID: mem.ID, TenantID: tn.ID}
	// A row stored before the ceilings existed.
	_ = st.PutUserLimit(ctx, mem.ID, store.UserQuota{CPULimit: 65536, DiskGB: 50000})

	m := &manager{store: st}
	if _, c, d := m.resolveWorkspaceSize(ctx, ws); c != 65536 || d != 50000 {
		t.Fatalf("no ceilings: cpu=%d disk=%d, want the stored values", c, d)
	}
	m.cpuMaxUnits, m.diskMaxGB = defaultMaxWorkspaceCPUUnits, defaultMaxWorkspaceDiskGB
	if _, c, d := m.resolveWorkspaceSize(ctx, ws); c != 16384 || d != 1024 {
		t.Fatalf("clamped: cpu=%d disk=%d, want 16384/1024", c, d)
	}
	_ = st.PutUserLimit(ctx, mem.ID, store.UserQuota{CPULimit: 8192, DiskGB: 160})
	if _, c, d := m.resolveWorkspaceSize(ctx, ws); c != 8192 || d != 160 {
		t.Fatalf("within ceilings: cpu=%d disk=%d, want unchanged", c, d)
	}
}

func TestSetUserLimitRejectsAboveTheDeploymentCeilings(t *testing.T) {
	ctx := context.Background()
	st, mgr, tn := userKeyFixture(t)
	mgr.cpuMaxUnits, mgr.diskMaxGB = 8192, 200
	adm := newAdminAPI(mgr)
	id, _ := st.UpsertIdentity(ctx, "m@example.com", "m-example-com", "")
	mem, err := st.EnsureMembership(ctx, id.ID, tn.ID, "member")
	if err != nil {
		t.Fatal(err)
	}
	put := func(cpu, disk string) *httptest.ResponseRecorder {
		return callAdmin(adm.setUserLimit, http.MethodPut, "/api/admin/user-limits",
			`{"tenant_slug":"sales","user_key":"m-example-com","cpu_limit":`+cpu+`,"disk_gb":`+disk+`}`)
	}
	for _, c := range []struct{ cpu, disk, want string }{
		{"16384", "100", "cpu_limit"}, {"4096", "201", "disk_gb"}, {"16384", "9999", "ceilings"},
	} {
		w := put(c.cpu, c.disk)
		if w.Code != http.StatusBadRequest || apiErrCode(t, w) != "above_deployment_ceiling" || !strings.Contains(w.Body.String(), c.want) {
			t.Errorf("cpu=%s disk=%s: %d %s, want 400 above_deployment_ceiling naming %q", c.cpu, c.disk, w.Code, w.Body.String(), c.want)
		}
	}
	if ul, ok, _ := st.GetUserLimit(ctx, mem.ID); ok && (ul.CPULimit != 0 || ul.DiskGB != 0) {
		t.Fatalf("a refused save was stored: %+v", ul)
	}
	// The ceiling itself is allowed, and so is a deployment without ceilings.
	if w := put("8192", "200"); w.Code != http.StatusOK {
		t.Fatalf("at the ceiling: %d %s", w.Code, w.Body.String())
	}
	mgr.cpuMaxUnits, mgr.diskMaxGB = 0, 0
	if w := put("65536", "5000"); w.Code != http.StatusOK {
		t.Fatalf("no ceilings: %d %s", w.Code, w.Body.String())
	}
}
