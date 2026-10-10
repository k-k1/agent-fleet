package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// sealedSchemaExempt lists the columns that look sealed by name and are not. Each needs a
// reason: an entry here is a column the rewrap command never touches.
var sealedSchemaExempt = map[string]string{
	"tenant.key_ref": "the tenant's own key reference, not a value sealed under one",
}

// TestSealedColumnsCoverSchema fails when the migrated schema holds a column that looks like a
// custodian-sealed value (key_ref, *_enc, ciphertext) and sealedColumns does not list it. The
// rewrap command walks sealedColumns only, so such a column would keep master-key-only
// protection after an operator ran it and was told nothing was left.
func TestSealedColumnsCoverSchema(t *testing.T) {
	ctx := context.Background()
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, c := range sealedColumns {
		listed[c.Table+"."+c.Value] = true
		listed[c.Table+"."+c.KeyRef] = true
	}
	rows, err := st.DB().QueryContext(ctx,
		`SELECT m.name, p.name FROM sqlite_master m, pragma_table_info(m.name) p WHERE m.type='table'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var table, col string
		if err := rows.Scan(&table, &col); err != nil {
			t.Fatal(err)
		}
		if col != "key_ref" && col != "ciphertext" && !strings.HasSuffix(col, "_enc") {
			continue
		}
		seen++
		k := table + "." + col
		if listed[k] {
			delete(listed, k)
			continue
		}
		if _, ok := sealedSchemaExempt[k]; ok {
			continue
		}
		t.Errorf("%s looks like a custodian-sealed column but is not in sealedColumns (store_sealed.go); add it there so rewrap-keys re-seals it, or to sealedSchemaExempt with the reason it is not sealed", k)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	// Proof the scan saw the schema at all: an empty pragma join would pass silently.
	if seen < len(sealedColumns)*2 {
		t.Fatalf("scan matched %d candidate columns, want at least %d", seen, len(sealedColumns)*2)
	}
	for k := range listed {
		t.Errorf("sealedColumns lists %s, which the migrated schema does not have", k)
	}
}

func TestSealedValueSwap(t *testing.T) {
	ctx := context.Background()
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx,
		`INSERT INTO mcp_server(id, tenant_id, name, headers_enc, key_ref, created_at, updated_at) VALUES('m1','t1','a','old','t1','x','x'), ('m2','t1','b','','','x','x')`); err != nil {
		t.Fatal(err)
	}
	ids, err := st.ListSealedIDs(ctx, "mcp_server")
	if err != nil || len(ids) != 1 || ids[0] != "m1" {
		t.Fatalf("ListSealedIDs = %v, %v; want [m1]", ids, err)
	}
	v, ok, err := st.GetSealedValue(ctx, "mcp_server", "m1")
	if err != nil || !ok || v != (SealedValue{Value: "old", KeyRef: "t1"}) {
		t.Fatalf("GetSealedValue = %+v, %v, %v", v, ok, err)
	}
	// A row that changed since the read is left alone.
	if ok, err := st.SwapSealedValue(ctx, "mcp_server", "m1", SealedValue{Value: "other", KeyRef: "t1"}, "new"); err != nil || ok {
		t.Fatalf("swap against a stale value = %v, %v; want false", ok, err)
	}
	if ok, err := st.SwapSealedValue(ctx, "mcp_server", "m1", v, "new"); err != nil || !ok {
		t.Fatalf("swap = %v, %v; want true", ok, err)
	}
	if v, _, _ := st.GetSealedValue(ctx, "mcp_server", "m1"); v.Value != "new" {
		t.Fatalf("after swap value = %q", v.Value)
	}
	if _, err := st.ListSealedIDs(ctx, "workspace"); err == nil {
		t.Fatal("a table outside sealedColumns was accepted")
	}

	if err := st.SetSetting(ctx, "k", "a"); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.SwapSetting(ctx, "k", "b", "c"); err != nil || ok {
		t.Fatalf("SwapSetting against a stale value = %v, %v", ok, err)
	}
	if ok, err := st.SwapSetting(ctx, "k", "a", "c"); err != nil || !ok {
		t.Fatalf("SwapSetting = %v, %v", ok, err)
	}
	if got, _ := st.GetSetting(ctx, "k"); got != "c" {
		t.Fatalf("setting = %q, want c", got)
	}
}
