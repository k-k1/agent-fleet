// workspace_handlers.go — Workspace/Session の backend 非依存 HTTP ハンドラ群。
// runtime.go からの機械的分割（docs/log/23 P2-W1）→ workspaceAPI struct に集約（docs/log/23 残③）。
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
)

// workspaceAPI は Workspace ライフサイクル + Session 管理の機能ハンドラ集
// （docs/log/23 残③）。解決プリアンブルは埋め込みの memberAuth（登録側で
// withResolved に包む）。Session 系ハンドラは末尾で Agent へ素通しするため
// agentProxyAPI を保持し、autostart（P3-9 on-demand start フラグ）だけ config
// から写す — それ以外の依存はすべて a.mgr 経由で足りる。
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
	store     Store
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

func acquireWorkspaceLifecycleLease(ctx context.Context, st Store, owner string) (*workspaceLifecycleLeaseGuard, error) {
	return acquireWorkspaceLifecycleLeaseWithTiming(ctx, st, owner, workspaceLifecycleLease, workspaceLifecycleHeartbeat)
}

func acquireWorkspaceLifecycleLeaseWithTiming(ctx context.Context, st Store, owner string, ttl, heartbeat time.Duration) (*workspaceLifecycleLeaseGuard, error) {
	now := time.Now().UTC()
	token := newID()
	ok, err := st.AcquireSessionShareOwnerLease(ctx, owner, token, leaseTS(now), leaseTS(now.Add(ttl)))
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errSessionShareOwnerBusy
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
		return errSessionShareOwnerBusy
	}
	now := time.Now().UTC()
	ok, err := l.store.RenewSessionShareOwnerLease(ctx, l.owner, l.token, leaseTS(now), leaseTS(now.Add(l.ttl)))
	if err != nil || !ok {
		l.markLost()
		if err != nil {
			return err
		}
		return errSessionShareOwnerBusy
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
	if errors.Is(err, errSessionShareOwnerBusy) {
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
// state は呼び出し側が引いて渡す。events の tick では stats 側でも同じ状態が要り、
// ecs-ec2 の State() は実 AWS 呼び出しなので、1 tick 1 回に束ねている
// （docs/log/63 §63.9）。
func (a workspaceAPI) workspacePayload(ctx context.Context, res *resolved, state string) map[string]any {
	rt := res.rt
	m := map[string]any{"name": rt.Name(), "state": state}
	// Live boot-install phase for the "starting" dialog (native rootfs only —
	// docs/log/35 §35.9-9). State() now says "starting" for that window too, but only
	// bootPhase can say WHAT is taking minutes (どの CLI を落としているか), which is
	// the difference between a progress dialog and a silent spinner. Only the native
	// runtime implements it; a non-empty value means a boot is in progress.
	if bp, ok := rt.(interface{ BootPhase() string }); ok {
		if phase := bp.BootPhase(); phase != "" {
			m["bootPhase"] = phase
		}
	}
	// Backend drift: the container is running older code than a fresh start would
	// use (workspace_stale.go). The Console turns this into the WS-bar 要再起動 badge
	// and into the extra line of the update toast. Only meaningful while running —
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
	// Stop は「まだ存在しない」等は許容する(best-effort)が、まだ生きているなら中断する
	// — ライブ bind-mount 配下の削除は不整合を起こすため。"starting"（起動途中で
	// Agent 未応答）も生きている側: コンテナは走っていて home に書き込みうる。
	if err := res.rt.Stop(lease.Context()); err != nil && workspaceAlive(res.rt.State(r.Context())) {
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
	if err := removeAllContext(lease.Context(), filepath.Join(a.mgr.rootedDataDir(res.ws), "home", "repos")); err != nil {
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
// "home 掃除" uses the same cleanHome but leaves the container stopped; here we start
// back up so the member lands in a working, freshly-seeded environment.)
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
	// recreate と同じく、Stop 失敗かつまだ生きているなら中断(ライブ bind-mount 配下
	// の削除を避ける)。
	if err := res.rt.Stop(lease.Context()); err != nil && workspaceAlive(res.rt.State(r.Context())) {
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
	if err := cleanHomeContext(lease.Context(), a.mgr.rootedDataDir(res.ws)); err != nil {
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
//   - agentReadyWait (既定 55 秒) keeps the response inside the ingress idle timeout;
//     blocking past it does not deliver a slower success, it delivers a 504.
//   - past that we answer 409 workspace_starting — a code the Console already renders
//     as「起動中です」and a caller can retry — instead of the old 500 whose body was the
//     raw "agent did not become healthy within 15s". The boot keeps going either way.
func (a workspaceAPI) ensureWorkspaceReady(ctx context.Context, res *resolved) *apiError {
	// 期限は **入口で 1 回** 決める。Start 自身も同期猶予ぶん待つので、そこを数えないと
	// 「起動の待ち＋到達の待ち」で合計が ingress の上限を越え、応答ごと消える。
	deadline := time.Now().Add(agentReadyWait())
	if aerr := a.ensureWorkspaceStarted(ctx, res); aerr != nil {
		return aerr
	}
	if res.rt.State(ctx) == "running" {
		return nil // 既に応答している(=印が落ちている)。追加の probe すら要らない。
	}
	if remaining := time.Until(deadline); remaining > 0 {
		err := waitAgentHealthy(ctx, res.rt.Endpoint(), remaining)
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
	return a.ensureWorkspaceStartedRT(ctx, res, rt, unattendedStartEnv)
}

// ensureWorkspaceStartedRT is the shared body, parameterized by the runtime that drives
// this particular start (they differ only in the container env they would apply).
// Serialized per workspace locally (startLockFor) and across CP replicas (owner
// lease): the state check and Start form a check-then-act that explicit starts,
// auto-starts and scheduler wake must not interleave.
func (a workspaceAPI) ensureWorkspaceStartedRT(ctx context.Context, res *resolved, rt Runtime, extraEnv ...string) *apiError {
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

func (a workspaceAPI) ensureWorkspaceStartedRTLocked(ctx context.Context, res *resolved, rt Runtime, lease *workspaceLifecycleLeaseGuard, extraEnv ...string) *apiError {
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
	if _, mounts := rt.(runtimeDocsMounter); mounts {
		if err := stageWorkspaceDocs(a.mgr.rootedDataDir(res.ws)); err != nil {
			log.Printf("stage workspace docs (ws=%s): %v", res.ws.ID, err)
		}
	}
	if err := lease.checkpoint(ctx); err != nil {
		return workspaceLifecycleLeaseError(err)
	}
	// docs/log/81 §4: このコンテナ起動ぶんのプレビュー slug を、コンテナが作られる **前** に
	// 確定させ、URL を env に載せた runtime で起動する。順番が逆だと、アプリが自分の
	// 公開 URL を env から知る作り（Next.js の NEXTAUTH_URL / AUTH_URL / metadataBase）
	// では「URL は出たがアプリは自分の場所を間違えている」状態になる。プレビューが
	// 無効なデプロイ、または slug を用意できなかったときは rt をそのまま使う——
	// プレビューが付かないことは起動を止める理由にならない。
	if armed := a.mgr.armPreviewForStart(ctx, res, extraEnv); armed != nil {
		rt = armed
	}
	if err := rt.Start(ctx); err != nil {
		return internalErr(err)
	}
	if err := lease.checkpoint(ctx); err != nil {
		if f, ok := rt.(runtimeStartFencer); ok {
			abortCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = f.AbortUncommittedStart(abortCtx)
			cancel()
		}
		return workspaceLifecycleLeaseError(err)
	}
	if f, ok := rt.(runtimeStartFencer); ok {
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
	// Driver（docs/log/27 P1.5）: "managed" = 共有 runtime 駆動の paneless セッション。
	// この struct は Agent 応答を decode→再 emit する中継なので、ここに無い field は
	// silently drop される（下の Title の前科と同型）— 載せ忘れると Console の
	// isManagedSession が一生 false になり managed UI が起動しない。DB ミラー
	// （停止中の再配信）には列が無いので載らず、停止中は tui 扱いで表示され、
	// 再開後の次ポーリングで正しい driver に戻る。
	Driver string `json:"driver,omitempty"`
	Dir    string `json:"dir"`
	// Subdir: the folder BENEATH Dir the agent actually runs in ("" = Dir itself).
	// Same relay caveat as the fields around it — absent here, the Console never sees
	// which folder a session was launched in. DB ミラーには列が無いので停止中は載らない。
	Subdir        string `json:"subdir,omitempty"`
	Repo          string `json:"repo"`
	WorkingCopyID string `json:"workingCopyId,omitempty"`
	// Title: the user-supplied display title. Console の displayName は title を最優先
	// で見るが、この struct に無かった頃は中継で silently drop されていた（claude 系は
	// label にも title が埋まるため露見せず、label を使わない shell/ssm だけ表示名が
	// フォールバックに落ちるバグ）。DB ミラー（停止中の再配信）には列が無いので載らない。
	Title   string `json:"title,omitempty"`
	Display string `json:"display"` // human-readable name from the Agent (title → label → repo@time)
	// Color: terminal background hue (hex; SSM sessions carry their host color).
	// この struct に無かった頃は中継で drop され、CP 経由では SSM の背景色が
	// 常に既定色だった。DB ミラーには列が無いので停止中は載らない。
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
	// badge, just silently pin every session to the generic 「BG実行中」.
	BackgroundBusyReason string `json:"backgroundBusyReason,omitempty"`
	// RateLimitResumeAt passes through the Agent's「予約済み自動再開の時刻」(state ==
	// "limited" のときだけ入る RFC3339)。これが落ちると Console のチップは
	// 「制限解除待ち」とだけ言い、いつ動くのかを言えなくなる。DB ミラーには列が無い
	// （停止中のワークスペースに進行中のエピソードは無い）。
	RateLimitResumeAt string `json:"rateLimitResumeAt,omitempty"`
	// Context: claude のコンテキスト残量（ContextBar の元データ）。shape は Agent と
	// Console（chat view）が所有し CP は解釈しないので RawMessage で素通しする。
	// この struct に無かった頃は中継で drop され、CP 経由では ContextBar が出なかった。
	Context json.RawMessage `json:"context,omitempty"`
	// Branch/worktree metadata passed through from the Agent (this struct decodes the
	// Agent's /sessions response and is re-emitted to the Console, so any field absent
	// here is silently dropped). Drives the branch-drift badge and the worktree branch-
	// rename menu. omitempty so the DB-mirror path (stopped workspace) omits them.
	Branch        string `json:"branch,omitempty"`
	CurrentBranch string `json:"currentBranch,omitempty"`
	BranchDrift   bool   `json:"branchDrift,omitempty"`
	Worktree      bool   `json:"worktree,omitempty"`
	// Exit cause of a STOPPED session (docs/log/26): "oom" | "killed" | "crashed"、
	// クリーン終了・意図停止では空。ExitCode は pane の生 wait status（137 = OOM
	// SIGKILL）、ExitSignal は導出シグナル番号。この struct に無かった頃は中継で
	// drop され、docs/log/26 の exit chip（OOM/クラッシュ表示）が CP 経由では一度も
	// 表示されなかった。DB ミラーには列が無いので停止中の再配信には載らない
	// （exit を持つのは停止セッションなので、Agent 停止中は chip も消える点は既知の割り切り）。
	// NOTE: Agent の wire にはこの他に tmux（"claude_"+name、Console 未使用・導出可能）
	// があるが、これは意図して中継しない。新 field を Agent 側 wire（internal/session
	// の Session）に足すときは、ここへの追加漏れ＝silent drop になることに注意。
	ExitReason string `json:"exitReason,omitempty"`
	ExitCode   int    `json:"exitCode,omitempty"`
	ExitSignal int    `json:"exitSignal,omitempty"`
	// KeepAwakeUntil: 利用者が「アイドル自動停止から守れ」と宣言した期限（RFC3339・
	// docs/log/75）。この struct に無いと silent drop で、押したピンが reaper に届かない
	// ＝「押したのに止まった」になる。DB ミラーには列を作らない — 停止中の Workspace に
	// 守るべき走行中のジョブは無い（次に起きたとき Agent が改めて申告する）。
	KeepAwakeUntil string `json:"keepAwakeUntil,omitempty"`
	// Carried は「畳まれたときに答えを待っていた対話」の種類（docs/log/75 §75.6.5）。
	// 中継漏れは silent drop なので、この struct と DB ミラーの**両方**に要る:
	// 稼働中は Agent 由来のこの値、停止中は ReplaceSessions で焼いた列が一覧を作る。
	// 片方だけだと「Workspace を止めた瞬間にバッジが消える」形の嘘になる。
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
			rows := make([]SessionRow, 0, len(list))
			for _, s := range list {
				state := "stopped"
				if s.Alive {
					state = "running"
				}
				rows = append(rows, SessionRow{
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
			// 停止中でも「答えを待っている質問がある」ことは出す（docs/log/75 §75.6.5）。
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

// attention は「人が今この Console を触っている」という 1 ビット（docs/log/75 P3）。
//
// なぜ要るか: 在席の判定を端末の打鍵に絞った結果、**打鍵も送信もせずミラーで過去ログを
// 読み続けている人**が不在に見える。読んでいる最中に Workspace が止まると、Agent ごと
// 落ちるので転写すら取れなくなる（ミラーは「停止中は履歴を取得できません」に変わる）。
//
// ★「タブが開いている」ではなく「人が操作した」を送る。Console は document が可視の
// あいだの実操作（pointerdown / keydown / wheel）を 60 秒に 1 回だけここへ投げる。
// 開きっぱなしのタブは何も送らないので、P3 が消した「ソケットがあるだけで温まる」は
// 戻らない。
//
// ★auto-start は通さない。ここを通すと、停止した Workspace のタブを開いて画面を
// クリックしただけでコンテナが起き上がる（端末アタッチが auto-start しないのと同じ理屈）。
// 起こすのは利用者の明示操作だけ。
func (a workspaceAPI) attention(w http.ResponseWriter, r *http.Request, res *resolved) {
	// 停止処理と競合したときの 409 は無視してよい: 在席の記録が 1 回落ちるだけで、
	// 次の操作でまた届く。呼び出し側（ビーコン）も応答を見ない。
	_ = a.mgr.touchWorkspace(r.Context(), res.ws.ID)
	w.WriteHeader(http.StatusNoContent)
}

// sessionCarriedAnswer answers a carried interaction (docs/log/75): the question / plan /
// permission that was on screen when the session was folded away. Like sessionStart it
// auto-starts a cold workspace first — this is the ONE write whose whole point is to
// reach a workspace the idle-stop reaper turned off, and the user pressing 回答して再開
// in the Console is exactly the deliberate "I am using this now" action auto-start is for.
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
