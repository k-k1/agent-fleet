package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/pgtest"
	"github.com/k-k1/agent-fleet/control-plane/internal/providedroutes"
	"github.com/k-k1/agent-fleet/control-plane/internal/routeprice"
)

// providedRouteStores runs f against SQLite and, when AF_TEST_DATABASE_URL is set, Postgres.
// open returns another handle on the same database: a second CP.
func providedRouteStores(t *testing.T, f func(t *testing.T, st *SQL, open func() *SQL)) {
	t.Run("sqlite", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "cp.db")
		open := func() *SQL {
			st, err := OpenSQLite(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { st.Close() })
			return st
		}
		st := open()
		if err := st.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		f(t, st, open)
	})
	t.Run("postgres", func(t *testing.T) {
		url, ok := pgtest.Schema(t)
		if !ok {
			t.Skip("set AF_TEST_DATABASE_URL to run the Postgres half")
		}
		open := func() *SQL {
			st, err := OpenPostgres(url)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { st.Close() })
			return st
		}
		st := open()
		if err := st.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		f(t, st, open)
	})
}

const (
	prARN   = "arn:aws:iam::123456789012:role/af-model-route/bedrock"
	prModel = "us.anthropic.claude-opus-5-5"
)

func i64(v int64) *int64 { return &v }

// A source's fingerprint, policy version and activation are the store's to derive: an allow-list
// edit bumps the policy version and leaves the payer (and the activation) alone, a payer edit
// re-fingerprints and deactivates, and a probe result about an old payer activates nothing.
func TestProvidedRouteSources(t *testing.T) {
	providedRouteStores(t, func(t *testing.T, st *SQL, _ func() *SQL) {
		ctx := context.Background()
		tn, _ := limitMembers(t, st)
		dep, err := st.PutProvidedRouteSource(ctx, ProvidedRouteSourceInput{Source: providedroutes.SourceDeployment,
			RoleARN: prARN, Regions: []string{"us-west-2", "us-east-1", "us-east-1"}, AllowList: []string{prModel}})
		if err != nil {
			t.Fatal(err)
		}
		if dep.ID == "" || dep.PolicyVersion != 1 || dep.Active || dep.ExternalID != "" || dep.ExternalIDVersion != 0 ||
			len(dep.Regions) != 2 || dep.Fingerprint != providedroutes.Fingerprint(providedroutes.Payer{RoleARN: prARN, Regions: []string{"us-east-1", "us-west-2"}}) {
			t.Fatalf("registered = %+v", dep)
		}
		if _, _, ok := providedroutes.ParseBinding(dep.Binding()); !ok {
			t.Fatalf("Binding() = %q does not parse", dep.Binding())
		}
		must := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		must(st.SetProvidedRouteSourceActive(ctx, dep.ID, dep.Fingerprint, true))

		// The allow-list changes: a new policy version, the same payer, still active.
		in := ProvidedRouteSourceInput{ID: dep.ID, Source: dep.Source, RoleARN: prARN, Regions: dep.Regions,
			AllowList: []string{prModel, "global.anthropic.claude-sonnet-5-5"}}
		ed, err := st.PutProvidedRouteSource(ctx, in)
		must(err)
		if ed.PolicyVersion != 2 || ed.Fingerprint != dep.Fingerprint || !ed.Active {
			t.Fatalf("allow-list edit = %+v, want policy 2, the same fingerprint, still active", ed)
		}
		// The same set again, reordered: nothing moves.
		in.AllowList = []string{"global.anthropic.claude-sonnet-5-5", prModel, prModel}
		if ed, err = st.PutProvidedRouteSource(ctx, in); err != nil || ed.PolicyVersion != 2 {
			t.Fatalf("same set: %+v %v", ed, err)
		}
		// The payer changes: a new fingerprint, inactive, and the old probe result cannot revive it.
		in.Regions = []string{"us-east-1"}
		ed, err = st.PutProvidedRouteSource(ctx, in)
		must(err)
		if ed.Fingerprint == dep.Fingerprint || ed.Active || ed.PolicyVersion != 2 {
			t.Fatalf("payer edit = %+v, want a new fingerprint and inactive", ed)
		}
		if err := st.SetProvidedRouteSourceActive(ctx, dep.ID, dep.Fingerprint, true); !errors.Is(err, ErrProvidedRouteFingerprint) {
			t.Fatalf("activation against the old fingerprint: %v", err)
		}
		if got, _ := st.GetProvidedRouteSource(ctx, dep.ID); got.Active {
			t.Fatal("an old probe result activated the new payer")
		}
		if err := st.SetProvidedRouteSourceActive(ctx, "nope", ed.Fingerprint, true); !errors.Is(err, ErrProvidedRouteSourceNotFound) {
			t.Fatalf("activation of a missing source: %v", err)
		}

		// A tenant source: the CP generates the external id, rotation re-fingerprints.
		ten, err := st.PutProvidedRouteSource(ctx, ProvidedRouteSourceInput{Source: providedroutes.SourceTenant, TenantID: tn.ID,
			RoleARN: "arn:aws:iam::210987654321:role/af-model-route/x", Regions: []string{"eu-west-1"}})
		must(err)
		if ten.ExternalID == "" || ten.ExternalIDVersion != 1 {
			t.Fatalf("tenant source = %+v, want a generated external id", ten)
		}
		must(st.SetProvidedRouteSourceActive(ctx, ten.ID, ten.Fingerprint, true))
		rot, err := st.RotateProvidedRouteExternalID(ctx, ten.ID)
		must(err)
		if rot.ExternalID == ten.ExternalID || rot.ExternalIDVersion != 2 || rot.Fingerprint == ten.Fingerprint || rot.Active {
			t.Fatalf("rotated = %+v", rot)
		}
		if got, _ := st.GetProvidedRouteSource(ctx, ten.ID); got.ExternalID != rot.ExternalID || got.Fingerprint != rot.Fingerprint {
			t.Fatalf("stored after rotation = %+v", got)
		}
		if _, err := st.RotateProvidedRouteExternalID(ctx, dep.ID); err == nil {
			t.Error("a deployment source was given an external id")
		}

		for name, bad := range map[string]ProvidedRouteSourceInput{
			"unpriced model":      {Source: providedroutes.SourceDeployment, RoleARN: prARN, Regions: []string{"us-east-1"}, AllowList: []string{"us.anthropic.nope"}},
			"ARN model":           {Source: providedroutes.SourceDeployment, RoleARN: prARN, Regions: []string{"us-east-1"}, AllowList: []string{"arn:aws:bedrock:us-east-1:123456789012:application-inference-profile/x"}},
			"tenant without one":  {Source: providedroutes.SourceTenant, RoleARN: prARN, Regions: []string{"us-east-1"}},
			"deployment with one": {Source: providedroutes.SourceDeployment, TenantID: tn.ID, RoleARN: prARN, Regions: []string{"us-east-1"}},
			"kind changed":        {ID: dep.ID, Source: providedroutes.SourceTenant, TenantID: tn.ID, RoleARN: prARN, Regions: []string{"us-east-1"}},
			"missing":             {ID: "0123456789abcdef", Source: providedroutes.SourceDeployment, RoleARN: prARN, Regions: []string{"us-east-1"}},
		} {
			if _, err := st.PutProvidedRouteSource(ctx, bad); err == nil {
				t.Errorf("%s: stored", name)
			}
		}

		if l, err := st.ListProvidedRouteSources(ctx, ""); err != nil || len(l) != 1 || l[0].ID != dep.ID {
			t.Fatalf("deployment sources = %+v %v", l, err)
		}
		if _, err := st.PutProvidedRouteSource(ctx, ProvidedRouteSourceInput{Source: providedroutes.SourceTenant, TenantID: " " + tn.ID + " ",
			RoleARN: prARN, Regions: []string{"us-east-1"}}); err != nil {
			t.Fatal(err)
		}
		if l, err := st.ListProvidedRouteSources(ctx, tn.ID); err != nil || len(l) != 2 || (l[0].ID != ten.ID && l[1].ID != ten.ID) {
			t.Fatalf("tenant sources = %+v %v", l, err)
		}
		must(st.DeleteProvidedRouteSource(ctx, ten.ID))
		if _, err := st.GetProvidedRouteSource(ctx, ten.ID); !errors.Is(err, ErrProvidedRouteSourceNotFound) {
			t.Fatalf("after delete: %v", err)
		}
	})
}

// Every grant is off until someone turns it on, the default member budget is zero (open question
// 2), and "no budget, no use" holds at each level.
func TestProvidedRouteGrantsAndBudgets(t *testing.T) {
	providedRouteStores(t, func(t *testing.T, st *SQL, _ func() *SQL) {
		ctx := context.Background()
		tn, ms := limitMembers(t, st, "a")
		dep := providedRouteDeployment(t, st)
		const src = providedroutes.SourceDeployment
		g, err := st.GetProvidedRouteGrant(ctx, tn.ID, src)
		if err != nil || g.Granted || g.CeilingMicro != nil || g.MembersOnly || g.MemberDefaultMicro != 0 {
			t.Fatalf("an unconfigured tenant = %+v %v, want nothing granted", g, err)
		}
		m, err := st.GetProvidedRouteMember(ctx, ms[0].ID, src)
		if err != nil || m.Granted || m.BudgetMicro != nil {
			t.Fatalf("an unconfigured member = %+v %v", m, err)
		}
		if g.MayUse(m, dep) {
			t.Fatal("usable with nothing granted")
		}
		if err := st.SetProvidedRouteGrant(ctx, tn.ID, src, true, nil); err == nil {
			t.Fatal("a deployment grant without a ceiling was saved")
		}
		if err := st.SetProvidedRouteGrant(ctx, tn.ID, "member", true, i64(1)); err == nil {
			t.Fatal("a grant for the member's own source was saved")
		}
		mustDo(t, st.SetProvidedRouteGrant(ctx, tn.ID, src, true, i64(50_000_000)))
		g, _ = st.GetProvidedRouteGrant(ctx, tn.ID, src)
		if !g.Granted || *g.CeilingMicro != 50_000_000 || g.MayUse(m, dep) {
			t.Fatalf("granted with the zero default budget = %+v, MayUse %v; want still unusable", g, g.MayUse(m, dep))
		}
		mustDo(t, st.SetProvidedRouteMemberPolicy(ctx, tn.ID, src, false, 10_000_000))
		g, _ = st.GetProvidedRouteGrant(ctx, tn.ID, src)
		if !g.Granted || g.MemberDefaultMicro != 10_000_000 || !g.MayUse(m, dep) {
			t.Fatalf("the policy save must keep the grant: %+v", g)
		}
		inactive := dep
		inactive.Active = false
		if g.MayUse(m, inactive) {
			t.Error("usable on an inactive source")
		}
		// Granted members only: the member needs a grant of their own.
		mustDo(t, st.SetProvidedRouteMemberPolicy(ctx, tn.ID, src, true, 10_000_000))
		g, _ = st.GetProvidedRouteGrant(ctx, tn.ID, src)
		if g.MayUse(m, dep) {
			t.Fatal("an ungranted member may use a members-only source")
		}
		mustDo(t, st.SetProvidedRouteMember(ctx, tn.ID, ms[0].ID, src, true, nil))
		m, _ = st.GetProvidedRouteMember(ctx, ms[0].ID, src)
		if !g.MayUse(m, dep) || g.MemberBudgetMicro(m) != 10_000_000 {
			t.Fatalf("a granted member on the default budget: %+v", m)
		}
		// An override of zero is "no budget".
		mustDo(t, st.SetProvidedRouteMember(ctx, tn.ID, ms[0].ID, src, true, i64(0)))
		m, _ = st.GetProvidedRouteMember(ctx, ms[0].ID, src)
		if m.BudgetMicro == nil || *m.BudgetMicro != 0 || g.MayUse(m, dep) {
			t.Fatalf("an override of zero: %+v", m)
		}
		// The grant save keeps the tenant_admin's policy.
		mustDo(t, st.SetProvidedRouteGrant(ctx, tn.ID, src, false, i64(50_000_000)))
		g, _ = st.GetProvidedRouteGrant(ctx, tn.ID, src)
		if g.Granted || !g.MembersOnly || g.MemberDefaultMicro != 10_000_000 {
			t.Fatalf("the grant save dropped the policy: %+v", g)
		}
		// The tenant source's ceiling is optional.
		mustDo(t, st.SetProvidedRouteGrant(ctx, tn.ID, providedroutes.SourceTenant, true, nil))
		for _, bad := range []error{
			st.SetProvidedRouteGrant(ctx, tn.ID, src, true, i64(-1)),
			st.SetProvidedRouteMemberPolicy(ctx, tn.ID, src, false, -1),
			st.SetProvidedRouteMember(ctx, tn.ID, ms[0].ID, src, true, i64(-1)),
		} {
			if bad == nil {
				t.Error("a negative amount was saved")
			}
		}
	})
}

func mustDo(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func providedRouteDeployment(t *testing.T, st *SQL) ProvidedRouteSource {
	t.Helper()
	ctx := context.Background()
	src, err := st.PutProvidedRouteSource(ctx, ProvidedRouteSourceInput{Source: providedroutes.SourceDeployment,
		RoleARN: prARN, Regions: []string{"us-east-1"}, AllowList: []string{prModel}})
	mustDo(t, err)
	mustDo(t, st.SetProvidedRouteSourceActive(ctx, src.ID, src.Fingerprint, true))
	src.Active = true
	return src
}

func prCall(id string, tn Tenant, m Membership, src ProvidedRouteSource, reserved int64, at string) ProvidedRouteCall {
	return ProvidedRouteCall{CallID: id, MembershipID: m.ID, TenantID: tn.ID, Source: src.Source, Binding: src.Binding(),
		Model: prModel, API: "invoke-with-response-stream", PriceVersion: routeprice.Version(), ReservedMicro: reserved,
		AdmittedAt: at, CPInstance: "cp-a"}
}

// Full-fit admission at both levels, the in-flight cap, exactly-once settlement into the month of
// admission, and a reservation that is never released to nothing.
func TestProvidedRouteAdmissionAndSettlement(t *testing.T) {
	providedRouteStores(t, func(t *testing.T, st *SQL, _ func() *SQL) {
		ctx := context.Background()
		tn, ms := limitMembers(t, st, "a", "b")
		src := providedRouteDeployment(t, st)
		a, b := ms[0], ms[1]
		lim := ProvidedRouteLimits{MemberBudgetMicro: 100, TenantCeilingMicro: i64(150)}
		admit := func(c ProvidedRouteCall, lim ProvidedRouteLimits) ProvidedRouteAdmission {
			t.Helper()
			got, err := st.AdmitProvidedRouteCall(ctx, c, lim)
			mustDo(t, err)
			return got
		}
		const nov = "2026-11-30T23:59:59Z"
		if got := admit(prCall("c1", tn, a, src, 60, nov), lim); !got.Admitted {
			t.Fatalf("c1: %+v", got)
		}
		// 60 + 41 > 100: the member's budget refuses, and nothing of it is kept.
		if got := admit(prCall("c2", tn, a, src, 41, nov), lim); got.Admitted || got.Refused != ProvidedRouteRefusedMemberBudget {
			t.Fatalf("c2: %+v, want the member budget", got)
		}
		// 60 + 40 = 100 fits exactly.
		if got := admit(prCall("c3", tn, a, src, 40, nov), lim); !got.Admitted {
			t.Fatalf("c3 fits exactly: %+v", got)
		}
		// b has its own budget, but the tenant's ceiling of 150 has 50 left.
		if got := admit(prCall("c4", tn, b, src, 51, nov), lim); got.Refused != ProvidedRouteRefusedTenantCeiling {
			t.Fatalf("c4: %+v, want the tenant ceiling", got)
		}
		if p, _ := st.GetProvidedRoutePeriod(ctx, tn.ID, b.ID, src.Source, "2026-11"); p.ReservedMicro != 0 {
			t.Fatalf("a refused admission left %d reserved on the member", p.ReservedMicro)
		}
		// The in-flight cap: a already has two calls in flight.
		if got := admit(prCall("c5", tn, a, src, 1, nov), ProvidedRouteLimits{MemberBudgetMicro: 1000, MaxInFlight: 2}); got.Refused != ProvidedRouteRefusedInFlight {
			t.Fatalf("c5: %+v, want the in-flight cap", got)
		}
		if _, err := st.AdmitProvidedRouteCall(ctx, prCall("c1", tn, a, src, 1, nov), ProvidedRouteLimits{MemberBudgetMicro: 1000}); !errors.Is(err, ErrProvidedRouteCallExists) {
			t.Fatalf("a call id admitted twice: %v", err)
		}
		if _, err := st.GetProvidedRouteCall(ctx, "nope"); !errors.Is(err, ErrProvidedRouteCallNotFound) {
			t.Fatalf("an unknown call: %v, want ErrProvidedRouteCallNotFound", err)
		}
		if _, err := st.AdmitProvidedRouteCall(ctx, prCall("c6", tn, a, src, 0, nov), lim); err == nil {
			t.Fatal("a zero reservation was admitted")
		}

		c1, err := st.GetProvidedRouteCall(ctx, "c1")
		mustDo(t, err)
		if c1.Month != "2026-11" || c1.State != providedroutes.StateInFlight || c1.PriceVersion != routeprice.Version() || c1.FinalMicro != nil {
			t.Fatalf("c1 row = %+v", c1)
		}
		// Settled after midnight: it still goes to November, exactly once.
		ok, err := st.SettleProvidedRouteCall(ctx, "c1", 25)
		mustDo(t, err)
		if !ok {
			t.Fatal("c1 did not settle")
		}
		if again, err := st.SettleProvidedRouteCall(ctx, "c1", 25); err != nil || again {
			t.Fatalf("a duplicate settlement: %v %v, want no change", again, err)
		}
		want := map[string]ProvidedRoutePeriod{a.ID: {SpentMicro: 25, ReservedMicro: 40}, "": {SpentMicro: 25, ReservedMicro: 40}}
		for mid, w := range want {
			if p, _ := st.GetProvidedRoutePeriod(ctx, tn.ID, mid, src.Source, "2026-11"); p != w {
				t.Errorf("November %q = %+v, want %+v", mid, p, w)
			}
			if p, _ := st.GetProvidedRoutePeriod(ctx, tn.ID, mid, src.Source, "2026-12"); p != (ProvidedRoutePeriod{}) {
				t.Errorf("December %q = %+v, want nothing", mid, p)
			}
		}
		// Settled above its reservation (a table defect): recorded as reported.
		if ok, err := st.SettleProvidedRouteCall(ctx, "c3", 55); err != nil || !ok {
			t.Fatalf("c3: %v %v", ok, err)
		}
		if p, _ := st.GetProvidedRoutePeriod(ctx, tn.ID, a.ID, src.Source, "2026-11"); p != (ProvidedRoutePeriod{SpentMicro: 80}) {
			t.Fatalf("after c3 = %+v", p)
		}
		if ok, _ := st.SettleProvidedRouteCall(ctx, "nope", 1); ok {
			t.Fatal("an unknown call settled")
		}
		if _, err := st.SettleProvidedRouteCall(ctx, "c3", -1); err == nil {
			t.Fatal("a negative final amount was accepted")
		}
		// December opens with its own budget.
		if got := admit(prCall("c7", tn, a, src, 100, "2026-12-01T00:00:00Z"), lim); !got.Admitted {
			t.Fatalf("c7 in December: %+v", got)
		}
	})
}

// An admission retried after its answer was lost reads as admitted and reserves nothing more, even
// when the first one filled the budget, and so do retries racing the original from two CPs. The
// same id with other contents is refused as a different call.
func TestProvidedRouteAdmissionReplay(t *testing.T) {
	providedRouteStores(t, func(t *testing.T, st *SQL, open func() *SQL) {
		ctx := context.Background()
		tn, ms := limitMembers(t, st, "a")
		src := providedRouteDeployment(t, st)
		lim := ProvidedRouteLimits{MemberBudgetMicro: 100, TenantCeilingMicro: i64(100), MaxInFlight: 1}
		c := prCall("r1", tn, ms[0], src, 100, "2026-11-08T12:00:00Z")
		if got, err := st.AdmitProvidedRouteCall(ctx, c, lim); err != nil || !got.Admitted || got.Replayed {
			t.Fatalf("first: %+v %v", got, err)
		}
		retry := c
		retry.AdmittedAt = "" // a retry may leave the time to the store
		if got, err := st.AdmitProvidedRouteCall(ctx, retry, lim); err != nil || !got.Admitted || !got.Replayed {
			t.Fatalf("retry: %+v %v, want admitted as a replay", got, err)
		}
		other := c
		other.Model = "global.anthropic.claude-sonnet-5-5"
		if _, err := st.AdmitProvidedRouteCall(ctx, other, lim); !errors.Is(err, ErrProvidedRouteCallExists) {
			t.Fatalf("the same id for another call: %v", err)
		}
		if p, _ := st.GetProvidedRoutePeriod(ctx, tn.ID, ms[0].ID, src.Source, "2026-11"); p.ReservedMicro != 100 {
			t.Fatalf("reserved %d after retries, want 100", p.ReservedMicro)
		}
		if ok, err := st.SettleProvidedRouteCall(ctx, "r1", 40); err != nil || !ok {
			t.Fatalf("the retried call does not settle: %v %v", ok, err)
		}

		// Concurrent copies of one admission from two CPs: one reserves, the rest are replays.
		cps := []*SQL{st, open()}
		d := prCall("r2", tn, ms[0], src, 50, "2026-11-08T12:00:00Z")
		var wg sync.WaitGroup
		var mu sync.Mutex
		fresh, replays := 0, 0
		for i := range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				got, err := cps[i%2].AdmitProvidedRouteCall(ctx, d, ProvidedRouteLimits{MemberBudgetMicro: 1000})
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				defer mu.Unlock()
				switch {
				case got.Replayed:
					replays++
				case got.Admitted:
					fresh++
				}
			}()
		}
		wg.Wait()
		if p, _ := st.GetProvidedRoutePeriod(ctx, tn.ID, ms[0].ID, src.Source, "2026-11"); fresh != 1 || replays != 7 || p.ReservedMicro != 50 {
			t.Fatalf("concurrent copies: %d fresh, %d replays, %d reserved; want 1, 7, 50", fresh, replays, p.ReservedMicro)
		}
	})
}

// The database refuses a source or a state the code does not know, beside the Go checks.
func TestProvidedRouteSchemaChecks(t *testing.T) {
	providedRouteStores(t, func(t *testing.T, st *SQL, _ func() *SQL) {
		ctx := context.Background()
		if _, err := st.db.ExecContext(ctx, `INSERT INTO provided_route_grant(tenant_id, source, updated_at) VALUES('t', 'member', 'x')`); err == nil {
			t.Error("a grant for source 'member' was stored")
		}
		if _, err := st.db.ExecContext(ctx, `INSERT INTO provided_route_call(call_id, membership_id, tenant_id, source, binding, model, api,
			month, price_version, reserved_micro, state, admitted_at, cp_instance)
			VALUES('x', 'm', 't', 'deployment', 'b', 'm', 'a', '2026-11', 'v', 1, 'done', 'x', 'cp')`); err == nil {
			t.Error("a call in state 'done' was stored")
		}
	})
}

// Two CPs racing at the budget edge never admit past it (D4): every admission is the conditional
// write, so of N concurrent calls exactly limit/reservation get in. Each level is raced on its own
// so neither condition hides behind the other.
func TestProvidedRouteAdmissionRace(t *testing.T) {
	providedRouteStores(t, func(t *testing.T, st *SQL, open func() *SQL) {
		ctx := context.Background()
		tn, ms := limitMembers(t, st, "a", "b")
		src := providedRouteDeployment(t, st)
		cps := []*SQL{st, open()}
		const n, limit, each = 24, 1000, 100
		for _, tc := range []struct {
			name  string
			m     Membership
			lim   ProvidedRouteLimits
			level string // the membership id whose month is full
		}{
			{"member budget", ms[0], ProvidedRouteLimits{MemberBudgetMicro: limit}, ms[0].ID},
			{"tenant ceiling", ms[1], ProvidedRouteLimits{MemberBudgetMicro: 100 * limit, TenantCeilingMicro: i64(2 * limit)}, ""},
		} {
			var wg sync.WaitGroup
			var mu sync.Mutex
			admitted := 0
			for i := range n {
				wg.Add(1)
				go func() {
					defer wg.Done()
					c := prCall(fmt.Sprintf("race-%s-%d", tc.m.ID, i), tn, tc.m, src, each, "2026-11-08T12:00:00Z")
					got, err := cps[i%2].AdmitProvidedRouteCall(ctx, c, tc.lim)
					if err != nil {
						t.Error(err)
						return
					}
					if got.Admitted {
						mu.Lock()
						admitted++
						mu.Unlock()
					}
				}()
			}
			wg.Wait()
			// The tenant's month already holds the member race's 1000, so 1000 more fit under 2000.
			p, _ := st.GetProvidedRoutePeriod(ctx, tn.ID, tc.level, src.Source, "2026-11")
			if want := limit / each; admitted != want || p.ReservedMicro != map[string]int64{ms[0].ID: limit, "": 2 * limit}[tc.level] {
				t.Errorf("%s: admitted %d with %d reserved, want %d", tc.name, admitted, p.ReservedMicro, want)
			}
		}
	})
}

// A member's budget override goes with the seat; the money stays. A tenant's sources and grants go
// with the tenant.
func TestProvidedRouteCascade(t *testing.T) {
	providedRouteStores(t, func(t *testing.T, st *SQL, _ func() *SQL) {
		ctx := context.Background()
		tn, ms := limitMembers(t, st, "a")
		src := providedRouteDeployment(t, st)
		mustDo(t, st.SetProvidedRouteGrant(ctx, tn.ID, src.Source, true, i64(1000)))
		mustDo(t, st.SetProvidedRouteMember(ctx, tn.ID, ms[0].ID, src.Source, true, i64(500)))
		if got, err := st.AdmitProvidedRouteCall(ctx, prCall("k1", tn, ms[0], src, 10, "2026-11-08T00:00:00Z"),
			ProvidedRouteLimits{MemberBudgetMicro: 500, TenantCeilingMicro: i64(1000)}); err != nil || !got.Admitted {
			t.Fatalf("admit: %+v %v", got, err)
		}
		ten, err := st.PutProvidedRouteSource(ctx, ProvidedRouteSourceInput{Source: providedroutes.SourceTenant, TenantID: tn.ID,
			RoleARN: prARN, Regions: []string{"us-east-1"}})
		mustDo(t, err)

		mustDo(t, st.DeleteMembership(ctx, ms[0].ID))
		if m, _ := st.GetProvidedRouteMember(ctx, ms[0].ID, src.Source); m.Granted || m.BudgetMicro != nil {
			t.Fatalf("the override outlived the membership: %+v", m)
		}
		if c, err := st.GetProvidedRouteCall(ctx, "k1"); err != nil || c.ReservedMicro != 10 {
			t.Fatalf("the reservation went with the membership: %+v %v", c, err)
		}
		if p, _ := st.GetProvidedRoutePeriod(ctx, tn.ID, "", src.Source, "2026-11"); p.ReservedMicro != 10 {
			t.Fatalf("the tenant's month lost the member's reservation: %+v", p)
		}

		mustDo(t, st.DeleteTenant(ctx, tn.ID))
		if g, _ := st.GetProvidedRouteGrant(ctx, tn.ID, src.Source); g.Granted || g.CeilingMicro != nil {
			t.Fatalf("the grant outlived the tenant: %+v", g)
		}
		if _, err := st.GetProvidedRouteSource(ctx, ten.ID); !errors.Is(err, ErrProvidedRouteSourceNotFound) {
			t.Fatalf("the tenant's source outlived it: %v", err)
		}
		if _, err := st.GetProvidedRouteSource(ctx, src.ID); err != nil {
			t.Fatalf("the deployment source went with a tenant: %v", err)
		}
	})
}
