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
	// SlotClass is the tenant's default machine class — which of the deployment's
	// declared ladders a member of this tenant lands on when they have no per-user
	// value (docs/70 §70.4.3). "" => the deployment default class. tenant_admin-set,
	// like the idle timeouts; inert on every runtime but ecs-ec2.
	SlotClass string `json:"slot_class,omitempty"`
	// AllowedSlotClasses restricts which classes a tenant_admin may choose from,
	// super_admin-set — the same two-stage shape as MaxWorkspace*: the operator's
	// ceiling above, the tenant's choice below. Empty (the default) = no restriction,
	// so a deployment that never sets it behaves exactly as before.
	//
	// ⚠️ This is a policy, not a cap on a number: an out-of-range memory value can be
	// clamped down to something usable, but a class that is not allowed has no
	// "smaller" version — resolveSlotClass falls back to the tenant default and SAYS
	// so, rather than silently running the person somewhere they didn't ask for.
	AllowedSlotClasses []string `json:"allowed_slot_classes,omitempty"`
	// P3-9 idle-stop (docs/19): per-tenant, super_admin-editable.
	SessionIdleTimeout string `json:"session_idle_timeout,omitempty"` // tier-1: idle claude -> halt
	WSIdleTimeout      string `json:"ws_idle_timeout,omitempty"`      // tier-2: cold workspace -> docker stop
	// InteractionIdleTimeout is tier-1's clock for a session parked on a HUMAN decision —
	// a question, a plan awaiting approval, a permission prompt, the usage-limit menu, an
	// expired login (docs/75 §75.5). Separate from SessionIdleTimeout because the two
	// answer different questions: "how long may an idle session hold RAM" vs "how long do
	// we keep a container up for someone who hasn't come back to decide". A tenant that
	// wants questions folded away quickly (they cost a running container until answered)
	// but plain idle sessions kept warm, or the reverse, needs both knobs.
	//
	// "" => the tenant's own SessionIdleTimeout when they set one, else the deployment
	// default (AF_INTERACTION_IDLE_TIMEOUT, itself defaulting to the session default).
	// "0" => never fold a human-wait session for this tenant. 畳まれた対話は失われない —
	// 保留ペイロードは持ち越しへ退避され、Console から答えれば再開して届く（docs/75 §75.6）。
	InteractionIdleTimeout string `json:"interaction_idle_timeout,omitempty"`
	// HomeHibernateAfter is the third step of the same series and the only one that
	// touches the user's data: a home nobody has opened for this long is snapshotted and
	// its volume deleted, and the next Start restores it (ADR 0045 決定 13-2, docs/64
	// §64.18.2). Only the ecs-ec2 runtime can do this; on every other runtime the value
	// is inert. Same resolution as the two timeouts above — "" => deployment default
	// (AF_ECS_EC2_HIBERNATE_AFTER_SEC), "0" => never hibernate this tenant's homes.
	HomeHibernateAfter string `json:"home_hibernate_after,omitempty"`
	// HomeBackupEvery is how often to keep a copy of a home somewhere its Availability
	// Zone is not (ADR 0045 決定 17). It is the tenant's RPO — how much work they accept
	// losing — which is why it sits next to the timeouts rather than in the deployment
	// env; how many copies to pay for is the operator's call (AF_ECS_EC2_BACKUP_KEEP).
	// ecs-ec2 only; "" => deployment default, "0" => no backups for this tenant.
	HomeBackupEvery string `json:"home_backup_every,omitempty"`
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
// interactionTimeout resolves tier-1's clock for a human-wait session. The fallback
// chain is deliberate: an admin who set only session_idle_timeout has expressed a
// tempo for that tenant, and silently running human waits on the DEPLOYMENT default
// instead would make one of the two numbers on the screen a lie.
func interactionTimeout(lim tenantLimits, sessTO time.Duration, sessOn bool, def time.Duration) (time.Duration, bool) {
	if lim.InteractionIdleTimeout != "" {
		return idleTimeout(lim.InteractionIdleTimeout, def)
	}
	if lim.SessionIdleTimeout != "" {
		return sessTO, sessOn
	}
	return idleTimeout("", def)
}

func idleTimeout(tenantVal string, def time.Duration) (d time.Duration, enabled bool) {
	d = def
	if tenantVal != "" {
		if p, err := time.ParseDuration(tenantVal); err == nil {
			d = p
		}
	}
	return d, d > 0
}
