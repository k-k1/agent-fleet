package awsx

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/cloudlogin"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Decision 10 as af-aws-exec, the poll, the SSM pane and the Console reach it.

func teamTarget(sp Profile) keyBinding {
	return keyBinding{Session: "af-team", Owner: sp.TokenOwner(), StartURL: sp.StartURL, SSORegion: sp.SSORegion}
}

// A key the member's own files take is theirs: vetting leaves its token alone, even one for
// another portal.
func TestOwnSessionTokenSurvivesSyncVetting(t *testing.T) {
	s := teamSettings(t)
	own := "[sso-session af-team]\nsso_start_url = https://mine.awsapps.com/start\nsso_region = us-east-1\n"
	b, _ := os.ReadFile(ConfigPath())
	if err := os.WriteFile(ConfigPath(), append([]byte(own), b...), 0o600); err != nil {
		t.Fatal(err)
	}
	token(t, "af-team", "https://mine.awsapps.com/start", "us-east-1", "theirs")
	if err := vetAll(s, UsableNow()); err != nil {
		t.Fatal(err)
	}
	if !exists(ssoCachePath("af-team")) {
		t.Fatal("the member's own login was retired")
	}
	if err := VetKeyOf(s, "a"); err != nil || !exists(ssoCachePath("af-team")) {
		t.Fatalf("VetKeyOf touched the member's own key: %v", err)
	}
}

// Every Sync vets before it returns: a portal change retires the old portal's login at once.
func TestSyncRetiresAChangedPortalBeforeReturning(t *testing.T) {
	s := teamSettings(t)
	sp := s["a"]
	token(t, "af-team", sp.StartURL, sp.SSORegion, "old")
	if !accepted(t, s, "af-team") {
		t.Fatal("control: the current portal's login is kept")
	}
	moved := []Profile{s["a"], s["b"]}
	for i := range moved {
		moved[i].StartURL = "https://other.awsapps.com/start"
	}
	body, _ := json.Marshal(map[string]any{"profiles": moved})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
	defer srv.Close()
	t.Setenv("AF_CP_BASE_URL", srv.URL)
	if _, err := Sync(); err != nil {
		t.Fatal(err)
	}
	if exists(ssoCachePath("af-team")) {
		t.Fatal("Sync returned with the old portal's login still readable")
	}
}

// A landing for an owner Settings no longer gives the key is retired at once, so the key's new
// owner cannot adopt it; a landing still under way is not adopted either.
func TestFailedLandingCannotBeAdoptedForAnotherOwner(t *testing.T) {
	s := teamSettings(t)
	sp := s["a"]
	old := teamTarget(sp)
	old.Owner = "sign-in:old"
	token(t, "af-team", sp.StartURL, sp.SSORegion, "for-old")
	if bindLanding("af-team", old) {
		t.Fatal("a landing for another owner bound")
	}
	if accepted(t, s, "af-team") {
		t.Fatal("the new owner adopted a token written for another")
	}

	token(t, "af-team", sp.StartURL, sp.SSORegion, "landing")
	_, unregister := registerAttempt(teamTarget(sp))
	if err := vetKey(s, names(s), "af-team"); err != nil {
		t.Fatal(err)
	}
	if exists(bindingPath("af-team")) {
		t.Fatal("a token was adopted while its login attempt was still landing")
	}
	unregister()
	if !accepted(t, s, "af-team") || !exists(bindingPath("af-team")) {
		t.Fatal("control: an unbound token is adopted once no attempt lands it")
	}
}

// A sync ends the attempts whose target Settings no longer names, a renamed (dropped) key's
// included, and leaves the others running.
func TestRenamedAttemptIsEndedBySyncVetting(t *testing.T) {
	s := teamSettings(t)
	sp := s["a"]
	keep := logins.Begin("af-team", "", "a", func() {})
	gone := logins.Begin("af-gone", "", "x", func() {})
	t.Cleanup(func() { keep.End(cloudlogin.PhaseFailed, ""); gone.End(cloudlogin.PhaseFailed, "") })
	r1, u1 := registerAttempt(teamTarget(sp))
	defer u1()
	attachAttempt(r1, keep)
	r2, u2 := registerAttempt(keyBinding{Session: "af-gone", Owner: "sign-in:g9", StartURL: sp.StartURL, SSORegion: sp.SSORegion})
	defer u2()
	attachAttempt(r2, gone)
	if err := vetAll(s, UsableNow()); err != nil {
		t.Fatal(err)
	}
	if gone.Live() {
		t.Fatal("the attempt of a key Settings dropped kept running")
	}
	if !keep.Live() {
		t.Fatal("an attempt whose target still holds was ended")
	}
	if landingFor("af-gone") {
		t.Fatal("the ended attempt is still registered")
	}
}

// A pending request serves only the owner and portal it was filed for, and any current member
// of the sign-in, not just the profile that filed it.
func TestPendingLoginRejectsAChangedOwner(t *testing.T) {
	s := teamSettings(t)
	want := teamTarget(s["a"])
	req := LoginRequest{Profile: "a", Key: "af-team", Target: want.id()}
	if sp, ok := settingsFor(req, s); !ok || sp.Name != "a" {
		t.Fatal("control: the request's own target")
	}
	onlyB := map[string]Profile{"b": s["b"]}
	if sp, ok := settingsFor(req, onlyB); !ok || sp.Name != "b" {
		t.Fatalf("the filing profile's removal hid the request: %v %v", sp.Name, ok)
	}
	other := map[string]Profile{}
	for k, v := range s {
		v.SignInID = "g2"
		other[k] = v
	}
	if _, ok := settingsFor(req, other); ok {
		t.Fatal("a request was served for another owner of its key")
	}

	// A run for another target of the same key files its own request.
	r1, _, err := logins.FileCancel("a", "af-team", CacheState{}, LoginWaiter{Profile: "a", Target: want.id()}, nil, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	r2, _, err := logins.FileCancel("a", "af-team", CacheState{}, LoginWaiter{Profile: "a", Target: "sign-in:g2|x|y"}, nil, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if r1.ID == r2.ID {
		t.Fatal("a run for another owner joined the request")
	}

	// A run planned for one owner does not export once Settings gives the key to another.
	check, _ := plannedTarget(s, "af-team")
	if check == nil || check("af-team") != nil {
		t.Fatal("control: the planned owner exports")
	}
	ps := []Profile{other["a"], other["b"]}
	if err := saveSettingsCache(ps, nil); err != nil {
		t.Fatal(err)
	}
	if err := check("af-team"); !errors.Is(err, ErrSettingsChangedRun) {
		t.Fatalf("export after an owner change = %v", err)
	}
}

// The SSM pane and discovery read the key their meta renders: a meta rendered for another
// sign-in, owner or portal than Settings gives that key is refused, and the forced re-login
// does not drop a login the meta does not own.
func TestSSMMetaThatSettingsNoLongerMatchesIsRefused(t *testing.T) {
	s := teamSettings(t)
	sp := s["a"]
	token(t, "af-team", sp.StartURL, sp.SSORegion, "tok")
	meta := session.SSMMeta{Profile: "a", StartURL: sp.StartURL, SSORegion: sp.SSORegion, AccountID: sp.AccountID,
		RoleName: sp.RoleName, SSOSession: "af-team", SSOOwnerID: "g1"}
	if err := vetSSMLogin(meta); err != nil {
		t.Fatalf("control: the current meta = %v", err)
	}
	for name, m := range map[string]session.SSMMeta{
		"old wire (af-<profile>)": {Profile: "a", StartURL: sp.StartURL, SSORegion: sp.SSORegion},
		"another owner":           func() session.SSMMeta { x := meta; x.SSOOwnerID = "g9"; return x }(),
		"another portal":          func() session.SSMMeta { x := meta; x.StartURL = "https://other.awsapps.com/start"; return x }(),
		"a profile not in it":     func() session.SSMMeta { x := meta; x.Profile = "zz"; return x }(),
	} {
		if err := vetSSMLogin(m); !errors.Is(err, ErrSSMSettingsChanged) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if !exists(ssoCachePath("af-team")) {
		t.Fatal("a refused meta moved the shared login")
	}
	other := meta
	other.SSOOwnerID = "g9"
	if err := forgetSSMLogin(other); !errors.Is(err, ErrSSMSettingsChanged) || !exists(ssoCachePath("af-team")) {
		t.Fatalf("a forced re-login for another owner = %v, token kept %v", err, exists(ssoCachePath("af-team")))
	}
	// A profile Settings does not have renders a key that is not Agent Fleet's: used as before,
	// unless a binding there names another portal.
	zz := session.SSMMeta{Profile: "zz", StartURL: sp.StartURL, SSORegion: sp.SSORegion}
	if err := vetSSMLogin(zz); err != nil {
		t.Fatalf("control: a profile of its own = %v", err)
	}
	writeJSONAtomic(bindingPath("af-zz"), keyBinding{Session: "af-zz", Owner: "profile:pz", StartURL: "https://other.awsapps.com/start", SSORegion: sp.SSORegion})
	if err := vetSSMLogin(zz); !errors.Is(err, ErrSSMSettingsChanged) {
		t.Fatalf("over another portal's binding = %v", err)
	}
}

// A retire that cannot delete what it must is reported, and af-aws-exec's vet fails.
func TestRetireFailureCannotExportCredentials(t *testing.T) {
	s := teamSettings(t)
	sp := s["a"]
	token(t, "af-team", sp.StartURL, sp.SSORegion, "tok")
	writeJSONAtomic(bindingPath("af-team"), keyBinding{Session: "af-team", Owner: "sign-in:old", StartURL: sp.StartURL, SSORegion: sp.SSORegion})
	// A role cache that cannot be removed: a non-empty directory in its place.
	stuck := ssoRoleCachePath(sp.AccountID, sp.RoleName, "af-team")
	if err := os.MkdirAll(filepath.Join(stuck, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := VetKeyOf(s, "a"); !errors.Is(err, ErrKeyRetireFailed) {
		t.Fatalf("VetKeyOf = %v", err)
	}
}

// botocore serves cached role credentials without reading the token, so a binding that names
// another owner deletes them even when no token is cached.
func TestNoTokenStillInvalidatesOldRoleCaches(t *testing.T) {
	s := teamSettings(t)
	sp := s["a"]
	old := roleCache(t, Profile{AccountID: "999999999999", RoleName: "Old"}, "af-team")
	cur := roleCache(t, sp, "af-team")
	writeJSONAtomic(bindingPath("af-team"), keyBinding{Session: "af-team", Owner: "sign-in:old", StartURL: sp.StartURL,
		SSORegion: sp.SSORegion, Roles: [][2]string{{"999999999999", "Old"}}})
	if err := VetKeyOf(s, "a"); err != nil {
		t.Fatal(err)
	}
	if exists(old) || exists(cur) || exists(bindingPath("af-team")) {
		t.Fatal("role credentials taken for another owner survived with no token cached")
	}
}

// A leftover is kept when the member's config cannot be read: it may define that sso-session.
func TestUnreadableConfigCannotRevokeALeftover(t *testing.T) {
	p := fakePortal(t)
	s := teamSettings(t)
	sp := s["a"]
	token(t, "af-a", sp.StartURL, sp.SSORegion, "left-a")
	writeJSONAtomic(bindingPath("af-a"), keyBinding{Session: "af-a", Owner: "profile:pa", StartURL: sp.StartURL, SSORegion: sp.SSORegion})
	cfg, _ := os.ReadFile(ConfigPath())
	os.Remove(ConfigPath())
	if err := os.MkdirAll(ConfigPath(), 0o700); err != nil { // reading it fails
		t.Fatal(err)
	}
	forgetLeftovers([]Profile{sp}, "af-team")
	if !exists(ssoCachePath("af-a")) || strings.Contains(strings.Join(p.seen(), "\n"), "left-a") {
		t.Fatal("a leftover was revoked although the member's config could not be read")
	}
	os.Remove(ConfigPath())
	os.WriteFile(ConfigPath(), cfg, 0o600)
	forgetLeftovers([]Profile{sp}, "af-team")
	if exists(ssoCachePath("af-a")) {
		t.Fatal("control: a provable leftover is removed once the config reads")
	}
}

// A confirmation grant names the sign-in whose login the run uses: joining or switching one
// asks again. A profile in no sign-in keeps its earlier identity.
func TestGateIdentityIncludesTheSignIn(t *testing.T) {
	p := prof("a")
	p.ID = "pa"
	plain := identityOf(p, nil)
	if plain != hashParts("sso", p.ID, p.AccountID, p.RoleName, p.Region, p.StartURL, p.SSORegion) {
		t.Fatal("the identity of a profile in no sign-in changed")
	}
	g1, g2 := p, p
	g1.SignIn, g1.SignInID = "team", "g1"
	g2.SignIn, g2.SignInID = "team", "g2"
	if identityOf(g1, nil) == plain || identityOf(g1, nil) == identityOf(g2, nil) {
		t.Fatal("the sign-in is not part of the identity")
	}
	chain := Profile{ID: "pc", Name: "c", Kind: "assume_role", RoleARN: "arn:aws:iam::123456789012:role/R", SourceProfile: "a"}
	if identityOf(chain, map[string]Profile{"a": g1}) == identityOf(chain, map[string]Profile{"a": g2}) {
		t.Fatal("a chain's identity does not follow its source's sign-in")
	}
}

// The state answer and the expiry warning describe a token only while vetting would keep it.
func TestExpiryDoesNotDescribeAnUnusableToken(t *testing.T) {
	s := teamSettings(t)
	sp := s["a"]
	path := ssoCachePath("af-team")
	os.MkdirAll(filepath.Dir(path), 0o700)
	b, _ := json.Marshal(map[string]string{"accessToken": "t", "expiresAt": time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339),
		"startUrl": "https://other.awsapps.com/start", "region": sp.SSORegion})
	os.WriteFile(path, b, 0o600)
	if _, ok := keyExpiry(sp, s, UsableNow(), time.Now()); ok {
		t.Fatal("the end of another portal's token was reported")
	}
	rec := httptest.NewRecorder()
	HandleProfileLoginStates(rec, httptest.NewRequest("GET", "/aws-login/profiles", nil))
	var got profileLoginStatesWire
	json.Unmarshal(rec.Body.Bytes(), &got)
	for _, p := range got.Profiles {
		if p.ExpiresAt != "" || p.Expiring || p.State != loginStateNone {
			t.Fatalf("row %s describes an unusable token: %+v", p.Name, p)
		}
	}
	if len(got.SignIns) != 1 || got.SignIns[0].ExpiresAt != "" || got.SignIns[0].Expiring {
		t.Fatalf("sign-in row describes an unusable token: %+v", got.SignIns)
	}
}

// A shared hold taken after the token entered the refresh window is traded for an exclusive one.
func TestRefreshWindowIsRecheckedAfterWaitingForTheLock(t *testing.T) {
	s := teamSettings(t)
	sp := s["a"]
	token(t, "af-team", sp.StartURL, sp.SSORegion, "far") // an hour left
	held, err := logins.LockKey("af-team", false)
	if err != nil {
		t.Fatal(err)
	}
	var got []bool
	old := exportLockHook
	exportLockHook = func(shared bool) { got = append(got, shared) }
	t.Cleanup(func() { exportLockHook = old })
	done := make(chan struct{})
	go func() {
		defer close(done)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = exportLocked(awsRunner{bin: "/bin/false", ctx: ctx}, "a", "af-team")
	}()
	time.Sleep(100 * time.Millisecond) // the export waits for a shared hold
	b, _ := json.Marshal(map[string]string{"accessToken": "t", "expiresAt": time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339),
		"startUrl": sp.StartURL, "region": sp.SSORegion})
	os.WriteFile(ssoCachePath("af-team"), b, 0o600)
	held()
	<-done
	if len(got) != 1 || got[0] {
		t.Fatalf("holds taken = %v, want one exclusive", got)
	}
}
