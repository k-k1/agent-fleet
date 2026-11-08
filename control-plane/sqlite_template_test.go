package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// sqliteTemplate holds the bytes of one fully migrated, empty SQLite database, built
// once per test binary. Applying the 120-odd migrations is by far the most expensive step
// of a typical test here (measured: 1–3 s per store on a loaded host, against ~1 ms for
// copying the finished file), and hundreds of tests do it.
var (
	sqliteTemplateOnce sync.Once
	sqliteTemplateDir  string
	sqliteTemplateDB   []byte
	sqliteTemplateErr  error
)

// openMigratedSQLite is store.OpenSQLite for a database that already carries the current
// schema: it drops a copy of the migrated template at path and opens that. The caller's own
// Migrate call afterwards is a cheap no-op, so call sites keep their shape.
//
// Use it only for a database a test wants fresh and fully migrated. A test that exercises
// Migrate itself, or starts from an older schema, must keep calling store.OpenSQLite.
func openMigratedSQLite(path string) (*store.SQL, error) {
	sqliteTemplateOnce.Do(buildSQLiteTemplate)
	if sqliteTemplateErr != nil {
		return nil, sqliteTemplateErr
	}
	if err := os.WriteFile(path, sqliteTemplateDB, 0o600); err != nil {
		return nil, err
	}
	return store.OpenSQLite(path)
}

func buildSQLiteTemplate() {
	dir, err := os.MkdirTemp("", "sqlite-template-*")
	if err != nil {
		sqliteTemplateErr = err
		return
	}
	sqliteTemplateDir = dir
	path := filepath.Join(dir, "template.db")
	st, err := store.OpenSQLite(path)
	if err != nil {
		sqliteTemplateErr = err
		return
	}
	if err := st.Migrate(context.Background()); err != nil {
		st.Close()
		sqliteTemplateErr = fmt.Errorf("migrate template: %w", err)
		return
	}
	// Closing the last connection checkpoints the WAL into the main file, so the main
	// file alone is the whole database; the -wal file is not copied.
	if err := st.Close(); err != nil {
		sqliteTemplateErr = err
		return
	}
	sqliteTemplateDB, sqliteTemplateErr = os.ReadFile(path)
}

// sqliteTemplateCleanup removes the template directory; called from TestMain because a
// t.TempDir() would vanish with the first test that triggered the build.
func sqliteTemplateCleanup() {
	if sqliteTemplateDir != "" {
		_ = os.RemoveAll(sqliteTemplateDir)
	}
}
