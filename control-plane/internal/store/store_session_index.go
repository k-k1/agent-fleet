package store

import (
	"context"
	"strings"
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
	// Links are the work items and PRs the session is linked to (#3134), in the Agent's order.
	// Written to session_work_link with the row; ListSessions does not fill it (ListSessionLinks
	// does), so a reader that only counts sessions does not read every link title.
	Links []SessionLink
}

// SessionLink is one mirrored session ↔ work item link. Key is the inbox's shape
// ("owner/repo#45", "PROJ-12"); only non-confidential coordinates, never a body.
type SessionLink struct {
	Provider, Key, Role, Title, URL, At string
	// State is the last state seen for a pull request link ("" = none), as the Agent records it.
	State string
}

// linkFold is the case-insensitive identity of a link within one session, the same
// comparison the Agent makes (owner/repo names compare without case).
func linkFold(l SessionLink) (provider, key string) {
	return strings.ToLower(l.Provider), strings.ToLower(l.Key)
}

// SessionIndexStore mirrors the Agent's session list: ReplaceSessions swaps the
// workspace's rows for the current list; ListSessions serves them while the
// container is down.
type SessionIndexStore interface {
	ReplaceSessions(ctx context.Context, workspaceID string, rows []SessionRow) error
	ListSessions(ctx context.Context, workspaceID string) ([]SessionRow, error)
	// ListSessionLinks is the workspace's mirrored links by session name, each in the Agent's order.
	ListSessionLinks(ctx context.Context, workspaceID string) (map[string][]SessionLink, error)
}

func (s *SQL) ReplaceSessions(ctx context.Context, workspaceID string, rows []SessionRow) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, stmt := range []string{
		`DELETE FROM session_work_link WHERE workspace_id=?`,
		`DELETE FROM session WHERE workspace_id=?`,
	} {
		if _, err := tx.ExecContext(ctx, stmt, workspaceID); err != nil {
			return err
		}
	}
	for _, r := range rows {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO session(workspace_id, name, kind, dir, repo, label, created_at, state, last_seen, carried, studio)
			 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			workspaceID, r.Name, r.Kind, r.Dir, r.Repo, r.Label, r.CreatedAt, r.State, NowTS(), r.Carried, r.Studio); err != nil {
			return err
		}
		if err := insertSessionLinks(ctx, tx, workspaceID, r.Name, r.Links); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// insertSessionLinks writes one session's links in one statement: the mirror is rewritten on
// every list poll, and a round trip per link would hold the transaction open that much
// longer. A second link to the same item (differing only in case) is skipped rather than
// allowed to hit the primary key: the list comes from the Agent, and one odd entry must not
// fail the whole mirror write. At most 51 rows × 11 values (SessionLinksFromJSON's cap), well
// under either dialect's bound-parameter limit.
func insertSessionLinks(ctx context.Context, tx *sqlTx, workspaceID, session string, links []SessionLink) error {
	seen := make(map[[2]string]bool, len(links))
	var q strings.Builder
	var args []any
	for i, l := range links {
		provider, fold := linkFold(l)
		if provider == "" || fold == "" || seen[[2]string{provider, fold}] {
			continue
		}
		seen[[2]string{provider, fold}] = true
		if len(args) == 0 {
			q.WriteString(`INSERT INTO session_work_link(workspace_id, session_name, ord, provider, item_key, item_fold, role, title, url, linked_at, state) VALUES`)
		} else {
			q.WriteString(",")
		}
		q.WriteString("(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)")
		args = append(args, workspaceID, session, i, provider, l.Key, fold, l.Role, l.Title, l.URL, l.At, l.State)
	}
	if len(args) == 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, q.String(), args...)
	return err
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

func (s *SQL) ListSessionLinks(ctx context.Context, workspaceID string) (map[string][]SessionLink, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT session_name, provider, item_key, role, title, url, linked_at, state
		 FROM session_work_link WHERE workspace_id=? ORDER BY session_name, ord`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]SessionLink{}
	for rows.Next() {
		var name string
		var l SessionLink
		if err := rows.Scan(&name, &l.Provider, &l.Key, &l.Role, &l.Title, &l.URL, &l.At, &l.State); err != nil {
			return nil, err
		}
		out[name] = append(out[name], l)
	}
	return out, rows.Err()
}
