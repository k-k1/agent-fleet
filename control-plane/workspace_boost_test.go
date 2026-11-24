package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
	"github.com/k-k1/agent-fleet/control-plane/internal/usagehistory"
)

// ADR 0139 decisions 4 and 5: the boost override, its resolution in withResolvedSize and the API
// around it.

func boostSizing() runtime.WorkspaceSizing {
	rung := func(t string, mem int64) runtime.WorkspaceSlot {
		return runtime.WorkspaceSlot{InstanceType: t, MemMiB: mem, UsableMemMiB: mem - 2048}
	}
	return runtime.WorkspaceSizing{
		Runtime: "ecs-ec2", MemMeaning: runtime.MemMeaningSlot, DefaultSlotClass: "standard",
		SlotClasses: []runtime.WorkspaceSlotClass{
			{ID: "standard", Label: "Standard", Arch: "x86_64",
				Slots: []runtime.WorkspaceSlot{rung("m7i.large", 8192), rung("m7i.xlarge", 16384), rung("m7i.2xlarge", 32768)}},
			{ID: "arm", Label: "Arm", Arch: "arm64",
				Slots: []runtime.WorkspaceSlot{rung("m8g.large", 8192), rung("m8g.xlarge", 16384)}},
		},
	}
}

type boostEnv struct {
	m        *manager
	tenantID string
	memID    string
	ident    store.Identity
	now      time.Time
}

func newBoostEnv(t *testing.T, lim tenantLimits) *boostEnv {
	t.Helper()
	ctx := context.Background()
	m := p3Manager(t, p3Store(t))
	m.rtFactory = sizingOnlyFactory{sizing: boostSizing()}
	ten, err := m.store.CreateTenant(ctx, "acme", "Acme")
	if err != nil {
		t.Fatal(err)
	}
	ident, err := m.store.UpsertIdentity(ctx, "a@example.com", "a", "")
	if err != nil {
		t.Fatal(err)
	}
	mem, err := m.store.EnsureMembership(ctx, ident.ID, ten.ID, "member")
	if err != nil {
		t.Fatal(err)
	}
	e := &boostEnv{m: m, tenantID: ten.ID, memID: mem.ID, ident: ident, now: time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)}
	e.setLimits(t, lim)
	old := boostNow
	boostNow = func() time.Time { return e.now }
	t.Cleanup(func() { boostNow = old })
	return e
}

func (e *boostEnv) setLimits(t *testing.T, lim tenantLimits) {
	t.Helper()
	lj, _ := json.Marshal(lim)
	if err := e.m.store.SetTenantLimits(context.Background(), e.tenantID, string(lj)); err != nil {
		t.Fatal(err)
	}
}

func (e *boostEnv) ws() store.Workspace {
	return store.Workspace{TenantID: e.tenantID, MembershipID: e.memID}
}

func (e *boostEnv) grant(t *testing.T, class string, memMiB int64, until time.Duration) {
	t.Helper()
	if err := e.m.store.PutSizeOverride(context.Background(), store.SizeOverride{
		MembershipID: e.memID, Source: store.SizeOverrideBoost, SlotClass: class, MemMiB: memMiB,
		ExpiresAt: e.now.Add(until).Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
}

func (e *boostEnv) store(t *testing.T, q store.UserQuota) {
	t.Helper()
	if err := e.m.store.PutUserLimit(context.Background(), e.memID, q); err != nil {
		t.Fatal(err)
	}
}

var onBoost = tenantLimits{BoostEnabled: true}

func TestBoostResolution(t *testing.T) {
	ctx := context.Background()
	const gib8, gib16 = 8192, 16384

	t.Run("a live boost is one rung above the stored box and the stored size is untouched", func(t *testing.T) {
		e := newBoostEnv(t, onBoost)
		e.store(t, store.UserQuota{MemLimit: 8 * gib, SlotClass: "standard"})
		e.grant(t, "standard", gib16, time.Hour)
		ws := e.m.withResolvedSize(ctx, e.ws())
		if ws.MemBytes != gib16*mib || ws.SlotClass != "standard" {
			t.Fatalf("resolved %d MiB class %q, want 16384 standard", ws.MemBytes/mib, ws.SlotClass)
		}
		ul, _, _ := e.m.store.GetUserLimit(ctx, e.memID)
		if ul.MemLimit != 8*gib {
			t.Fatalf("the stored size was rewritten to %d", ul.MemLimit)
		}
	})

	t.Run("a stored size of 0 is the smallest rung and a boost is the next one", func(t *testing.T) {
		e := newBoostEnv(t, onBoost)
		e.grant(t, "standard", gib16, time.Hour)
		ws := e.m.withResolvedSize(ctx, e.ws())
		if ws.MemBytes != gib16*mib {
			t.Fatalf("resolved %d MiB, want 16384", ws.MemBytes/mib)
		}
		ns := e.m.resolveNextStart(ctx, e.ws())
		if ns.Source != "boost" || ns.baseIdx != 0 {
			t.Fatalf("source %q baseIdx %d, want boost over rung 0", ns.Source, ns.baseIdx)
		}
	})

	t.Run("an expired boost is inert", func(t *testing.T) {
		e := newBoostEnv(t, onBoost)
		e.grant(t, "standard", gib16, time.Hour)
		e.now = e.now.Add(2 * time.Hour)
		ns := e.m.resolveNextStart(ctx, e.ws())
		if ns.Source != "stored" || ns.MemBytes != 0 || ns.Note != "" {
			t.Fatalf("%+v, want the stored box and no note", ns)
		}
	})

	notApplied := map[string]func(*testing.T, *boostEnv){
		"the tenant switched boost off": func(t *testing.T, e *boostEnv) { e.setLimits(t, tenantLimits{}) },
		"the class is no longer allowed": func(t *testing.T, e *boostEnv) {
			e.setLimits(t, tenantLimits{BoostEnabled: true, AllowedSlotClasses: []string{"arm"}})
		},
		"the member moved to another class": func(t *testing.T, e *boostEnv) {
			e.store(t, store.UserQuota{SlotClass: "arm"})
		},
		"the tenant memory maximum is below the rung": func(t *testing.T, e *boostEnv) {
			e.setLimits(t, tenantLimits{BoostEnabled: true, MaxWorkspaceMem: 8 * gib})
		},
		"the deployment memory maximum is below the rung": func(t *testing.T, e *boostEnv) { e.m.memMaxBytes = 8 * gib },
		"the tenant's highest boost rung is below it": func(t *testing.T, e *boostEnv) {
			e.setLimits(t, tenantLimits{BoostEnabled: true, BoostMaxMemMiB: 8192})
		},
		"the rung is no longer declared":  func(t *testing.T, e *boostEnv) { e.grant(t, "standard", 12288, time.Hour) },
		"the class is no longer declared": func(t *testing.T, e *boostEnv) { e.grant(t, "gone", 16384, time.Hour) },
		"the stored size already reaches the rung": func(t *testing.T, e *boostEnv) {
			e.store(t, store.UserQuota{MemLimit: 16 * gib})
		},
	}
	for name, mutate := range notApplied {
		t.Run("not applied when "+name, func(t *testing.T) {
			e := newBoostEnv(t, onBoost)
			e.grant(t, "standard", gib16, time.Hour)
			mutate(t, e)
			ns := e.m.resolveNextStart(ctx, e.ws())
			if ns.Source == "boost" {
				t.Fatalf("applied anyway: %+v", ns)
			}
			if ns.Note == "" {
				t.Fatalf("an override that was not applied must say why: %+v", ns)
			}
			// It never falls through to the deployment default class: the base is what the
			// stored choice resolved to, whatever that is.
			base := e.m.withResolvedSize(ctx, e.ws())
			wantClass, _ := e.m.resolveSlotClass(ctx, e.ws())
			if base.SlotClass != wantClass || base.MemBytes != e.m.resolveWorkspaceMemBytes(ctx, e.ws()) {
				t.Fatalf("resolved %q/%d, want the un-boosted %q/%d", base.SlotClass, base.MemBytes, wantClass, e.m.resolveWorkspaceMemBytes(ctx, e.ws()))
			}
		})
	}

	t.Run("a boost for a class the tenant no longer allows does not land on the deployment default", func(t *testing.T) {
		e := newBoostEnv(t, tenantLimits{BoostEnabled: true, SlotClass: "arm"})
		e.grant(t, "arm", gib16, time.Hour)
		e.setLimits(t, tenantLimits{BoostEnabled: true, SlotClass: "arm", AllowedSlotClasses: []string{"standard"}})
		ws := e.m.withResolvedSize(ctx, e.ws())
		if ws.MemBytes == gib16*mib && ws.SlotClass == "arm" {
			t.Fatalf("applied a boost in a class the tenant may not use")
		}
	})

	t.Run("no boost on a runtime with no rungs", func(t *testing.T) {
		e := newBoostEnv(t, onBoost)
		e.m.rtFactory = sizingOnlyFactory{sizing: runtime.WorkspaceSizing{Runtime: "ecs", MemMeaning: runtime.MemMeaningLimit}}
		e.grant(t, "", gib16, time.Hour)
		if ns := e.m.resolveNextStart(ctx, e.ws()); ns.Source != "stored" {
			t.Fatalf("%+v", ns)
		}
	})
}

func (e *boostEnv) call(t *testing.T, method, body string) (int, workspaceSizingWire, string) {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, "/api/workspace/boost", nil)
	} else {
		r = httptest.NewRequest(method, "/api/workspace/boost", strings.NewReader(body))
	}
	w := httptest.NewRecorder()
	res := &resolved{ws: e.ws(), ident: e.ident}
	api := newWorkspaceAPI(e.m, false)
	if method == http.MethodDelete {
		api.cancelBoost(w, r, res)
	} else {
		api.boost(w, r, res)
	}
	var out workspaceSizingWire
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out, w.Body.String()
}

type countingBoost struct {
	hours   float64
	counted bool
	members []memberBoostHours
	err     error
}

func (c countingBoost) BoostedRunningHours(context.Context, string, time.Time, time.Time) (float64, bool, error) {
	return c.hours, c.counted, c.err
}

func (c countingBoost) TenantBoostedRunningHours(context.Context, string, time.Time, time.Time) ([]memberBoostHours, bool, error) {
	return c.members, c.counted, c.err
}

func TestBoostAPI(t *testing.T) {
	ctx := context.Background()

	t.Run("grants one rung up, applied at the next start", func(t *testing.T) {
		e := newBoostEnv(t, tenantLimits{BoostEnabled: true, BoostMonthlyHours: 40})
		code, out, body := e.call(t, http.MethodPost, `{"hours":6}`)
		if code != 200 {
			t.Fatalf("%d %s", code, body)
		}
		if out.NextStart.Source != "boost" || out.NextStart.Box.MemMiB != 16384 || out.Stored.MemMiB != 8192 {
			t.Fatalf("%+v", out)
		}
		if want := e.now.Add(6 * time.Hour).Format(time.RFC3339); out.NextStart.Until != want {
			t.Fatalf("until %q, want %q", out.NextStart.Until, want)
		}
		if out.RestartOffered {
			t.Fatalf("nothing is running, so no restart is offered")
		}
		if rows := auditRowsOf(t, e.m.store, e.tenantID, "workspace.boost"); len(rows) != 1 {
			t.Fatalf("audit rows %+v", rows)
		}
	})

	t.Run("boosting while a boost is live extends it and does not climb", func(t *testing.T) {
		e := newBoostEnv(t, onBoost)
		e.call(t, http.MethodPost, `{"hours":2}`)
		e.now = e.now.Add(time.Hour)
		code, out, body := e.call(t, http.MethodPost, `{"hours":8}`)
		if code != 200 || out.NextStart.Box.MemMiB != 16384 {
			t.Fatalf("%d %s", code, body)
		}
		if want := e.now.Add(8 * time.Hour).Format(time.RFC3339); out.NextStart.Until != want {
			t.Fatalf("until %q, want %q", out.NextStart.Until, want)
		}
		// A shorter request never shortens a live boost.
		_, out, _ = e.call(t, http.MethodPost, `{"hours":1}`)
		if want := e.now.Add(8 * time.Hour).Format(time.RFC3339); out.NextStart.Until != want {
			t.Fatalf("until %q after a shorter request, want %q", out.NextStart.Until, want)
		}
	})

	t.Run("a boost after expiry is a new boost from the next-start box", func(t *testing.T) {
		e := newBoostEnv(t, onBoost)
		e.call(t, http.MethodPost, `{"hours":1}`)
		e.now = e.now.Add(3 * time.Hour)
		code, out, _ := e.call(t, http.MethodPost, `{"hours":1}`)
		if code != 200 || out.NextStart.Box.MemMiB != 16384 || out.Stored.MemMiB != 8192 {
			t.Fatalf("%d %+v", code, out)
		}
	})

	t.Run("refusals", func(t *testing.T) {
		cases := []struct {
			name  string
			lim   tenantLimits
			q     store.UserQuota
			body  string
			count *countingBoost
			code  int
			errc  string
		}{
			{"disabled", tenantLimits{}, store.UserQuota{}, ``, nil, 403, "boost_disabled"},
			{"top rung", onBoost, store.UserQuota{MemLimit: 32 * gib}, ``, nil, 409, "boost_top_rung"},
			{"tenant highest rung", tenantLimits{BoostEnabled: true, BoostMaxMemMiB: 8192}, store.UserQuota{}, ``, nil, 409, "boost_limit"},
			{"hours too many", onBoost, store.UserQuota{}, `{"hours":99}`, nil, 400, "bad_hours"},
			{"budget spent", tenantLimits{BoostEnabled: true, BoostMonthlyHours: 10}, store.UserQuota{}, ``, &countingBoost{hours: 10, counted: true}, 409, "boost_budget_spent"},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				e := newBoostEnv(t, c.lim)
				e.store(t, c.q)
				if c.count != nil {
					e.m.boostHours = *c.count
				}
				code, _, body := e.call(t, http.MethodPost, c.body)
				if code != c.code || !strings.Contains(body, c.errc) {
					t.Fatalf("%d %s, want %d %s", code, body, c.code, c.errc)
				}
				if _, ok, _ := e.m.store.GetSizeOverride(ctx, e.memID, store.SizeOverrideBoost); ok {
					t.Fatalf("a refused boost left a row")
				}
			})
		}
	})

	t.Run("the budget is not enforced while hours cannot be counted", func(t *testing.T) {
		e := newBoostEnv(t, tenantLimits{BoostEnabled: true, BoostMonthlyHours: 1})
		if code, _, body := e.call(t, http.MethodPost, ``); code != 200 {
			t.Fatalf("no counter wired: %d %s", code, body)
		}
		e.m.boostHours = countingBoost{hours: 99, counted: false}
		if code, _, body := e.call(t, http.MethodPost, ``); code != 200 {
			t.Fatalf("counter says not counted: %d %s", code, body)
		}
	})

	t.Run("a boost granted under budget is offered back as spent", func(t *testing.T) {
		e := newBoostEnv(t, tenantLimits{BoostEnabled: true, BoostMonthlyHours: 5})
		e.m.boostHours = countingBoost{hours: 5, counted: true}
		sz := e.m.sizingFor(ctx, &resolved{ws: e.ws(), ident: e.ident})
		if sz.Boost.Available || sz.Boost.Reason != "budget_spent" || !sz.Boost.Budget.Spent {
			t.Fatalf("%+v", sz.Boost)
		}
	})

	t.Run("cancel removes it for the next start", func(t *testing.T) {
		e := newBoostEnv(t, onBoost)
		e.call(t, http.MethodPost, ``)
		code, out, _ := e.call(t, http.MethodDelete, ``)
		if code != 200 || out.NextStart.Source != "stored" {
			t.Fatalf("%d %+v", code, out)
		}
	})
}

// runningFake is a runtime whose container is up and whose Agent says it is on a given box.
type runningFake struct {
	runtime.Runtime
	endpoint string
}

func (r runningFake) State(context.Context) string { return "running" }
func (r runningFake) Endpoint() string             { return r.endpoint }
func (r runningFake) Token() string                { return "" }

func TestSizingOffersARestartOnlyIntoABiggerBox(t *testing.T) {
	ctx := context.Background()
	agentOn := func(instance string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"instance_type":"` + instance + `"}`))
		}))
	}
	cases := []struct {
		name    string
		running string
		boosted bool
		restart bool
		runMem  int64
	}{
		{"running on the small box, boost live", "m7i.large", true, true, 8192},
		{"already on the boosted box", "m7i.xlarge", true, false, 16384},
		{"still on the boosted box after the boost expired", "m7i.xlarge", false, false, 16384},
		{"running on a bigger box than the next start", "m7i.2xlarge", true, false, 32768},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newBoostEnv(t, onBoost)
			if c.boosted {
				e.grant(t, "standard", 16384, time.Hour)
			}
			srv := agentOn(c.running)
			defer srv.Close()
			sz := e.m.sizingFor(ctx, &resolved{ws: e.ws(), ident: e.ident, rt: runningFake{endpoint: srv.URL}})
			if sz.Running == nil || sz.Running.MemMiB != c.runMem {
				t.Fatalf("running %+v, want %d MiB", sz.Running, c.runMem)
			}
			if sz.RestartOffered != c.restart {
				t.Fatalf("restart_offered=%v, want %v (next %+v running %+v)", sz.RestartOffered, c.restart, sz.NextStart, sz.Running)
			}
		})
	}

	t.Run("an unknown running box offers nothing", func(t *testing.T) {
		e := newBoostEnv(t, onBoost)
		e.grant(t, "standard", 16384, time.Hour)
		srv := agentOn("")
		defer srv.Close()
		sz := e.m.sizingFor(ctx, &resolved{ws: e.ws(), ident: e.ident, rt: runningFake{endpoint: srv.URL}})
		if sz.Running != nil || sz.RestartOffered {
			t.Fatalf("%+v", sz)
		}
	})
}

func TestTenantBoostPolicy(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	m := p3Manager(t, st)
	m.rtFactory = sizingOnlyFactory{sizing: boostSizing()}
	acme, _ := st.CreateTenant(ctx, "acme", "Acme")
	_, _ = st.CreateTenant(ctx, "other", "Other")
	admin, _ := st.UpsertIdentity(ctx, "boss@acme.co.jp", "boss-acme-co-jp", "")
	if _, err := st.EnsureMembership(ctx, admin.ID, acme.ID, "tenant_admin"); err != nil {
		t.Fatal(err)
	}
	mem, _ := st.UpsertIdentity(ctx, "m@acme.co.jp", "m-acme-co-jp", "")
	if _, err := st.EnsureMembership(ctx, mem.ID, acme.ID, "member"); err != nil {
		t.Fatal(err)
	}
	api := newWorkspaceAPI(m, false)
	do := func(slug, email, method, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/admin/tenants/"+slug+"/boost", strings.NewReader(body))
		r.Header.Set("X-Forwarded-Email", email)
		r.SetPathValue("slug", slug)
		w := httptest.NewRecorder()
		if method == http.MethodGet {
			api.tenantBoost(w, r)
		} else {
			api.setTenantBoost(w, r)
		}
		return w
	}
	put := `{"enabled":true,"max_mem_mib":16384,"monthly_hours":20}`

	if w := do("acme", "boss@acme.co.jp", http.MethodPut, put); w.Code != 200 {
		t.Fatalf("tenant admin PUT = %d %s", w.Code, w.Body.String())
	}
	tn, _, _ := st.GetTenantBySlug(ctx, "acme")
	if l := parseLimits(tn.Limits); !l.BoostEnabled || l.BoostMaxMemMiB != 16384 || l.BoostMonthlyHours != 20 {
		t.Fatalf("stored %+v", l)
	}
	if rows := auditRowsOf(t, st, acme.ID, "tenant.boost"); len(rows) != 1 {
		t.Fatalf("audit %+v", rows)
	}
	if w := do("acme", "boss@acme.co.jp", http.MethodGet, ""); !strings.Contains(w.Body.String(), `"monthly_hours":20`) || !strings.Contains(w.Body.String(), `"budget_counted":false`) {
		t.Fatalf("GET %s", w.Body.String())
	}

	// Cross-tenant and non-admin writes change nothing.
	if w := do("other", "boss@acme.co.jp", http.MethodPut, put); w.Code != http.StatusForbidden {
		t.Fatalf("another tenant's admin = %d %s, want 403", w.Code, w.Body.String())
	}
	if w := do("acme", "m@acme.co.jp", http.MethodPut, `{"enabled":false}`); w.Code != http.StatusForbidden {
		t.Fatalf("a member = %d, want 403", w.Code)
	}
	if o, _, _ := st.GetTenantBySlug(ctx, "other"); parseLimits(o.Limits).BoostEnabled {
		t.Fatalf("the other tenant was written")
	}
	for _, bad := range []string{`{"enabled":true,"max_mem_mib":12345}`, `{"monthly_hours":-1}`} {
		if w := do("acme", "boss@acme.co.jp", http.MethodPut, bad); w.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d, want 400", bad, w.Code)
		}
	}
	tn, _, _ = st.GetTenantBySlug(ctx, "acme")
	if !parseLimits(tn.Limits).BoostEnabled {
		t.Fatalf("a rejected PUT changed the policy")
	}

	// A super_admin's whole-blob save of the quotas must not erase the tenant admin's policy.
	if w := putLimits(m, "acme", `{"max_workspaces":4}`); w.Code != 200 {
		t.Fatalf("limits PUT = %d %s", w.Code, w.Body.String())
	}
	tn, _, _ = st.GetTenantBySlug(ctx, "acme")
	if l := parseLimits(tn.Limits); !l.BoostEnabled || l.BoostMaxMemMiB != 16384 || l.BoostMonthlyHours != 20 || l.MaxWorkspaces != 4 {
		t.Fatalf("after the limits save: %+v", l)
	}
}

func TestSizeOverrideStore(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	o := store.SizeOverride{MembershipID: "M1", Source: store.SizeOverrideBoost, SlotClass: "standard", MemMiB: 16384, ExpiresAt: "2026-11-17T10:00:00Z"}
	if err := st.PutSizeOverride(ctx, o); err != nil {
		t.Fatal(err)
	}
	auto := o
	auto.Source, auto.MemMiB = store.SizeOverrideAuto, 4096
	if err := st.PutSizeOverride(ctx, auto); err != nil {
		t.Fatal(err)
	}
	o.MemMiB = 32768
	if err := st.PutSizeOverride(ctx, o); err != nil {
		t.Fatal(err)
	}
	got, ok, _ := st.GetSizeOverride(ctx, "M1", store.SizeOverrideBoost)
	if !ok || got.MemMiB != 32768 {
		t.Fatalf("%+v", got)
	}
	if a, _, _ := st.GetSizeOverride(ctx, "M1", store.SizeOverrideAuto); a.MemMiB != 4096 {
		t.Fatalf("auto is stored apart from boost: %+v", a)
	}
	if err := st.DeleteSizeOverride(ctx, "M1", store.SizeOverrideBoost); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.GetSizeOverride(ctx, "M1", store.SizeOverrideBoost); ok {
		t.Fatal("not deleted")
	}
	if _, ok, _ := st.GetSizeOverride(ctx, "M1", store.SizeOverrideAuto); !ok {
		t.Fatal("deleting the boost took the auto override with it")
	}
}

func TestResolvedSizeSourceFollowsTheBoost(t *testing.T) {
	ctx := context.Background()
	e := newBoostEnv(t, onBoost)
	if _, src := e.m.withResolvedSizeSource(ctx, e.ws()); src != "" {
		t.Fatalf("no boost: override %q", src)
	}
	e.grant(t, "standard", 16384, time.Hour)
	if ws, src := e.m.withResolvedSizeSource(ctx, e.ws()); src != "boost" || ws.MemBytes != 16384*mib {
		t.Fatalf("live boost: override %q mem %d, want boost on 16384", src, ws.MemBytes/mib)
	}
	e.now = e.now.Add(2 * time.Hour)
	if ws, src := e.m.withResolvedSizeSource(ctx, e.ws()); src != "" || ws.MemBytes != 0 {
		t.Fatalf("expired: override %q mem %d", src, ws.MemBytes)
	}
}

// boxFactory builds runtimes that name the rung their size resolved to, the way ecs-ec2 does,
// and records the box each Start ran on.
type boxFactory struct {
	mu         sync.Mutex
	started    []string
	startedEnv [][]string
}

type boxStartRT struct {
	stubRuntime
	f   *boxFactory
	box runtime.WorkspaceMachine
	env []string
}

func (r *boxStartRT) MachineProfile() runtime.WorkspaceMachine { return r.box }
func (r *boxStartRT) Start(context.Context) error {
	r.f.mu.Lock()
	defer r.f.mu.Unlock()
	r.f.started = append(r.f.started, r.box.InstanceType)
	r.f.startedEnv = append(r.f.startedEnv, r.env)
	return nil
}

func (f *boxFactory) New(ws runtime.Workspace, _ runtime.SecretKeys, env []string) runtime.Runtime {
	ladder := ladderOf(boostSizing(), ws.SlotClass)
	s := ladder[rungIndex(ladder, ws.MemBytes)]
	return &boxStartRT{stubRuntime: stubRuntime{state: "stopped"}, f: f, env: env,
		box: runtime.WorkspaceMachine{InstanceType: s.InstanceType, ClassID: ws.SlotClass}}
}

func (f *boxFactory) SizingProfile() runtime.WorkspaceSizing { return boostSizing() }

// TestStartRecordsTheBoxItStarts: a boost that ends while a Start is under way must not leave
// the launch record saying "boost" over an unboosted box (that box's hours would be charged to
// the budget), nor the reverse. The Start resolves the box once and both take it from there.
func TestStartRecordsTheBoxItStarts(t *testing.T) {
	ctx := context.Background()
	e := newBoostEnv(t, onBoost)
	f := &boxFactory{}
	e.m.rtFactory = f
	if err := e.m.store.CreateWorkspace(ctx, store.Workspace{ID: "W-1", TenantID: e.tenantID, MembershipID: e.memID,
		ContainerName: "af-ws-acme-a", AgentPort: "7731", AgentToken: "tok", State: "stopped", CreatedAt: store.NowTS()}); err != nil {
		t.Fatal(err)
	}
	e.grant(t, "standard", 16384, time.Hour)
	mv, _, _ := e.m.store.GetMembershipByID(ctx, e.memID)
	res, aerr := e.m.buildResolved(ctx, e.ident, mv)
	if aerr != nil {
		t.Fatal(aerr)
	}
	// From here the boost has ended on the second read of the clock and on every one after it.
	reads := 0
	expiry := e.now.Add(time.Hour)
	boostNow = func() time.Time {
		reads++
		if reads == 1 {
			return expiry.Add(-time.Millisecond)
		}
		return expiry.Add(time.Millisecond)
	}
	if aerr := newWorkspaceAPI(e.m, false).ensureWorkspaceStarted(ctx, res); aerr != nil {
		t.Fatal(aerr)
	}
	l, ok, err := e.m.store.OpenLaunch(ctx, e.memID)
	if err != nil || !ok {
		t.Fatalf("no launch recorded: %v", err)
	}
	f.mu.Lock()
	started := f.started
	f.mu.Unlock()
	if len(started) != 1 {
		t.Fatalf("started %v", started)
	}
	// Whichever side of the expiry the one read fell on, the record and the box agree.
	if boosted := started[0] == "m7i.xlarge"; boosted != (l.Override == "boost") || l.InstanceType != started[0] {
		t.Fatalf("started on %s, recorded %s override %q", started[0], l.InstanceType, l.Override)
	}
}

// TestStartRecordsTheRuntimeItStarts: the runtime handed to the Start was built for another box
// than the one the Start resolves (an unattended start whose own read of the boost failed). The
// launch record is read off the runtime that actually starts, not off the one handed in.
func TestStartRecordsTheRuntimeItStarts(t *testing.T) {
	ctx := context.Background()
	e := newBoostEnv(t, onBoost)
	f := &boxFactory{}
	e.m.rtFactory = f
	if err := e.m.store.CreateWorkspace(ctx, store.Workspace{ID: "W-1", TenantID: e.tenantID, MembershipID: e.memID,
		ContainerName: "af-ws-acme-a", AgentPort: "7731", AgentToken: "tok", State: "stopped", CreatedAt: store.NowTS()}); err != nil {
		t.Fatal(err)
	}
	e.grant(t, "standard", 16384, time.Hour)
	mv, _, _ := e.m.store.GetMembershipByID(ctx, e.memID)
	res, aerr := e.m.buildResolved(ctx, e.ident, mv)
	if aerr != nil {
		t.Fatal(aerr)
	}
	unboosted := f.New(runtime.Workspace{SlotClass: "standard"}, runtime.SecretKeys{}, nil)
	if aerr := newWorkspaceAPI(e.m, false).ensureWorkspaceStartedRT(ctx, res, unboosted); aerr != nil {
		t.Fatal(aerr)
	}
	l, _, _ := e.m.store.OpenLaunch(ctx, e.memID)
	f.mu.Lock()
	started := f.started
	f.mu.Unlock()
	if len(started) != 1 || started[0] != "m7i.xlarge" || l.InstanceType != "m7i.xlarge" || l.Override != "boost" {
		t.Fatalf("started %v, recorded %s override %q", started, l.InstanceType, l.Override)
	}
	// The runtime that started carries the recorded launch's id, which is what files the
	// Agent's minutes under this launch.
	f.mu.Lock()
	env := f.startedEnv[0]
	f.mu.Unlock()
	if got := envValue(strings.Join(env, " "), usagehistory.LaunchEnv); got != l.ID {
		t.Fatalf("started with %s=%q, recorded launch %q", usagehistory.LaunchEnv, got, l.ID)
	}
}

func TestBoostNoEndAndShadowedRows(t *testing.T) {
	ctx := context.Background()
	t.Run("an empty expiry means no end", func(t *testing.T) {
		e := newBoostEnv(t, onBoost)
		if err := e.m.store.PutSizeOverride(ctx, store.SizeOverride{MembershipID: e.memID, Source: store.SizeOverrideBoost, SlotClass: "standard", MemMiB: 16384}); err != nil {
			t.Fatal(err)
		}
		e.now = e.now.Add(1000 * time.Hour)
		if ns := e.m.resolveNextStart(ctx, e.ws()); ns.Source != "boost" {
			t.Fatalf("%+v", ns)
		}
	})

	t.Run("a boost row the stored size caught up with boosts nothing, so pressing is a new boost", func(t *testing.T) {
		e := newBoostEnv(t, onBoost)
		e.grant(t, "standard", 16384, time.Hour)
		e.store(t, store.UserQuota{MemLimit: 16 * gib}) // the stored size now reaches the row's rung
		code, out, body := e.call(t, http.MethodPost, ``)
		if code != 200 || out.NextStart.Box.MemMiB != 32768 || out.Stored.MemMiB != 16384 {
			t.Fatalf("%d %s", code, body)
		}
	})

	t.Run("a base class the tenant does not allow cannot be boosted", func(t *testing.T) {
		// The tenant allows only a class the deployment no longer declares, so resolveSlotClass
		// falls back to the deployment default (standard) as a last resort and the overlay would
		// refuse it: the plan must say so instead of granting a row that never applies.
		e := newBoostEnv(t, tenantLimits{BoostEnabled: true, AllowedSlotClasses: []string{"retired"}})
		sz := e.m.sizingFor(ctx, &resolved{ws: e.ws(), ident: e.ident})
		if sz.Boost.Available || sz.Boost.Reason != "class" {
			t.Fatalf("%+v", sz.Boost)
		}
		if code, _, body := e.call(t, http.MethodPost, ``); code != 409 || !strings.Contains(body, "boost_class") {
			t.Fatalf("%d %s", code, body)
		}
		if _, ok, _ := e.m.store.GetSizeOverride(ctx, e.memID, store.SizeOverrideBoost); ok {
			t.Fatal("a dead row was written")
		}
	})

	t.Run("a memoized runtime built under a boost is dropped when the boost ends", func(t *testing.T) {
		e := newBoostEnv(t, onBoost)
		e.m.rts["M"] = cachedRT{sizeUntil: e.now.Add(time.Hour)}
		if _, ok := e.m.cachedRTFor("M"); !ok {
			t.Fatal("dropped too early")
		}
		e.now = e.now.Add(2 * time.Hour)
		if _, ok := e.m.cachedRTFor("M"); ok {
			t.Fatal("served a runtime sized for an ended boost")
		}
	})
}

func TestRunningBoxFallsBackToTheMemoryCap(t *testing.T) {
	ctx := context.Background()
	e := newBoostEnv(t, onBoost)
	e.grant(t, "standard", 16384, time.Hour)
	// No SMBIOS reading: the cgroup cap (8192 less the 2048 reserve) names the small rung.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"mem_max":6442450944}`))
	}))
	defer srv.Close()
	sz := e.m.sizingFor(ctx, &resolved{ws: e.ws(), ident: e.ident, rt: runningFake{endpoint: srv.URL}})
	if sz.Running == nil || sz.Running.MemMiB != 8192 || !sz.RestartOffered {
		t.Fatalf("%+v", sz)
	}
}

// TestBoostBudgetCountsLaunchRecords: the production counter reads the launch records, so the
// budget is enforced from the hours a member actually ran boosted this month (ADR 0139 decision
// 5), whether or not the boost has since expired.
func TestBoostBudgetCountsLaunchRecords(t *testing.T) {
	ctx := context.Background()
	e := newBoostEnv(t, tenantLimits{BoostEnabled: true, BoostMonthlyHours: 5})
	e.m.boostHours = boostHoursFor(e.m.store, 5*time.Minute)
	at := func(d time.Duration) int64 { return e.now.Add(d).Unix() }
	launch := func(id, override string, from, to time.Duration) {
		t.Helper()
		if err := e.m.store.BeginLaunch(ctx, store.Launch{ID: id, WorkspaceID: "w1", MembershipID: e.memID,
			TenantID: e.tenantID, StartedAt: at(from), Override: override}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := e.m.store.EndOpenLaunch(ctx, e.memID, at(to)); err != nil {
			t.Fatal(err)
		}
	}
	// Last month's boosted hours, unboosted and adopted launches, and a boost that never ran
	// count for nothing.
	launch("prev", store.SizeOverrideBoost, -40*24*time.Hour, -39*24*time.Hour)
	launch("plain", "", -100*time.Hour, -90*time.Hour)
	launch("adopted", "adopted", -80*time.Hour, -70*time.Hour)
	launch("never", store.SizeOverrideBoost, -60*time.Hour, -60*time.Hour)
	// Four boosted hours this month: well past any boost's end, which is the point.
	launch("b1", store.SizeOverrideBoost, -50*time.Hour, -46*time.Hour)

	sz := e.m.sizingFor(ctx, &resolved{ws: e.ws(), ident: e.ident})
	if b := sz.Boost.Budget; !b.Counted || b.UsedHours != 4 || b.Spent || !sz.Boost.Available {
		t.Fatalf("4 of 5 hours: %+v available=%t", b, sz.Boost.Available)
	}
	if code, _, body := e.call(t, http.MethodPost, ``); code != 200 {
		t.Fatalf("under budget: %d %s", code, body)
	}

	// One more boosted hour, still open and last seen an hour in.
	if err := e.m.store.BeginLaunch(ctx, store.Launch{ID: "b2", WorkspaceID: "w1", MembershipID: e.memID,
		TenantID: e.tenantID, StartedAt: at(-2 * time.Hour), Override: store.SizeOverrideBoost}); err != nil {
		t.Fatal(err)
	}
	if err := e.m.store.TouchLaunch(ctx, "b2", at(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	sz = e.m.sizingFor(ctx, &resolved{ws: e.ws(), ident: e.ident})
	if b := sz.Boost.Budget; b.UsedHours != 5 || !b.Spent || sz.Boost.Reason != "budget_spent" {
		t.Fatalf("5 of 5 hours: %+v reason=%q", b, sz.Boost.Reason)
	}
	// Spent refuses an extension of the live boost as well as a new one.
	if code, _, body := e.call(t, http.MethodPost, `{"hours":2}`); code != http.StatusConflict || !strings.Contains(body, "boost_budget_spent") {
		t.Fatalf("over budget: %d %s", code, body)
	}

	// A new month starts from zero.
	e.now = time.Date(2026, 12, 1, 0, 30, 0, 0, time.UTC)
	if b := e.m.boostBudgetOf(ctx, e.memID, tenantLimits{BoostEnabled: true, BoostMonthlyHours: 5}); b.UsedHours != 0 || b.Spent {
		t.Fatalf("a new month: %+v", b)
	}
}

// TestBoostBudgetIsCountedOnlyWithTheSampler: main's wiring counts the budget while the usage
// sampler runs, and says it is not counted when the sampler is off, where an open launch's hours
// would read 0 for as long as it runs.
func TestBoostBudgetIsCountedOnlyWithTheSampler(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		interval time.Duration
		counted  bool
	}{{5 * time.Minute, true}, {0, false}, {500 * time.Millisecond, false}} {
		e := newBoostEnv(t, tenantLimits{BoostEnabled: true, BoostMonthlyHours: 1})
		e.m.boostHours = boostHoursFor(e.m.store, c.interval)
		if err := e.m.store.BeginLaunch(ctx, store.Launch{ID: "b", WorkspaceID: "w1", MembershipID: e.memID,
			TenantID: e.tenantID, StartedAt: e.now.Add(-3 * time.Hour).Unix(), Override: store.SizeOverrideBoost}); err != nil {
			t.Fatal(err)
		}
		if err := e.m.store.TouchLaunch(ctx, "b", e.now.Unix()); err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		ten, _ := e.m.store.GetTenant(ctx, e.tenantID)
		newWorkspaceAPI(e.m, false).writeTenantBoost(w, ctx, ten, parseLimits(ten.Limits))
		if want := fmt.Sprintf(`"budget_counted":%t`, c.counted); !strings.Contains(w.Body.String(), want) {
			t.Fatalf("interval %v: GET %s, want %s", c.interval, w.Body.String(), want)
		}
		b := e.m.sizingFor(ctx, &resolved{ws: e.ws(), ident: e.ident}).Boost.Budget
		if b.Counted != c.counted || b.Spent != c.counted {
			t.Fatalf("interval %v: budget %+v", c.interval, b)
		}
	}
}

// TestTenantBoostShowsTheOverrun: the tenant's Boost card reads this month's boosted hours of
// every member who has any from the same launch records the refusal is decided on, says who is
// past the budget and by how much, and shows nothing (not a 0) when the hours cannot be told. It
// goes through the GET and PUT handlers, so the gate and the PUT's own answer are covered too.
func TestTenantBoostShowsTheOverrun(t *testing.T) {
	ctx := context.Background()
	lim := tenantLimits{BoostEnabled: true, BoostMonthlyHours: 5}
	e := newBoostEnv(t, lim)
	e.m.boostHours = boostHoursFor(e.m.store, 5*time.Minute)
	st := e.m.store
	other, _ := st.UpsertIdentity(ctx, "b@example.com", "b", "")
	otherMem, _ := st.EnsureMembership(ctx, other.ID, e.tenantID, "member")
	elsewhere, _ := st.CreateTenant(ctx, "elsewhere", "Elsewhere")
	outsider, _ := st.EnsureMembership(ctx, other.ID, elsewhere.ID, "member")
	boss, _ := st.UpsertIdentity(ctx, "boss@example.com", "boss-example-com", "")
	if _, err := st.EnsureMembership(ctx, boss.ID, e.tenantID, "tenant_admin"); err != nil {
		t.Fatal(err)
	}
	stranger, _ := st.UpsertIdentity(ctx, "boss@elsewhere.example", "boss-elsewhere-example", "")
	if _, err := st.EnsureMembership(ctx, stranger.ID, elsewhere.ID, "tenant_admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertIdentity(ctx, "root@example.com", "root-example-com", "super_admin"); err != nil {
		t.Fatal(err)
	}
	launch := func(id, membershipID, tenantID string, from, to time.Duration) {
		t.Helper()
		if err := st.BeginLaunch(ctx, store.Launch{ID: id, WorkspaceID: "w-" + membershipID, MembershipID: membershipID,
			TenantID: tenantID, StartedAt: e.now.Add(from).Unix(), Override: store.SizeOverrideBoost}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := st.EndOpenLaunch(ctx, membershipID, e.now.Add(to).Unix()); err != nil {
			t.Fatal(err)
		}
	}
	// a: 6.5 hours, past the budget by 1.5 because a running boosted box is never stopped.
	launch("a1", e.memID, e.tenantID, -20*time.Hour, -13*time.Hour-30*time.Minute)
	// b: 2 hours, and last month's hours do not count.
	launch("b0", otherMem.ID, e.tenantID, -40*24*time.Hour, -39*24*time.Hour)
	launch("b1", otherMem.ID, e.tenantID, -5*time.Hour, -3*time.Hour)
	// Another tenant's member is never on this tenant's card.
	launch("x1", outsider.ID, elsewhere.ID, -9*time.Hour, -1*time.Hour)

	api := newWorkspaceAPI(e.m, false)
	do := func(email, method, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "/api/admin/tenants/acme/boost", strings.NewReader(body))
		r.Header.Set("X-Forwarded-Email", email)
		r.SetPathValue("slug", "acme")
		w := httptest.NewRecorder()
		if method == http.MethodGet {
			api.tenantBoost(w, r)
		} else {
			api.setTenantBoost(w, r)
		}
		return w
	}
	decode := func(w *httptest.ResponseRecorder) tenantBoostWire {
		t.Helper()
		if w.Code != http.StatusOK {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		var out tenantBoostWire
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	get := func() tenantBoostWire { return decode(do("boss@example.com", http.MethodGet, "")) }

	got := get()
	want := []tenantBoostUsage{
		{UserKey: "a", Email: "a@example.com", UsedHours: 6.5, Spent: true, OverHours: 1.5},
		{UserKey: "b", Email: "b@example.com", UsedHours: 2},
	}
	if got.Month != "2026-11" || !reflect.DeepEqual(got.Members, want) {
		t.Fatalf("month %q members %+v, want 2026-11 %+v", got.Month, got.Members, want)
	}
	// The card and the member's own refusal agree.
	for i, id := range []string{e.memID, otherMem.ID} {
		if b := e.m.boostBudgetOf(ctx, id, lim); b.UsedHours != want[i].UsedHours || b.Spent != want[i].Spent {
			t.Fatalf("member %d: budget %+v, card %+v", i, b, want[i])
		}
	}
	// A super_admin sees the tenant it asked for; another tenant's admin sees nothing of it.
	if g := decode(do("root@example.com", http.MethodGet, "")); !reflect.DeepEqual(g.Members, want) {
		t.Fatalf("super_admin: %+v", g.Members)
	}
	if w := do("boss@elsewhere.example", http.MethodGet, ""); w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "a@example.com") {
		t.Fatalf("another tenant's admin: %d %s", w.Code, w.Body.String())
	}

	// The PUT answers with the budget it just saved: exactly at it is spent, with nothing over.
	g := decode(do("boss@example.com", http.MethodPut, `{"enabled":true,"monthly_hours":2}`))
	if g.MonthlyHours != 2 || len(g.Members) != 2 || !g.Members[1].Spent || g.Members[1].OverHours != 0 || g.Members[0].OverHours != 4.5 {
		t.Fatalf("after the PUT: %+v", g)
	}
	// No budget: the hours are shown and nobody is over.
	g = decode(do("boss@example.com", http.MethodPut, `{"enabled":true,"monthly_hours":0}`))
	if len(g.Members) != 2 || g.Members[0].Spent || g.Members[0].OverHours != 0 || g.Members[0].UsedHours != 6.5 {
		t.Fatalf("no budget: %+v", g.Members)
	}
	// Switched off, this month's hours are still told.
	if g := decode(do("boss@example.com", http.MethodPut, `{"enabled":false}`)); len(g.Members) != 2 {
		t.Fatalf("boost off: %+v", g)
	}

	// A new month: counted, and nobody has boosted yet.
	e.now = time.Date(2026, 12, 1, 0, 30, 0, 0, time.UTC)
	if g := get(); g.Month != "2026-12" || len(g.Members) != 0 {
		t.Fatalf("a new month: %+v", g)
	}

	// Hours that cannot be told are not on the wire at all: no key, not an empty one.
	for name, c := range map[string]boostHoursCounter{
		"no counter":  nil,
		"not counted": countingBoost{members: []memberBoostHours{{UserKey: "a", Hours: 9}}},
		"read error":  countingBoost{counted: true, members: []memberBoostHours{{UserKey: "a", Hours: 9}}, err: errors.New("db down")},
	} {
		e.m.boostHours = c
		w := do("boss@example.com", http.MethodGet, "")
		if body := w.Body.String(); w.Code != 200 || strings.Contains(body, `"month"`) || strings.Contains(body, `"members"`) {
			t.Fatalf("%s: %d %s", name, w.Code, body)
		}
	}
}
