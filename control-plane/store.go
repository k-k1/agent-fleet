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
	// MemLimit is the per-workspace RAM cap in BYTES (0 = unset → deployment default
	// WS_MEMORY / AF_ECS_TASK_MEMORY). A tenant_admin sets it within the tenant cap
	// (tenantLimits.MaxWorkspaceMem); resolveWorkspaceMemBytes clamps and applies it at
	// container start (docker --memory / ECS task size). See docs/26 / roadmap P3-4.
	MemLimit  int64
	CreatedAt string
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
// it. TenantID scopes the entry ("" = deployment-wide). HTTPStatus is the
// proxied operation's status when recorded (0 for legacy/non-HTTP events).
// At is RFC3339.
type AuditLog struct {
	ID, TenantID, ActorKind, ActorID, Action, Target, Detail, At string
	HTTPStatus                                                   int
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

// Memo is one queued note (docs/21), personal scope. Grouped by Repo then Category
// (a free-form sub-project label); Repo="" is the common/unfiled bucket. Kind is
// "file" (RefPath points at a ~/repos path, Body is an optional comment) or "text"
// (Body is the note). SentAt="" means unsent; a non-empty RFC3339 stamp marks a
// flushed memo kept until the retention sweep removes it.
type Memo struct {
	ID, MembershipID, Repo, Category string
	Kind, Body, RefPath              string
	// Attachments is a JSON array of {path,name} image attachments (docs/21 画像添付),
	// "" = none. path is an absolute in-container path under ~/.cache/agent-fleet/
	// memo-images; the bytes live in the workspace, this only references them.
	Attachments       string
	Position          int
	CreatedAt, SentAt string
}

// MemoCategory persists a memo category as a first-class row (docs/21 UI刷新), so a
// category can exist while empty and carry an explicit order. Name is unique within a
// (MembershipID, Repo) and stays the grouping key that Memo.Category references.
type MemoCategory struct {
	ID, MembershipID, Repo, Name string
	Position                     int
	CreatedAt                    string
}

// Schedule is one operator-authored scheduled task (docs/38 + ADR0021). It lives
// in the CP DB because the CP is the only thing alive while the workspace is stopped.
// SpecKind is "cron" (Spec is a 5-field cron expr), "interval" (Spec is whole
// seconds) or "once" (Spec is an RFC3339 absolute instant). Spec is the evaluated
// source of truth; TZ is the IANA zone cron is evaluated in (DST included); SpecLabel
// keeps the operator's original natural-language phrasing for display only. NextRun/
// LastRun are UTC RFC3339 fire-ledger stamps (NextRun="" means never/disabled).
type Schedule struct {
	ID, MembershipID, TenantID string
	OwnerConv                  string // operator conversation id — the report_to target (docs/30)
	SpecKind, Spec, SpecLabel  string
	TZ                         string
	WakePolicy                 string // wake (default) | skip | catch_up
	SessionMode, ReuseTarget   string // new (default) | reuse; ReuseTarget = pinned session name for reuse (P6)
	AgentKind, Model           string
	Repo, Worktree             string
	NewBranch                  bool
	Prompt                     string
	OverlapPolicy              string // skip (default) | queue | restart (reuse only, P6)
	// Report opts a fire into the docs/30 completion report: true passes OwnerConv as the
	// session's report_to so the result comes back to the operator/assistant conversation.
	// Default false = fire silently (run history / failure notifications still surface).
	Report               bool
	Enabled              bool
	NextRun, LastRun     string
	LastStatus           string
	CreatedAt, UpdatedAt string
	// Reuse ledger (P6, docs/38): the current long-lived session the scheduler drives in
	// session_mode=reuse, when it started, and how many fires it has taken since the last
	// rotation. Rotation is a JSON blob of triggers (every_runs/after/calendar). MissingTargetPolicy
	// governs a pinned reuse whose target session vanished (recreate default | fail).
	ReuseSession, ReuseStartedAt string
	ReuseRunCount                int
	Rotation                     string
	MissingTargetPolicy          string
	// ManualFirePending is set by run-now and read+cleared by the scheduler on the next
	// fire to tag that run as manual (docs/38). Transient — not part of the schedule DTO.
	ManualFirePending bool
}

// ScheduleRun is one fire-attempt history row (docs/38 P3 get_schedule_runs).
// Status mirrors the schedule's last_status token; FiredAt is UTC RFC3339. Session is
// the session the fire drove — the newly created session (session_mode=new) or the
// long-lived reuse target (session_mode=reuse) — so the Console history can open it. It
// is empty for soft skips that ran nothing (skipped_* / error before a session existed).
// Trigger is how the fire was initiated: "manual" (a run-now) or "scheduled" (an automatic
// wall-clock fire, the default), so the history can tell an on-demand run from a timed one.
type ScheduleRun struct {
	ID, ScheduleID, MembershipID string
	FiredAt, Status, Detail      string
	Session                      string
	Trigger                      string
}

// MCPServerRow is one tenant-distributed MCP server definition (docs/48 P4 +
// ADR0031). It is deliberately NOT the agent's full ServerDef: there is no Command /
// Args / Env, because a tenant-distributed stdio server would be an admin running an
// arbitrary command in every member's container. The table has no such columns either,
// so the refusal cannot be relaxed by editing one validation branch.
//
// HeadersEnc is the header map sealed by the tenant key custodian (KeyRef names the
// key), or plaintext JSON with an empty KeyRef on a deployment with no master key —
// the same degradation the Agent's secret store makes in dev. Targets and Kinds are
// comma lists on the wire to the DB ("assistant,session"; empty Kinds = every kind).
//
// UserSecret=1 distributes the URL and the header NAMES only: each member supplies the
// values into their own encrypted store (docs/48 §5.2). It exists because a token in a
// distributed header is readable in plaintext by every member of the tenant, which
// container isolation cannot prevent.
type MCPServerRow struct {
	ID, TenantID         string
	Name, Label          string
	Transport            string // always "http" — stdio is refused (ADR0031 決定 2)
	URL                  string
	HeadersEnc, KeyRef   string
	Targets, Kinds       string
	TimeoutMS            int
	Enabled, UserSecret  bool
	CreatedBy            string
	CreatedAt, UpdatedAt string
}

// Notification is a membership-scoped, content-free activity record shared by
// every browser the member uses. Payload contains only structured metadata; chat
// answer/question text is deliberately never persisted here.
type Notification struct {
	Seq                                                           int64
	EventID, MembershipID, Kind, TargetType, TargetID, TargetKind string
	DisplayName, Payload, CreatedAt, SeenAt                       string
}

type UsageNotificationState struct {
	MembershipID, Source, WindowKey, ResetsAt string
	Armed                                     int
}

// Workspace is one container per Membership (= identity × tenant).
type Workspace struct {
	ID, TenantID, MembershipID      string
	ContainerName, Network, DataDir string
	AgentPort, AgentToken, State    string
	CreatedAt, LastActiveAt         string
	// MemBytes is the RESOLVED per-workspace RAM cap in bytes for the NEXT container
	// start (0 = use the runtime's deployment default). It is NOT a persisted column:
	// buildResolved fills it via resolveWorkspaceMemBytes before the runtime is built,
	// and the factory (docker --memory / ECS task size) honors it when >0. Read/stop
	// call sites leave it 0, which never needs a memory value.
	MemBytes int64
}

// SessionRow mirrors one Agent session into the CP DB so the session list can be
// served while the Workspace container is stopped (the Agent is the source of
// truth when running). state is "running" | "stopped".
type SessionRow struct {
	WorkspaceID, Name, Kind, Dir, Repo, Label string
	CreatedAt, State, LastSeen                string
}

type SessionShare struct {
	ID, TenantID, OwnerMembershipID, RecipientMembershipID string
	ScopeType, ScopeKey, Permission, CreatedAt, UpdatedAt  string
}

type SharedSessionCatalog struct {
	ID, WorkspaceID, OwnerMembershipID, Name, Kind, Dir, Repo string
	WorkingCopyID, Title, Label, CreatedAt, State, LastSeen   string
	Archived                                                  bool
	// Worktree/Parent: docs/59 の受信側プロジェクト/worktreeツリー表示用。
	// Parent は worktree の場合のみ、親(ベース)working copy のフォルダ名。
	Worktree bool
	Parent   string
	// ParentWorkingCopyID は worktree の場合のみ、親(ベース)working copy の
	// workingCopyId。repo 共有はプロジェクト全体(ベース＋その worktree)を対象に
	// するので、ACL 判定はフォルダ名(Parent)ではなくこの ID で行う — 名前は
	// 付け替えられるが workingCopyId は作業コピーの世代に固定されるため。
	ParentWorkingCopyID string
	// Branch は作業コピーが今チェックアウトしているブランチ(表示専用)。worktree の
	// フォルダ名はランダム slug なので、受信側はこれが無いとどの作業か分からない。
	Branch string
	// Activity は Agent の live state(working | idle | question | plan | permission |
	// blocked | compacting)。State は running/stopped(生存)なので別に持つ。停止中は空。
	Activity string
}

type SessionShareProposal struct {
	ID, TenantID, CatalogID, OwnerMembershipID, ProposerMembershipID string
	Action, Ciphertext, KeyRef, Status                               string
	CreatedAt, ExpiresAt, DecidedAt, DecidedBy                       string
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

// Store is the MetadataStore port — the union of the feature-scoped sub-
// interfaces below (docs/23 P2-W3). 実装は単一の sqlStore（sqlite/postgres 共用）
// のまま。利用側は原則 Store を持つが、独立コンポーネントは必要最小のサブ
// インターフェース（narrow view）に依存できる（例: git_gc.go）。メソッドの
// 追加は該当サブインターフェースへ。
type Store interface {
	TenantStore
	IdentityStore
	MembershipStore
	WorkspaceStore
	QuotaStore
	DEKStore
	SessionIndexStore
	PATStore
	GitRepoStore
	LFSObjectStore
	LFSLockStore
	AuditStore
	EgressStore
	SettingsStore
	UsageStore
	SSMStore
	MemoStore
	NotificationStore
	ScheduleStore
	MCPServerStore
	SessionShareStore

	Close() error
}

type SessionShareStore interface {
	PutSessionShare(ctx context.Context, row SessionShare) error
	GetSessionShare(ctx context.Context, id string) (SessionShare, bool, error)
	ListSessionSharesByOwner(ctx context.Context, membershipID string) ([]SessionShare, error)
	ListSessionSharesByRecipient(ctx context.Context, membershipID string) ([]SessionShare, error)
	DeleteSessionShare(ctx context.Context, id, ownerMembershipID string) error
	DeleteSessionSharesByScope(ctx context.Context, ownerMembershipID, scopeType, scopeKey string) error
	UpdateSessionSharePermission(ctx context.Context, id, ownerMembershipID, permission, updatedAt string) (bool, error)
	ReplaceSharedSessionCatalog(ctx context.Context, workspaceID, ownerMembershipID string, rows []SharedSessionCatalog) error
	GetSharedSessionCatalog(ctx context.Context, id string) (SharedSessionCatalog, bool, error)
	ListSharedSessionCatalogByOwner(ctx context.Context, membershipID string) ([]SharedSessionCatalog, error)
	CreateSessionShareProposal(ctx context.Context, row SessionShareProposal) error
	CreateSessionShareProposalLimited(ctx context.Context, row SessionShareProposal, maxPending int) (bool, error)
	GetSessionShareProposal(ctx context.Context, id string) (SessionShareProposal, bool, error)
	ListSessionShareProposalsByOwner(ctx context.Context, membershipID string) ([]SessionShareProposal, error)
	CountPendingSessionShareProposals(ctx context.Context, catalogID string) (int, error)
	ExpireSessionShareProposals(ctx context.Context, ownerMembershipID, now string) error
	TransitionSessionShareProposal(ctx context.Context, id, from, to, decidedBy, decidedAt string, clearBody bool) (bool, error)
	ClaimSessionShareProposal(ctx context.Context, id, ownerMembershipID, decidedBy, now, leaseUntil string) (SessionShareProposal, SharedSessionCatalog, string, error)
	FinalizeSessionShareProposal(ctx context.Context, id, ownerMembershipID, decidedBy, decidedAt string) (bool, error)
	AcquireSessionShareOwnerLease(ctx context.Context, ownerMembershipID, operationID, now, leaseUntil string) (bool, error)
	RenewSessionShareOwnerLease(ctx context.Context, ownerMembershipID, operationID, now, leaseUntil string) (bool, error)
	ReleaseSessionShareOwnerLease(ctx context.Context, ownerMembershipID, operationID string) error
}

type TenantStore interface {
	EnsureDefaultTenant(ctx context.Context) (Tenant, error)
	CreateTenant(ctx context.Context, slug, name string) (Tenant, error)
	GetTenant(ctx context.Context, id string) (Tenant, error)
	GetTenantBySlug(ctx context.Context, slug string) (Tenant, bool, error)
	SetTenantLimits(ctx context.Context, tenantID, limitsJSON string) error
	ListTenants(ctx context.Context) ([]Tenant, error)
}

type IdentityStore interface {
	// UpsertIdentity creates/updates the person. roleHint, when non-empty,
	// upgrades the deployment role (e.g. "super_admin" from SUPER_ADMIN_EMAILS);
	// it never downgrades.
	UpsertIdentity(ctx context.Context, email, key, roleHint string) (Identity, error)
	// LinkIdentity is UpsertIdentity for a login that proved an IdP subject
	// (AUTH=oauth). The (provider, subject) pair decides who this is, so an email
	// change keeps the same identity — and therefore the same user_key, workspace
	// and secrets (docs/61 §61.5). isNew reports that no existing identity matched
	// and a new one was created, which is the only signal the caller has that this
	// person landed in a different workspace than the one they may expect.
	// fallbackKey is used only on that path (it is sanitizeUser(email)).
	LinkIdentity(ctx context.Context, provider, subject, email, fallbackKey, roleHint string) (ident Identity, isNew bool, err error)
	GetIdentityByID(ctx context.Context, id string) (Identity, bool, error)
	// GetIdentityByUserKey is the READ-ONLY lookup for view paths (admin stats,
	// admin MCP list tools): unlike UpsertIdentity it neither inserts a row for a
	// mistyped key nor touches last_login_at.
	GetIdentityByUserKey(ctx context.Context, key string) (Identity, bool, error)
}

type MembershipStore interface {
	ListMemberships(ctx context.Context, identityID string) ([]MembershipView, error)
	ListMembersByTenant(ctx context.Context, tenantID string) ([]MemberInfo, error)
	EnsureMembership(ctx context.Context, identityID, tenantID, role string) (Membership, error)
	GetMembership(ctx context.Context, identityID, tenantID string) (Membership, bool, error)
	// GetMembershipByID resolves a membership (with its tenant slug + live role) by
	// its id — used by the internal-git smart-HTTP handler to map a git token back
	// to (tenant, role) on every request. ok=false when it is missing/inactive.
	GetMembershipByID(ctx context.Context, membershipID string) (MembershipView, bool, error)
	// IdentityIDForMembership maps a membership id back to its owning identity id —
	// used by the internal memo-bridge flush to resolve the workspace runtime from a
	// memo token (which carries only the membership). ok=false when it is missing.
	IdentityIDForMembership(ctx context.Context, membershipID string) (string, bool, error)
	// SetMembershipRole changes a membership's tenant-scoped role (member |
	// tenant_admin). EnsureMembership only inserts, so this is the update path.
	SetMembershipRole(ctx context.Context, membershipID, role string) error
	// MembershipOwnerName resolves a membership to a human display label (email, or
	// the user key) — e.g. for stamping on an LFS lock. "" when it can't be resolved.
	MembershipOwnerName(ctx context.Context, membershipID string) (string, error)
}

type WorkspaceStore interface {
	GetWorkspaceByMembership(ctx context.Context, membershipID string) (Workspace, bool, error)
	CreateWorkspace(ctx context.Context, ws Workspace) error
	SetWorkspaceState(ctx context.Context, workspaceID, state string) error
	RecordWorkspaceActivity(ctx context.Context, workspaceID, lastSeenAt, connectedUntil, now string) (bool, error)
	WorkspaceHasRecentActivity(ctx context.Context, workspaceID, cutoff, now string) (bool, error)
	ClaimWorkspaceIdleStop(ctx context.Context, workspaceID, ownerMembershipID, operationID, cutoff, now string) (bool, error)
	ReleaseWorkspaceIdleStop(ctx context.Context, workspaceID, operationID string) error
	ClearWorkspaceIdleStop(ctx context.Context, workspaceID string) error
	AcquireWorkspaceOperationFence(ctx context.Context, workspaceID string) (func(), error)
	// Per-workspace member settings (JSON blob; "" = none). CP-owned so they can be
	// read/written while the container is stopped; mapped to env at container start.
	GetWorkspaceSettings(ctx context.Context, workspaceID string) (string, error)
	SetWorkspaceSettings(ctx context.Context, workspaceID, settingsJSON string) error
	MaxAgentPort(ctx context.Context) (int, error)
	ListWorkspaces(ctx context.Context, tenantID string) ([]Workspace, error)
}

// QuotaStore is the per-membership quota override (docs/16 P3-4).
type QuotaStore interface {
	GetUserLimit(ctx context.Context, membershipID string) (UserLimit, bool, error)
	PutUserLimit(ctx context.Context, membershipID string, maxSessions, diskGB int, memLimit int64) error
}

// DEKStore holds the envelope-encrypted per-workspace DEK (docs/15 P3-3).
type DEKStore interface {
	GetWrappedDEK(ctx context.Context, workspaceID string) (ciphertext, keyRef string, ok bool, err error)
	PutWrappedDEK(ctx context.Context, workspaceID, ciphertext, keyRef string) error
}

// SessionIndexStore mirrors the Agent's session list: ReplaceSessions swaps the
// workspace's rows for the current list; ListSessions serves them while the
// container is down.
type SessionIndexStore interface {
	ReplaceSessions(ctx context.Context, workspaceID string, rows []SessionRow) error
	ListSessions(ctx context.Context, workspaceID string) ([]SessionRow, error)
}

// PATStore holds Personal Access Tokens for the MCP endpoint (docs/decisions/0006, P3-6).
type PATStore interface {
	CreatePAT(ctx context.Context, p PAT, tokenHash string) error
	GetPATByHash(ctx context.Context, tokenHash string) (PAT, bool, error)
	ListPATsByIdentity(ctx context.Context, identityID string) ([]PAT, error)
	RevokePAT(ctx context.Context, id, identityID string) error
	TouchPAT(ctx context.Context, id string) error
}

// GitRepoStore is the internal-git repository ledger (docs/reference/
// internal-git-provider, ADR 0010). CreateGitRepo inserts the ledger row (the
// bare is created on disk by the caller); the (tenant_id, name) uniqueness is
// enforced by the schema.
type GitRepoStore interface {
	CreateGitRepo(ctx context.Context, g GitRepo) error
	ListGitReposByTenant(ctx context.Context, tenantID string) ([]GitRepo, error)
	GetGitRepo(ctx context.Context, tenantID, name string) (GitRepo, bool, error)
	CountGitReposByTenant(ctx context.Context, tenantID string) (int, error)
	RenameGitRepo(ctx context.Context, tenantID, oldName, newName string) error
	DeleteGitRepo(ctx context.Context, tenantID, name string) error
}

// LFSObjectStore is the Git LFS object ledger (P3). PutLFSObject records an
// uploaded object (dedup on (tenant, repo, oid)); TenantLFSBytes sums a tenant's
// stored bytes for the capacity quota; the repo-scoped ops keep the ledger in
// step with repo delete/rename (the bytes on disk move with the .git dir).
type LFSObjectStore interface {
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
}

// LFSLockStore is the Git LFS file-lock ledger (P3). CreateLFSLock inserts a
// lock (the (tenant, repo, path) UNIQUE makes a second lock on a path fail —
// the caller pre-checks for the 409). ListLFSLocks paginates by an opaque
// cursor (offset); a filter of "" matches all. The repo-scoped ops keep locks
// in step with repo delete/rename.
type LFSLockStore interface {
	CreateLFSLock(ctx context.Context, l LFSLock) error
	GetLFSLockByPath(ctx context.Context, tenantID, repo, path string) (LFSLock, bool, error)
	GetLFSLock(ctx context.Context, tenantID, repo, id string) (LFSLock, bool, error)
	ListLFSLocks(ctx context.Context, tenantID, repo, filterPath, filterID string, limit int, cursor string) ([]LFSLock, string, error)
	DeleteLFSLock(ctx context.Context, tenantID, repo, id string) error
	DeleteLFSLocksByRepo(ctx context.Context, tenantID, repo string) error
	RenameLFSLocksRepo(ctx context.Context, tenantID, oldRepo, newRepo string) error
}

// AuditStore is the audit log (docs/decisions/0006, P3-6; docs/20 M1).
// InsertAudit records one action; ListAuditByTenant serves the most recent
// entries (newest first) scoped to a tenant, or — when tenantID=="" —
// deployment-wide (super_admin).
type AuditStore interface {
	InsertAudit(ctx context.Context, a AuditLog) error
	ListAuditByTenant(ctx context.Context, tenantID string, limit int) ([]AuditLog, error)
}

// EgressStore aggregates egress observation (docs/20 M2, log-only proxy) and
// holds the allowlist (M3). RecordEgress adds hits into the (day, host,
// allowed) bucket; ListEgress returns the busiest hosts on/after sinceDay
// (YYYY-MM-DD) with their would-allow / would-block split. ListAllowlist
// returns entries with the given state ("" = any); AddAllowlist inserts one;
// SetAllowlistState transitions a row (approve a proposed entry to active, or
// retire one); EffectiveAllowlist returns the active entry strings the proxy
// enforces.
type EgressStore interface {
	RecordEgress(ctx context.Context, day, host string, allowed bool, count int) error
	ListEgress(ctx context.Context, sinceDay string, limit int) ([]EgressStat, error)
	ListAllowlist(ctx context.Context, state string, limit int) ([]AllowlistEntry, error)
	AddAllowlist(ctx context.Context, e AllowlistEntry) error
	SetAllowlistState(ctx context.Context, id, state string) error
	EffectiveAllowlist(ctx context.Context) ([]string, error)
}

// SettingsStore is a small kv for deployment-wide toggles such as the egress
// mode (docs/20 M3). GetSetting returns "" when unset. ListSettingKeys /
// DeleteSetting exist for prefix-scoped cursors (claude audit) so stale keys
// don't accumulate forever.
type SettingsStore interface {
	GetSetting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value string) error
	ListSettingKeys(ctx context.Context, prefix string) ([]string, error)
	DeleteSetting(ctx context.Context, key string) error
}

// UsageStore is showback usage (docs/roadmap.md P3-9). AddUsage accumulates
// workspace running-seconds into the (membership, day) bucket; ListUsage
// returns the per-day rows in [fromDay, toDay] (inclusive, YYYY-MM-DD), scoped
// to one tenant or, when tenantID=="", every tenant (super_admin).
type UsageStore interface {
	AddUsage(ctx context.Context, membershipID, tenantID, day string, secs int) error
	ListUsage(ctx context.Context, tenantID, fromDay, toDay string) ([]UsageRow, error)
}

// SSMStore is the SSM login config (docs/history/p3-ssm-session.md), personal
// scope. No AWS secrets stored. Mutations are scoped by membership so a member
// only touches their own rows. A profile is the common auth bundle; a host
// references one.
type SSMStore interface {
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
}

// MemoStore is the memo queue (docs/21), personal scope. Mutations are scoped
// by membership so a member only touches their own rows. ListMemos returns
// unsent memos plus sent ones still inside the retention window (sent before
// retainBefore are swept).
type MemoStore interface {
	ListMemos(ctx context.Context, membershipID, retainBefore string) ([]Memo, error)
	GetMemo(ctx context.Context, id string) (Memo, bool, error)
	CreateMemo(ctx context.Context, m Memo) error
	UpdateMemo(ctx context.Context, m Memo) error
	DeleteMemo(ctx context.Context, id, membershipID string) error
	MarkMemosSent(ctx context.Context, membershipID string, ids []string, sentAt string) error
	SweepSentMemos(ctx context.Context, retainBefore string) error

	// Categories (docs/21 UI刷新). First-class so they can be created empty, reordered,
	// and renamed (the rename cascades onto the memos via ReassignMemoCategory). All
	// ownership-guarded by membership_id, like the memo methods above.
	ListCategories(ctx context.Context, membershipID string) ([]MemoCategory, error)
	GetCategory(ctx context.Context, id string) (MemoCategory, bool, error)
	CreateCategory(ctx context.Context, c MemoCategory) error
	UpdateCategory(ctx context.Context, c MemoCategory) error
	DeleteCategory(ctx context.Context, id, membershipID string) error
	// ReassignMemoCategory moves every owned memo in (repo, from) to category `to`
	// (to="" empties them). Used by rename/merge and by category delete.
	ReassignMemoCategory(ctx context.Context, membershipID, repo, from, to string) error
}

type NotificationStore interface {
	InsertNotification(ctx context.Context, n Notification) error
	ListNotifications(ctx context.Context, membershipID, retainAfter string, limit int) ([]Notification, error)
	CountUnseenNotifications(ctx context.Context, membershipID, retainAfter string) (int, error)
	MarkNotificationsSeenThrough(ctx context.Context, membershipID string, seq int64, seenAt string) error
	MarkNotificationsSeen(ctx context.Context, membershipID string, eventIDs []string, seenAt string) error
	SweepNotifications(ctx context.Context, retainBefore string) error
	GetUsageNotificationState(ctx context.Context, membershipID, source, windowKey string) (UsageNotificationState, bool, error)
	PutUsageNotificationState(ctx context.Context, state UsageNotificationState) error
}

// ScheduleStore is the scheduled-execution definition store (docs/38 + ADR0021).
// The CP-resident scheduler (scheduler.go) reads ListDueSchedules on every tick and
// stamps the fire ledger via RecordScheduleFire; the operator MCP (P3) drives the
// membership-scoped CRUD. Mutations that a member issues carry membership_id in the
// WHERE so one member never touches another's rows.
type ScheduleStore interface {
	CreateSchedule(ctx context.Context, s Schedule) error
	GetSchedule(ctx context.Context, id string) (Schedule, bool, error)
	ListSchedules(ctx context.Context, membershipID string) ([]Schedule, error)
	// ListDueSchedules returns enabled schedules whose next_run is non-empty and at
	// or before nowRFC (an RFC3339 UTC cutoff; RFC3339 sorts chronologically).
	ListDueSchedules(ctx context.Context, nowRFC string) ([]Schedule, error)
	UpdateSchedule(ctx context.Context, s Schedule) error
	SetScheduleEnabled(ctx context.Context, id, membershipID string, enabled bool, nextRun, updatedAt string) error
	// MarkManualFirePending is run-now: it sets next_run so the ticker fires immediately and
	// flags manual_fire_pending so the resulting run is tagged as a manual fire (docs/38).
	MarkManualFirePending(ctx context.Context, id, membershipID, nextRun, updatedAt string) error
	DeleteSchedule(ctx context.Context, id, membershipID string) error
	// RecordScheduleFire stamps the ledger after a fire attempt: last_run/last_status
	// and the recomputed next_run, disabling the row (enabled=0) when next_run is ""
	// (a spent "once"). The scheduler is the only caller, so no membership scoping.
	RecordScheduleFire(ctx context.Context, id, lastRun, lastStatus, nextRun string, enabled bool, updatedAt string) error
	// SetScheduleReuse persists the reuse ledger (P6): the current long-lived session,
	// when it started, and the fire count since the last rotation. Only reuse schedules
	// use it; the firer calls it, so no membership scoping.
	SetScheduleReuse(ctx context.Context, id, reuseSession, reuseStartedAt string, runCount int, updatedAt string) error
	// AppendScheduleRun inserts a run-history row and trims that schedule's history to
	// the keepN most recent rows so a frequent schedule cannot grow it without bound.
	AppendScheduleRun(ctx context.Context, run ScheduleRun, keepN int) error
	// ListScheduleRuns returns a schedule's most-recent runs (newest first), scoped by
	// membership so a member only sees their own schedule's history.
	ListScheduleRuns(ctx context.Context, scheduleID, membershipID string, limit int) ([]ScheduleRun, error)
}

// MCPServerStore is the tenant-distributed MCP server registry (docs/48 P4 +
// ADR0031). Rows are tenant-scoped: every mutation carries tenant_id in the WHERE so a
// tenant_admin of one tenant can never reach another's definitions even if an id leaks.
// HeadersEnc is stored and returned as opaque ciphertext — decryption is the handler's
// job (mcp_server.go), which keeps the crypto out of the SQL layer.
type MCPServerStore interface {
	ListMCPServers(ctx context.Context, tenantID string) ([]MCPServerRow, error)
	GetMCPServer(ctx context.Context, tenantID, id string) (MCPServerRow, bool, error)
	CreateMCPServer(ctx context.Context, row MCPServerRow) error
	UpdateMCPServer(ctx context.Context, row MCPServerRow) error
	DeleteMCPServer(ctx context.Context, tenantID, id string) error
}

// newID mints an opaque record id (not a strict UUID; sufficient for keys).
// randHex is defined in oauth_bitbucket.go.
func newID() string { return randHex(16) }

func nowTS() string { return time.Now().UTC().Format(time.RFC3339) }
