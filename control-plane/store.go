package main

import (
	"context"
	"time"
)

// MetadataStore (docs/12 P3-1) — the persistent source of truth for the tenant
// hierarchy, replacing the old in-memory map + docker-inspect reconstruction.
// P3-1 keeps it minimal: tenant / app_user / workspace. The default adapter is
// SQLite (store_sqlite.go); Postgres is added behind this same interface for
// AWS/HA later (docs/12 P3-7). Sessions/repos stay in the Agent (proxied).

type Tenant struct {
	ID, Slug, Name, Status, Limits, Isolation, KeyRef, CreatedAt string
}

type User struct {
	ID, TenantID, Email, UserKey, Role, Status, LastLoginAt string
}

type Workspace struct {
	ID, TenantID, UserID            string
	ContainerName, Network, DataDir string
	AgentPort, AgentToken, State    string
	CreatedAt, LastActiveAt         string
}

// Store is the MetadataStore port. The SQLite adapter is the only implementation
// in P3-1; the interface keeps a Postgres adapter a drop-in for AWS/HA.
type Store interface {
	EnsureDefaultTenant(ctx context.Context) (Tenant, error)
	UpsertUser(ctx context.Context, tenantID, email, key string) (User, error)
	GetWorkspaceByUser(ctx context.Context, userID string) (Workspace, bool, error)
	CreateWorkspace(ctx context.Context, ws Workspace) error
	MaxAgentPort(ctx context.Context) (int, error)
	ListWorkspaces(ctx context.Context, tenantID string) ([]Workspace, error)
	Close() error
}

// newID mints an opaque record id (not a strict UUID; sufficient for keys).
// randHex is defined in oauth_bitbucket.go.
func newID() string { return randHex(16) }

func nowTS() string { return time.Now().UTC().Format(time.RFC3339) }
