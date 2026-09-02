// limits.go — テナント limits JSON のパースとアイドルタイムアウト解決。
// manager.go からの機械的分割（docs/log/23 P2-W2）。
package main

import (
	"context"
	"encoding/json"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
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
	// value (docs/log/70 §70.4.3). "" => the deployment default class. tenant_admin-set,
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
	// P3-9 idle-stop (docs/log/19): per-tenant, super_admin-editable.
	SessionIdleTimeout string `json:"session_idle_timeout,omitempty"` // tier-1: idle claude -> halt
	WSIdleTimeout      string `json:"ws_idle_timeout,omitempty"`      // tier-2: cold workspace -> docker stop
	// InteractionIdleTimeout is tier-1's clock for a session parked on a HUMAN decision —
	// a question, a plan awaiting approval, a permission prompt, the usage-limit menu, an
	// expired login (docs/log/75 §75.5). Separate from SessionIdleTimeout because the two
	// answer different questions: "how long may an idle session hold RAM" vs "how long do
	// we keep a container up for someone who hasn't come back to decide". A tenant that
	// wants questions folded away quickly (they cost a running container until answered)
	// but plain idle sessions kept warm, or the reverse, needs both knobs.
	//
	// "" => the tenant's own SessionIdleTimeout when they set one, else the deployment
	// default (AF_INTERACTION_IDLE_TIMEOUT, itself defaulting to the session default).
	// "0" => never fold a human-wait session for this tenant. 畳まれた対話は失われない —
	// 保留ペイロードは持ち越しへ退避され、Console から答えれば再開して届く（docs/log/75 §75.6）。
	InteractionIdleTimeout string `json:"interaction_idle_timeout,omitempty"`
	// HomeHibernateAfter is the third step of the same series and the only one that
	// touches the user's data: a home nobody has opened for this long is snapshotted and
	// its volume deleted, and the next Start restores it (ADR 0045 決定 13-2, docs/log/64
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

// --- テナント上限とスロットプールの突き合わせ（docs/log/64 §64.35）---
//
// ⚠️ この 2 つは**別の分母を数えている**。混ぜて 1 つの数字にしてはいけない。
//
//   max_workspaces  … そのテナントの**同時に running / starting な Workspace の数**
//                     （countRunningInTenant）。停止中の WS は数えない。
//   Ec2MaxSlots     … **存在してよい箱の数**。停止中の WS も、遅延返却で箱を掴んだまま
//                     なので、こちらには数えられている。
//
// 比べられるのは「running な WS はちょうど 1 つの箱を要る」からで、差分は**停止中の WS が
// 握っている箱**である。したがって Σ ≤ 容量 は**必要条件であって十分条件ではない**：
// 枠内でも休眠中の箱が上限を埋めて立ち退きが起きうる（`Ec2SlotTerminateAfterSec` を
// 入れると差分は「最後の N 時間ぶん」に縮むが、0 にはならない）。
//
// 画面でも 2 つを 1 つの言葉にしないこと——「同時利用」と「占有スロット」は別物である。

// slotCapacityReporter is implemented by the one runtime whose boxes are a FIXED,
// deployment-wide pool. Everything else provisions per workspace and has no number to
// compare a tenant quota against, which is why the check below simply does not exist
// there (rather than passing vacuously).
type slotCapacityReporter interface {
	MaxSlots() int
}

// poolBudget is declared by the adapters' package (internal/runtime/deps.go): the EC2
// pool status DTO embeds it, and that DTO moved with the adapter. The type and its OK()
// went along because a Go alias cannot carry methods. Everything on this side — the
// totalling below, the admin API, the pool screen's JSON — is unchanged.
type poolBudget = runtime.PoolBudget

// poolBudget totals the tenants' concurrency quotas against the pool cap.
//
// overrideTenantID / overrideMax substitute a value that is not stored yet, so the
// warning an admin gets on save is about the number they just typed rather than about
// the one it replaced. Pass "" to read what is stored.
//
// ok=false on every runtime without a pool. That is the whole answer there, not "fine".
func (m *manager) poolBudget(ctx context.Context, overrideTenantID string, overrideMax int) (poolBudget, bool, error) {
	cap, ok := m.rtFactory.(slotCapacityReporter)
	if !ok {
		return poolBudget{}, false, nil
	}
	ts, err := m.store.ListTenants(ctx)
	if err != nil {
		return poolBudget{}, true, err
	}
	b := poolBudget{MaxSlots: cap.MaxSlots(), Reserved: runtime.BakeReservedSlots}
	b.Capacity = b.MaxSlots - b.Reserved
	for _, t := range ts {
		// A suspended tenant runs nothing, so counting it would make an operator lower
		// a live tenant to make room for one that is switched off.
		if t.Status != "" && t.Status != "active" {
			continue
		}
		n := parseLimits(t.Limits).MaxWorkspaces
		if t.ID == overrideTenantID {
			n = overrideMax
		}
		if n <= 0 {
			b.Unbounded = append(b.Unbounded, t.Slug)
			continue
		}
		b.Allocated += n
	}
	b.Over = len(b.Unbounded) == 0 && b.Allocated > b.Capacity
	return b, true, nil
}
