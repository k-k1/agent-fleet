package store

import (
	"context"
)

// SessionRow mirrors one Agent session into the CP DB so the session list can be
// served while the Workspace container is stopped (the Agent is the source of
// truth when running). state is "running" | "stopped".
type SessionRow struct {
	WorkspaceID, Name, Kind, Dir, Repo, Label string
	CreatedAt, State, LastSeen                string
	// Carried is the kind of interaction that was still waiting for an answer when the
	// session was folded away (docs/log/75 §75.6.5). While the Workspace is stopped this
	// mirror is the list's only source, so anything not recorded here did not happen as
	// far as the Console is concerned.
	Carried string
	// Studio is the image studio the session is bound to (ADR 0100 decision 2), "" for none.
	Studio string
}

// SessionIndexStore mirrors the Agent's session list: ReplaceSessions swaps the
// workspace's rows for the current list; ListSessions serves them while the
// container is down.
type SessionIndexStore interface {
	ReplaceSessions(ctx context.Context, workspaceID string, rows []SessionRow) error
	ListSessions(ctx context.Context, workspaceID string) ([]SessionRow, error)
}

func (s *SQL) ReplaceSessions(ctx context.Context, workspaceID string, rows []SessionRow) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM session WHERE workspace_id=?`, workspaceID); err != nil {
		return err
	}
	for _, r := range rows {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO session(workspace_id, name, kind, dir, repo, label, created_at, state, last_seen, carried, studio)
			 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			workspaceID, r.Name, r.Kind, r.Dir, r.Repo, r.Label, r.CreatedAt, r.State, NowTS(), r.Carried, r.Studio); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQL) ListSessions(ctx context.Context, workspaceID string) ([]SessionRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT name, kind, dir, repo, label, created_at, state, last_seen, carried, studio
		 FROM session WHERE workspace_id=? ORDER BY created_at DESC`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionRow
	for rows.Next() {
		r := SessionRow{WorkspaceID: workspaceID}
		if err := rows.Scan(&r.Name, &r.Kind, &r.Dir, &r.Repo, &r.Label, &r.CreatedAt, &r.State, &r.LastSeen, &r.Carried, &r.Studio); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
