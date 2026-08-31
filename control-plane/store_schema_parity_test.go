package main

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestSchemaDialectParity は 2 つのマイグレーション系列が**同じスキーマに着地する**ことを
// 実測で固定する。
//
// ★ なぜ要るか（2026-08-22 に踏んだ）。`migrations/` と `migrations-pg/` は別々に手で
// 書かれていて、**片方に足して片方を忘れても誰も気づかない**。`memo_category` は
// `migrations/0020` に入ったまま Postgres 側へ写されず、ECS/RDS のデプロイでは
// カテゴリの API が全部 500（relation does not exist）を返していた。しかも Console は
// 配列でない応答を空リストに畳むので、症状は「エラー」ではなく**「カテゴリが出ない」**
// ——誰も障害として報告しようがない形で残っていた。
//
// 見つかったきっかけは、除名済みメンバーの物理削除（docs/log/61 §61.18）で表名を直に並べる
// cascade を書いたことで、**取り消せない操作の途中で 500 を踏む**寸前だった。以後は
// このテストが差分を先に落とす。
//
// AF_TEST_DATABASE_URL が無ければ skip（他の Postgres テストと同じ作法）。
func TestSchemaDialectParity(t *testing.T) {
	url := os.Getenv("AF_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set AF_TEST_DATABASE_URL to compare the two migration series")
	}
	ctx := context.Background()

	lite, err := openSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer lite.Close()
	if err := lite.migrate(ctx); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	pg, err := openPostgres(url)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	defer pg.Close()
	if _, err := pg.db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatalf("reset schema: %v", err)
	}
	if err := pg.migrate(ctx); err != nil {
		t.Fatalf("migrate postgres: %v", err)
	}

	liteCols := sqliteSchema(t, ctx, lite)
	pgCols := postgresSchema(t, ctx, pg)

	// 型は比べない。SQLite は宣言型を保持するだけで実際には型付けが緩く、Postgres の
	// TEXT/INTEGER と文字列比較しても「違う」としか言えない。ここで守りたいのは
	// **「片方にしか無い」**——それが実際に起きた壊れ方である。
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

func sqliteSchema(t *testing.T, ctx context.Context, s *sqlStore) map[string]map[string]bool {
	t.Helper()
	rows, err := s.db.QueryContext(ctx,
		`SELECT m.name, p.name FROM sqlite_master m JOIN pragma_table_info(m.name) p
		 WHERE m.type='table' AND m.name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatalf("read sqlite schema: %v", err)
	}
	return scanSchema(t, rows)
}

func postgresSchema(t *testing.T, ctx context.Context, s *sqlStore) map[string]map[string]bool {
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
