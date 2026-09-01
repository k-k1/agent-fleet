package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestMigrationSeriesDeclareTheSameSchema は 2 つのマイグレーション系列を **SQL のまま
// 読んで**比べる。TestSchemaDialectParity と目的は同じだが、決定的に違うのは
// **Postgres サーバが要らない**こと。
//
// 🔥 なぜ要るか（2026-09-01 に本番で踏んだ）。パリティ検査は既にあったのに
// `AF_TEST_DATABASE_URL` が無ければ skip する作りで、CI に Postgres が無い以上
// **一度も走っていなかった**。走らない検査は無いのと同じで、その隙間に
// `workspace.settings`（`migrations/0009`）が **Postgres 側へ写されないまま**残った。
//
// 症状の出方が最悪だった: 読み出しは `GetWorkspaceSettings` のエラーを握りつぶすので
// **全部の設定が既定値に見え**、書き込みだけが 500。つまり Postgres のデプロイでは
// 「ワークスペース設定を変えても保存できない」が、**画面上はいつも既定値**という形で
// 何週間も残っていた（プレビューの設定を触ろうとして初めて表に出た）。
//
// ★ 型は比べない。SQLite の宣言型と Postgres の型は文字列として一致しないし、守りたい
// のは「片方にしか無い」——実際に起きた壊れ方はそれだけである。
func TestMigrationSeriesDeclareTheSameSchema(t *testing.T) {
	lite := declaredSchema(t, "migrations")
	pg := declaredSchema(t, "migrations-pg")
	if len(lite) == 0 || len(pg) == 0 {
		t.Fatalf("parsed nothing: sqlite=%d tables, postgres=%d tables", len(lite), len(pg))
	}
	for _, d := range append(missing(lite, pg, "postgres"), missing(pg, lite, "sqlite")...) {
		if reason, ok := declaredSchemaExempt[exemptKeyOf(d)]; ok {
			t.Logf("known difference (%s): %s", reason, d)
			continue
		}
		t.Error(d)
	}
}

// declaredSchemaExempt lists the differences that are REAL on paper and correct in
// practice, because the SQLite series finishes its 0002 rebuild in Go rather than in
// SQL (store_sqlite.go: migrateMemberships + legacyHook). Each entry needs a reason —
// an exemption without one is how a genuine miss gets waved through.
//
// ⚠️ 新しい行を足す前に「本当に Go 側で埋まるのか」を確かめること。ここに書けば検査は
// 黙るので、**この表が検査そのものより弱い場所**になる。
var declaredSchemaExempt = map[string]string{
	"app_user":                "sqlite だけに残る membership 導入前の表。migrateMemberships が identity/membership へ畳んで捨てる",
	"workspace_new":           "0002 が作る過渡的な表。legacyHook が DROP workspace → RENAME workspace_new TO workspace で入れ替える",
	"workspace.user_id":       "同上の入れ替えで消える旧列（membership_id に置き換わる）",
	"workspace.membership_id": "紙の上では workspace_new 側に居るので sqlite の workspace に見えないだけ。入れ替え後は存在する",
}

// exemptKeyOf turns one diff line back into the thing it is about ("app_user" /
// "workspace.settings"), so the exemption table stays readable.
func exemptKeyOf(diff string) string {
	f := strings.Fields(diff)
	if len(f) < 2 || f[0] != "table" {
		return diff
	}
	// ★ 「表ごと無い」と「列が足りない」はどちらも "table X is …" で始まる。
	// column(s) の有無で分けないと、列の差分が**表ごとの免除に化ける**。
	if !strings.Contains(diff, "column(s)") {
		return f[1]
	}
	table := f[1]
	open, close := strings.Index(diff, "["), strings.Index(diff, "]")
	if open < 0 || close < open {
		return diff
	}
	cols := strings.Fields(strings.ReplaceAll(diff[open+1:close], ",", " "))
	if len(cols) != 1 {
		return diff // 複数まとめては免除しない（1 つずつ理由を書かせる）
	}
	return table + "." + cols[0]
}

var (
	reCreateTable = regexp.MustCompile(`(?is)^\s*CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([a-z0-9_"]+)\s*\((.*)\)\s*$`)
	reAlterAdd    = regexp.MustCompile(`(?is)^\s*ALTER\s+TABLE\s+([a-z0-9_"]+)\s+ADD\s+COLUMN\s+(?:IF\s+NOT\s+EXISTS\s+)?([a-z0-9_"]+)`)
	reDropTable   = regexp.MustCompile(`(?is)^\s*DROP\s+TABLE\s+(?:IF\s+EXISTS\s+)?([a-z0-9_"]+)`)
	reComment     = regexp.MustCompile(`(?m)--.*$`)
)

// declaredSchema replays one migration directory on paper: table -> column set.
//
// ⚠️ 実際に流して比べる TestSchemaDialectParity と違い、こちらは**書いてあるとおり**を
// 読む。Go 側の legacyHook（sqlite で workspace を作り直すやつ）のような、SQL の外で
// 起きる細工は見えない —— そこは repairWorkspaceColumns と、その回帰テストの担当。
func declaredSchema(t *testing.T, dir string) map[string]map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files) // 番号順 = 適用順
	out := map[string]map[string]bool{}
	for _, name := range files {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s/%s: %v", dir, name, err)
		}
		for _, stmt := range strings.Split(reComment.ReplaceAllString(string(raw), ""), ";") {
			if strings.TrimSpace(stmt) == "" {
				continue
			}
			switch {
			case reCreateTable.MatchString(stmt):
				m := reCreateTable.FindStringSubmatch(stmt)
				table := ident(m[1])
				cols := out[table]
				if cols == nil {
					cols = map[string]bool{}
					out[table] = cols
				}
				for _, c := range columnsOf(m[2]) {
					cols[c] = true
				}
			case reAlterAdd.MatchString(stmt):
				m := reAlterAdd.FindStringSubmatch(stmt)
				table := ident(m[1])
				if out[table] == nil {
					out[table] = map[string]bool{}
				}
				out[table][ident(m[2])] = true
			case reDropTable.MatchString(stmt):
				delete(out, ident(reDropTable.FindStringSubmatch(stmt)[1]))
			}
		}
	}
	return out
}

// columnsOf splits a CREATE TABLE body at top-level commas and keeps the ones that
// name a column. テーブル制約（PRIMARY KEY (...) / UNIQUE (...) / CHECK (...) …）は
// 同じ形で並ぶので、先頭の語で落とす。
func columnsOf(body string) []string {
	var out []string
	depth, start := 0, 0
	items := []string{}
	for i, r := range body {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				items = append(items, body[start:i])
				start = i + 1
			}
		}
	}
	items = append(items, body[start:])
	for _, it := range items {
		fields := strings.Fields(it)
		if len(fields) == 0 {
			continue
		}
		switch strings.ToUpper(fields[0]) {
		case "PRIMARY", "UNIQUE", "FOREIGN", "CHECK", "CONSTRAINT", "EXCLUDE":
			continue
		}
		out = append(out, ident(fields[0]))
	}
	return out
}

func ident(s string) string { return strings.ToLower(strings.Trim(strings.TrimSpace(s), `"`)) }
