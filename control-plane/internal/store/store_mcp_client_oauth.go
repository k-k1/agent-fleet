package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
)

// MCPClientOAuthState is one MCP sign-in in flight (ADR 0119 decision 4): which member started
// it, for which membership and workspace, against which registered server. Issuer and
// Registration are what the Agent reported when it prepared the authorization URL, kept so the
// audit row and the redirect can name them even when the callback fails.
type MCPClientOAuthState struct {
	IdentityID, MembershipID, TenantID, WorkspaceID string
	ServerID, ServerName, Issuer, Registration      string
	CreatedAt, ExpiresAt                            int64
}

// MCPClientOAuthStore holds the sign-in state of MCP servers that require OAuth.
type MCPClientOAuthStore interface {
	// PutMCPClientOAuthState records a sign-in under the SHA-256 of state and purges the
	// rows already expired at now.
	PutMCPClientOAuthState(ctx context.Context, state string, s MCPClientOAuthState, now int64) error
	// TakeMCPClientOAuthState deletes the row for state and returns it, in one statement: of
	// any number of concurrent callers exactly one gets ok. Expired rows are deleted too and
	// return ok=false with the row, so the caller can tell "expired" from "never existed".
	TakeMCPClientOAuthState(ctx context.Context, state string, now int64) (s MCPClientOAuthState, ok, expired bool, err error)
}

func mcpClientOAuthHash(state string) string {
	sum := sha256.Sum256([]byte(state))
	return hex.EncodeToString(sum[:])
}

func (s *SQL) PutMCPClientOAuthState(ctx context.Context, state string, st MCPClientOAuthState, now int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM mcp_client_oauth_state WHERE expires_at<=?`, now); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO mcp_client_oauth_state(state_hash, identity_id, membership_id, tenant_id, workspace_id,
		   server_id, server_name, issuer, registration, created_at, expires_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		mcpClientOAuthHash(state), st.IdentityID, st.MembershipID, st.TenantID, st.WorkspaceID,
		st.ServerID, st.ServerName, st.Issuer, st.Registration, now, st.ExpiresAt)
	return err
}

func (s *SQL) TakeMCPClientOAuthState(ctx context.Context, state string, now int64) (MCPClientOAuthState, bool, bool, error) {
	var st MCPClientOAuthState
	err := s.db.QueryRowContext(ctx,
		`DELETE FROM mcp_client_oauth_state WHERE state_hash=?
		 RETURNING identity_id, membership_id, tenant_id, workspace_id, server_id, server_name,
		   issuer, registration, created_at, expires_at`,
		mcpClientOAuthHash(state)).Scan(&st.IdentityID, &st.MembershipID, &st.TenantID, &st.WorkspaceID,
		&st.ServerID, &st.ServerName, &st.Issuer, &st.Registration, &st.CreatedAt, &st.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return MCPClientOAuthState{}, false, false, nil
	}
	if err != nil {
		return MCPClientOAuthState{}, false, false, err
	}
	if st.ExpiresAt <= now {
		return st, false, true, nil
	}
	return st, true, false, nil
}
