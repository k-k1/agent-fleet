// workspace_handlers.go — Workspace/Session の backend 非依存 HTTP ハンドラ群。
// runtime.go からの機械的分割（docs/23 P2-W1）。
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// --- HTTP handlers ---

// rtFor resolves the request's user (AuthGateway) and returns its runtime. When
// the gateway provides no identity (proxy mode, missing header) it writes 401
// and returns ok=false; callers must stop.
// resolvedFor returns the full per-request resolution (runtime + workspace +
// identity + membership). Tenant selection: header for REST; query param for the
// terminal WebSocket (browsers can't set custom headers on a WS handshake).
func (c config) resolvedFor(w http.ResponseWriter, r *http.Request) (*resolved, bool) {
	id := c.mgr.resolveIdentity(r)
	if id.key == "" {
		writeAPIErr(w, &apiError{http.StatusUnauthorized, "unauthenticated", "no gateway identity"})
		return nil, false
	}
	tenantSel := r.Header.Get("X-AF-Tenant")
	if tenantSel == "" {
		tenantSel = r.URL.Query().Get("tenant")
	}
	res, aerr := c.mgr.resolveFull(r.Context(), id.key, id.email, tenantSel)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return nil, false
	}
	return res, true
}

func (c config) rtFor(w http.ResponseWriter, r *http.Request) (Runtime, bool) {
	res, ok := c.resolvedFor(w, r)
	if !ok {
		return nil, false
	}
	return res.rt, true
}

// handleWhoami reports how the AuthGateway resolved this request, plus the raw
// gateway headers — used to verify the funnel -> oauth2-proxy -> Caddy -> CP
// chain actually delivers the authenticated email. In dev mode resolved_user is
// the fixed id, but the email/sanitized fields still show what proxy mode would
// pick, so the chain can be verified without flipping AUTH globally.
func (c config) handleWhoami(w http.ResponseWriter, r *http.Request) {
	email := r.Header.Get(c.mgr.emailHeader)
	writeJSON(w, http.StatusOK, map[string]any{
		"auth_mode":          c.mgr.authMode,
		"resolved_user":      c.mgr.resolveUser(r),
		"email_header":       c.mgr.emailHeader,
		"email":              email,
		"sanitized_user":     sanitizeUser(email),
		"x_forwarded_user":   r.Header.Get("X-Forwarded-User"),
		"preferred_username": r.Header.Get("X-Forwarded-Preferred-Username"),
	})
}

func (c config) handleWorkspaceGet(w http.ResponseWriter, r *http.Request) {
	rt, ok := c.rtFor(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": rt.Name(), "state": rt.State(r.Context())})
}

func (c config) handleWorkspaceStart(w http.ResponseWriter, r *http.Request) {
	res, ok := c.resolvedFor(w, r)
	if !ok {
		return
	}
	c.startResolved(w, r, res)
}

// handleWorkspaceRecreate tears the container down and starts a fresh one from
// the current image — a clean rebuild (pick up a new build / clear a wedged
// container). Login (a separate CLAUDE_CONFIG_DIR mount) and connections (the
// encrypted store under ~/.config) persist; the cloned working copies under
// ~/repos are wiped (the user accepts losing them, incl. uncommitted work) and
// running sessions are lost — so the Console guards this behind a warning dialog.
func (c config) handleWorkspaceRecreate(w http.ResponseWriter, r *http.Request) {
	res, ok := c.resolvedFor(w, r)
	if !ok {
		return
	}
	_ = res.rt.Stop(r.Context()) // best-effort: may not exist yet
	// Clear the working copies while the container is down. Targeted: we keep the
	// encrypted secrets store and everything else in home.
	_ = os.RemoveAll(filepath.Join(c.mgr.rootedDataDir(res.ws), "home", "repos"))
	c.startResolved(w, r, res)
}

// ensureWorkspaceStarted brings a stopped workspace up, enforcing the same
// max_workspaces quota as a manual start (docs/16 P3-4; 0/unset = unlimited,
// counted authoritatively via docker). No-op if already running. Shared by the
// explicit start/recreate handlers and P3-9 auto-start.
func (c config) ensureWorkspaceStarted(ctx context.Context, res *resolved) *apiError {
	rt := res.rt
	switch rt.State(ctx) {
	case "running":
		return nil
	case "starting":
		// A launch is already converging (ECS cold pull can take minutes). Calling
		// Start again would double-drive the service (fresh task def + forced
		// deployment), so return and let the poller observe the transition.
		return nil
	}
	t, err := c.mgr.store.GetTenant(ctx, res.ws.TenantID)
	if err != nil {
		return internalErr(err)
	}
	if lim := parseLimits(t.Limits); lim.MaxWorkspaces > 0 {
		n, err := c.mgr.countRunningInTenant(ctx, res.ws.TenantID)
		if err != nil {
			return internalErr(err)
		}
		if n >= lim.MaxWorkspaces {
			return &apiError{http.StatusTooManyRequests, "quota_workspaces",
				fmt.Sprintf("tenant workspace limit reached (%d)", lim.MaxWorkspaces)}
		}
	}
	if err := rt.Start(ctx); err != nil {
		return internalErr(err)
	}
	_ = c.mgr.store.SetWorkspaceState(ctx, res.ws.ID, "running")
	return nil
}

// startResolved starts the container (with quota) and writes the JSON result.
// Shared by the explicit start and recreate handlers. The reported state is the
// live one, not a hardcoded "running" — on ECS a successful Start can leave the
// service still "starting" (cold image pull), which the Console keeps polling.
func (c config) startResolved(w http.ResponseWriter, r *http.Request, res *resolved) {
	if aerr := c.ensureWorkspaceStarted(r.Context(), res); aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": res.rt.Name(), "state": res.rt.State(r.Context())})
}

func (c config) handleWorkspaceStop(w http.ResponseWriter, r *http.Request) {
	res, ok := c.resolvedFor(w, r)
	if !ok {
		return
	}
	if err := res.rt.Stop(r.Context()); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	_ = c.mgr.store.SetWorkspaceState(r.Context(), res.ws.ID, "stopped")
	writeJSON(w, http.StatusOK, map[string]any{"name": res.rt.Name(), "state": "stopped"})
}

// sessionWire is the session shape exchanged with the Agent and the Console. The
// CP mirrors it into the DB so it can be re-served while the container is down.
type sessionWire struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Dir       string `json:"dir"`
	Repo      string `json:"repo"`
	Display   string `json:"display"` // human-readable name from the Agent (title → label → repo@time)
	Label     string `json:"label"`
	Started   string `json:"started"`
	CreatedAt string `json:"createdAt"`
	RemoteUrl string `json:"remoteUrl"`
	State     string `json:"state"`
	Alive     bool   `json:"alive"`
	Resumable bool   `json:"resumable"`
	// BackgroundBusy passes through the Agent's "idle but a run_in_background task is
	// still running" flag so the Console can badge it. Not persisted to the DB mirror
	// (a stopped workspace has no live background work).
	BackgroundBusy bool `json:"backgroundBusy"`
	// Branch/worktree metadata passed through from the Agent (this struct decodes the
	// Agent's /sessions response and is re-emitted to the Console, so any field absent
	// here is silently dropped). Drives the branch-drift badge and the worktree branch-
	// rename menu. omitempty so the DB-mirror path (stopped workspace) omits them.
	Branch        string `json:"branch,omitempty"`
	CurrentBranch string `json:"currentBranch,omitempty"`
	BranchDrift   bool   `json:"branchDrift,omitempty"`
	Worktree      bool   `json:"worktree,omitempty"`
}

func fmtStarted(createdAt string) string {
	if t, err := time.Parse(time.RFC3339, createdAt); err == nil {
		return t.Local().Format("01/02 15:04")
	}
	return ""
}

// handleSessionsList serves GET /api/sessions. While the Workspace runs the Agent
// is authoritative: fetch its list and mirror it into the DB. While it is stopped
// (or the Agent is briefly unreachable) serve the last mirrored list from the DB —
// as stopped — so the user still sees, and can resume, their sessions.
func (c config) handleSessionsList(w http.ResponseWriter, r *http.Request) {
	res, ok := c.resolvedFor(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if res.rt.State(ctx) == "running" {
		if list, err := c.mgr.agentSessions(ctx, res.rt); err == nil {
			rows := make([]SessionRow, 0, len(list))
			for _, s := range list {
				state := "stopped"
				if s.Alive {
					state = "running"
				}
				rows = append(rows, SessionRow{
					Name: s.Name, Kind: s.Kind, Dir: s.Dir, Repo: s.Repo,
					Label: s.Label, CreatedAt: s.CreatedAt, State: state,
				})
			}
			_ = c.mgr.store.ReplaceSessions(ctx, res.ws.ID, rows)
			writeJSON(w, http.StatusOK, map[string]any{"sessions": list})
			return
		}
		// Agent unreachable (e.g. mid-start): fall through to the DB mirror.
	}
	rows, err := c.mgr.store.ListSessions(ctx, res.ws.ID)
	if err != nil {
		rows = nil
	}
	out := make([]sessionWire, 0, len(rows))
	for _, r0 := range rows {
		out = append(out, sessionWire{
			Name: r0.Name, Kind: r0.Kind, Dir: r0.Dir, Repo: r0.Repo, Label: r0.Label,
			Started: fmtStarted(r0.CreatedAt), CreatedAt: r0.CreatedAt, Alive: false,
			// Container is down: we can't check the dir, so assume resumable; the
			// Agent re-checks and refuses on actual attach if the dir is gone.
			Resumable: true,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

// handleSessionCreate enforces the per-user session quota (docs/16 P3-4) then
// proxies POST /api/sessions to the Agent.
func (c config) handleSessionCreate(w http.ResponseWriter, r *http.Request) {
	res, ok := c.resolvedFor(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	// SSM sessions (docs/history/p3-ssm-session.md): resolve the host bookmark
	// server-side and rewrite the body with the (non-secret) instance/document/SSO
	// coordinates before proxying — the client only sends ssm_host_id.
	if aerr := c.rewriteSSMCreate(ctx, res, r); aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	// P3-9 auto-start: creating a session needs a running Agent, so bring a cold
	// (idle-stopped or manually stopped) workspace back up on demand first.
	if c.autostart {
		if aerr := c.ensureWorkspaceStarted(ctx, res); aerr != nil {
			writeAPIErr(w, aerr)
			return
		}
	}
	if aerr := c.sessionQuotaExceeded(ctx, res); aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	c.proxyAgentREST(w, r)
}

// handleSessionFork forks a claude session into a new one (POST
// /api/sessions/{name}/fork). Like create, it auto-starts a cold workspace and
// enforces the per-user session quota (a fork adds a session), then proxies to the
// Agent's fork endpoint.
func (c config) handleSessionFork(w http.ResponseWriter, r *http.Request) {
	res, ok := c.resolvedFor(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if c.autostart {
		if aerr := c.ensureWorkspaceStarted(ctx, res); aerr != nil {
			writeAPIErr(w, aerr)
			return
		}
	}
	if aerr := c.sessionQuotaExceeded(ctx, res); aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	c.proxyAgentREST(w, r)
}

// handleSessionStart relaunches a stopped session (POST /api/sessions/{name}/start)
// without attaching — used by the SSM login modal's resume flow. Auto-starts a cold
// workspace first, then proxies to the Agent (which runs ensureSessionTmux). No quota
// check: resuming doesn't create a new session slot the user didn't already have.
func (c config) handleSessionStart(w http.ResponseWriter, r *http.Request) {
	res, ok := c.resolvedFor(w, r)
	if !ok {
		return
	}
	if c.autostart {
		if aerr := c.ensureWorkspaceStarted(r.Context(), res); aerr != nil {
			writeAPIErr(w, aerr)
			return
		}
	}
	c.proxyAgentREST(w, r)
}

// sessionQuotaExceeded returns a 429 apiError when the caller is at its per-user
// (or tenant-default) concurrent-session cap, else nil. 0/unset = unlimited. Shared
// by session create and fork (both add a running session). If the workspace isn't
// reachable the check is skipped — the proxy reports the real error.
func (c config) sessionQuotaExceeded(ctx context.Context, res *resolved) *apiError {
	lim := 0
	if ul, ok, _ := c.mgr.store.GetUserLimit(ctx, res.mv.MembershipID); ok && ul.MaxSessions > 0 {
		lim = ul.MaxSessions
	} else if t, err := c.mgr.store.GetTenant(ctx, res.ws.TenantID); err == nil {
		lim = parseLimits(t.Limits).MaxSessions
	}
	if lim > 0 {
		if n, err := c.mgr.countSessions(ctx, res.rt); err == nil && n >= lim {
			// Developer-facing fallback; the Console localizes by `code` (quota_sessions).
			return &apiError{http.StatusTooManyRequests, errCodeQuotaSessions,
				fmt.Sprintf("concurrent session limit reached (%d running, max %d)", n, lim)}
		}
	}
	return nil
}
