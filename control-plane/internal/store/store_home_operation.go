package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// HomeOperation is the durable record of a home operation that runs the stack's home task
// (ecs, #1544). It exists from before RunTask until the step after the task has been
// applied, and its id is the task's RunTask clientToken.
type HomeOperation struct {
	ID           string
	WorkspaceID  string
	MembershipID string
	// Kind says which step follows the task: HomeOpMemberWipe, HomeOpAdminErase,
	// HomeOpDestroy.
	Kind string
	// Op is what the task removes (the runtime's HomeWipe: repos, clean, destroy).
	Op string
	// TaskARN is the task RunTask answered, once it has.
	TaskARN string
	// Audit is the outcome entry an administrator's operation still owes; nil for a
	// member's.
	Audit     *HomeOpAudit
	CreatedAt string
	UpdatedAt string
}

// The kinds of HomeOperation.
const (
	HomeOpMemberWipe = "member-wipe"
	HomeOpAdminErase = "admin-erase"
	HomeOpDestroy    = "destroy"
)

// HomeOpAudit is the outcome entry of an administrator's irreversible action, kept with
// the operation so whichever process finishes it writes the entry the request's intent
// row is waiting for.
type HomeOpAudit struct {
	// Base is the action's row as BeginIrreversible was given it.
	Base AuditLog `json:"base"`
	// OK is the detail on success. Leftovers, when set, appends what Destroy could not
	// remove (DestroyedDetail).
	OK        string `json:"ok"`
	Leftovers bool   `json:"leftovers,omitempty"`
	// FailPrefix starts the detail on failure; the error follows it.
	FailPrefix string `json:"failPrefix"`
	// FailStatus is the HTTP status recorded with a failure.
	FailStatus int `json:"failStatus"`
	OKStatus   int `json:"okStatus"`
}

// Entry is the outcome row for err (nil: success) and the leftovers Destroy reported.
func (a HomeOpAudit) Entry(err error, leftovers []string) AuditLog {
	out := a.Base
	out.ID = NewID()
	out.At = NowTS()
	switch {
	case err != nil:
		out.Detail, out.HTTPStatus = a.FailPrefix+err.Error(), a.FailStatus
	case a.Leftovers:
		out.Detail, out.HTTPStatus = DestroyedDetail(a.OK, leftovers), a.OKStatus
	default:
		out.Detail, out.HTTPStatus = a.OK, a.OKStatus
	}
	return out
}

// DestroyedDetail appends what could NOT be deleted — the part of a destroy's audit entry
// that matters. On Fargate the EFS directories survive their access points and keep
// billing (docs/log/64 §64.18.4); if that only ever appeared in an HTTP response nobody
// would ever find it again.
func DestroyedDetail(detail string, leftovers []string) string {
	if len(leftovers) > 0 {
		detail += "; NOT deleted: " + strings.Join(leftovers, ", ")
	}
	return detail
}

// HomeOperationFinish is what FinishHomeOperation writes with the record's deletion.
type HomeOperationFinish struct {
	// Audit is the outcome row, when the operation owes one.
	Audit *AuditLog
	// DeleteWorkspace removes the workspace row and everything keyed to it (a Destroy
	// that succeeded), as DeleteWorkspace does.
	DeleteWorkspace bool
	// StopWorkspace records the workspace as stopped (an administrator's Clean home that
	// succeeded), as SetWorkspaceState does.
	StopWorkspace bool
}

// ErrHomeOperationOpen refuses a second operation on a workspace whose last one is not
// finished.
var ErrHomeOperationOpen = errors.New("an operation on this workspace's home is not finished yet")

// HomeOperationStore keeps HomeOperation.
type HomeOperationStore interface {
	// InsertHomeOperation writes the record; ErrHomeOperationOpen when the workspace
	// already has one.
	InsertHomeOperation(ctx context.Context, op HomeOperation) error
	// SetHomeOperationTask records the task's ARN.
	SetHomeOperationTask(ctx context.Context, id, taskARN string) error
	GetHomeOperationByWorkspace(ctx context.Context, workspaceID string) (HomeOperation, bool, error)
	ListHomeOperations(ctx context.Context) ([]HomeOperation, error)
	// FinishHomeOperation deletes the record and applies f in one transaction. claimed is
	// false, and nothing is written, when the record was already gone: somebody else
	// finished it, and the step after the task must not run twice.
	FinishHomeOperation(ctx context.Context, id string, f HomeOperationFinish) (claimed bool, err error)
}

func (s *SQL) InsertHomeOperation(ctx context.Context, op HomeOperation) error {
	audit := ""
	if op.Audit != nil {
		b, err := json.Marshal(op.Audit)
		if err != nil {
			return err
		}
		audit = string(b)
	}
	now := NowTS()
	res, err := s.db.ExecContext(ctx, `INSERT INTO home_operation
		(id, workspace_id, membership_id, kind, op, task_arn, audit, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, '', ?, ?, ?) ON CONFLICT(workspace_id) DO NOTHING`,
		op.ID, op.WorkspaceID, op.MembershipID, op.Kind, op.Op, audit, now, now)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrHomeOperationOpen
	}
	return nil
}

func (s *SQL) SetHomeOperationTask(ctx context.Context, id, taskARN string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE home_operation SET task_arn=?, updated_at=? WHERE id=?`,
		taskARN, NowTS(), id)
	return err
}

const homeOperationCols = `id, workspace_id, membership_id, kind, op, task_arn, audit, created_at, updated_at`

func scanHomeOperation(row interface{ Scan(...any) error }) (HomeOperation, error) {
	var op HomeOperation
	var audit string
	if err := row.Scan(&op.ID, &op.WorkspaceID, &op.MembershipID, &op.Kind, &op.Op, &op.TaskARN,
		&audit, &op.CreatedAt, &op.UpdatedAt); err != nil {
		return HomeOperation{}, err
	}
	if audit != "" {
		op.Audit = &HomeOpAudit{}
		if err := json.Unmarshal([]byte(audit), op.Audit); err != nil {
			return HomeOperation{}, fmt.Errorf("home operation %s: audit: %w", op.ID, err)
		}
	}
	return op, nil
}

func (s *SQL) GetHomeOperationByWorkspace(ctx context.Context, workspaceID string) (HomeOperation, bool, error) {
	op, err := scanHomeOperation(s.db.QueryRowContext(ctx,
		`SELECT `+homeOperationCols+` FROM home_operation WHERE workspace_id=?`, workspaceID))
	if errors.Is(err, sql.ErrNoRows) {
		return HomeOperation{}, false, nil
	}
	return op, err == nil, err
}

func (s *SQL) ListHomeOperations(ctx context.Context) ([]HomeOperation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+homeOperationCols+` FROM home_operation ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HomeOperation
	for rows.Next() {
		op, err := scanHomeOperation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

func (s *SQL) FinishHomeOperation(ctx context.Context, id string, f HomeOperationFinish) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var wsID string
	switch err := tx.QueryRowContext(ctx, `SELECT workspace_id FROM home_operation WHERE id=?`, id).Scan(&wsID); {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM home_operation WHERE id=?`, id)
	if err != nil {
		return false, err
	}
	// The DELETE is the claim: of two finishers that both read the row, only one deletes
	// it (Postgres re-checks the row after the other commits, SQLite has one writer).
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return false, err
	}
	if f.Audit != nil {
		a := *f.Audit
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO audit_log(id, tenant_id, actor_kind, actor_id, action, target, detail, at, http_status)
			 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			a.ID, a.TenantID, a.ActorKind, a.ActorID, a.Action, a.Target, a.Detail, a.At, a.HTTPStatus); err != nil {
			return false, err
		}
	}
	switch {
	case f.DeleteWorkspace:
		if err := deleteWorkspaceTx(ctx, tx, wsID); err != nil {
			return false, err
		}
	case f.StopWorkspace:
		if err := setWorkspaceStateTx(ctx, tx, wsID, "stopped"); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}
