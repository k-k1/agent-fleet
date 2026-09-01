package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"
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
//	sed -i '1i local all af_rot_test scram-sha-256' "$D/data/pg_hba.conf"
//	"$D/dist/bin/pg_ctl" -D "$D/data" reload
//	AF_TEST_DATABASE_URL="postgres://postgres@/postgres?host=$D/sock&sslmode=disable" \
//	  go test -run TestPostgresPasswordRotation -v
func TestPostgresPasswordRotation(t *testing.T) {
	adminURL := os.Getenv("AF_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("set AF_TEST_DATABASE_URL to run the Postgres rotation test")
	}
	ctx := context.Background()

	admin, err := sql.Open("pgx", adminURL)
	if err != nil {
		t.Fatalf("open admin: %v", err)
	}
	defer admin.Close()
	if err := admin.PingContext(ctx); err != nil {
		t.Fatalf("ping admin: %v", err)
	}

	const role = "af_rot_test"
	const pw1, pw2 = "rot-before-1", "rot-after-2"
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
	// A plain DROP ROLE fails while the GRANT below still references it, which on a
	// persistent server leaves the role behind and makes the NEXT run fail at CREATE.
	dropRole := func() {
		c := context.Background()
		admin.ExecContext(c, fmt.Sprintf(`REVOKE ALL ON DATABASE %q FROM %s`, dbName, role))
		admin.ExecContext(c, `DROP ROLE IF EXISTS `+role)
	}
	dropRole() // best effort; a previous run may have left it
	exec(fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD '%s'", role, pw1))
	t.Cleanup(dropRole)
	exec(fmt.Sprintf(`GRANT CONNECT ON DATABASE %q TO %s`, dbName, role))

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
	exec(fmt.Sprintf("ALTER ROLE %s PASSWORD '%s'", role, pw2))

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
