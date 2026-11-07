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
// deployment that had no master key), ID the primary key. Name is how the rewrap command and
// these accessors address it: the table, unless the table seals a second value, which needs a
// name of its own or every lookup would find the first column.
type SealedColumn struct {
	Name, Table, ID, Value, KeyRef string
	// Plain says how the column is stored when the writer had no master key, and which key
	// reference sealing it later uses. The zero value means the column never holds
	// plaintext (the writer refuses without a key), so the seal-plaintext pass skips it.
	Plain PlainSeal
}

// PlainSeal describes a column's keyless storage. Encoding is "raw" (the bytes as written)
// or "base64" (the writers of inbox / handoff / share bodies store base64 of the body).
// RefConst is the key reference of a deployment-wide value; otherwise RefQuery selects it
// for one row id (the tenant the writer would have sealed under).
type PlainSeal struct {
	Encoding, RefConst, RefQuery string
}

// Plain encodings.
const (
	PlainRaw    = "raw"
	PlainBase64 = "base64"
)

func tenantRef(table, id string) string {
	return `SELECT tenant_id FROM ` + table + ` WHERE ` + id + `=?`
}

func (c SealedColumn) name() string {
	if c.Name != "" {
		return c.Name
	}
	return c.Table
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
	{Table: "home_dek", ID: "membership_id", Value: "ciphertext", KeyRef: "key_ref"},
	{Table: "mcp_server", ID: "id", Value: "headers_enc", KeyRef: "key_ref", Plain: PlainSeal{Encoding: PlainRaw, RefQuery: tenantRef("mcp_server", "id")}},
	{Name: "mcp_server.oauth_secret_enc", Table: "mcp_server", ID: "id", Value: "oauth_secret_enc", KeyRef: "oauth_key_ref", Plain: PlainSeal{Encoding: PlainRaw, RefQuery: tenantRef("mcp_server", "id")}},
	{Table: "tenant_idp", ID: "id", Value: "secret_enc", KeyRef: "key_ref", Plain: PlainSeal{Encoding: PlainRaw, RefQuery: tenantRef("tenant_idp", "id")}},
	{Table: "tenant_git_oauth", ID: "id", Value: "secret_enc", KeyRef: "key_ref", Plain: PlainSeal{Encoding: PlainRaw, RefQuery: tenantRef("tenant_git_oauth", "id")}},
	{Table: "dev_idp_key", ID: "kid", Value: "private_enc", KeyRef: "key_ref", Plain: PlainSeal{Encoding: PlainRaw, RefConst: "deployment"}},
	{Table: "dev_idp_client", ID: "workspace_id", Value: "secret_enc", KeyRef: "key_ref", Plain: PlainSeal{Encoding: PlainRaw, RefQuery: `SELECT tenant_id FROM workspace WHERE id=?`}},
	{Table: "session_share_proposal", ID: "id", Value: "ciphertext", KeyRef: "key_ref", Plain: PlainSeal{Encoding: PlainBase64, RefQuery: tenantRef("session_share_proposal", "id")}},
	{Table: "session_handoff_offer", ID: "id", Value: "ciphertext", KeyRef: "key_ref", Plain: PlainSeal{Encoding: PlainBase64, RefQuery: tenantRef("session_handoff_offer", "id")}},
	{Table: "inbox_item", ID: "id", Value: "ciphertext", KeyRef: "key_ref", Plain: PlainSeal{Encoding: PlainBase64, RefQuery: tenantRef("inbox_item", "id")}},
	{Table: "inbox_attachment", ID: "id", Value: "ciphertext", KeyRef: "key_ref", Plain: PlainSeal{Encoding: PlainBase64, RefQuery: `SELECT tenant_id FROM inbox_item WHERE id=(SELECT item_id FROM inbox_attachment WHERE id=?)`}},
}

// SealedTables lists the tables of sealedColumns, in the order the command walks them.
func SealedTables() []string {
	out := make([]string, len(sealedColumns))
	for i, c := range sealedColumns {
		out[i] = c.name()
	}
	return out
}

func sealedColumn(table string) (SealedColumn, error) {
	for _, c := range sealedColumns {
		if c.name() == table {
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

// PlainSource reports how table stores a value written without a master key; ok is false for
// a column that never holds one.
func PlainSource(table string) (encoding string, ok bool) {
	c, err := sealedColumn(table)
	if err != nil || c.Plain.Encoding == "" {
		return "", false
	}
	return c.Plain.Encoding, true
}

// CountPlaintextSealed counts the rows of table whose sealed column is not empty but whose
// key reference is: values stored unsealed. Zero for a column that never holds one.
func (s *SQL) CountPlaintextSealed(ctx context.Context, table string) (int, error) {
	c, err := sealedColumn(table)
	if err != nil || c.Plain.Encoding == "" {
		return 0, err
	}
	var n int
	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM `+c.Table+` WHERE `+c.Value+` <> '' AND `+c.KeyRef+` = ''`).Scan(&n)
	return n, err
}

// PlaintextKeyRef is the key reference the writer would have sealed row id of table under
// (its tenant, or the deployment-wide reference). It fails when the row has none, so a
// value is never sealed under an empty or guessed reference.
func (s *SQL) PlaintextKeyRef(ctx context.Context, table, id string) (string, error) {
	c, err := sealedColumn(table)
	if err != nil {
		return "", err
	}
	if c.Plain.RefConst != "" {
		return c.Plain.RefConst, nil
	}
	if c.Plain.RefQuery == "" {
		return "", fmt.Errorf("store: %q holds no plaintext values", table)
	}
	var ref string
	if err := s.db.QueryRowContext(ctx, c.Plain.RefQuery, id).Scan(&ref); err != nil {
		return "", err
	}
	if ref == "" {
		return "", fmt.Errorf("store: no key reference for %s %s", table, id)
	}
	return ref, nil
}

// SealPlaintextValue writes sealed and keyRef into a row that still holds old unsealed (the
// value as read, with an empty key reference). One statement, so a row is never sealed
// without its reference; false means the row changed or vanished and is left to the newer
// value.
func (s *SQL) SealPlaintextValue(ctx context.Context, table, id, oldValue, sealed, keyRef string) (bool, error) {
	c, err := sealedColumn(table)
	if err != nil {
		return false, err
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE `+c.Table+` SET `+c.Value+`=?, `+c.KeyRef+`=? WHERE `+c.ID+`=? AND `+c.Value+`=? AND `+c.KeyRef+`=''`,
		sealed, keyRef, id, oldValue)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// SwapSettingPair is SwapSetting for a value whose key reference is a second setting (the
// engine tokens): both are written in one transaction, only while the value still is old
// and the reference setting is empty or absent.
func (s *SQL) SwapSettingPair(ctx context.Context, valueKey, refKey, old, value, ref string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE deployment_setting SET value=? WHERE key=? AND value=?`, value, valueKey, old)
	if err != nil {
		return false, err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM deployment_setting WHERE key=?`, refKey); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO deployment_setting(key, value) VALUES(?, ?)`, refKey, ref); err != nil {
		return false, err
	}
	return true, tx.Commit()
}
