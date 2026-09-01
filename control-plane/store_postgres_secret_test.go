package main

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// authErr is what pgx hands back when the server refuses the credentials, minus
// the connection machinery: the SQLSTATE arrives wrapped, and errors.As is what
// isPgAuthFailure walks. The real *pgconn.ConnectError wrapping (whose inner error
// is unexported and cannot be constructed here) is covered by
// TestPostgresPasswordRotation against a live server.
func authErr() error {
	return fmt.Errorf("failed to connect: %w", &pgconn.PgError{
		Severity: "FATAL", Code: "28P01",
		Message: `password authentication failed for user "afadmin"`,
	})
}

func TestIsPgAuthFailure(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"28P01 wrapped", authErr(), true},
		{"28000", fmt.Errorf("x: %w", &pgconn.PgError{Code: "28000"}), true},
		{"joined with fallbacks", errors.Join(errors.New("dial tcp: refused"), authErr()), true},
		{"other sqlstate", fmt.Errorf("x: %w", &pgconn.PgError{Code: "42P01"}), false},
		{"host down", errors.New("dial tcp 10.0.0.1:5432: connect: connection refused"), false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		if got := isPgAuthFailure(c.err); got != c.want {
			t.Errorf("%s: isPgAuthFailure = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestDBPasswordFromSecret(t *testing.T) {
	const rdsShape = `{"username":"afadmin","password":"s3cr3t"}`
	if got, err := dbPasswordFromSecret(rdsShape, "password"); err != nil || got != "s3cr3t" {
		t.Errorf("rds-managed shape: got %q, %v", got, err)
	}
	// key "" = the secret string IS the password (a hand-rolled deployment).
	if got, err := dbPasswordFromSecret("s3cr3t", ""); err != nil || got != "s3cr3t" {
		t.Errorf("raw: got %q, %v", got, err)
	}
	if _, err := dbPasswordFromSecret("not json", "password"); err == nil {
		t.Error("a non-JSON secret with a key configured must be an error, not an empty password")
	}
	if _, err := dbPasswordFromSecret(`{"username":"afadmin"}`, "password"); err == nil {
		t.Error("a missing field must be an error, not an empty password")
	}
	if _, err := dbPasswordFromSecret(`{"password":""}`, "password"); err == nil {
		t.Error("an empty field must be an error: connecting with '' would just look like a rotation")
	}
}

func TestRegionFromSecretARN(t *testing.T) {
	const arn = "arn:aws:secretsmanager:ap-northeast-1:000000000000:secret:rds!db-abc-123-AbCdEf"
	if got := regionFromSecretARN(arn); got != "ap-northeast-1" {
		t.Errorf("got %q", got)
	}
	for _, bad := range []string{"", "rds!db-abc", "arn:aws:ssm:ap-northeast-1:1:parameter/x", "arn:aws:secretsmanager::1:secret:x"} {
		if got := regionFromSecretARN(bad); got != "" {
			t.Errorf("%q: got %q, want empty", bad, got)
		}
	}
}

// Without AF_DB_PASSWORD_SECRET_ARN nothing in this file may run. That is what
// keeps compose, on-prem, SQLite and every test behaving exactly as before.
func TestDBPasswordSourceInertWithoutARN(t *testing.T) {
	src := newDBPasswordSource("", "password")
	called := false
	src.fetch = func(context.Context, string, string) (string, error) { called = true; return "new", nil }
	src.seed("boot")

	if _, ok := src.refresh(context.Background(), stageCurrent, "boot"); ok {
		t.Error("refresh must report nothing-to-try when no secret is configured")
	}
	if called {
		t.Error("no secret configured, yet the store was called")
	}
	if src.current() != "boot" {
		t.Errorf("password changed to %q", src.current())
	}
}

func TestDBPasswordSourceRefresh(t *testing.T) {
	src := newDBPasswordSource("arn:aws:secretsmanager:ap-northeast-1:1:secret:rds!db-x-y", "password")
	src.seed("old")
	var asked []string
	src.fetch = func(_ context.Context, _, stage string) (string, error) {
		asked = append(asked, stage)
		if stage == stageCurrent {
			return "old", nil // mid-rotation: setSecret has run, finishSecret has not
		}
		return "new", nil
	}

	// AWSCURRENT still hands back the password that just failed → nothing to try.
	if _, ok := src.refresh(context.Background(), stageCurrent, "old"); ok {
		t.Error("AWSCURRENT returned the failing password; refresh must not claim progress")
	}
	if src.current() != "old" {
		t.Errorf("password must be untouched, got %q", src.current())
	}
	// AWSPENDING is the way through the setSecret→finishSecret window.
	pw, ok := src.refresh(context.Background(), stagePending, "old")
	if !ok || pw != "new" {
		t.Fatalf("AWSPENDING: got %q, ok=%v", pw, ok)
	}
	if src.current() != "new" {
		t.Errorf("adopted password is %q", src.current())
	}
	if strings.Join(asked, ",") != stageCurrent+","+stagePending {
		t.Errorf("stages asked for: %v", asked)
	}
}

// A wrong password must not turn into a Secrets Manager call per connection
// attempt: the pool retries forever, the API does not.
func TestDBPasswordSourceThrottles(t *testing.T) {
	src := newDBPasswordSource("arn:aws:secretsmanager:ap-northeast-1:1:secret:rds!db-x-y", "password")
	src.seed("old")
	calls := 0
	src.fetch = func(context.Context, string, string) (string, error) { calls++; return "", errors.New("AccessDenied") }

	for range 5 {
		src.refresh(context.Background(), stageCurrent, "old")
	}
	if calls != 1 {
		t.Errorf("fetched %d times inside the throttle window, want 1", calls)
	}
	// The window is per stage: AWSPENDING must still get its one chance.
	src.refresh(context.Background(), stagePending, "old")
	if calls != 2 {
		t.Errorf("AWSPENDING was throttled by AWSCURRENT's window (calls=%d)", calls)
	}

	src.mu.Lock()
	src.last[stageCurrent] = time.Now().Add(-time.Hour)
	src.mu.Unlock()
	src.refresh(context.Background(), stageCurrent, "old")
	if calls != 3 {
		t.Errorf("the throttle never reopens (calls=%d)", calls)
	}
}

// A rotation fails every connection the pool tries to open at once. One of them
// should do the work; the rest must be handed the answer, NOT throttled into
// giving up — that would turn a two-second blip into a partial outage.
func TestDBPasswordSourceConcurrentRefreshShareOneFetch(t *testing.T) {
	src := newDBPasswordSource("arn:aws:secretsmanager:ap-northeast-1:1:secret:rds!db-x-y", "password")
	src.seed("old")
	calls := 0
	src.fetch = func(context.Context, string, string) (string, error) {
		calls++
		time.Sleep(5 * time.Millisecond)
		return "new", nil
	}

	var wg sync.WaitGroup
	got := make([]string, 8)
	oks := make([]bool, 8)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i], oks[i] = src.refresh(context.Background(), stageCurrent, "old")
		}()
	}
	wg.Wait()

	if calls != 1 {
		t.Errorf("8 racing connections made %d API calls, want 1", calls)
	}
	for i := range got {
		if !oks[i] || got[i] != "new" {
			t.Errorf("goroutine %d got %q ok=%v, want the refreshed password", i, got[i], oks[i])
		}
	}
}

// --- the connector -----------------------------------------------------------

type stubConn struct{}

func (stubConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not used") }
func (stubConn) Close() error                        { return nil }
func (stubConn) Begin() (driver.Tx, error)           { return nil, errors.New("not used") }

// fakeConnector stands in for pgx's: it accepts exactly one password, the way a
// server does, and records what it was offered.
type fakeConnector struct {
	src      *dbPasswordSource
	accepts  string
	offered  []string
	downWith error // when set, fail with this regardless of the password
}

func (f *fakeConnector) Connect(context.Context) (driver.Conn, error) {
	pw := f.src.current()
	f.offered = append(f.offered, pw)
	if f.downWith != nil {
		return nil, f.downWith
	}
	if pw != f.accepts {
		return nil, authErr()
	}
	return stubConn{}, nil
}
func (f *fakeConnector) Driver() driver.Driver { return nil }

func newTestConnector(accepts, seed, arn string) (*refreshingConnector, *fakeConnector, *dbPasswordSource) {
	src := newDBPasswordSource(arn, "password")
	src.seed(seed)
	fake := &fakeConnector{src: src, accepts: accepts}
	return &refreshingConnector{Connector: fake, src: src}, fake, src
}

const testARN = "arn:aws:secretsmanager:ap-northeast-1:1:secret:rds!db-x-y"

// The 2026-09-01 outage, reproduced and then not happening: the injected password
// is stale, and the caller never learns.
func TestRefreshingConnectorRecoversFromRotation(t *testing.T) {
	c, fake, src := newTestConnector("rotated", "stale", testARN)
	src.fetch = func(context.Context, string, string) (string, error) { return "rotated", nil }

	conn, err := c.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if conn == nil {
		t.Fatal("nil conn")
	}
	if want := []string{"stale", "rotated"}; strings.Join(fake.offered, ",") != strings.Join(want, ",") {
		t.Errorf("offered %v, want %v", fake.offered, want)
	}
	// The pool keeps the new password, so the next connection costs no API call.
	if src.current() != "rotated" {
		t.Errorf("source still holds %q", src.current())
	}
}

// Mid-rotation: AWSCURRENT is the password that just failed, AWSPENDING is the one
// the database has already been given.
func TestRefreshingConnectorFallsBackToPending(t *testing.T) {
	c, fake, src := newTestConnector("pending", "stale", testARN)
	src.fetch = func(_ context.Context, _, stage string) (string, error) {
		if stage == stageCurrent {
			return "stale", nil
		}
		return "pending", nil
	}

	if _, err := c.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if want := []string{"stale", "pending"}; strings.Join(fake.offered, ",") != strings.Join(want, ",") {
		t.Errorf("offered %v, want %v", fake.offered, want)
	}
}

// No secret configured: one attempt, the original error, no retry loop. This is
// every non-ECS deployment.
func TestRefreshingConnectorInertWithoutARN(t *testing.T) {
	c, fake, src := newTestConnector("right", "wrong", "")
	src.fetch = func(context.Context, string, string) (string, error) {
		t.Error("the store must not be consulted when no ARN is configured")
		return "right", nil
	}

	_, err := c.Connect(context.Background())
	if !isPgAuthFailure(err) {
		t.Fatalf("want the auth error back, got %v", err)
	}
	if len(fake.offered) != 1 {
		t.Errorf("attempted %d connections, want 1", len(fake.offered))
	}
}

// A database that is down is not a rotation. Refreshing a password would not help
// and must not be attempted.
func TestRefreshingConnectorDoesNotChaseNonAuthErrors(t *testing.T) {
	c, fake, src := newTestConnector("right", "right", testARN)
	fake.downWith = errors.New("dial tcp 10.0.0.1:5432: connect: connection refused")
	src.fetch = func(context.Context, string, string) (string, error) {
		t.Error("a connection refused is not a credentials problem")
		return "", nil
	}

	if _, err := c.Connect(context.Background()); err == nil {
		t.Fatal("want an error")
	}
	if len(fake.offered) != 1 {
		t.Errorf("attempted %d connections, want 1", len(fake.offered))
	}
}

// The password really is wrong (a misconfigured deployment, not a rotation): both
// stages get one chance each and then it gives up, rather than spinning.
func TestRefreshingConnectorGivesUp(t *testing.T) {
	c, fake, src := newTestConnector("right", "wrong", testARN)
	src.fetch = func(context.Context, string, string) (string, error) { return "also-wrong", nil }

	_, err := c.Connect(context.Background())
	if !isPgAuthFailure(err) {
		t.Fatalf("want the auth error back, got %v", err)
	}
	// stale + AWSCURRENT + AWSPENDING(throttled to the same value, so no third try)
	if len(fake.offered) > 3 {
		t.Errorf("attempted %d connections; this must be bounded", len(fake.offered))
	}
}
