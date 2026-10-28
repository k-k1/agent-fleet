package store

import (
	"context"
	"database/sql"
	"strings"
)

// Memo is one queued note (docs/log/21), personal scope. Grouped by Repo then Category
// (a free-form sub-project label); Repo="" is the common/unfiled bucket. Kind is
// "file" (RefPath points at a ~/repos path, Body is an optional comment) or "text"
// (Body is the note). SentAt="" means unsent; a non-empty RFC3339 stamp marks a
// flushed memo kept until the retention sweep removes it.
type Memo struct {
	ID, MembershipID, Repo, Category string
	Kind, Body, RefPath              string
	// Attachments is a JSON array of {path,name} image attachments (docs/log/21, image
	// attachments), "" = none. path is an absolute in-container path under
	// ~/.cache/agent-fleet/memo-images; the bytes live in the workspace, this only
	// references them.
	Attachments       string
	Position          int
	CreatedAt, SentAt string
	// The inbox item this memo was taken in from and its sender (ADR 0111 decision 3), set
	// by the CP at take-in and by nothing else: CreateMemo from a client leaves them empty,
	// and UpdateMemo never writes them, so an edit cannot remove or forge an origin.
	OriginItemID, OriginSenderMembershipID, OriginSenderKey string
}

// MemoCategory persists a memo category as a first-class row (docs/log/21 UI overhaul), so a
// category can exist while empty and carry an explicit order. Name is unique within a
// (MembershipID, Repo) and stays the grouping key that Memo.Category references.
type MemoCategory struct {
	ID, MembershipID, Repo, Name string
	Position                     int
	CreatedAt                    string
}

// MemoStore is the memo queue (docs/log/21), personal scope. Mutations are scoped
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

	// Categories (docs/log/21 UI overhaul). First-class so they can be created empty, reordered,
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

// --- Memo queue (docs/log/21) --------------------------------------------------------

const memoCols = `SELECT id, membership_id, repo, category, kind, body, ref_path, attachments, position, created_at, sent_at,
	origin_item_id, origin_sender_membership_id, origin_sender_key FROM memo`

func scanMemo(row scanner) (Memo, error) {
	var m Memo
	err := row.Scan(&m.ID, &m.MembershipID, &m.Repo, &m.Category, &m.Kind,
		&m.Body, &m.RefPath, &m.Attachments, &m.Position, &m.CreatedAt, &m.SentAt,
		&m.OriginItemID, &m.OriginSenderMembershipID, &m.OriginSenderKey)
	return m, err
}

// ListMemos returns unsent memos plus sent ones still inside the retention window
// (sent_at empty, or sent_at at/after retainBefore). retainBefore is an RFC3339
// cutoff; RFC3339 strings sort chronologically so the lexical compare is correct.
func (s *SQL) ListMemos(ctx context.Context, membershipID, retainBefore string) ([]Memo, error) {
	rows, err := s.db.QueryContext(ctx,
		memoCols+` WHERE membership_id=? AND (sent_at='' OR sent_at>=?)
		           ORDER BY repo, category, position, created_at`, membershipID, retainBefore)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Memo
	for rows.Next() {
		m, err := scanMemo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *SQL) GetMemo(ctx context.Context, id string) (Memo, bool, error) {
	m, err := scanMemo(s.db.QueryRowContext(ctx, memoCols+` WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return Memo{}, false, nil
	}
	return m, err == nil, err
}

// CreateMemo inserts a memo a member or their agent wrote. It never writes an origin: a
// taken-in memo is created only by TakeInInboxRecipe.
func (s *SQL) CreateMemo(ctx context.Context, m Memo) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO memo(id, membership_id, repo, category, kind, body, ref_path, attachments, position, created_at, sent_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		m.ID, m.MembershipID, m.Repo, m.Category, m.Kind, m.Body, m.RefPath, m.Attachments, m.Position, m.CreatedAt, m.SentAt)
	return err
}

func (s *SQL) UpdateMemo(ctx context.Context, m Memo) error {
	// membership_id in the WHERE so a member can only update their own row.
	_, err := s.db.ExecContext(ctx,
		`UPDATE memo SET repo=?, category=?, kind=?, body=?, ref_path=?, attachments=?, position=?
		   WHERE id=? AND membership_id=?`,
		m.Repo, m.Category, m.Kind, m.Body, m.RefPath, m.Attachments, m.Position, m.ID, m.MembershipID)
	return err
}

func (s *SQL) DeleteMemo(ctx context.Context, id, membershipID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM memo WHERE id=? AND membership_id=?`, id, membershipID)
	return err
}

// MarkMemosSent stamps sent_at on the caller's memos in ids (ownership enforced by
// membership_id in the WHERE, so foreign ids are silently ignored).
func (s *SQL) MarkMemosSent(ctx context.Context, membershipID string, ids []string, sentAt string) error {
	if len(ids) == 0 {
		return nil
	}
	args := make([]any, 0, len(ids)+2)
	args = append(args, sentAt)
	ph := make([]string, len(ids))
	for i, id := range ids {
		ph[i] = "?"
		args = append(args, id)
	}
	args = append(args, membershipID)
	_, err := s.db.ExecContext(ctx,
		`UPDATE memo SET sent_at=? WHERE id IN (`+strings.Join(ph, ",")+`) AND membership_id=?`, args...)
	return err
}

// SweepSentMemos deletes sent memos whose sent_at is before the retention cutoff.
func (s *SQL) SweepSentMemos(ctx context.Context, retainBefore string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM memo WHERE sent_at!='' AND sent_at<?`, retainBefore)
	return err
}

// --- Memo categories (docs/log/21 UI overhaul) -------------------------------------------

const memoCategoryCols = `SELECT id, membership_id, repo, name, position, created_at FROM memo_category`

func scanMemoCategory(row scanner) (MemoCategory, error) {
	var c MemoCategory
	err := row.Scan(&c.ID, &c.MembershipID, &c.Repo, &c.Name, &c.Position, &c.CreatedAt)
	return c, err
}

// ListCategories returns a membership's categories ordered by repo then explicit
// position (created_at breaks ties so the order is stable).
func (s *SQL) ListCategories(ctx context.Context, membershipID string) ([]MemoCategory, error) {
	rows, err := s.db.QueryContext(ctx,
		memoCategoryCols+` WHERE membership_id=? ORDER BY repo, position, created_at`, membershipID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MemoCategory
	for rows.Next() {
		c, err := scanMemoCategory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *SQL) GetCategory(ctx context.Context, id string) (MemoCategory, bool, error) {
	c, err := scanMemoCategory(s.db.QueryRowContext(ctx, memoCategoryCols+` WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return MemoCategory{}, false, nil
	}
	return c, err == nil, err
}

func (s *SQL) CreateCategory(ctx context.Context, c MemoCategory) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO memo_category(id, membership_id, repo, name, position, created_at)
		 VALUES(?,?,?,?,?,?)`,
		c.ID, c.MembershipID, c.Repo, c.Name, c.Position, c.CreatedAt)
	return err
}

// UpdateCategory sets name + position on an owned category (membership_id in the WHERE).
func (s *SQL) UpdateCategory(ctx context.Context, c MemoCategory) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE memo_category SET name=?, position=? WHERE id=? AND membership_id=?`,
		c.Name, c.Position, c.ID, c.MembershipID)
	return err
}

func (s *SQL) DeleteCategory(ctx context.Context, id, membershipID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM memo_category WHERE id=? AND membership_id=?`, id, membershipID)
	return err
}

// ReassignMemoCategory rewrites memo.category from `from` to `to` for the caller's memos
// in a repo bucket (ownership by membership_id in the WHERE).
func (s *SQL) ReassignMemoCategory(ctx context.Context, membershipID, repo, from, to string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE memo SET category=? WHERE membership_id=? AND repo=? AND category=?`,
		to, membershipID, repo, from)
	return err
}
