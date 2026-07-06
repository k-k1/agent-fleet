package main

import (
	"context"
	"time"
)

// MetadataStore (docs/12 P3-1/P3-2) — the persistent source of truth for the
// identity/tenant hierarchy, replacing the old in-memory map. As of P3-2 (docs/14)
// identity↔tenant is many-to-many: an Identity (person, unique email) joins one or
// more Tenants via Membership, and a Workspace exists per Membership (= identity ×
// tenant, fully isolated). The default adapter is SQLite (store_sqlite.go);
// Postgres drops in behind this interface for AWS/HA later (docs/12 P3-7).

type Tenant struct {
	ID, Slug, Name, Status, Limits, Isolation, KeyRef, CreatedAt string
}

// Identity is a person, unique by email within the deployment. role is the
// deployment-scoped role (user | super_admin).
type Identity struct {
	ID, Email, UserKey, Role, Status, LastLoginAt string
}

// Membership joins an Identity to a Tenant with a tenant-scoped role
// (member | tenant_admin).
type Membership struct {
	ID, IdentityID, TenantID, Role, Status, CreatedAt string
}

// UserLimit is a per-membership quota override (docs/16 P3-4). 0 = unset.
type UserLimit struct {
	MembershipID        string
	MaxSessions, DiskGB int
	CreatedAt           string
}

// MembershipView is a membership enriched with its tenant's slug/name, for the
// /api/tenants picker and per-request tenant resolution.
type MembershipView struct {
	MembershipID, TenantID, TenantSlug, TenantName, Role string
}

// MemberInfo is a tenant member for the admin UI (docs/12 P3-6 / admin).
type MemberInfo struct {
	MembershipID, UserKey, Email, IdentityRole, MemberRole string
}

// PAT is a Personal Access Token (docs/decisions/0006, P3-6). It authenticates
// the MCP endpoint. role is NOT stored — it is resolved live from identity +
// membership at call time so a demotion/revocation takes effect immediately.
// scope is fixed at issuance (read | write | admin:dangerous), clamped to the
// issuer's ceiling. Timestamps are RFC3339 ("" = unset).
type PAT struct {
	ID, IdentityID, MembershipID, Scope, Name   string
	CreatedAt, ExpiresAt, RevokedAt, LastUsedAt string
}

// AuditLog records one administrative action (docs/decisions/0006, P3-6).
// ActorKind ∈ user | admin | mcp | system; ActorID is the identity/PAT id behind
// it. TenantID scopes the entry ("" = deployment-wide). At is RFC3339.
type AuditLog struct {
	ID, TenantID, ActorKind, ActorID, Action, Target, Detail, At string
}

// EgressStat is one destination host with its would-allow / would-block hit counts
// aggregated over the queried window (docs/20 M2, log-only egress proxy).
type EgressStat struct {
	Host             string
	Allowed, Blocked int
}

// AllowlistEntry is one versioned egress allowlist rule (docs/20 M3). TenantID is a
// tenant id, or "" for a deployment-global rule. State ∈ active | proposed | retired
// (proposed rules come from the M4 agent and need admin approval to go active).
type AllowlistEntry struct {
	ID, TenantID, Entry, State, Reason, AddedBy, AddedAt string
}

// UsageRow is one (membership, day) showback bucket enriched with human labels
// for reporting (docs/roadmap.md P3-9). RunningSecs is accumulated workspace
// occupancy in seconds. A member with no membership record still surfaces (its
// workspace outlived the membership) with empty UserKey/Email.
type UsageRow struct {
	TenantID, TenantSlug, MembershipID, UserKey, Email, Day string
	RunningSecs                                             int
}

// SSMProfile is the COMMON auth bundle shared by many hosts (docs/history/p3-ssm-
// session.md): the AWS IAM Identity Center (SSO) portal + account/role/default region.
// It maps to one ~/.aws named profile; `aws sso login` authenticates it. Personal
// scope. NON-SECRET: the CP never sees AWS credentials — the in-container aws CLI logs
// in directly against StartURL and caches the token in the workspace home.
type SSMProfile struct {
	ID, MembershipID, Label                  string
	StartURL, SSORegion, AccountID, RoleName string
	Region, CreatedAt                        string
}

// SSMHost is a per-member bookmark for one SSM Session Manager target: which instance,
// which run-as SSM document, an optional region override, and which profile to
// authenticate with. No secrets. Mirrors the operator's hand-rolled bash map (host
// alias -> "document instance") as first-class WebUI records.
type SSMHost struct {
	ID, MembershipID, Alias, ProfileID  string
	Region                              string // optional per-host override ("" = profile default)
	InstanceID, DocumentName, CreatedAt string
}

// Workspace is one container per Membership (= identity × tenant).
type Workspace struct {
	ID, TenantID, MembershipID      string
	ContainerName, Network, DataDir string
	AgentPort, AgentToken, State    string
	CreatedAt, LastActiveAt         string
}

// SessionRow mirrors one Agent session into the CP DB so the session list can be
// served while the Workspace container is stopped (the Agent is the source of
// truth when running). state is "running" | "stopped".
type SessionRow struct {
	WorkspaceID, Name, Kind, Dir, Repo, Label string
	CreatedAt, State, LastSeen                string
}

// GitRepo is one internal bare repository owned by a tenant (docs/reference/
// internal-git-provider). The on-disk bare lives at ${DATA_DIR}/git/<slug>/<name>.git;
// this row is the ledger the list/serve paths trust over an FS walk.
type GitRepo struct {
	ID, TenantID, Name, DefaultBranch, CreatedBy, CreatedAt string
}

// LFSLock is one Git LFS file lock (docs/reference/internal-git-provider, P3).
// OwnerID is the locker's membership id; OwnerName is a display label.
type LFSLock struct {
	ID, TenantID, RepoName, Path, RefName, OwnerID, OwnerName, LockedAt string
}

// Store is the MetadataStore port. SQLite is the only adapter in P3-1/P3-2.
type Store interface {
	EnsureDefaultTenant(ctx context.Context) (Tenant, error)
	CreateTenant(ctx context.Context, slug, name string) (Tenant, error)
	GetTenant(ctx context.Context, id string) (Tenant, error)
	GetTenantBySlug(ctx context.Context, slug string) (Tenant, bool, error)
	SetTenantLimits(ctx context.Context, tenantID, limitsJSON string) error

	// UpsertIdentity creates/updates the person. roleHint, when non-empty,
	// upgrades the deployment role (e.g. "super_admin" from SUPER_ADMIN_EMAILS);
	// it never downgrades.
	UpsertIdentity(ctx context.Context, email, key, roleHint string) (Identity, error)
	GetIdentityByID(ctx context.Context, id string) (Identity, bool, error)

	ListTenants(ctx context.Context) ([]Tenant, error)
	ListMemberships(ctx context.Context, identityID string) ([]MembershipView, error)
	ListMembersByTenant(ctx context.Context, tenantID string) ([]MemberInfo, error)
	EnsureMembership(ctx context.Context, identityID, tenantID, role string) (Membership, error)
	GetMembership(ctx context.Context, identityID, tenantID string) (Membership, bool, error)
	// GetMembershipByID resolves a membership (with its tenant slug + live role) by
	// its id — used by the internal-git smart-HTTP handler to map a git token back
	// to (tenant, role) on every request. ok=false when it is missing/inactive.
	GetMembershipByID(ctx context.Context, membershipID string) (MembershipView, bool, error)
	// SetMembershipRole changes a membership's tenant-scoped role (member |
	// tenant_admin). EnsureMembership only inserts, so this is the update path.
	SetMembershipRole(ctx context.Context, membershipID, role string) error

	GetWorkspaceByMembership(ctx context.Context, membershipID string) (Workspace, bool, error)
	CreateWorkspace(ctx context.Context, ws Workspace) error
	SetWorkspaceState(ctx context.Context, workspaceID, state string) error
	// Per-workspace member settings (JSON blob; "" = none). CP-owned so they can be
	// read/written while the container is stopped; mapped to env at container start.
	GetWorkspaceSettings(ctx context.Context, workspaceID string) (string, error)
	SetWorkspaceSettings(ctx context.Context, workspaceID, settingsJSON string) error
	MaxAgentPort(ctx context.Context) (int, error)
	ListWorkspaces(ctx context.Context, tenantID string) ([]Workspace, error)

	// Per-membership quota override (docs/16 P3-4).
	GetUserLimit(ctx context.Context, membershipID string) (UserLimit, bool, error)
	PutUserLimit(ctx context.Context, membershipID string, maxSessions, diskGB int) error

	// Envelope-encrypted per-workspace DEK (docs/15 P3-3).
	GetWrappedDEK(ctx context.Context, workspaceID string) (ciphertext, keyRef string, ok bool, err error)
	PutWrappedDEK(ctx context.Context, workspaceID, ciphertext, keyRef string) error

	// Session index mirror: ReplaceSessions swaps the workspace's rows for the
	// Agent's current list; ListSessions serves them while the container is down.
	ReplaceSessions(ctx context.Context, workspaceID string, rows []SessionRow) error
	ListSessions(ctx context.Context, workspaceID string) ([]SessionRow, error)

	// Personal Access Tokens for the MCP endpoint (docs/decisions/0006, P3-6).
	CreatePAT(ctx context.Context, p PAT, tokenHash string) error
	GetPATByHash(ctx context.Context, tokenHash string) (PAT, bool, error)
	ListPATsByIdentity(ctx context.Context, identityID string) ([]PAT, error)
	RevokePAT(ctx context.Context, id, identityID string) error
	TouchPAT(ctx context.Context, id string) error

	// Internal git repositories (docs/reference/internal-git-provider, ADR 0010).
	// CreateGitRepo inserts the ledger row (the bare is created on disk by the
	// caller); the (tenant_id, name) uniqueness is enforced by the schema.
	CreateGitRepo(ctx context.Context, g GitRepo) error
	ListGitReposByTenant(ctx context.Context, tenantID string) ([]GitRepo, error)
	GetGitRepo(ctx context.Context, tenantID, name string) (GitRepo, bool, error)
	CountGitReposByTenant(ctx context.Context, tenantID string) (int, error)
	RenameGitRepo(ctx context.Context, tenantID, oldName, newName string) error
	DeleteGitRepo(ctx context.Context, tenantID, name string) error

	// Git LFS object ledger (P3). PutLFSObject records an uploaded object (dedup on
	// (tenant, repo, oid)); TenantLFSBytes sums a tenant's stored bytes for the
	// capacity quota; the repo-scoped ops keep the ledger in step with repo
	// delete/rename (the bytes on disk move with the .git dir).
	PutLFSObject(ctx context.Context, tenantID, repo, oid string, size int64) error
	TenantLFSBytes(ctx context.Context, tenantID string) (int64, error)
	DeleteLFSObjectsByRepo(ctx context.Context, tenantID, repo string) error
	RenameLFSObjectsRepo(ctx context.Context, tenantID, oldRepo, newRepo string) error
	// DeleteLFSObject drops one object's ledger row (used by LFS GC when it prunes
	// an orphaned object from disk, so the tenant's capacity quota frees up).
	DeleteLFSObject(ctx context.Context, tenantID, repo, oid string) error
	// ListLFSObjectOIDs returns the oids the ledger records for a repo — the set GC
	// walks to reconcile against what git still references.
	ListLFSObjectOIDs(ctx context.Context, tenantID, repo string) ([]string, error)

	// Git LFS file locks (P3). CreateLFSLock inserts a lock (the (tenant, repo, path)
	// UNIQUE makes a second lock on a path fail — the caller pre-checks for the 409).
	// ListLFSLocks paginates by an opaque cursor (offset); a filter of "" matches all.
	// The repo-scoped ops keep locks in step with repo delete/rename.
	CreateLFSLock(ctx context.Context, l LFSLock) error
	GetLFSLockByPath(ctx context.Context, tenantID, repo, path string) (LFSLock, bool, error)
	GetLFSLock(ctx context.Context, tenantID, repo, id string) (LFSLock, bool, error)
	ListLFSLocks(ctx context.Context, tenantID, repo, filterPath, filterID string, limit int, cursor string) ([]LFSLock, string, error)
	DeleteLFSLock(ctx context.Context, tenantID, repo, id string) error
	DeleteLFSLocksByRepo(ctx context.Context, tenantID, repo string) error
	RenameLFSLocksRepo(ctx context.Context, tenantID, oldRepo, newRepo string) error
	// MembershipOwnerName resolves a membership to a human display label (email, or
	// the user key) for stamping on a lock. "" when it can't be resolved.
	MembershipOwnerName(ctx context.Context, membershipID string) (string, error)

	// Audit log (docs/decisions/0006, P3-6; docs/20 M1). InsertAudit records one
	// action; ListAuditByTenant serves the most recent entries (newest first) scoped
	// to a tenant, or — when tenantID=="" — deployment-wide (super_admin).
	InsertAudit(ctx context.Context, a AuditLog) error
	ListAuditByTenant(ctx context.Context, tenantID string, limit int) ([]AuditLog, error)

	// Egress observation aggregation (docs/20 M2, log-only proxy). RecordEgress adds
	// hits into the (day, host, allowed) bucket; ListEgress returns the busiest hosts
	// on/after sinceDay (YYYY-MM-DD) with their would-allow / would-block split.
	RecordEgress(ctx context.Context, day, host string, allowed bool, count int) error
	ListEgress(ctx context.Context, sinceDay string, limit int) ([]EgressStat, error)

	// Egress allowlist (docs/20 M3). ListAllowlist returns entries with the given
	// state ("" = any); AddAllowlist inserts one; SetAllowlistState transitions a row
	// (approve a proposed entry to active, or retire one); EffectiveAllowlist returns
	// the active entry strings the proxy enforces.
	ListAllowlist(ctx context.Context, state string, limit int) ([]AllowlistEntry, error)
	AddAllowlist(ctx context.Context, e AllowlistEntry) error
	SetAllowlistState(ctx context.Context, id, state string) error
	EffectiveAllowlist(ctx context.Context) ([]string, error)

	// Deployment settings (docs/20 M3): small kv for deployment-wide toggles such as
	// the egress mode. GetSetting returns "" when unset.
	GetSetting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value string) error

	// Showback usage (docs/roadmap.md P3-9). AddUsage accumulates workspace
	// running-seconds into the (membership, day) bucket; ListUsage returns the
	// per-day rows in [fromDay, toDay] (inclusive, YYYY-MM-DD), scoped to one
	// tenant or, when tenantID=="", every tenant (super_admin).
	AddUsage(ctx context.Context, membershipID, tenantID, day string, secs int) error
	ListUsage(ctx context.Context, tenantID, fromDay, toDay string) ([]UsageRow, error)

	// SSM login config (docs/history/p3-ssm-session.md), personal scope. No AWS
	// secrets stored. Mutations are scoped by membership so a member only touches
	// their own rows. A profile is the common auth bundle; a host references one.
	ListSSMProfiles(ctx context.Context, membershipID string) ([]SSMProfile, error)
	GetSSMProfile(ctx context.Context, id string) (SSMProfile, bool, error)
	CreateSSMProfile(ctx context.Context, p SSMProfile) error
	UpdateSSMProfile(ctx context.Context, p SSMProfile) error
	DeleteSSMProfile(ctx context.Context, id, membershipID string) error
	ListSSMHosts(ctx context.Context, membershipID string) ([]SSMHost, error)
	GetSSMHost(ctx context.Context, id string) (SSMHost, bool, error)
	CreateSSMHost(ctx context.Context, h SSMHost) error
	UpdateSSMHost(ctx context.Context, h SSMHost) error
	DeleteSSMHost(ctx context.Context, id, membershipID string) error

	Close() error
}

// newID mints an opaque record id (not a strict UUID; sufficient for keys).
// randHex is defined in oauth_bitbucket.go.
func newID() string { return randHex(16) }

func nowTS() string { return time.Now().UTC().Format(time.RFC3339) }
