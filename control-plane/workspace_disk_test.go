package main

import (
	"context"
	"errors"
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
	st, _, _, _, wsRow := usageHourFixture(t)
	newer := store.WorkspaceDisk{WorkspaceID: wsRow.ID, Used: 2, Total: 10, ObservedAt: "2026-10-20T10:05:00Z"}
	older := store.WorkspaceDisk{WorkspaceID: wsRow.ID, Used: 1, Total: 10, ObservedAt: "2026-10-20T10:00:00Z"}
	for _, d := range []store.WorkspaceDisk{newer, older} {
		if err := st.PutWorkspaceDisk(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	if got, _, _ := st.GetWorkspaceDisk(ctx, wsRow.ID); got != newer {
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

// limitFailStore fails the quota read only.
type limitFailStore struct{ store.Store }

func (limitFailStore) GetUserLimit(context.Context, string) (store.UserLimit, bool, error) {
	return store.UserLimit{}, false, errors.New("db busy")
}

// A du that worked but a quota read that failed must not store total=0 over the last good
// denominator: a full disk would read as "no quota".
func TestSampleKeepsTheLastDiskReadingWhenTheQuotaCannotBeRead(t *testing.T) {
	ctx := context.Background()
	st, mgr, _, _, ws := usageHourFixture(t)
	mgr.dataRoot = t.TempDir()
	home := filepath.Join(mgr.rootedDataDir(ws), "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	diskUsages.m = map[string]diskSample{}
	old := store.WorkspaceDisk{WorkspaceID: ws.ID, Used: 5, Total: 10 * gib, ObservedAt: "2026-10-01T00:00:00Z"}
	if err := st.PutWorkspaceDisk(ctx, old); err != nil {
		t.Fatal(err)
	}
	mgr.store = limitFailStore{st}
	srv := diskAgent(t, `{}`)
	mgr.rtFactory = sizedFactory{stubFactory{rt: stubRuntime{endpoint: srv.URL}}, runtime.DiskMeaningQuota}

	newUsageSampler(mgr, 5*time.Minute).sample(ctx)

	if d, _, _ := st.GetWorkspaceDisk(ctx, ws.ID); d != old {
		t.Errorf("disk = %+v, want the earlier reading %+v", d, old)
	}
}

// A du that never returns is cut off per workspace: the earlier reading stays and the rest of
// the sweep (usage row, heartbeat) still happens.
func TestSampleCutsOffASlowDuAndFinishesTheSweep(t *testing.T) {
	ctx := context.Background()
	st, mgr, _, mem, ws := usageHourFixture(t)
	mgr.dataRoot = t.TempDir()
	diskUsages.m = map[string]diskSample{}
	old := store.WorkspaceDisk{WorkspaceID: ws.ID, Used: 5, Total: 10, ObservedAt: "2026-10-01T00:00:00Z"}
	if err := st.PutWorkspaceDisk(ctx, old); err != nil {
		t.Fatal(err)
	}
	prevDU, prevTO := runDU, usageDiskTimeout
	t.Cleanup(func() { runDU, usageDiskTimeout = prevDU, prevTO })
	runDU = func(ctx context.Context, _ string) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	usageDiskTimeout = 50 * time.Millisecond
	srv := diskAgent(t, `{}`)
	mgr.rtFactory = sizedFactory{stubFactory{rt: stubRuntime{endpoint: srv.URL}}, runtime.DiskMeaningQuota}

	done := make(chan struct{})
	go func() {
		newUsageSampler(mgr, 5*time.Minute).sample(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the sweep is stuck behind du")
	}
	if d, _, _ := st.GetWorkspaceDisk(ctx, ws.ID); d != old {
		t.Errorf("disk = %+v, want the earlier reading", d)
	}
	rows, _ := st.ListUsageHourly(ctx, "", "2000-01-01T00", "2999-01-01T00")
	var member, beat bool
	for _, r := range rows {
		member = member || r.MembershipID == mem.ID
		beat = beat || r.MembershipID == ""
	}
	if !member || !beat {
		t.Errorf("sweep did not finish: member row %v, heartbeat %v", member, beat)
	}
}

// Withdrawn after a wipe: the old figure is gone, and a reading that began before the wipe and
// lands after it does not bring it back; a reading taken after the wipe does.
func TestClearWorkspaceDiskWithdrawsTheReadingAndRefusesOlderOnes(t *testing.T) {
	ctx := context.Background()
	st, _, _, _, ws := usageHourFixture(t)
	before := time.Now().Add(-time.Minute)
	if err := st.PutWorkspaceDisk(ctx, store.WorkspaceDisk{WorkspaceID: ws.ID, Used: 9, Total: 10, ObservedAt: store.DiskObservedAt(before)}); err != nil {
		t.Fatal(err)
	}
	if err := st.ClearWorkspaceDisk(ctx, ws.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.GetWorkspaceDisk(ctx, ws.ID); ok {
		t.Fatal("reading survived the clear")
	}
	if err := st.PutWorkspaceDisk(ctx, store.WorkspaceDisk{WorkspaceID: ws.ID, Used: 9, Total: 10, ObservedAt: store.DiskObservedAt(before)}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.GetWorkspaceDisk(ctx, ws.ID); ok {
		t.Fatal("a reading taken before the wipe came back")
	}
	if err := st.PutWorkspaceDisk(ctx, store.WorkspaceDisk{WorkspaceID: ws.ID, Used: 1, Total: 10, ObservedAt: store.DiskObservedAt(time.Now().Add(time.Second))}); err != nil {
		t.Fatal(err)
	}
	if d, ok, _ := st.GetWorkspaceDisk(ctx, ws.ID); !ok || d.Used != 1 {
		t.Errorf("a later reading was refused: %+v ok=%v", d, ok)
	}
}

// Both admin Clean home and the member's wipe withdraw the reading.
func TestHomeWipesWithdrawTheDiskReading(t *testing.T) {
	ctx := context.Background()
	seed := func(t *testing.T, st store.Store, wsID string) {
		t.Helper()
		if err := st.PutWorkspaceDisk(ctx, store.WorkspaceDisk{WorkspaceID: wsID, Used: 9, Total: 10,
			ObservedAt: store.DiskObservedAt(time.Now().Add(-time.Hour))}); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("admin Clean home", func(t *testing.T) {
		st, ws, mgr := reaperLifecycleFixture(t)
		seed(t, st, ws.ID)
		mgr.rtFactory = fixedRuntimeFactory{&reachableHomeRuntime{unreachableHomeRuntime: unreachableHomeRuntime{rec: &wipeRecorder{}, state: "running"}}}
		if err := mgr.cleanHomeByMembership(ctx, ws.MembershipID); err != nil {
			t.Fatal(err)
		}
		if d, ok, _ := st.GetWorkspaceDisk(ctx, ws.ID); ok {
			t.Errorf("reading kept after Clean home: %+v", d)
		}
	})
	t.Run("member Clean home", func(t *testing.T) {
		mgr, res := wipeResolved(t, &reachableHomeRuntime{unreachableHomeRuntime: unreachableHomeRuntime{rec: &wipeRecorder{}, state: "running"}})
		seed(t, mgr.store, res.ws.ID)
		if w := callMemberWipe(newWorkspaceAPI(mgr, false), "clean-home", res); w.Code != http.StatusOK {
			t.Fatalf("clean-home = %d %s", w.Code, w.Body.String())
		}
		if d, ok, _ := mgr.store.GetWorkspaceDisk(ctx, res.ws.ID); ok {
			t.Errorf("reading kept after the member's wipe: %+v", d)
		}
	})
}

// A sweep still waiting on the Agent when the workspace is destroyed must not recreate the row.
func TestSampleDoesNotResurrectTheDiskRowOfADestroyedWorkspace(t *testing.T) {
	ctx := context.Background()
	st, mgr, _, _, ws := usageHourFixture(t)
	arrived := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sessions":
			_, _ = w.Write([]byte(`{"sessions":[]}`))
		case "/workspace/stats":
			close(arrived)
			<-release
			_, _ = w.Write([]byte(`{"disk_used":9,"disk_total":10}`))
		}
	}))
	defer srv.Close()
	mgr.rtFactory = sizedFactory{stubFactory{rt: stubRuntime{endpoint: srv.URL}}, runtime.DiskMeaningHome}
	done := make(chan struct{})
	go func() {
		newUsageSampler(mgr, 5*time.Minute).sample(ctx)
		close(done)
	}()
	<-arrived
	if err := st.DeleteWorkspace(ctx, ws.ID); err != nil {
		t.Fatal(err)
	}
	close(release)
	<-done
	var n int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM workspace_disk WHERE workspace_id=?`, ws.ID).Scan(&n); err != nil || n != 0 {
		t.Errorf("workspace_disk rows for the destroyed workspace = %d (err %v), want 0", n, err)
	}
}

// du results are cached for a minute. A wipe must drop the cached size of that home, or the next
// sample reads the old figure and, stamped after the wipe, outranks the cleared mark.
func TestWipeDropsTheCachedDuSoTheNextSampleSeesAnEmptyHome(t *testing.T) {
	ctx := context.Background()
	st, mgr, _, _, ws := usageHourFixture(t)
	mgr.dataRoot = t.TempDir()
	diskUsages.m = map[string]diskSample{}
	home := filepath.Join(mgr.rootedDataDir(ws), "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	blob := filepath.Join(home, "blob")
	if err := os.WriteFile(blob, make([]byte, 200000), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := diskAgent(t, `{}`)
	rt := &reachableHomeRuntime{unreachableHomeRuntime: unreachableHomeRuntime{rec: &wipeRecorder{}, state: "running"}}
	mgr.rtFactory = sizedFactory{stubFactory{rt: stubRuntime{endpoint: srv.URL}}, runtime.DiskMeaningQuota}
	sampler := newUsageSampler(mgr, 5*time.Minute)

	sampler.sample(ctx) // fills the du cache with the full size
	if d, _, _ := st.GetWorkspaceDisk(ctx, ws.ID); d.Used < 200000 {
		t.Fatalf("setup: used = %d", d.Used)
	}
	if err := os.Remove(blob); err != nil { // what the wipe did to the files
		t.Fatal(err)
	}
	if err := mgr.eraseHome(ctx, ws, rt); err != nil {
		t.Fatal(err)
	}
	sampler.sample(ctx)

	d, ok, _ := st.GetWorkspaceDisk(ctx, ws.ID)
	if !ok || d.Used >= 200000 {
		t.Errorf("after the wipe: %+v ok=%v, want the emptied size", d, ok)
	}
}

// A du that began before the wipe must not refill the cache when it finishes after it.
func TestDuCacheRefusesAResultThatStartedBeforeTheWipe(t *testing.T) {
	c := &diskUsageCache{m: map[string]diskSample{}}
	start := time.Now()
	c.forget("h", start.Add(time.Second))
	c.put("h", 999, start)
	if _, ok := c.get("h", start.Add(2*time.Second)); ok {
		t.Error("a pre-wipe du result was cached")
	}
	c.put("h", 1, start.Add(2*time.Second))
	if v, ok := c.get("h", start.Add(3*time.Second)); !ok || v != 1 {
		t.Errorf("a post-wipe result was refused: %d %v", v, ok)
	}
}

// A disk read that blocks must not hold up the accounting: when du is first entered, every
// running workspace's usage row and the heartbeat are already written.
func TestSlowDiskReadDoesNotDelayUsageAccounting(t *testing.T) {
	ctx := context.Background()
	st, mgr, tn, _, _ := usageHourFixture(t)
	id2, _ := st.UpsertIdentity(ctx, "x@acme.co.jp", "x-acme-co-jp", "")
	mem2, err := st.EnsureMembership(ctx, id2.ID, tn.ID, "member")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateWorkspace(ctx, store.Workspace{ID: "W-2", TenantID: tn.ID, MembershipID: mem2.ID,
		ContainerName: "af-ws-sales-x", DataDir: "/srv/data/sales/x", AgentPort: "7732", AgentToken: "tok",
		State: "running", CreatedAt: store.NowTS()}); err != nil {
		t.Fatal(err)
	}
	mgr.dataRoot = t.TempDir()
	diskUsages.m = map[string]diskSample{}
	prevDU := runDU
	t.Cleanup(func() { runDU = prevDU })
	var rowsAtFirstDU = -1
	runDU = func(c context.Context, _ string) ([]byte, error) {
		if rowsAtFirstDU < 0 {
			rows, _ := st.ListUsageHourly(ctx, "", "2000-01-01T00", "2999-01-01T00")
			rowsAtFirstDU = len(rows) // two members + the heartbeat when nothing waited on du
		}
		<-c.Done()
		return nil, c.Err()
	}
	srv := diskAgent(t, `{}`)
	mgr.rtFactory = sizedFactory{stubFactory{rt: stubRuntime{endpoint: srv.URL}}, runtime.DiskMeaningQuota}

	u := newUsageSampler(mgr, 5*time.Minute)
	u.diskBudget = 300 * time.Millisecond
	started := time.Now()
	u.sample(ctx)

	if rowsAtFirstDU != 3 {
		t.Errorf("rows written before the first du = %d, want 3 (both workspaces + heartbeat)", rowsAtFirstDU)
	}
	if time.Since(started) > 5*time.Second {
		t.Errorf("sweep took %v: the disk budget did not bound it", time.Since(started))
	}
}

// The Agent's list of failed boot-installs is stored with the disk reading and served on the
// roster; a later clean reading withdraws it, and a stopped workspace keeps the last one.
func TestSampleStoresFailedBootInstallsAndTheRosterServesThem(t *testing.T) {
	ctx := context.Background()
	st, mgr, tn, mv, _ := networkFixture(t)
	ws := store.Workspace{ID: store.NewID(), TenantID: tn.ID, MembershipID: mv.MembershipID,
		ContainerName: "af-yamada", Network: "n", DataDir: "d", AgentPort: "1", AgentToken: "t",
		State: "running", CreatedAt: store.NowTS()}
	if err := st.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	row := func() map[string]any { return memberRows(t, mgr, "boss@acme.co.jp")["yamada-acme-co-jp"] }

	failed := diskAgent(t, `{"disk_used":9000,"disk_total":10000,"boot_install_failed":{"claude":"no_space"}}`)
	mgr.rtFactory = sizedFactory{stubFactory{rt: stubRuntime{endpoint: failed.URL, state: "running"}}, runtime.DiskMeaningHome}
	newUsageSampler(mgr, 5*time.Minute).sample(ctx)
	bf, _ := row()["boot_failed"].(map[string]any)
	if len(bf) != 1 || bf["claude"] != "no_space" {
		t.Fatalf("boot_failed = %v, want claude=no_space", row()["boot_failed"])
	}

	// Stopped: nothing is sampled, the last start's outcome stays on the card.
	mgr.rtFactory = sizedFactory{stubFactory{rt: stubRuntime{state: "stopped"}}, runtime.DiskMeaningHome}
	newUsageSampler(mgr, 5*time.Minute).sample(ctx)
	if _, ok := row()["boot_failed"]; !ok {
		t.Error("boot_failed dropped for a stopped workspace")
	}

	clean := diskAgent(t, `{"disk_used":1000,"disk_total":10000}`)
	mgr.rtFactory = sizedFactory{stubFactory{rt: stubRuntime{endpoint: clean.URL, state: "running"}}, runtime.DiskMeaningHome}
	time.Sleep(2 * time.Millisecond) // observed_at is microsecond-resolution and must not go backwards
	newUsageSampler(mgr, 5*time.Minute).sample(ctx)
	if v, ok := row()["boot_failed"]; ok {
		t.Errorf("boot_failed = %v after a clean start, want it withdrawn", v)
	}
}

// Member detail polls memberStats: the Agent's failed list must pass through the CP, and an
// Agent that reports none (or an older one) must leave the key out.
func TestMemberStatsForwardsFailedBootInstalls(t *testing.T) {
	srv := statsAgent(t, http.StatusOK, `{"mem_used":100,"boot_install_failed":{"claude":"no_space"}}`)
	_, mgr, _, _ := destroyFixture(t, agentStatsFactory{endpoint: srv.URL, state: "running"})
	bf, _ := memberStatsJSON(t, mgr)["boot_install_failed"].(map[string]any)
	if len(bf) != 1 || bf["claude"] != "no_space" {
		t.Errorf("boot_install_failed = %v, want claude=no_space", bf)
	}

	none := statsAgent(t, http.StatusOK, `{"mem_used":100}`)
	_, mgr2, _, _ := destroyFixture(t, agentStatsFactory{endpoint: none.URL, state: "running"})
	if v, ok := memberStatsJSON(t, mgr2)["boot_install_failed"]; ok {
		t.Errorf("boot_install_failed = %v, want the key absent", v)
	}
}
