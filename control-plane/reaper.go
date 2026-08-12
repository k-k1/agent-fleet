package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/url"
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

const (
	workspacePresenceHeartbeat = 5 * time.Second
	workspacePresenceTTL       = 15 * time.Second
)

var errWorkspaceStopping = errors.New("workspace idle stop is in progress")

func workspaceActivityAPIError(err error) *apiError {
	if errors.Is(err, errWorkspaceStopping) {
		return &apiError{http.StatusConflict, "workspace_stopping", "workspace is stopping; retry after it has stopped"}
	}
	return &apiError{http.StatusServiceUnavailable, "activity_unavailable", "workspace activity could not be recorded"}
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

// touchWorkspace mirrors request activity both locally and into a single
// monotonic DB row. The shared watermark closes the HA blind spot where the
// reaper on CP-B could not see traffic served by CP-A.
func (m *manager) activityLockFor(wsID string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.activityLocks == nil {
		m.activityLocks = map[string]*sync.Mutex{}
	}
	if m.activityLocks[wsID] == nil {
		m.activityLocks[wsID] = &sync.Mutex{}
	}
	return m.activityLocks[wsID]
}

func (m *manager) touchWorkspace(ctx context.Context, wsID string) error {
	m.conns.touch(wsID)
	return m.recordWorkspaceActivity(ctx, wsID, false)
}

func (m *manager) recordWorkspaceActivity(ctx context.Context, wsID string, force bool) error {
	lock := m.activityLockFor(wsID)
	lock.Lock()
	defer lock.Unlock()
	now := time.Now().UTC()
	if !force {
		m.mu.Lock()
		protected := m.activityProtectedUntil[wsID]
		m.mu.Unlock()
		if protected.After(now) {
			return nil
		}
	}
	_, lastSeen, seen := m.conns.snapshot(wsID)
	if !seen {
		lastSeen = time.Now()
	}
	accepted, err := m.store.RecordWorkspaceActivity(ctx, wsID, leaseTS(lastSeen),
		leaseTS(now.Add(workspacePresenceTTL)), leaseTS(now))
	if err != nil {
		return err
	}
	if !accepted {
		return errWorkspaceStopping
	}
	m.mu.Lock()
	if m.activityProtectedUntil == nil {
		m.activityProtectedUntil = map[string]time.Time{}
	}
	m.activityProtectedUntil[wsID] = now.Add(workspacePresenceHeartbeat)
	m.mu.Unlock()
	return nil
}

// trackWorkspaceConnection publishes a renewable cross-replica presence lease
// for a terminal/scheduler keepalive. One goroutine per long-lived connection is
// bounded by the number of actual connections; DB storage remains one row per WS.
func (m *manager) trackWorkspaceConnection(ctx context.Context, wsID, session string) (func(), error) {
	m.conns.addConn(wsID, session)
	if err := m.recordWorkspaceActivity(ctx, wsID, true); err != nil {
		m.conns.doneConn(wsID, session)
		return nil, err
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(workspacePresenceHeartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				hbCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				if err := m.recordWorkspaceActivity(hbCtx, wsID, true); err != nil {
					log.Printf("workspace presence: %s: %v", wsID, err)
				}
				cancel()
			case <-stop:
				return
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(stop)
			<-done
			m.conns.doneConn(wsID, session)
			flushCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = m.recordWorkspaceActivity(flushCtx, wsID, true)
			cancel()
		})
	}, nil
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
	// Only a "running" workspace is swept. "starting" (ECS cold pull) is deliberately
	// left alone — idle-stopping a workspace that is still converging would cancel a
	// legitimate launch; its idle clock starts once it actually runs.
	if rt.State(ctx) != "running" {
		return
	}
	now := time.Now()

	// Ask the Agent for the live session list once; drives both tiers.
	sessions, err := rp.mgr.agentSessions(ctx, rt)
	if err != nil {
		// Agent unreachable (starting/unhealthy) — leave it be this pass.
		return
	}
	// busy = any session working/question => workspace is active.
	//
	// "blocked" (claude の利用上限メニュー、または codex managed の usageLimitExceeded) は
	// 意図的に除外する。question と違い、これは「すぐ答えが返ってくる対話」ではなく人が気づくまで
	// 何日でも続きうる停止で、それでコンテナを起こし続けるのが元のバグの実害そのものだった
	// （進行中 に貼り付いた1セッションが busy を立て続け、tier1・tier2 とも効かないまま
	// 約16時間コンテナが占有された — 2026-07-31 実測）。ターンは既に終わっているので、
	// ここで tier2 に停止させても失われる作業は無く、セッションは resumable のまま残る。
	busy := false
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
		rp.stopWorkspace(ctx, rt, ws, wsTO)
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
	req, _ := http.NewRequestWithContext(ctx, "POST", rt.Endpoint()+"/sessions/"+url.PathEscape(name)+"/halt", nil)
	if rt.Token() != "" {
		req.Header.Set("Authorization", "Bearer "+rt.Token())
	}
	resp, err := agentHTTPClient.Do(req)
	if err != nil {
		log.Printf("idle-stop: halt %s/%s: %v", ws.ContainerName, name, err)
		return
	}
	_ = resp.Body.Close()
	log.Printf("idle-stop: halted idle claude session %s in %s (tenant %s)", name, ws.ContainerName, ws.TenantID)
}

// stopWorkspace stops a cold workspace through the same distributed lifecycle
// fence as explicit/admin stops. The idle reaper runs independently in every CP
// replica, so a direct Runtime.Stop could otherwise cross an approved shared
// operation or another holder's recreate/clean/start.
func (rp *reaper) stopWorkspace(ctx context.Context, rt Runtime, ws Workspace, wsTO time.Duration) {
	lock := rp.mgr.startLockFor(ws.ID)
	lock.Lock()
	defer lock.Unlock()
	lease, err := acquireWorkspaceLifecycleLease(ctx, rp.mgr.store, ws.MembershipID)
	if err != nil {
		log.Printf("idle-stop: lifecycle busy %s: %v", ws.ContainerName, err)
		return
	}
	defer lease.Close()
	releaseFence, err := acquireRuntimeOperationFence(lease.Context(), rt)
	if err != nil {
		log.Printf("idle-stop: runtime fence %s: %v", ws.ContainerName, err)
		return
	}
	defer releaseFence()
	if err := lease.checkpoint(ctx); err != nil {
		log.Printf("idle-stop: lifecycle lost %s: %v", ws.ContainerName, err)
		return
	}
	// A start/recreate may have won the local lock while this sweep was waiting.
	// Never turn its non-running transitional state into a fresh Stop.
	if rt.State(lease.Context()) != "running" {
		return
	}
	// The initial idle decision was made before potentially waiting on three
	// fences. Re-read every activity signal while we own them; otherwise a proxy
	// reconnect or a session becoming busy during that wait would be stopped by a
	// stale sweep decision.
	freshWS, ok, err := rp.mgr.store.GetWorkspaceByMembership(lease.Context(), ws.MembershipID)
	if err != nil || !ok {
		log.Printf("idle-stop: refresh workspace %s: found=%v err=%v", ws.ContainerName, ok, err)
		return
	}
	sessions, err := rp.mgr.agentSessions(lease.Context(), rt)
	if err != nil {
		log.Printf("idle-stop: refresh sessions %s: %v", ws.ContainerName, err)
		return
	}
	for _, s := range sessions {
		if s.Alive && (s.State == "working" || s.State == "question") {
			return
		}
	}
	conns, lastSeen, seen := rp.mgr.conns.snapshot(ws.ID)
	if conns > 0 || time.Since(rp.idleBase(seen, lastSeen, freshWS.LastActiveAt)) < wsTO {
		return
	}
	checkNow := time.Now().UTC()
	recent, err := rp.mgr.store.WorkspaceHasRecentActivity(lease.Context(), ws.ID,
		leaseTS(checkNow.Add(-wsTO)), leaseTS(checkNow))
	if err != nil {
		log.Printf("idle-stop: shared activity %s: %v", ws.ContainerName, err)
		return
	}
	if recent {
		return
	}
	claimed, err := rp.mgr.store.ClaimWorkspaceIdleStop(lease.Context(), ws.ID, ws.MembershipID,
		lease.token, leaseTS(checkNow.Add(-wsTO)), leaseTS(checkNow))
	if err != nil || !claimed {
		if err != nil {
			log.Printf("idle-stop: claim %s: %v", ws.ContainerName, err)
		}
		return
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = rp.mgr.store.ReleaseWorkspaceIdleStop(releaseCtx, ws.ID, lease.token)
		cancel()
	}()
	if err := rt.Stop(lease.Context()); err != nil {
		log.Printf("idle-stop: stop %s: %v", ws.ContainerName, err)
		return
	}
	if err := lease.checkpoint(ctx); err != nil {
		log.Printf("idle-stop: lifecycle lost after stop %s: %v", ws.ContainerName, err)
		return
	}
	if err := rp.mgr.store.SetWorkspaceState(ctx, ws.ID, "stopped"); err != nil {
		log.Printf("idle-stop: mark stopped %s: %v", ws.ContainerName, err)
	}
	log.Printf("idle-stop: stopped cold workspace %s (tenant %s)", ws.ContainerName, ws.TenantID)
}
