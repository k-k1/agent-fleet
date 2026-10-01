// Package pgtest is the one way a test reaches the Postgres named by AF_TEST_DATABASE_URL.
//
// That database is persistent and shared: two sessions point at the same af-db database,
// and `go test ./...` runs packages in parallel. Resetting `public` there drops the tables
// out from under every other run, which surfaces as `relation "tenant" does not exist` in a
// test that has nothing wrong with it. So each caller of Schema gets a schema of its own,
// created here and dropped when the test ends, and its connections see nothing else.
//
// It is imported from tests only and imports nothing from this module, so the store
// package's internal tests can use it without an import cycle.
package pgtest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" driver
)

// EnvVar names the database the Postgres tests run against; unset means they skip.
const EnvVar = "AF_TEST_DATABASE_URL"

// URL returns the configured database URL, or "" when the Postgres tests should skip.
// Only tests that need cluster-level objects (roles) and no tables should use it directly;
// anything that migrates or writes rows takes a Schema.
func URL() string { return os.Getenv(EnvVar) }

// Schema creates a fresh, uniquely named schema in the test database and returns a DSN
// whose every connection has search_path set to that schema alone, or ok=false when
// AF_TEST_DATABASE_URL is unset. The schema is dropped in a t.Cleanup, and a failed drop
// fails the test: a leftover schema on a persistent server is the leak this exists to stop.
//
// Close every pool opened on the DSN before the test ends (a defer, or a t.Cleanup
// registered after this call — cleanups run last-registered first). The drop itself runs on
// a pool owned here, which stays open until the drop has finished.
//
// `public` is deliberately left off the search_path: with it there, a table a migration
// forgot to create would resolve to another run's copy in public and the test would pass.
func Schema(t testing.TB) (dsn string, ok bool) {
	t.Helper()
	base := URL()
	if base == "" {
		return "", false
	}
	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatalf("pgtest: random schema suffix: %v", err)
	}
	// At most 6+10+1+12 = 29 bytes, well inside Postgres' 63-byte identifier limit, which
	// otherwise truncates silently and could make two names collide.
	name := fmt.Sprintf("aft_%d_%s", os.Getpid(), hex.EncodeToString(suffix[:]))
	quoted := pgx.Identifier{name}.Sanitize()

	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatalf("pgtest: open %s: %v", EnvVar, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := admin.ExecContext(ctx, `CREATE SCHEMA `+quoted); err != nil {
		admin.Close()
		t.Fatalf("pgtest: create schema %s: %v", name, err)
	}
	// One cleanup, so the order inside it is fixed: drop on the live pool, then close it.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, `DROP SCHEMA `+quoted+` CASCADE`); err != nil {
			t.Errorf("pgtest: cleanup: drop schema %s: %v", name, err)
		}
		if err := admin.Close(); err != nil {
			t.Errorf("pgtest: cleanup: close admin pool: %v", err)
		}
	})
	dsn, err = withSearchPath(base, quoted)
	if err != nil {
		t.Fatalf("pgtest: %v", err)
	}
	return dsn, true
}

// withSearchPath adds search_path to a URL or keyword/value DSN. pgx sends a parameter it
// does not recognise as a runtime parameter on every connection it opens, so this covers
// the whole pool, not just the first connection.
func withSearchPath(dsn, quotedSchema string) (string, error) {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return "", fmt.Errorf("parse %s: %w", EnvVar, err)
		}
		q := u.Query()
		if q.Has("search_path") {
			return "", fmt.Errorf("%s already sets search_path; drop it, the harness owns it", EnvVar)
		}
		q.Set("search_path", quotedSchema)
		u.RawQuery = q.Encode()
		return u.String(), nil
	}
	if strings.Contains(dsn, "search_path") {
		return "", fmt.Errorf("%s already sets search_path; drop it, the harness owns it", EnvVar)
	}
	// Keyword/value form: single-quote the value so the double quotes survive.
	return dsn + " search_path='" + quotedSchema + "'", nil
}
