package secrets

import "testing"

func seedOAuth(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	keyEnv(t, "", "")
	if err := Update(func(s *Data) error {
		s.Jira = &JiraCreds{AuthKind: "oauth", AccessToken: "j-a1", RefreshToken: "j-r1", Expiry: 100,
			CloudID: "c1", Sites: []JiraSite{{CloudID: "c1", Scopes: []string{"read:jira-work"}}}}
		s.Bitbucket = &BitbucketCreds{AccessToken: "b-a1", RefreshToken: "b-r1", Expiry: 100}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// rotate stands in for another process renewing both connections under the store lock.
func rotate(t *testing.T) {
	t.Helper()
	if err := Update(func(s *Data) error {
		s.Jira.AccessToken, s.Jira.RefreshToken, s.Jira.Expiry = "j-a2", "j-r2", 200
		s.Bitbucket.AccessToken, s.Bitbucket.RefreshToken, s.Bitbucket.Expiry = "b-a2", "b-r2", 200
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func mustLoad(t *testing.T) *Data {
	t.Helper()
	s, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A snapshot saved for an unrelated change must not put back refresh tokens that were
// rotated after it was loaded: the provider has retired them.
func TestSaveKeepsRotatedOAuthTokens(t *testing.T) {
	seedOAuth(t)
	stale := mustLoad(t)
	rotate(t)
	stale.Claude = "unrelated-edit"
	if err := stale.Save(); err != nil {
		t.Fatal(err)
	}
	got := mustLoad(t)
	if got.Jira.RefreshToken != "j-r2" || got.Jira.AccessToken != "j-a2" || got.Jira.Expiry != 200 {
		t.Fatalf("jira rolled back: %+v", got.Jira)
	}
	if got.Bitbucket.RefreshToken != "b-r2" || got.Bitbucket.AccessToken != "b-a2" {
		t.Fatalf("bitbucket rolled back: %+v", got.Bitbucket)
	}
	if got.Claude != "unrelated-edit" {
		t.Fatalf("the caller's own edit was lost: %q", got.Claude)
	}
}

// A caller that edited only cached fields of the entry keeps its edit, on top of the
// rotated tokens.
func TestSaveMergesCacheEditOntoRotatedTokens(t *testing.T) {
	seedOAuth(t)
	stale := mustLoad(t)
	rotate(t)
	stale.Bitbucket.Account, stale.Bitbucket.Email = "handle", "h@example.com"
	stale.Jira.Account = "Display Name"
	if err := stale.Save(); err != nil {
		t.Fatal(err)
	}
	got := mustLoad(t)
	if got.Bitbucket.RefreshToken != "b-r2" || got.Bitbucket.Account != "handle" || got.Bitbucket.Email != "h@example.com" {
		t.Fatalf("bitbucket: %+v", got.Bitbucket)
	}
	if got.Jira.RefreshToken != "j-r2" || got.Jira.Account != "Display Name" {
		t.Fatalf("jira: %+v", got.Jira)
	}
}

// The caller's own change to the connection still wins: a renewal it ran itself, and a
// disconnect.
func TestSaveWritesCallersOwnConnectionChange(t *testing.T) {
	seedOAuth(t)
	s := mustLoad(t)
	s.Bitbucket.AccessToken, s.Bitbucket.RefreshToken, s.Bitbucket.Expiry = "b-a9", "b-r9", 900
	s.Jira = nil
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	got := mustLoad(t)
	if got.Jira != nil {
		t.Fatalf("disconnect lost: %+v", got.Jira)
	}
	if got.Bitbucket.RefreshToken != "b-r9" {
		t.Fatalf("own renewal lost: %+v", got.Bitbucket)
	}

	// The baseline follows a Save: a second save of the same snapshot after another
	// rotation keeps that rotation too.
	if err := Update(func(d *Data) error { d.Bitbucket.RefreshToken = "b-r10"; return nil }); err != nil {
		t.Fatal(err)
	}
	s.Claude = "again"
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if got := mustLoad(t); got.Bitbucket.RefreshToken != "b-r10" {
		t.Fatalf("second save rolled back: %+v", got.Bitbucket)
	}
}

// A reconnect or disconnect stored meanwhile (not just new tokens) wins over a snapshot
// that kept the tokens, even one that edited a cached field, and the edit does not graft
// onto the new connection.
func TestSaveKeepsReplacedConnection(t *testing.T) {
	for _, edit := range []bool{false, true} {
		seedOAuth(t)
		stale := mustLoad(t)
		if err := Update(func(d *Data) error {
			d.Jira = &JiraCreds{AuthKind: "oauth", AccessToken: "n-a", RefreshToken: "n-r", Expiry: 300, CloudID: "c2"}
			d.Bitbucket = nil
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		stale.Claude = "x"
		if edit {
			stale.Jira.Account = "stale cache"
			stale.Bitbucket.Account = "stale cache"
		}
		if err := stale.Save(); err != nil {
			t.Fatal(err)
		}
		got := mustLoad(t)
		if got.Jira.CloudID != "c2" || got.Jira.RefreshToken != "n-r" || got.Jira.Account != "" {
			t.Fatalf("edit=%v: reconnect rolled back: %+v", edit, got.Jira)
		}
		if got.Bitbucket != nil {
			t.Fatalf("edit=%v: disconnect rolled back: %+v", edit, got.Bitbucket)
		}
	}
}

// The baseline is a deep copy: mutating the snapshot's slices in place must still read as
// a change by the caller.
func TestOAuthBaseIsDeepCopy(t *testing.T) {
	seedOAuth(t)
	s := mustLoad(t)
	s.Jira.Sites[0].Scopes[0] = "changed"
	if keepRotated(s.Jira, s.jiraBase, s.jiraBase, jiraTokens, setJiraTokens) != s.Jira {
		t.Fatal("an in-place slice edit was taken for an untouched entry")
	}
}

// The everyday status-poll path: a cached field filled in while nothing else moved is
// written as is.
func TestSaveWritesCacheFillWithoutRotation(t *testing.T) {
	seedOAuth(t)
	s := mustLoad(t)
	s.Bitbucket.Account = "handle"
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if got := mustLoad(t); got.Bitbucket.Account != "handle" || got.Bitbucket.RefreshToken != "b-r1" {
		t.Fatalf("cache fill lost: %+v", got.Bitbucket)
	}
}

// A connection the caller creates is written; a connection created meanwhile survives a
// snapshot that never had one.
func TestSaveNewConnectionAndStaleAbsence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	keyEnv(t, "", "")
	stale := mustLoad(t) // store not created yet
	fresh := mustLoad(t)
	fresh.Jira = &JiraCreds{AuthKind: "oauth", AccessToken: "a", RefreshToken: "r", Expiry: 1}
	if err := fresh.Save(); err != nil {
		t.Fatal(err)
	}
	if got := mustLoad(t); got.Jira == nil || got.Jira.RefreshToken != "r" {
		t.Fatalf("new connection not written: %+v", got.Jira)
	}
	stale.Claude = "x"
	if err := stale.Save(); err != nil {
		t.Fatal(err)
	}
	if got := mustLoad(t); got.Jira == nil || got.Jira.RefreshToken != "r" {
		t.Fatalf("connection made meanwhile was dropped: %+v", got.Jira)
	}
}

// When the store changed more than the tokens (a scope backfill, a site switch) and the
// caller only edited a cached field, the stored entry wins and the cache edit is dropped:
// the cache is refilled on the next poll, a lost backfill or site choice is not.
func TestSaveCacheEditYieldsToOtherStoredChange(t *testing.T) {
	seedOAuth(t)
	stale := mustLoad(t)
	if err := Update(func(d *Data) error {
		d.Jira.Sites[0].Scopes = append(d.Jira.Sites[0].Scopes, "read:board-scope:jira-software")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	stale.Jira.Account = "stale cache"
	if err := stale.Save(); err != nil {
		t.Fatal(err)
	}
	got := mustLoad(t)
	if len(got.Jira.Sites[0].Scopes) != 2 || got.Jira.Account != "" {
		t.Fatalf("stored change not kept: %+v", got.Jira)
	}
}

// An empty, non-nil slice in the snapshot reads as untouched, not as an edit.
func TestOAuthBaseKeepsEmptySlices(t *testing.T) {
	c := &JiraCreds{Sites: []JiraSite{{CloudID: "c", Scopes: []string{}}}}
	if b := cloneJira(c); b.Sites[0].Scopes == nil {
		t.Fatal("clone turned an empty slice into nil")
	}
}
