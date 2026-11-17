// workspace_boost.go — the member's Boost (ADR 0139 decision 5): one rung up from the box the
// next start would use, in the same class, for a limited time, plus the sizing answer the Console
// reads instead of recomputing the box itself, and the tenant admin's policy for it.
//
// A boost is stored apart from the member's stored size (workspace_size_override) and applied by
// overlaySizeOverrides at the NEXT start only. Nothing here restarts or shrinks a running
// workspace.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

const (
	boostDefaultHours = 4
	boostMaxHours     = 24
)

// boostHoursCounter counts the hours a member actually RAN on a box a boost put them on,
// including hours after the boost expired while the workspace kept running (ADR 0139 decision
// 5). The source of truth is the CP's launch records (#4333), which are not on main yet; until
// they are, no implementation is wired and the budget is not enforced.
//
// counted=false means "this deployment cannot count yet", and the caller must neither block
// on it nor show a number.
type boostHoursCounter interface {
	BoostedRunningHours(ctx context.Context, membershipID string, from, to time.Time) (hours float64, counted bool, err error)
}

// boostBudget is a member's month so far against the tenant's budget.
type boostBudget struct {
	MonthlyHours int     `json:"monthly_hours"`
	Counted      bool    `json:"counted"`
	UsedHours    float64 `json:"used_hours"`
	Spent        bool    `json:"spent"`
}

func (m *manager) boostBudgetOf(ctx context.Context, membershipID string, lim tenantLimits) boostBudget {
	b := boostBudget{MonthlyHours: lim.BoostMonthlyHours}
	if m.boostHours == nil || lim.BoostMonthlyHours <= 0 {
		return b
	}
	now := boostNow().UTC()
	from := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	h, counted, err := m.boostHours.BoostedRunningHours(ctx, membershipID, from, now)
	if err != nil || !counted {
		return b // an unreadable count never locks a member out
	}
	b.Counted, b.UsedHours = true, h
	b.Spent = h >= float64(lim.BoostMonthlyHours)
	return b
}

// sizingBox is one box on the sizing wire.
type sizingBox struct {
	Class        string `json:"class,omitempty"`
	ClassLabel   string `json:"class_label,omitempty"`
	InstanceType string `json:"instance_type,omitempty"`
	Arch         string `json:"arch,omitempty"`
	VCPU         int    `json:"vcpu,omitempty"`
	MemMiB       int64  `json:"mem_mib"`
	UsableMemMiB int64  `json:"usable_mem_mib,omitempty"`
}

func boxOf(p runtime.WorkspaceSizing, class string, s runtime.WorkspaceSlot) *sizingBox {
	b := &sizingBox{Class: class, InstanceType: s.InstanceType, VCPU: s.VCPU, MemMiB: s.MemMiB, UsableMemMiB: s.UsableMemMiB}
	for _, c := range p.SlotClasses {
		if c.ID == class {
			b.ClassLabel, b.Arch = c.Label, c.Arch
		}
	}
	return b
}

type sizingNextStart struct {
	Box *sizingBox `json:"box"`
	// Source is "stored", "boost" or (later) "auto". Until is when the override ends.
	Source string `json:"source"`
	Until  string `json:"until,omitempty"`
	// Note says why an override that exists was not applied.
	Note string `json:"note,omitempty"`
}

type sizingBoost struct {
	Enabled bool `json:"enabled"`
	Active  bool `json:"active"`
	// Available: pressing Boost would do something. Otherwise Reason says why not:
	// "disabled" | "top_rung" | "limit" | "budget_spent".
	Available    bool        `json:"available"`
	Reason       string      `json:"reason,omitempty"`
	Target       *sizingBox  `json:"target,omitempty"`
	DefaultHours int         `json:"default_hours"`
	MaxHours     int         `json:"max_hours"`
	Budget       boostBudget `json:"budget"`
}

// workspaceSizingWire is the response of GET /api/workspace/sizing: the stored, next-start and
// running boxes, and where an override came from. Supported is false where there are no rungs.
type workspaceSizingWire struct {
	Supported bool             `json:"supported"`
	Runtime   string           `json:"runtime"`
	Stored    *sizingBox       `json:"stored,omitempty"`
	NextStart *sizingNextStart `json:"next_start,omitempty"`
	Running   *sizingBox       `json:"running,omitempty"`
	// RestartOffered: the workspace is running on a box SMALLER than the next start's, so a
	// restart would land on a bigger one. A restart ends the sessions running in it, which is
	// why it is an offer and never done on its own.
	RestartOffered bool        `json:"restart_offered"`
	Boost          sizingBoost `json:"boost"`
}

// boostPlan is what pressing Boost would take: the rung one above the next-start box's base.
func (m *manager) boostPlan(ns nextStart, lim tenantLimits) (target *runtime.WorkspaceSlot, reason string) {
	switch {
	case len(ns.baseLadder) == 0:
		return nil, "unsupported"
	case !lim.BoostEnabled:
		return nil, "disabled"
	case ns.baseIdx+1 >= len(ns.baseLadder):
		return nil, "top_rung"
	}
	t := ns.baseLadder[ns.baseIdx+1]
	if !m.boostTargetOK(lim, t) {
		return nil, "limit"
	}
	return &t, ""
}

// runningBox finds the declared rung the running workspace is on, from what the Agent measured
// (instance type first, the cgroup memory cap as the fallback). nil when it cannot be told:
// the caller then offers no restart rather than guessing.
func (m *manager) runningBox(ctx context.Context, res *resolved, p runtime.WorkspaceSizing, preferClass string) *sizingBox {
	if res.rt == nil || res.rt.State(ctx) != "running" {
		return nil
	}
	meas, err := m.agentMachine(ctx, res.rt)
	if err != nil || meas == nil {
		return nil
	}
	type cand struct {
		class string
		slot  runtime.WorkspaceSlot
	}
	var all []cand
	if len(p.SlotClasses) == 0 {
		for _, s := range p.Slots {
			all = append(all, cand{"", s})
		}
	}
	for _, c := range p.SlotClasses {
		for _, s := range c.Slots {
			all = append(all, cand{c.ID, s})
		}
	}
	pick := func(match func(runtime.WorkspaceSlot) bool) *sizingBox {
		var found *cand
		for i := range all {
			if match(all[i].slot) && (found == nil || all[i].class == preferClass && found.class != preferClass) {
				found = &all[i]
			}
		}
		if found == nil {
			return nil
		}
		return boxOf(p, found.class, found.slot)
	}
	if meas.InstanceType != "" {
		if b := pick(func(s runtime.WorkspaceSlot) bool { return s.InstanceType == meas.InstanceType }); b != nil {
			return b
		}
	}
	if meas.MemMax != nil && *meas.MemMax > 0 {
		cap := int64(*meas.MemMax) / mib
		return pick(func(s runtime.WorkspaceSlot) bool {
			return s.MemMiB == cap || (s.UsableMemMiB > 0 && s.UsableMemMiB == cap)
		})
	}
	return nil
}

func (m *manager) sizingFor(ctx context.Context, res *resolved) workspaceSizingWire {
	p := m.workspaceSizing()
	out := workspaceSizingWire{Runtime: p.Runtime}
	ns := m.resolveNextStart(ctx, res.ws)
	if res.ws.MemBytes != ns.MemBytes || res.ws.SlotClass != ns.SlotClass {
		// The memoized runtime was built before a boost ended (or an admin change landed): drop
		// it so its own "declared" box, which the Machine screen reads, is rebuilt.
		m.evictMembershipCache(res.ws.MembershipID)
	}
	if p.MemMeaning != runtime.MemMeaningSlot || len(ns.baseLadder) == 0 {
		out.Boost = sizingBoost{DefaultHours: boostDefaultHours, MaxHours: boostMaxHours, Available: false, Reason: "unsupported"}
		return out
	}
	out.Supported = true
	lim := m.tenantLimitsOf(ctx, res.ws.TenantID)
	out.Stored = boxOf(p, ns.baseClass, ns.baseLadder[ns.baseIdx])
	nextIdx := rungIndex(ns.baseLadder, ns.MemBytes)
	out.NextStart = &sizingNextStart{
		Box:    boxOf(p, ns.SlotClass, ns.baseLadder[nextIdx]),
		Source: ns.Source, Until: ns.Until, Note: ns.Note,
	}
	out.Running = m.runningBox(ctx, res, p, ns.SlotClass)
	out.RestartOffered = out.Running != nil && out.NextStart.Box.MemMiB > out.Running.MemMiB

	b := sizingBoost{Enabled: lim.BoostEnabled, Active: ns.Source == store.SizeOverrideBoost,
		DefaultHours: boostDefaultHours, MaxHours: boostMaxHours,
		Budget: m.boostBudgetOf(ctx, res.ws.MembershipID, lim)}
	target, reason := m.boostPlan(ns, lim)
	switch {
	case target == nil:
		b.Reason = reason
	case b.Budget.Spent:
		b.Reason = "budget_spent"
		b.Target = boxOf(p, ns.baseClass, *target)
	default:
		b.Available = true
		b.Target = boxOf(p, ns.baseClass, *target)
	}
	if b.Active {
		b.Target = out.NextStart.Box // an active boost shows the box it gave, not the next rung
	}
	out.Boost = b
	return out
}

// sizing (GET /api/workspace/sizing) — the caller's OWN workspace only.
func (a workspaceAPI) sizing(w http.ResponseWriter, r *http.Request, res *resolved) {
	writeJSON(w, http.StatusOK, a.mgr.sizingFor(r.Context(), res))
}

// boost (POST /api/workspace/boost {hours}) grants, or extends, the caller's own boost.
func (a workspaceAPI) boost(w http.ResponseWriter, r *http.Request, res *resolved) {
	ctx := r.Context()
	var body struct {
		Hours int `json:"hours"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid json"})
			return
		}
	}
	if body.Hours == 0 {
		body.Hours = boostDefaultHours
	}
	if body.Hours < 1 || body.Hours > boostMaxHours {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_hours", fmt.Sprintf("hours must be between 1 and %d", boostMaxHours)})
		return
	}
	m := a.mgr
	lim := m.tenantLimitsOf(ctx, res.ws.TenantID)
	ns := m.resolveNextStart(ctx, res.ws)
	target, reason := m.boostPlan(ns, lim)
	switch reason {
	case "unsupported":
		writeAPIErr(w, &apiError{http.StatusConflict, "boost_unsupported", "this runtime has no machine sizes to boost between"})
		return
	case "disabled":
		writeAPIErr(w, &apiError{http.StatusForbidden, "boost_disabled", "boost is not enabled for this tenant"})
		return
	case "top_rung":
		writeAPIErr(w, &apiError{http.StatusConflict, "boost_top_rung", "there is no bigger machine size in this class"})
		return
	case "limit":
		writeAPIErr(w, &apiError{http.StatusConflict, "boost_limit", "the next size up is above a limit set for this tenant"})
		return
	}
	if m.boostBudgetOf(ctx, res.ws.MembershipID, lim).Spent {
		writeAPIErr(w, &apiError{http.StatusConflict, "boost_budget_spent", "this month's boost hours are used up"})
		return
	}
	now := boostNow().UTC()
	until := now.Add(time.Duration(body.Hours) * time.Hour)
	ov := store.SizeOverride{MembershipID: res.ws.MembershipID, Source: store.SizeOverrideBoost,
		SlotClass: ns.baseClass, ExpiresAt: until.Format(time.RFC3339)}
	if ns.Source == store.SizeOverrideBoost {
		// Boosting while a boost is live extends it and does not climb further. It keeps the
		// box it has even if the recommender has since moved the base.
		ov.SlotClass, ov.MemMiB = ns.SlotClass, ns.MemBytes/mib
		if cur, perr := time.Parse(time.RFC3339, ns.Until); perr == nil && cur.After(until) {
			ov.ExpiresAt = ns.Until
		}
	} else {
		ov.MemMiB = target.MemMiB
	}
	if err := m.store.PutSizeOverride(ctx, ov); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	m.evictMembershipCache(res.ws.MembershipID)
	_ = m.store.InsertAudit(ctx, store.AuditLog{
		ID: store.NewID(), TenantID: res.ws.TenantID, ActorKind: "user", ActorID: res.ident.ID,
		Action: "workspace.boost", Target: res.ws.MembershipID, At: store.NowTS(),
		Detail: fmt.Sprintf("class=%s mem_mib=%d until=%s", ov.SlotClass, ov.MemMiB, ov.ExpiresAt),
	})
	writeJSON(w, http.StatusOK, m.sizingFor(ctx, res))
}

// cancelBoost (DELETE /api/workspace/boost) ends the caller's boost for the next start. A
// running workspace keeps the box it is on.
func (a workspaceAPI) cancelBoost(w http.ResponseWriter, r *http.Request, res *resolved) {
	ctx := r.Context()
	if err := a.mgr.store.DeleteSizeOverride(ctx, res.ws.MembershipID, store.SizeOverrideBoost); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	a.mgr.evictMembershipCache(res.ws.MembershipID)
	_ = a.mgr.store.InsertAudit(ctx, store.AuditLog{
		ID: store.NewID(), TenantID: res.ws.TenantID, ActorKind: "user", ActorID: res.ident.ID,
		Action: "workspace.boost_cancel", Target: res.ws.MembershipID, At: store.NowTS(),
	})
	writeJSON(w, http.StatusOK, a.mgr.sizingFor(ctx, res))
}

// tenantBoostWire is GET/PUT /api/admin/tenants/{slug}/boost.
type tenantBoostWire struct {
	Tenant       string `json:"tenant"`
	Enabled      bool   `json:"enabled"`
	MaxMemMiB    int64  `json:"max_mem_mib"`
	MonthlyHours int    `json:"monthly_hours"`
	// Editable is false on a runtime with no rungs, where a boost has nothing to move between.
	Editable bool `json:"editable"`
	// BudgetCounted is false while no launch records exist to count boosted hours from: the
	// monthly budget is stored but not enforced.
	BudgetCounted bool `json:"budget_counted"`
}

func (a workspaceAPI) tenantBoost(w http.ResponseWriter, r *http.Request) {
	_, t, ok := a.tenantAdminFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	a.writeTenantBoost(w, r.Context(), t)
}

func (a workspaceAPI) writeTenantBoost(w http.ResponseWriter, ctx context.Context, t store.Tenant) {
	lim := parseLimits(t.Limits)
	if fresh, err := a.mgr.store.GetTenant(ctx, t.ID); err == nil {
		lim = parseLimits(fresh.Limits)
	}
	p := a.mgr.workspaceSizing()
	writeJSON(w, http.StatusOK, tenantBoostWire{
		Tenant: t.Slug, Enabled: lim.BoostEnabled, MaxMemMiB: lim.BoostMaxMemMiB, MonthlyHours: lim.BoostMonthlyHours,
		Editable:      p.MemMeaning == runtime.MemMeaningSlot,
		BudgetCounted: a.mgr.boostHours != nil,
	})
}

// setTenantBoost (PUT /api/admin/tenants/{slug}/boost) — tenant_admin of THAT tenant only
// (tenantAdminFor resolves the tenant from the path and checks the caller against it).
func (a workspaceAPI) setTenantBoost(w http.ResponseWriter, r *http.Request) {
	caller, t, ok := a.tenantAdminFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	var body struct {
		Enabled      bool  `json:"enabled"`
		MaxMemMiB    int64 `json:"max_mem_mib"`
		MonthlyHours int   `json:"monthly_hours"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid json"})
		return
	}
	if body.MonthlyHours < 0 || body.MaxMemMiB < 0 {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_limit", "monthly_hours and max_mem_mib cannot be negative (0 = no limit)"})
		return
	}
	p := a.mgr.workspaceSizing()
	if p.MemMeaning != runtime.MemMeaningSlot {
		writeAPIErr(w, &apiError{http.StatusConflict, "boost_unsupported", "this runtime has no machine sizes to boost between"})
		return
	}
	if body.MaxMemMiB > 0 && !declaresRung(p, body.MaxMemMiB) {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_rung", "max_mem_mib must be the memory of a declared machine size"})
		return
	}
	// Read-modify-write of the whole blob: the quotas live in the same JSON.
	fresh, err := a.mgr.store.GetTenant(r.Context(), t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	lim := parseLimits(fresh.Limits)
	lim.BoostEnabled, lim.BoostMaxMemMiB, lim.BoostMonthlyHours = body.Enabled, body.MaxMemMiB, body.MonthlyHours
	lj, err := json.Marshal(lim)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if err := a.mgr.store.SetTenantLimits(r.Context(), t.ID, string(lj)); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	a.mgr.evictTenantCache(t.ID)
	_ = a.mgr.store.InsertAudit(r.Context(), store.AuditLog{
		ID: store.NewID(), TenantID: t.ID, ActorKind: "user", ActorID: caller.ID,
		Action: "tenant.boost", Target: t.Slug, At: store.NowTS(),
		Detail: fmt.Sprintf("enabled=%t max_mem_mib=%d monthly_hours=%d", body.Enabled, body.MaxMemMiB, body.MonthlyHours),
	})
	a.writeTenantBoost(w, r.Context(), t)
}

// declaresRung: some declared class has a rung of exactly this memory.
func declaresRung(p runtime.WorkspaceSizing, memMiB int64) bool {
	has := func(l []runtime.WorkspaceSlot) bool {
		return slices.ContainsFunc(l, func(s runtime.WorkspaceSlot) bool { return s.MemMiB == memMiB })
	}
	if len(p.SlotClasses) == 0 {
		return has(p.Slots)
	}
	return slices.ContainsFunc(p.SlotClasses, func(c runtime.WorkspaceSlotClass) bool { return has(c.Slots) })
}

// --- the resolver ---
//
// Precedence is boost > auto > the stored choice > the tenant default. The stored choice and the
// tenant default are what resolveWorkspaceSize / resolveSlotClass already answer; this layer sits on
// top and never edits them. Only the boost has a writer today: the right-sizing recommender (#4334)
// will store its result as source "auto", and overlaySizeOverrides is the one place that has to
// learn it. An override that fails a check is NOT applied and says why in Note; it never falls
// through to the deployment default, which could be on another architecture. That strictness is the
// override path's contract; the non-override resolution is unchanged.

// nextStart is the answer to "what will the next start run on, and why".
type nextStart struct {
	MemBytes  int64
	SlotClass string
	// Source is where MemBytes/SlotClass came from: "stored" (the member's stored choice or
	// the tenant default), "boost" or, later, "auto".
	Source string
	// Until is when the deciding override ends (RFC3339 UTC), "" for a stored choice.
	Until string
	// Note is why an override that exists was NOT applied, "" otherwise.
	Note string

	// The base box: what the next start would run on with no override. A boost is one rung
	// above it. Zero value when the runtime has no rungs.
	baseClass  string
	baseLadder []runtime.WorkspaceSlot
	baseIdx    int
}

// boostNow is the clock the boost resolver reads. A test replaces it.
var boostNow = time.Now

// ladderOf returns the ascending rungs of a class; the profile's default ladder when the
// deployment declared no classes.
func ladderOf(p runtime.WorkspaceSizing, classID string) []runtime.WorkspaceSlot {
	if len(p.SlotClasses) == 0 {
		return p.Slots
	}
	for _, c := range p.SlotClasses {
		if c.ID == classID {
			return c.Slots
		}
	}
	return nil
}

// rungIndex is rungFor's rule on the declared ladder: the smallest rung whose memory holds the
// requirement, the top rung when none does, and 0 (the smallest rung) for a requirement of 0.
func rungIndex(ladder []runtime.WorkspaceSlot, memBytes int64) int {
	want := memBytes / mib
	for i, s := range ladder {
		if want <= s.MemMiB {
			return i
		}
	}
	return len(ladder) - 1
}

// classUsable: the class is declared by the deployment and allowed for the tenant, the same two
// tests resolveSlotClass applies to a stored class.
func classUsable(p runtime.WorkspaceSizing, lim tenantLimits, id string) bool {
	if len(p.SlotClasses) == 0 {
		return id == ""
	}
	if !slices.ContainsFunc(p.SlotClasses, func(c runtime.WorkspaceSlotClass) bool { return c.ID == id }) {
		return false
	}
	return len(lim.AllowedSlotClasses) == 0 || slices.Contains(lim.AllowedSlotClasses, id)
}

// boostTargetOK reports whether a rung is within every bound a boost must respect: the
// tenant's and the deployment's memory maximum (resolveWorkspaceSize's clamps) and the highest
// rung the tenant admin allowed for a boost.
func (m *manager) boostTargetOK(lim tenantLimits, s runtime.WorkspaceSlot) bool {
	b := s.MemMiB * mib
	if lim.MaxWorkspaceMem > 0 && b > lim.MaxWorkspaceMem {
		return false
	}
	if m.memMaxBytes > 0 && b > m.memMaxBytes {
		return false
	}
	return lim.BoostMaxMemMiB <= 0 || s.MemMiB <= lim.BoostMaxMemMiB
}

func (m *manager) tenantLimitsOf(ctx context.Context, tenantID string) tenantLimits {
	if t, err := m.store.GetTenant(ctx, tenantID); err == nil {
		return parseLimits(t.Limits)
	}
	return tenantLimits{}
}

// overlaySizeOverrides applies the live, still-valid overrides on top of the box the stored
// choice resolved to (baseMem, baseClass).
func (m *manager) overlaySizeOverrides(ctx context.Context, ws store.Workspace, baseMem int64, baseClass string) nextStart {
	ns := nextStart{MemBytes: baseMem, SlotClass: baseClass, Source: "stored", baseClass: baseClass}
	p := m.workspaceSizing()
	if p.MemMeaning != runtime.MemMeaningSlot || ws.MembershipID == "" {
		return ns
	}
	ns.baseLadder = ladderOf(p, baseClass)
	if len(ns.baseLadder) == 0 {
		return ns
	}
	ns.baseIdx = rungIndex(ns.baseLadder, baseMem)

	// Precedence boost > auto. The auto override (#4334) is applied FIRST by the recommender's
	// own code path when it lands: it moves the base box, and a boost is one rung above that.
	ov, ok, err := m.store.GetSizeOverride(ctx, ws.MembershipID, store.SizeOverrideBoost)
	if err != nil {
		ns.Note = "boost could not be read"
		return ns
	}
	if !ok {
		return ns
	}
	if exp, perr := time.Parse(time.RFC3339, ov.ExpiresAt); perr != nil || !boostNow().Before(exp) {
		return ns // expired (or unreadable, which is treated the same): inert, never an error
	}
	lim := m.tenantLimitsOf(ctx, ws.TenantID)
	switch {
	case !lim.BoostEnabled:
		ns.Note = "boost is switched off for this tenant"
		return ns
	case ov.SlotClass != baseClass:
		ns.Note = "the boost was for another machine class"
		return ns
	case !classUsable(p, lim, ov.SlotClass):
		ns.Note = "the boost's machine class is not available here"
		return ns
	}
	idx := slices.IndexFunc(ns.baseLadder, func(s runtime.WorkspaceSlot) bool { return s.MemMiB == ov.MemMiB })
	switch {
	case idx < 0:
		ns.Note = "the boost's rung is no longer declared"
		return ns
	case idx <= ns.baseIdx:
		ns.Note = "the boost is no bigger than the box the next start already uses"
		return ns
	case !m.boostTargetOK(lim, ns.baseLadder[idx]):
		ns.Note = "the boost's rung is above a memory limit"
		return ns
	}
	ns.MemBytes = ns.baseLadder[idx].MemMiB * mib
	ns.Source = store.SizeOverrideBoost
	ns.Until = ov.ExpiresAt
	return ns
}

// resolveNextStart answers what ws's NEXT start runs on, the same way withResolvedSize does.
func (m *manager) resolveNextStart(ctx context.Context, ws store.Workspace) nextStart {
	mem := m.resolveWorkspaceMemBytes(ctx, ws)
	class, _ := m.resolveSlotClass(ctx, ws)
	return m.overlaySizeOverrides(ctx, ws, mem, class)
}
