package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// sizedFactory is stubFactory with a declared disk meaning, the way the real adapters declare theirs.
type sizedFactory struct {
	stubFactory
	meaning string
}

func (f sizedFactory) SizingProfile() runtime.WorkspaceSizing {
	return runtime.WorkspaceSizing{Runtime: "stub", DiskMeaning: f.meaning}
}

// diskAgent plays the Agent: /sessions for the sampler's session read, /workspace/stats for statfs.
func diskAgent(t *testing.T, stats string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sessions":
			_, _ = w.Write([]byte(`{"sessions":[]}`))
		case "/workspace/stats":
			_, _ = w.Write([]byte(stats))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSampleRecordsTheAgentsStatfsWhereTheHomeIsTheMembersOwnVolume(t *testing.T) {
	ctx := context.Background()
	st, mgr, _, _, ws := usageHourFixture(t)
	srv := diskAgent(t, `{"disk_used":9000,"disk_total":10000}`)
	mgr.rtFactory = sizedFactory{stubFactory{rt: stubRuntime{endpoint: srv.URL}}, runtime.DiskMeaningHome}

	newUsageSampler(mgr, 5*time.Minute).sample(ctx)

	d, ok, err := st.GetWorkspaceDisk(ctx, ws.ID)
	if err != nil || !ok || d.Used != 9000 || d.Total != 10000 || d.ObservedAt == "" {
		t.Fatalf("disk = %+v ok=%v err=%v, want 9000 / 10000 with a time", d, ok, err)
	}
}

func TestSampleRecordsHostSideUsageAgainstTheQuota(t *testing.T) {
	ctx := context.Background()
	st, mgr, _, mem, ws := usageHourFixture(t)
	mgr.dataRoot = t.TempDir()
	home := filepath.Join(mgr.rootedDataDir(ws), "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "blob"), make([]byte, 50000), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := diskAgent(t, `{"disk_used":1,"disk_total":2}`) // must be ignored for "quota"
	mgr.rtFactory = sizedFactory{stubFactory{rt: stubRuntime{endpoint: srv.URL}}, runtime.DiskMeaningQuota}

	// No quota set: used alone, no denominator.
	newUsageSampler(mgr, 5*time.Minute).sample(ctx)
	d, ok, _ := st.GetWorkspaceDisk(ctx, ws.ID)
	if !ok || d.Used < 50000 || d.Total != 0 {
		t.Fatalf("no quota: disk = %+v ok=%v, want used >= 50000 and total 0", d, ok)
	}

	// Quota set: total is the quota.
	if err := st.PutUserLimit(ctx, mem.ID, store.UserQuota{DiskGB: 20}); err != nil {
		t.Fatal(err)
	}
	diskUsages.m = map[string]diskSample{} // du is cached per path for a minute
	newUsageSampler(mgr, 5*time.Minute).sample(ctx)
	d, _, _ = st.GetWorkspaceDisk(ctx, ws.ID)
	if d.Total != 20*gib {
		t.Errorf("with quota: total = %d, want %d", d.Total, 20*gib)
	}
}

func TestSampleRecordsNoDiskWhereTheDiskIsAWorkingDisk(t *testing.T) {
	ctx := context.Background()
	st, mgr, _, _, ws := usageHourFixture(t)
	srv := diskAgent(t, `{"disk_used":9000,"disk_total":10000}`)
	mgr.rtFactory = sizedFactory{stubFactory{rt: stubRuntime{endpoint: srv.URL}}, runtime.DiskMeaningWork}

	newUsageSampler(mgr, 5*time.Minute).sample(ctx)

	if d, ok, _ := st.GetWorkspaceDisk(ctx, ws.ID); ok {
		t.Errorf("recorded %+v for a working disk", d)
	}
}

// An Agent that cannot be reached (or has no stats route) leaves the last reading, and its age, alone.
func TestSampleKeepsTheLastDiskReadingWhenTheAgentCannotBeRead(t *testing.T) {
	ctx := context.Background()
	st, mgr, _, _, ws := usageHourFixture(t)
	old := store.WorkspaceDisk{WorkspaceID: ws.ID, Used: 5, Total: 10, ObservedAt: "2026-10-01T00:00:00Z"}
	if err := st.PutWorkspaceDisk(ctx, old); err != nil {
		t.Fatal(err)
	}
	mgr.rtFactory = sizedFactory{stubFactory{rt: stubRuntime{endpoint: "http://127.0.0.1:1"}}, runtime.DiskMeaningHome}
	newUsageSampler(mgr, 5*time.Minute).sample(ctx)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sessions" {
			_, _ = w.Write([]byte(`{"sessions":[]}`))
			return
		}
		http.NotFound(w, r) // an older Agent: no /workspace/stats
	}))
	defer srv.Close()
	mgr.rtFactory = sizedFactory{stubFactory{rt: stubRuntime{endpoint: srv.URL}}, runtime.DiskMeaningHome}
	newUsageSampler(mgr, 5*time.Minute).sample(ctx)

	if d, _, _ := st.GetWorkspaceDisk(ctx, ws.ID); d != old {
		t.Errorf("disk = %+v, want the earlier reading %+v untouched", d, old)
	}
}

// A slow sweep's older reading must not replace a newer one.
func TestPutWorkspaceDiskRefusesAnOlderReading(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	newer := store.WorkspaceDisk{WorkspaceID: "W", Used: 2, Total: 10, ObservedAt: "2026-10-20T10:05:00Z"}
	older := store.WorkspaceDisk{WorkspaceID: "W", Used: 1, Total: 10, ObservedAt: "2026-10-20T10:00:00Z"}
	for _, d := range []store.WorkspaceDisk{newer, older} {
		if err := st.PutWorkspaceDisk(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	if got, _, _ := st.GetWorkspaceDisk(ctx, "W"); got != newer {
		t.Errorf("stored %+v, want the newer reading %+v", got, newer)
	}
}

// The roster returns the stored reading from the DB; total is left out when there is none, and a
// workspace never measured has no disk key at all.
func TestAdminMembersCarryTheStoredDiskReading(t *testing.T) {
	ctx := context.Background()
	st, mgr, tn, mv, _ := networkFixture(t)
	ws := store.Workspace{ID: store.NewID(), TenantID: tn.ID, MembershipID: mv.MembershipID,
		ContainerName: "af-yamada", Network: "n", DataDir: "d", AgentPort: "1", AgentToken: "t",
		State: "stopped", CreatedAt: store.NowTS()}
	if err := st.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	mgr.rtFactory = stubFactory{rt: stubRuntime{state: "stopped"}}
	if _, ok := memberRows(t, mgr, "boss@acme.co.jp")["yamada-acme-co-jp"]["disk"]; ok {
		t.Error("disk present for a workspace never measured")
	}
	if err := st.PutWorkspaceDisk(ctx, store.WorkspaceDisk{WorkspaceID: ws.ID, Used: 7, ObservedAt: "2026-10-20T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	disk, _ := memberRows(t, mgr, "boss@acme.co.jp")["yamada-acme-co-jp"]["disk"].(map[string]any)
	if disk["used"] != float64(7) || disk["observed_at"] != "2026-10-20T10:00:00Z" {
		t.Errorf("disk = %v", disk)
	}
	if _, has := disk["total"]; has {
		t.Errorf("total present without a denominator: %v", disk)
	}
}
