package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/pgtest"
	"github.com/k-k1/agent-fleet/control-plane/internal/usagehist"
)

func TestUsageHistorySQLite(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	checkUsageHistory(t, st)
}

func TestUsageHistoryPostgres(t *testing.T) {
	url, ok := pgtest.Schema(t)
	if !ok {
		t.Skip("set AF_TEST_DATABASE_URL to run against Postgres")
	}
	st, err := OpenPostgres(url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	checkUsageHistory(t, st)
}

func fp(v float64) *float64 { return &v }
func up(v uint64) *uint64   { return &v }

func checkUsageHistory(t *testing.T, st *SQL) {
	t.Helper()
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tn, _ := st.EnsureDefaultTenant(ctx)
	ident, _ := st.UpsertIdentity(ctx, "a@x.com", "a-x-com", "")
	mem, err := st.EnsureMembership(ctx, ident.ID, tn.ID, "member")
	if err != nil {
		t.Fatal(err)
	}
	ident2, _ := st.UpsertIdentity(ctx, "b@x.com", "b-x-com", "")
	other, _ := st.EnsureMembership(ctx, ident2.ID, tn.ID, "member")

	day1 := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC).Unix()
	at := func(h, m int) int64 { return day1 + int64(h*3600+m*60) }
	days := func(member string) map[string]UsageDayLabelled {
		rows, err := st.ListUsageDays(ctx, "", member, "2026-11-01", "2026-11-30")
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]UsageDayLabelled{}
		for _, r := range rows {
			out[r.Day+"/"+r.InstanceType+"/"+r.Override] = r
		}
		return out
	}

	// --- launch A on a rung that declares 2 vCPU and a 40% baseline, 23:00 -> 01:00 next day.
	a := Launch{ID: "LA", WorkspaceID: "w", MembershipID: mem.ID, TenantID: tn.ID, StartedAt: at(23, 0) - 24*3600,
		InstanceType: "t3.large", VCPU: 2, BaselinePct: 40}
	if err := st.BeginLaunch(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := st.TouchLaunch(ctx, "LA", at(1, 0)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ { // folding again and again counts the seconds once
		if err := st.FoldRunning(ctx, mem.ID); err != nil {
			t.Fatal(err)
		}
	}
	d := days(mem.ID)
	if got := d["2026-11-09/t3.large/"].RunningSecs; got != 3600 {
		t.Fatalf("day before = %d s", got)
	}
	if got := d["2026-11-10/t3.large/"].RunningSecs; got != 3600 {
		t.Fatalf("day of = %d s (a launch over midnight splits at UTC midnight)", got)
	}
	if err := st.TouchLaunch(ctx, "LA", at(1, 30)); err != nil {
		t.Fatal(err)
	}
	_ = st.FoldRunning(ctx, mem.ID)
	if got := days(mem.ID)["2026-11-10/t3.large/"].RunningSecs; got != 5400 {
		t.Fatalf("after the next tick = %d s, want only the 1800 s delta added", got)
	}

	// Two folds that both read the launch before either wrote (a tick and a Stop, or two replicas)
	// hold the same stale view. Only the first may add seconds.
	if err := st.TouchLaunch(ctx, "LA", at(1, 45)); err != nil {
		t.Fatal(err)
	}
	fresh, _, _ := st.GetLaunch(ctx, "LA")
	for i := 0; i < 2; i++ {
		if err := st.foldLaunchRunning(ctx, fresh); err != nil {
			t.Fatal(err)
		}
	}
	if got := days(mem.ID)["2026-11-10/t3.large/"].RunningSecs; got != 6300 {
		t.Fatalf("two folds from the same starting point counted %d s, want 5400 + 900", got)
	}

	// --- windows: only the figures that were measured count, absent is not zero.
	ws := []UsageWindow{
		{T: at(0, 10), LID: "LA", CPUPct: fp(100), WorkingSet: up(4 << 30), Some10Max: fp(12), Full60: fp(1.5), RefaultRate: fp(3)},
		{T: at(0, 11), LID: "LA", CPUPct: fp(60), WorkingSet: up(5 << 30), Some10Max: fp(0.5), Full60: fp(0.1)},
		{T: at(0, 12), LID: "LA", WorkingSet: up(6 << 30)}, // CPU, PSI and refault unmeasured
		{T: at(0, 13), LID: "NOPE", CPUPct: fp(1)},         // unknown launch: dropped
	}
	if err := st.FoldWindows(ctx, mem.ID, 0, at(0, 13), ws); err != nil {
		t.Fatal(err)
	}
	r := days(mem.ID)["2026-11-10/t3.large/"]
	if r.CPUMeasuredMin != 2 || r.WSMeasuredMin != 3 || r.PSIMeasuredMin != 2 || r.RefaultMeasuredMin != 1 {
		t.Fatalf("measured minutes = cpu %d ws %d psi %d refault %d", r.CPUMeasuredMin, r.WSMeasuredMin, r.PSIMeasuredMin, r.RefaultMeasuredMin)
	}
	if r.PSIWarnMin != 1 || r.PSIFullMax == nil || *r.PSIFullMax != 1.5 || r.WSMax == nil || *r.WSMax != 6<<30 || r.CPUMax == nil || *r.CPUMax != 100 {
		t.Fatalf("row = %+v", r)
	}
	// 100% of a core on 2 vCPU is 50% of the box, above the 40% baseline. 60% is 30%, below.
	if r.BurstMin == nil || *r.BurstMin != 1 {
		t.Fatalf("burst minutes = %v", r.BurstMin)
	}
	if p, _ := r.CPUHist.Quantile(usagehist.CPU, 0.95); p < 60 || p > 125 {
		t.Fatalf("cpu p95 = %v", p)
	}
	// The same pull again (a second replica, a retried tick) must not count a minute twice.
	if err := st.FoldWindows(ctx, mem.ID, 0, at(0, 13), ws); !errors.Is(err, ErrUsageCursorMoved) {
		t.Fatalf("second fold from the same cursor = %v", err)
	}
	if err := st.FoldWindows(ctx, mem.ID, at(0, 12), at(0, 14), ws); !errors.Is(err, ErrUsageCursorMoved) {
		t.Fatalf("fold from a cursor that is not the stored one = %v", err)
	}
	if got := days(mem.ID)["2026-11-10/t3.large/"]; got.CPUMeasuredMin != 2 || got.WSMeasuredMin != 3 {
		t.Fatalf("a refused fold changed the row: %+v", got)
	}
	if c, _ := st.UsagePullCursor(ctx, mem.ID); c != at(0, 13) {
		t.Fatalf("cursor = %d", c)
	}

	// --- a resize: launch B on another box, and A's late minutes still go to A.
	if _, ok, err := st.EndOpenLaunch(ctx, mem.ID, at(2, 0)); err != nil || !ok {
		t.Fatalf("end A: %v %v", ok, err)
	}
	_ = st.FoldRunning(ctx, mem.ID)
	if got := days(mem.ID)["2026-11-10/t3.large/"].RunningSecs; got != 7200 {
		t.Fatalf("A through its stop = %d s", got)
	}
	b := Launch{ID: "LB", WorkspaceID: "w", MembershipID: mem.ID, TenantID: tn.ID, StartedAt: at(3, 0), InstanceType: "m7i.xlarge", Override: "boost"}
	if err := st.BeginLaunch(ctx, b); err != nil {
		t.Fatal(err)
	}
	_ = st.TouchLaunch(ctx, "LB", at(3, 10))
	_ = st.FoldRunning(ctx, mem.ID)
	late := []UsageWindow{
		{T: at(1, 50), LID: "LA", CPUPct: fp(10)}, // pulled after B started
		{T: at(3, 5), LID: "LB", CPUPct: fp(80), WorkingSet: up(2 << 30)},
		{T: at(3, 6), LID: "LB", CPUPct: fp(80), Full60: fp(1)}, // PSI half-read: not measured
		{T: at(3, 7), LID: other.ID + "x", CPUPct: fp(5)},
	}
	if err := st.FoldWindows(ctx, mem.ID, at(0, 13), at(3, 7), late); err != nil {
		t.Fatal(err)
	}
	d = days(mem.ID)
	if got := d["2026-11-10/t3.large/"]; got.CPUMeasuredMin != 3 || got.RunningSecs != 7200 {
		t.Fatalf("old box = %+v", got)
	}
	nb := d["2026-11-10/m7i.xlarge/boost"]
	if nb.RunningSecs != 600 || nb.CPUMeasuredMin != 2 || nb.PSIMeasuredMin != 0 {
		t.Fatalf("new box = %+v", nb)
	}
	// The new rung declared no vCPU or baseline: the burst column is NULL, not 0, and
	// normalisation has nothing to divide by.
	if nb.BurstMin != nil || nb.VCPU != 0 {
		t.Fatalf("undeclared rung: burst %v vcpu %d", nb.BurstMin, nb.VCPU)
	}
	if nb.PSIFullMax != nil {
		t.Fatalf("unmeasured pressure max = %v", *nb.PSIFullMax)
	}
	// A launch of another member cannot be folded into this one's rows.
	lo := Launch{ID: "LO", WorkspaceID: "w2", MembershipID: other.ID, TenantID: tn.ID, StartedAt: at(3, 0), InstanceType: "m7i.large"}
	_ = st.BeginLaunch(ctx, lo)
	if err := st.FoldWindows(ctx, mem.ID, at(3, 7), at(3, 9), []UsageWindow{{T: at(3, 8), LID: "LO", CPUPct: fp(9)}}); err != nil {
		t.Fatal(err)
	}
	if got := days(other.ID); len(got) != 0 {
		t.Fatalf("a window carrying someone else's launch id landed on %+v", got)
	}

	// Scoping: a tenant read sees both members, a member read only their own.
	all, _ := st.ListUsageDays(ctx, tn.ID, "", "2026-11-01", "2026-11-30")
	one, _ := st.ListUsageDays(ctx, "", other.ID, "2026-11-01", "2026-11-30")
	if len(all) < 3 || len(one) != 0 {
		t.Fatalf("all=%d one=%d", len(all), len(one))
	}

	// A new launch closes a stale open one where it was last seen.
	if l, ok, _ := st.OpenLaunch(ctx, mem.ID); !ok || l.ID != "LB" {
		t.Fatalf("open = %+v %v", l, ok)
	}
	c := Launch{ID: "LC", WorkspaceID: "w", MembershipID: mem.ID, TenantID: tn.ID, StartedAt: at(9, 0), InstanceType: "m7i.xlarge"}
	if err := st.BeginLaunch(ctx, c); err != nil {
		t.Fatal(err)
	}
	if l, _, _ := st.GetLaunch(ctx, "LB"); l.StoppedAt != at(3, 10) {
		t.Fatalf("stale launch closed at %d, want its last-seen %d", l.StoppedAt, at(3, 10))
	}
	_ = st.FoldRunning(ctx, mem.ID)
	if got := days(mem.ID)["2026-11-10/m7i.xlarge/boost"].RunningSecs; got != 600 {
		t.Fatalf("the gap between a vanished launch and the next start was counted: %d s", got)
	}

	// Two replicas adopting the same running workspace at once leave two open launches. Ending
	// the membership's launch closes both, each no earlier than it was last seen.
	for _, id := range []string{"LD1", "LD2"} {
		if _, err := st.db.ExecContext(ctx, `INSERT INTO workspace_launch(id, workspace_id, membership_id, tenant_id, started_at, last_seen_at, ran_through, instance_type)
			VALUES(?,?,?,?,?,?,?,?)`, id, "w", other.ID, tn.ID, at(5, 0), at(5, 10), at(5, 0), "m7i.large"); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok, err := st.EndOpenLaunch(ctx, other.ID, at(5, 5)); err != nil || !ok {
		t.Fatalf("end both: %v %v", ok, err)
	}
	if _, open, _ := st.OpenLaunch(ctx, other.ID); open {
		t.Fatal("a duplicate open launch survived the end")
	}
	for _, id := range []string{"LD1", "LD2"} {
		if l, _, _ := st.GetLaunch(ctx, id); l.StoppedAt != at(5, 10) {
			t.Fatalf("%s stopped at %d, want its last-seen %d", id, l.StoppedAt, at(5, 10))
		}
	}

	// Retention.
	if err := st.PruneUsageHistory(ctx, "2026-11-10", at(0, 0)+1); err != nil {
		t.Fatal(err)
	}
	if len(days(mem.ID)) == 0 {
		t.Fatal("rows on the cutoff day were pruned")
	}
	if rows, _ := st.ListUsageDays(ctx, "", mem.ID, "2026-11-01", "2026-11-09"); len(rows) != 0 {
		t.Fatalf("rows before the cutoff survive: %d", len(rows))
	}
}

func TestOverrideRunningSecsSQLite(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	checkOverrideRunningSecs(t, st)
}

func TestOverrideRunningSecsPostgres(t *testing.T) {
	url, ok := pgtest.Schema(t)
	if !ok {
		t.Skip("set AF_TEST_DATABASE_URL to run against Postgres")
	}
	st, err := OpenPostgres(url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	checkOverrideRunningSecs(t, st)
}

// checkOverrideRunningSecs: only launches with the override count, each clipped to the window,
// an open one up to its last sighting, and a launch that never ran adds nothing.
func checkOverrideRunningSecs(t *testing.T, st *SQL) {
	t.Helper()
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tn, _ := st.EnsureDefaultTenant(ctx)
	ident, _ := st.UpsertIdentity(ctx, "a@x.com", "a-x-com", "")
	mem, _ := st.EnsureMembership(ctx, ident.ID, tn.ID, "member")
	ident2, _ := st.UpsertIdentity(ctx, "b@x.com", "b-x-com", "")
	other, _ := st.EnsureMembership(ctx, ident2.ID, tn.ID, "member")

	month := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).Unix()
	h := func(n float64) int64 { return month + int64(n*3600) }
	begin := func(id, membership, override string, start int64) {
		t.Helper()
		if err := st.BeginLaunch(ctx, Launch{ID: id, WorkspaceID: "w-" + membership, MembershipID: membership,
			TenantID: tn.ID, StartedAt: start, Override: override}); err != nil {
			t.Fatal(err)
		}
	}
	end := func(membership string, at int64) {
		t.Helper()
		if _, _, err := st.EndOpenLaunch(ctx, membership, at); err != nil {
			t.Fatal(err)
		}
	}
	// Straddles the start of the month: only its 2 hours inside count.
	begin("L1", mem.ID, "boost", h(-3))
	end(mem.ID, h(2))
	// Not boosted.
	begin("L2", mem.ID, "", h(10))
	end(mem.ID, h(20))
	// Boosted, never ran (a failed Start closes at its own start).
	begin("L3", mem.ID, "boost", h(30))
	end(mem.ID, h(30))
	// Another member's boost.
	begin("L4", other.ID, "boost", h(0))
	end(other.ID, h(50))
	// Open, last seen 1.5 hours in.
	begin("L5", mem.ID, "boost", h(40))
	if err := st.TouchLaunch(ctx, "L5", h(41.5)); err != nil {
		t.Fatal(err)
	}

	got, err := st.OverrideRunningSecs(ctx, mem.ID, "boost", month, h(100))
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(3.5 * 3600); got != want {
		t.Fatalf("boosted secs = %d, want %d", got, want)
	}
	// The window's end clips an open launch too.
	if got, _ := st.OverrideRunningSecs(ctx, mem.ID, "boost", month, h(41)); got != int64(3*3600) {
		t.Fatalf("clipped at the end = %d, want %d", got, 3*3600)
	}
	if got, _ := st.OverrideRunningSecs(ctx, mem.ID, "boost", h(100), h(200)); got != 0 {
		t.Fatalf("an empty window counted %d", got)
	}
	for _, w := range [][2]int64{{h(41), h(41)}, {h(41), h(40.5)}} {
		if got, err := st.OverrideRunningSecs(ctx, mem.ID, "boost", w[0], w[1]); err != nil || got != 0 {
			t.Fatalf("window %v counted %d (%v), want 0", w, got, err)
		}
	}

	// ReopenLaunch: never while a launch is open; then only the newest, only when it was
	// closed at its last sighting, and only within closedSince.
	if _, ok, _ := st.ReopenLaunch(ctx, mem.ID, h(100), 0); ok {
		t.Fatal("reopened while L5 is open")
	}
	if _, _, err := st.EndOpenLaunch(ctx, mem.ID, h(41.5)); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.ReopenLaunch(ctx, mem.ID, h(100), h(42)); ok {
		t.Fatal("reopened a stop older than closedSince")
	}
	// A reading taken before the stop (or before the launch) cannot take it back.
	if _, ok, _ := st.ReopenLaunch(ctx, mem.ID, h(41.5), 0); ok {
		t.Fatal("reopened a stop made after the reading")
	}
	l, ok, err := st.ReopenLaunch(ctx, mem.ID, h(41.6), h(41))
	if err != nil || !ok || l.ID != "L5" || l.StoppedAt != 0 {
		t.Fatalf("reopen = %+v %v %v, want L5 open", l, ok, err)
	}
	if _, _, err := st.EndOpenLaunch(ctx, mem.ID, h(45)); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.ReopenLaunch(ctx, mem.ID, h(100), 0); ok {
		t.Fatal("reopened a launch closed after its last sighting (a stop the CP made)")
	}
}
