package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// shrinkingHomeStub answers every ResizeHome with "the volume is larger than the setting",
// the way ecs-ec2 does for a grown home, whatever the save was about.
type shrinkingHomeStub struct{ runtime.Runtime }

func (shrinkingHomeStub) ResizeHome(context.Context) (runtime.HomeResize, error) {
	return runtime.HomeResize{Outcome: runtime.HomeResizeShrink, FromGiB: 51, ToGiB: 50}, nil
}

// Saving a member's limits reports a shrink only when the disk the member resolves to
// changed (#4406): an unrelated save, or a number that the tenant cap clamps to the same
// size, must stay silent, while a real reduction still warns.
func TestSetUserLimitReportsShrinkOnlyWhenTheDiskChanged(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name       string
		capGB      int
		storedDisk int
		saveDisk   int
		wantShrink bool
	}{
		{"unrelated field, disk unchanged", 0, 100, 100, false},
		{"disk 0 saved again", 0, 0, 0, false},
		{"disk lowered", 0, 100, 40, true},
		{"disk raised under a tenant cap", 50, 60, 70, false},
		{"disk lowered but clamped to the same size", 50, 70, 60, false},
		{"disk lowered below the cap", 50, 70, 30, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st, mgr, victim, tn := destroyFixture(t, fixedRuntimeFactory{shrinkingHomeStub{}})
			mem, _, err := st.GetMembership(ctx, victim.ID, tn.ID)
			if err != nil {
				t.Fatal(err)
			}
			if c.capGB > 0 {
				lj, _ := json.Marshal(tenantLimits{MaxWorkspaceDiskGB: c.capGB})
				if err := st.SetTenantLimits(ctx, tn.ID, string(lj)); err != nil {
					t.Fatal(err)
				}
			}
			if err := st.PutUserLimit(ctx, mem.ID, store.UserQuota{MaxSessions: 2, DiskGB: c.storedDisk}); err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			body := `{"tenant_slug":"sales","user_key":"leaver-acme-co-jp","max_sessions":3,"disk_gb":` + strconv.Itoa(c.saveDisk) + `}`
			newAdminAPI(mgr).setUserLimit(w, adminRequest(http.MethodPut, "/api/admin/user-limits", body))
			if w.Code != http.StatusOK {
				t.Fatalf("save = %d %s", w.Code, w.Body.String())
			}
			if got := strings.Contains(w.Body.String(), `"home_resize"`); got != c.wantShrink {
				t.Errorf("home_resize present = %v, want %v: %s", got, c.wantShrink, w.Body.String())
			}
		})
	}
}
