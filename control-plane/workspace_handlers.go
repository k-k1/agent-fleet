// workspace_handlers.go — backend-independent HTTP handlers for Workspace/Session.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// workspaceAPI is the handler set for the Workspace lifecycle and Session management. The
// resolution preamble comes from the embedded memberAuth (registration wraps it in
// withResolved). Session handlers end by passing the request through to the Agent, so it
// holds an agentProxyAPI, and it copies only autostart (the P3-9 on-demand start flag) from
// config — every other dependency is reachable through a.mgr.
type workspaceAPI struct {
	memberAuth
	proxy     agentProxyAPI
	autostart bool
}

const (
	workspaceLifecycleLease     = 30 * time.Second
	workspaceLifecycleHeartbeat = 10 * time.Second
)

type workspaceLifecycleLeaseGuard struct {
	store     store.Store
	owner     string
	token     string
	ttl       time.Duration
	ctx       context.Context
	cancel    context.CancelFunc
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
	lost      atomic.Bool
}

func leaseTS(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000000Z07:00") }

func acquireWorkspaceLifecycleLease(ctx context.Context, st store.Store, owner string) (*workspaceLifecycleLeaseGuard, error) {
	return acquireWorkspaceLifecycleLeaseWithTiming(ctx, st, owner, workspaceLifecycleLease, workspaceLifecycleHeartbeat)
}

func acquireWorkspaceLifecycleLeaseWithTiming(ctx context.Context, st store.Store, owner string, ttl, heartbeat time.Duration) (*workspaceLifecycleLeaseGuard, error) {
	now := time.Now().UTC()
	token := store.NewID()
	ok, err := st.AcquireSessionShareOwnerLease(ctx, owner, token, leaseTS(now), leaseTS(now.Add(ttl)))
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, store.ErrSessionShareOwnerBusy
	}
	opCtx, cancel := context.WithCancel(ctx)
	guard := &workspaceLifecycleLeaseGuard{store: st, owner: owner, token: token, ttl: ttl,
		ctx: opCtx, cancel: cancel, stop: make(chan struct{}), done: make(chan struct{})}
	go guard.heartbeat(heartbeat)
	return guard, nil
}

func (l *workspaceLifecycleLeaseGuard) heartbeat(interval time.Duration) {
	defer close(l.done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			err := l.checkpoint(ctx)
			cancel()
			if err != nil {
				l.markLost()
				return
			}
		case <-l.stop:
			return
		case <-l.ctx.Done():
			return
		}
	}
}

func (l *workspaceLifecycleLeaseGuard) markLost() {
	l.lost.Store(true)
	l.cancel()
}

func (l *workspaceLifecycleLeaseGuard) checkpoint(ctx context.Context) error {
	if l.lost.Load() {
		return store.ErrSessionShareOwnerBusy
	}
	now := time.Now().UTC()
	ok, err := l.store.RenewSessionShareOwnerLease(ctx, l.owner, l.token, leaseTS(now), leaseTS(now.Add(l.ttl)))
	if err != nil || !ok {
		l.markLost()
		if err != nil {
			return err
		}
		return store.ErrSessionShareOwnerBusy
	}
	return nil
}

func (l *workspaceLifecycleLeaseGuard) Context() context.Context { return l.ctx }

func (l *workspaceLifecycleLeaseGuard) Close() {
	l.closeOnce.Do(func() {
		close(l.stop)
		<-l.done
		l.cancel()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = l.store.ReleaseSessionShareOwnerLease(ctx, l.owner, l.token)
	})
}

func workspaceLifecycleLeaseError(err error) *apiError {
	if errors.Is(err, store.ErrSessionShareOwnerBusy) {
		return &apiError{http.StatusConflict, "workspace_operation_in_progress", "an approved Agent or workspace operation is in progress"}
	}
	return internalErr(err)
}

func newWorkspaceAPI(m *manager, autostart bool) workspaceAPI {
	return workspaceAPI{memberAuth{m}, newAgentProxyAPI(m), autostart}
}

// --- HTTP handlers ---

// whoami reports how the AuthGateway resolved this request, plus the raw
// gateway headers — used to verify the funnel -> oauth2-proxy -> Caddy -> CP
// chain actually delivers the authenticated email. In dev mode resolved_user is
// the fixed id, but the email/sanitized fields still show what proxy mode would
// pick, so the chain can be verified without flipping AUTH globally.
// No resolution preamble (pure header echo), so it registers unwrapped.
func (a workspaceAPI) whoami(w http.ResponseWriter, r *http.Request) {
	email := r.Header.Get(a.mgr.emailHeader)
	writeJSON(w, http.StatusOK, map[string]any{
		"auth_mode":          a.mgr.authMode,
		"resolved_user":      a.mgr.resolveUser(r),
		"email_header":       a.mgr.emailHeader,
		"email":              email,
		"sanitized_user":     sanitizeUser(email),
		"x_forwarded_user":   r.Header.Get("X-Forwarded-User"),
		"preferred_username": r.Header.Get("X-Forwarded-Preferred-Username"),
		// Deployment capability flag: whether the CP scheduler goroutine is running
		// (AF_SCHEDULER_INTERVAL > 0). The Console hides the schedules rail section when
		// it is off, since no schedule can ever fire on this deployment (docs/log/38).
		"scheduler_enabled": schedulerRunning,
	})
}

// workspacePayload composes the GET /api/workspace body. Shared by the REST
// handler and the /api/events push channel so both emit the identical shape.
//
// state is looked up by the caller and passed in: an events tick needs the same state on
// the stats side as well, and ecs-ec2's State() is a real AWS call, so it is taken once per
// tick (docs/log/63 §63.9).
func (a workspaceAPI) workspacePayload(ctx context.Context, res *resolved, state string) map[string]any {
	rt := res.rt
	m := map[string]any{"name": rt.Name(), "state": state}
	// Live boot-install phase for the "starting" dialog (native rootfs only —
	// docs/log/35 §35.9-9). State() now says "starting" for that window too, but only
	// bootPhase can say WHAT is taking minutes (which CLI is being downloaded), which is
	// the difference between a progress dialog and a silent spinner. Only the native
	// runtime implements it; a non-empty value means a boot is in progress.
	if bp, ok := rt.(interface{ BootPhase() string }); ok {
		if phase := bp.BootPhase(); phase != "" {
			m["bootPhase"] = phase
		}
	}
	// Backend drift: the container is running older code than a fresh start would
	// use (workspace_stale.go). The Console turns this into the WS-bar "restart required"
	// badge and into the extra line of the update toast. Only meaningful while running —
	// a stopped workspace picks up the new image on its next start anyway — and only
	// emitted when true, so the payload keeps its usual shape.
	if m["state"] == "running" && workspaceStale(ctx, rt) {
		m["stale"] = true
	}
	return m
}

func (a workspaceAPI) get(w http.ResponseWriter, r *http.Request, res *resolved) {
	ctx := r.Context()
	writeJSON(w, http.StatusOK, a.workspacePayload(ctx, res, res.rt.State(ctx)))
}

func (a workspaceAPI) start(w http.ResponseWriter, r *http.Request, res *resolved) {
	a.startResolved(w, r, res)
}

// recreate tears the container down and starts a fresh one from
// the current image — a clean rebuild (pick up a new build / clear a wedged
// container). Login (a separate CLAUDE_CONFIG_DIR mount) and connections (the
// encrypted store under ~/.config) persist; the cloned working copies under
// ~/repos are wiped (the user accepts losing them, incl. uncommitted work) and
// running sessions are lost — so the Console guards this behind a warning dialog.
func (a workspaceAPI) recreate(w http.ResponseWriter, r *http.Request, res *resolved) {
	// Stop + wipe + restart under the local start lock and distributed owner lease
	// so neither another process nor another CP replica can enter mid-teardown.
	lock := a.mgr.startLockFor(res.ws.ID)
	lock.Lock()
	defer lock.Unlock()
	lease, err := acquireWorkspaceLifecycleLease(r.Context(), a.mgr.store, res.mv.MembershipID)
	if err != nil {
		writeAPIErr(w, workspaceLifecycleLeaseError(err))
		return
	}
	defer lease.Close()
	releaseFence, err := a.mgr.acquireWorkspaceOperationFence(lease.Context(), res.ws.ID, res.rt)
	if err != nil {
		writeAPIErr(w, workspaceLifecycleLeaseError(err))
		return
	}
	defer releaseFence()
	if err := lease.checkpoint(r.Context()); err != nil {
		writeAPIErr(w, workspaceLifecycleLeaseError(err))
		return
	}
	// Stop tolerates "does not exist yet" and the like (best-effort), but abort when the
	// workspace is still alive — deleting under a live bind-mount leaves it inconsistent.
	// "starting" (container up, Agent not answering yet) counts as alive: it is running
	// and can write to home.
	if err := res.rt.Stop(lease.Context()); err != nil && runtime.WorkspaceAlive(res.rt.State(r.Context())) {
		log.Printf("recreate: stop failed for ws %s (still running, aborting wipe): %v", res.ws.ID, err)
		writeAPIErr(w, &apiError{http.StatusInternalServerError, "stop_failed", "could not stop the workspace; recreate aborted"})
		return
	}
	if err := lease.checkpoint(r.Context()); err != nil {
		writeAPIErr(w, workspaceLifecycleLeaseError(err))
		return
	}
	// Clear the working copies while the container is down. Targeted: we keep the
	// encrypted secrets store and everything else in home.
	if err := runtime.RemoveAllContext(lease.Context(), filepath.Join(a.mgr.rootedDataDir(res.ws), "home", "repos")); err != nil {
		if leaseErr := lease.checkpoint(r.Context()); leaseErr != nil {
			writeAPIErr(w, workspaceLifecycleLeaseError(leaseErr))
		} else {
			writeAPIErr(w, internalErr(err))
		}
		return
	}
	if err := lease.checkpoint(r.Context()); err != nil {
		writeAPIErr(w, workspaceLifecycleLeaseError(err))
		return
	}
	if aerr := a.ensureWorkspaceStartedRTLocked(lease.Context(), res, res.rt, lease); aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": res.rt.Name(), "state": res.rt.State(r.Context())})
}

// cleanHome tears the container down, wipes the whole home EXCEPT auth/connection
// state (the homeKeep keep-list: logins, git/agent connections), and starts a fresh
// one from the current image. A deeper reset than recreate, which deletes only
// ~/repos: use this when something under home *outside* ~/repos is wedged — a broken
// ~/.local claude install, corrupt caches, a bad dotfile. Login and connections
// survive; everything else in home (repos, ~/.local, ~/.cache, ~/.gradle, dotfiles)
// is wiped and re-seeded by the entrypoint on start. Same self-serve member action
// as recreate — the Console guards it behind its own warning dialog. (The admin
// "clean home" action uses the same cleanHome but leaves the container stopped; here we
// start back up so the member lands in a working, freshly-seeded environment.)
func (a workspaceAPI) cleanHome(w http.ResponseWriter, r *http.Request, res *resolved) {
	lock := a.mgr.startLockFor(res.ws.ID)
	lock.Lock()
	defer lock.Unlock()
	lease, err := acquireWorkspaceLifecycleLease(r.Context(), a.mgr.store, res.mv.MembershipID)
	if err != nil {
		writeAPIErr(w, workspaceLifecycleLeaseError(err))
		return
	}
	defer lease.Close()
	releaseFence, err := a.mgr.acquireWorkspaceOperationFence(lease.Context(), res.ws.ID, res.rt)
	if err != nil {
		writeAPIErr(w, workspaceLifecycleLeaseError(err))
		return
	}
	defer releaseFence()
	if err := lease.checkpoint(r.Context()); err != nil {
		writeAPIErr(w, workspaceLifecycleLeaseError(err))
		return
	}
	// As in recreate: abort when Stop failed and the workspace is still alive, to avoid
	// deleting under a live bind-mount.
	if err := res.rt.Stop(lease.Context()); err != nil && runtime.WorkspaceAlive(res.rt.State(r.Context())) {
		log.Printf("clean-home: stop failed for ws %s (still running, aborting wipe): %v", res.ws.ID, err)
		writeAPIErr(w, &apiError{http.StatusInternalServerError, "stop_failed", "could not stop the workspace; clean-home aborted"})
		return
	}
	if err := lease.checkpoint(r.Context()); err != nil {
		writeAPIErr(w, workspaceLifecycleLeaseError(err))
		return
	}
	// Wipe home (keep-list preserved) while the container is down — deleting under a
	// live bind-mount risks inconsistency (see cleanHome's contract in runtime_docker.go).
	if err := runtime.CleanHomeContext(lease.Context(), a.mgr.rootedDataDir(res.ws)); err != nil {
		if leaseErr := lease.checkpoint(r.Context()); leaseErr != nil {
			writeAPIErr(w, workspaceLifecycleLeaseError(leaseErr))
		} else {
			writeAPIErr(w, internalErr(err))
		}
		return
	}
	if err := lease.checkpoint(r.Context()); err != nil {
		writeAPIErr(w, workspaceLifecycleLeaseError(err))
		return
	}
	if aerr := a.ensureWorkspaceStartedRTLocked(lease.Context(), res, res.rt, lease); aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": res.rt.Name(), "state": res.rt.State(r.Context())})
}

// ensureWorkspaceStarted brings a stopped workspace up, enforcing the same
// max_workspaces quota as a manual start (docs/16 P3-4; 0/unset = unlimited,
// counted authoritatively via docker). No-op if already running. Shared by the
// explicit start/recreate handlers and P3-9 auto-start.
func (a workspaceAPI) ensureWorkspaceStarted(ctx context.Context, res *resolved) *apiError {
	return a.ensureWorkspaceStartedRT(ctx, res, res.rt)
}

// ensureWorkspaceReady is ensureWorkspaceStarted for the callers that immediately need
// the AGENT, not merely a running container: session create / fork / start, carried
// answers, the SSM node lookup. All of them proxy to the workspace in the very next
// line, and Start deliberately no longer guarantees reachability (Runtime port doc) —
// a cold boot can still be installing when it returns.
//
// So this is where the waiting lives now, and it is bounded twice over:
//   - agentReadyWait (55s by default) keeps the response inside the ingress idle timeout;
//     blocking past it does not deliver a slower success, it delivers a 504.
//   - past that we answer 409 workspace_starting — a code the Console already renders as
//     "starting up" and a caller can retry — instead of a 500 whose body was the raw
//     "agent did not become healthy within 15s". The boot keeps going either way.
func (a workspaceAPI) ensureWorkspaceReady(ctx context.Context, res *resolved) *apiError {
	// Fix the deadline ONCE, at the entrance. Start itself waits out its own synchronous
	// grace period, and not counting that makes "wait for the start + wait for
	// reachability" exceed the ingress limit, taking the response with it.
	deadline := time.Now().Add(runtime.AgentReadyWait())
	if aerr := a.ensureWorkspaceStarted(ctx, res); aerr != nil {
		return aerr
	}
	if res.rt.State(ctx) == "running" {
		return nil // Already answering (the starting mark is gone); not even a probe is needed.
	}
	if remaining := time.Until(deadline); remaining > 0 {
		err := runtime.WaitAgentHealthy(ctx, res.rt.Endpoint(), remaining)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return internalErr(err)
		}
	}
	return &apiError{http.StatusConflict, "workspace_starting",
		"workspace is still starting — try again in a moment"}
}

// ensureWorkspaceStartedUnattended is ensureWorkspaceStarted for a start nobody is
// watching (the scheduler's wake): the container comes up with unattendedStartEnv, so
// the entrypoint keeps the installed agent CLI versions instead of pulling @latest
// before the agent can listen. Falls back to the normal path if the override runtime
// cannot be built — a start with self-update is better than no start at all.
func (a workspaceAPI) ensureWorkspaceStartedUnattended(ctx context.Context, res *resolved) *apiError {
	rt, err := a.mgr.runtimeForUnattended(ctx, res)
	if err != nil {
		log.Printf("unattended runtime for ws %s: %v (falling back to normal start)", res.ws.ID, err)
		return a.ensureWorkspaceStarted(ctx, res)
	}
	return a.ensureWorkspaceStartedRT(ctx, res, rt, runtime.UnattendedStartEnv)
}

// ensureWorkspaceStartedRT is the shared body, parameterized by the runtime that drives
// this particular start (they differ only in the container env they would apply).
// Serialized per workspace locally (startLockFor) and across CP replicas (owner
// lease): the state check and Start form a check-then-act that explicit starts,
// auto-starts and scheduler wake must not interleave.
func (a workspaceAPI) ensureWorkspaceStartedRT(ctx context.Context, res *resolved, rt runtime.Runtime, extraEnv ...string) *apiError {
	lock := a.mgr.startLockFor(res.ws.ID)
	lock.Lock()
	defer lock.Unlock()
	lease, err := acquireWorkspaceLifecycleLease(ctx, a.mgr.store, res.mv.MembershipID)
	if err != nil {
		return workspaceLifecycleLeaseError(err)
	}
	defer lease.Close()
	releaseFence, err := a.mgr.acquireWorkspaceOperationFence(lease.Context(), res.ws.ID, rt)
	if err != nil {
		return workspaceLifecycleLeaseError(err)
	}
	defer releaseFence()
	return a.ensureWorkspaceStartedRTLocked(lease.Context(), res, rt, lease, extraEnv...)
}

func (a workspaceAPI) ensureWorkspaceStartedRTLocked(ctx context.Context, res *resolved, rt runtime.Runtime, lease *workspaceLifecycleLeaseGuard, extraEnv ...string) *apiError {
	if err := lease.checkpoint(ctx); err != nil {
		return workspaceLifecycleLeaseError(err)
	}
	// A reaper may have crashed after committing its stop intent but before
	// Runtime.Stop. This explicit lifecycle operation owns the replacement DB
	// lease and host fence, so reconcile the orphan even when State is running
	// and Start would otherwise return early.
	if err := a.mgr.store.ClearWorkspaceIdleStop(ctx, res.ws.ID); err != nil {
		return internalErr(err)
	}
	switch rt.State(ctx) {
	case "running":
		return nil
	case "starting":
		// A launch is already converging (ECS cold pull can take minutes). Calling
		// Start again would double-drive the service (fresh task def + forced
		// deployment), so return and let the poller observe the transition.
		return nil
	}
	t, err := a.mgr.store.GetTenant(ctx, res.ws.TenantID)
	if err != nil {
		return internalErr(err)
	}
	if lim := parseLimits(t.Limits); lim.MaxWorkspaces > 0 {
		n, err := a.mgr.countRunningInTenant(ctx, res.ws.TenantID)
		if err != nil {
			return internalErr(err)
		}
		if n >= lim.MaxWorkspaces {
			return &apiError{http.StatusTooManyRequests, "quota_workspaces",
				fmt.Sprintf("tenant workspace limit reached (%d)", lim.MaxWorkspaces)}
		}
	}
	// Stage the user guide into <dataDir>/docs so the container's agents can answer
	// environment questions from the authoritative documentation. Everyone receives the
	// same tree (ADR 0064); the developer tree is not baked into the CP image at all,
	// so no container ever holds the decision records or the frozen work journals.
	// Best-effort: a failure just means no docs mount, never a failed start.
	//
	// Only for the adapters that then MOUNT that directory. On ECS nothing would ever
	// read it (the task has no path into the CP's filesystem), so staging there would
	// just copy megabytes onto the CP's disk for nobody; that container pulls the same
	// tree over /internal/docs instead (docs_bridge.go).
	if _, mounts := rt.(runtime.DocsMounter); mounts {
		if err := stageWorkspaceDocs(a.mgr.rootedDataDir(res.ws)); err != nil {
			log.Printf("stage workspace docs (ws=%s): %v", res.ws.ID, err)
		}
	}
	if err := lease.checkpoint(ctx); err != nil {
		return workspaceLifecycleLeaseError(err)
	}
	// docs/log/81 §4: fix this container run's preview slug BEFORE the container is
	// created and start with a runtime that carries the URL in its env. In the other
	// order, an app that learns its own public URL from the env (Next.js NEXTAUTH_URL /
	// AUTH_URL / metadataBase) ends up in the "the URL is up but the app has the wrong
	// idea of where it lives" state. Where preview is disabled for the deployment, or no
	// slug could be prepared, use rt as-is — not getting a preview is no reason to block
	// the start.
	if armed := a.mgr.armPreviewForStart(ctx, res, extraEnv); armed != nil {
		rt = armed
	}
	if err := rt.Start(ctx); err != nil {
		return internalErr(err)
	}
	if err := lease.checkpoint(ctx); err != nil {
		if f, ok := rt.(runtime.StartFencer); ok {
			abortCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = f.AbortUncommittedStart(abortCtx)
			cancel()
		}
		return workspaceLifecycleLeaseError(err)
	}
	if f, ok := rt.(runtime.StartFencer); ok {
		f.CommitStart()
	}
	_ = a.mgr.store.SetWorkspaceState(ctx, res.ws.ID, "running")
	// A start IS activity: reset the in-memory idle clock too (SetWorkspaceState
	// already bumped DB last_active_at). Without this the reaper could stop the
	// freshly-started workspace on its next sweep off a stale in-memory lastSeen
	// (P3-9; see reaper.idleBase).
	_ = a.mgr.touchWorkspace(ctx, res.ws.ID)
	return nil
}

// startResolved starts the container (with quota) and writes the JSON result.
// Shared by the explicit start and recreate handlers. The reported state is the
// live one, not a hardcoded "running" — on ECS a successful Start can leave the
// service still "starting" (cold image pull), which the Console keeps polling.
func (a workspaceAPI) startResolved(w http.ResponseWriter, r *http.Request, res *resolved) {
	if aerr := a.ensureWorkspaceStarted(r.Context(), res); aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": res.rt.Name(), "state": res.rt.State(r.Context())})
}

func (a workspaceAPI) stop(w http.ResponseWriter, r *http.Request, res *resolved) {
	// Serialize with starts/recreates and owner-approved shared operations. Once a
	// stop wins this lock, no shared operation may pass a stale running check;
	// once an approved operation wins it, stop waits for its durable outcome.
	lock := a.mgr.startLockFor(res.ws.ID)
	lock.Lock()
	defer lock.Unlock()
	lease, err := acquireWorkspaceLifecycleLease(r.Context(), a.mgr.store, res.mv.MembershipID)
	if err != nil {
		writeAPIErr(w, workspaceLifecycleLeaseError(err))
		return
	}
	defer lease.Close()
	releaseFence, err := a.mgr.acquireWorkspaceOperationFence(lease.Context(), res.ws.ID, res.rt)
	if err != nil {
		writeAPIErr(w, workspaceLifecycleLeaseError(err))
		return
	}
	defer releaseFence()
	if err := lease.checkpoint(r.Context()); err != nil {
		writeAPIErr(w, workspaceLifecycleLeaseError(err))
		return
	}
	if err := res.rt.Stop(lease.Context()); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if err := lease.checkpoint(r.Context()); err != nil {
		writeAPIErr(w, workspaceLifecycleLeaseError(err))
		return
	}
	_ = a.mgr.store.SetWorkspaceState(r.Context(), res.ws.ID, "stopped")
	writeJSON(w, http.StatusOK, map[string]any{"name": res.rt.Name(), "state": "stopped"})
}

// sessionWire is the session shape exchanged with the Agent and the Console. The
// CP mirrors it into the DB so it can be re-served while the container is down.
type sessionWire struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Driver (docs/log/27 P1.5): "managed" = a paneless session driven by the shared
	// runtime. This struct is a relay that decodes the Agent response and re-emits it, so
	// any field absent here is silently dropped — without this one the Console's
	// isManagedSession stays false forever and the managed UI never starts. The DB mirror
	// (re-serving while stopped) has no column for it, so a stopped session displays as
	// tui and returns to the right driver on the first poll after it resumes.
	Driver string `json:"driver,omitempty"`
	Dir    string `json:"dir"`
	// Subdir: the folder BENEATH Dir the agent actually runs in ("" = Dir itself).
	// Same relay caveat as the fields around it — absent here, the Console never sees
	// which folder a session was launched in. The DB mirror has no column for it, so it is
	// absent while stopped.
	Subdir        string `json:"subdir,omitempty"`
	Repo          string `json:"repo"`
	WorkingCopyID string `json:"workingCopyId,omitempty"`
	// Title: the user-supplied display title, which the Console's displayName prefers above
	// everything else. Dropping it in the relay barely shows for claude kinds (title is
	// baked into label too) — only shell/ssm, which use no label, fall back to a wrong
	// display name. The DB mirror (re-serving while stopped) has no column for it.
	Title   string `json:"title,omitempty"`
	Display string `json:"display"` // human-readable name from the Agent (title → label → repo@time)
	// Color: terminal background hue (hex; SSM sessions carry their host color).
	// Dropped in the relay, an SSM session's background always arrives through the CP as
	// the default colour. The DB mirror has no column for it, so it is absent while stopped.
	Color     string `json:"color,omitempty"`
	Label     string `json:"label"`
	Started   string `json:"started"`
	CreatedAt string `json:"createdAt"`
	RemoteUrl string `json:"remoteUrl"`
	State     string `json:"state"`
	Alive     bool   `json:"alive"`
	Resumable bool   `json:"resumable"`
	// Locked is the user's deletion lock. sessionWire decode→re-emits the Agent
	// response for both GET /api/sessions and SSE, so omitting this field makes a
	// successfully saved lock immediately disappear from the Console.
	Locked   bool `json:"locked,omitempty"`
	Archived bool `json:"archived,omitempty"`
	// BackgroundBusy passes through the Agent's "idle but a run_in_background task is
	// still running" flag so the Console can badge it. Not persisted to the DB mirror
	// (a stopped workspace has no live background work).
	BackgroundBusy bool `json:"backgroundBusy"`
	// BackgroundBusyReason passes through WHAT is running ("process" | "subagent" |
	// "shell"), which only picks the badge's wording. Dropping it here would not hide the
	// badge, just silently pin every session to the generic "running in background" wording.
	BackgroundBusyReason string `json:"backgroundBusyReason,omitempty"`
	// RateLimitResumeAt passes through the Agent's scheduled auto-resume time (RFC3339,
	// present only while state == "limited"). Dropped, the Console's chip can only say it
	// is waiting for the limit to lift and not when work will resume. No DB-mirror column
	// (a stopped workspace has no episode in progress).
	RateLimitResumeAt string `json:"rateLimitResumeAt,omitempty"`
	// Context: claude's remaining context (the source data for ContextBar). The shape is
	// owned by the Agent and the Console (chat view) and the CP does not interpret it, so
	// it is relayed as RawMessage. Dropped in the relay, ContextBar never appears through
	// the CP.
	Context json.RawMessage `json:"context,omitempty"`
	// Branch/worktree metadata passed through from the Agent (this struct decodes the
	// Agent's /sessions response and is re-emitted to the Console, so any field absent
	// here is silently dropped). Drives the branch-drift badge and the worktree branch-
	// rename menu. omitempty so the DB-mirror path (stopped workspace) omits them.
	Branch        string `json:"branch,omitempty"`
	CurrentBranch string `json:"currentBranch,omitempty"`
	BranchDrift   bool   `json:"branchDrift,omitempty"`
	Worktree      bool   `json:"worktree,omitempty"`
	// Exit cause of a STOPPED session (docs/log/26): "oom" | "killed" | "crashed"; empty
	// after a clean or deliberate stop. ExitCode is the pane's raw wait status (137 = OOM
	// SIGKILL), ExitSignal the derived signal number. Dropped in the relay, the docs/log/26
	// exit chip (OOM / crash) never shows through the CP. The DB mirror has no column, so
	// it is absent while stopped — an accepted trade-off, since only stopped sessions have
	// an exit and the chip therefore disappears while the Agent is down.
	// NOTE: the Agent wire also carries tmux ("claude_"+name), which the Console does not
	// use and could derive; it is deliberately not relayed. When adding a field to the
	// Agent-side wire (Session in internal/session), remember that forgetting it here is a
	// silent drop.
	ExitReason string `json:"exitReason,omitempty"`
	ExitCode   int    `json:"exitCode,omitempty"`
	ExitSignal int    `json:"exitSignal,omitempty"`
	// KeepAwakeUntil: the deadline until which the user declared this session protected
	// from idle auto-stop (RFC3339, docs/log/75). Absent here it is silently dropped and
	// the pin never reaches the reaper — "I pinned it and it stopped anyway". No DB-mirror
	// column: a stopped Workspace has no running job to protect (the Agent declares it
	// again the next time it wakes).
	KeepAwakeUntil string `json:"keepAwakeUntil,omitempty"`
	// Carried is the kind of interaction that was waiting for an answer when the session
	// was folded away (docs/log/75 §75.6.5). A gap in the relay is a silent drop, so it is
	// needed in BOTH this struct and the DB mirror: while running the list is built from
	// this Agent-supplied value, while stopped from the column baked in by ReplaceSessions.
	// With only one of the two, the badge lies by vanishing the moment the Workspace stops.
	Carried string `json:"carried,omitempty"`
}

func fmtStarted(createdAt string) string {
	if t, err := time.Parse(time.RFC3339, createdAt); err == nil {
		return t.Local().Format("01/02 15:04")
	}
	return ""
}

// sessionsPayload composes the GET /api/sessions body. While the Workspace runs
// the Agent is authoritative: fetch its list and mirror it into the DB. While it
// is stopped (or the Agent is briefly unreachable) serve the last mirrored list
// from the DB — as stopped — so the user still sees, and can resume, their
// sessions. Shared by the REST handler and the /api/events push channel.
func (a workspaceAPI) sessionsPayload(ctx context.Context, res *resolved) map[string]any {
	if res.rt.State(ctx) == "running" {
		if list, err := a.mgr.agentSessions(ctx, res.rt); err == nil {
			rows := make([]store.SessionRow, 0, len(list))
			for _, s := range list {
				state := "stopped"
				if s.Alive {
					state = "running"
				}
				rows = append(rows, store.SessionRow{
					Name: s.Name, Kind: s.Kind, Dir: s.Dir, Repo: s.Repo,
					Label: s.Label, CreatedAt: s.CreatedAt, State: state,
					Carried: s.Carried,
				})
			}
			_ = a.mgr.store.ReplaceSessions(ctx, res.ws.ID, rows)
			return map[string]any{"sessions": list}
		}
		// Agent unreachable (e.g. mid-start): fall through to the DB mirror.
	}
	rows, err := a.mgr.store.ListSessions(ctx, res.ws.ID)
	if err != nil {
		rows = nil
	}
	out := make([]sessionWire, 0, len(rows))
	for _, r0 := range rows {
		out = append(out, sessionWire{
			Name: r0.Name, Kind: r0.Kind, Dir: r0.Dir, Repo: r0.Repo, Label: r0.Label,
			Started: fmtStarted(r0.CreatedAt), CreatedAt: r0.CreatedAt, Alive: false,
			// Surface "something is still waiting for an answer" even while the
			// workspace is stopped (docs/log/75 §75.6.5).
			Carried: r0.Carried,
			// Container is down: we can't check the dir, so assume resumable; the
			// Agent re-checks and refuses on actual attach if the dir is gone.
			Resumable: true,
		})
	}
	return map[string]any{"sessions": out}
}

func (a workspaceAPI) sessionsList(w http.ResponseWriter, r *http.Request, res *resolved) {
	writeJSON(w, http.StatusOK, a.sessionsPayload(r.Context(), res))
}

// sessionCreate enforces the per-user session quota (docs/16 P3-4) then
// proxies POST /api/sessions to the Agent.
func (a workspaceAPI) sessionCreate(w http.ResponseWriter, r *http.Request, res *resolved) {
	ctx := r.Context()
	// SSM sessions (docs/log/p3-ssm-session.md): resolve the host bookmark
	// server-side and rewrite the body with the (non-secret) instance/document/SSO
	// coordinates before proxying — the client only sends ssm_host_id.
	if aerr := a.rewriteSSMCreate(ctx, res, r); aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	// P3-9 auto-start: creating a session needs a running Agent, so bring a cold
	// (idle-stopped or manually stopped) workspace back up on demand first.
	if a.autostart {
		if aerr := a.ensureWorkspaceReady(ctx, res); aerr != nil {
			writeAPIErr(w, aerr)
			return
		}
	}
	if aerr := a.sessionQuotaExceeded(ctx, res, peekSessionKind(r)); aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	a.proxy.rest(w, r, res)
}

// sessionFork forks a session's conversation into a new one (POST
// /api/sessions/{name}/fork). Like create, it auto-starts a cold workspace and
// enforces the per-user session quota (a fork adds a session), then proxies to the
// Agent's fork endpoint. The optional body (`{"at": …}` — a point fork, docs/log/55) is
// relayed verbatim by proxy.rest; the Agent owns validating the anchor.
func (a workspaceAPI) sessionFork(w http.ResponseWriter, r *http.Request, res *resolved) {
	ctx := r.Context()
	if a.autostart {
		if aerr := a.ensureWorkspaceReady(ctx, res); aerr != nil {
			writeAPIErr(w, aerr)
			return
		}
	}
	if aerr := a.sessionQuotaExceeded(ctx, res, ""); aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	a.proxy.rest(w, r, res)
}

// sessionStart relaunches a stopped session (POST /api/sessions/{name}/start)
// without attaching — used by the SSM login modal's resume flow. Auto-starts a cold
// workspace first, then proxies to the Agent (which runs ensureSessionTmux). No quota
// check: resuming doesn't create a new session slot the user didn't already have.
func (a workspaceAPI) sessionStart(w http.ResponseWriter, r *http.Request, res *resolved) {
	if a.autostart {
		if aerr := a.ensureWorkspaceReady(r.Context(), res); aerr != nil {
			writeAPIErr(w, aerr)
			return
		}
	}
	a.proxy.rest(w, r, res)
}

// attention is the single bit "a human is touching this Console right now" (docs/log/75 P3).
//
// Presence is otherwise decided by terminal keystrokes, which makes someone who is reading
// back through the mirror — neither typing nor sending — look absent. If the Workspace stops
// while they read, the Agent goes down with it and even the transcript becomes unavailable
// (the mirror switches to "history cannot be fetched while stopped").
//
// What is reported is "a human acted", not "a tab is open": the Console posts here at most
// once every 60 seconds, on real interaction (pointerdown / keydown / wheel) while the
// document is visible. An idle open tab sends nothing, so "a socket alone keeps it warm",
// which P3 removed, does not come back.
//
// Deliberately not routed through auto-start: otherwise opening the tab of a stopped
// Workspace and clicking would bring the container up (the same reasoning that keeps
// terminal attach from auto-starting). Only an explicit user action wakes it.
func (a workspaceAPI) attention(w http.ResponseWriter, r *http.Request, res *resolved) {
	// A 409 from racing a stop is fine to ignore: one presence record is lost and the next
	// interaction sends another. The caller (a beacon) does not read the response either.
	_ = a.mgr.touchWorkspace(r.Context(), res.ws.ID)
	w.WriteHeader(http.StatusNoContent)
}

// sessionCarriedAnswer answers a carried interaction (docs/log/75): the question / plan /
// permission that was on screen when the session was folded away. Like sessionStart it
// auto-starts a cold workspace first — this is the ONE write whose whole point is to
// reach a workspace the idle-stop reaper turned off, and the user pressing "answer and
// resume" in the Console is exactly the deliberate "I am using this now" action auto-start
// is for.
// The Agent then resumes the session and delivers the answer as ordinary prose.
func (a workspaceAPI) sessionCarriedAnswer(w http.ResponseWriter, r *http.Request, res *resolved) {
	if a.autostart {
		if aerr := a.ensureWorkspaceReady(r.Context(), res); aerr != nil {
			writeAPIErr(w, aerr)
			return
		}
	}
	a.proxy.rest(w, r, res)
}

// ssmInstances resolves a member-owned SSO profile and proxies an online-node
// lookup to the workspace. AWS credentials never pass through the Control Plane.
func (a workspaceAPI) ssmInstances(w http.ResponseWriter, r *http.Request, res *resolved) {
	var in struct {
		ProfileID string `json:"profileId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid JSON body"})
		return
	}
	p, found, err := a.mgr.store.GetSSMProfile(r.Context(), strings.TrimSpace(in.ProfileID))
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !found || p.MembershipID != res.mv.MembershipID {
		writeAPIErr(w, &apiError{http.StatusNotFound, "not_found", "SSM profile not found"})
		return
	}
	if a.autostart {
		if aerr := a.ensureWorkspaceReady(r.Context(), res); aerr != nil {
			writeAPIErr(w, aerr)
			return
		}
	}
	body, _ := json.Marshal(map[string]string{
		"Profile": ssmProfileName(p.Label), "Region": p.Region, "StartURL": p.StartURL,
		"SSORegion": p.SSORegion, "AccountID": p.AccountID, "RoleName": p.RoleName,
	})
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.Header.Set("Content-Length", strconv.Itoa(len(body)))
	a.proxy.rest(w, r, res)
}

// sessionQuotaExceeded returns a 429 apiError when the caller is at its per-user
// (or tenant-default) concurrent-session cap, else nil. 0/unset = unlimited. Shared
// by session create and fork (both add a running session). If the workspace isn't
// reachable the check is skipped — the proxy reports the real error.
//
// createKind is the kind of the session being created ("" for fork, which is always
// claude). shell/ssm are unmetered terminals, so creating one is never blocked (and
// countSessions already omits them from the running count). Native runtime is a
// single-user host with no shared-host contention, so the cap is not enforced there.
// peekSessionKind reads a create-session request body to extract its kind, then
// restores the body so the proxy can forward it unchanged. Returns "" on any read/
// parse failure — the quota check then treats it as a metered agent session (the
// safe default) and the Agent surfaces the real error.
func peekSessionKind(r *http.Request) string {
	body, err := readAllBody(r)
	if err != nil {
		return ""
	}
	restoreBody(r, body)
	var peek struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(body, &peek); err != nil {
		return ""
	}
	return peek.Kind
}

func (a workspaceAPI) sessionQuotaExceeded(ctx context.Context, res *resolved, createKind string) *apiError {
	if a.mgr.nativeRuntime || isUnmeteredKind(createKind) {
		return nil
	}
	lim := 0
	if ul, ok, _ := a.mgr.store.GetUserLimit(ctx, res.mv.MembershipID); ok && ul.MaxSessions > 0 {
		lim = ul.MaxSessions
	} else if t, err := a.mgr.store.GetTenant(ctx, res.ws.TenantID); err == nil {
		lim = parseLimits(t.Limits).MaxSessions
	}
	if lim > 0 {
		if n, err := a.mgr.countSessions(ctx, res.rt); err == nil && n >= lim {
			// Developer-facing fallback; the Console localizes by `code` (quota_sessions).
			return &apiError{http.StatusTooManyRequests, errCodeQuotaSessions,
				fmt.Sprintf("concurrent session limit reached (%d running, max %d)", n, lim)}
		}
	}
	return nil
}
