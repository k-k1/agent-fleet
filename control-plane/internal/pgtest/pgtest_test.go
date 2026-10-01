package pgtest

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func TestWithSearchPath(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"postgres://u:p@/db?host=/run/pg&sslmode=disable", `postgres://u:p@/db?host=%2Frun%2Fpg&search_path=%22aft_1_ab%22&sslmode=disable`},
		{"postgres://u@localhost:5432/db", `postgres://u@localhost:5432/db?search_path=%22aft_1_ab%22`},
		{"host=/run/pg dbname=db", `host=/run/pg dbname=db search_path='"aft_1_ab"'`},
	} {
		got, err := withSearchPath(c.in, `"aft_1_ab"`)
		if err != nil || got != c.want {
			t.Errorf("withSearchPath(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	// A search_path in the configured URL would be silently overridden or doubled.
	for _, in := range []string{"postgres://u@/db?search_path=x", "host=/run/pg search_path=x"} {
		if _, err := withSearchPath(in, `"s"`); err == nil {
			t.Errorf("withSearchPath(%q) accepted a URL that already sets search_path", in)
		}
	}
}

// Two schemas from one database do not see each other's tables, every pooled connection
// lands in its own schema, and the schema is gone once the test that made it ends.
func TestSchemaIsolatesAndCleansUp(t *testing.T) {
	if URL() == "" {
		t.Skip("set AF_TEST_DATABASE_URL to exercise the Postgres test harness")
	}
	ctx := context.Background()
	var names []string
	t.Run("pair", func(t *testing.T) {
		open := func() (*sql.DB, string) {
			dsn, ok := Schema(t)
			if !ok {
				t.Fatal("Schema reported no database although URL is set")
			}
			db, err := sql.Open("pgx", dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			// More connections than one, so a search_path set only on the first would show.
			db.SetMaxIdleConns(0)
			var name string
			if err := db.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&name); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(name, "aft_") {
				t.Fatalf("current_schema() = %q, want the harness's schema", name)
			}
			return db, name
		}
		a, an := open()
		b, bn := open()
		names = []string{an, bn}
		if an == bn {
			t.Fatalf("both calls got schema %q", an)
		}
		if _, err := a.ExecContext(ctx, `CREATE TABLE tenant(id TEXT)`); err != nil {
			t.Fatal(err)
		}
		if _, err := b.ExecContext(ctx, `SELECT 1 FROM tenant`); err == nil {
			t.Fatal("the second schema sees the first schema's table")
		}
		for range 3 {
			var got string
			if err := a.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&got); err != nil || got != an {
				t.Fatalf("a fresh connection landed in %q (%v), want %q", got, err, an)
			}
		}
	})
	admin, err := sql.Open("pgx", URL())
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	for _, n := range names {
		var left int
		if err := admin.QueryRowContext(ctx, `SELECT count(*) FROM pg_namespace WHERE nspname=$1`, n).Scan(&left); err != nil {
			t.Fatal(err)
		}
		if left != 0 {
			t.Errorf("schema %s survived its test", n)
		}
	}
}
