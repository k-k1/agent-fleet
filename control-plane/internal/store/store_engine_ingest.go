package store

// store_engine_ingest.go — ingest jobs (ADR 0072 decision 6, phase P4).
//
// A job outlives the request that started it by minutes (Hugging Face has been measured at
// 4-236 MB/s, so an 18.5 GB model is anywhere from 80 seconds to 75 minutes), and it has to
// outlive the Control Plane process too: a CP replaced mid-download would otherwise leave an
// ECS task running with nobody watching it and no row to show anyone.
//
// The truth about whether the TASK is alive is ECS's, not this table's — see EngineIngestJob.

import (
	"context"
	"strings"
)

// EngineIngestJob is one "take this file into the bucket" in flight.
type EngineIngestJob struct {
	ID, Role, ModelID, S3Key string
	// Source is what a person reads: `hf:<repo>/<file>`, `civitai:<version>`, or the URL. Not
	// parsed by anything — the machine-readable half was consumed when the job was created.
	Source, TaskArn, State string
	// Message is why it failed, in the words the task used ("sha256 mismatch: got … want …",
	// "401 on a gated repository"). An exit code is not an answer anybody can act on.
	Message string
	Bytes   int64
	// Spec is the catalogue row this job will create, as JSON (the Control Plane owns the
	// shape). Persisted so a CP replaced mid-download still writes the row when the task it
	// never started is found finished.
	Spec      string
	StartedBy string
	// TenantID is the tenant whose grant this job was started under (ADR 0072 open question
	// 11). Empty for a super_admin, who acts for the whole deployment and has no tenant to be
	// acting for — so it is NOT a filter value anybody may pass in; see
	// ListEngineIngestJobsByTenant.
	TenantID             string
	CreatedAt, UpdatedAt string
}

// The states a job moves through. `pending` exists because RunTask can fail on its own (no
// capacity, a bad task definition), and a job that never got a task must still be visible with
// the reason — otherwise the panel shows nothing at all and the operator presses again.
const (
	EngineIngestPending = "pending"
	EngineIngestRunning = "running"
	EngineIngestDone    = "done"
	EngineIngestFailed  = "failed"
)

const engineIngestCols = `id, role, model_id, s3_key, source, task_arn, state, message,
	bytes, spec, started_by, tenant_id, created_at, updated_at`

// EngineIngestStore is the job ledger. Separate from EngineModelStore because the two answer
// different questions — "what may this engine load" versus "what is being fetched right now" —
// and only the first one is read on the request path.
type EngineIngestStore interface {
	PutEngineIngestJob(ctx context.Context, j EngineIngestJob) error
	// ListEngineIngestJobs returns the most recent jobs first. `limit` <= 0 means a sane cap
	// rather than everything: this feeds a panel, and a deployment that has taken in two
	// hundred models does not want them all in one response.
	ListEngineIngestJobs(ctx context.Context, role string, limit int) ([]EngineIngestJob, error)
	// ListEngineIngestJobsByTenant is the same list narrowed to ONE tenant's jobs, for the
	// reduced panel a granted tenant_admin sees (ADR 0072 open question 11). A separate method
	// rather than an empty-string filter on the one above, because "" is a real stored value
	// (the operator's own jobs) and an ambiguous sentinel here would silently hand a
	// tenant_admin every job the super_admin started.
	ListEngineIngestJobsByTenant(ctx context.Context, role, tenantID string, limit int) ([]EngineIngestJob, error)
	GetEngineIngestJob(ctx context.Context, id string) (EngineIngestJob, bool, error)
	// ListActiveEngineIngestJobs is what the reconciler polls: only the jobs whose outcome is
	// still unknown, so a CP that has been up for a week does not ask ECS about last Tuesday.
	ListActiveEngineIngestJobs(ctx context.Context) ([]EngineIngestJob, error)
	// DeleteEngineIngestJob forgets ONE job by id, reporting found=false when it was already
	// gone. Deliberately the only removal there is: no TTL, no prune, no "delete everything
	// finished before X".
	//
	// 🔴 The reason there is no timer is that this table is not only a progress display. While
	// nothing in the catalogue points at the S3 key a `done` job wrote, this row is the
	// deployment's ONLY written record that those bytes exist — the Control Plane cannot list
	// the bucket, having no S3 permission at all (ADR 0072 review R3). Ageing rows out would
	// quietly delete the address of files that keep being paid for.
	//
	// It also does NOT filter by state. A `running` job must not be deleted (the ECS task
	// outlives the row and still writes a catalogue row nobody is waiting for), but that
	// refusal belongs to the route, which can say why: filtered here, "still running" and "no
	// such job" would arrive as the same found=false.
	DeleteEngineIngestJob(ctx context.Context, id string) (bool, error)
}

func (s *SQL) PutEngineIngestJob(ctx context.Context, j EngineIngestJob) error {
	now := NowTS()
	if j.CreatedAt == "" {
		j.CreatedAt = now
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO engine_ingest_jobs(`+engineIngestCols+`)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(id) DO UPDATE SET
		   task_arn=excluded.task_arn, state=excluded.state, message=excluded.message,
		   bytes=excluded.bytes, spec=excluded.spec, updated_at=excluded.updated_at`,
		j.ID, j.Role, j.ModelID, j.S3Key, j.Source, j.TaskArn, j.State, j.Message,
		j.Bytes, j.Spec, j.StartedBy, j.TenantID, j.CreatedAt, now)
	return err
}

func (s *SQL) ListEngineIngestJobs(ctx context.Context, role string, limit int) ([]EngineIngestJob, error) {
	return s.engineIngestList(ctx, role, "", false, limit)
}

func (s *SQL) ListEngineIngestJobsByTenant(ctx context.Context, role, tenantID string, limit int) ([]EngineIngestJob, error) {
	return s.engineIngestList(ctx, role, tenantID, true, limit)
}

// engineIngestList is the body of the two above. An explicit `byTenant` flag rather than
// "filter when tenantID is not empty" is what keeps the OPERATOR's own jobs (tenant_id is empty
// for those) out of a tenant's list: with a non-empty test, a caller that resolved no tenant
// would be handed exactly those.
func (s *SQL) engineIngestList(ctx context.Context, role, tenantID string, byTenant bool, limit int) ([]EngineIngestJob, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := `SELECT ` + engineIngestCols + ` FROM engine_ingest_jobs`
	var where []string
	var args []any
	if role != "" {
		where = append(where, `role=?`)
		args = append(args, role)
	}
	if byTenant {
		where = append(where, `tenant_id=?`)
		args = append(args, tenantID)
	}
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, ` AND `)
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	return s.engineIngestRows(ctx, q, args...)
}

func (s *SQL) ListActiveEngineIngestJobs(ctx context.Context) ([]EngineIngestJob, error) {
	return s.engineIngestRows(ctx,
		`SELECT `+engineIngestCols+` FROM engine_ingest_jobs WHERE state IN (?,?) ORDER BY created_at`,
		EngineIngestPending, EngineIngestRunning)
}

func (s *SQL) GetEngineIngestJob(ctx context.Context, id string) (EngineIngestJob, bool, error) {
	rows, err := s.engineIngestRows(ctx,
		`SELECT `+engineIngestCols+` FROM engine_ingest_jobs WHERE id=?`, id)
	if err != nil || len(rows) == 0 {
		return EngineIngestJob{}, false, err
	}
	return rows[0], true, nil
}

func (s *SQL) DeleteEngineIngestJob(ctx context.Context, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM engine_ingest_jobs WHERE id=?`, id)
	return affected(res, err)
}

func (s *SQL) engineIngestRows(ctx context.Context, q string, args ...any) ([]EngineIngestJob, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EngineIngestJob
	for rows.Next() {
		var j EngineIngestJob
		if err := rows.Scan(&j.ID, &j.Role, &j.ModelID, &j.S3Key, &j.Source, &j.TaskArn,
			&j.State, &j.Message, &j.Bytes, &j.Spec, &j.StartedBy, &j.TenantID,
			&j.CreatedAt, &j.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}
