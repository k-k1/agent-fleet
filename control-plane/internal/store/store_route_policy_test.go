package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

// The three administered layers are separate rows (ADR 0138 D8): writing one never touches
// another, every write takes a revision strictly greater than any before it, a row never written
// reads as no restriction, and a tenant's rows go with the tenant.
func TestRoutePolicyRows(t *testing.T) {
	providedRouteStores(t, func(t *testing.T, st *SQL, _ func() *SQL) {
		ctx := context.Background()
		tn, _ := limitMembers(t, st, "a")
		if r, err := st.GetRoutePolicy(ctx, RoutePolicyTenant, tn.ID); err != nil || r.Rule != "{}" || r.Revision != 0 {
			t.Fatalf("unwritten row = %+v %v, want no restriction at revision 0", r, err)
		}
		_, dep, err := st.PutRoutePolicy(ctx, RoutePolicyDeployment, "", `{"deny":["x"]}`, "su", nil)
		mustDo(t, err)
		before, cp, err := st.PutRoutePolicy(ctx, RoutePolicyTenantCap, tn.ID, `{"classes":["provided"]}`, "su", nil)
		mustDo(t, err)
		if before.Revision != 0 || cp.Revision <= dep.Revision {
			t.Fatalf("cap before %+v after %+v, deployment %+v", before, cp, dep)
		}
		before, rule, err := st.PutRoutePolicy(ctx, RoutePolicyTenant, tn.ID, `{"deny":["y"]}`, "ta", nil)
		mustDo(t, err)
		if before.Rule != "{}" || rule.Revision <= cp.Revision {
			t.Fatalf("tenant rule before %+v after %+v", before, rule)
		}
		// Clearing the tenant's rule leaves the cap as it was.
		before, cleared, err := st.PutRoutePolicy(ctx, RoutePolicyTenant, tn.ID, "", "ta", nil)
		mustDo(t, err)
		if before.Rule != `{"deny":["y"]}` || cleared.Rule != "{}" || cleared.Revision <= rule.Revision {
			t.Fatalf("clear: before %+v after %+v", before, cleared)
		}
		if got, _ := st.GetRoutePolicy(ctx, RoutePolicyTenantCap, tn.ID); got.Rule != `{"classes":["provided"]}` || got.Revision != cp.Revision {
			t.Fatalf("clearing the tenant rule touched the cap: %+v", got)
		}
		for _, bad := range [][2]string{{RoutePolicyDeployment, tn.ID}, {RoutePolicyTenant, ""}, {"member", tn.ID}} {
			if _, _, err := st.PutRoutePolicy(ctx, bad[0], bad[1], "{}", "x", nil); err == nil {
				t.Errorf("scope %q tenant %q accepted", bad[0], bad[1])
			}
		}
		mustDo(t, st.DeleteTenant(ctx, tn.ID))
		if got, _ := st.GetRoutePolicy(ctx, RoutePolicyTenantCap, tn.ID); got.Revision != 0 {
			t.Fatalf("the cap outlived the tenant: %+v", got)
		}
		if got, _ := st.GetRoutePolicy(ctx, RoutePolicyDeployment, ""); got.Revision != dep.Revision {
			t.Fatalf("the deployment rule went with a tenant: %+v", got)
		}
	})
}

// Concurrent writes still get distinct, strictly increasing revisions.
func TestRoutePolicyRevisionsAreDistinct(t *testing.T) {
	providedRouteStores(t, func(t *testing.T, st *SQL, _ func() *SQL) {
		ctx := context.Background()
		tn, _ := limitMembers(t, st, "a")
		var mu sync.Mutex
		seen := map[int64]bool{}
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				scope, tid := RoutePolicyTenant, tn.ID
				if i%2 == 0 {
					scope, tid = RoutePolicyDeployment, ""
				}
				var err error
				var after RoutePolicyRow
				for try := 0; try < 20; try++ {
					if _, after, err = st.PutRoutePolicy(ctx, scope, tid, "{}", "x", nil); err == nil {
						break
					}
				}
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				defer mu.Unlock()
				if seen[after.Revision] {
					t.Errorf("revision %d given twice", after.Revision)
				}
				seen[after.Revision] = true
			}(i)
		}
		wg.Wait()
	})
}

// A write and its audit row commit together: when the audit row cannot be inserted (here a
// duplicate id the database refuses), the rule, its revision and the sequence stay as they were,
// for every scope (D15).
func TestRoutePolicyWriteAndAuditAreAtomic(t *testing.T) {
	providedRouteStores(t, func(t *testing.T, st *SQL, _ func() *SQL) {
		ctx := context.Background()
		tn, _ := limitMembers(t, st, "a")
		mustDo(t, st.InsertAudit(ctx, AuditLog{ID: "taken", Action: "x", At: NowTS()}))
		failing := func(before, after RoutePolicyRow) AuditLog { return AuditLog{ID: "taken", Action: "y", At: NowTS()} }
		for _, sc := range [][2]string{{RoutePolicyCeiling, ""}, {RoutePolicyDeployment, ""}, {RoutePolicyTenantCap, tn.ID}, {RoutePolicyTenant, tn.ID}} {
			if _, _, err := st.PutRoutePolicy(ctx, sc[0], sc[1], `{"deny":["z"]}`, "x", failing); err == nil {
				t.Errorf("%s: the write succeeded although its audit row could not be written", sc[0])
			}
			if r, _ := st.GetRoutePolicy(ctx, sc[0], sc[1]); r.Revision != 0 || r.Rule != "{}" {
				t.Errorf("%s: the rule committed without its audit row: %+v", sc[0], r)
			}
		}
		ok := func(before, after RoutePolicyRow) AuditLog {
			return AuditLog{ID: NewID(), TenantID: tn.ID, Action: "route_policy.tenant.put", Detail: before.Rule + "->" + after.Rule, At: NowTS()}
		}
		_, after, err := st.PutRoutePolicy(ctx, RoutePolicyTenant, tn.ID, `{"deny":["z"]}`, "x", ok)
		mustDo(t, err)
		if after.Revision != 1 {
			t.Errorf("the failed writes consumed revisions: the first good one is %d", after.Revision)
		}
		logs, _ := st.ListAuditByTenant(ctx, tn.ID, 10)
		if len(logs) != 1 || logs[0].Detail != `{}->{"deny":["z"]}` {
			t.Errorf("audit rows = %+v", logs)
		}
	})
}

// One read returns every layer of a tenant from one snapshot, the effective revision being its
// newest row; another tenant's rows are not in it.
func TestRoutePolicyLayersRead(t *testing.T) {
	providedRouteStores(t, func(t *testing.T, st *SQL, _ func() *SQL) {
		ctx := context.Background()
		tn, _ := limitMembers(t, st, "a")
		other, err := st.CreateTenant(ctx, "other", "Other")
		mustDo(t, err)
		empty, err := st.GetRoutePolicyLayers(ctx, tn.ID)
		mustDo(t, err)
		if empty.Revision() != 0 || empty.Tenant.Rule != "{}" || empty.Ceiling.Rule != "{}" {
			t.Fatalf("empty read = %+v", empty)
		}
		_, c, _ := st.PutRoutePolicy(ctx, RoutePolicyCeiling, "", `{"deny":["c"]}`, "op", nil)
		_, d, _ := st.PutRoutePolicy(ctx, RoutePolicyDeployment, "", `{"deny":["d"]}`, "su", nil)
		_, tr, _ := st.PutRoutePolicy(ctx, RoutePolicyTenant, tn.ID, `{"deny":["t"]}`, "ta", nil)
		_, o, _ := st.PutRoutePolicy(ctx, RoutePolicyTenant, other.ID, `{"deny":["o"]}`, "ta", nil)
		got, err := st.GetRoutePolicyLayers(ctx, tn.ID)
		mustDo(t, err)
		if got.Ceiling.Revision != c.Revision || got.Deployment.Revision != d.Revision || got.Tenant.Rule != `{"deny":["t"]}` ||
			got.TenantCap.Revision != 0 || got.Revision() != tr.Revision || got.Revision() >= o.Revision {
			t.Errorf("layers = %+v", got)
		}
		dep, err := st.GetRoutePolicyLayers(ctx, "")
		mustDo(t, err)
		if dep.Tenant.Revision != 0 || dep.Revision() != d.Revision {
			t.Errorf("the deployment-only read = %+v", dep)
		}
	})
}

// ListTenantRoutePolicies reads one tenant-scoped layer across tenants, never the deployment
// rows or the other layer, and goes with the tenant.
func TestListTenantRoutePolicies(t *testing.T) {
	providedRouteStores(t, func(t *testing.T, st *SQL, _ func() *SQL) {
		ctx := context.Background()
		a, _ := limitMembers(t, st, "a")
		b, err := st.CreateTenant(ctx, "other", "Other")
		mustDo(t, err)
		_, _, err = st.PutRoutePolicy(ctx, RoutePolicyDeployment, "", `{"deny":["d"]}`, "su", nil)
		mustDo(t, err)
		_, _, err = st.PutRoutePolicy(ctx, RoutePolicyTenantCap, a.ID, `{"deny":["a"]}`, "su", nil)
		mustDo(t, err)
		_, _, err = st.PutRoutePolicy(ctx, RoutePolicyTenantCap, b.ID, `{"deny":["b"]}`, "su", nil)
		mustDo(t, err)
		_, _, err = st.PutRoutePolicy(ctx, RoutePolicyTenant, a.ID, `{"deny":["t"]}`, "ta", nil)
		mustDo(t, err)
		caps, err := st.ListTenantRoutePolicies(ctx, RoutePolicyTenantCap)
		mustDo(t, err)
		if len(caps) != 2 || caps[0].Scope != RoutePolicyTenantCap || caps[1].Scope != RoutePolicyTenantCap {
			t.Fatalf("caps = %+v, want the two cap rows only", caps)
		}
		own, err := st.ListTenantRoutePolicies(ctx, RoutePolicyTenant)
		mustDo(t, err)
		if len(own) != 1 || own[0].TenantID != a.ID || own[0].Rule != `{"deny":["t"]}` {
			t.Fatalf("own = %+v", own)
		}
		mustDo(t, st.DeleteTenant(ctx, b.ID))
		caps, err = st.ListTenantRoutePolicies(ctx, RoutePolicyTenantCap)
		mustDo(t, err)
		if len(caps) != 1 || caps[0].TenantID != a.ID {
			t.Fatalf("caps after deleting a tenant = %+v, want its row gone", caps)
		}
		for _, scope := range []string{RoutePolicyDeployment, RoutePolicyCeiling, "x"} {
			if _, err := st.ListTenantRoutePolicies(ctx, scope); err == nil {
				t.Errorf("scope %q was read as a tenant layer", scope)
			}
		}
	})
}

// A conditional write passes only at the revision the caller loaded (0 for a row never written),
// and a refused one leaves the row, the audit log and the revision sequence untouched.
func TestPutRoutePolicyIfPrecondition(t *testing.T) {
	providedRouteStores(t, func(t *testing.T, st *SQL, _ func() *SQL) {
		ctx := context.Background()
		zero := int64(0)
		_, first, err := st.PutRoutePolicyIf(ctx, RoutePolicyDeployment, "", `{"deny":["a"]}`, "su", &zero, nil)
		mustDo(t, err)
		// Loaded at 0 again: the row is now at first.Revision.
		before, _, err := st.PutRoutePolicyIf(ctx, RoutePolicyDeployment, "", `{"deny":["b"]}`, "su", &zero, nil)
		if !errors.Is(err, ErrRoutePolicyStale) || before.Revision != first.Revision {
			t.Fatalf("stale write = before %+v, %v; want ErrRoutePolicyStale reporting the current row", before, err)
		}
		if got, _ := st.GetRoutePolicy(ctx, RoutePolicyDeployment, ""); got.Rule != `{"deny":["a"]}` || got.Revision != first.Revision {
			t.Fatalf("a refused write changed the row: %+v", got)
		}
		audited := false
		_, _, err = st.PutRoutePolicyIf(ctx, RoutePolicyDeployment, "", `{"deny":["b"]}`, "su", &zero,
			func(_, _ RoutePolicyRow) AuditLog { audited = true; return AuditLog{} })
		if !errors.Is(err, ErrRoutePolicyStale) || audited {
			t.Fatalf("a refused write must not build or insert an audit row (err %v, audited %v)", err, audited)
		}
		// The refusal took no revision: the next write is the one right after first.
		cur := first.Revision
		_, next, err := st.PutRoutePolicyIf(ctx, RoutePolicyDeployment, "", `{"deny":["c"]}`, "su", &cur, nil)
		mustDo(t, err)
		if next.Revision != first.Revision+1 {
			t.Fatalf("revision after a refused write = %d, want %d", next.Revision, first.Revision+1)
		}
		// nil is today's unconditional write.
		_, _, err = st.PutRoutePolicyIf(ctx, RoutePolicyDeployment, "", `{}`, "su", nil, nil)
		mustDo(t, err)
	})
}

// Writers that all loaded the same revision race, each with its own rule: exactly one wins, the
// others are told the row moved, none is turned into another error by lock contention (there is
// no retry here, any other error fails), and the row holds the winner's rule at base+1.
func TestPutRoutePolicyIfConcurrentWriters(t *testing.T) {
	providedRouteStores(t, func(t *testing.T, st *SQL, _ func() *SQL) {
		ctx := context.Background()
		for round := 0; round < 5; round++ {
			row, err := st.GetRoutePolicy(ctx, RoutePolicyDeployment, "")
			mustDo(t, err)
			base := row.Revision
			const writers = 8
			var wg sync.WaitGroup
			start := make(chan struct{})
			errs := make([]error, writers)
			afters := make([]RoutePolicyRow, writers)
			for i := 0; i < writers; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					_, afters[i], errs[i] = st.PutRoutePolicyIf(ctx, RoutePolicyDeployment, "", fmt.Sprintf(`{"deny":["w%d"]}`, i), "su", &base, nil)
				}(i)
			}
			close(start)
			wg.Wait()
			winner := -1
			for i, err := range errs {
				switch {
				case err == nil:
					if winner >= 0 {
						t.Fatalf("round %d: two writers won (%d and %d)", round, winner, i)
					}
					winner = i
				case errors.Is(err, ErrRoutePolicyStale):
				default:
					t.Fatalf("round %d: writer %d failed with %v, want a win or ErrRoutePolicyStale", round, i, err)
				}
			}
			if winner < 0 {
				t.Fatalf("round %d: nobody won: %v", round, errs)
			}
			got, err := st.GetRoutePolicy(ctx, RoutePolicyDeployment, "")
			mustDo(t, err)
			if want := fmt.Sprintf(`{"deny":["w%d"]}`, winner); got.Rule != want || got.Revision != base+1 || afters[winner].Revision != base+1 {
				t.Fatalf("round %d: stored %+v, want %s at revision %d", round, got, want, base+1)
			}
		}
	})
}
