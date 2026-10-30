package store

import (
	"context"
	"database/sql"
)

// UserQuota is the settable part of a per-membership quota override — the three
// workspace size axes plus the session count. All are 0 = unset, meaning "use the
// deployment default". The axes are held as independent numbers rather than as a
// named size (S/M/L…): the named sizes live in the Console and MCP as a way to
// PRESENT valid combinations, while storage stays runtime-neutral so docker keeps
// its byte-exact --memory and --cpus (ADR 0044 decision 1).
type UserQuota struct {
	MaxSessions int
	// DiskGB is the per-workspace working disk in GiB. On ECS it becomes the task's
	// ephemeral storage (21–200 GiB) or, above that, an ECS-managed EBS volume
	// (ADR 0044 decision 2). On docker it stays the display-only quota it has always been.
	DiskGB int
	// MemLimit is the per-workspace RAM cap in BYTES (0 = unset → deployment default
	// WS_MEMORY / AF_ECS_TASK_MEMORY). A tenant_admin sets it within the tenant cap
	// (tenantLimits.MaxWorkspaceMem); resolveWorkspaceMemBytes clamps and applies it at
	// container start (docker --memory / ECS task size). See docs/log/26 / roadmap P3-4.
	MemLimit int64
	// CPULimit is the per-workspace CPU cap in Fargate CPU units (1024 = 1 vCPU),
	// bounded by tenantLimits.MaxWorkspaceCPU. Independent of MemLimit so "8 GB with
	// 4 vCPU" is expressible; fargateSize snaps the pair onto a valid Fargate size.
	CPULimit int
	// SlotClass is which KIND of machine the workspace lands on, as a
	// deployment-declared class id ("" = unset → the tenant default, then the
	// deployment default). Only ecs-ec2 reads it; everywhere else it is inert.
	//
	// It is a string and not a number because it is not a size — the three axes above
	// say HOW BIG, this says WHICH LADDER (docs/log/70 §70.4). Still runtime-neutral in the
	// sense that matters: the id is opaque here, and the operator alone maps it to
	// instance types and an architecture (ADR 0044 decision 1).
	SlotClass string
}

// UserLimit is a per-membership quota override (docs/16 P3-4) as stored.
type UserLimit struct {
	MembershipID string
	UserQuota
	CreatedAt string
}

// QuotaStore is the per-membership quota override (docs/16 P3-4).
type QuotaStore interface {
	GetUserLimit(ctx context.Context, membershipID string) (UserLimit, bool, error)
	PutUserLimit(ctx context.Context, membershipID string, q UserQuota) error
}

func (s *SQL) GetUserLimit(ctx context.Context, membershipID string) (UserLimit, bool, error) {
	var u UserLimit
	err := s.db.QueryRowContext(ctx,
		`SELECT membership_id, max_sessions, disk_gb, mem_limit, cpu_limit, slot_class, created_at FROM user_limit WHERE membership_id=?`, membershipID).
		Scan(&u.MembershipID, &u.MaxSessions, &u.DiskGB, &u.MemLimit, &u.CPULimit, &u.SlotClass, &u.CreatedAt)
	if err == sql.ErrNoRows {
		return UserLimit{}, false, nil
	}
	if err != nil {
		return UserLimit{}, false, err
	}
	return u, true, nil
}

func (s *SQL) PutUserLimit(ctx context.Context, membershipID string, q UserQuota) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO user_limit(membership_id, max_sessions, disk_gb, mem_limit, cpu_limit, slot_class, created_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(membership_id) DO UPDATE SET max_sessions=excluded.max_sessions, disk_gb=excluded.disk_gb,
		   mem_limit=excluded.mem_limit, cpu_limit=excluded.cpu_limit, slot_class=excluded.slot_class`,
		membershipID, q.MaxSessions, q.DiskGB, q.MemLimit, q.CPULimit, q.SlotClass, NowTS())
	return err
}
