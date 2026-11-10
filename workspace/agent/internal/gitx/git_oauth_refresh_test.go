package gitx

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// seedBB stores a bridge pointing at srv and a Bitbucket pair.
func seedBB(t *testing.T, srvURL string, c secrets.BitbucketCreds) {
	t.Helper()
	if err := secrets.Update(func(d *secrets.Data) error {
		d.GitOAuthBridge = &secrets.CPBridge{BaseURL: srvURL, Token: "afo_tok"}
		d.Bitbucket = &c
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func storedBB(t *testing.T) *secrets.BitbucketCreds {
	t.Helper()
	d, err := secrets.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return d.Bitbucket
}

func expiredBB() secrets.BitbucketCreds {
	return secrets.BitbucketCreds{AccessToken: "old", RefreshToken: "r0", Expiry: 1}
}

// Two refreshers holding the same expired snapshot: exactly one grant, and both end on the pair it produced.
func TestRefreshBitbucketStoredConcurrentCallersShareOneGrant(t *testing.T) {
	withAgentHome(t)
	var grants int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&grants, 1)
		time.Sleep(100 * time.Millisecond) // widen the race window
		_, _ = w.Write([]byte(`{"access_token":"fresh","refresh_token":"r1","expires_in":3600}`))
	}))
	defer srv.Close()
	seedBB(t, srv.URL, expiredBB())

	var wg sync.WaitGroup
	res := make([]secrets.BitbucketCreds, 4)
	errs := make([]error, 4)
	for i := range res {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := expiredBB()
			errs[i] = RefreshBitbucketStored(context.Background(), &c)
			res[i] = c
		}(i)
	}
	wg.Wait()
	if n := atomic.LoadInt32(&grants); n != 1 {
		t.Fatalf("grant calls = %d, want 1", n)
	}
	for i := range res {
		if errs[i] != nil || res[i].AccessToken != "fresh" || res[i].RefreshToken != "r1" {
			t.Fatalf("caller %d: %+v %v", i, res[i], errs[i])
		}
	}
	if st := storedBB(t); st.AccessToken != "fresh" || st.RefreshToken != "r1" {
		t.Fatalf("stored: %+v", st)
	}
}

// A caller whose snapshot is behind a pair another process already renewed adopts it
// without a grant, and never presents its own stale refresh token.
func TestRefreshBitbucketStoredAdoptsAnotherProcessesPair(t *testing.T) {
	withAgentHome(t)
	var grants int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&grants, 1)
		_, _ = w.Write([]byte(`{"access_token":"never","refresh_token":"never","expires_in":3600}`))
	}))
	defer srv.Close()
	renewed := secrets.BitbucketCreds{AccessToken: "other", RefreshToken: "r1", Expiry: time.Now().Unix() + 3600}
	seedBB(t, srv.URL, renewed)

	c := expiredBB()
	if err := RefreshBitbucketStored(context.Background(), &c); err != nil {
		t.Fatal(err)
	}
	if grants != 0 || c.AccessToken != "other" || c.RefreshToken != "r1" {
		t.Fatalf("grants=%d creds=%+v", grants, c)
	}
}

// An adopted pair that is itself about to expire is not adopted: the stored refresh token is exchanged.
func TestRefreshBitbucketStoredDoesNotAdoptAnExpiredPair(t *testing.T) {
	withAgentHome(t)
	var gotBody atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody.Store(string(b))
		_, _ = w.Write([]byte(`{"access_token":"fresh","refresh_token":"r2","expires_in":3600}`))
	}))
	defer srv.Close()
	seedBB(t, srv.URL, secrets.BitbucketCreds{AccessToken: "other", RefreshToken: "r1", Expiry: time.Now().Unix() + 5})

	c := expiredBB()
	if err := RefreshBitbucketStored(context.Background(), &c); err != nil {
		t.Fatal(err)
	}
	if b, _ := gotBody.Load().(string); !strings.Contains(b, `"r1"`) {
		t.Fatalf("must exchange the STORED refresh token r1, sent %q", b)
	}
	if c.AccessToken != "fresh" || storedBB(t).RefreshToken != "r2" {
		t.Fatalf("creds=%+v stored=%+v", c, storedBB(t))
	}
}

// A disconnect that lands while the grant is running is not undone by the renewal.
func TestRefreshBitbucketStoredDisconnectDuringGrantIsNotUndone(t *testing.T) {
	withAgentHome(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		RemoveBitbucketOAuth() // the member clicks disconnect while the CP is answering
		_, _ = w.Write([]byte(`{"access_token":"fresh","refresh_token":"r1","expires_in":3600}`))
	}))
	defer srv.Close()
	seedBB(t, srv.URL, expiredBB())

	c := expiredBB()
	err := RefreshBitbucketStored(context.Background(), &c)
	if !errors.Is(err, errBitbucketReplaced) {
		t.Fatalf("err = %v, want errBitbucketReplaced", err)
	}
	if st := storedBB(t); st != nil {
		t.Fatalf("the disconnected connection came back: %+v", st)
	}
	if c.AccessToken != "old" {
		t.Fatalf("a failed refresh must leave the caller's creds alone: %+v", c)
	}
}

// A reconnect (new pair) that lands during the grant is kept, and the caller is handed it
// rather than the stale grant result — unless it is another account's.
func TestRefreshBitbucketStoredReplacementDuringGrantWins(t *testing.T) {
	for _, tc := range []struct {
		name, stored string
		wantErr      bool
	}{{"same account", "alice", false}, {"unresolved account", "", false}, {"other account", "bob", true}} {
		t.Run(tc.name, func(t *testing.T) {
			withAgentHome(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = secrets.Update(func(d *secrets.Data) error {
					d.Bitbucket = &secrets.BitbucketCreds{AccessToken: "reconnected", RefreshToken: "rX",
						Expiry: time.Now().Unix() + 3600, Account: tc.stored}
					return nil
				})
				_, _ = w.Write([]byte(`{"access_token":"fresh","refresh_token":"r1","expires_in":3600}`))
			}))
			defer srv.Close()
			seeded := expiredBB()
			seeded.Account = "alice"
			seedBB(t, srv.URL, seeded)

			c := seeded
			err := RefreshBitbucketStored(context.Background(), &c)
			if tc.wantErr != errors.Is(err, errBitbucketReplaced) || (!tc.wantErr && err != nil) {
				t.Fatalf("err = %v", err)
			}
			if st := storedBB(t); st.AccessToken != "reconnected" || st.RefreshToken != "rX" {
				t.Fatalf("stored: %+v", st)
			}
			if !tc.wantErr && (c.AccessToken != "reconnected" || c.RefreshToken != "rX") {
				t.Fatalf("caller must get the reconnected pair: %+v", c)
			}
			if tc.wantErr && c.AccessToken != "old" {
				t.Fatalf("caller's creds must stay: %+v", c)
			}
		})
	}
}

// Adopting a pair the bridge already migrated must carry its (empty) legacy secret too.
func TestRefreshBitbucketStoredAdoptCarriesLegacySecretDrop(t *testing.T) {
	withAgentHome(t)
	seedBB(t, "http://127.0.0.1:1", secrets.BitbucketCreds{AccessToken: "other", RefreshToken: "r1", Expiry: time.Now().Unix() + 3600})
	c := expiredBB()
	c.Key, c.Secret = "legacy-key", "legacy-secret"
	if err := RefreshBitbucketStored(context.Background(), &c); err != nil {
		t.Fatal(err)
	}
	if c.Key != "" || c.Secret != "" {
		t.Fatalf("legacy secret came back: %+v", c)
	}
}

// The grant ends with the refresh budget, so a hung CP cannot hold the lock for its full retry ladder.
func TestRefreshBitbucketStoredGrantIsBoundedByTheBudget(t *testing.T) {
	withAgentHome(t)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	seedBB(t, srv.URL, expiredBB())
	old := bbRefreshWait
	bbRefreshWait = 300 * time.Millisecond
	defer func() { bbRefreshWait = old }()

	c := expiredBB()
	start := time.Now()
	if err := RefreshBitbucketStored(context.Background(), &c); err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("err=%v after %v", err, time.Since(start))
	}
}

// A disconnect while the caller waited for the lock fails the refresh without a grant.
func TestRefreshBitbucketStoredDisconnectBeforeLockFails(t *testing.T) {
	withAgentHome(t)
	var grants int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&grants, 1)
	}))
	defer srv.Close()
	seedBB(t, srv.URL, expiredBB())
	RemoveBitbucketOAuth()

	c := expiredBB()
	if err := RefreshBitbucketStored(context.Background(), &c); !errors.Is(err, errBitbucketReplaced) || grants != 0 {
		t.Fatalf("err=%v grants=%d", err, grants)
	}
}

// A lock held elsewhere makes the caller give up inside its budget instead of wedging git.
func TestRefreshBitbucketStoredLockWaitIsBounded(t *testing.T) {
	withAgentHome(t)
	seedBB(t, "http://127.0.0.1:1", expiredBB())
	old := bbRefreshWait
	bbRefreshWait = 200 * time.Millisecond
	defer func() { bbRefreshWait = old }()

	held, release := make(chan struct{}), make(chan struct{})
	go func() {
		_ = secrets.WithLock("bitbucket-refresh", func() error { close(held); <-release; return nil })
	}()
	<-held
	defer close(release)
	c := expiredBB()
	start := time.Now()
	err := RefreshBitbucketStored(context.Background(), &c)
	if !errors.Is(err, secrets.ErrLockTimeout) || time.Since(start) > 5*time.Second {
		t.Fatalf("err=%v after %v", err, time.Since(start))
	}
}

// BitbucketAuthHeader and RefreshBitbucketAndRetry go through the helper: two of them racing make one grant.
func TestBitbucketAuthPathsShareTheHelper(t *testing.T) {
	withAgentHome(t)
	var grants int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&grants, 1)
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write([]byte(`{"access_token":"fresh","refresh_token":"r1","expires_in":3600}`))
	}))
	defer srv.Close()
	seedBB(t, srv.URL, expiredBB())

	// Both snapshots are taken before either refresh starts, as two concurrent handlers would.
	s1, _ := secrets.Load()
	s2, _ := secrets.Load()
	var wg sync.WaitGroup
	var got [2]string
	wg.Add(2)
	go func() {
		defer wg.Done()
		got[0], _ = BitbucketAuthHeader(s1)
	}()
	go func() {
		defer wg.Done()
		s := s2
		_ = RefreshBitbucketAndRetry(s, ErrBitbucketUnauthorized, func(a string) error { got[1] = a; return nil })
	}()
	wg.Wait()
	if grants != 1 || got[0] != "Bearer fresh" || got[1] != "Bearer fresh" {
		t.Fatalf("grants=%d got=%v", grants, got)
	}
}
