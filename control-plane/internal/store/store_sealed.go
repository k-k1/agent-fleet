package store

// Raw access to the columns that hold a value sealed by the key custodian, for the one-shot
// `rewrap-keys` command (ADR 0005 addendum 2026-10-10). Every other reader goes through the
// typed methods; this surface exists so one loop can walk every sealed column without a
// method per table.

import (
	"context"
	"database/sql"
	"fmt"
)

// SealedColumn names a table column holding a custodian-sealed value: Value is the sealed
// text, KeyRef the custodian key reference it was sealed under ("" = stored unsealed, a
// deployment that had no master key), ID the primary key.
type SealedColumn struct {
	Table, ID, Value, KeyRef string
}

// sealedColumns is every table column the custodian seals into. TestSealedColumnsCoverSchema
// fails when a migration adds a key_ref / *_enc / ciphertext column that is not listed here,
// and the rewrap command walks exactly this list, so a column missing here is a column whose
// rows keep master-key-only protection after the rewrap. Settings rows are not here: their key
// reference lives in another row or inside a JSON value, so the command handles them itself.
//
// These names are interpolated into SQL; they come from this literal only, never from input.
var sealedColumns = []SealedColumn{
	{Table: "wrapped_dek", ID: "workspace_id", Value: "ciphertext", KeyRef: "key_ref"},
	{Table: "mcp_server", ID: "id", Value: "headers_enc", KeyRef: "key_ref"},
	{Table: "tenant_idp", ID: "id", Value: "secret_enc", KeyRef: "key_ref"},
	{Table: "tenant_git_oauth", ID: "id", Value: "secret_enc", KeyRef: "key_ref"},
	{Table: "session_share_proposal", ID: "id", Value: "ciphertext", KeyRef: "key_ref"},
	{Table: "session_handoff_offer", ID: "id", Value: "ciphertext", KeyRef: "key_ref"},
}

// SealedTables lists the tables of sealedColumns, in the order the command walks them.
func SealedTables() []string {
	out := make([]string, len(sealedColumns))
	for i, c := range sealedColumns {
		out[i] = c.Table
	}
	return out
}

func sealedColumn(table string) (SealedColumn, error) {
	for _, c := range sealedColumns {
		if c.Table == table {
			return c, nil
		}
	}
	return SealedColumn{}, fmt.Errorf("store: %q is not a sealed-value table", table)
}

// SealedValue is one row's sealed column and the key reference stored beside it.
type SealedValue struct {
	Value, KeyRef string
}

// ListSealedIDs returns the ids of the rows of table whose sealed column is not empty. Ids
// only, so a walk over large handoff bodies holds one body at a time.
func (s *SQL) ListSealedIDs(ctx context.Context, table string) ([]string, error) {
	c, err := sealedColumn(table)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+c.ID+` FROM `+c.Table+` WHERE `+c.Value+` <> '' ORDER BY `+c.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// GetSealedValue reads one row's sealed column and key reference; ok is false when the row
// is gone.
func (s *SQL) GetSealedValue(ctx context.Context, table, id string) (SealedValue, bool, error) {
	c, err := sealedColumn(table)
	if err != nil {
		return SealedValue{}, false, err
	}
	var v SealedValue
	err = s.db.QueryRowContext(ctx,
		`SELECT `+c.Value+`, `+c.KeyRef+` FROM `+c.Table+` WHERE `+c.ID+`=?`, id).Scan(&v.Value, &v.KeyRef)
	if err == sql.ErrNoRows {
		return SealedValue{}, false, nil
	}
	if err != nil {
		return SealedValue{}, false, err
	}
	return v, true, nil
}

// SwapSealedValue replaces one row's sealed value with sealed, only while the row still holds
// old (value and key reference). It reports false when the row changed or vanished in the
// meantime: the Control Plane may be running, and a value it wrote after the read is newer
// than ours and must not be overwritten. One statement, so the row is never half-written.
func (s *SQL) SwapSealedValue(ctx context.Context, table, id string, old SealedValue, sealed string) (bool, error) {
	c, err := sealedColumn(table)
	if err != nil {
		return false, err
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE `+c.Table+` SET `+c.Value+`=? WHERE `+c.ID+`=? AND `+c.Value+`=? AND `+c.KeyRef+`=?`,
		sealed, id, old.Value, old.KeyRef)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// SwapSetting is SwapSealedValue for a deployment_setting row: it writes value only while the
// row still holds old.
func (s *SQL) SwapSetting(ctx context.Context, key, old, value string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE deployment_setting SET value=? WHERE key=? AND value=?`, value, key, old)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}
