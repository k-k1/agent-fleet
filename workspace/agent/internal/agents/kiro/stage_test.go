package kiro

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func makeDB(t *testing.T, home string, auth map[string]string, history int) {
	t.Helper()
	p := dataDB(home)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+p)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`CREATE TABLE auth_kv (key TEXT PRIMARY KEY, value TEXT)`,
		`CREATE TABLE history (id INTEGER PRIMARY KEY, command TEXT)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for k, v := range auth {
		if _, err := db.Exec(`INSERT INTO auth_kv VALUES(?, ?)`, k, v); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < history; i++ {
		if _, err := db.Exec(`INSERT INTO history(command) VALUES('ls')`); err != nil {
			t.Fatal(err)
		}
	}
}

func authOf(t *testing.T, home string) map[string]string {
	t.Helper()
	rows, err := readAuthRows(dataDB(home))
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	for _, r := range rows {
		m[r.key] = r.value
	}
	return m
}

// Only the login moves: the live auth_kv rows become the staged ones and the rest of the
// database (history, conversations) stays.
func TestInstallLoginReplacesOnlyTheAuthRows(t *testing.T) {
	stage, home := t.TempDir(), t.TempDir()
	makeDB(t, stage, map[string]string{"kirocli:odic:token": "new", "kirocli:odic:device-registration": "reg-new"}, 0)
	makeDB(t, home, map[string]string{"kirocli:odic:token": "old", "kirocli:old-only": "x"}, 3)

	if err := installLogin(stage, home); err != nil {
		t.Fatal(err)
	}
	got := authOf(t, home)
	if len(got) != 2 || got["kirocli:odic:token"] != "new" || got["kirocli:odic:device-registration"] != "reg-new" {
		t.Fatalf("auth_kv = %v", got)
	}
	db, _ := sql.Open("sqlite", "file:"+dataDB(home))
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM history`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("history rows = %d, %v", n, err)
	}
}

// A first sign-in has no live database to keep; the staged one is installed whole.
func TestInstallLoginFirstSignIn(t *testing.T) {
	stage, home := t.TempDir(), t.TempDir()
	makeDB(t, stage, map[string]string{"kirocli:odic:token": "new"}, 0)
	if err := installLogin(stage, home); err != nil {
		t.Fatal(err)
	}
	if got := authOf(t, home); got["kirocli:odic:token"] != "new" {
		t.Fatalf("auth_kv = %v", got)
	}
}

// A staged database without login rows is not a sign-in, and the live login stays.
func TestInstallLoginWithoutStagedRows(t *testing.T) {
	stage, home := t.TempDir(), t.TempDir()
	makeDB(t, stage, nil, 0)
	makeDB(t, home, map[string]string{"kirocli:odic:token": "old"}, 0)
	if err := installLogin(stage, home); err == nil {
		t.Fatal("installed a sign-in with no auth rows")
	}
	if got := authOf(t, home); got["kirocli:odic:token"] != "old" {
		t.Fatalf("live login touched: %v", got)
	}
}
