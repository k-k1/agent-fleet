package store

import (
	"context"
	"errors"
	"fmt"
	"log"
)

// AuditRequestedSuffix is appended to an action's name on the row BeginIrreversible writes
// before the action runs. The outcome row keeps the plain name, so every existing reader of
// "workspace.destroy" and friends still sees one row per completed request.
const AuditRequestedSuffix = ".requested"

// ErrAuditUnavailable wraps the store's error when the intent of an irreversible action could
// not be written. A handler that gets it must not act.
var ErrAuditUnavailable = errors.New("the audit log could not be written")

// AuditIntent is an irreversible admin action whose request is already on record. Done
// writes its outcome.
//
// The pair exists because the outcome row alone is written after the fact: once the home is
// erased or the tenant deleted, a failed insert leaves an action nobody can attribute. The
// intent row is written first and must succeed, so the worst a later failure can leave is a
// request whose outcome is unknown, never an action with no record of who asked for it.
type AuditIntent struct {
	st   AuditStore
	base AuditLog
}

// BeginIrreversible records that a.ActorID asked for a.Action on a.Target, before anything
// happens. a.Detail describes the request. On error nothing was recorded and the caller must
// refuse the action; the error wraps ErrAuditUnavailable.
//
// The writes do not follow the request's cancellation: an action that outlives its client
// still owes its record.
func BeginIrreversible(ctx context.Context, st AuditStore, a AuditLog) (*AuditIntent, error) {
	intent := a
	intent.ID = NewID()
	intent.Action = a.Action + AuditRequestedSuffix
	intent.HTTPStatus = 0
	intent.At = NowTS()
	if err := st.InsertAudit(context.WithoutCancel(ctx), intent); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAuditUnavailable, err)
	}
	return &AuditIntent{st: st, base: a}, nil
}

// Done records the outcome — what was done, what was not, or why it was refused — with the
// HTTP status the caller answers. Every path after a successful BeginIrreversible calls it
// exactly once, so a lone intent row means the outcome write failed or the process died.
//
// A failed write is logged, never returned: the action has already happened (or not), and
// the intent row still names who asked.
func (i *AuditIntent) Done(ctx context.Context, detail string, status int) {
	out := i.base
	out.ID = NewID()
	out.Detail = detail
	out.HTTPStatus = status
	out.At = NowTS()
	if err := i.st.InsertAudit(context.WithoutCancel(ctx), out); err != nil {
		log.Printf("audit: %s on %q by %s answered %d, but its outcome was not written (the request is on record): %s: %v",
			out.Action, out.Target, out.ActorID, status, detail, err)
	}
}
