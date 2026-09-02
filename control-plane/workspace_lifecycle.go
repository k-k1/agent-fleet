// workspace_lifecycle.go — ワークスペースレコードのライフサイクルとワークスペース単位の環境変数導出。
// manager.go からの機械的分割（docs/log/23 P2-W2）。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// createWorkspaceMu serializes port allocation (MaxAgentPort+1) と CreateWorkspace
// の check-then-act。これが無いと並行 create が同一ポートを二重割当する(TOCTOU)。
// プロセス内直列化で足りる(CP は単一プロセス)。DB 一意制約による恒久対策は残課題。
var createWorkspaceMu sync.Mutex

// createWorkspace allocates a new workspace record for a membership. An existing
// container of the conventional name has its port/AGENT_TOKEN adopted; otherwise
// a fresh port (DB max+1, floored at portBase) and token are minted.
func (m *manager) createWorkspace(ctx context.Context, mv store.MembershipView, userKey string) (store.Workspace, error) {
	createWorkspaceMu.Lock()
	defer createWorkspaceMu.Unlock()
	name, network, dataDir := m.workspaceNames(mv.TenantSlug, userKey)
	port := runtime.DockerPublishedPort(name)
	if port == "" {
		mx, err := m.store.MaxAgentPort(ctx)
		if err != nil {
			return store.Workspace{}, err
		}
		next := m.portBase
		if mx+1 > next {
			next = mx + 1
		}
		port = strconv.Itoa(next)
	}
	token := runtime.DockerEnvValue(name, "AGENT_TOKEN")
	if token == "" {
		token = randHex(24)
	}
	ws := store.Workspace{
		ID:            store.NewID(),
		TenantID:      mv.TenantID,
		TenantSlug:    mv.TenantSlug, // not persisted; every later read joins it back
		MembershipID:  mv.MembershipID,
		ContainerName: name,
		Network:       network,
		DataDir:       dataDir,
		AgentPort:     port,
		AgentToken:    token,
		State:         "stopped",
		CreatedAt:     store.NowTS(),
	}
	if err := m.store.CreateWorkspace(ctx, ws); err != nil {
		return store.Workspace{}, err
	}
	return ws, nil
}

// backfill records existing on-disk default-tenant users into the store on boot
// (docs/13 S3). Each top-level <dataRoot>/<key>/home is one default-tenant user;
// non-default tenants nest under <dataRoot>/<slug>/<key> and are created on demand.
func (m *manager) backfill(ctx context.Context) error {
	entries, err := os.ReadDir(m.dataRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	t, err := m.store.EnsureDefaultTenant(ctx)
	if err != nil {
		return err
	}
	m.defaultTenantID = t.ID
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		key := e.Name()
		if _, err := os.Stat(filepath.Join(m.dataRoot, key, "home")); err != nil {
			continue // tenant dir or non-user layout; skip
		}
		ident, err := m.store.UpsertIdentity(ctx, "", key, "")
		if err != nil {
			return err
		}
		mem, err := m.store.EnsureMembership(ctx, ident.ID, t.ID, "member")
		if err != nil {
			return err
		}
		if _, ok, err := m.store.GetWorkspaceByMembership(ctx, mem.ID); err != nil {
			return err
		} else if !ok {
			mv := store.MembershipView{MembershipID: mem.ID, TenantID: t.ID, TenantSlug: t.Slug, TenantName: t.Name, Role: mem.Role}
			if _, err := m.createWorkspace(ctx, mv, key); err != nil {
				return err
			}
		}
	}
	return nil
}

// countRunningInTenant counts running Workspace containers in a tenant via the
// runtime (authoritative — independent of the DB state column). "starting" counts
// too: an ECS task mid-pull already occupies a workspace slot, and excluding it
// would let a tenant burst past max_workspaces during cold starts.
func (m *manager) countRunningInTenant(ctx context.Context, tenantID string) (int, error) {
	wss, err := m.store.ListWorkspaces(ctx, tenantID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, ws := range wss {
		switch m.runtimeFor(ws, "").State(ctx) {
		case "running", "starting":
			n++
		}
	}
	return n, nil
}

// workspaceStateByMembership returns a membership's container name + live docker
// state ("none" if no workspace record) for the admin UI.
func (m *manager) workspaceStateByMembership(ctx context.Context, membershipID string) (string, string) {
	ws, ok, err := m.store.GetWorkspaceByMembership(ctx, membershipID)
	if err != nil || !ok {
		return "", "none"
	}
	return ws.ContainerName, m.runtimeFor(ws, "").State(ctx)
}

// stopWorkspaceByMembership force-stops a member's workspace (admin action).
func (m *manager) stopWorkspaceByMembership(ctx context.Context, membershipID string) error {
	ws, ok, err := m.store.GetWorkspaceByMembership(ctx, membershipID)
	if err != nil || !ok {
		return err
	}
	lock := m.startLockFor(ws.ID)
	lock.Lock()
	defer lock.Unlock()
	lease, err := acquireWorkspaceLifecycleLease(ctx, m.store, membershipID)
	if err != nil {
		return err
	}
	defer lease.Close()
	rt := m.runtimeFor(ws, "")
	releaseFence, err := m.acquireWorkspaceOperationFence(lease.Context(), ws.ID, rt)
	if err != nil {
		return err
	}
	defer releaseFence()
	if err := lease.checkpoint(ctx); err != nil {
		return err
	}
	if err := rt.Stop(lease.Context()); err != nil {
		return err
	}
	if err := lease.checkpoint(ctx); err != nil {
		return err
	}
	return m.store.SetWorkspaceState(ctx, ws.ID, "stopped")
}

// cleanHomeByMembership wipes a member's workspace home except auth/connection state
// (admin action). Stops the container first and leaves it stopped; the home is
// recreated on the next start.
func (m *manager) cleanHomeByMembership(ctx context.Context, membershipID string) error {
	ws, ok, err := m.store.GetWorkspaceByMembership(ctx, membershipID)
	if err != nil || !ok {
		return err
	}
	lock := m.startLockFor(ws.ID)
	lock.Lock()
	defer lock.Unlock()
	lease, err := acquireWorkspaceLifecycleLease(ctx, m.store, membershipID)
	if err != nil {
		return err
	}
	defer lease.Close()
	rt := m.runtimeFor(ws, "")
	releaseFence, err := m.acquireWorkspaceOperationFence(lease.Context(), ws.ID, rt)
	if err != nil {
		return err
	}
	defer releaseFence()
	if err := lease.checkpoint(ctx); err != nil {
		return err
	}
	_ = rt.Stop(lease.Context()) // best-effort
	if err := lease.checkpoint(ctx); err != nil {
		return err
	}
	if err := runtime.CleanHomeContext(lease.Context(), m.rootedDataDir(ws)); err != nil {
		return err
	}
	if err := lease.checkpoint(ctx); err != nil {
		return err
	}
	return m.store.SetWorkspaceState(ctx, ws.ID, "stopped")
}

// workspaceExtraEnv derives per-workspace container env from the workspace's tenant
// policy — currently just the agent self-update gate (allow_agent_self_update):
// when the tenant allows it, inject AF_AGENT_SELF_UPDATE_ALLOWED=1 so the entrypoint
// honors a member's opt-in. Resolved when the runtime is built (cached per
// membership); an admin limits edit evicts the tenant's cache (evictTenantCache) so
// a policy change reaches the next container start. Best-effort: on a lookup error
// we inject nothing (safe default = pinned).
// unattendedStartEnv marks a container start that nobody is watching (currently only
// the scheduler's wake). The entrypoint skips its agent CLI self-update for that boot
// and runs the already-installed versions. Deliberately NOT "AF_AGENT_SELF_UPDATE=0":
// that is the member's opt-in being OFF, whose entrypoint branch also TEARS DOWN the
// ~/.local shadow to fall back to the baked pin — on a baked image an unattended start
// would then uninstall ~1.3GB of CLIs that the next interactive start reinstalls. This
// is a per-boot skip, not a policy change.
// リテラルは docker アダプタ（internal/runtime/deps.go）が持つ。コンテナの env から
// 読み戻して health の猶予を決めるので、この文字列は workspace イメージの entrypoint との
// 契約であり、2 通りの綴りを持つと黙って腐る。

// runtimeForUnattended rebuilds a workspace's Runtime so its NEXT container start
// carries unattendedStartEnv. Mirrors the construction in resolveByMembership (the
// per-workspace env is fixed at `docker run` time, so an override has to be in place
// before Start). The result is intentionally NOT written to the runtime cache: only
// this one start differs, and every later call (state/exec/endpoint) is unaffected by
// container env.
func (m *manager) runtimeForUnattended(ctx context.Context, res *resolved) (runtime.Runtime, error) {
	dekHex, err := m.resolveDEK(ctx, res.ws, res.ident.UserKey)
	if err != nil {
		return nil, err
	}
	ws := res.ws
	ws.MemBytes, ws.CPUUnits, ws.DiskGB = m.resolveWorkspaceSize(ctx, ws)
	env := append(m.workspaceExtraEnv(ctx, ws), runtime.UnattendedStartEnv)
	return m.runtimeFor(ws, dekHex, env...), nil
}

// armPreviewForStart mints the preview slug for the container start that is about to
// happen and returns a runtime whose env names the URLs it will answer on
// (docs/log/81 §4 + §8). nil = nothing to do (previews off for this deployment) or the
// slug could not be minted — the caller then starts with the runtime it already had.
//
// ★ なぜ runtime を作り直すのか: コンテナ env は起動の瞬間に確定し、buildResolved が
// 組んだ runtime はこの起動の slug をまだ知らない。「URL は発行したが、コンテナの中の
// アプリはそれを知らない」は、Next.js のように自分の公開 URL を env から読む作りでは
// 半分壊れた状態そのものになる。
func (m *manager) armPreviewForStart(ctx context.Context, res *resolved, extraEnv []string) runtime.Runtime {
	if m.previewDomain == "" {
		return nil
	}
	slug, err := m.rotatePreviewSlug(ctx, res.ws)
	if err != nil {
		log.Printf("preview slug for ws %s: %v (starting without preview URLs)", res.ws.ID, err)
		return nil
	}
	dekHex, err := m.resolveDEK(ctx, res.ws, res.ident.UserKey)
	if err != nil {
		log.Printf("preview arm: resolve DEK for ws %s: %v (starting without preview URLs)", res.ws.ID, err)
		return nil
	}
	ws := res.ws
	ws.PreviewSlug = slug
	ws.MemBytes, ws.CPUUnits, ws.DiskGB = m.resolveWorkspaceSize(ctx, ws)
	ws.SlotClass, _ = m.resolveSlotClass(ctx, ws)
	return m.runtimeFor(ws, dekHex, append(m.workspaceExtraEnv(ctx, ws), extraEnv...)...)
}

// rotatePreviewSlug decides which slug THIS start runs under and persists it.
//
//   - 既定は毎回引き直す（要件そのもの）。前回の URL はこの瞬間に死ぬ。
//   - PreviewFixedSlug が ON の Workspace だけは、設定に予約した slug を使い回す
//     （docs/log/81 §4.1）。外部 IdP の redirect URI 登録が前方一致もワイルドカードも
//     受け付けないため、これが無いと NextAuth / Auth.js の構成が成立しない。
//   - ★ 公開モードは起動のたびに必ず OFF へ戻す（fail-closed・決定 12）。この機能の
//     事故は「公開のままにしていたのを忘れる」以外にほぼ無い。
func (m *manager) rotatePreviewSlug(ctx context.Context, ws store.Workspace) (string, error) {
	raw, _ := m.store.GetWorkspaceSettings(ctx, ws.ID)
	st := parseWSSettings(raw)
	dirty := false
	if st.PreviewPublic {
		st.PreviewPublic = false
		dirty = true
	}
	slug := st.PreviewReservedSlug
	if !st.PreviewFixedSlug || slug == "" {
		fresh, err := newPreviewSlug()
		if err != nil {
			return "", err
		}
		slug = fresh
		if st.PreviewFixedSlug {
			st.PreviewReservedSlug = slug // 次の起動からは同じ URL に戻る
			dirty = true
		} else if st.PreviewReservedSlug != "" {
			st.PreviewReservedSlug = "" // 固定を解除したら予約も捨てる
			dirty = true
		}
	}
	if dirty {
		if out, err := json.Marshal(st); err == nil {
			if err := m.store.SetWorkspaceSettings(ctx, ws.ID, string(out)); err != nil {
				return "", err
			}
		}
	}
	if err := m.store.SetWorkspacePreviewSlug(ctx, ws.ID, slug); err != nil {
		return "", err
	}
	return slug, nil
}

func (m *manager) workspaceExtraEnv(ctx context.Context, ws store.Workspace) []string {
	t, err := m.store.GetTenant(ctx, ws.TenantID)
	if err != nil {
		return nil
	}
	allowUpd := parseLimits(t.Limits).AllowAgentSelfUpdate
	var env []string
	if allowUpd {
		env = append(env, "AF_AGENT_SELF_UPDATE_ALLOWED=1")
	}
	// Per-workspace member settings (CP DB) → container env. Add new mappings here as
	// settings grow; each is a DB-backed value editable while the container is stopped.
	raw, _ := m.store.GetWorkspaceSettings(ctx, ws.ID)
	st := parseWSSettings(raw)
	if allowUpd && st.AgentUpdate {
		env = append(env, "AF_AGENT_SELF_UPDATE=1")
	}
	if days := parseLimits(t.Limits).TerminalHistoryRetentionDays; days > 0 {
		env = append(env, "AF_TERMINAL_HISTORY_RETENTION_DAYS="+strconv.Itoa(days))
	}
	// プレビュー用サブドメイン（docs/log/81 §8）。★ これは飾りではない: URL は起動ごとに
	// 変わるので、自分の公開 URL を env から知る作り（Next.js の NEXTAUTH_URL /
	// AUTH_URL / metadataBase、Spring の app.base-url）に、正しい値を渡せる場所が
	// ここしか無い。無いと「URL は出たがアプリが自分の場所を間違える」になる。
	// ws.PreviewSlug が入るのは起動直前の armPreviewForStart 経由の呼び出しだけで、
	// 通常の解決（buildResolved）では空 = 何も注入しない。
	if m.previewDomain != "" && ws.PreviewSlug != "" {
		ports := previewPortsOf(st)
		strs := make([]string, 0, len(ports))
		for _, p := range ports {
			strs = append(strs, strconv.Itoa(p))
			env = append(env, "AF_PREVIEW_URL_"+strconv.Itoa(p)+"="+previewURLFor(ws.PreviewSlug, p, m.previewDomain))
		}
		env = append(env,
			"AF_PREVIEW_DOMAIN="+m.previewDomain,
			"AF_PREVIEW_SLUG="+ws.PreviewSlug,
			"AF_PREVIEW_PORTS="+strings.Join(strs, ","))
	}
	// Internal git provider: inject the host + this membership's deterministic git
	// token so the Agent seeds its cred store (secrets.go seedInternalGit) and
	// clone/push authenticate transparently. Deterministic, so re-injection on
	// every start is idempotent. Skipped when PUBLIC_BASE_URL is unset.
	if m.internalGitHost != "" && ws.MembershipID != "" {
		token := mintGitToken(gitSignKey(m.tokenSignMaster()), ws.MembershipID)
		env = append(env,
			"AF_INTERNAL_GIT_HOST="+m.internalGitHost,
			"AF_INTERNAL_GIT_TOKEN="+token)
	}
	// Memo bridge: inject the CP public base + this membership's memo token so the
	// in-container フリート・オペレーター can read/write the memo queue over the public
	// hairpin (memo_bridge.go). Deterministic token → idempotent re-injection. Requires
	// PUBLIC_BASE_URL (else the bridge is unreachable, so we inject nothing).
	if m.publicBaseURL != "" && ws.MembershipID != "" {
		env = append(env,
			"AF_CP_BASE_URL="+m.publicBaseURL,
			"AF_MEMO_TOKEN="+mintMemoToken(memoSignKey(m.tokenSignMaster()), ws.MembershipID),
			// Schedule bridge (docs/log/38 P3): separate per-membership token so the
			// operator MCP can drive /internal/schedules over the same hairpin. A
			// distinct credential from the memo token — a leak is scoped to schedules.
			"AF_SCHEDULE_TOKEN="+mintScheduleToken(scheduleSignKey(m.tokenSignMaster()), ws.MembershipID),
			// MCP registry bridge (docs/log/48 P4): the agent polls /internal/mcp-servers for
			// this tenant's distributed MCP definitions. Its own credential, because the
			// response can carry tenant secrets (a user_secret=0 server's headers) — a leak
			// must not also grant memo/schedule access, and vice versa.
			"AF_MCP_TOKEN="+mintMCPToken(mcpSignKey(m.tokenSignMaster()), ws.MembershipID),
			// Docs bridge (docs/build/04 §4.9): the agent pulls its role-scoped docs subset
			// when nothing was bind-mounted — i.e. on ECS, where no host path exists to
			// mount. Its own credential for the same reason as the others; a leak reads
			// this member's docs subset and nothing else.
			"AF_DOCS_TOKEN="+mintDocsToken(docsSignKey(m.tokenSignMaster()), ws.MembershipID),
			// Git OAuth refresh bridge (docs/log/71 §71.8): the agent posts its Bitbucket
			// refresh token here so the CP can add the TENANT's client secret, which
			// therefore never reaches the container. Its own credential for the same
			// reason as the others; a leak refreshes this member's git token and
			// nothing else.
			"AF_GIT_OAUTH_TOKEN="+mintGitOAuthToken(gitOAuthSignKey(m.tokenSignMaster()), ws.MembershipID))
	}
	return env
}

// memFloorBytes is the smallest RAM a workspace may be sized to. An explicit per-user
// value below this is raised to it so a fat-finger can't starve a container into an
// unusable state; an unset value (0) is untouched and falls back to the default.
const memFloorBytes = 256 * mib

// resolveWorkspaceSize returns the three RESOLVED size axes for ws's NEXT container
// start — RAM (bytes), CPU (Fargate units, 1024 = 1 vCPU) and working disk (GiB).
// 0 on an axis means "use the runtime's deployment default", so an operator who never
// set a per-user value keeps exactly the behaviour they configured.
//
// A tenant_admin's per-user values are honored but clamped to the per-tenant caps
// (tenantLimits.MaxWorkspace*) and, for memory, to the deployment hard ceiling
// (memMaxBytes) and floor — so the shared host stays protected regardless of what was
// entered. Best-effort: a store error falls back to the defaults rather than guessing.
//
// The axes are resolved together because they are read from the same two rows; the
// runtime factory then maps them per backend (docker --memory/--cpus, ECS task size +
// ephemeral storage). Fargate's valid (cpu, memory) pairs are enforced later by
// fargateSize, not here — this function is runtime-neutral (ADR 0044 決定 1).
func (m *manager) resolveWorkspaceSize(ctx context.Context, ws store.Workspace) (memBytes int64, cpuUnits, diskGB int) {
	ul, ok, err := m.store.GetUserLimit(ctx, ws.MembershipID)
	if err != nil || !ok {
		return 0, 0, 0
	}
	var lim tenantLimits
	if t, err := m.store.GetTenant(ctx, ws.TenantID); err == nil {
		lim = parseLimits(t.Limits)
	}
	if ul.MemLimit > 0 {
		memBytes = ul.MemLimit
		if lim.MaxWorkspaceMem > 0 && memBytes > lim.MaxWorkspaceMem {
			memBytes = lim.MaxWorkspaceMem
		}
		if m.memMaxBytes > 0 && memBytes > m.memMaxBytes {
			memBytes = m.memMaxBytes
		}
		if memBytes < memFloorBytes {
			memBytes = memFloorBytes
		}
	}
	if ul.CPULimit > 0 {
		cpuUnits = ul.CPULimit
		if lim.MaxWorkspaceCPU > 0 && cpuUnits > lim.MaxWorkspaceCPU {
			cpuUnits = lim.MaxWorkspaceCPU
		}
	}
	if ul.DiskGB > 0 {
		diskGB = ul.DiskGB
		if lim.MaxWorkspaceDiskGB > 0 && diskGB > lim.MaxWorkspaceDiskGB {
			diskGB = lim.MaxWorkspaceDiskGB
		}
	}
	return memBytes, cpuUnits, diskGB
}

// resolveSlotClass returns the machine class ws's NEXT container start lands on, and
// a note when the stored answer could not be honoured (docs/log/70 §70.4.3).
//
// The chain is user → tenant default → deployment default, and each candidate must be
// both DECLARED by the deployment and ALLOWED by the tenant. "" is returned on every
// runtime that has no classes, which is every runtime but ecs-ec2 and every ecs-ec2
// deployment that declared a single unnamed ladder — i.e. this is inert until an
// operator opts in, and nothing about an existing deployment changes.
//
// ⚠️ The note exists because there is nothing to clamp TO. An out-of-range memory
// value has a smaller version; a class that is not available has no "nearer" class,
// so the person silently runs somewhere they did not ask for. That is invisible until
// the bill arrives, which is why the substitution is reported rather than just done.
func (m *manager) resolveSlotClass(ctx context.Context, ws store.Workspace) (id, note string) {
	p := m.workspaceSizing()
	if len(p.SlotClasses) == 0 {
		return "", ""
	}
	declared := map[string]bool{}
	for _, c := range p.SlotClasses {
		declared[c.ID] = true
	}
	var lim tenantLimits
	if t, err := m.store.GetTenant(ctx, ws.TenantID); err == nil {
		lim = parseLimits(t.Limits)
	}
	ok := func(id string) bool {
		if id == "" || !declared[id] {
			return false
		}
		if len(lim.AllowedSlotClasses) == 0 {
			return true
		}
		return slices.Contains(lim.AllowedSlotClasses, id)
	}
	// The fallback the tenant lands on when the per-user value cannot be used: the
	// tenant's own default, else the deployment's, else the first class the tenant is
	// allowed at all. The last step matters — a super_admin can restrict a tenant to a
	// set that excludes the deployment default, and "no usable class" must not become
	// a failed Start.
	fallback := p.DefaultSlotClass
	if ok(lim.SlotClass) {
		fallback = lim.SlotClass
	} else if !ok(fallback) {
		fallback = ""
		for _, c := range p.SlotClasses {
			if ok(c.ID) {
				fallback = c.ID
				break
			}
		}
		if fallback == "" {
			fallback = p.DefaultSlotClass // the operator's list wins over a tenant restriction that leaves nothing
		}
	}
	ul, found, err := m.store.GetUserLimit(ctx, ws.MembershipID)
	if err != nil || !found || ul.SlotClass == "" {
		return fallback, ""
	}
	if ok(ul.SlotClass) {
		return ul.SlotClass, ""
	}
	return fallback, fmt.Sprintf("slot class %q is not available here; using %q", ul.SlotClass, fallback)
}

// resolveWorkspaceMemBytes is the memory axis alone, kept because the admin API and
// MCP echo the post-clamp memory value back to the caller.
func (m *manager) resolveWorkspaceMemBytes(ctx context.Context, ws store.Workspace) int64 {
	b, _, _ := m.resolveWorkspaceSize(ctx, ws)
	return b
}

// destroyWorkspaceByMembership is the irreversible half of ADR 0045 決定 13: it tears
// down the runtime's resources (home, and on the cloud adapters the ECS service, EFS
// access points, SSM secrets, EBS volume and any hibernation snapshot) and then removes
// the DB row. It returns the resources the adapter could NOT remove, for the audit log.
//
// Only reachable from an explicit administrator action. In particular it does NOT run on
// offboarding: removeMembership is a logical delete that keeps the home on purpose
// (docs/log/61 §61.10.6), and the automatic sweep hibernates rather than destroys.
//
// ⚠️ This cannot honour the deletion locks of ADR 0028. They live inside the home
// (~/.config/agent-fleet/), which is unreadable while the workspace is stopped — a
// structural consequence of where the locks are kept, not an omission. Callers must
// surface "this overrides deletion locks" in the UI.
//
// Order: runtime first, DB row last. The reverse would turn any runtime failure into an
// orphaned cloud resource with nothing in the database pointing at it — exactly the leak
// this operation exists to close. Every adapter's Destroy is idempotent, so the retry
// after a partial failure is safe.
func (m *manager) destroyWorkspaceByMembership(ctx context.Context, membershipID string) ([]string, error) {
	ws, ok, err := m.store.GetWorkspaceByMembership(ctx, membershipID)
	if err != nil || !ok {
		return nil, err
	}
	lock := m.startLockFor(ws.ID)
	lock.Lock()
	defer lock.Unlock()
	lease, err := acquireWorkspaceLifecycleLease(ctx, m.store, membershipID)
	if err != nil {
		return nil, err
	}
	defer lease.Close()
	rt := m.runtimeFor(ws, "")
	releaseFence, err := m.acquireWorkspaceOperationFence(lease.Context(), ws.ID, rt)
	if err != nil {
		return nil, err
	}
	defer releaseFence()
	if err := lease.checkpoint(ctx); err != nil {
		return nil, err
	}
	leftovers, err := runtime.DestroyRuntime(lease.Context(), rt)
	if err != nil {
		return nil, err
	}
	if err := lease.checkpoint(ctx); err != nil {
		return leftovers, err
	}
	if err := m.store.DeleteWorkspace(ctx, ws.ID); err != nil {
		return leftovers, err
	}
	m.evictMembershipCache(membershipID)
	return leftovers, nil
}

// runtimePoolStatuser is implemented by the one adapter that has a POOL to report on.
// Everything else in the product is per-workspace, so there is nothing to show — and the
// admin UI hides the screen rather than showing an empty one.
type runtimePoolStatuser interface {
	PoolStatus(context.Context) (runtime.EC2PoolStatus, error)
}

// poolStatus reports the EC2 slot pool, or ok=false on every other runtime profile.
func (m *manager) poolStatus(ctx context.Context) (runtime.EC2PoolStatus, bool, error) {
	p, ok := m.rtFactory.(runtimePoolStatuser)
	if !ok {
		return runtime.EC2PoolStatus{}, false, nil
	}
	st, err := p.PoolStatus(ctx)
	st.AutoBake = m.autoBakeGolden
	// Σ(tenant max_workspaces) against the cap. It belongs HERE rather than in the
	// adapter for the same reason AutoBake does: the adapter has no database (ADR 0012),
	// and this comparison is half quota rows and half deployment env. Only attached when
	// it says something — a budget that fits is not news, and the screen already shows
	// both numbers it is made of.
	if b, ok, e := m.poolBudget(ctx, "", 0); ok && e == nil && !b.OK() {
		st.Budget = &b
	}
	// With the baker switched off, "nothing is being baked" is not a phase of a bake —
	// it is the whole answer, and the pool being full is not what is stopping it. A
	// bake already in flight (an operator who switched it off mid-round) keeps its real
	// phase: those resources exist and somebody has to be told about them.
	if !st.AutoBake {
		for i := range st.Goldens {
			switch st.Goldens[i].Phase {
			case runtime.EC2BakePhaseIdle, runtime.EC2BakePhaseBlocked:
				st.Goldens[i].Phase, st.Goldens[i].SlotsInUse = runtime.EC2BakePhaseOff, 0
			}
		}
	}
	return st, true, err
}
