package main

import (
	"context"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// P3-9 idle-stop (scale-to-zero). A single background goroutine drives a
// two-tier reclaim so an OOM-prone single host (see host-oom-fleet-risk) sheds
// RAM as workspaces go cold:
//
//	Tier 1 — session auto-stop: an idle claude session (jsonl-durable, so
//	  resumable) is halted into 停止中 once it has sat idle past the tenant's
//	  session_idle_timeout. This frees the heavy claude process while the
//	  container keeps running. Shells are NOT halted here (halt = kill and a
//	  shell's live process/jobs are not durable) — they ride tier 2.
//
//	Tier 2 — workspace stop: a workspace with no live activity (no open
//	  terminal/preview connection, no working/question session, no recent
//	  request) past ws_idle_timeout is docker-stopped, reclaiming the rest.
//
// Timeouts are per-tenant (tenantLimits, super_admin-editable) with a
// deployment default from env; "0" disables idle-stop for that tenant.

// connRegistry tracks the live signals the reaper needs but the DB doesn't
// carry: which workspaces have an open long-lived connection (someone is
// watching), which sessions have an attached terminal, and when each workspace
// was last touched by any request. All in-memory (resets on CP restart, which
// the reaper treats as a fresh grace window via bootTime).
type connRegistry struct {
	mu       sync.Mutex
	wsConns  map[string]int            // workspaceID -> open long-lived conns (terminal/preview)
	attached map[string]map[string]int // workspaceID -> session name -> attached terminals
	lastSeen map[string]time.Time      // workspaceID -> last request activity
}

func newConnRegistry() *connRegistry {
	return &connRegistry{
		wsConns:  map[string]int{},
		attached: map[string]map[string]int{},
		lastSeen: map[string]time.Time{},
	}
}

// touch records request activity against a workspace (called from every CP
// ingress). Cheap in-memory stamp — no DB write on the hot path.
func (r *connRegistry) touch(wsID string) {
	if r == nil || wsID == "" {
		return
	}
	r.mu.Lock()
	r.lastSeen[wsID] = time.Now()
	r.mu.Unlock()
}

// addConn/doneConn bracket a long-lived connection. session may be "" (preview /
// a fresh shell with no session name); a non-empty session also marks
// that specific session as terminal-attached so tier 1 won't halt it.
func (r *connRegistry) addConn(wsID, session string) {
	if r == nil || wsID == "" {
		return
	}
	r.mu.Lock()
	r.wsConns[wsID]++
	r.lastSeen[wsID] = time.Now()
	if session != "" {
		if r.attached[wsID] == nil {
			r.attached[wsID] = map[string]int{}
		}
		r.attached[wsID][session]++
	}
	r.mu.Unlock()
}

func (r *connRegistry) doneConn(wsID, session string) {
	if r == nil || wsID == "" {
		return
	}
	r.mu.Lock()
	if r.wsConns[wsID] > 0 {
		r.wsConns[wsID]--
	}
	if r.wsConns[wsID] == 0 {
		delete(r.wsConns, wsID)
	}
	r.lastSeen[wsID] = time.Now() // a disconnect is itself recent activity
	if session != "" && r.attached[wsID] != nil {
		if r.attached[wsID][session] > 0 {
			r.attached[wsID][session]--
		}
		if r.attached[wsID][session] == 0 {
			delete(r.attached[wsID], session)
		}
		if len(r.attached[wsID]) == 0 {
			delete(r.attached, wsID)
		}
	}
	r.mu.Unlock()
}

// snapshot returns (open conns, last-seen, ok) for a workspace.
func (r *connRegistry) snapshot(wsID string) (conns int, lastSeen time.Time, ok bool) {
	if r == nil {
		return 0, time.Time{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	ls, has := r.lastSeen[wsID]
	return r.wsConns[wsID], ls, has
}

func (r *connRegistry) isAttached(wsID, session string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.attached[wsID] != nil && r.attached[wsID][session] > 0
}

// reaper owns the sweep loop. sessionDef/wsDef are the deployment-default
// timeouts; per-tenant overrides come from tenantLimits.
type reaper struct {
	mgr        *manager
	interval   time.Duration
	sessionDef time.Duration
	wsDef      time.Duration
	bootTime   time.Time

	// idleSince tracks, per live claude session, when it was first observed
	// idle-and-unattached. Reset when it goes busy/attached/away. Reaper is the
	// only writer (single goroutine) so no lock needed.
	idleSince map[string]time.Time // workspaceID|session -> first idle

	// ownerEmail caches a workspace membership's owning identity email (immutable),
	// so the per-sweep "is the owner locked out?" check needs no repeated DB joins.
	ownerEmail map[string]string // membershipID -> lowercased email
	// spared remembers which workspaces are currently skipped for an expired owner
	// login, so the "sparing" log line is emitted once per episode, not every sweep.
	spared map[string]bool // workspaceID -> currently spared
}

func newReaper(mgr *manager, interval, sessionDef, wsDef time.Duration) *reaper {
	return &reaper{
		mgr:        mgr,
		interval:   interval,
		sessionDef: sessionDef,
		wsDef:      wsDef,
		bootTime:   time.Now(),
		idleSince:  map[string]time.Time{},
		ownerEmail: map[string]string{},
		spared:     map[string]bool{},
	}
}

// ownerLoginExpired reports whether the workspace's owning identity's login has
// expired — the reaper then spares the workspace (a locked-out owner can't
// re-attach to keep a session warm, so reaping would destroy live work). Unknown
// owners (unresolvable membership, or never-observed cookie) are treated as NOT
// expired, so the reaper falls back to normal idle-stop rather than protecting a
// workspace it can't reason about. The membership->email map is cached (immutable).
func (rp *reaper) ownerLoginExpired(ctx context.Context, ws Workspace, now time.Time) bool {
	email, ok := rp.ownerEmail[ws.MembershipID]
	if !ok {
		if ws.MembershipID == "" {
			return false
		}
		idID, found, err := rp.mgr.store.IdentityIDForMembership(ctx, ws.MembershipID)
		if err != nil || !found {
			return false // transient/unknown — don't cache, retry next sweep
		}
		idn, found, err := rp.mgr.store.GetIdentityByID(ctx, idID)
		if err != nil || !found {
			return false
		}
		email = strings.ToLower(idn.Email)
		rp.ownerEmail[ws.MembershipID] = email
	}
	return rp.mgr.authReg.loginExpired(email, now.Unix())
}

func (rp *reaper) run(ctx context.Context) {
	log.Printf("idle-stop reaper: interval=%s session_default=%s ws_default=%s", rp.interval, rp.sessionDef, rp.wsDef)
	t := time.NewTicker(rp.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			rp.sweep(ctx)
		}
	}
}

// sweep runs one pass over every running workspace, applying both tiers.
func (rp *reaper) sweep(ctx context.Context) {
	tenants, err := rp.mgr.store.ListTenants(ctx)
	if err != nil {
		log.Printf("idle-stop: list tenants: %v", err)
		return
	}
	live := map[string]bool{} // sessions seen this pass, to prune idleSince
	for _, t := range tenants {
		lim := parseLimits(t.Limits)
		sessTO, sessOn := idleTimeout(lim.SessionIdleTimeout, rp.sessionDef)
		wsTO, wsOn := idleTimeout(lim.WSIdleTimeout, rp.wsDef)
		if !sessOn && !wsOn {
			continue // idle-stop fully disabled for this tenant
		}
		wss, err := rp.mgr.store.ListWorkspaces(ctx, t.ID)
		if err != nil {
			log.Printf("idle-stop: list workspaces (%s): %v", t.Slug, err)
			continue
		}
		for _, ws := range wss {
			rp.sweepWorkspace(ctx, ws, sessTO, sessOn, wsTO, wsOn, live)
		}
	}
	// Drop trackers for sessions that no longer exist so a resumed session
	// starts its idle clock fresh.
	for k := range rp.idleSince {
		if !live[k] {
			delete(rp.idleSince, k)
		}
	}
}

func (rp *reaper) sweepWorkspace(ctx context.Context, ws Workspace, sessTO time.Duration, sessOn bool, wsTO time.Duration, wsOn bool, live map[string]bool) {
	rt := rp.mgr.runtimeFor(ws, "") // secretKey unused for read/halt calls
	// Only a "running" workspace is swept. "starting" (ECS cold pull) is deliberately
	// left alone — idle-stopping a workspace that is still converging would cancel a
	// legitimate launch; its idle clock starts once it actually runs.
	if rt.State(ctx) != "running" {
		return
	}
	now := time.Now()

	// Spare a workspace whose owner's login has expired: they are locked out and
	// cannot re-attach a terminal to keep a session "watched", so neither tier-1
	// (halt an idle session) nor tier-2 (stop the workspace) may fire — that would
	// destroy live work the user has no way to defend. Protection lifts on re-auth
	// (a fresh cookie flips loginExpired back to false). The trade-off is deliberate
	// (unbounded): an abandoned expired workspace keeps holding RAM until re-login.
	if rp.ownerLoginExpired(ctx, ws, now) {
		if !rp.spared[ws.ID] {
			rp.spared[ws.ID] = true
			log.Printf("idle-stop: sparing %s — owner login expired (locked out; not reaped until re-login)", ws.ContainerName)
		}
		return
	}
	if rp.spared[ws.ID] {
		delete(rp.spared, ws.ID)
		log.Printf("idle-stop: resuming normal idle-stop for %s — owner re-authenticated", ws.ContainerName)
	}

	// Ask the Agent for the live session list once; drives both tiers.
	sessions, err := rp.mgr.agentSessions(ctx, rt)
	if err != nil {
		// Agent unreachable (starting/unhealthy) — leave it be this pass.
		return
	}
	busy := false // any session working/question => workspace is active
	for _, s := range sessions {
		if s.Alive && (s.State == "working" || s.State == "question") {
			busy = true
		}
		if !sessOn || !s.Alive || s.Kind != "claude" || s.State != "idle" {
			continue
		}
		key := ws.ID + "|" + s.Name
		live[key] = true
		if rp.mgr.conns.isAttached(ws.ID, s.Name) {
			delete(rp.idleSince, key) // someone is watching it
			continue
		}
		since, ok := rp.idleSince[key]
		if !ok {
			rp.idleSince[key] = now
			continue
		}
		if now.Sub(since) >= sessTO {
			rp.haltSession(ctx, rt, ws, s.Name)
			delete(rp.idleSince, key)
		}
	}

	if !wsOn {
		return
	}
	// Tier 2: stop the whole workspace once it is fully cold.
	conns, lastSeen, seen := rp.mgr.conns.snapshot(ws.ID)
	if conns > 0 || busy {
		return // being watched or actively working
	}
	base := rp.idleBase(seen, lastSeen, ws.LastActiveAt)
	if now.Sub(base) >= wsTO {
		rp.stopWorkspace(ctx, rt, ws)
	}
}

// idleBase is the tier-2 idle clock's start: the LATEST of the three activity
// signals — the reaper boot time (a CP restart grants a fresh grace window
// instead of reaping everything that looks stale), the in-memory last request
// (proxy/preview/chat traffic), and the DB last_active_at (bumped on every
// explicit start/stop). All three are considered unconditionally: an earlier
// version only consulted the DB when there was NO in-memory record, so a stale
// in-memory lastSeen (from an old terminal, hours ago) masked the fresh
// last_active_at written by a just-issued Start — and the reaper stopped the
// workspace within one sweep of it coming up (a manual start looked "cold").
func (rp *reaper) idleBase(seen bool, lastSeen time.Time, dbLastActive string) time.Time {
	base := rp.bootTime
	if seen && lastSeen.After(base) {
		base = lastSeen
	}
	if dbTS, err := time.Parse(time.RFC3339, dbLastActive); err == nil && dbTS.After(base) {
		base = dbTS
	}
	return base
}

// haltSession halts one idle claude session (Agent POST /sessions/{name}/halt).
func (rp *reaper) haltSession(ctx context.Context, rt Runtime, ws Workspace, name string) {
	req, _ := http.NewRequestWithContext(ctx, "POST", rt.Endpoint()+"/sessions/"+name+"/halt", nil)
	if rt.Token() != "" {
		req.Header.Set("Authorization", "Bearer "+rt.Token())
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("idle-stop: halt %s/%s: %v", ws.ContainerName, name, err)
		return
	}
	_ = resp.Body.Close()
	log.Printf("idle-stop: halted idle claude session %s in %s (tenant %s)", name, ws.ContainerName, ws.TenantID)
}

// stopWorkspace docker-stops a cold workspace and mirrors state to the DB.
func (rp *reaper) stopWorkspace(ctx context.Context, rt Runtime, ws Workspace) {
	if err := rt.Stop(ctx); err != nil {
		log.Printf("idle-stop: stop %s: %v", ws.ContainerName, err)
		return
	}
	if err := rp.mgr.store.SetWorkspaceState(ctx, ws.ID, "stopped"); err != nil {
		log.Printf("idle-stop: mark stopped %s: %v", ws.ContainerName, err)
	}
	log.Printf("idle-stop: stopped cold workspace %s (tenant %s)", ws.ContainerName, ws.TenantID)
}
