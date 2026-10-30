package awsx

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Shared IAM Identity Center sign-ins on the Agent (ADR 0125 decisions 4, 5 and 10).

func member(name, id string) Profile {
	p := prof(name)
	p.ID, p.SignIn, p.SignInID = id, "team", "g1"
	return p
}

// teamSettings is a sign-in "team" with members a and b, saved as the Settings cache, and the
// managed block written from it.
func teamSettings(t *testing.T, extra ...Profile) map[string]Profile {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_AWS_PROFILES_TOKEN", "test-token")
	ps := append([]Profile{member("a", "pa"), member("b", "pb")}, extra...)
	if err := saveSettingsCache(ps, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(ConfigPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(ConfigPath(), ps); err != nil {
		t.Fatal(err)
	}
	m := map[string]Profile{}
	for _, p := range ps {
		m[p.Name] = p
	}
	return m
}

// token writes a cached login for session, for the portal given.
func token(t *testing.T, session, startURL, region, access string) {
	t.Helper()
	path := ssoCachePath(session)
	os.MkdirAll(filepath.Dir(path), 0o700)
	b, _ := json.Marshal(map[string]string{"accessToken": access, "refreshToken": "rt-" + access, "clientId": "c",
		"clientSecret": "s", "expiresAt": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		"registrationExpiresAt": time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
		"startUrl":              startURL, "region": region})
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func roleCache(t *testing.T, p Profile, session string) string {
	t.Helper()
	path := ssoRoleCachePath(p.AccountID, p.RoleName, session)
	os.MkdirAll(filepath.Dir(path), 0o700)
	if err := os.WriteFile(path, []byte(`{"ProviderType":"sso"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func names(s map[string]Profile) []string {
	out := make([]string, 0, len(s))
	for n := range s {
		out = append(out, n)
	}
	return out
}

// accepted vets key against s with every profile of s usable, and reports whether its token is
// still where a reader finds it.
func accepted(t *testing.T, s map[string]Profile, key string) bool {
	t.Helper()
	if err := vetKey(s, names(s), key); err != nil {
		t.Fatal(err)
	}
	return exists(ssoCachePath(key))
}

// Decision 4: the members of a sign-in share one [sso-session af-<sign-in>], written once; the
// member's own [sso-session af-<sign-in>] holds back every member, their own [profile x] only
// x; a sign-in named like a profile with a login of its own exports neither.
func TestRenderSharedSignIn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	res, err := Apply(path, []Profile{member("a", "pa"), member("b", "pb"), prof("solo")})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	cfg := string(b)
	if strings.Count(cfg, "[sso-session af-team]") != 1 || strings.Count(cfg, "sso_session = af-team") != 2 {
		t.Fatalf("shared section not written once:\n%s", cfg)
	}
	if !strings.Contains(cfg, "[sso-session af-solo]") || strings.Contains(cfg, "[sso-session af-a]") {
		t.Fatalf("a profile with a login of its own changed:\n%s", cfg)
	}
	if len(res.Exported) != 3 {
		t.Fatalf("exported %v", res.Exported)
	}

	os.WriteFile(path, []byte("[sso-session af-team]\nsso_region = us-east-1\n"), 0o600)
	res, _ = Apply(path, []Profile{member("a", "pa"), member("b", "pb")})
	if len(res.Exported) != 0 || len(res.SessionShadowed) != 2 {
		t.Fatalf("own [sso-session af-team]: exported %v, session-shadowed %v", res.Exported, res.SessionShadowed)
	}

	os.WriteFile(path, []byte("[profile a]\nregion = us-east-1\n"), 0o600)
	res, _ = Apply(path, []Profile{member("a", "pa"), member("b", "pb")})
	b, _ = os.ReadFile(path)
	if !slices.Contains(res.Shadowed, "a") || !slices.Equal(res.Exported, []string{"b"}) || !strings.Contains(string(b), "[sso-session af-team]") {
		t.Fatalf("own [profile a]: %+v\n%s", res, b)
	}

	x := prof("team")
	x.ID = "px"
	res, _ = Apply(filepath.Join(t.TempDir(), "config"), []Profile{member("a", "pa"), x})
	if len(res.Exported) != 0 || len(res.SessionConflict) != 2 {
		t.Fatalf("a sign-in named like a profile: exported %v, conflict %v", res.Exported, res.SessionConflict)
	}
}

// Decision 10: an unbound token for the current portal is adopted; one for another portal, or
// bound to another owner, is retired with every member's role credentials; the one legitimate
// hand-over (a member that becomes a sign-in of its own name) keeps the login.
func TestVetKeyBindsAdoptsAndRetires(t *testing.T) {
	s := teamSettings(t)
	sp := s["a"]
	token(t, "af-team", sp.StartURL, sp.SSORegion, "tok1")
	if !accepted(t, s, "af-team") {
		t.Fatal("an unbound token of the current portal was not adopted")
	}
	var b keyBinding
	if !readJSON(bindingPath("af-team"), &b) || b.Owner != "sign-in:g1" {
		t.Fatalf("binding = %+v", b)
	}
	if st := keyLoginState(sp, s, names(s), time.Now()); st != loginStateSignedIn {
		t.Fatalf("state = %s", st)
	}

	// The portal changes in Settings: the token is for the old one.
	moved := map[string]Profile{}
	for k, v := range s {
		v.StartURL = "https://other.awsapps.com/start"
		moved[k] = v
	}
	ra, rb := roleCache(t, s["a"], "af-team"), roleCache(t, s["b"], "af-team")
	if st := keyLoginState(moved["a"], moved, names(moved), time.Now()); st != loginStateNone {
		t.Fatalf("state after a portal change = %s", st)
	}
	if !exists(ssoCachePath("af-team")) {
		t.Fatal("a read moved the token")
	}
	if accepted(t, moved, "af-team") {
		t.Fatal("a token for another portal was accepted")
	}
	if exists(ssoCachePath("af-team")) || exists(ra) || exists(rb) || !exists(ssoCachePath("af-team")+".af-retired") {
		t.Fatal("the token or a member's role credentials were left where a reader finds them")
	}

	// Another owner under the same key and portal (a name freed and taken).
	token(t, "af-team", sp.StartURL, sp.SSORegion, "tok2")
	writeJSONAtomic(bindingPath("af-team"), keyBinding{Session: "af-team", Owner: "sign-in:old", StartURL: sp.StartURL, SSORegion: sp.SSORegion})
	if accepted(t, s, "af-team") {
		t.Fatal("a token bound to another owner was accepted")
	}

	// Hand-over: profile "solo" (bound, signed in) becomes the member of a sign-in named solo.
	solo := prof("solo")
	solo.ID = "ps"
	token(t, "af-solo", solo.StartURL, solo.SSORegion, "tok3")
	if !accepted(t, map[string]Profile{"solo": solo}, "af-solo") {
		t.Fatal("control: solo's own login")
	}
	grouped := solo
	grouped.SignIn, grouped.SignInID = "solo", "g2"
	other := prof("solo-2")
	other.ID, other.SignIn, other.SignInID = "ps2", "solo", "g2"
	after := map[string]Profile{"solo": grouped, "solo-2": other}
	if !accepted(t, after, "af-solo") {
		t.Fatal("the member's login did not carry over to the sign-in of its name")
	}
	if !readJSON(bindingPath("af-solo"), &b) || b.Owner != "sign-in:g2" {
		t.Fatalf("hand-over binding = %+v", b)
	}
	// A profile that was never that member hands nothing over.
	writeJSONAtomic(bindingPath("af-solo"), keyBinding{Session: "af-solo", Owner: "profile:someone-else", StartURL: solo.StartURL, SSORegion: solo.SSORegion})
	if accepted(t, after, "af-solo") {
		t.Fatal("another profile's token was handed over")
	}
}

// A login attempt binds only the owner and portal it started for: Settings changing during
// the login fails the attempt and retires what it wrote, so the key's next owner cannot adopt it.
func TestLandingBindsOnlyItsTarget(t *testing.T) {
	s := teamSettings(t)
	sp := s["a"]
	target := keyBinding{Session: "af-team", Owner: sp.TokenOwner(), StartURL: sp.StartURL, SSORegion: sp.SSORegion}
	token(t, "af-team", sp.StartURL, sp.SSORegion, "tok")
	if !bindLanding("af-team", target) {
		t.Fatal("control: an unchanged target binds")
	}
	os.Remove(bindingPath("af-team"))
	moved := sp
	moved.StartURL = "https://other.awsapps.com/start"
	if err := saveSettingsCache([]Profile{moved, s["b"]}, nil); err != nil {
		t.Fatal(err)
	}
	if bindLanding("af-team", target) || exists(bindingPath("af-team")) {
		t.Fatal("a landing bound after Settings changed its portal")
	}
}

// Decision 5: logging out of a member logs out the sign-in, removes every member's role
// credentials, and reports them; a member's leftover per-profile token is revoked and removed
// only when it is provably theirs.
func TestLogoutCoversTheSignInAndProvableLeftovers(t *testing.T) {
	p := fakePortal(t)
	solo := prof("c")
	solo.ID = "pc"
	s := teamSettings(t, solo)
	sp := s["a"]
	token(t, "af-team", sp.StartURL, sp.SSORegion, "shared-secret")
	ra, rb, rc := roleCache(t, s["a"], "af-team"), roleCache(t, s["b"], "af-team"), roleCache(t, solo, "af-c")
	// a joined from a login of its own (bound to it); b's leftover is not provably b's.
	token(t, "af-a", sp.StartURL, sp.SSORegion, "left-a")
	writeJSONAtomic(bindingPath("af-a"), keyBinding{Session: "af-a", Owner: "profile:pa", StartURL: sp.StartURL, SSORegion: sp.SSORegion})
	token(t, "af-b", sp.StartURL, sp.SSORegion, "left-b")

	rec, out := profileLogout(t, "a")
	if rec.Code != http.StatusOK || !slices.Equal(out.Profiles, []string{"a", "b"}) {
		t.Fatalf("logout = %d %s", rec.Code, rec.Body.String())
	}
	if exists(ssoCachePath("af-team")) || exists(ra) || exists(rb) {
		t.Fatal("the sign-in's token or a member's role credentials survived")
	}
	if !exists(rc) {
		t.Fatal("another profile's role credentials were removed")
	}
	if exists(ssoCachePath("af-a")) {
		t.Fatal("a's provable leftover survived")
	}
	if !exists(ssoCachePath("af-b")) {
		t.Fatal("b's unbound leftover was removed")
	}
	calls := strings.Join(p.seen(), "\n")
	if !strings.Contains(calls, "shared-secret") || !strings.Contains(calls, "left-a") || strings.Contains(calls, "left-b") {
		t.Fatalf("revoked:\n%s", calls)
	}
}

// No token reaches an answer of the routes that read the cache.
func TestSignInRoutesNeverCarryAToken(t *testing.T) {
	s := teamSettings(t)
	sp := s["a"]
	token(t, "af-team", sp.StartURL, sp.SSORegion, "zq-sentinel-access")
	accepted(t, s, "af-team")
	rec := httptest.NewRecorder()
	HandleProfileLoginStates(rec, httptest.NewRequest("GET", "/aws-login/profiles", nil))
	body := rec.Body.String()
	if strings.Contains(body, "zq-sentinel") || strings.Contains(body, "rt-zq") {
		t.Fatalf("a token reached the state answer: %s", body)
	}
	var got profileLoginStatesWire
	json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.SignIns) != 1 || got.SignIns[0].Name != "team" || got.SignIns[0].State != loginStateSignedIn ||
		!slices.Equal(got.SignIns[0].Profiles, []string{"a", "b"}) {
		t.Fatalf("sign-ins = %+v", got.SignIns)
	}
}

// Decision 5: an export takes the key's lock exclusively near a refresh, shared otherwise.
func TestExportLockIsExclusiveNearARefresh(t *testing.T) {
	s := teamSettings(t)
	sp := s["a"]
	held, err := logins.LockKey("af-team", true)
	if err != nil {
		t.Fatal(err)
	}
	defer held()
	wait := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		_, err := exportLocked(awsRunner{bin: "/bin/false", ctx: ctx}, "a", "af-team")
		return err
	}
	token(t, "af-team", sp.StartURL, sp.SSORegion, "tok") // an hour left: no refresh
	if err := wait(); err != nil && strings.Contains(err.Error(), "stopped") {
		t.Fatalf("an export far from a refresh waited for a shared holder: %v", err)
	}
	path := ssoCachePath("af-team")
	b, _ := json.Marshal(map[string]string{"accessToken": "t", "expiresAt": time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339),
		"startUrl": sp.StartURL, "region": sp.SSORegion})
	os.WriteFile(path, b, 0o600)
	if err := wait(); err == nil || !strings.Contains(err.Error(), "stopped") {
		t.Fatalf("an export near a refresh did not wait for the shared holder: %v", err)
	}
}

// The SSM pane's forced re-login drops the shared token and every member's role credentials,
// and leaves another login alone.
func TestForcedSSMReloginDropsTheSharedLogin(t *testing.T) {
	solo := prof("c")
	solo.ID = "pc"
	s := teamSettings(t, solo)
	sp := s["a"]
	token(t, "af-team", sp.StartURL, sp.SSORegion, "tok")
	token(t, "af-c", solo.StartURL, solo.SSORegion, "other")
	ra, rb := roleCache(t, s["a"], "af-team"), roleCache(t, s["b"], "af-team")
	err := forgetSSMLogin(session.SSMMeta{Profile: "a", StartURL: sp.StartURL, SSORegion: sp.SSORegion, AccountID: sp.AccountID,
		RoleName: sp.RoleName, SSOSession: "af-team"})
	if err != nil {
		t.Fatal(err)
	}
	if exists(ssoCachePath("af-team")) || exists(ra) || exists(rb) {
		t.Fatal("the shared login or a member's role credentials survived the forced re-login")
	}
	if !exists(ssoCachePath("af-c")) {
		t.Fatal("another login was dropped")
	}
}

// The conflict shape the Control Plane reports for a sign-in collision (every left-out profile
// by its own name) is refused before the CLI starts, also from a block an earlier sync left.
func TestConflictedMemberIsRefusedBeforeTheCLI(t *testing.T) {
	bin, state := fakeAWS(t, ssoProfile)
	os.WriteFile(filepath.Join(state, "loggedIn"), nil, 0o600)
	_, _, _, err := PlanExec(bin, workloadEnv, ExecOptions{Profile: "prod", Account: "123456789012", Login: "never",
		Argv: []string{"true"}, Quiet: true, Conflicts: []Conflict{{Name: "prod", Labels: []string{"prod", "team"}}}})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("PlanExec = %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(state, "calls")); len(b) != 0 {
		t.Fatalf("the CLI ran %d times", strings.Count(string(b), "\n"))
	}
}
