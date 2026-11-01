package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Workspace is one container per Membership (= identity × tenant).
type Workspace struct {
	ID, TenantID, MembershipID      string
	ContainerName, Network, DataDir string
	// TenantSlug is the owning tenant's slug, joined in by every read rather than
	// stored on the row (the tenant table owns it). It exists because the AWS
	// adapters stamp `af-tenant` on billable resources and the cost-allocation tag
	// has to be READABLE in Cost Explorer — an opaque tenant id there would say
	// nothing the membership id does not already imply (docs/log/67 §67.4, ADR 0048
	// decision 3). Empty on a Workspace built in memory by a test.
	TenantSlug                   string
	AgentPort, AgentToken, State string
	CreatedAt, LastActiveAt      string
	// MemBytes is the RESOLVED per-workspace RAM cap in bytes for the NEXT container
	// start (0 = use the runtime's deployment default). It is NOT a persisted column:
	// buildResolved fills it via resolveWorkspaceMemBytes before the runtime is built,
	// and the factory (docker --memory / ECS task size) honors it when >0. Read/stop
	// call sites leave it 0, which never needs a memory value.
	MemBytes int64
	// CPUUnits and DiskGB are the other two RESOLVED size axes for the next container
	// start (0 = deployment default), filled alongside MemBytes by buildResolved.
	// CPUUnits is in Fargate CPU units (1024 = 1 vCPU); DiskGB is the working disk in
	// GiB. Like MemBytes they are not persisted columns — the persisted values live in
	// user_limit and are resolved through the tenant cap on the way here (ADR 0044).
	CPUUnits int
	DiskGB   int
	// SlotClass is the RESOLVED machine class for the next container start (docs/log/70).
	// Same lifecycle as the three axes above — not a persisted column, filled by
	// buildResolved through resolveSlotClass, and read only by the ecs-ec2 factory.
	// "" means "the deployment default", which is also what every other runtime sees.
	SlotClass string
	// PreviewSlug is the random label this workspace's preview subdomains are served
	// under (docs/log/81 §4): https://{PreviewSlug}-{port}.{PreviewDomain}. Persisted,
	// unlike the four resolved axes above — the preview request arrives on a bare
	// host with no session and no tenant selection, so the slug IS the lookup key.
	// Minted at container start and cleared on stop, so a stopped workspace's URLs
	// stop resolving and the previous start's URLs die with it.
	PreviewSlug string
}

// WorkspaceAutoStop is why the Control Plane itself last stopped a workspace, kept so a
// tenant admin can see it without the CP log (#1384). Kind is the stop's cause, today only
// "start-deadline"; Phase is the last boot phase the runtime reported before the stop
// (on ecs-ec2 the ECS sentence naming why the task cannot be placed), which the stop
// itself clears everywhere else. The row goes away when the workspace is next marked
// running.
type WorkspaceAutoStop struct {
	Kind         string `json:"kind"`
	Phase        string `json:"phase"`
	LimitMinutes int    `json:"limit_minutes"`
	StoppedAt    string `json:"stopped_at"`
}

// CurrentAutoStop is the membership's WorkspaceAutoStop when state, the live runtime state,
// says the workspace is still down, else nil. The row is only deleted when a start reaches
// SetWorkspaceState("running"), so a launch still in flight must not be shown as stopped.
// A read error is nil too: the admin views it feeds degrade to plain "stopped".
func CurrentAutoStop(ctx context.Context, s WorkspaceStore, membershipID, state string) *WorkspaceAutoStop {
	if state == "running" || state == "starting" {
		return nil
	}
	a, ok, err := s.GetWorkspaceAutoStopByMembership(ctx, membershipID)
	if err != nil || !ok {
		return nil
	}
	return &a
}

type WorkspaceStore interface {
	GetWorkspaceByMembership(ctx context.Context, membershipID string) (Workspace, bool, error)
	CreateWorkspace(ctx context.Context, ws Workspace) error
	// DeleteWorkspace removes the row and its dependents. Irreversible, and only ever
	// reached through the explicit destroy operation (ADR 0045 decision 13-2).
	DeleteWorkspace(ctx context.Context, workspaceID string) error
	// SetWorkspaceState also deletes the WorkspaceAutoStop row when state is "running":
	// every start path ends there, so none of them can leave a stale reason behind.
	SetWorkspaceState(ctx context.Context, workspaceID, state string) error
	SetWorkspaceAutoStop(ctx context.Context, workspaceID string, a WorkspaceAutoStop) error
	// ClearWorkspaceAutoStop is called just before a Start: a start that then fails must not
	// read as the previous launch's stop.
	ClearWorkspaceAutoStop(ctx context.Context, workspaceID string) error
	GetWorkspaceAutoStopByMembership(ctx context.Context, membershipID string) (WorkspaceAutoStop, bool, error)
	RecordWorkspaceActivity(ctx context.Context, workspaceID, lastSeenAt, connectedUntil, now string) (bool, error)
	WorkspaceHasRecentActivity(ctx context.Context, workspaceID, cutoff, now string) (bool, error)
	ClaimWorkspaceIdleStop(ctx context.Context, workspaceID, ownerMembershipID, operationID, cutoff, now string) (bool, error)
	ReleaseWorkspaceIdleStop(ctx context.Context, workspaceID, operationID string) error
	ClearWorkspaceIdleStop(ctx context.Context, workspaceID string) error
	AcquireWorkspaceOperationFence(ctx context.Context, workspaceID string) (func(), error)
	// Per-workspace member settings (JSON blob; "" = none). CP-owned so they can be
	// read/written while the container is stopped; mapped to env at container start.
	GetWorkspaceSettings(ctx context.Context, workspaceID string) (string, error)
	SetWorkspaceSettings(ctx context.Context, workspaceID, settingsJSON string) error
	MaxAgentPort(ctx context.Context) (int, error)
	ListWorkspaces(ctx context.Context, tenantID string) ([]Workspace, error)
	// Preview subdomain slug (docs/log/81 §4). SetWorkspacePreviewSlug writes the slug
	// minted for THIS container start (""=clear, on stop/destroy);
	// GetWorkspaceByPreviewSlug is the reverse lookup a preview request needs — it is
	// the ONLY thing that request carries, so it must resolve without a session.
	SetWorkspacePreviewSlug(ctx context.Context, workspaceID, slug string) error
	GetWorkspaceByPreviewSlug(ctx context.Context, slug string) (Workspace, bool, error)
}

func (s *SQL) SetWorkspaceState(ctx context.Context, workspaceID, state string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = setWorkspaceStateTx(ctx, tx, workspaceID, state); err != nil {
		return err
	}
	return tx.Commit()
}

// setWorkspaceStateTx is SetWorkspaceState inside a caller's transaction.
func setWorkspaceStateTx(ctx context.Context, tx *sqlTx, workspaceID, state string) (err error) {
	if err = lockWorkspace(ctx, tx, workspaceID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE workspace SET state=?, last_active_at=? WHERE id=?`, state, NowTS(), workspaceID); err != nil {
		return err
	}
	// "a workspace that is not running has no preview slug" is tied to the state
	// transition itself (docs/log/81 §4). There is more than one way to stop — the
	// user's stop, the idle stop, a recreate, a home clean — so clearing the slug at
	// each of them would eventually miss one, and the symptom is the previous URL
	// still answering after a restart: the promise that it is reissued per launch,
	// silently broken. Here, a new stop path is covered without touching it.
	if state != "running" {
		if _, err = tx.ExecContext(ctx, `UPDATE workspace SET preview_slug='' WHERE id=?`, workspaceID); err != nil {
			return err
		}
		// Temporary preview grants die with the start they were made in.
		if err = clearPreviewGrantsTx(ctx, tx, workspaceID); err != nil {
			return err
		}
	}
	if state == "running" {
		if _, err = tx.ExecContext(ctx, `DELETE FROM workspace_auto_stop WHERE workspace_id=?`, workspaceID); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM workspace_stop_intent WHERE workspace_id=?`, workspaceID); err != nil {
		return err
	}
	return nil
}

// RecordWorkspaceActivity merges a monotonic activity watermark and connection
// lease from any CP replica. CASE is portable across SQLite/Postgres and prevents
// an inactive replica from shortening another replica's live lease.
func lockWorkspace(ctx context.Context, q sqlExecQuery, workspaceID string) error {
	_, err := q.ExecContext(ctx, `UPDATE workspace SET id=id WHERE id=?`, workspaceID)
	return err
}

func (s *SQL) RecordWorkspaceActivity(ctx context.Context, workspaceID, lastSeenAt, connectedUntil, now string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err = lockWorkspace(ctx, tx, workspaceID); err != nil {
		return false, err
	}
	var stopping int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspace_stop_intent WHERE workspace_id=?`, workspaceID).Scan(&stopping); err != nil {
		return false, err
	}
	if stopping != 0 {
		return false, nil
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO workspace_activity
		(workspace_id, last_seen_at, connected_until, updated_at) VALUES(?,?,?,?)
		ON CONFLICT(workspace_id) DO UPDATE SET
		 last_seen_at=CASE WHEN workspace_activity.last_seen_at > excluded.last_seen_at
		                   THEN workspace_activity.last_seen_at ELSE excluded.last_seen_at END,
		 connected_until=CASE WHEN workspace_activity.connected_until > excluded.connected_until
		                      THEN workspace_activity.connected_until ELSE excluded.connected_until END,
		 updated_at=excluded.updated_at`, workspaceID, lastSeenAt, connectedUntil, now); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *SQL) WorkspaceHasRecentActivity(ctx context.Context, workspaceID, cutoff, now string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM workspace_activity
		WHERE workspace_id=? AND (last_seen_at > ? OR connected_until > ?)`, workspaceID, cutoff, now).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func (s *SQL) ClaimWorkspaceIdleStop(ctx context.Context, workspaceID, owner, operationID, cutoff, now string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err = lockWorkspace(ctx, tx, workspaceID); err != nil {
		return false, err
	}
	var leaseActive int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM session_share_owner_lease
		WHERE owner_membership_id=? AND operation_id=? AND expires_at>?`, owner, operationID, now).Scan(&leaseActive); err != nil {
		return false, err
	}
	if leaseActive == 0 {
		return false, nil
	}
	var recent int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspace_activity
		WHERE workspace_id=? AND (last_seen_at>? OR connected_until>?)`, workspaceID, cutoff, now).Scan(&recent); err != nil {
		return false, err
	}
	if recent != 0 {
		return false, nil
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO workspace_stop_intent
		(workspace_id,owner_membership_id,operation_id,created_at) VALUES(?,?,?,?)
		ON CONFLICT(workspace_id) DO UPDATE SET owner_membership_id=excluded.owner_membership_id,
		operation_id=excluded.operation_id,created_at=excluded.created_at`, workspaceID, owner, operationID, now); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *SQL) ReleaseWorkspaceIdleStop(ctx context.Context, workspaceID, operationID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM workspace_stop_intent WHERE workspace_id=? AND operation_id=?`, workspaceID, operationID)
	return err
}

// ClearWorkspaceIdleStop is the explicit lifecycle reconciliation path. Callers
// must already own the distributed lifecycle lease (and adapter host fence), so
// an intent orphaned by a crashed reaper can be safely superseded.
func (s *SQL) ClearWorkspaceIdleStop(ctx context.Context, workspaceID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockWorkspace(ctx, tx, workspaceID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM workspace_stop_intent WHERE workspace_id=?`, workspaceID); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteWorkspace removes the workspace row and everything keyed to it. It is the DB
// half of the irreversible destroy (ADR 0045 decision 13); the runtime half (home, cloud
// resources) has already run by the time this is called.
//
// The dependents are deleted EXPLICITLY rather than left to ON DELETE CASCADE: only two
// of the five tables declare one, and a delete that half-works because of a schema
// detail is the kind of thing that shows up as a foreign-key error in production and
// nowhere in tests. session_share_proposal does cascade off shared_session_catalog on
// both dialects, so it is the one dependent left implicit.
func (s *SQL) DeleteWorkspace(ctx context.Context, workspaceID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = deleteWorkspaceTx(ctx, tx, workspaceID); err != nil {
		return err
	}
	return tx.Commit()
}

// deleteWorkspaceTx is DeleteWorkspace inside a caller's transaction.
func deleteWorkspaceTx(ctx context.Context, tx *sqlTx, workspaceID string) error {
	if err := lockWorkspace(ctx, tx, workspaceID); err != nil {
		return err
	}
	for _, stmt := range []string{
		`DELETE FROM workspace_stop_intent WHERE workspace_id=?`,
		`DELETE FROM workspace_auto_stop WHERE workspace_id=?`,
		`DELETE FROM workspace_activity WHERE workspace_id=?`,
		`DELETE FROM preview_temp_port WHERE workspace_id=?`,
		`DELETE FROM preview_epoch WHERE workspace_id=?`,
		`DELETE FROM dev_idp_client WHERE workspace_id=?`,
		`DELETE FROM dev_idp_grant WHERE workspace_id=?`,
		`DELETE FROM dev_idp_persona WHERE workspace_id=?`,
		`DELETE FROM shared_session_catalog WHERE workspace_id=?`,
		`DELETE FROM session_work_link WHERE workspace_id=?`,
		`DELETE FROM session WHERE workspace_id=?`,
		`DELETE FROM workspace_disk WHERE workspace_id=?`,
		`DELETE FROM workspace_notice WHERE workspace_id=?`,
		`DELETE FROM wrapped_dek WHERE workspace_id=?`,
		`DELETE FROM workspace WHERE id=?`,
	} {
		if _, err := tx.ExecContext(ctx, stmt, workspaceID); err != nil {
			return fmt.Errorf("%s: %w", stmt, err)
		}
	}
	return nil
}

// AcquireWorkspaceOperationFence holds a Postgres session advisory lock across
// external Runtime I/O. Unlike a time-based lease it remains held while a CP is
// paused and is released automatically if the process/connection dies. SQLite is
// the single-CP local profile; native additionally supplies its kernel flock.
func (s *SQL) AcquireWorkspaceOperationFence(ctx context.Context, workspaceID string) (func(), error) {
	if s.dialect != "postgres" {
		return func() {}, nil
	}
	var conn *sql.Conn
	for {
		var err error
		conn, err = s.db.DB.Conn(ctx)
		if err != nil {
			return nil, err
		}
		var acquired bool
		err = conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1, 0))`, workspaceID).Scan(&acquired)
		if err != nil {
			discardSQLConn(conn) // server may have acquired immediately before the error
			return nil, err
		}
		if acquired {
			break
		}
		_ = conn.Close() // return the waiter connection before polling again
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			unlockCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			var unlocked bool
			err := conn.QueryRowContext(unlockCtx, `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, workspaceID).Scan(&unlocked)
			cancel()
			if err != nil || !unlocked {
				discardSQLConn(conn)
				return
			}
			_ = conn.Close()
		})
	}, nil
}

// database/sql Conn.Close returns a physical PG session to the pool, which is
// unsafe when advisory-lock ownership is uncertain. ErrBadConn tells the pool to
// destroy that session; PostgreSQL then releases all of its session locks.
func discardSQLConn(conn *sql.Conn) {
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	_ = conn.Close()
}

func (s *SQL) GetWorkspaceByMembership(ctx context.Context, membershipID string) (Workspace, bool, error) {
	ws, err := scanWorkspace(s.db.QueryRowContext(ctx, workspaceCols+` WHERE w.membership_id=?`, membershipID))
	if err == sql.ErrNoRows {
		return Workspace{}, false, nil
	}
	if err != nil {
		return Workspace{}, false, err
	}
	return ws, true, nil
}

func (s *SQL) CreateWorkspace(ctx context.Context, ws Workspace) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO workspace(id, tenant_id, membership_id, container_name, network, data_dir,
		   agent_port, agent_token, state, created_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?)`,
		ws.ID, ws.TenantID, ws.MembershipID, ws.ContainerName, ws.Network, ws.DataDir,
		ws.AgentPort, ws.AgentToken, ws.State, ws.CreatedAt)
	return err
}

func (s *SQL) GetWorkspaceSettings(ctx context.Context, workspaceID string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(settings,'') FROM workspace WHERE id=?`, workspaceID).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

func (s *SQL) SetWorkspaceSettings(ctx context.Context, workspaceID, settingsJSON string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE workspace SET settings=? WHERE id=?`, settingsJSON, workspaceID)
	return err
}

// SetWorkspacePreviewSlug records the slug this container start is reachable under
// (docs/log/81 §4); "" clears it, which is what a stop does. The unique index is partial
// (non-empty only), so every stopped workspace can hold "" at once.
//
// The temporary preview grants (preview_temp_port) belong to one slug, so they go in the same
// transaction: a grant saved under the old slug can no longer land after the rotation.
func (s *SQL) SetWorkspacePreviewSlug(ctx context.Context, workspaceID, slug string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockWorkspace(ctx, tx, workspaceID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE workspace SET preview_slug=? WHERE id=?`, slug, workspaceID); err != nil {
		return err
	}
	if err = clearPreviewGrantsTx(ctx, tx, workspaceID); err != nil {
		return err
	}
	return tx.Commit()
}

// SetWorkspaceAutoStop records why the Control Plane stopped the workspace, replacing the
// previous record.
func (s *SQL) SetWorkspaceAutoStop(ctx context.Context, workspaceID string, a WorkspaceAutoStop) error {
	return setWorkspaceAutoStopTx(ctx, s.db, workspaceID, a)
}

// setWorkspaceAutoStopTx is SetWorkspaceAutoStop on q, a caller's transaction or the pool.
func setWorkspaceAutoStopTx(ctx context.Context, q sqlExecQuery, workspaceID string, a WorkspaceAutoStop) error {
	_, err := q.ExecContext(ctx, `INSERT INTO workspace_auto_stop(workspace_id, kind, phase, limit_minutes, stopped_at)
		VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(workspace_id) DO UPDATE SET kind=excluded.kind, phase=excluded.phase,
		  limit_minutes=excluded.limit_minutes, stopped_at=excluded.stopped_at`,
		workspaceID, a.Kind, a.Phase, a.LimitMinutes, a.StoppedAt)
	return err
}

// ClearWorkspaceAutoStop deletes the automatic-stop record, if any.
func (s *SQL) ClearWorkspaceAutoStop(ctx context.Context, workspaceID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM workspace_auto_stop WHERE workspace_id=?`, workspaceID)
	return err
}

// GetWorkspaceAutoStopByMembership returns the automatic-stop record of the membership's
// workspace; ok is false when there is none.
func (s *SQL) GetWorkspaceAutoStopByMembership(ctx context.Context, membershipID string) (WorkspaceAutoStop, bool, error) {
	var a WorkspaceAutoStop
	err := s.db.QueryRowContext(ctx, `SELECT a.kind, a.phase, a.limit_minutes, a.stopped_at
		FROM workspace_auto_stop a JOIN workspace w ON w.id = a.workspace_id
		WHERE w.membership_id=?`, membershipID).Scan(&a.Kind, &a.Phase, &a.LimitMinutes, &a.StoppedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return a, false, nil
	}
	if err != nil {
		return a, false, err
	}
	return a, true, nil
}

// GetWorkspaceByPreviewSlug resolves a preview request's host label back to the
// workspace. An empty slug never matches: that is the value every STOPPED workspace
// carries, and a lookup that matched it would hand a stranger the first stopped
// workspace in the table.
func (s *SQL) GetWorkspaceByPreviewSlug(ctx context.Context, slug string) (Workspace, bool, error) {
	if slug == "" {
		return Workspace{}, false, nil
	}
	ws, err := scanWorkspace(s.db.QueryRowContext(ctx, workspaceCols+` WHERE w.preview_slug=?`, slug))
	if err == sql.ErrNoRows {
		return Workspace{}, false, nil
	}
	if err != nil {
		return Workspace{}, false, err
	}
	return ws, true, nil
}

func (s *SQL) MaxAgentPort(ctx context.Context) (int, error) {
	var max int
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(CAST(agent_port AS INTEGER)), 0) FROM workspace`).Scan(&max)
	return max, err
}

func (s *SQL) ListWorkspaces(ctx context.Context, tenantID string) ([]Workspace, error) {
	rows, err := s.db.QueryContext(ctx, workspaceCols+` WHERE w.tenant_id=? ORDER BY w.created_at`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Workspace
	for rows.Next() {
		ws, err := scanWorkspace(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ws)
	}
	return out, rows.Err()
}

// workspaceCols joins the tenant only for its slug (Workspace.TenantSlug). A LEFT
// JOIN on purpose: a workspace whose tenant row is missing is already broken, but it
// must still be readable — the alternative is that the sweeper and the reaper stop
// seeing it, which turns a bad row into leaked AWS resources.
const workspaceCols = `SELECT w.id, w.tenant_id, COALESCE(t.slug,''), w.membership_id,
	w.container_name, w.network, w.data_dir, w.agent_port, w.agent_token, w.state,
	w.created_at, COALESCE(w.last_active_at,''), COALESCE(w.preview_slug,'')
	FROM workspace w LEFT JOIN tenant t ON t.id = w.tenant_id`

func scanWorkspace(row scanner) (Workspace, error) {
	var ws Workspace
	err := row.Scan(&ws.ID, &ws.TenantID, &ws.TenantSlug, &ws.MembershipID, &ws.ContainerName,
		&ws.Network, &ws.DataDir, &ws.AgentPort, &ws.AgentToken, &ws.State, &ws.CreatedAt,
		&ws.LastActiveAt, &ws.PreviewSlug)
	return ws, err
}
