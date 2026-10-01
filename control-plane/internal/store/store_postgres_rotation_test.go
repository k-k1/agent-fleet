package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/k-k1/agent-fleet/control-plane/internal/pgtest"
)

// TestPostgresPasswordRotation is the one that would have caught the 2026-09-01
// outage: it rotates a role's password out from under a live pool and asserts that
// queries keep working.
//
// ⚠️ It has to prove that password authentication is HAPPENING first. The
// workspace's embedded-Postgres harness (docs/build/10-development §10.4) is
// initdb'd with --auth=trust, and under trust a wrong password connects fine — so
// this test would pass without exercising a single line of the code it covers.
// That is the same failure the pg dialect parity work ran into: a check that does
// not run is not a check. Hence the probe below, which SKIPS loudly rather than
// going green for nothing.
//
// To actually run it against the workspace harness, make one line of pg_hba.conf
// demand a password before the trust line and reload:
//
//	D=$HOME/.local/share/af-pgtest
//	sed -i '1i local all /^af_rot_test_ scram-sha-256' "$D/data/pg_hba.conf"
//	"$D/dist/bin/pg_ctl" -D "$D/data" reload
//	AF_TEST_DATABASE_URL="postgres://postgres@/postgres?host=$D/sock&sslmode=disable" \
//	  go test -run TestPostgresPasswordRotation -v
func TestPostgresPasswordRotation(t *testing.T) {
	// The role is cluster-wide and the test creates no tables, so it takes the database
	// itself rather than a pgtest.Schema.
	adminURL := pgtest.URL()
	if adminURL == "" {
		t.Skip("set AF_TEST_DATABASE_URL to run the Postgres rotation test")
	}
	ctx := context.Background()

	admin, err := sql.Open("pgx", adminURL)
	if err != nil {
		t.Fatalf("open admin: %v", err)
	}
	// A Cleanup, not a defer: defers run before Cleanups, and the role is dropped
	// through this pool in a Cleanup registered below.
	t.Cleanup(func() { admin.Close() })
	if err := admin.PingContext(ctx); err != nil {
		t.Fatalf("ping admin: %v", err)
	}

	// Roles are cluster-wide, so a fixed name collides with an overlapping run and with
	// any role an interrupted run left behind on a persistent server (af-db).
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatalf("random role suffix: %v", err)
	}
	role := fmt.Sprintf("af_rot_test_%d_%s", os.Getpid(), hex.EncodeToString(suffix[:]))
	quotedRole := pgx.Identifier{role}.Sanitize()
	const pw1, pw2 = "rot-before-1", "rot-after-2"
	// GRANT ... ON DATABASE and DROP OWNED both rewrite the database's one pg_database
	// row, and two runs doing it at once fail with "tuple concurrently updated"
	// (measured with two overlapping runs on one af-db database). The advisory lock is
	// scoped to this database, which is exactly the row they contend for.
	aclLocked := func(c context.Context, q string) error {
		tx, err := admin.BeginTx(c, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(c, `SELECT pg_advisory_xact_lock(hashtext('af_rot_test_database_acl'))`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(c, q); err != nil {
			return err
		}
		return tx.Commit()
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := admin.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	var dbName string
	if err := admin.QueryRowContext(ctx, `SELECT current_database()`).Scan(&dbName); err != nil {
		t.Fatalf("current_database: %v", err)
	}
	exec(fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD '%s'", quotedRole, pw1))
	t.Cleanup(func() {
		c := context.Background()
		// DROP OWNED revokes the role's privileges in this database and on shared
		// objects, the database ACL included; DROP ROLE refuses while any remain and
		// names the database that still holds one, so neither error is swallowed.
		if err := aclLocked(c, `DROP OWNED BY `+quotedRole); err != nil {
			t.Errorf("cleanup: DROP OWNED BY %s: %v", role, err)
		}
		if _, err := admin.ExecContext(c, `DROP ROLE `+quotedRole); err != nil {
			// The dependency list is in the error's DETAIL, which err.Error() omits.
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Detail != "" {
				err = fmt.Errorf("%w; %s", err, pgErr.Detail)
			}
			t.Errorf("cleanup: DROP ROLE %s: %v", role, err)
		}
	})
	grant := fmt.Sprintf(`GRANT CONNECT ON DATABASE %s TO %s`, pgx.Identifier{dbName}.Sanitize(), quotedRole)
	if err := aclLocked(ctx, grant); err != nil {
		t.Fatalf("%s: %v", grant, err)
	}

	dsn := func(pw string) string {
		u, err := url.Parse(adminURL)
		if err != nil {
			t.Fatalf("parse AF_TEST_DATABASE_URL: %v", err)
		}
		u.User = url.UserPassword(role, pw)
		return u.String()
	}

	// The probe. A server that lets us in with garbage is not authenticating, and
	// everything below would be theatre.
	probe, err := sql.Open("pgx", dsn("definitely-not-the-password"))
	if err != nil {
		t.Fatalf("open probe: %v", err)
	}
	probeErr := probe.PingContext(ctx)
	probe.Close()
	if probeErr == nil {
		t.Skip("this server accepts any password for " + role + " (pg_hba trust) — " +
			"the rotation path cannot be exercised here; see the comment above")
	}
	if !isPgAuthFailure(probeErr) {
		t.Fatalf("probe failed for some reason other than the password: %v", probeErr)
	}

	// A stand-in for Secrets Manager whose answer we can change mid-test.
	secret := pw1
	src := newDBPasswordSource(testARN, "password")
	// The 5s default keeps a genuinely-wrong password from hammering the API; here
	// it would just make the test sleep through the window it is measuring.
	src.minGap = 20 * time.Millisecond
	fetches := 0
	src.fetch = func(_ context.Context, _, stage string) (string, error) {
		fetches++
		if stage == stagePending {
			return "", fmt.Errorf("ResourceNotFoundException: no AWSPENDING version") // the usual case
		}
		return secret, nil
	}

	st, err := openPostgresWith(dsn(pw1), src)
	if err != nil {
		t.Fatalf("open as %s: %v", role, err)
	}
	defer st.Close()
	// Every query must take a fresh physical connection, which is what makes the
	// rotation bite at all. A pool that never reconnects would sail through this.
	st.db.SetMaxIdleConns(0)

	var one int
	if err := st.db.QueryRowContext(ctx, `SELECT 1`).Scan(&one); err != nil || one != 1 {
		t.Fatalf("before rotation: %v", err)
	}
	if fetches != 0 {
		t.Errorf("the store was consulted %d times while the password was still good", fetches)
	}

	// --- rotate, exactly as Secrets Manager does: the database first, the label after
	exec(fmt.Sprintf("ALTER ROLE %s PASSWORD '%s'", quotedRole, pw2))

	// setSecret has run, finishSecret has not: AWSCURRENT is still the old value.
	// The pool cannot recover yet, and must fail cleanly rather than hang or spin.
	deadline, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := st.db.QueryRowContext(deadline, `SELECT 1`).Scan(&one); err == nil {
		t.Fatal("connected with the old password after it was changed — the server is not authenticating")
	} else if !isPgAuthFailure(err) {
		t.Fatalf("mid-rotation error should be the auth failure, got: %v", err)
	}

	// --- finishSecret: AWSCURRENT now points at the new password
	secret = pw2
	time.Sleep(2 * src.minGap) // let the per-stage throttle reopen
	before := fetches
	if err := st.db.QueryRowContext(ctx, `SELECT 1`).Scan(&one); err != nil || one != 1 {
		t.Fatalf("after rotation the pool did not recover on its own: %v", err)
	}
	if fetches <= before {
		t.Error("recovered without re-reading the secret — something else is going on")
	}
	if src.current() != pw2 {
		t.Errorf("source holds %q, want the rotated password", src.current())
	}

	// And it stays recovered without asking again: the new password is cached.
	before = fetches
	for range 3 {
		if err := st.db.QueryRowContext(ctx, `SELECT 1`).Scan(&one); err != nil {
			t.Fatalf("steady state after rotation: %v", err)
		}
	}
	if fetches != before {
		t.Errorf("%d extra store calls once the password was current", fetches-before)
	}
}
