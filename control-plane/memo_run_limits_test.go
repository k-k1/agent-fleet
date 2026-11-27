package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func TestMemoRunLimitAdminSettings(t *testing.T) {
	st, mgr, member, tenant := destroyFixture(t, &destroyingFactory{})
	mem, ok, err := st.GetMembership(t.Context(), member.ID, tenant.ID)
	if err != nil || !ok {
		t.Fatalf("membership: %v", err)
	}
	admin := newAdminAPI(mgr)
	putMember := func(body string) *httptest.ResponseRecorder {
		r := adminRequest(http.MethodPut, "/api/admin/user-limits", body)
		w := httptest.NewRecorder()
		admin.setUserLimit(w, r)
		return w
	}
	expectCapacity := func(want int) {
		t.Helper()
		if err := st.MemoRead(t.Context(), mem.ID, func(rd store.MemoWaitOps) error {
			c, err := rd.MemoRunCapacity("")
			if err == nil && (c.Open != 0 || c.Limit != want) {
				t.Errorf("empty queue capacity = %+v, want 0 / %d", c, want)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	expectCapacity(4)
	for _, tc := range []struct {
		body string
		want int
	}{
		{`{"memo_run_limit":7}`, 7},
		{`{"max_sessions":15}`, 7},
	} {
		w := putLimits(mgr, tenant.Slug, tc.body)
		got := decode[struct {
			Limit int `json:"memo_run_limit"`
		}](t, w)
		if w.Code != http.StatusOK || got.Limit != tc.want {
			t.Fatalf("PUT tenant limits (%s): %d %s", tc.body, w.Code, w.Body.String())
		}
		expectCapacity(tc.want)
	}
	memberBody := fmt.Sprintf(`"tenant_slug":%q,"user_key":%q`, tenant.Slug, member.UserKey)
	expectMember := func(settings string, raw, effective int) {
		t.Helper()
		w := putMember("{" + memberBody + "," + settings + "}")
		if w.Code != http.StatusOK {
			t.Fatalf("PUT member limits: %d %s", w.Code, w.Body.String())
		}
		got := decode[struct {
			Override  *int `json:"memo_run_limit"`
			Effective int  `json:"memo_run_limit_effective"`
		}](t, w)
		if got.Override == nil || *got.Override != raw || got.Effective != effective {
			t.Fatalf("PUT member limits: %s, want override %d effective %d", w.Body.String(), raw, effective)
		}
		expectCapacity(effective)
	}
	expectMember(`"memo_run_limit":10`, 10, 10)
	expectMember(`"max_sessions":13`, 10, 10)
	// MCP and other quota users don't pass a memo override.
	if err := st.PutUserLimit(t.Context(), mem.ID, store.UserQuota{MaxSessions: 14}); err != nil {
		t.Fatal(err)
	}
	expectCapacity(10)
	expectMember(`"memo_run_limit":0`, 0, 7)
	for _, invalid := range []int{-1, 11} {
		limit := fmt.Sprintf(`"memo_run_limit":%d`, invalid)
		for _, w := range []*httptest.ResponseRecorder{putLimits(mgr, tenant.Slug, "{"+limit+"}"), putMember("{" + memberBody + "," + limit + "}")} {
			if w.Code != http.StatusBadRequest || decode[errResp](t, w).Error.Code != "bad_memo_run_limit" {
				t.Fatalf("invalid limit %d: %d %s", invalid, w.Code, w.Body.String())
			}
		}
	}
	expectCapacity(7)

	r := adminRequest(http.MethodGet, "/api/admin/tenants/sales/members", "")
	r.SetPathValue("slug", tenant.Slug)
	w := httptest.NewRecorder()
	admin.listMembers(w, r)
	got := decode[struct {
		Members []struct {
			UserKey   string `json:"user_key"`
			Effective int    `json:"memo_run_limit_effective"`
			Default   int    `json:"memo_run_limit_default"`
		} `json:"members"`
	}](t, w)
	found := false
	for _, row := range got.Members {
		if row.UserKey == member.UserKey {
			found = row.Default == 7 && row.Effective == 7
		}
	}
	if w.Code != http.StatusOK || !found {
		t.Fatalf("GET members: %d %s, want the inherited limit 7", w.Code, w.Body.String())
	}
	audit := strings.Join(auditActions(t, st, tenant), "\n")
	if !strings.Contains(audit, "tenant.limits:memo_run_limit=7") || !strings.Contains(audit, "memo_run_limit=10") || !strings.Contains(audit, "memo_run_limit=0") {
		t.Fatalf("missing limit audit entries:\n%s", audit)
	}
}

func TestMemoRunLimitGatesStartsAfterDecrease(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		_, other := e.autoQueue("app", "other", nil, 2)
		now := time.Now()
		e.autoTick(now)
		e.autoTick(now)
		runs := e.liveDeliveries(other)
		if len(runs) != 2 {
			t.Fatalf("started %d runs, want 2", len(runs))
		}
		q := e.queueWith("app", "next", map[string]any{"enabled": true})
		m := e.pending("app", "next", "next")
		limit := 1
		if _, err := e.st.PutUserLimitWithMemoRunLimit(t.Context(), e.members["a"], store.UserQuota{}, &limit); err != nil {
			t.Fatal(err)
		}
		list := decode[memoQueuesWire](t, e.do("a", "GET", "/api/memo-queues", nil))
		if list.OpenRuns != 2 || list.RunLimit != 1 {
			t.Fatalf("lowered capacity = %d / %d, want 2 / 1", list.OpenRuns, list.RunLimit)
		}
		found := false
		for _, queue := range list.Queues {
			if queue.CategoryID != q {
				continue
			}
			found = true
			for _, w := range []*memoWaiting{queue.Waiting, queue.FireBlock} {
				if w == nil || w.Reason != waitNoPlace || w.Detail == nil || w.Detail.By != "member" || w.Detail.RunLimit != 1 {
					t.Fatalf("waiting or fire block: %+v", w)
				}
			}
		}
		if !found {
			t.Fatalf("queue %s missing from the list", q)
		}
		for i, d := range runs {
			for _, suffix := range []string{"/fire", "/fire/" + m.ID} {
				rec := e.do("a", "POST", "/api/memo-queues/"+q+suffix, nil)
				got := decode[struct {
					errResp
					RunLimit int `json:"runLimit"`
				}](t, rec)
				if rec.Code != http.StatusConflict || got.Error.Code != errCodeMemoQueueMemberLimit || got.RunLimit != 1 {
					t.Fatalf("capacity refusal (%s): %d %s", suffix, rec.Code, rec.Body.String())
				}
			}
			e.autoTick(now)
			if len(e.liveDeliveries(other)) != 2-i || len(e.liveDeliveries(q)) != 0 {
				t.Fatal("lower limit retired an existing run or allowed a new one too soon")
			}
			rec := e.do("a", "POST", "/api/memo-queues/"+other+"/deliveries/"+d.ID+"/end", nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("end delivery: %d %s", rec.Code, rec.Body.String())
			}
		}
		e.autoTick(now)
		if got := e.liveDeliveries(q); len(got) != 1 || !got[0].Auto || !sameIDs(got[0].MessageIDs, m.ID) {
			t.Fatalf("after capacity returned, want one automatic run of %s, got %+v", m.ID, got)
		}
	})
}

func TestMemoRunLimitGatesPrerequisiteWake(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, _, _, _ := e.prChain("clean", now.Add(-time.Minute))
		other := e.queueWith("app", "other", nil)
		e.pending("app", "other", "occupies a slot")
		d := e.fired(other)
		e.runner().send(t.Context(), e.delivery(d.ID))
		if got := e.delivery(d.ID); got.State != store.DeliveryOpen {
			t.Fatalf("blocking delivery is %s, want open", got.State)
		}
		e.setRuntime(agent, "stopped")
		limit := 1
		if _, err := e.st.PutUserLimitWithMemoRunLimit(t.Context(), e.members["a"], store.UserQuota{}, &limit); err != nil {
			t.Fatal(err)
		}
		e.autoTick(now)
		if n := e.prWakes(now.Add(-time.Hour)); n != 0 {
			t.Fatalf("recorded %d prerequisite wakes while there was no free slot", n)
		}
		limit = 2
		if _, err := e.st.PutUserLimitWithMemoRunLimit(t.Context(), e.members["a"], store.UserQuota{}, &limit); err != nil {
			t.Fatal(err)
		}
		e.autoTick(now)
		if n := e.prWakes(now.Add(-time.Hour)); n != 1 {
			t.Fatalf("recorded %d prerequisite wakes after a slot was available, want 1", n)
		}
	})
}
