// workspace_lifecycle.go — ワークスペースレコードのライフサイクルとワークスペース単位の環境変数導出。
// manager.go からの機械的分割（docs/23 P2-W2）。
package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

// createWorkspaceMu serializes port allocation (MaxAgentPort+1) と CreateWorkspace
// の check-then-act。これが無いと並行 create が同一ポートを二重割当する(TOCTOU)。
// プロセス内直列化で足りる(CP は単一プロセス)。DB 一意制約による恒久対策は残課題。
var createWorkspaceMu sync.Mutex

// createWorkspace allocates a new workspace record for a membership. An existing
// container of the conventional name has its port/AGENT_TOKEN adopted; otherwise
// a fresh port (DB max+1, floored at portBase) and token are minted.
func (m *manager) createWorkspace(ctx context.Context, mv MembershipView, userKey string) (Workspace, error) {
	createWorkspaceMu.Lock()
	defer createWorkspaceMu.Unlock()
	name, network, dataDir := m.workspaceNames(mv.TenantSlug, userKey)
	port := dockerPublishedPort(name)
	if port == "" {
		mx, err := m.store.MaxAgentPort(ctx)
		if err != nil {
			return Workspace{}, err
		}
		next := m.portBase
		if mx+1 > next {
			next = mx + 1
		}
		port = strconv.Itoa(next)
	}
	token := dockerEnvValue(name, "AGENT_TOKEN")
	if token == "" {
		token = randHex(24)
	}
	ws := Workspace{
		ID:            newID(),
		TenantID:      mv.TenantID,
		MembershipID:  mv.MembershipID,
		ContainerName: name,
		Network:       network,
		DataDir:       dataDir,
		AgentPort:     port,
		AgentToken:    token,
		State:         "stopped",
		CreatedAt:     nowTS(),
	}
	if err := m.store.CreateWorkspace(ctx, ws); err != nil {
		return Workspace{}, err
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
			mv := MembershipView{MembershipID: mem.ID, TenantID: t.ID, TenantSlug: t.Slug, TenantName: t.Name, Role: mem.Role}
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
	if err := lease.checkpoint(ctx); err != nil {
		return err
	}
	if err := m.runtimeFor(ws, "").Stop(lease.Context()); err != nil {
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
	if err := lease.checkpoint(ctx); err != nil {
		return err
	}
	_ = m.runtimeFor(ws, "").Stop(lease.Context()) // best-effort
	if err := lease.checkpoint(ctx); err != nil {
		return err
	}
	if err := cleanHomeContext(lease.Context(), m.rootedDataDir(ws)); err != nil {
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
const unattendedStartEnv = "AF_AGENT_SELF_UPDATE_SKIP=1"

// runtimeForUnattended rebuilds a workspace's Runtime so its NEXT container start
// carries unattendedStartEnv. Mirrors the construction in resolveByMembership (the
// per-workspace env is fixed at `docker run` time, so an override has to be in place
// before Start). The result is intentionally NOT written to the runtime cache: only
// this one start differs, and every later call (state/exec/endpoint) is unaffected by
// container env.
func (m *manager) runtimeForUnattended(ctx context.Context, res *resolved) (Runtime, error) {
	dekHex, err := m.resolveDEK(ctx, res.ws, res.ident.UserKey)
	if err != nil {
		return nil, err
	}
	ws := res.ws
	ws.MemBytes = m.resolveWorkspaceMemBytes(ctx, ws)
	env := append(m.workspaceExtraEnv(ctx, ws), unattendedStartEnv)
	return m.runtimeFor(ws, dekHex, env...), nil
}

func (m *manager) workspaceExtraEnv(ctx context.Context, ws Workspace) []string {
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
			// Schedule bridge (docs/38 P3): separate per-membership token so the
			// operator MCP can drive /internal/schedules over the same hairpin. A
			// distinct credential from the memo token — a leak is scoped to schedules.
			"AF_SCHEDULE_TOKEN="+mintScheduleToken(scheduleSignKey(m.tokenSignMaster()), ws.MembershipID),
			// MCP registry bridge (docs/48 P4): the agent polls /internal/mcp-servers for
			// this tenant's distributed MCP definitions. Its own credential, because the
			// response can carry tenant secrets (a user_secret=0 server's headers) — a leak
			// must not also grant memo/schedule access, and vice versa.
			"AF_MCP_TOKEN="+mintMCPToken(mcpSignKey(m.tokenSignMaster()), ws.MembershipID))
	}
	return env
}

// memFloorBytes is the smallest RAM a workspace may be sized to. An explicit per-user
// value below this is raised to it so a fat-finger can't starve a container into an
// unusable state; an unset value (0) is untouched and falls back to the default.
const memFloorBytes = 256 * mib

// resolveWorkspaceMemBytes returns the RAM cap (bytes) for ws's NEXT container start,
// or 0 to mean "use the runtime's deployment default" (WS_MEMORY / AF_ECS_TASK_MEMORY).
// A tenant_admin's per-user mem_limit is honored but clamped to the per-tenant cap
// (tenantLimits.MaxWorkspaceMem) and the deployment hard ceiling (memMaxBytes), then
// floored — so the shared host stays protected regardless of what was entered. Unset
// per-user (0) returns 0 so the operator's default applies unchanged. Best-effort: a
// store error falls back to the default (0) rather than guessing a value.
func (m *manager) resolveWorkspaceMemBytes(ctx context.Context, ws Workspace) int64 {
	ul, ok, err := m.store.GetUserLimit(ctx, ws.MembershipID)
	if err != nil || !ok || ul.MemLimit <= 0 {
		return 0 // unset → deployment default
	}
	v := ul.MemLimit
	if t, err := m.store.GetTenant(ctx, ws.TenantID); err == nil {
		if cap := parseLimits(t.Limits).MaxWorkspaceMem; cap > 0 && v > cap {
			v = cap
		}
	}
	if m.memMaxBytes > 0 && v > m.memMaxBytes {
		v = m.memMaxBytes
	}
	if v < memFloorBytes {
		v = memFloorBytes
	}
	return v
}
