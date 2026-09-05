package store

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestMigrationSeriesDeclareTheSameSchema compares the two migration series by reading the
// SQL as written. Same purpose as TestSchemaDialectParity, with one decisive difference: it
// needs no Postgres server.
//
// Why it is needed: the parity check already existed, but it skipped itself without
// `AF_TEST_DATABASE_URL`, and with no Postgres in CI it had never run once. A check that
// does not run is no check at all, and through that gap `workspace.settings`
// (`migrations/0009`) stayed uncopied to the Postgres side.
//
// The failure mode was the worst kind: reads swallow the error from `GetWorkspaceSettings`,
// so every setting looked like its default and only writes were 500. On a Postgres
// deployment "workspace settings cannot be saved" therefore survived for weeks behind a
// screen that always showed defaults.
//
// Types are not compared. A SQLite declared type and a Postgres type do not match as
// strings, and what needs protecting is "present on one side only" — the only way this has
// actually broken.
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
// Confirm that the Go side really does fill it in before adding a row. Anything written
// here silences the check, which makes this table the weakest point in it.
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
	// "the whole table is missing" and "a column is missing" both start with "table X is …".
	// Without splitting on the presence of column(s), a column diff turns into a whole-table
	// exemption.
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
		return diff // no bulk exemption: one reason per entry
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
// Unlike TestSchemaDialectParity, which actually runs them and compares, this reads exactly
// what is written. Sleight of hand outside the SQL — the Go-side legacyHook that rebuilds
// workspace on sqlite, say — is invisible here; that belongs to repairWorkspaceColumns and
// its own regression test.
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
	sort.Strings(files) // numeric order = apply order
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

// columnsOf splits a CREATE TABLE body at top-level commas and keeps the ones that name a
// column. Table constraints (PRIMARY KEY (...) / UNIQUE (...) / CHECK (...) …) line up in
// the same shape, so they are dropped by their leading word.
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
