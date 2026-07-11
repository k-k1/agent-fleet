// workspace_handlers.go — Workspace/Session の backend 非依存 HTTP ハンドラ群。
// runtime.go からの機械的分割（docs/23 P2-W1）→ workspaceAPI struct に集約（docs/23 残③）。
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// workspaceAPI は Workspace ライフサイクル + Session 管理の機能ハンドラ集
// （docs/23 残③）。解決プリアンブルは埋め込みの memberAuth（登録側で
// withResolved に包む）。Session 系ハンドラは末尾で Agent へ素通しするため
// agentProxyAPI を保持し、autostart（P3-9 on-demand start フラグ）だけ config
// から写す — それ以外の依存はすべて a.mgr 経由で足りる。
type workspaceAPI struct {
	memberAuth
	proxy     agentProxyAPI
	autostart bool
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
	})
}

func (a workspaceAPI) get(w http.ResponseWriter, r *http.Request, res *resolved) {
	rt := res.rt
	writeJSON(w, http.StatusOK, map[string]any{"name": rt.Name(), "state": rt.State(r.Context())})
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
	_ = res.rt.Stop(r.Context()) // best-effort: may not exist yet
	// Clear the working copies while the container is down. Targeted: we keep the
	// encrypted secrets store and everything else in home.
	_ = os.RemoveAll(filepath.Join(a.mgr.rootedDataDir(res.ws), "home", "repos"))
	a.startResolved(w, r, res)
}

// ensureWorkspaceStarted brings a stopped workspace up, enforcing the same
// max_workspaces quota as a manual start (docs/16 P3-4; 0/unset = unlimited,
// counted authoritatively via docker). No-op if already running. Shared by the
// explicit start/recreate handlers and P3-9 auto-start.
func (a workspaceAPI) ensureWorkspaceStarted(ctx context.Context, res *resolved) *apiError {
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
	if err := rt.Start(ctx); err != nil {
		return internalErr(err)
	}
	_ = a.mgr.store.SetWorkspaceState(ctx, res.ws.ID, "running")
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
	if err := res.rt.Stop(r.Context()); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
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
	Dir  string `json:"dir"`
	Repo string `json:"repo"`
	// Title: the user-supplied display title. Console の displayName は title を最優先
	// で見るが、この struct に無かった頃は中継で silently drop されていた（claude 系は
	// label にも title が埋まるため露見せず、label を使わない shell/ssm だけ表示名が
	// フォールバックに落ちるバグ）。DB ミラー（停止中の再配信）には列が無いので載らない。
	Title     string `json:"title,omitempty"`
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

// sessionsList serves GET /api/sessions. While the Workspace runs the Agent
// is authoritative: fetch its list and mirror it into the DB. While it is stopped
// (or the Agent is briefly unreachable) serve the last mirrored list from the DB —
// as stopped — so the user still sees, and can resume, their sessions.
func (a workspaceAPI) sessionsList(w http.ResponseWriter, r *http.Request, res *resolved) {
	ctx := r.Context()
	if res.rt.State(ctx) == "running" {
		if list, err := a.mgr.agentSessions(ctx, res.rt); err == nil {
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
			_ = a.mgr.store.ReplaceSessions(ctx, res.ws.ID, rows)
			writeJSON(w, http.StatusOK, map[string]any{"sessions": list})
			return
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
			// Container is down: we can't check the dir, so assume resumable; the
			// Agent re-checks and refuses on actual attach if the dir is gone.
			Resumable: true,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

// sessionCreate enforces the per-user session quota (docs/16 P3-4) then
// proxies POST /api/sessions to the Agent.
func (a workspaceAPI) sessionCreate(w http.ResponseWriter, r *http.Request, res *resolved) {
	ctx := r.Context()
	// SSM sessions (docs/history/p3-ssm-session.md): resolve the host bookmark
	// server-side and rewrite the body with the (non-secret) instance/document/SSO
	// coordinates before proxying — the client only sends ssm_host_id.
	if aerr := a.rewriteSSMCreate(ctx, res, r); aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	// P3-9 auto-start: creating a session needs a running Agent, so bring a cold
	// (idle-stopped or manually stopped) workspace back up on demand first.
	if a.autostart {
		if aerr := a.ensureWorkspaceStarted(ctx, res); aerr != nil {
			writeAPIErr(w, aerr)
			return
		}
	}
	if aerr := a.sessionQuotaExceeded(ctx, res); aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	a.proxy.rest(w, r, res)
}

// sessionFork forks a claude session into a new one (POST
// /api/sessions/{name}/fork). Like create, it auto-starts a cold workspace and
// enforces the per-user session quota (a fork adds a session), then proxies to the
// Agent's fork endpoint.
func (a workspaceAPI) sessionFork(w http.ResponseWriter, r *http.Request, res *resolved) {
	ctx := r.Context()
	if a.autostart {
		if aerr := a.ensureWorkspaceStarted(ctx, res); aerr != nil {
			writeAPIErr(w, aerr)
			return
		}
	}
	if aerr := a.sessionQuotaExceeded(ctx, res); aerr != nil {
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
		if aerr := a.ensureWorkspaceStarted(r.Context(), res); aerr != nil {
			writeAPIErr(w, aerr)
			return
		}
	}
	a.proxy.rest(w, r, res)
}

// sessionQuotaExceeded returns a 429 apiError when the caller is at its per-user
// (or tenant-default) concurrent-session cap, else nil. 0/unset = unlimited. Shared
// by session create and fork (both add a running session). If the workspace isn't
// reachable the check is skipped — the proxy reports the real error.
func (a workspaceAPI) sessionQuotaExceeded(ctx context.Context, res *resolved) *apiError {
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
