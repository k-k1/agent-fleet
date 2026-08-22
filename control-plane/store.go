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
	// Per-tenant login rules (docs/61 §61.9.7, migration 0039). All CSV, all
	// optional; see TenantLoginRules for what each one governs and why there is no
	// allowed_emails among them. HiddenProviders (0042) is the one that is DISPLAY
	// only — accepted methods to leave off this tenant's login page (§61.15.9).
	AllowedProviders, AutoJoinDomains, AllowedDomains, HiddenProviders string
}

// TenantLoginRules is the login-relevant slice of a tenant, as loaded in bulk by
// the entry gate's cache (docs/61 §61.9.6/§61.9.7). Slug is carried because the
// deterministic tie-break for two tenants claiming the same auto-join domain is
// "lowest slug wins" (§61.9.8).
type TenantLoginRules struct {
	ID, Slug, Name                                    string
	AllowedProviders, AutoJoinDomains, AllowedDomains []string
	// HiddenProviders is not a gate. It only removes buttons from /login/<slug>;
	// the same method still admits its people (docs/61 §61.15.9 + 決定 14).
	HiddenProviders []string
	// AllowedCIDRs restricts which source networks may USE this tenant (docs/66,
	// ADR 0047). Empty = no restriction, and that is how the feature is switched off.
	// Unlike the fields above it belongs to the tenant_admin: it reaches nothing
	// outside this tenant. It is stored here rather than in limits JSON because it is
	// consulted on every request and these rules already have a cache in front of them.
	AllowedCIDRs []string
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

// UserQuota is the settable part of a per-membership quota override — the three
// workspace size axes plus the session count. All are 0 = unset, meaning "use the
// deployment default". The axes are held as independent numbers rather than as a
// named size (S/M/L…): the named sizes live in the Console and MCP as a way to
// PRESENT valid combinations, while storage stays runtime-neutral so docker keeps
// its byte-exact --memory and --cpus (ADR 0044 決定 1).
type UserQuota struct {
	MaxSessions int
	// DiskGB is the per-workspace working disk in GiB. On ECS it becomes the task's
	// ephemeral storage (21–200 GiB) or, above that, an ECS-managed EBS volume
	// (ADR 0044 決定 2). On docker it stays the display-only quota it has always been.
	DiskGB int
	// MemLimit is the per-workspace RAM cap in BYTES (0 = unset → deployment default
	// WS_MEMORY / AF_ECS_TASK_MEMORY). A tenant_admin sets it within the tenant cap
	// (tenantLimits.MaxWorkspaceMem); resolveWorkspaceMemBytes clamps and applies it at
	// container start (docker --memory / ECS task size). See docs/26 / roadmap P3-4.
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
	// say HOW BIG, this says WHICH LADDER (docs/70 §70.4). Still runtime-neutral in the
	// sense that matters: the id is opaque here, and the operator alone maps it to
	// instance types and an architecture (ADR 0044 決定 1).
	SlotClass string
}

// UserLimit is a per-membership quota override (docs/16 P3-4) as stored.
type UserLimit struct {
	MembershipID string
	UserQuota
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
	// TenantSlug is the owning tenant's slug, joined in by every read rather than
	// stored on the row (the tenant table owns it). It exists because the AWS
	// adapters stamp `af-tenant` on billable resources and the cost-allocation tag
	// has to be READABLE in Cost Explorer — an opaque tenant id there would say
	// nothing the membership id does not already imply (docs/67 §67.4, ADR 0048
	// 決定 3)。Empty on a Workspace built in memory by a test.
	TenantSlug                   string
	AgentPort, AgentToken, State string
	CreatedAt, LastActiveAt      string
	// MemBytes is the RESOLVED per-workspace RAM cap in bytes for the NEXT container
	// start (0 = use the runtime's deployment default). It is NOT a persisted column:
	// buildResolved fills it via resolveWorkspaceMemBytes before the runtime is built,
	// and the factory (docker --memory / ECS task size) honors it when >0. Read/stop
	// call sites leave it 0, which never needs a memory value.
	MemBytes int64
	// CPUUnits and DiskGB are the other two RESOLVED size axes for the next container
	// start (0 = deployment default), filled alongside MemBytes by buildResolved.
	// CPUUnits is in Fargate CPU units (1024 = 1 vCPU); DiskGB is the working disk in
	// GiB. Like MemBytes they are not persisted columns — the persisted values live in
	// user_limit and are resolved through the tenant cap on the way here (ADR 0044).
	CPUUnits int
	DiskGB   int
	// SlotClass is the RESOLVED machine class for the next container start (docs/70).
	// Same lifecycle as the three axes above — not a persisted column, filled by
	// buildResolved through resolveSlotClass, and read only by the ecs-ec2 factory.
	// "" means "the deployment default", which is also what every other runtime sees.
	SlotClass string
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
	CloudCostStore
	SSMStore
	MemoStore
	NotificationStore
	ScheduleStore
	MCPServerStore
	SessionShareStore
	TenantIdPStore
	TenantGitOAuthStore

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
	// DeleteTenant removes an EMPTY tenant (its leftover inactive memberships, its
	// configuration rows, and the row itself). Irreversible, super_admin only, and the
	// emptiness is proved by the handler — see deleteTenant in tenants.go for the five
	// refusals and why each one exists.
	DeleteTenant(ctx context.Context, tenantID string) error
	// SetTenantLogin stores the three CSV login rules (docs/61 §61.9.7). Values are
	// normalized by the caller (lowercased, deduped); this only writes them.
	// hiddenProviders is DISPLAY only (0042, docs/61 §61.15.9) — accepted methods to
	// leave off this tenant's login page, never a gate.
	SetTenantLogin(ctx context.Context, tenantID, allowedProviders, autoJoinDomains, allowedDomains, hiddenProviders string) error
	// ListTenantLoginRules is the entry gate's bulk read: one query behind a short
	// TTL cache rather than a per-request lookup (§61.9.7).
	ListTenantLoginRules(ctx context.Context) ([]TenantLoginRules, error)
	// The tenant's source-network restriction (docs/66, ADR 0047). Kept apart from
	// SetTenantLogin because the owner differs: those three fields are the operator's,
	// this one is the tenant_admin's. "" = no restriction.
	GetTenantAllowedCIDRs(ctx context.Context, tenantID string) (string, error)
	SetTenantAllowedCIDRs(ctx context.Context, tenantID, cidrs string) error
}

// TenantIdP is one tenant-defined login provider (docs/61 §61.11, migration 0040).
// SecretEnc is opaque ciphertext here exactly like MCPServerRow.HeadersEnc: the
// SQL layer stores and returns it, and tenant_idp.go owns the sealing.
//
// ★ Status governs everything. Only "active" rows are turned into a provider, and
// a row is born "pending" until a super_admin approves it (決定 30). ApprovedBy /
// ApprovedAt are the copy of that approval kept next to the row — the audit ledger
// is the record.
type TenantIdP struct {
	ID, TenantID, Name string
	LabelJA, LabelEN   string
	// Kind selects the adapter the row is built into and therefore which of the
	// fields below mean anything: "oidc" (the P4 default) uses Issuer/Trust/
	// AllowedTIDs, "github" uses AllowedOrgs and pins Issuer to https://github.com
	// (docs/61 §61.15 + 決定 35).
	Kind                        string
	Issuer, ClientID            string
	SecretEnc, KeyRef           string
	Trust                       string
	AllowedTIDs, AllowedDomains string // CSV
	AllowedOrgs                 string // CSV, kind=github only
	// LinkClaim names the stable claim rule 1.5 should match on, for an issuer whose
	// `sub` is pairwise (docs/61 §61.15.10). ★ A tenant may only name a claim from a
	// closed list (tenantLinkClaims): naming `email` or `upn` would build an
	// email join inside a shared realm, which is precisely what 決定 32 refuses. The
	// VALUE is never taken from this row — only the name is configuration.
	LinkClaim                       string
	Status                          string // pending | active | suspended
	ApprovedBy, ApprovedAt          string
	CreatedBy, CreatedAt, UpdatedAt string
}

// TenantIdPStore is the tenant-defined login provider registry (docs/61 §61.11).
// Every mutation carries tenant_id in the WHERE, the way MCPServerStore does, so a
// tenant_admin of one tenant can never reach another's row even if an id leaks.
// ListActiveTenantIdPs is the exception by design: it is the deployment-wide read
// the login layer needs to assemble the provider set, and it is never reached from
// a tenant-scoped handler.
// TenantRef is the owning tenant of a tenant_idp row, as the deployment-wide reads
// carry it: the slug because the provider id the rest of CP sees is built from it
// (t:<slug>:<name>), and the DISPLAY NAME because the default button label has to
// say WHICH company's method it is — otherwise a tenant's GitHub row and the
// deployment's GitHub button render the same text on one page (docs/61 §61.15.10).
type TenantRef struct{ Slug, Name string }

type TenantIdPStore interface {
	ListTenantIdPs(ctx context.Context, tenantID string) ([]TenantIdP, error)
	// ListAllTenantIdPs returns every row with its tenant, for the super_admin
	// approval queue. The tenants are returned separately (keyed by tenant id) so
	// the store stays free of view structs; the handler joins them.
	ListAllTenantIdPs(ctx context.Context) ([]TenantIdP, map[string]TenantRef, error)
	// ListActiveTenantIdPs returns the approved rows only — the login layer's bulk
	// read, behind the same short TTL cache the tenant rules use.
	ListActiveTenantIdPs(ctx context.Context) ([]TenantIdP, map[string]TenantRef, error)
	GetTenantIdP(ctx context.Context, tenantID, id string) (TenantIdP, bool, error)
	CreateTenantIdP(ctx context.Context, row TenantIdP) error
	UpdateTenantIdP(ctx context.Context, row TenantIdP) error
	// SetTenantIdPStatus is the approval / suspension path, kept apart from the
	// content update so approving cannot accidentally rewrite the issuer that was
	// being approved.
	SetTenantIdPStatus(ctx context.Context, tenantID, id, status, approvedBy, approvedAt, updatedAt string) error
	DeleteTenantIdP(ctx context.Context, tenantID, id string) error
	// TenantIdPIssuerInUse reports whether some OTHER row already names this issuer
	// (any tenant). It is the "second app registration of the same directory" signal
	// (docs/61 §61.17.4 (b)): the same person then arrives with a DIFFERENT subject
	// if the issuer is pairwise, which rule 2' refuses as email_taken. Deployment-wide
	// on purpose — identities are deployment-wide, so a row in another tenant splits
	// the same person just as effectively.
	TenantIdPIssuerInUse(ctx context.Context, issuer, excludeID string) (bool, error)
	// CountMembersOnlyOnProvider counts a tenant's ACTIVE members whose only proven
	// sign-in is this provider — the people who would have no way in if it stopped
	// (docs/61 §61.17.4 の順序). Used to warn before suspending, never to refuse:
	// stopping a compromised IdP must stay faster than starting one.
	CountMembersOnlyOnProvider(ctx context.Context, tenantID, providerID string) (int, error)
}

// TenantGitOAuth is one tenant's OAuth app for a git provider (docs/71, migration
// 0048). SecretEnc is opaque ciphertext here exactly like TenantIdP.SecretEnc: the
// SQL layer stores and returns it, tenant_git_oauth.go owns the sealing.
//
// ★ There is no status. Unlike TenantIdP, this row declares nothing about who
// anybody is — it only names the OAuth app a member's "connect GitHub / Bitbucket"
// button talks to — so it takes effect the moment the tenant_admin saves it
// (ADR0052 決定 3). An empty SecretEnc is normal for GitHub: its device flow
// authenticates with the client_id alone.
type TenantGitOAuth struct {
	ID, TenantID, Provider string
	ClientID               string
	SecretEnc, KeyRef      string
	UpdatedBy              string
	CreatedAt, UpdatedAt   string
}

// TenantGitOAuthStore is the per-tenant git provider OAuth app registry (docs/71).
// Every call carries tenant_id, the way TenantIdPStore does, so a tenant_admin of
// one tenant can never read or write another's app — and the secret in particular
// never leaves the tenant it was sealed for.
type TenantGitOAuthStore interface {
	ListTenantGitOAuth(ctx context.Context, tenantID string) ([]TenantGitOAuth, error)
	GetTenantGitOAuth(ctx context.Context, tenantID, provider string) (TenantGitOAuth, bool, error)
	// PutTenantGitOAuth is an upsert on (tenant_id, provider): a tenant has one app
	// per host, so "create" and "edit" are the same act and splitting them would only
	// invite a duplicate row the unique index then refuses.
	PutTenantGitOAuth(ctx context.Context, row TenantGitOAuth) error
	DeleteTenantGitOAuth(ctx context.Context, tenantID, provider string) error
}

// IdentityLink is one proven login, on its way to LinkIdentity. It is a struct
// rather than eight positional parameters because every field but one is a string
// and this is the identity path: a swapped pair here hands somebody another
// person's workspace, and `Realm: x, Subject: y` says which is which at the call
// site.
type IdentityLink struct {
	Provider string // the button that was pressed (env id, or t:<slug>:<name>)
	Subject  string // the IdP's stable id for this account
	// Realm is WHERE Subject was proven: the issuer URL for OIDC, and
	// https://github.com for the GitHub adapter. Two providers sharing a realm are
	// two buttons onto the same IdP, so the same subject there is the same person —
	// that is rule 1.5, and it is the one join a tenant-defined provider is allowed
	// to make, because the realm is asserted by the adapter and verified against
	// that IdP, never taken from the tenant's row (docs/61 §61.15).
	Realm string
	// RealmClaim / RealmSubject are rule 1.5's SECOND key (docs/61 §61.15.10 + 決定
	// 38). Some IdPs make `sub` pairwise — Entra's is a function of (app registration,
	// user) — so the same person through two app registrations on one issuer is two
	// subjects and rule 1.5 never fires. A provider may therefore also name a stable
	// claim (`oid`): RealmClaim is WHICH claim was read, RealmSubject is what it
	// carried. Both must match for the rule to join, so two providers reading
	// different claims never join on a coincidental value collision.
	//
	// ★ Subject keeps meaning `sub`. Replacing it would change the key of every row
	// already written, and a tenant-defined provider — which has no rule 2 to fall
	// back on — would refuse its own existing people as email_taken.
	RealmClaim   string
	RealmSubject string
	Email        string
	FallbackKey  string // sanitizeUser(email), used only when a new identity is created
	RoleHint     string
	EmailJoin    bool
}

// LinkedProvider is one sign-in method bound to a person, as shown on their own
// account panel (docs/61 §61.16). Subject is included because it is the only
// stable handle on the row; the Console shows the label and the address instead.
type LinkedProvider struct {
	Provider    string
	Subject     string
	Realm       string
	Email       string
	CreatedAt   string
	LastLoginAt string
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
	//
	// ★ EmailJoin selects rule 2 ("this address already belongs to someone — join
	// that identity"). It is FALSE for a tenant-defined provider (docs/61 §61.11 +
	// 決定 32): its issuer belongs to the subsidiary, not to the operator, so an
	// admin there could otherwise mint a token carrying a colleague's address and
	// land in that colleague's identity — home, secrets and all. The caller decides,
	// because the naming rule that distinguishes the two kinds of provider
	// (tenantProviderID) belongs to the login layer, not to SQL.
	LinkIdentity(ctx context.Context, link IdentityLink) (ident Identity, isNew bool, err error)
	// FillProviderRealm stamps the realm on the rows a provider wrote before the
	// column existed (or through a path that had no provider object). Startup-only
	// and idempotent — see the implementation for why it cannot be a migration.
	FillProviderRealm(ctx context.Context, provider, realm string) error
	// ListLinkedProviders returns the sign-in methods bound to one person, newest
	// login first. It is the read half of the "link a second sign-in method" flow
	// (docs/61 §61.16) and carries nothing secret — the pairs it lists are exactly
	// what the person proved by signing in.
	ListLinkedProviders(ctx context.Context, identityID string) ([]LinkedProvider, error)
	// AttachProvider binds an ALREADY-PROVEN (provider, subject) to an EXISTING
	// identity, at that person's own request (docs/61 §61.16 + 決定 37). It differs
	// from LinkIdentity in three ways that are the whole point:
	//
	//   - it never creates an identity and never resolves one — the caller passes the
	//     identity that is already signed in;
	//   - it never touches the identity row: not the email, and above all not the
	//     role (決定 31 — a linked method must not be able to hand out a deployment
	//     role, the same reason roleHint is suppressed for tenant-defined providers);
	//   - it REFUSES rather than re-points when the pair (or the same IdP account
	//     reached through another button — rule 1.5) already belongs to somebody:
	//     errLinkTaken. Joining two accounts that both have a login history is a
	//     merge, and a merge cannot be undone (§61.5).
	AttachProvider(ctx context.Context, identityID string, link IdentityLink) error
	// DetachProvider removes one sign-in method from a person's account (docs/61
	// §61.16.4, the deferred half of 決定 37). identityID is always in the WHERE, so
	// no caller can reach somebody else's row even with a (provider, subject) it
	// guessed.
	//
	// ★ It REFUSES to remove the last one (errLastLoginMethod). An account with no
	// method left cannot be signed into at all, and there is no recovery path from
	// the Console — this deployment has no password and no SMTP (決定 28). The count
	// is taken in SQL, in the same statement, because the caller's check and the
	// delete are otherwise two moments with a browser tab in between.
	// errNoSuchLoginMethod means the pair is not this person's (or not there at all);
	// the two are separate so the API can answer 409 and 404 rather than one 400.
	DetachProvider(ctx context.Context, identityID, provider, subject string) error
	GetIdentityByID(ctx context.Context, id string) (Identity, bool, error)
	// GetIdentityByUserKey is the READ-ONLY lookup for view paths (admin stats,
	// admin MCP list tools): unlike UpsertIdentity it neither inserts a row for a
	// mistyped key nor touches last_login_at.
	GetIdentityByUserKey(ctx context.Context, key string) (Identity, bool, error)
	// DemoteSuperAdmins drops every identity whose deployment role is super_admin
	// and whose email is NOT in keep back to "user", and returns the demoted
	// addresses. SUPER_ADMIN_EMAILS is the single source of truth and CP runs this
	// once at STARTUP (ADR0043 決定 24): a login-time sync would never reach the
	// person who left, because the person who left never logs in again. Identities
	// with an empty email are left alone — they cannot be named in the env, so
	// demoting them would be unrecoverable by the documented procedure.
	DemoteSuperAdmins(ctx context.Context, keep []string) ([]string, error)
}

type MembershipStore interface {
	ListMemberships(ctx context.Context, identityID string) ([]MembershipView, error)
	ListMembersByTenant(ctx context.Context, tenantID string) ([]MemberInfo, error)
	// ListRemovedMembersByTenant returns the tenant's DEACTIVATED memberships.
	// Deliberately a second method rather than a flag on the one above: every other
	// caller of that one (share targets, the MCP admin tools, the session overview)
	// reads it as "who is a member of this tenant" and must never be handed somebody
	// who was taken off the roster. The admin roster still needs them, because
	// offboarding runs remove → stop workspace → clean home and the last two steps
	// happen when the person is already off the list (docs/61 §61.10.6).
	ListRemovedMembersByTenant(ctx context.Context, tenantID string) ([]MemberInfo, error)
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
	// SetMembershipStatus deactivates (or restores) a membership. Offboarding is a
	// LOGICAL delete: the workspace, its home and its secrets survive, but every
	// path that resolves a membership already requires status='active', so the
	// person is locked out on the next request (docs/61 §61.10.6 / 決定 22).
	// This is what offboarding means, and it stays the default: DeleteMembership
	// below is a second, deliberate step taken later.
	SetMembershipStatus(ctx context.Context, membershipID, status string) error
	// DeleteMembership removes the row itself and everything keyed to it
	// (membershipCascade). It is the last step of the clean-up sequence — remove →
	// destroy the workspace → delete the row — and it is irreversible.
	//
	// ★ It used to be "deliberately not offered", on the grounds that schedules, audit
	// rows and shares reference the membership id. Two of those three turned out not to
	// be reasons: the schedules and shares ARE this person's and go with them, and
	// audit_log never referenced a membership at all (its actor is an identity). What
	// the reason really protected is the HISTORY — the audit ledger, and the cost and
	// occupancy rows an admin may still have to answer for — so those are what is kept
	// (docs/61 §61.18).
	//
	// Callers must have deactivated the membership and destroyed its workspace first;
	// this does not release a home or any cloud resource.
	DeleteMembership(ctx context.Context, membershipID string) error
	// EmailHasActiveMembership reports whether the person at this address is on any
	// tenant's roster. This is the term decision 16 adds to the entry gate: being
	// invited is itself permission to reach the login, so an invite-run deployment
	// need not also maintain AF_OAUTH_ALLOWED_*.
	// ★ Matched on identity.email, NOT on sanitizeUser(email): since P1 a person may
	// keep a user_key that no longer derives from their current address.
	EmailHasActiveMembership(ctx context.Context, email string) (bool, error)
	// EmailHasActiveMembershipInTenant is the same question asked of ONE tenant. A
	// tenant-defined provider (docs/61 §61.11) may only admit that tenant's own
	// people, so its entry gate cannot use the deployment-wide answer above: being
	// on some other subsidiary's roster is not permission to use this subsidiary's
	// IdP (決定 32-3).
	EmailHasActiveMembershipInTenant(ctx context.Context, email, tenantID string) (bool, error)
	// AnyActiveMembership reports whether the deployment has any roster at all —
	// used by the startup warning, which must no longer claim "every login is
	// denied" on a deployment that runs purely on invitations.
	AnyActiveMembership(ctx context.Context) (bool, error)
	// MembershipOwnerName resolves a membership to a human display label (email, or
	// the user key) — e.g. for stamping on an LFS lock. "" when it can't be resolved.
	MembershipOwnerName(ctx context.Context, membershipID string) (string, error)
}

type WorkspaceStore interface {
	GetWorkspaceByMembership(ctx context.Context, membershipID string) (Workspace, bool, error)
	CreateWorkspace(ctx context.Context, ws Workspace) error
	// DeleteWorkspace removes the row and its dependents. Irreversible, and only ever
	// reached through the explicit destroy operation (ADR 0045 決定 13-2).
	DeleteWorkspace(ctx context.Context, workspaceID string) error
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
	PutUserLimit(ctx context.Context, membershipID string, q UserQuota) error
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

// CloudCostRow is one (day, membership, service) slice of the AWS invoice, as landed
// by the Cost Explorer poller (docs/67, ADR 0048). MembershipID=="" is the SHARED
// bucket — infrastructure that belongs to nobody. Amounts are integer micro-units of
// Currency; they are never converted to another currency and never divided among
// people (決定 4 / 決定 6).
type CloudCostRow struct {
	Day, MembershipID, TenantID, Service string
	Unblended, Amortized                 int64
	Currency                             string
	Estimated                            bool
}

// CloudCostTotal is one member's attributed spend over a window, enriched with the
// labels needed to name them. The labels are resolved at READ time by join, exactly
// like UsageRow: a membership that has been deleted leaves rows whose money is still
// real, and they surface with empty UserKey/Email rather than vanishing.
type CloudCostTotal struct {
	TenantSlug, MembershipID, UserKey, Email string
	Unblended, Amortized                     int64
	Currency                                 string
}

// CloudCostStore holds the invoice slices the Cost Explorer poller writes.
//
// PutCloudCost REPLACES a day wholesale rather than accumulating: Cost Explorer
// restates recent days (they arrive `Estimated` and move for ~24h), so the poller
// re-fetches a trailing window every run and the newest answer has to win. An
// accumulating write would double every re-fetch — the opposite of AddUsage next door,
// and the reason these two look similar but must not share code.
type CloudCostStore interface {
	// PutCloudCost replaces every row for the given days with rows. days lists exactly
	// the days the caller re-fetched, so a day that came back empty is emptied here too.
	PutCloudCost(ctx context.Context, days []string, rows []CloudCostRow) error
	// ListCloudCost returns rows in [fromDay,toDay]. tenantID=="" spans every tenant;
	// membershipID!="" narrows to one person (the member's own view).
	ListCloudCost(ctx context.Context, tenantID, membershipID, fromDay, toDay string) ([]CloudCostRow, error)
	// CloudCostTotals aggregates the same window per member, with labels joined in.
	CloudCostTotals(ctx context.Context, tenantID, fromDay, toDay string) ([]CloudCostTotal, error)
	// CloudCostDays reports which days have any row at all, so the API can tell
	// "nothing was spent" apart from "the poller has never covered this range" —
	// the difference matters because cost allocation cannot be backfilled.
	CloudCostDays(ctx context.Context) (first, last string, err error)
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
