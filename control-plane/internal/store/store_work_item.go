package store

import (
	"context"
	"database/sql"
	"errors"
)

// WorkItemQuery is one saved search that feeds the work item inbox (docs/log/80). af holds
// the query, not a copy of the tracker: GitHub gets search syntax, Jira gets JQL. Full
// synchronisation is deliberately not a thing (docs/log/80 §80.12) — the query is what keeps
// the rail short. FetchedAt/LastError record the last attempt (see
// MarkWorkItemQueryFetched for why the stamp is written even when the fetch failed).
// MatchTotal says whether the last successful fetch left rows out: 0 when it did not, the
// tracker's count of matches when it did, -1 when it did but the tracker gave no count.
type WorkItemQuery struct {
	ID, MembershipID, Provider string
	Label, Query, RepoHint     string
	Enabled                    bool
	Position                   int
	CreatedAt                  string
	FetchedAt, LastError       string
	MatchTotal                 int
}

// WorkItem is one cached row of a query's result. ★ Non-secret metadata ONLY: the
// description, comments and attachments of a ticket are customer data and stay out of
// the CP (ADR 0061 decision 2) — they are read inside the session, where the Workspace
// is running and `gh` / the `jira` CLI can fetch them. Kind is "issue" or "pr"; State is
// normalised to open / in_progress / done / other; Repo is "owner/name" when the
// provider has one (it seeds the launch target).
type WorkItem struct {
	ID, MembershipID, QueryID, Provider string
	Kind, Key, Title, State, URL        string
	Assignee                            string
	Labels                              string // comma separated
	LabelColors                         string // JSON object: label name -> "rrggbb"; "" when none
	Repo                                string
	UpdatedAt, FetchedAt                string
	Checks                              string // JSON object {state,total,failed,pending}; "" when not read
	Mergeable                           string // clean / conflict / unknown; "" when not read
	ItemType                            string // the tracker's issue type (GitHub type, Jira issuetype); "" when none
	Comments                            int    // comment count from the provider's list response; -1 = unknown (provider does not say, or an older Agent)
	CanComment                          bool   // the Agent says the connection may post a comment (Bitbucket rows only)
}

// WorkItemSession is the ledger row written when a session is started from an item.
// It is keyed by ItemKey rather than a cache row id, and has no foreign key onto the
// cache, because the cache is a volatile query result while "someone started this"
// outlives it. Its main value is telling the next person that a ticket is already
// being worked on, before they start a second session on it.
type WorkItemSession struct {
	ID, MembershipID, Provider, ItemKey string
	SessionName, Repo, Branch           string
	CreatedAt                           string
}

// WorkItemStore is the work item inbox (docs/log/80 / ADR 0061), personal scope. The CP
// owns the saved queries and a cache of non-secret metadata; the Workspace Agent owns
// the provider tokens and does the fetching. Everything is scoped by membership.
type WorkItemStore interface {
	ListWorkItemQueries(ctx context.Context, membershipID string) ([]WorkItemQuery, error)
	GetWorkItemQuery(ctx context.Context, id string) (WorkItemQuery, bool, error)
	CreateWorkItemQuery(ctx context.Context, q WorkItemQuery) error
	UpdateWorkItemQuery(ctx context.Context, q WorkItemQuery) error
	DeleteWorkItemQuery(ctx context.Context, id, membershipID string) error
	// MarkWorkItemQueryFetched records the attempt (errMsg="" on success). fetchedAt is
	// stamped even on failure — it is the rate limiter as well as the "last fetched"
	// label, so a provider that 500s must not turn into a fetch on every 4s tick.
	MarkWorkItemQueryFetched(ctx context.Context, id, fetchedAt, errMsg string) error
	// SetWorkItemQueryMatchTotal records WorkItemQuery.MatchTotal. Called on a successful fetch
	// only: a failed one keeps its cached rows, so it keeps the note that goes with them.
	SetWorkItemQueryMatchTotal(ctx context.Context, id string, matchTotal int) error

	ListWorkItems(ctx context.Context, membershipID string) ([]WorkItem, error)
	// ReplaceWorkItems swaps the cached rows of the given queries for `items` in one
	// transaction. Only the listed queries are touched, so a partially failed refresh
	// keeps the other queries' rows instead of blanking the rail.
	ReplaceWorkItems(ctx context.Context, membershipID string, queryIDs []string, items []WorkItem) error

	// WorkItemCommentGen is the membership's Bitbucket-connection generation (0 = never changed).
	WorkItemCommentGen(ctx context.Context, membershipID string) (int64, error)
	// BumpWorkItemCommentGen starts a new generation and withdraws every cached row's posting
	// capability in one transaction (the rows stay).
	BumpWorkItemCommentGen(ctx context.Context, membershipID string) error
	// ReplaceWorkItemsAt is ReplaceWorkItems for a fetch that began under generation `gen`: in the
	// same transaction that writes the rows it checks the generation, and when it moved, the rows
	// are stored with CanComment cleared. Serialised against BumpWorkItemCommentGen by a row lock.
	ReplaceWorkItemsAt(ctx context.Context, membershipID string, queryIDs []string, items []WorkItem, gen int64) error
	// ClearWorkItemCanComment withdraws the posting capability of cached rows (the rows stay).
	// queryIDs empty = every row of the membership.
	ClearWorkItemCanComment(ctx context.Context, membershipID string, queryIDs []string) error

	ListWorkItemSessions(ctx context.Context, membershipID string) ([]WorkItemSession, error)
	CreateWorkItemSession(ctx context.Context, s WorkItemSession) error
	DeleteWorkItemSession(ctx context.Context, id, membershipID string) error
}

// ---------------------------------------------------------------------------
// Work item inbox (docs/log/80 / ADR 0061). The CP owns the saved queries and a cache
// of non-secret metadata; the provider tokens stay in the Workspace.
// ---------------------------------------------------------------------------

const workItemQueryCols = `SELECT id, membership_id, provider, label, query, repo_hint,
	enabled, position, created_at, fetched_at, last_error, match_total FROM work_item_query`

func scanWorkItemQuery(sc interface{ Scan(...any) error }) (WorkItemQuery, error) {
	var q WorkItemQuery
	var enabled int
	err := sc.Scan(&q.ID, &q.MembershipID, &q.Provider, &q.Label, &q.Query, &q.RepoHint,
		&enabled, &q.Position, &q.CreatedAt, &q.FetchedAt, &q.LastError, &q.MatchTotal)
	q.Enabled = enabled != 0
	return q, err
}

func (s *SQL) ListWorkItemQueries(ctx context.Context, membershipID string) ([]WorkItemQuery, error) {
	rows, err := s.db.QueryContext(ctx,
		workItemQueryCols+` WHERE membership_id=? ORDER BY position, created_at`, membershipID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WorkItemQuery
	for rows.Next() {
		q, err := scanWorkItemQuery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

func (s *SQL) GetWorkItemQuery(ctx context.Context, id string) (WorkItemQuery, bool, error) {
	q, err := scanWorkItemQuery(s.db.QueryRowContext(ctx, workItemQueryCols+` WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return WorkItemQuery{}, false, nil
	}
	return q, err == nil, err
}

func (s *SQL) CreateWorkItemQuery(ctx context.Context, q WorkItemQuery) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO work_item_query(id, membership_id, provider, label, query, repo_hint,
		 enabled, position, created_at, fetched_at, last_error)
		 VALUES(?,?,?,?,?,?,?,?,?,'','')`,
		q.ID, q.MembershipID, q.Provider, q.Label, q.Query, q.RepoHint,
		b2i(q.Enabled), q.Position, q.CreatedAt)
	return err
}

// UpdateWorkItemQuery is ownership-guarded by membership_id and deliberately leaves
// fetched_at / last_error alone — editing the label must not claim a fresh fetch.
func (s *SQL) UpdateWorkItemQuery(ctx context.Context, q WorkItemQuery) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE work_item_query SET provider=?, label=?, query=?, repo_hint=?, enabled=?, position=?
		 WHERE id=? AND membership_id=?`,
		q.Provider, q.Label, q.Query, q.RepoHint, b2i(q.Enabled), q.Position,
		q.ID, q.MembershipID)
	return err
}

// DeleteWorkItemQuery drops the query and the rows it cached. The ledger
// (work_item_session) is NOT touched: the sessions someone started are not a
// property of the search that surfaced them.
func (s *SQL) DeleteWorkItemQuery(ctx context.Context, id, membershipID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM work_item_cache WHERE query_id=? AND membership_id=?`, id, membershipID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM work_item_query WHERE id=? AND membership_id=?`, id, membershipID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQL) MarkWorkItemQueryFetched(ctx context.Context, id, fetchedAt, errMsg string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE work_item_query SET fetched_at=?, last_error=? WHERE id=?`, fetchedAt, errMsg, id)
	return err
}

func (s *SQL) SetWorkItemQueryMatchTotal(ctx context.Context, id string, matchTotal int) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE work_item_query SET match_total=? WHERE id=?`, matchTotal, id)
	return err
}

const workItemCols = `SELECT id, membership_id, query_id, provider, item_kind, item_key,
	title, state, url, assignee, labels, label_colors, repo, updated_at, fetched_at, checks, mergeable, item_type, can_comment, comments FROM work_item_cache`

func scanWorkItem(sc interface{ Scan(...any) error }) (WorkItem, error) {
	var w WorkItem
	var canComment int
	err := sc.Scan(&w.ID, &w.MembershipID, &w.QueryID, &w.Provider, &w.Kind, &w.Key,
		&w.Title, &w.State, &w.URL, &w.Assignee, &w.Labels, &w.LabelColors, &w.Repo, &w.UpdatedAt, &w.FetchedAt,
		&w.Checks, &w.Mergeable, &w.ItemType, &canComment, &w.Comments)
	w.CanComment = canComment != 0
	return w, err
}

func (s *SQL) ListWorkItems(ctx context.Context, membershipID string) ([]WorkItem, error) {
	rows, err := s.db.QueryContext(ctx,
		workItemCols+` WHERE membership_id=? ORDER BY updated_at DESC, item_key`, membershipID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WorkItem
	for rows.Next() {
		w, err := scanWorkItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// ReplaceWorkItems swaps the cached rows of `queryIDs` for `items` in one transaction.
// ★ Only the named queries are cleared: a refresh where one query failed must keep the
// other queries' rows, otherwise one 401 blanks the whole rail.
func (s *SQL) ReplaceWorkItems(ctx context.Context, membershipID string, queryIDs []string, items []WorkItem) error {
	return s.replaceWorkItems(ctx, membershipID, queryIDs, items, nil)
}

func (s *SQL) ReplaceWorkItemsAt(ctx context.Context, membershipID string, queryIDs []string, items []WorkItem, gen int64) error {
	return s.replaceWorkItems(ctx, membershipID, queryIDs, items, &gen)
}

func (s *SQL) WorkItemCommentGen(ctx context.Context, membershipID string) (int64, error) {
	var g int64
	err := s.db.QueryRowContext(ctx,
		`SELECT gen FROM work_item_comment_gen WHERE membership_id=?`, membershipID).Scan(&g)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return g, err
}

func (s *SQL) BumpWorkItemCommentGen(ctx context.Context, membershipID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO work_item_comment_gen(membership_id, gen) VALUES(?, 1)
		 ON CONFLICT(membership_id) DO UPDATE SET gen = work_item_comment_gen.gen + 1`, membershipID); err != nil {
		return err
	}
	// The upsert holds the row's lock until commit, so a concurrent replaceWorkItems either
	// committed before it (and is cleared here) or waits and then sees the new generation.
	if _, err := tx.ExecContext(ctx,
		`UPDATE work_item_cache SET can_comment=0 WHERE membership_id=? AND can_comment<>0`, membershipID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQL) replaceWorkItems(ctx context.Context, membershipID string, queryIDs []string, items []WorkItem, gen *int64) error {
	if len(queryIDs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if gen != nil {
		// Lock the generation row (creating it at 0 when absent) before reading it, so the check
		// and the write below sit on one side of any concurrent bump.
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO work_item_comment_gen(membership_id, gen) VALUES(?, 0) ON CONFLICT(membership_id) DO NOTHING`,
			membershipID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE work_item_comment_gen SET gen=gen WHERE membership_id=?`, membershipID); err != nil {
			return err
		}
		var cur int64
		if err := tx.QueryRowContext(ctx,
			`SELECT gen FROM work_item_comment_gen WHERE membership_id=?`, membershipID).Scan(&cur); err != nil {
			return err
		}
		if cur != *gen {
			items = append([]WorkItem(nil), items...)
			for i := range items {
				items[i].CanComment = false
			}
		}
	}
	for _, qid := range queryIDs {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM work_item_cache WHERE membership_id=? AND query_id=?`, membershipID, qid); err != nil {
			return err
		}
	}
	for _, w := range items {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO work_item_cache(id, membership_id, query_id, provider, item_kind, item_key,
			 title, state, url, assignee, labels, label_colors, repo, updated_at, fetched_at,
			 checks, mergeable, item_type, can_comment, comments)
			 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			w.ID, membershipID, w.QueryID, w.Provider, w.Kind, w.Key, w.Title, w.State,
			w.URL, w.Assignee, w.Labels, w.LabelColors, w.Repo, w.UpdatedAt, w.FetchedAt,
			w.Checks, w.Mergeable, w.ItemType, b2i(w.CanComment), w.Comments); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQL) ClearWorkItemCanComment(ctx context.Context, membershipID string, queryIDs []string) error {
	if len(queryIDs) == 0 {
		_, err := s.db.ExecContext(ctx,
			`UPDATE work_item_cache SET can_comment=0 WHERE membership_id=? AND can_comment<>0`, membershipID)
		return err
	}
	for _, qid := range queryIDs {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE work_item_cache SET can_comment=0 WHERE membership_id=? AND query_id=? AND can_comment<>0`,
			membershipID, qid); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQL) ListWorkItemSessions(ctx context.Context, membershipID string) ([]WorkItemSession, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, membership_id, provider, item_key, session_name, repo, branch, created_at
		 FROM work_item_session WHERE membership_id=? ORDER BY created_at DESC`, membershipID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WorkItemSession
	for rows.Next() {
		var s WorkItemSession
		if err := rows.Scan(&s.ID, &s.MembershipID, &s.Provider, &s.ItemKey,
			&s.SessionName, &s.Repo, &s.Branch, &s.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (s *SQL) CreateWorkItemSession(ctx context.Context, w WorkItemSession) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO work_item_session(id, membership_id, provider, item_key, session_name,
		 repo, branch, created_at) VALUES(?,?,?,?,?,?,?,?)`,
		w.ID, w.MembershipID, w.Provider, w.ItemKey, w.SessionName, w.Repo, w.Branch, w.CreatedAt)
	return err
}

func (s *SQL) DeleteWorkItemSession(ctx context.Context, id, membershipID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM work_item_session WHERE id=? AND membership_id=?`, id, membershipID)
	return err
}
