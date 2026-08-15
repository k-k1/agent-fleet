// limits.go — テナント limits JSON のパースとアイドルタイムアウト解決。
// manager.go からの機械的分割（docs/23 P2-W2）。
package main

import (
	"encoding/json"
	"time"
)

// tenantLimits is the parsed tenant.limits JSON (0 = unlimited for the int
// quotas). The idle timeouts are human-editable duration strings (e.g. "30m"):
// "" => fall back to the deployment default (env); "0" => idle-stop disabled for
// this tenant. See idleTimeout for resolution.
type tenantLimits struct {
	MaxWorkspaces int `json:"max_workspaces"`
	MaxSessions   int `json:"max_sessions"`
	// MaxGitRepos caps the tenant's internal git repositories (docs/reference/
	// internal-git-provider, P2). 0 = unlimited, like the other int quotas.
	MaxGitRepos int `json:"max_git_repos,omitempty"`
	// MaxLFSBytes caps the tenant's total stored Git LFS bytes (P3). 0 = unlimited.
	MaxLFSBytes int64 `json:"max_lfs_bytes,omitempty"`
	// MaxWorkspaceMem caps a single workspace's RAM allocation in BYTES (roadmap P3-4;
	// super_admin-set). A tenant_admin's per-user mem_limit is clamped to this at
	// container start (resolveWorkspaceMemBytes). 0 = no tenant cap (a per-user value is
	// still bounded by the deployment hard ceiling AF_MAX_WORKSPACE_MEM and the floor).
	MaxWorkspaceMem int64 `json:"max_workspace_mem,omitempty"`
	// MaxWorkspaceCPU caps a single workspace's CPU in Fargate CPU units (1024 =
	// 1 vCPU) and MaxWorkspaceDiskGB its working disk in GiB. Same two-stage shape as
	// MaxWorkspaceMem: super_admin sets the tenant ceiling, a tenant_admin's per-user
	// value is clamped to it at container start. 0 = no tenant cap (ADR 0044).
	MaxWorkspaceCPU    int `json:"max_workspace_cpu,omitempty"`
	MaxWorkspaceDiskGB int `json:"max_workspace_disk_gb,omitempty"`
	// P3-9 idle-stop (docs/19): per-tenant, super_admin-editable.
	SessionIdleTimeout string `json:"session_idle_timeout,omitempty"` // tier-1: idle claude -> halt
	WSIdleTimeout      string `json:"ws_idle_timeout,omitempty"`      // tier-2: cold workspace -> docker stop
	// AllowAgentSelfUpdate: the operator gate for member-driven CLI self-update
	// (claude/opencode/codex). When true the CP injects AF_AGENT_SELF_UPDATE_ALLOWED=1
	// so a member who opted in (toolchains.agentUpdate) gets the baked /usr/local CLIs
	// updated to latest IN PLACE at container start; false (default) pins everyone to
	// the image versions. Enforced in the entrypoint (the env gate), so a member
	// editing toolchains.json can't bypass. Stop→Start recreates from the image, so
	// turning the toggle off reverts to the baked versions.
	AllowAgentSelfUpdate bool `json:"allow_agent_self_update,omitempty"`
	// TerminalHistoryRetentionDays promotes the default container-local terminal
	// history into the workspace home volume for this many days. 0 keeps the
	// standard short-lived /tmp history only.
	TerminalHistoryRetentionDays int `json:"terminal_history_retention_days,omitempty"`
}

func parseLimits(s string) tenantLimits {
	var l tenantLimits
	if s != "" {
		_ = json.Unmarshal([]byte(s), &l)
	}
	return l
}

// idleTimeout resolves a tenant idle-timeout field to a duration and whether
// idle-stop is enabled. Empty => the deployment default (def); an explicit "0"
// (or any non-positive value) disables idle-stop for that tenant; a bad string
// falls back to the default rather than silently disabling.
func idleTimeout(tenantVal string, def time.Duration) (d time.Duration, enabled bool) {
	d = def
	if tenantVal != "" {
		if p, err := time.ParseDuration(tenantVal); err == nil {
			d = p
		}
	}
	return d, d > 0
}
