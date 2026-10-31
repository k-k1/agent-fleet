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

// A sync ends the attempts whose target Settings no longer names, a renamed (dropped) key's
// included, and leaves the others running.
func TestRenamedAttemptIsEndedBySyncVetting(t *testing.T) {
	s := teamSettings(t)
	sp := s["a"]
	keep := logins.Begin("af-team", "", "a", func() {})
	gone := logins.Begin("af-gone", "", "x", func() {})
	t.Cleanup(func() { keep.End(cloudlogin.PhaseFailed, ""); gone.End(cloudlogin.PhaseFailed, "") })
	r1, u1 := registerAttempt(teamTarget(sp), true)
	defer u1()
	attachAttempt(r1, keep)
	r2, u2 := registerAttempt(keyBinding{Session: "af-gone", Owner: "sign-in:g9", StartURL: sp.StartURL, SSORegion: sp.SSORegion}, true)
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
	if _, ok := registered(r2); ok {
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

// fakeLoginCLI is an aws whose `sso login` writes a token for sp's portal where the CLI's HOME
// says, touches written, and exits 0 once approve exists.
func fakeLoginCLI(t *testing.T, session string, sp Profile, access string) (bin, state string) {
	t.Helper()
	dir := t.TempDir()
	state = filepath.Join(dir, "state")
	os.MkdirAll(state, 0o700)
	tok, _ := json.Marshal(map[string]string{"accessToken": access, "expiresAt": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		"startUrl": sp.StartURL, "region": sp.SSORegion})
	base := filepath.Base(ssoCachePath(session))
	script := `#!/bin/sh
S="` + state + `"
[ "$1 $2" = "sso login" ] || exit 1
echo "Open https://device.sso.` + sp.SSORegion + `.amazonaws.com/?user_code=ABCD-EFGH"
mkdir -p "$HOME/.aws/sso/cache"
printf '%s' '` + string(tok) + `' > "$HOME/.aws/sso/cache/` + base + `"
touch "$S/written"
while [ ! -f "$S/approve" ]; do sleep 0.02; done
exit 0
`
	bin = filepath.Join(dir, "aws")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, state
}

func waitFile(t *testing.T, path string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !exists(path); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%s never appeared", path)
		}
	}
}

func waitExited(t *testing.T, a *cloudlogin.Attempt) {
	t.Helper()
	select {
	case <-a.Exited():
	case <-time.After(10 * time.Second):
		t.Fatal("the login CLI did not exit")
	}
}

// B1: an attempt's CLI writes its token where no reader looks; only the attempt's own check under
// the key lock publishes it. A token written for an owner Settings dropped meanwhile is never
// published, however the CLI ends.
func TestAttemptTokenIsStagedUntilBound(t *testing.T) {
	s := teamSettings(t)
	sp := s["a"]
	bin, state := fakeLoginCLI(t, "af-team", sp, "first")
	a, err := startLoginAttempt(bin, "af-team", "", sp)
	if err != nil {
		t.Fatal(err)
	}
	waitFile(t, filepath.Join(state, "written"))
	if exists(ssoCachePath("af-team")) {
		t.Fatal("the attempt's token was readable before it was bound")
	}
	os.WriteFile(filepath.Join(state, "approve"), nil, 0o600)
	waitExited(t, a)
	var b keyBinding
	if !exists(ssoCachePath("af-team")) || !readJSON(bindingPath("af-team"), &b) || b.Owner != "sign-in:g1" {
		t.Fatalf("control: an approved login is published and bound (%+v)", b)
	}

	// The key changes hands while a second login waits; the sync ends it.
	bin, state = fakeLoginCLI(t, "af-team", sp, "second")
	a, err = startLoginAttempt(bin, "af-team", "", sp)
	if err != nil {
		t.Fatal(err)
	}
	waitFile(t, filepath.Join(state, "written"))
	ps := []Profile{s["a"], s["b"]}
	for i := range ps {
		ps[i].SignInID = "g2"
	}
	saveSettingsCache(ps, nil)
	next := map[string]Profile{"a": ps[0], "b": ps[1]}
	if err := vetAll(next, UsableNow()); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(state, "approve"), nil, 0o600)
	waitExited(t, a)
	if got, _ := os.ReadFile(ssoCachePath("af-team")); strings.Contains(string(got), "second") {
		t.Fatal("a login for the former owner was published")
	}
	if accepted(t, next, "af-team") {
		t.Fatal("the new owner can use a token written for the former one")
	}
}

// B2: a run planned while Agent Fleet used the key stops once the member's own files take it.
func TestPlannedRunStopsWhenItsKeyBecomesMemberOwned(t *testing.T) {
	s := teamSettings(t)
	sp := s["a"]
	token(t, "af-team", sp.StartURL, sp.SSORegion, "tok")
	check, _ := plannedTarget(s, "af-team")
	if check == nil || check("af-team") != nil {
		t.Fatal("control: the planned key exports")
	}
	own := "[sso-session af-team]\nsso_start_url = https://mine.awsapps.com/start\nsso_region = us-east-1\n"
	b, _ := os.ReadFile(ConfigPath())
	os.WriteFile(ConfigPath(), append([]byte(own), b...), 0o600)
	if err := check("af-team"); !errors.Is(err, ErrSettingsChangedRun) {
		t.Fatalf("export after the member took the key = %v", err)
	}
}

// B3: a chain in no sign-in logs in through af-<source>, as the pane renders it; and a known
// owner mismatch is refused while the key is held back.
func TestSSMChainAndHeldBackKeysAreCheckedAgainstSettings(t *testing.T) {
	src := chainSrc()
	src.ID = "ps"
	ch := chainProf()
	ch.ID = "pd"
	s := teamSettings(t, src, ch)
	meta := session.SSMMeta{Profile: "deploy", RoleARN: ch.RoleARN, SourceProfile: "src", StartURL: src.StartURL,
		SSORegion: src.SSORegion, AccountID: src.AccountID, RoleName: src.RoleName, SSOOwnerID: "ps"}
	if err := vetSSMLogin(meta); err != nil {
		t.Fatalf("an unshared chain was refused: %v", err)
	}

	sp := s["a"]
	own := "[sso-session af-team]\nsso_start_url = https://mine.awsapps.com/start\nsso_region = us-east-1\n"
	b, _ := os.ReadFile(ConfigPath())
	os.WriteFile(ConfigPath(), append([]byte(own), b...), 0o600)
	stale := session.SSMMeta{Profile: "a", StartURL: sp.StartURL, SSORegion: sp.SSORegion, AccountID: sp.AccountID,
		RoleName: sp.RoleName, SSOSession: "af-team", SSOOwnerID: "old"}
	if err := vetSSMLogin(stale); !errors.Is(err, ErrSSMSettingsChanged) {
		t.Fatalf("another owner over a held-back key = %v", err)
	}
}

// B4: a sync that cannot retire the old portal's login publishes nothing and says so.
func TestSyncMustNotPublishAPortalWhenRetireFails(t *testing.T) {
	s := teamSettings(t)
	sp := s["a"]
	token(t, "af-team", sp.StartURL, sp.SSORegion, "old")
	if !accepted(t, s, "af-team") {
		t.Fatal("control: the current portal's login is kept")
	}
	if err := os.MkdirAll(filepath.Join(ssoCachePath("af-team")+".af-retired", "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	moved := []Profile{s["a"], s["b"]}
	for i := range moved {
		moved[i].StartURL = "https://other.awsapps.com/start"
	}
	body, _ := json.Marshal(map[string]any{"profiles": moved})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
	defer srv.Close()
	t.Setenv("AF_CP_BASE_URL", srv.URL)
	if _, err := Sync(); !errors.Is(err, ErrKeyRetireFailed) {
		t.Fatalf("Sync = %v", err)
	}
	if cfg, _ := os.ReadFile(ConfigPath()); strings.Contains(string(cfg), "other.awsapps.com") {
		t.Fatal("the new portal was published beside the old portal's login")
	}
}

// B5: a leftover is kept while any current Settings owner, usable or not, renders its key.
func TestLeftoverCannotBelongToAnyCurrentSettingsOwner(t *testing.T) {
	p := fakePortal(t)
	b := prof("c")
	b.ID, b.SignIn, b.SignInID = "pc", "a", "g3" // sign-in "a" renders af-a
	s := teamSettings(t, b)
	sp := s["a"]
	own := "[profile c]\nregion = us-east-1\n" // holds c back
	cfg, _ := os.ReadFile(ConfigPath())
	os.WriteFile(ConfigPath(), append([]byte(own), cfg...), 0o600)
	token(t, "af-a", sp.StartURL, sp.SSORegion, "left-a")
	writeJSONAtomic(bindingPath("af-a"), keyBinding{Session: "af-a", Owner: "profile:pa", StartURL: sp.StartURL, SSORegion: sp.SSORegion})
	forgetLeftovers([]Profile{sp}, "af-team")
	if !exists(ssoCachePath("af-a")) || strings.Contains(strings.Join(p.seen(), "\n"), "left-a") {
		t.Fatal("the login of a sign-in that owns the name now was revoked as a leftover")
	}
}

// B6: role credentials with neither a token nor a binding carry no portal: they go.
func TestLegacyRoleCacheCannotBeUsedWithoutTokenOrBinding(t *testing.T) {
	s := teamSettings(t)
	legacy := roleCache(t, s["a"], "af-team")
	if err := VetKeyOf(s, "a"); err != nil {
		t.Fatal(err)
	}
	if exists(legacy) {
		t.Fatal("role credentials of unknown portal survived")
	}
}

// ownTeamSession puts the member's own [sso-session af-team] for portal Y in front of the block,
// which holds back every member of the sign-in.
func ownTeamSession(t *testing.T, s map[string]Profile) {
	t.Helper()
	own := "[sso-session af-team]\nsso_start_url = https://mine.awsapps.com/start\nsso_region = us-east-1\n"
	b, _ := os.ReadFile(ConfigPath())
	if err := os.WriteFile(ConfigPath(), append([]byte(own), b...), 0o600); err != nil {
		t.Fatal(err)
	}
	// The next sync drops the block's own section, as a member's sso-session holds the members back.
	if res, err := Apply(ConfigPath(), []Profile{s["a"], s["b"]}); err != nil || len(res.SessionShadowed) != 2 {
		t.Fatalf("re-apply = %+v %v", res, err)
	}
}

// C1: a held-back key is only read, never vetted, but a pane, discovery or forced re-login is
// refused when it holds or is defined for another portal; the force deletes nothing.
func TestSSMHeldBackKeyCannotUseOrDropAnotherPortalsLogin(t *testing.T) {
	s := teamSettings(t)
	sp := s["a"]
	meta := session.SSMMeta{Profile: "a", StartURL: sp.StartURL, SSORegion: sp.SSORegion, AccountID: sp.AccountID,
		RoleName: sp.RoleName, SSOSession: "af-team", SSOOwnerID: "g1"}
	ownTeamSession(t, s)
	if err := vetSSMLogin(meta); !errors.Is(err, ErrSSMKeyHeldBack) {
		t.Fatalf("own section for another portal, no token: %v", err)
	}
	token(t, "af-team", "https://mine.awsapps.com/start", "us-east-1", "theirs")
	if err := vetSSMLogin(meta); !errors.Is(err, ErrSSMKeyHeldBack) {
		t.Fatalf("another portal's token: %v", err)
	}
	if err := forgetSSMLogin(meta); !errors.Is(err, ErrSSMKeyHeldBack) || !exists(ssoCachePath("af-team")) {
		t.Fatalf("forced re-login = %v, member's token kept %v", err, exists(ssoCachePath("af-team")))
	}
}

// C2: a binding that cannot be written publishes no token, so a later owner has nothing to adopt.
func TestLandingBindingWriteFailureCannotPublishTheToken(t *testing.T) {
	s := teamSettings(t)
	sp := s["a"]
	target := teamTarget(sp)
	staged := ssoCacheIn(t.TempDir(), "af-team")
	os.MkdirAll(filepath.Dir(staged), 0o700)
	b, _ := json.Marshal(map[string]string{"accessToken": "landed", "expiresAt": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		"startUrl": sp.StartURL, "region": sp.SSORegion})
	os.WriteFile(staged, b, 0o600)
	reg, unregister := registerAttempt(target, true)
	defer unregister()
	// The bindings directory refuses new files: nothing to retire, but the binding cannot be written.
	dir := filepath.Dir(bindingPath("af-team"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	os.Chmod(dir, 0o500)
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	if err := bindLanding(reg, "af-team", target, staged); err == nil {
		t.Fatal("control: the binding write failed")
	}
	if exists(ssoCachePath("af-team")) {
		t.Fatal("a token was published without its binding")
	}
	os.Chmod(dir, 0o700)
	next := map[string]Profile{}
	for k, v := range s {
		v.SignInID = "g2"
		next[k] = v
	}
	if accepted(t, next, "af-team") {
		t.Fatal("another owner adopted the failed landing")
	}
}

// C3: a landing started while Agent Fleet used the key is not published once the member's own
// files took it, and their token stays as it was.
func TestLandingCannotOverwriteASessionThatBecameMemberOwned(t *testing.T) {
	s := teamSettings(t)
	sp := s["a"]
	target := teamTarget(sp)
	staged := ssoCacheIn(t.TempDir(), "af-team")
	os.MkdirAll(filepath.Dir(staged), 0o700)
	b, _ := json.Marshal(map[string]string{"accessToken": "landed", "expiresAt": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		"startUrl": sp.StartURL, "region": sp.SSORegion})
	os.WriteFile(staged, b, 0o600)
	reg, unregister := registerAttempt(target, true)
	defer unregister()
	ownTeamSession(t, s)
	token(t, "af-team", "https://mine.awsapps.com/start", "us-east-1", "theirs")
	if err := bindLanding(reg, "af-team", target, staged); err == nil {
		t.Fatal("a landing was published over the member's own sso-session")
	}
	if got, _ := os.ReadFile(ssoCachePath("af-team")); !strings.Contains(string(got), "theirs") {
		t.Fatalf("the member's token was replaced: %s", got)
	}
}

// C1: a forced re-login never deletes what a held-back key holds, even when the member's own
// sso-session points at the same portal (the pane itself may still use it).
func TestForcedSSMReloginKeepsAHeldBackKeysLogin(t *testing.T) {
	s := teamSettings(t)
	sp := s["a"]
	own := "[sso-session af-team]\nsso_start_url = " + sp.StartURL + "\nsso_region = " + sp.SSORegion + "\n"
	b, _ := os.ReadFile(ConfigPath())
	os.WriteFile(ConfigPath(), append([]byte(own), b...), 0o600)
	// The next sync drops the block's own [sso-session af-team]: the member's holds the members back.
	if res, err := Apply(ConfigPath(), []Profile{s["a"], s["b"]}); err != nil || len(res.SessionShadowed) != 2 {
		t.Fatalf("re-apply = %+v %v", res, err)
	}
	token(t, "af-team", sp.StartURL, sp.SSORegion, "theirs")
	meta := session.SSMMeta{Profile: "a", StartURL: sp.StartURL, SSORegion: sp.SSORegion, AccountID: sp.AccountID,
		RoleName: sp.RoleName, SSOSession: "af-team", SSOOwnerID: "g1"}
	if err := vetSSMLogin(meta); err != nil {
		t.Fatalf("control: the same portal's held-back key opens: %v", err)
	}
	if err := forgetSSMLogin(meta); !errors.Is(err, ErrSSMKeyHeldBack) || !exists(ssoCachePath("af-team")) {
		t.Fatalf("forced re-login = %v, member's token kept %v", err, exists(ssoCachePath("af-team")))
	}
}

// D1: an attempt that started while the key was not Agent Fleet's is published only in the
// compatible case, the member's own definition matching Settings; another portal's own
// sso-session (which also makes the key unusable) keeps its token.
func TestUnusableAtStartIsNotTakenAsCompatible(t *testing.T) {
	s := teamSettings(t)
	sp := s["a"]
	target := teamTarget(sp)
	staged := ssoCacheIn(t.TempDir(), "af-team")
	os.MkdirAll(filepath.Dir(staged), 0o700)
	b, _ := json.Marshal(map[string]string{"accessToken": "landed", "expiresAt": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		"startUrl": sp.StartURL, "region": sp.SSORegion})
	os.WriteFile(staged, b, 0o600)
	ownTeamSession(t, s)
	token(t, "af-team", "https://mine.awsapps.com/start", "us-east-1", "theirs")
	if _, err := startLoginAttempt("/bin/false", "af-team", "", sp); err == nil {
		t.Fatal("a login started over another portal's own sso-session")
	}
	reg, unregister := registerAttempt(target, false)
	defer unregister()
	if err := bindLanding(reg, "af-team", target, staged); err == nil {
		t.Fatal("a landing was published over another portal's own sso-session")
	}
	if got, _ := os.ReadFile(ssoCachePath("af-team")); !strings.Contains(string(got), "theirs") {
		t.Fatalf("the member's token was replaced: %s", got)
	}

	// The compatible case: the member's own section is Settings' portal.
	cfg, _ := os.ReadFile(ConfigPath())
	same := strings.NewReplacer("https://mine.awsapps.com/start", sp.StartURL, "us-east-1", sp.SSORegion).Replace(string(cfg))
	os.WriteFile(ConfigPath(), []byte(same), 0o600)
	os.Rename(ssoCachePath("af-team"), ssoCachePath("af-team")+".theirs")
	if err := bindLanding(reg, "af-team", target, staged); err != nil {
		t.Fatalf("control: the compatible case publishes: %v", err)
	}
}

// D1 through the real route: the member's own sso-session for another portal, with its token,
// appears after HandleLoginStart checked the request and before the attempt registers (while
// the CLI is being found). The staged login must not replace the member's token.
func TestPendingStartCannotTakeAnotherPortalAsCompatible(t *testing.T) {
	s := teamSettings(t)
	sp := s["a"]
	req, _, err := logins.FileCancel("a", "af-team", CacheState{}, LoginWaiter{Profile: "a", Target: teamTarget(sp).id()}, nil, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	bin, state := fakeLoginCLI(t, "af-team", sp, "landed")
	os.WriteFile(filepath.Join(state, "approve"), nil, 0o600) // the CLI exits 0 as soon as it wrote
	old := LoginAWSBin
	LoginAWSBin = func() (string, error) {
		ownTeamSession(t, s)
		token(t, "af-team", "https://mine.awsapps.com/start", "us-east-1", "theirs")
		return bin, nil
	}
	t.Cleanup(func() { LoginAWSBin = old })
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/aws-login/"+req.ID+"/start", nil)
	r.SetPathValue("id", req.ID)
	HandleLoginStart(rec, r)
	if a := logins.Current("af-team"); a != nil {
		waitExited(t, a)
	}
	if got, _ := os.ReadFile(ssoCachePath("af-team")); !strings.Contains(string(got), "theirs") {
		t.Fatalf("start = %d; the member's token was replaced: %s", rec.Code, got)
	}
	if rec.Code == http.StatusOK {
		t.Fatalf("a login started over another portal's own sso-session: %s", rec.Body.String())
	}
}
