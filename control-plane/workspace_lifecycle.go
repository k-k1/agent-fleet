// workspace_lifecycle.go — ワークスペースレコードのライフサイクルとワークスペース単位の環境変数導出。
// manager.go からの機械的分割（docs/23 P2-W2）。
package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
)

// createWorkspace allocates a new workspace record for a membership. An existing
// container of the conventional name has its port/AGENT_TOKEN adopted; otherwise
// a fresh port (DB max+1, floored at portBase) and token are minted.
func (m *manager) createWorkspace(ctx context.Context, mv MembershipView, userKey string) (Workspace, error) {
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
	if err := m.runtimeFor(ws, "").Stop(ctx); err != nil {
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
	_ = m.runtimeFor(ws, "").Stop(ctx) // best-effort
	if err := cleanHome(m.rootedDataDir(ws)); err != nil {
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
	// Internal git provider: inject the host + this membership's deterministic git
	// token so the Agent seeds its cred store (secrets.go seedInternalGit) and
	// clone/push authenticate transparently. Deterministic, so re-injection on
	// every start is idempotent. Skipped when PUBLIC_BASE_URL is unset.
	if m.internalGitHost != "" && ws.MembershipID != "" {
		token := mintGitToken(gitSignKey(m.master32), ws.MembershipID)
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
			"AF_MEMO_TOKEN="+mintMemoToken(memoSignKey(m.master32), ws.MembershipID))
	}
	return env
}
