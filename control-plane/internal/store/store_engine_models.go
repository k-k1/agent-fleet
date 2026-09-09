package store

// store_engine_models.go — the engine model catalogue (ADR 0072 decision 2).
//
// Dialect-neutral like the rest of the store: `?` placeholders the wrapper rebinds, and
// `ON CONFLICT … DO UPDATE`, which SQLite and Postgres spell the same way.
//
// The three list-shaped columns (files, args, sizes) are JSON text rather than child tables.
// They are read and written whole, always by the same row's owner, and nothing joins or filters
// on their contents — a child table would buy referential integrity for a relation nobody
// queries and cost three more migrations to keep in step.

import (
	"context"
	"database/sql"
	"encoding/json"
)

const engineModelCols = `role, id, kind, files, enabled, selected, is_default, args,
	context_tokens, max_output_tokens, sizes, description, vram_mib,
	license, license_name, license_url, model_precision, base_model,
	license_accepted_by, license_accepted_at, commercial_use, source, created_at, updated_at`

func (s *SQL) ListEngineModels(ctx context.Context, role string) ([]EngineModel, error) {
	q := `SELECT ` + engineModelCols + ` FROM engine_models`
	var args []any
	if role != "" {
		q += ` WHERE role=?`
		args = append(args, role)
	}
	q += ` ORDER BY role, id`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EngineModel
	for rows.Next() {
		var (
			m                        EngineModel
			files, argsJSON, sizes   string
			enabled, selected, isDef int
		)
		if err := rows.Scan(&m.Role, &m.ID, &m.Kind, &files, &enabled, &selected, &isDef, &argsJSON,
			&m.ContextTokens, &m.MaxOutputTokens, &sizes, &m.Description, &m.VramMiB,
			&m.License, &m.LicenseName, &m.LicenseURL, &m.Precision, &m.BaseModel,
			&m.LicenseAcceptedBy, &m.LicenseAcceptedAt, &m.CommercialUse, &m.Source,
			&m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		m.Enabled, m.Selected, m.Default = enabled != 0, selected != 0, isDef != 0
		// A row whose JSON does not parse is returned with that list empty rather than
		// failing the whole listing: one unreadable catalogue entry must not take the admin
		// panel — the only place it can be deleted from — down with it.
		_ = json.Unmarshal([]byte(files), &m.Files)
		_ = json.Unmarshal([]byte(argsJSON), &m.Args)
		_ = json.Unmarshal([]byte(sizes), &m.Sizes)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *SQL) PutEngineModel(ctx context.Context, m EngineModel) error {
	now := NowTS()
	if m.CreatedAt == "" {
		m.CreatedAt = now
	}
	files := jsonList(m.Files)
	args := jsonList(m.Args)
	sizes := jsonList(m.Sizes)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO engine_models(`+engineModelCols+`)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(role, id) DO UPDATE SET
		   kind=excluded.kind, files=excluded.files, enabled=excluded.enabled,
		   selected=excluded.selected, is_default=excluded.is_default, args=excluded.args,
		   context_tokens=excluded.context_tokens, max_output_tokens=excluded.max_output_tokens,
		   sizes=excluded.sizes, description=excluded.description, vram_mib=excluded.vram_mib,
		   license=excluded.license, license_name=excluded.license_name,
		   license_url=excluded.license_url, model_precision=excluded.model_precision,
		   base_model=excluded.base_model, license_accepted_by=excluded.license_accepted_by,
		   license_accepted_at=excluded.license_accepted_at,
		   commercial_use=excluded.commercial_use, source=excluded.source,
		   updated_at=excluded.updated_at`,
		m.Role, m.ID, m.Kind, files, boolInt(m.Enabled), boolInt(m.Selected), boolInt(m.Default), args,
		m.ContextTokens, m.MaxOutputTokens, sizes, m.Description, m.VramMiB,
		m.License, m.LicenseName, m.LicenseURL, m.Precision, m.BaseModel,
		m.LicenseAcceptedBy, m.LicenseAcceptedAt, m.CommercialUse, m.Source, m.CreatedAt, now)
	return err
}

func (s *SQL) SetEngineModelEnabled(ctx context.Context, role, id string, enabled bool) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE engine_models SET enabled=?, updated_at=? WHERE role=? AND id=?`,
		boolInt(enabled), NowTS(), role, id)
	return affected(res, err)
}

// SetEngineModelSelected and SetEngineModelDefault clear the role's other rows in the SAME
// transaction as the one they set. Doing it in two calls leaves a window in which the image
// role has two selected checkpoints, and the sidecar reading the active set in that window
// builds a command line for whichever it saw first — a failure that reproduces once.
func (s *SQL) SetEngineModelSelected(ctx context.Context, role, id string) (bool, error) {
	return s.setEngineModelExclusive(ctx, "selected", role, id)
}

func (s *SQL) SetEngineModelDefault(ctx context.Context, role, id string) (bool, error) {
	return s.setEngineModelExclusive(ctx, "is_default", role, id)
}

// setEngineModelExclusive is shared by the two above. `col` is never caller-supplied — the two
// exported wrappers are the only callers and both pass a literal.
func (s *SQL) setEngineModelExclusive(ctx context.Context, col, role, id string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	now := NowTS()
	if _, err := tx.ExecContext(ctx,
		`UPDATE engine_models SET `+col+`=0, updated_at=? WHERE role=? AND `+col+`<>0`, now, role); err != nil {
		return false, err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE engine_models SET `+col+`=1, enabled=1, updated_at=? WHERE role=? AND id=?`, now, role, id)
	ok, err := affected(res, err)
	if err != nil || !ok {
		return false, err
	}
	return true, tx.Commit()
}

func (s *SQL) DeleteEngineModel(ctx context.Context, role, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM engine_models WHERE role=? AND id=?`, role, id)
	return affected(res, err)
}

// jsonList marshals a slice to the text the column holds. A nil slice becomes `[]` rather than
// `null`, so a reader never has to tell the two apart.
func jsonList(v any) string {
	b, err := json.Marshal(v)
	if err != nil || len(b) == 0 || string(b) == "null" {
		return "[]"
	}
	return string(b)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// affected turns "did that statement touch a row" into the bool the callers answer 404 from.
// RowsAffected is not supported by every driver; an error from it is reported as "yes", because
// the statement itself succeeded and claiming the row was absent would be the wrong lie.
func affected(res sql.Result, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	n, aerr := res.RowsAffected()
	if aerr != nil {
		return true, nil
	}
	return n > 0, nil
}
