package store

import (
	"context"
	"database/sql"
	"errors"
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
	// The message fields of ADR 0137 (store_memo_queue.go). State is "" for a plain note.
	// Provenance and the producer pair are written once, at the insert, and UpdateMemo never
	// writes them. ReleaseHash and ReleasedAt are set by a release (or a Console write in a
	// queue) and cleared when a message goes back to held or leaves its queue.
	State, Provenance, Producer, ProducerOrigin string
	ReleaseHash, ReleasedAt                     string
	// ReleasedBy is "" for a release the member pressed or wrote through the Console, and
	// MemoReleasedBySetting for one the queue's accept-from-own-sessions switch made. It lives
	// and dies with ReleaseHash.
	ReleasedBy string
	// SendToken is the attempt of a flush in flight on this row, SendStartedAt when it began
	// ("" for none). See MemoSendStaleAfter.
	SendToken, SendStartedAt string
	// DeliveryID is the delivery that claimed this message (store_memo_delivery.go), "" for
	// none. Only the delivery statements write it: InsertMemo and UpdateMemo never do, so a
	// write that read the row before a claim cannot take the claim away.
	DeliveryID string
	// RefProvider, RefKey and RefURL are the typed reference of ADR 0137 section 4: a pointer to
	// a work item, never a copy of it. All three are set or none is, and the API layer is what
	// validates them. A reference is part of what a release is bound to.
	RefProvider, RefKey, RefURL string
	// The tidy fields of ADR 0137 section 8, written by a tidy run (store_memo_tidy.go) and by
	// nothing else: InsertMemo writes them for a derived row, WriteMemo and UpdateMemo never do.
	// DerivedFrom is the JSON array of the source ids of a message a run made, SourceProvenance
	// the provenance its sources had (Provenance is curator for such a row), TidyFrom the category
	// id a run moved the row from, TidyRunID the run that made, moved or superseded it, and
	// TidySeenHash the content hash a run last looked at.
	DerivedFrom, SourceProvenance, TidyFrom, TidySeenHash, TidyRunID string
	// AfterIDs is the JSON array of the ids of the messages of the same queue this one waits for
	// (ADR 0137, addendum 2026-11-19, part A), "" for none. It is part of what a release is bound
	// to. HeldReason is why the dispatcher put the message back to held because a prerequisite can
	// no longer be satisfied, "" otherwise: a write that leaves held clears it.
	AfterIDs, HeldReason string
	// NotBefore, DeferCount and DeferReasons are the hand-back of ADR 0137, addendum 2026-11-19,
	// part B: the earliest time a returned message may be claimed again ("" for none), how many
	// times it was handed back since a person last released or returned it, and the JSON array of
	// those hand-backs (MemoDeferEntry). Only the return transition (ReturnDelivered) sets them,
	// and a release clears them: no insert or edit writes them.
	NotBefore    string
	DeferCount   int
	DeferReasons string
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

	// Queues and message states (ADR 0137, store_memo_queue.go).
	MemoQueueStore
}

// --- Memo queue (docs/log/21) --------------------------------------------------------

const memoCols = `SELECT id, membership_id, repo, category, kind, body, ref_path, attachments, position, created_at, sent_at,
	origin_item_id, origin_sender_membership_id, origin_sender_key,
	state, provenance, producer, producer_origin, release_hash, released_at, send_token, send_started_at, delivery_id,
	ref_provider, ref_key, ref_url, released_by,
	derived_from, source_provenance, tidy_from, tidy_seen_hash, tidy_run_id, after_ids, held_reason,
	not_before, defer_count, defer_reasons FROM memo`

func scanMemo(row scanner) (Memo, error) {
	var m Memo
	err := row.Scan(&m.ID, &m.MembershipID, &m.Repo, &m.Category, &m.Kind,
		&m.Body, &m.RefPath, &m.Attachments, &m.Position, &m.CreatedAt, &m.SentAt,
		&m.OriginItemID, &m.OriginSenderMembershipID, &m.OriginSenderKey,
		&m.State, &m.Provenance, &m.Producer, &m.ProducerOrigin, &m.ReleaseHash, &m.ReleasedAt, &m.SendToken, &m.SendStartedAt, &m.DeliveryID,
		&m.RefProvider, &m.RefKey, &m.RefURL, &m.ReleasedBy,
		&m.DerivedFrom, &m.SourceProvenance, &m.TidyFrom, &m.TidySeenHash, &m.TidyRunID, &m.AfterIDs, &m.HeldReason,
		&m.NotBefore, &m.DeferCount, &m.DeferReasons)
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
	return insertMemo(ctx, s.db, m)
}

func insertMemo(ctx context.Context, q memoQuerier, m Memo) error {
	rel := releasedContent(m)
	_, err := q.ExecContext(ctx,
		`INSERT INTO memo(id, membership_id, repo, category, kind, body, ref_path, attachments, position, created_at, sent_at,
		   state, provenance, producer, producer_origin, release_hash, released_at,
		   ref_provider, ref_key, ref_url,
		   release_repo, release_category, release_kind, release_body, release_ref_path, release_attachments,
		   release_ref_provider, release_ref_key, release_ref_url, released_by,
		   derived_from, source_provenance, tidy_from, tidy_seen_hash, tidy_run_id,
		   after_ids, release_after_ids, held_reason)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		m.ID, m.MembershipID, m.Repo, m.Category, m.Kind, m.Body, m.RefPath, m.Attachments, m.Position, m.CreatedAt, m.SentAt,
		m.State, m.Provenance, m.Producer, m.ProducerOrigin, m.ReleaseHash, m.ReleasedAt,
		m.RefProvider, m.RefKey, m.RefURL,
		rel[0], rel[1], rel[2], rel[3], rel[4], rel[5], rel[6], rel[7], rel[8], m.ReleasedBy,
		m.DerivedFrom, m.SourceProvenance, m.TidyFrom, m.TidySeenHash, m.TidyRunID,
		m.AfterIDs, rel[9], heldReasonOf(m))
	return err
}

// heldReasonOf is what held_reason holds: the reason of a held message, nothing otherwise.
func heldReasonOf(m Memo) string {
	if m.State != MemoStateHeld {
		return ""
	}
	return m.HeldReason
}

// releasedContent is what the release_* columns hold: the content of a pending message, and
// nothing otherwise. The CHECKs of migrations 0131, 0138 and 0144 require it for every pending row.
func releasedContent(m Memo) [10]string {
	if m.State != MemoStatePending {
		return [10]string{}
	}
	return [10]string{m.Repo, m.Category, m.Kind, m.Body, m.RefPath, m.Attachments, m.RefProvider, m.RefKey, m.RefURL, m.AfterIDs}
}

// UpdateMemo writes the editable columns, sent_at and the message state of an owned row.
// Provenance, producer and origin are not among them: they are fixed at the insert. Pass a
// row read in the same MemoTx: a stale one writes back stale columns.
func (s *SQL) UpdateMemo(ctx context.Context, m Memo) error {
	return writeMemo(ctx, s.db, m)
}

// ErrMemoSuperseded is the refusal of a write that would make a superseded row held or pending
// again. Only an undo (RestoreMemo) does that.
var ErrMemoSuperseded = errors.New("a superseded message becomes held or pending again only by undoing its run")

func writeMemo(ctx context.Context, q memoQuerier, m Memo) error {
	return writeMemoRow(ctx, q, m, false, false)
}

// writeMemoRow is writeMemo. withTidy also writes the columns of a tidy run, which only a run's
// apply and undo do. restore lifts the guard on a superseded row.
func writeMemoRow(ctx context.Context, q memoQuerier, m Memo, withTidy, restore bool) error {
	rel := releasedContent(m)
	tidyCols, tidyArgs := "", []any(nil)
	if withTidy {
		tidyCols, tidyArgs = ", tidy_from=?, tidy_run_id=?, tidy_seen_hash=?", []any{m.TidyFrom, m.TidyRunID, m.TidySeenHash}
	}
	// A superseded row never becomes held or pending by a write: the row is the evidence of what a
	// run did, and the run's undo is the one way back. Locked rows reach here with their state
	// unchanged, and a row that leaves its queue is written as a plain note or as delivered.
	guard := ""
	if !restore {
		guard = " AND (state<>'superseded' OR CAST(? AS TEXT) NOT IN ('held','pending'))"
	}
	args := []any{m.Repo, m.Category, m.Kind, m.Body, m.RefPath, m.Attachments, m.Position, m.SentAt,
		m.State, m.ReleaseHash, m.ReleasedAt, m.SendToken, m.SendStartedAt,
		m.RefProvider, m.RefKey, m.RefURL,
		rel[0], rel[1], rel[2], rel[3], rel[4], rel[5], rel[6], rel[7], rel[8], m.ReleasedBy,
		m.AfterIDs, rel[9], heldReasonOf(m)}
	args = append(args, tidyArgs...)
	// A write that takes a held message to pending is a person's release (the Console's own
	// write in a queue): it clears the hand-backs as ReleaseMemo does. The CASE reads the row's
	// state before this write.
	args = append(args, m.State, m.State, m.State)
	args = append(args, m.ID, m.MembershipID)
	if !restore {
		args = append(args, m.State)
	}
	// membership_id in the WHERE so a member can only update their own row.
	res, err := q.ExecContext(ctx,
		`UPDATE memo SET repo=?, category=?, kind=?, body=?, ref_path=?, attachments=?, position=?, sent_at=?,
		   state=?, release_hash=?, released_at=?, send_token=?, send_started_at=?,
		   ref_provider=?, ref_key=?, ref_url=?,
		   release_repo=?, release_category=?, release_kind=?, release_body=?, release_ref_path=?, release_attachments=?,
		   release_ref_provider=?, release_ref_key=?, release_ref_url=?, released_by=?,
		   after_ids=?, release_after_ids=?, held_reason=?`+tidyCols+`,
		   not_before=CASE WHEN state='held' AND CAST(? AS TEXT)='pending' THEN '' ELSE not_before END,
		   defer_count=CASE WHEN state='held' AND CAST(? AS TEXT)='pending' THEN 0 ELSE defer_count END,
		   defer_reasons=CASE WHEN state='held' AND CAST(? AS TEXT)='pending' THEN '' ELSE defer_reasons END
		   WHERE id=? AND membership_id=?`+guard, args...)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 && !restore && (m.State == MemoStateHeld || m.State == MemoStatePending) {
		var one int
		if q.QueryRowContext(ctx, `SELECT 1 FROM memo WHERE id=? AND membership_id=? AND state='superseded'`, m.ID, m.MembershipID).Scan(&one) == nil {
			return ErrMemoSuperseded
		}
	}
	return nil
}

func (s *SQL) DeleteMemo(ctx context.Context, id, membershipID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM memo WHERE id=? AND membership_id=?`, id, membershipID)
	return err
}

// MarkMemosSent stamps sent_at on the caller's memos in ids (ownership enforced by
// membership_id in the WHERE, so foreign ids are silently ignored). A message in a queue (a
// non-empty state) becomes delivered: a manual send is a delivery by the person.
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
		`UPDATE memo SET sent_at=?, state=CASE WHEN state='' THEN '' ELSE 'delivered' END WHERE id IN (`+strings.Join(ph, ",")+`) AND membership_id=?`, args...)
	return err
}

// SweepSentMemos deletes sent memos whose sent_at is before the retention cutoff, except a
// delivered message that a held, pending or claimed message of its queue still names as a
// prerequisite (a claimed one may yet return to pending when its delivery is not sent):
// deleting it would turn a satisfied prerequisite into a deleted one and hold the dependent.
func (s *SQL) SweepSentMemos(ctx context.Context, retainBefore string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM memo WHERE sent_at!='' AND sent_at<? AND NOT EXISTS (
		   SELECT 1 FROM memo w WHERE w.membership_id=memo.membership_id AND w.repo=memo.repo AND w.category=memo.category
		     AND w.state IN (?,?,?) AND w.after_ids LIKE '%"' || memo.id || '"%')`, retainBefore, MemoStateHeld, MemoStatePending, MemoStateClaimed)
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

// CreateCategory inserts a row outside MemoTx and fails on a duplicate name; kept for store
// tests. Route code uses MemoTxOps.EnsureCategory inside MemoTx, which is race-safe.
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
