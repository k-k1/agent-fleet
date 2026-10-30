package store

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

// Store is the MetadataStore port — the union of the feature-scoped sub-
// interfaces below (docs/log/23 P2-W3). The implementation stays a single SQL layer
// shared by sqlite and postgres. Callers normally hold Store, but a self-contained
// component may depend on the narrowest sub-interface it needs instead (git_gc.go, for
// example). A new method goes on the sub-interface it belongs to.
type Store interface {
	TenantStore
	IdentityStore
	MembershipStore
	WorkspaceStore
	QuotaStore
	DEKStore
	SessionIndexStore
	PATStore
	MCPOAuthStore
	MCPClientOAuthStore
	GitRepoStore
	LFSObjectStore
	LFSLockStore
	AuditStore
	EgressStore
	EgressAttributionStore
	SettingsStore
	UsageStore
	EngineModelStore
	EngineIngestStore
	EngineUsageAttributionStore
	CloudCostStore
	SSMStore
	GCPProfileStore
	MemoStore
	WorkItemStore
	NotificationStore
	ScheduleStore
	MCPServerStore
	SessionShareStore
	TenantIdPStore
	TenantGitOAuthStore
	EngineAccessStore
	TenantBranchRulesStore
	PreviewTempPortStore
	DevIdPStore
	MemberUIStateStore
	WorkspaceDiskStore
	WorkspaceBootStore
	TenantMemberDefaultsStore
	HomeOperationStore
	CPLeaseStore
	LimitBoardStore
	InboxStore

	// Ping backs GET /readyz. Not in a sub-interface: "is the database reachable"
	// belongs to the store as a whole, not to a feature.
	Ping(ctx context.Context) error
	Close() error
}

// NewID mints an opaque record id (not a strict UUID; sufficient for keys).
// randHex is defined in oauth_bitbucket.go.
func NewID() string { return randHex(16) }

func NowTS() string { return time.Now().UTC().Format(time.RFC3339) }
