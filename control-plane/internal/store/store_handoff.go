package store

// Store for member-to-member handoff (docs/log/77 / ADR 0057).
//
// A handoff is derived from the share ACL, so revocation is done by
// `invalidateUnauthorizedShareDerivatives` (store_share.go), in the same transaction as the
// share change. All that lives here is creation, reads, state transitions and expiry: this
// feature has no side effect on the Agent — the recipient starts the session against their
// own Workspace — so the owner lease and idempotency ledger an RW share proposal needed are
// absent (ADR 0057 decision 3).

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// SessionHandoffOffer is an offer of "please carry this on" held out to a share
// recipient (docs/log/77 / ADR 0057).
//
// It runs in the OPPOSITE direction to SessionShareProposal: that one goes recipient →
// owner (the recipient proposes, the owner approves), this one goes owner → recipient
// (the owner offers, the recipient accepts). The owner / recipient roles swap, which is
// why the table and the type are kept separate.
//
// Ciphertext is the body (Title is the plaintext heading, the prompt is the content). It
// is encrypted where a custodian exists, and cleared on decline, expiry and withdrawal.
// RepoRemote / Branch / HeadSha are facts the Agent asked git for — the coordinates the
// recipient needs to identify the matching working copy on their side.
type SessionHandoffOffer struct {
	ID, TenantID, CatalogID, OwnerMembershipID, RecipientMembershipID string
	Title, Ciphertext, KeyRef                                         string
	RepoRemote, Branch, HeadSha                                       string
	SourceSessionName, SourceSessionKind                              string
	Status, CreatedAt, ExpiresAt, DecidedAt, AcceptedSessionName      string
	// TakenAt is set while the recipient holds the offer for a launch in progress
	// (TakeSessionHandoffOffer), TakeConfirmedAt once that launch's create reached the Agent
	// (ConfirmSessionHandoffTake). The take's token is read back only by
	// ConfirmedSessionHandoffTake, for the CP to look its create up on the recipient's Agent;
	// it never leaves the CP.
	TakenAt, TakeConfirmedAt string
}

const handoffOfferCols = `id,tenant_id,catalog_id,owner_membership_id,recipient_membership_id,title,
	ciphertext,key_ref,repo_remote,branch,head_sha,source_session_name,source_session_kind,
	status,created_at,expires_at,decided_at,accepted_session_name,taken_at,take_confirmed_at`

func scanHandoffOffer(row interface{ Scan(...any) error }) (SessionHandoffOffer, bool, error) {
	var r SessionHandoffOffer
	err := row.Scan(&r.ID, &r.TenantID, &r.CatalogID, &r.OwnerMembershipID, &r.RecipientMembershipID,
		&r.Title, &r.Ciphertext, &r.KeyRef, &r.RepoRemote, &r.Branch, &r.HeadSha,
		&r.SourceSessionName, &r.SourceSessionKind, &r.Status, &r.CreatedAt, &r.ExpiresAt,
		&r.DecidedAt, &r.AcceptedSessionName, &r.TakenAt, &r.TakeConfirmedAt)
	if err == sql.ErrNoRows {
		return SessionHandoffOffer{}, false, nil
	}
	return r, err == nil, err
}

// CreateSessionHandoffOffer creates one offer. created=false means "this session already
// has an outstanding handoff" (ADR 0057 decision 10).
//
// Deliberately not shaped as count-then-INSERT: two concurrent requests would both see zero
// and both go through. Let the partial unique index refuse it, and translate only that
// violation into created=false.
func (s *SQL) CreateSessionHandoffOffer(ctx context.Context, r SessionHandoffOffer) (bool, error) {
	_, err := s.db.ExecContext(ctx, `INSERT INTO session_handoff_offer
		(id,tenant_id,catalog_id,owner_membership_id,recipient_membership_id,title,ciphertext,key_ref,
		 repo_remote,branch,head_sha,source_session_name,source_session_kind,status,created_at,expires_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.TenantID, r.CatalogID, r.OwnerMembershipID, r.RecipientMembershipID, r.Title,
		r.Ciphertext, r.KeyRef, r.RepoRemote, r.Branch, r.HeadSha, r.SourceSessionName,
		r.SourceSessionKind, r.Status, r.CreatedAt, r.ExpiresAt)
	if err != nil && isUniqueViolation(err) {
		return false, nil
	}
	return err == nil, err
}

// isUniqueViolation decides "hit a unique constraint" in both dialects. Depending on the
// driver's error type would 500 on one series only (the same break as
// [[schema-dialect-parity]]), so both messages are matched.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "unique constraint") || // sqlite (modernc) / some postgres
		strings.Contains(m, "duplicate key value") || // postgres
		strings.Contains(m, "constraint failed: unique") // sqlite's other wording
}

func (s *SQL) GetSessionHandoffOffer(ctx context.Context, id string) (SessionHandoffOffer, bool, error) {
	return scanHandoffOffer(s.db.QueryRowContext(ctx,
		`SELECT `+handoffOfferCols+` FROM session_handoff_offer WHERE id=?`, id))
}

func (s *SQL) listHandoffOffers(ctx context.Context, where string, arg string) ([]SessionHandoffOffer, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+handoffOfferCols+` FROM session_handoff_offer WHERE `+where+` ORDER BY created_at DESC`, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SessionHandoffOffer{}
	for rows.Next() {
		r, _, err := scanHandoffOffer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListSessionHandoffOffersByOwner is the sender's ledger: the history of handoffs they
// offered. Notifications are ephemeral by decision, so this is the only way to trace one
// afterwards — decided offers are returned too (docs/log/77 §77.10).
func (s *SQL) ListSessionHandoffOffersByOwner(ctx context.Context, membershipID string) ([]SessionHandoffOffer, error) {
	return s.listHandoffOffers(ctx, `owner_membership_id=?`, membershipID)
}

// ListSessionHandoffOffersByRecipient is the recipient's inbox. It returns only outstanding
// offers and leaves out those on sessions the owner archived — the same discipline as the
// share list (docs/log/59 §1: a folded conversation is not shown to recipients). Decided
// ones are left out because an accepted offer is evidenced by the new session, and a
// declined one may simply disappear.
func (s *SQL) ListSessionHandoffOffersByRecipient(ctx context.Context, membershipID string) ([]SessionHandoffOffer, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+handoffOfferCols+` FROM session_handoff_offer
		WHERE recipient_membership_id=? AND status='pending' AND ciphertext<>''
		  AND EXISTS (SELECT 1 FROM shared_session_catalog c
		              WHERE c.id=session_handoff_offer.catalog_id AND c.archived=0)
		ORDER BY created_at DESC`, membershipID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SessionHandoffOffer{}
	for rows.Next() {
		r, _, err := scanHandoffOffer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// HandoffTakeWindow is how long an UNCONFIRMED take holds a pending offer against the
// sender's withdraw and expiry, and against another take. It covers the time between the
// launch press and the create reaching the Agent, which can include a cold workspace start
// (the same allowance as the inbox's launch window). Past it, a Console that closed before
// launching no longer pins the offer, and if it wakes up its create fails the confirm.
// A confirmed take does not go stale: its session may exist.
const HandoffTakeWindow = 10 * time.Minute

// handoffTakeGuards is what makes a decision on a pending offer wait for an outstanding
// take. It is a variable only so the race test's positive control can turn it off and show
// the race is real; nothing else assigns it.
var handoffTakeGuards = true

// handoffTakeStale is the taken_at at or before which an unconfirmed take no longer holds.
func handoffTakeStale() string {
	return time.Now().UTC().Add(-HandoffTakeWindow).Format(time.RFC3339)
}

// handoffNotHeld is the condition "no take holds this offer": none, or an unconfirmed one
// past the take window. It takes handoffTakeStale() as its one argument.
const handoffNotHeld = ` AND (take_token='' OR (take_confirmed_at='' AND taken_at<=?))`

// TransitionSessionHandoffOffer is the conditional from → to update. changed=false means
// somebody decided it first, or, from pending, that a take holds it (TakeSessionHandoffOffer).
// Anything but accepted clears the body: there is no reason to keep a decided handoff's body
// in the CP (accepted keeps it so the recipient can start the session again).
//
// From pending, what a take blocks depends on who decides:
//   - accepted without a token (a Console from before the take) lands only on an offer
//     nobody has taken, so it can never settle or erase another launch's take;
//   - withdrawn, declined and expired wait for any take that holds (handoffNotHeld): a
//     confirmed take never yields to them, because its session may already exist and the
//     sender reads any decided status as "launch without withdrawing" (ADR 0057 decision 6).
//     Being the recipient proves nothing about the earlier create, so the recipient's own
//     decline does not pass it either.
func (s *SQL) TransitionSessionHandoffOffer(ctx context.Context, id, from, to, decidedAt, acceptedSessionName string) (bool, error) {
	body := `''`
	if to == "accepted" {
		body = `ciphertext`
	}
	q := `UPDATE session_handoff_offer
		SET status=?,decided_at=?,accepted_session_name=?,ciphertext=` + body + `,take_token='',taken_at='',take_confirmed_at=''
		WHERE id=? AND status=?`
	args := []any{to, decidedAt, acceptedSessionName, id, from}
	if from == "pending" && handoffTakeGuards {
		switch to {
		case "accepted":
			q += ` AND take_token=''`
		default:
			q += handoffNotHeld
			args = append(args, handoffTakeStale())
		}
	}
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// TakeSessionHandoffOffer reserves a pending offer for its recipient before the launch
// (issue #2020). taken=false means it is no longer pending, has run past its deadline, is no
// longer launchable (unshared or archived: a recovery row must never yield a launch, whatever
// a Console renders), or a take still holds it. Together with the guard on TransitionSessionHandoffOffer this makes
// the sender's withdraw and the recipient's take exclusive: whichever lands first wins, the
// other changes nothing (ADR 0057 decision 6).
func (s *SQL) TakeSessionHandoffOffer(ctx context.Context, id, recipientMembershipID, token, now string) (bool, error) {
	if token == "" {
		return false, nil
	}
	res, err := s.db.ExecContext(ctx, `UPDATE session_handoff_offer SET take_token=?,taken_at=?,take_confirmed_at=''
		WHERE id=? AND recipient_membership_id=? AND status='pending' AND expires_at>?`+handoffLaunchable+handoffNotHeld,
		token, now, id, recipientMembershipID, now, handoffTakeStale())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ConfirmSessionHandoffTake is the commit point of a launch from an offer: the CP runs it
// immediately before the session create reaches the Agent. It holds only while the offer is
// still pending under this very token, so a Console that took the offer, went quiet past
// the take window and lost it — to the sender's withdraw or to another take — cannot start
// the work when it resumes. Once confirmed, the take holds until it is accepted or released.
//
// A take confirms once: a second create under the same token is refused here, before it
// reaches the Agent (issue #2040). Two creates under one take would share one Agent key, and
// the answer of one could then settle the take while the other made the session.
func (s *SQL) ConfirmSessionHandoffTake(ctx context.Context, id, recipientMembershipID, token, now string) (bool, error) {
	if token == "" {
		return false, nil
	}
	res, err := s.db.ExecContext(ctx, `UPDATE session_handoff_offer SET take_confirmed_at=?
		WHERE id=? AND recipient_membership_id=? AND status='pending' AND take_token=? AND take_confirmed_at=''`,
		now, id, recipientMembershipID, token)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ReleaseSessionHandoffOffer gives any take back, confirmed or not, so the offer is pending and
// decidable again. Only the CP calls it, on the recipient's Agent's own word that no session
// exists under the take's key: the create was refused (a refusal is final for that key), or
// the key was abandoned (reconcileTake). A Console's release goes through
// ReleaseUnconfirmedSessionHandoffTake instead.
// Only the take's own token releases it. An offer whose share was revoked while its take was
// confirmed (body already erased by invalidateUnauthorizedShareDerivatives) expires instead:
// nobody may take it any more.
func (s *SQL) ReleaseSessionHandoffOffer(ctx context.Context, id, recipientMembershipID, token string) (bool, error) {
	if token == "" {
		return false, nil
	}
	now := NowTS()
	res, err := s.db.ExecContext(ctx, `UPDATE session_handoff_offer
		SET take_token='',taken_at='',take_confirmed_at='',
		    status=CASE WHEN ciphertext='' THEN 'expired' ELSE status END,
		    decided_at=CASE WHEN ciphertext='' THEN ? ELSE decided_at END
		WHERE id=? AND recipient_membership_id=? AND status='pending' AND take_token=?`,
		now, id, recipientMembershipID, token)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ReleaseUnconfirmedSessionHandoffTake gives back a take whose create never reached the Agent,
// or was refused by it: the Console's release, and the holder's next press after losing the
// create's answer (issue #2040). A confirmed take is left alone, since its session may exist.
// As with ReleaseSessionHandoffOffer, an offer whose share was revoked meanwhile expires. Releasing the
// unconfirmed one is safe even while its create is still on the way, because that create's
// confirm needs this very token and fails once it is gone.
func (s *SQL) ReleaseUnconfirmedSessionHandoffTake(ctx context.Context, id, recipientMembershipID, token string) (bool, error) {
	if token == "" {
		return false, nil
	}
	now := NowTS()
	res, err := s.db.ExecContext(ctx, `UPDATE session_handoff_offer
		SET take_token='',taken_at='',take_confirmed_at='',
		    status=CASE WHEN ciphertext='' THEN 'expired' ELSE status END,
		    decided_at=CASE WHEN ciphertext='' THEN ? ELSE decided_at END
		WHERE id=? AND recipient_membership_id=? AND status='pending' AND take_token=? AND take_confirmed_at=''`,
		now, id, recipientMembershipID, token)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// HandoffTakeState names where an offer's take stands at now, for the two sides' screens and
// for GiveBackUnknownSessionHandoffTake, whose SQL condition is the "unknown" case here:
//   - "" — no take holds the offer (none, the offer is decided, or an unconfirmed take past
//     the take window);
//   - "starting" — an unconfirmed take within the window: the recipient is launching;
//   - "confirming" — a confirmed take within the window: the create was sent and may still be
//     running on the recipient's Agent;
//   - "unknown" — a confirmed take older than the window: the CP never heard the create's
//     outcome, and only the recipient's next press (reconcileTake) or their give-back
//     settles it.
func HandoffTakeState(o SessionHandoffOffer, now time.Time) string {
	if o.Status != "pending" || o.TakenAt == "" {
		return ""
	}
	stale := now.UTC().Add(-HandoffTakeWindow).Format(time.RFC3339)
	switch {
	case o.TakeConfirmedAt == "" && o.TakenAt <= stale:
		return ""
	case o.TakeConfirmedAt == "":
		return "starting"
	case o.TakeConfirmedAt <= stale:
		return "unknown"
	default:
		return "confirming"
	}
}

// handoffGiveBackGuards is what confines a give-back to an unknown take. A variable only so
// the race test's positive control can turn it off; nothing else assigns it.
var handoffGiveBackGuards = true

// GiveBackUnknownSessionHandoffTake releases the confirmed take `token` once its create has had
// the whole take window to answer and never did (HandoffTakeState "unknown"), on the
// recipient's own statement that no session came of it (issue #2058). It is one conditional
// update bound to that take, so it cannot release a take that is not in that state when it
// lands: one still within the window (its create may be on its way to the Agent), one that a
// reconcile accepted or gave back meanwhile, or a later take that replaced the one the
// recipient confirmed against (a stale screen or a delayed request). The window is the least
// the recipient waits, not a point by which the create has finished: giving back cancels
// nothing on the Agent. Like the other releases, an offer whose share was revoked meanwhile
// expires instead of going back to pending.
func (s *SQL) GiveBackUnknownSessionHandoffTake(ctx context.Context, id, recipientMembershipID, token string, now time.Time) (bool, error) {
	if token == "" {
		return false, nil
	}
	cond := ` AND status='pending' AND take_token=? AND take_confirmed_at<>'' AND take_confirmed_at<=?`
	args := []any{now.UTC().Format(time.RFC3339), id, recipientMembershipID}
	if handoffGiveBackGuards {
		args = append(args, token, now.UTC().Add(-HandoffTakeWindow).Format(time.RFC3339))
	} else {
		cond = ""
	}
	res, err := s.db.ExecContext(ctx, `UPDATE session_handoff_offer
		SET take_token='',taken_at='',take_confirmed_at='',
		    status=CASE WHEN ciphertext='' THEN 'expired' ELSE status END,
		    decided_at=CASE WHEN ciphertext='' THEN ? ELSE decided_at END
		WHERE id=? AND recipient_membership_id=?`+cond, args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// handoffLaunchable is the condition "the recipient can still launch from this offer": its body
// is there (not erased by an unshare) and its source session is not archived. It is the inbox's
// own filter, so a new take is never handed out for an offer the inbox does not show.
const handoffLaunchable = ` AND ciphertext<>'' AND EXISTS (SELECT 1 FROM shared_session_catalog c
		WHERE c.id=session_handoff_offer.catalog_id AND c.archived=0)`

// ReceivedHandoffOffer is one row of the recipient's received list. ConfirmedTakeToken is the
// token of the recipient's confirmed take as read in the same SELECT as the row, so a revision
// derived from it names the take the row shows and never a later one; it is "" when no
// confirmed take holds the offer, and it never leaves the CP.
type ReceivedHandoffOffer struct {
	SessionHandoffOffer
	// RecoveryOnly: listed only because the recipient's confirmed take still holds an offer the
	// inbox would otherwise drop (unshared or archived).
	RecoveryOnly       bool
	ConfirmedTakeToken string
}

// ListReceivedSessionHandoffOffers is the recipient's received list in one snapshot: the inbox's
// offers (ListSessionHandoffOffersByRecipient's filter) plus, as recovery rows, pending offers
// a confirmed take of theirs still holds although the share was revoked (body erased) or the
// source session archived. Nothing else can release such a take — the sender's withdraw and
// expiry wait for it — so the recipient must still see it to give it back (issue #2058). The
// caller shows recovery rows without body or coordinates, and TakeSessionHandoffOffer never
// hands out a launch for them.
func (s *SQL) ListReceivedSessionHandoffOffers(ctx context.Context, membershipID string) ([]ReceivedHandoffOffer, error) {
	launchable := `(1=1` + handoffLaunchable + `)`
	rows, err := s.db.QueryContext(ctx, `SELECT `+handoffOfferCols+`,
		CASE WHEN `+launchable+` THEN 0 ELSE 1 END,
		CASE WHEN take_confirmed_at<>'' THEN take_token ELSE '' END
		FROM session_handoff_offer
		WHERE recipient_membership_id=? AND status='pending'
		  AND (`+launchable+` OR (take_token<>'' AND take_confirmed_at<>''))
		ORDER BY created_at DESC`, membershipID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ReceivedHandoffOffer{}
	for rows.Next() {
		var r ReceivedHandoffOffer
		var recovery int
		o := &r.SessionHandoffOffer
		if err := rows.Scan(&o.ID, &o.TenantID, &o.CatalogID, &o.OwnerMembershipID, &o.RecipientMembershipID,
			&o.Title, &o.Ciphertext, &o.KeyRef, &o.RepoRemote, &o.Branch, &o.HeadSha,
			&o.SourceSessionName, &o.SourceSessionKind, &o.Status, &o.CreatedAt, &o.ExpiresAt,
			&o.DecidedAt, &o.AcceptedSessionName, &o.TakenAt, &o.TakeConfirmedAt,
			&recovery, &r.ConfirmedTakeToken); err != nil {
			return nil, err
		}
		r.RecoveryOnly = recovery == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

// ConfirmedSessionHandoffTake returns the token of the confirmed take holding a pending offer
// of this recipient: a launch whose create reached the Agent and whose outcome the CP never
// learnt. The CP needs the token to ask the recipient's Agent what became of that create; it
// is never sent to a client.
func (s *SQL) ConfirmedSessionHandoffTake(ctx context.Context, id, recipientMembershipID string) (string, bool, error) {
	var token string
	err := s.db.QueryRowContext(ctx, `SELECT take_token FROM session_handoff_offer
		WHERE id=? AND recipient_membership_id=? AND status='pending' AND take_token<>'' AND take_confirmed_at<>''`,
		id, recipientMembershipID).Scan(&token)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	return token, err == nil, err
}

// AcceptSessionHandoffOffer settles a take as accepted once the session exists. It needs the
// take's token: a launch that never took the offer, or whose take was overtaken, cannot
// accept it. The body is kept, as for every acceptance.
func (s *SQL) AcceptSessionHandoffOffer(ctx context.Context, id, recipientMembershipID, token, decidedAt, acceptedSessionName string) (bool, error) {
	if token == "" {
		return false, nil
	}
	res, err := s.db.ExecContext(ctx, `UPDATE session_handoff_offer
		SET status='accepted',decided_at=?,accepted_session_name=?,take_token='',taken_at='',take_confirmed_at=''
		WHERE id=? AND recipient_membership_id=? AND status='pending' AND take_token=?`,
		decidedAt, acceptedSessionName, id, recipientMembershipID, token)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ExpireSessionHandoffOffers expires the offers past their deadline and returns the rows it
// expired. They come back so the caller can assemble "tell the owner exactly once as it
// lapses" (docs/log/77 §77.9): asking afterwards which offers were affected queries a set
// that is already empty.
func (s *SQL) ExpireSessionHandoffOffers(ctx context.Context, now string) ([]SessionHandoffOffer, error) {
	due, err := s.listHandoffOffers(ctx, `status='pending' AND expires_at<=?`, now)
	if err != nil || len(due) == 0 {
		return nil, err
	}
	out := make([]SessionHandoffOffer, 0, len(due))
	for _, o := range due {
		changed, err := s.TransitionSessionHandoffOffer(ctx, o.ID, "pending", "expired", now, "")
		if err != nil {
			return out, err
		}
		if changed {
			out = append(out, o)
		}
	}
	return out, nil
}
