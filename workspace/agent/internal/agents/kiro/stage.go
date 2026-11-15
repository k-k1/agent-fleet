package kiro

import (
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (registers "sqlite")
)

// What a kiro sign-in writes that matters (measured on a real sign-in under a scratch HOME): two
// rows of data.sqlite3's auth_kv table, kirocli:odic:device-registration and kirocli:odic:token.
// Copied alone into another home's database they make `whoami` succeed. The same file holds the
// member's history and conversations, so the file is never replaced, only that table.

// dataDB is kiro's database under home.
func dataDB(home string) string {
	return filepath.Join(home, ".local", "share", "kiro-cli", "data.sqlite3")
}

// signedInAt reports a finished sign-in under the staging home. A var for tests.
var signedInAt = func(home string) bool { return whoamiOK(home) }

// installLogin replaces home's auth_kv rows with the staged ones in one transaction, so kiro
// reads the old login or the new one, never a mix. With no database in home yet (a first
// sign-in) there is nothing to keep, and the staged database is installed whole.
//
// Both paths go through SQLite, never a file copy: kiro may leave its last commit in a -wal file
// beside the database, and copying the main file alone would drop it.
func installLogin(stage, home string) error {
	src, dst := dataDB(stage), dataDB(home)
	rows, err := readAuthRows(src)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return errors.New("the sign-in completed but kiro stored no auth_kv rows")
	}
	if _, err := os.Stat(dst); errors.Is(err, fs.ErrNotExist) {
		return copyDatabase(src, dst)
	}
	// IMMEDIATE takes the write lock up front, so a running kiro holding the database is
	// waited for (busy_timeout) rather than met mid-transaction.
	db, err := sql.Open("sqlite", "file:"+dst+"?_pragma=busy_timeout(5000)&_txlock=immediate")
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() // no-op after Commit
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS auth_kv (key TEXT PRIMARY KEY, value TEXT)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM auth_kv`); err != nil {
		return err
	}
	for _, r := range rows {
		if _, err := tx.Exec(`INSERT INTO auth_kv(key, value) VALUES(?, ?)`, r.key, r.value); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// copyDatabase writes a consistent copy of src (its -wal content included) to dst with VACUUM
// INTO, through a temporary file renamed into place.
func copyDatabase(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	tmp := dst + ".af-install"
	_ = os.Remove(tmp)
	defer os.Remove(tmp) // no-op once renamed
	db, err := sql.Open("sqlite", "file:"+src+"?_pragma=busy_timeout(3000)")
	if err != nil {
		return err
	}
	_, err = db.Exec(`VACUUM INTO ?`, tmp)
	db.Close()
	if err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

type authRow struct{ key, value string }

func readAuthRows(path string) ([]authRow, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(3000)")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	q, err := db.Query(`SELECT key, value FROM auth_kv`)
	if err != nil {
		return nil, err
	}
	defer q.Close()
	var out []authRow
	for q.Next() {
		var r authRow
		if err := q.Scan(&r.key, &r.value); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, q.Err()
}
