package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
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
	// Origin and OriginSession are who raised the session and the session it came from (ADR 0073).
	// The Console classifies scheduled and auto-run runs by them, so the stopped list must carry them.
	Origin, OriginSession string
	// ScheduleID is the schedule that started the session (origin schedule), "" otherwise.
	ScheduleID string
	// QueueCategoryID is the message-queue category whose delivery started the session (origin
	// queue), "" otherwise.
	QueueCategoryID string
	Move            SessionMove
	// Place is where the session runs as the Agent stored it at create (#5219); nil when the
	// Agent reported none. Without it a stopped Workspace's list files a space session under the
	// repository that shares its folder name.
	Place *SessionPlace
	// StoppedAt is when the Agent first saw the session stopped (RFC3339), "" for a live row or
	// one the Agent reported without it. The Console's Active view lists a stopped session for a
	// day after it, so a stopped Workspace's list has to carry it.
	StoppedAt string
	// Links are the work items and PRs the session is linked to (#3134), in the Agent's order.
	// Written to session_work_link with the row; ListSessions does not fill it (ListSessionLinks
	// does), so a reader that only counts sessions does not read every link title.
	Links []SessionLink
}

// SessionMove keeps the queue lineage and move gate in the stopped workspace mirror.
type SessionMove struct {
	Moving         string   `json:"moving,omitempty"`
	MovedTo        string   `json:"movedTo,omitempty"`
	QueueReplaces  []string `json:"queueReplaces,omitempty"`
	QueueSuccessor string   `json:"queueSuccessor,omitempty"`
	PromptPending  bool     `json:"promptPending,omitempty"`
	StoppedLimit   string   `json:"stoppedLimit,omitempty"`
}

func (m SessionMove) column() string {
	if m.Moving == "" && m.MovedTo == "" && m.QueueSuccessor == "" && len(m.QueueReplaces) == 0 && !m.PromptPending && m.StoppedLimit == "" {
		return ""
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// SessionPlace is a mirrored session's place: Kind is repo, worktree, space, home or other, ID
// the folder under ~/repos or ~/spaces, Parent a worktree's main working copy. Stored as JSON in
// the nullable session.place column.
type SessionPlace struct {
	Kind   string `json:"kind"`
	ID     string `json:"id,omitempty"`
	Parent string `json:"parent,omitempty"`
}

// stoppedAtColumn is the value to store in session.stopped_at: the Agent's RFC3339 stop time, or ""
// for anything else. The Agent is the writer, but the row is replaced wholesale: one value the
// column refuses (a NUL byte in Postgres TEXT) would roll back the whole swap and leave every
// session on the previous mirror, so a value that is not a time is dropped instead.
func stoppedAtColumn(v string) string {
	if _, err := time.Parse(time.RFC3339, v); err != nil {
		return ""
	}
	return v
}

// placeColumn encodes p for session.place: NULL for no place.
func placeColumn(p *SessionPlace) any {
	if p == nil || p.Kind == "" {
		return nil
	}
	b, err := json.Marshal(p)
	if err != nil {
		return nil
	}
	return string(b)
}

// placeOfColumn decodes session.place. A NULL, or a value that does not decode to a kind, reads as
// no place, so the Console falls back to the folder name as it does for an older session.
func placeOfColumn(v sql.NullString) *SessionPlace {
	if !v.Valid {
		return nil
	}
	var p SessionPlace
	if err := json.Unmarshal([]byte(v.String), &p); err != nil || p.Kind == "" {
		return nil
	}
	return &p
}

// SessionLink is one mirrored session ↔ work item link. Key is the inbox's shape
// ("owner/repo#45", "PROJ-12"); only non-confidential coordinates, never a body.
type SessionLink struct {
	Provider, Key, Role, Title, URL, At string
	// State is the last state seen for a pull request link ("" = none), as the Agent records it.
	State string
	// Facts are a PR link's merge conditions as last observed; the zero value (no ObservedAt) is
	// no observation, which reads as not ready.
	Facts PRFacts
}

// PRFacts are a mirrored PR link's merge conditions (ADR 0143 decision 4): the CI rollup
// ("success", "failure", "pending", "" for nothing ran), whether changes were requested, whether
// it conflicts, the Agent's ready-to-merge verdict, and when they were observed (RFC3339, "" for
// never).
type PRFacts struct {
	Checks                            string
	ChangesRequested, Conflict, Ready bool
	ObservedAt                        string
}

// boolColumn is b as the 0/1 INTEGER both dialects store booleans as.
func boolColumn(b bool) int {
	if b {
		return 1
	}
	return 0
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
			`INSERT INTO session(workspace_id, name, kind, dir, repo, label, created_at, state, last_seen, carried, studio, origin, origin_session, schedule_id, queue_category_id, place, queue_move, stopped_at)
			 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			workspaceID, r.Name, r.Kind, r.Dir, r.Repo, r.Label, r.CreatedAt, r.State, NowTS(), r.Carried, r.Studio, dropNUL(r.Origin), dropNUL(r.OriginSession), dropNUL(r.ScheduleID), dropNUL(r.QueueCategoryID), placeColumn(r.Place), r.Move.column(), stoppedAtColumn(r.StoppedAt)); err != nil {
			return err
		}
		if err := insertSessionLinks(ctx, tx, workspaceID, r.Name, r.Links); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// dropNUL empties a value that holds a NUL byte: PostgreSQL TEXT rejects it, and one rejected row
// would roll back the whole ReplaceSessions swap. Stripping the byte instead would forge a valid
// classification ("qu\x00eue" -> "queue") that the Console then acts on, so the field is dropped whole.
func dropNUL(v string) string {
	if strings.ContainsRune(v, 0) {
		return ""
	}
	return v
}

// insertSessionLinks writes one session's links in one statement: the mirror is rewritten on
// every list poll, and a round trip per link would hold the transaction open that much
// longer. A second link to the same item (differing only in case) is skipped rather than
// allowed to hit the primary key: the list comes from the Agent, and one odd entry must not
// fail the whole mirror write. At most 51 rows × 16 values (SessionLinksFromJSON's cap), well
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
			q.WriteString(`INSERT INTO session_work_link(workspace_id, session_name, ord, provider, item_key, item_fold, role, title, url, linked_at, state, checks, changes_requested, conflict, ready, observed_at) VALUES`)
		} else {
			q.WriteString(",")
		}
		q.WriteString("(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)")
		f := l.Facts
		args = append(args, workspaceID, session, i, provider, l.Key, fold, l.Role, l.Title, l.URL, l.At, l.State,
			f.Checks, boolColumn(f.ChangesRequested), boolColumn(f.Conflict), boolColumn(f.Ready), f.ObservedAt)
	}
	if len(args) == 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, q.String(), args...)
	return err
}

func (s *SQL) ListSessions(ctx context.Context, workspaceID string) ([]SessionRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT name, kind, dir, repo, label, created_at, state, last_seen, carried, studio, origin, origin_session, schedule_id, queue_category_id, place, queue_move, stopped_at
		 FROM session WHERE workspace_id=? ORDER BY created_at DESC`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionRow
	for rows.Next() {
		r := SessionRow{WorkspaceID: workspaceID}
		var place sql.NullString
		var move string
		if err := rows.Scan(&r.Name, &r.Kind, &r.Dir, &r.Repo, &r.Label, &r.CreatedAt, &r.State, &r.LastSeen, &r.Carried, &r.Studio, &r.Origin, &r.OriginSession, &r.ScheduleID, &r.QueueCategoryID, &place, &move, &r.StoppedAt); err != nil {
			return nil, err
		}
		r.Place = placeOfColumn(place)
		if move != "" {
			_ = json.Unmarshal([]byte(move), &r.Move)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *SQL) ListSessionLinks(ctx context.Context, workspaceID string) (map[string][]SessionLink, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT session_name, provider, item_key, role, title, url, linked_at, state, checks, changes_requested, conflict, ready, observed_at
		 FROM session_work_link WHERE workspace_id=? ORDER BY session_name, ord`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]SessionLink{}
	for rows.Next() {
		var name string
		var l SessionLink
		var changes, conflict, ready int
		if err := rows.Scan(&name, &l.Provider, &l.Key, &l.Role, &l.Title, &l.URL, &l.At, &l.State,
			&l.Facts.Checks, &changes, &conflict, &ready, &l.Facts.ObservedAt); err != nil {
			return nil, err
		}
		l.Facts.ChangesRequested, l.Facts.Conflict, l.Facts.Ready = changes != 0, conflict != 0, ready != 0
		out[name] = append(out[name], l)
	}
	return out, rows.Err()
}
