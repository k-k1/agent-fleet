package main

import (
	"context"
	"log"
	"net/http"
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
	wsConns  map[string]int            // workspaceID -> open long-lived conns (terminal/preview/ocweb)
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
// ocweb / a fresh shell with no session name); a non-empty session also marks
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
}

func newReaper(mgr *manager, interval, sessionDef, wsDef time.Duration) *reaper {
	return &reaper{
		mgr:        mgr,
		interval:   interval,
		sessionDef: sessionDef,
		wsDef:      wsDef,
		bootTime:   time.Now(),
		idleSince:  map[string]time.Time{},
	}
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
	if rt.state(ctx) != "running" {
		return
	}
	now := time.Now()

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
	// Idle base = latest of: in-memory last request, DB last_active_at, and the
	// reaper boot time (so a CP restart grants a fresh grace window instead of
	// stopping everything that looks stale).
	base := rp.bootTime
	if seen && lastSeen.After(base) {
		base = lastSeen
	}
	if !seen {
		if dbTS, err := time.Parse(time.RFC3339, ws.LastActiveAt); err == nil && dbTS.After(base) {
			base = dbTS
		}
	}
	if now.Sub(base) >= wsTO {
		rp.stopWorkspace(ctx, rt, ws)
	}
}

// haltSession halts one idle claude session (Agent POST /sessions/{name}/halt).
func (rp *reaper) haltSession(ctx context.Context, rt *dockerRuntime, ws Workspace, name string) {
	req, _ := http.NewRequestWithContext(ctx, "POST", rt.agentBase()+"/sessions/"+name+"/halt", nil)
	if rt.token != "" {
		req.Header.Set("Authorization", "Bearer "+rt.token)
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
func (rp *reaper) stopWorkspace(ctx context.Context, rt *dockerRuntime, ws Workspace) {
	if err := rt.stop(ctx); err != nil {
		log.Printf("idle-stop: stop %s: %v", ws.ContainerName, err)
		return
	}
	if err := rp.mgr.store.SetWorkspaceState(ctx, ws.ID, "stopped"); err != nil {
		log.Printf("idle-stop: mark stopped %s: %v", ws.ContainerName, err)
	}
	log.Printf("idle-stop: stopped cold workspace %s (tenant %s)", ws.ContainerName, ws.TenantID)
}
