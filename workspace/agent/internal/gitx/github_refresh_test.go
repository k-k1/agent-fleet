package gitx

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// Renewal of an expiring GitHub App user token (ADR 0052 decision 8). The fake below
// behaves like GitHub's token endpoint for a device-flow token: no client_secret, the
// refresh token is single use, and a spent or unknown one is answered with 200 and
// {"error":"bad_refresh_token"}. Nothing here has run against real GitHub.

type fakeGitHubTokens struct {
	mu      sync.Mutex
	valid   string // the one refresh token GitHub still honours
	n       int    // grants served
	calls   int32  // requests received, whatever the answer
	forms   []map[string]string
	status  int // when non-zero, answer every request with this status
	rotates int
}

func newFakeGitHubTokens(t *testing.T, validRefresh string) (*fakeGitHubTokens, *httptest.Server) {
	t.Helper()
	f := &fakeGitHubTokens{valid: validRefresh}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	old := githubTokenURL
	githubTokenURL = srv.URL
	t.Cleanup(func() { githubTokenURL = old })
	oldB := githubRefreshBackoff
	githubRefreshBackoff = []time.Duration{time.Millisecond, time.Millisecond}
	t.Cleanup(func() { githubRefreshBackoff = oldB })
	return f, srv
}

func (f *fakeGitHubTokens) serve(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt32(&f.calls, 1)
	_ = r.ParseForm()
	f.mu.Lock()
	defer f.mu.Unlock()
	form := map[string]string{}
	for k := range r.PostForm {
		form[k] = r.PostForm.Get(k)
	}
	f.forms = append(f.forms, form)
	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if form["grant_type"] != "refresh_token" || form["refresh_token"] != f.valid || form["client_id"] == "" {
		_, _ = w.Write([]byte(`{"error":"bad_refresh_token","error_description":"The refresh token passed is incorrect or expired."}`))
		return
	}
	f.n++
	f.valid = "fake-refresh-" + strconv.Itoa(f.n)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": "fake-access-" + strconv.Itoa(f.n), "refresh_token": f.valid,
		"expires_in": 28800, "refresh_token_expires_in": 15811200, "token_type": "bearer",
	})
}

func seedExpiring(t *testing.T, access, refresh string, expiry, refreshExpiry int64) {
	t.Helper()
	if err := secrets.Update(func(d *secrets.Data) error {
		d.Git["github.com"] = secrets.GitEntry{
			User: "x-access-token", Token: access, RefreshToken: refresh,
			Expiry: expiry, RefreshExpiry: refreshExpiry, ClientID: "fake-client-id", Login: "octo",
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func storedGitHub(t *testing.T) secrets.GitEntry {
	t.Helper()
	d, err := secrets.Load()
	if err != nil {
		t.Fatal(err)
	}
	return d.Git["github.com"]
}

func TestGitHubTokenWithoutRefreshTokenIsUntouched(t *testing.T) {
	withAgentHome(t)
	f, _ := newFakeGitHubTokens(t, "unused")
	if err := secrets.Update(func(d *secrets.Data) error {
		// Expiry in the past on purpose: with no refresh token it must still be used as is.
		d.Git["github.com"] = secrets.GitEntry{User: "x-access-token", Token: "fake-pat", Expiry: 1}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	d, _ := secrets.Load()
	tok, err := GitHubToken(d)
	if err != nil || tok != "fake-pat" {
		t.Fatalf("tok=%q err=%v", tok, err)
	}
	if f.calls != 0 {
		t.Fatalf("a token without a refresh token must never call GitHub, got %d", f.calls)
	}
}

func TestGitHubTokenFarFromExpiryIsNotRenewed(t *testing.T) {
	withAgentHome(t)
	f, _ := newFakeGitHubTokens(t, "fake-refresh-0")
	seedExpiring(t, "fake-access-0", "fake-refresh-0", time.Now().Add(2*time.Hour).Unix(), time.Now().Add(100*24*time.Hour).Unix())
	d, _ := secrets.Load()
	if tok, err := GitHubToken(d); err != nil || tok != "fake-access-0" {
		t.Fatalf("tok=%q err=%v", tok, err)
	}
	if f.calls != 0 {
		t.Fatalf("calls=%d", f.calls)
	}
}

func TestGitHubTokenRenewsNearExpiryAndRotatesTheRefreshToken(t *testing.T) {
	withAgentHome(t)
	f, _ := newFakeGitHubTokens(t, "fake-refresh-0")
	seedExpiring(t, "fake-access-0", "fake-refresh-0", time.Now().Add(time.Minute).Unix(), time.Now().Add(100*24*time.Hour).Unix())
	d, _ := secrets.Load()
	tok, err := GitHubToken(d)
	if err != nil || tok != "fake-access-1" {
		t.Fatalf("tok=%q err=%v", tok, err)
	}
	if got := f.forms[0]; got["client_id"] != "fake-client-id" || got["grant_type"] != "refresh_token" ||
		got["refresh_token"] != "fake-refresh-0" {
		t.Fatalf("grant form: %v", got)
	}
	if _, has := f.forms[0]["client_secret"]; has {
		t.Fatal("no client_secret may be sent: the device flow does not need one")
	}
	// What a restart sees: the rotated pair, the new expiry, and the untouched cache.
	e := storedGitHub(t)
	if e.Token != "fake-access-1" || e.RefreshToken != "fake-refresh-1" || e.Login != "octo" {
		t.Fatalf("stored: %+v", e)
	}
	if e.Expiry < time.Now().Add(7*time.Hour).Unix() || e.RefreshExpiry < time.Now().Add(180*24*time.Hour).Unix() {
		t.Fatalf("expiries not moved forward: %+v", e)
	}
	// A second process starting now needs no further grant.
	d2, _ := secrets.Load()
	if tok, err := GitHubToken(d2); err != nil || tok != "fake-access-1" || f.calls != 1 {
		t.Fatalf("after restart tok=%q err=%v calls=%d", tok, err, f.calls)
	}
}

// Two callers holding the same stale snapshot must not both spend the refresh token: the
// fake rejects a second use, which would show up as a reconnect.
func TestGitHubTokenConcurrentCallersSpendTheRefreshTokenOnce(t *testing.T) {
	withAgentHome(t)
	f, _ := newFakeGitHubTokens(t, "fake-refresh-0")
	seedExpiring(t, "fake-access-0", "fake-refresh-0", time.Now().Add(time.Minute).Unix(), time.Now().Add(100*24*time.Hour).Unix())
	const n = 12
	toks := make([]string, n)
	errs := make([]error, n)
	snaps := make([]*secrets.Data, n)
	for i := range snaps {
		snaps[i], _ = secrets.Load() // every caller saw the stale pair
	}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			toks[i], errs[i] = GitHubToken(snaps[i])
		}(i)
	}
	wg.Wait()
	for i := range toks {
		if errs[i] != nil || toks[i] != "fake-access-1" {
			t.Fatalf("caller %d: tok=%q err=%v", i, toks[i], errs[i])
		}
	}
	if f.calls != 1 {
		t.Fatalf("the refresh token was spent %d times", f.calls)
	}
	if e := storedGitHub(t); e.ReconnectNeeded || e.RefreshToken != "fake-refresh-1" {
		t.Fatalf("stored: %+v", e)
	}
}

func TestGitHubTokenRefusedRefreshTokenMeansReconnectAndStaysSo(t *testing.T) {
	withAgentHome(t)
	f, _ := newFakeGitHubTokens(t, "some-other-refresh") // GitHub no longer honours ours
	seedExpiring(t, "fake-access-0", "fake-refresh-0", time.Now().Add(time.Minute).Unix(), time.Now().Add(100*24*time.Hour).Unix())
	d, _ := secrets.Load()
	_, err := GitHubToken(d)
	if !errors.Is(err, ErrGitHubReconnect) {
		t.Fatalf("err=%v", err)
	}
	if strings.Contains(err.Error(), "fake-refresh-0") || strings.Contains(err.Error(), "bad_refresh_token") {
		t.Fatalf("error text must carry no token or provider body: %v", err)
	}
	if e := storedGitHub(t); !e.ReconnectNeeded || !GitHubReconnectNeeded(e) {
		t.Fatalf("the state must be persisted for the status: %+v", e)
	}
	before := f.calls
	d2, _ := secrets.Load()
	if _, err := GitHubToken(d2); !errors.Is(err, ErrGitHubReconnect) || f.calls != before {
		t.Fatalf("a refused connection must not ask GitHub again: err=%v calls=%d->%d", err, before, f.calls)
	}
}

func TestGitHubTokenExpiredRefreshTokenMeansReconnectWithoutAsking(t *testing.T) {
	withAgentHome(t)
	f, _ := newFakeGitHubTokens(t, "fake-refresh-0")
	seedExpiring(t, "fake-access-0", "fake-refresh-0", time.Now().Add(-time.Hour).Unix(), time.Now().Add(-time.Minute).Unix())
	d, _ := secrets.Load()
	if _, err := GitHubToken(d); !errors.Is(err, ErrGitHubReconnect) || f.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, f.calls)
	}
}

func TestGitHubTokenTransientFailureIsBoundedAndKeepsTheConnection(t *testing.T) {
	withAgentHome(t)
	f, _ := newFakeGitHubTokens(t, "fake-refresh-0")
	f.status = http.StatusBadGateway
	seedExpiring(t, "fake-access-0", "fake-refresh-0", time.Now().Add(time.Minute).Unix(), time.Now().Add(100*24*time.Hour).Unix())
	d, _ := secrets.Load()
	tok, err := GitHubToken(d)
	if err != nil || tok != "fake-access-0" {
		t.Fatalf("the still-valid token must keep working: tok=%q err=%v", tok, err)
	}
	if f.calls != int32(len(githubRefreshBackoff)+1) {
		t.Fatalf("retries must be bounded: %d calls", f.calls)
	}
	if e := storedGitHub(t); e.ReconnectNeeded || e.RefreshToken != "fake-refresh-0" {
		t.Fatalf("an outage is not a revocation: %+v", e)
	}
	// Once GitHub is back the next caller renews.
	f.mu.Lock()
	f.status = 0
	f.mu.Unlock()
	if tok, err := GitHubToken(d); err != nil || tok != "fake-access-1" {
		t.Fatalf("tok=%q err=%v", tok, err)
	}
}

func TestWithGitHubTokenRetriesOnceAfterA401(t *testing.T) {
	withAgentHome(t)
	f, _ := newFakeGitHubTokens(t, "fake-refresh-0")
	// Not near expiry by the recorded clock: only the 401 reveals it is dead.
	seedExpiring(t, "fake-access-0", "fake-refresh-0", time.Now().Add(2*time.Hour).Unix(), time.Now().Add(100*24*time.Hour).Unix())
	d, _ := secrets.Load()
	var seen []string
	err := WithGitHubToken(d, errors.New("not connected"), func(tok string) error {
		seen = append(seen, tok)
		if tok == "fake-access-0" {
			return NewGitHubUnauthorized("rejected")
		}
		return nil
	})
	if err != nil || strings.Join(seen, ",") != "fake-access-0,fake-access-1" || f.calls != 1 {
		t.Fatalf("err=%v seen=%v calls=%d", err, seen, f.calls)
	}
}

func TestWithGitHubTokenLeavesA401OfAPlainTokenAlone(t *testing.T) {
	withAgentHome(t)
	f, _ := newFakeGitHubTokens(t, "unused")
	if err := secrets.Update(func(d *secrets.Data) error {
		d.Git["github.com"] = secrets.GitEntry{User: "x-access-token", Token: "fake-pat"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	d, _ := secrets.Load()
	runs := 0
	err := WithGitHubToken(d, errors.New("not connected"), func(string) error {
		runs++
		return NewGitHubUnauthorized("rejected")
	})
	if !errors.Is(err, ErrGitHubUnauthorized) || runs != 1 || f.calls != 0 {
		t.Fatalf("err=%v runs=%d calls=%d", err, runs, f.calls)
	}
}

func TestWithGitHubTokenNotConnected(t *testing.T) {
	withAgentHome(t)
	sentinel := errors.New("not connected")
	d, _ := secrets.Load()
	if err := WithGitHubToken(d, sentinel, func(string) error { t.Fatal("must not run"); return nil }); err != sentinel {
		t.Fatalf("err=%v", err)
	}
}

// The old refresh token is spent the moment GitHub answers. If the store then refuses the
// write the new pair must survive in memory and be written on the next call, without a
// second grant (which GitHub would answer with bad_refresh_token).
func TestGitHubTokenFailedWriteDoesNotStrandTheConnection(t *testing.T) {
	withAgentHome(t)
	f, _ := newFakeGitHubTokens(t, "fake-refresh-0")
	seedExpiring(t, "fake-access-0", "fake-refresh-0", time.Now().Add(time.Minute).Unix(), time.Now().Add(100*24*time.Hour).Unix())
	if err := secrets.WithLock("github-refresh", func() error { return nil }); err != nil { // lock file exists before the dir is sealed
		t.Fatal(err)
	}
	dir := paths.AgentConfigDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	restore := func() { _ = os.Chmod(dir, 0o700) }
	t.Cleanup(restore)
	if _, err := os.CreateTemp(dir, ".probe-*"); err == nil {
		t.Skip("directory permissions are not enforced here (running as root?)")
	}

	d, _ := secrets.Load()
	tok, err := GitHubToken(d)
	if err != nil || tok != "fake-access-1" {
		t.Fatalf("the renewed token must be usable even when it could not be saved: tok=%q err=%v", tok, err)
	}
	restore()
	if e := storedGitHub(t); e.Token != "fake-access-0" {
		t.Fatalf("store should still hold the old pair: %+v", e)
	}
	d2, _ := secrets.Load()
	tok, err = GitHubToken(d2)
	if err != nil || tok != "fake-access-1" || f.calls != 1 {
		t.Fatalf("second call: tok=%q err=%v calls=%d", tok, err, f.calls)
	}
	if e := storedGitHub(t); e.Token != "fake-access-1" || e.RefreshToken != "fake-refresh-1" {
		t.Fatalf("the pending pair must reach the store: %+v", e)
	}
}

// A member who reconnects while a renewal is in flight keeps their new connection.
func TestGitHubRenewalDoesNotOverwriteAReconnect(t *testing.T) {
	withAgentHome(t)
	newFakeGitHubTokens(t, "fake-refresh-0")
	seedExpiring(t, "fake-access-0", "fake-refresh-0", time.Now().Add(time.Minute).Unix(), time.Now().Add(100*24*time.Hour).Unix())
	if err := persistGitHub("fake-access-0", secrets.GitEntry{Token: "fake-access-9", RefreshToken: "fake-refresh-9"}); err != nil {
		t.Fatal(err)
	}
	if err := persistGitHub("fake-access-0", secrets.GitEntry{Token: "late", RefreshToken: "late"}); !errors.Is(err, errSuperseded) {
		t.Fatalf("err=%v", err)
	}
	if e := storedGitHub(t); e.Token != "fake-access-9" {
		t.Fatalf("stored: %+v", e)
	}
}

func TestGitHubStoreFileHoldsNoClientSecret(t *testing.T) {
	withAgentHome(t)
	newFakeGitHubTokens(t, "fake-refresh-0")
	seedExpiring(t, "fake-access-0", "fake-refresh-0", time.Now().Add(time.Minute).Unix(), time.Now().Add(100*24*time.Hour).Unix())
	d, _ := secrets.Load()
	if _, err := GitHubToken(d); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(paths.AgentConfigDir(), "secrets.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "client_secret") {
		t.Fatalf("no client secret may be stored: %s", b)
	}
}
