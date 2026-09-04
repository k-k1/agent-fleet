package store

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestSchemaDialectParity measures that the two migration series land on the same schema.
//
// `migrations/` and `migrations-pg/` are written by hand, separately, so adding to one and
// forgetting the other goes unnoticed: `memo_category` reached `migrations/0020` and was
// never mirrored to Postgres, and on ECS/RDS every category API returned 500 (relation
// does not exist). The Console folds a non-array response into an empty list, so the
// symptom was "no categories appear" rather than an error — nothing anyone could report
// as an outage. The cascade that physically deletes a removed member (docs/log/61 §61.18)
// names tables directly, so the same divergence would mean a 500 in the middle of an
// irreversible operation.
//
// Skipped without AF_TEST_DATABASE_URL, like the other Postgres tests.
func TestSchemaDialectParity(t *testing.T) {
	url := os.Getenv("AF_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set AF_TEST_DATABASE_URL to compare the two migration series")
	}
	ctx := context.Background()

	lite, err := OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer lite.Close()
	if err := lite.Migrate(ctx); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	pg, err := OpenPostgres(url)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	defer pg.Close()
	if _, err := pg.db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatalf("reset schema: %v", err)
	}
	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("migrate postgres: %v", err)
	}

	liteCols := sqliteSchema(t, ctx, lite)
	pgCols := postgresSchema(t, ctx, pg)

	// Types are deliberately not compared: SQLite only keeps the declared type and is
	// loosely typed in practice, so comparing its strings against Postgres TEXT/INTEGER
	// can say nothing but "different". What matters here is "present on one side only",
	// which is the way this actually broke.
	for _, d := range append(missing(liteCols, pgCols, "postgres"), missing(pgCols, liteCols, "sqlite")...) {
		t.Error(d)
	}
}

func missing(have, want map[string]map[string]bool, absentFrom string) []string {
	var out []string
	for table, cols := range have {
		other, ok := want[table]
		if !ok {
			out = append(out, "table "+table+" is missing from "+absentFrom+
				" — a migration was added to one series and not the other")
			continue
		}
		var miss []string
		for c := range cols {
			if !other[c] {
				miss = append(miss, c)
			}
		}
		if len(miss) > 0 {
			sort.Strings(miss)
			out = append(out, "table "+table+" is missing column(s) ["+strings.Join(miss, ", ")+"] in "+absentFrom)
		}
	}
	sort.Strings(out)
	return out
}

func sqliteSchema(t *testing.T, ctx context.Context, s *SQL) map[string]map[string]bool {
	t.Helper()
	rows, err := s.db.QueryContext(ctx,
		`SELECT m.name, p.name FROM sqlite_master m JOIN pragma_table_info(m.name) p
		 WHERE m.type='table' AND m.name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatalf("read sqlite schema: %v", err)
	}
	return scanSchema(t, rows)
}

func postgresSchema(t *testing.T, ctx context.Context, s *SQL) map[string]map[string]bool {
	t.Helper()
	rows, err := s.db.QueryContext(ctx,
		`SELECT table_name, column_name FROM information_schema.columns WHERE table_schema='public'`)
	if err != nil {
		t.Fatalf("read postgres schema: %v", err)
	}
	return scanSchema(t, rows)
}

func scanSchema(t *testing.T, rows interface {
	Next() bool
	Scan(...any) error
	Close() error
	Err() error
},
) map[string]map[string]bool {
	t.Helper()
	defer rows.Close()
	out := map[string]map[string]bool{}
	for rows.Next() {
		var table, col string
		if err := rows.Scan(&table, &col); err != nil {
			t.Fatalf("scan schema row: %v", err)
		}
		if out[table] == nil {
			out[table] = map[string]bool{}
		}
		out[table][col] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("schema rows: %v", err)
	}
	return out
}
