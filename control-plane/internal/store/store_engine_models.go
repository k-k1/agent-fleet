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
	"errors"
	"strings"
)

const engineModelCols = `role, id, kind, files, enabled, selected, is_default, args,
	context_tokens, max_output_tokens, sizes, description, vram_mib,
	license, license_name, license_url, model_precision, base_model,
	license_accepted_by, license_accepted_at, license_accepted_tenant, license_accepted_license,
	commercial_use, source, kv_layers, kv_heads_kv, kv_key_len, kv_value_len,
	negative_prompt, trained_words, params,
	display_name, version_name, preview_url, thumb_url, created_at, updated_at`

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
			params, trained          string
			enabled, selected, isDef int
		)
		if err := rows.Scan(&m.Role, &m.ID, &m.Kind, &files, &enabled, &selected, &isDef, &argsJSON,
			&m.ContextTokens, &m.MaxOutputTokens, &sizes, &m.Description, &m.VramMiB,
			&m.License, &m.LicenseName, &m.LicenseURL, &m.Precision, &m.BaseModel,
			&m.LicenseAcceptedBy, &m.LicenseAcceptedAt,
			&m.LicenseAcceptedTenant, &m.LicenseAcceptedLicense,
			&m.CommercialUse, &m.Source,
			&m.KVLayers, &m.KVHeadsKV, &m.KVKeyLen, &m.KVValueLen,
			&m.NegativePrompt, &trained, &params,
			&m.DisplayName, &m.VersionName, &m.PreviewURL, &m.ThumbURL,
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
		_ = json.Unmarshal([]byte(trained), &m.TrainedWords)
		// Absent rather than zeroed when the column is empty or unreadable: a row that declares
		// no parameters must reach the provider as "use the family's recipe", and an
		// EngineParams full of zeros says the same thing only as long as nobody adds a field
		// whose zero value means something.
		if strings.TrimSpace(params) != "" {
			var p EngineParams
			if json.Unmarshal([]byte(params), &p) == nil && p != (EngineParams{}) {
				m.Params = &p
			}
		}
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
	trained := jsonList(m.TrainedWords)
	params := ""
	if m.Params != nil && *m.Params != (EngineParams{}) {
		if b, err := json.Marshal(m.Params); err == nil {
			params = string(b)
		}
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO engine_models(`+engineModelCols+`)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(role, id) DO UPDATE SET
		   kind=excluded.kind, files=excluded.files, enabled=excluded.enabled,
		   selected=excluded.selected, is_default=excluded.is_default, args=excluded.args,
		   context_tokens=excluded.context_tokens, max_output_tokens=excluded.max_output_tokens,
		   sizes=excluded.sizes, description=excluded.description, vram_mib=excluded.vram_mib,
		   license=excluded.license, license_name=excluded.license_name,
		   license_url=excluded.license_url, model_precision=excluded.model_precision,
		   base_model=excluded.base_model, license_accepted_by=excluded.license_accepted_by,
		   license_accepted_at=excluded.license_accepted_at,
		   license_accepted_tenant=excluded.license_accepted_tenant,
		   license_accepted_license=excluded.license_accepted_license,
		   commercial_use=excluded.commercial_use, source=excluded.source,
		   kv_layers=excluded.kv_layers, kv_heads_kv=excluded.kv_heads_kv,
		   kv_key_len=excluded.kv_key_len, kv_value_len=excluded.kv_value_len,
		   negative_prompt=excluded.negative_prompt, trained_words=excluded.trained_words,
		   params=excluded.params, display_name=excluded.display_name,
		   version_name=excluded.version_name, preview_url=excluded.preview_url,
		   thumb_url=excluded.thumb_url, updated_at=excluded.updated_at`,
		m.Role, m.ID, m.Kind, files, boolInt(m.Enabled), boolInt(m.Selected), boolInt(m.Default), args,
		m.ContextTokens, m.MaxOutputTokens, sizes, m.Description, m.VramMiB,
		m.License, m.LicenseName, m.LicenseURL, m.Precision, m.BaseModel,
		m.LicenseAcceptedBy, m.LicenseAcceptedAt, m.LicenseAcceptedTenant, m.LicenseAcceptedLicense,
		m.CommercialUse, m.Source,
		m.KVLayers, m.KVHeadsKV, m.KVKeyLen, m.KVValueLen,
		m.NegativePrompt, trained, params,
		m.DisplayName, m.VersionName, m.PreviewURL, m.ThumbURL, m.CreatedAt, now)
	return err
}

// CreateEngineModel is the ingest writer. Unlike the administrative Put above, a collision is
// not an edit: the new bytes and their licence were chosen for a different row, so the caller
// must keep the row that won the race and report the conflict.
func (s *SQL) CreateEngineModel(ctx context.Context, m EngineModel) (bool, error) {
	now := NowTS()
	if m.CreatedAt == "" {
		m.CreatedAt = now
	}
	params := ""
	if m.Params != nil && *m.Params != (EngineParams{}) {
		if b, err := json.Marshal(m.Params); err == nil {
			params = string(b)
		}
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO engine_models(`+engineModelCols+`)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(role, id) DO NOTHING`,
		m.Role, m.ID, m.Kind, jsonList(m.Files), boolInt(m.Enabled), boolInt(m.Selected), boolInt(m.Default), jsonList(m.Args),
		m.ContextTokens, m.MaxOutputTokens, jsonList(m.Sizes), m.Description, m.VramMiB,
		m.License, m.LicenseName, m.LicenseURL, m.Precision, m.BaseModel,
		m.LicenseAcceptedBy, m.LicenseAcceptedAt, m.LicenseAcceptedTenant, m.LicenseAcceptedLicense,
		m.CommercialUse, m.Source,
		m.KVLayers, m.KVHeadsKV, m.KVKeyLen, m.KVValueLen,
		m.NegativePrompt, jsonList(m.TrainedWords), params,
		m.DisplayName, m.VersionName, m.PreviewURL, m.ThumbURL, m.CreatedAt, now)
	return affected(res, err)
}

// AppendEngineModelFile adds one file to a row that already exists, which is what lets a SPLIT
// model be assembled by taking its parts in one at a time (ADR 0072 decision 2's
// `text_encoders/`). Without it an ingest could only ever create a row of one file, and a
// four-file FLUX.1 had to be staged as three throwaway rows and then re-typed through
// `POST /models`.
//
// An optimistic compare-and-swap rather than PutEngineModel: the row carries a licence
// acceptance, a source and an enabled flag that the ingest that is appending knows nothing
// about. Comparing the JSON read also keeps two concurrent parts from overwriting each other.
//
// A key already listed is a no-op reporting success: the caller is a job reconciler that may
// see the same finished task twice, and a second append would put the same file in the active
// set twice.
var ErrEngineModelFileSlotTaken = errors.New("engine model file slot is already taken")

func (s *SQL) AppendEngineModelFile(ctx context.Context, role, id string, f EngineModelFile) (bool, error) {
	for {
		var raw string
		switch err := s.db.QueryRowContext(ctx,
			`SELECT files FROM engine_models WHERE role=? AND id=?`, role, id).Scan(&raw); {
		case err == sql.ErrNoRows:
			return false, nil
		case err != nil:
			return false, err
		}
		var files []EngineModelFile
		_ = json.Unmarshal([]byte(raw), &files)
		for _, e := range files {
			if e.S3Key == f.S3Key {
				return true, nil
			}
			if strings.TrimSpace(e.Flag) == strings.TrimSpace(f.Flag) {
				return false, ErrEngineModelFileSlotTaken
			}
		}
		files = append(files, f)
		updated, err := affected(s.db.ExecContext(ctx,
			`UPDATE engine_models SET files=?, updated_at=? WHERE role=? AND id=? AND files=?`,
			jsonList(files), NowTS(), role, id, raw))
		if err != nil || updated {
			return updated, err
		}
	}
}

// ReplaceEngineModelFile swaps the file a row holds under one flag, leaving the rest of the row
// exactly as it is — which is the whole point of it existing.
//
// 🔴 The gap it closes: AppendEngineModelFile refuses a flag the row already declares, and the
// unlabelled slot is THE checkpoint, which cannot be appended to at all. So until this, changing
// which file a model reads meant forgetting the row and taking it in again — and the row is
// where the licence acceptance (a record of a human act), the family, the params, the enabled
// flag and the provenance live. Swapping a t5xxl for another quantisation threw all of them away.
//
// The same optimistic compare-and-swap as the append above, and for the same reason: an upsert
// through PutEngineModel would have to carry every other column back out and in again, while an
// unchecked read-modify-write could discard a part attached at the same time.
//
// A flag the row does not declare is NOT created here. "Replace what is there" and "add a part"
// are different acts with different refusals, and silently turning one into the other is how a
// typo in a flag becomes a row with two checkpoints.
func (s *SQL) ReplaceEngineModelFile(ctx context.Context, role, id string, f EngineModelFile, kv *EngineModelKV) (bool, error) {
	for {
		var raw string
		switch err := s.db.QueryRowContext(ctx,
			`SELECT files FROM engine_models WHERE role=? AND id=?`, role, id).Scan(&raw); {
		case err == sql.ErrNoRows:
			return false, nil
		case err != nil:
			return false, err
		}
		var files []EngineModelFile
		_ = json.Unmarshal([]byte(raw), &files)
		at := -1
		for i, e := range files {
			if strings.TrimSpace(e.Flag) == strings.TrimSpace(f.Flag) {
				at = i
				break
			}
		}
		if at < 0 {
			return false, nil
		}
		files[at] = f
		var updated bool
		var err error
		if kv == nil {
			updated, err = affected(s.db.ExecContext(ctx,
				`UPDATE engine_models SET files=?, updated_at=? WHERE role=? AND id=? AND files=?`,
				jsonList(files), NowTS(), role, id, raw))
		} else {
			// Written even when it is all zeros: an unreadable header means the row's geometry is
			// now UNKNOWN. Keeping the previous file's numbers would describe bytes no longer used.
			updated, err = affected(s.db.ExecContext(ctx,
				`UPDATE engine_models SET files=?, kv_layers=?, kv_heads_kv=?, kv_key_len=?, kv_value_len=?,
				   updated_at=? WHERE role=? AND id=? AND files=?`,
				jsonList(files), kv.Layers, kv.HeadsKV, kv.KeyLen, kv.ValueLen,
				NowTS(), role, id, raw))
		}
		if err != nil || updated {
			return updated, err
		}
	}
}

// MoveEngineModelFile rewrites the one file a row holds at `fromKey` — its role AND its key —
// leaving every other file and every other column alone. It is the catalogue half of relocating
// bytes inside the bucket, and it exists because neither of the two writers above can express it:
// appending would leave the old declaration behind (two files, one of them pointing at a key the
// move emptied) and replacing is keyed by the flag, which is the very thing that changes.
//
// The same optimistic compare-and-swap as its neighbours, and idempotent in the one way that
// matters: a reconciler that sees the same finished task twice finds the row already moved —
// `fromKey` gone, `f` present — and reports success rather than failing a job that did its work.
func (s *SQL) MoveEngineModelFile(ctx context.Context, role, id, fromKey string, f EngineModelFile) (bool, error) {
	for {
		var raw string
		switch err := s.db.QueryRowContext(ctx,
			`SELECT files FROM engine_models WHERE role=? AND id=?`, role, id).Scan(&raw); {
		case err == sql.ErrNoRows:
			return false, nil
		case err != nil:
			return false, err
		}
		var files []EngineModelFile
		_ = json.Unmarshal([]byte(raw), &files)
		at, done := -1, false
		for i, e := range files {
			switch {
			case strings.TrimSpace(e.S3Key) == strings.TrimSpace(fromKey):
				at = i
			case strings.TrimSpace(e.S3Key) == strings.TrimSpace(f.S3Key) &&
				strings.TrimSpace(e.Flag) == strings.TrimSpace(f.Flag):
				done = true
			}
		}
		if at < 0 {
			return done, nil
		}
		// 🔴 The destination role must be free, or the move would silently give the row two files
		// under one flag — the state AppendEngineModelFile refuses for the same reason.
		for i, e := range files {
			if i != at && strings.TrimSpace(e.Flag) == strings.TrimSpace(f.Flag) {
				return false, ErrEngineModelFileSlotTaken
			}
		}
		files[at] = f
		updated, err := affected(s.db.ExecContext(ctx,
			`UPDATE engine_models SET files=?, updated_at=? WHERE role=? AND id=? AND files=?`,
			jsonList(files), NowTS(), role, id, raw))
		if err != nil || updated {
			return updated, err
		}
	}
}

func (s *SQL) SetEngineModelEnabled(ctx context.Context, role, id string, enabled bool) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE engine_models SET enabled=?, updated_at=? WHERE role=? AND id=?`,
		boolInt(enabled), NowTS(), role, id)
	return affected(res, err)
}

// SetEngineModelBaseModel corrects ONE column, and exists because a row can be complete in
// every other way and still unusable: ComfyUI picks its workflow graph from the family and
// refuses to guess one, so a seeded row (the seed cannot know a family) or one written before
// the family was validated has to be fixable without being re-typed. A targeted UPDATE rather
// than a read-modify-write through PutEngineModel: the row carries a licence acceptance, a
// source and a sha256 that nothing else in this request knows, and a round trip would have to
// carry them back out and in again to change one word.
// SetEngineModelParams writes the same one column, and is targeted for the same reason as the
// base model next door: the row carries a licence acceptance and a sha256 that a request
// changing two numbers knows nothing about.
func (s *SQL) SetEngineModelParams(ctx context.Context, role, id string, p *EngineParams) (bool, error) {
	raw := ""
	if p != nil && *p != (EngineParams{}) {
		b, err := json.Marshal(p)
		if err != nil {
			return false, err
		}
		raw = string(b)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE engine_models SET params=?, updated_at=? WHERE role=? AND id=?`,
		raw, NowTS(), role, id)
	return affected(res, err)
}

func (s *SQL) SetEngineModelBaseModel(ctx context.Context, role, id, baseModel string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE engine_models SET base_model=?, updated_at=? WHERE role=? AND id=?`,
		baseModel, NowTS(), role, id)
	return affected(res, err)
}

// SetEngineModelWindow corrects the declared window, and writes BOTH columns because they are
// one declaration: the catalogue only carries max_output_tokens when context_tokens is above
// zero, so a row that moved one of them alone is a row the panel cannot explain.
//
// It exists because the window is what a wrong row costs money to discover: measured on a
// borrowed llm engine, a 17 GB model whose context_tokens still said 262144 allocated a 16 GiB
// KV cache and died with `cudaMalloc failed: out of memory` — four minutes after a GPU box had
// been bought. Re-registering the row to change one number would carry its licence acceptance,
// its source and its sha256 back out and in again.
func (s *SQL) SetEngineModelWindow(ctx context.Context, role, id string, contextTokens, maxOutputTokens int) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE engine_models SET context_tokens=?, max_output_tokens=?, updated_at=? WHERE role=? AND id=?`,
		contextTokens, maxOutputTokens, NowTS(), role, id)
	return affected(res, err)
}

// SetEngineModelVram writes the operator's own VRAM measurement, and 0 withdraws it — which
// puts the row back on the floor derived from its files rather than on a number nobody stands
// behind any more.
func (s *SQL) SetEngineModelVram(ctx context.Context, role, id string, vramMiB int) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE engine_models SET vram_mib=?, updated_at=? WHERE role=? AND id=?`,
		vramMiB, NowTS(), role, id)
	return affected(res, err)
}

// SetEngineModelNegativePrompt corrects ONE column, for the same reason SetEngineModelBaseModel
// does: it is a field an administrator tunes after watching what the checkpoint actually draws,
// and a read-modify-write through PutEngineModel would carry a licence acceptance, a source and
// a sha256 back out and in again to change one sentence.
func (s *SQL) SetEngineModelNegativePrompt(ctx context.Context, role, id, negative string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE engine_models SET negative_prompt=?, updated_at=? WHERE role=? AND id=?`,
		negative, NowTS(), role, id)
	return affected(res, err)
}

// SetEngineModelTrainedWords replaces the words an adapter answers to, one targeted column like
// the negative prompt above and for the same reason: the row carries a licence acceptance and a
// sha256 that a request correcting a trigger word knows nothing about.
//
// An empty list is written as `[]` rather than refused — "this adapter has no trigger" is a real
// answer, and it is the only way back from a word Civitai published that turns out to be wrong.
func (s *SQL) SetEngineModelTrainedWords(ctx context.Context, role, id string, words []string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE engine_models SET trained_words=?, updated_at=? WHERE role=? AND id=?`,
		jsonList(words), NowTS(), role, id)
	return affected(res, err)
}

// SetEngineModelDisplay writes what the publisher calls this model and where its example image
// is (ADR 0088), one targeted column group like the two above and for the same reason: the row
// carries a licence acceptance and a sha256 that a re-read of a model page knows nothing about.
//
// All four are written together, empty included. They are one snapshot of one upstream document
// — keeping an old picture beside a new name would show a model that is half of each — and
// "the page no longer publishes an image" is an answer this has to be able to record.
func (s *SQL) SetEngineModelDisplay(ctx context.Context, role, id string, d EngineModelDisplay) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE engine_models SET display_name=?, version_name=?, preview_url=?, thumb_url=?,
		 updated_at=? WHERE role=? AND id=?`,
		d.DisplayName, d.VersionName, d.PreviewURL, d.ThumbURL, NowTS(), role, id)
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
