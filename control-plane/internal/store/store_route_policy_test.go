package store

import (
	"context"
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
		_, dep, err := st.PutRoutePolicy(ctx, RoutePolicyDeployment, "", `{"deny":["x"]}`, "su")
		mustDo(t, err)
		before, cp, err := st.PutRoutePolicy(ctx, RoutePolicyTenantCap, tn.ID, `{"classes":["provided"]}`, "su")
		mustDo(t, err)
		if before.Revision != 0 || cp.Revision <= dep.Revision {
			t.Fatalf("cap before %+v after %+v, deployment %+v", before, cp, dep)
		}
		before, rule, err := st.PutRoutePolicy(ctx, RoutePolicyTenant, tn.ID, `{"deny":["y"]}`, "ta")
		mustDo(t, err)
		if before.Rule != "{}" || rule.Revision <= cp.Revision {
			t.Fatalf("tenant rule before %+v after %+v", before, rule)
		}
		// Clearing the tenant's rule leaves the cap as it was.
		before, cleared, err := st.PutRoutePolicy(ctx, RoutePolicyTenant, tn.ID, "", "ta")
		mustDo(t, err)
		if before.Rule != `{"deny":["y"]}` || cleared.Rule != "{}" || cleared.Revision <= rule.Revision {
			t.Fatalf("clear: before %+v after %+v", before, cleared)
		}
		if got, _ := st.GetRoutePolicy(ctx, RoutePolicyTenantCap, tn.ID); got.Rule != `{"classes":["provided"]}` || got.Revision != cp.Revision {
			t.Fatalf("clearing the tenant rule touched the cap: %+v", got)
		}
		for _, bad := range [][2]string{{RoutePolicyDeployment, tn.ID}, {RoutePolicyTenant, ""}, {"member", tn.ID}} {
			if _, _, err := st.PutRoutePolicy(ctx, bad[0], bad[1], "{}", "x"); err == nil {
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
					if _, after, err = st.PutRoutePolicy(ctx, scope, tid, "{}", "x"); err == nil {
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
