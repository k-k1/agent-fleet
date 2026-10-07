package gcpx

import (
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/cloudlogin"
)

func stg() Profile {
	return Profile{ID: "id-2", Name: "stg", Label: "Stg", LoginMethod: LoginGoogle, Project: "stg-project"}
}

func ops() Profile {
	return Profile{ID: "id-3", Name: "ops", Label: "Ops", LoginMethod: LoginGoogle, Project: "ops-project"}
}

// addAccessToken caches an access token for account the way gcloud's access_tokens.db does.
func addAccessToken(t *testing.T, account string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(ConfigRoot(), "access_tokens.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS access_tokens (account_id TEXT PRIMARY KEY, access_token TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT OR REPLACE INTO access_tokens VALUES (?, ?)`, account, "ya"+"29."+randHex(t, 8)); err != nil {
		t.Fatal(err)
	}
}

func accessTokenRows(t *testing.T, account string) int {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(ConfigRoot(), "access_tokens.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM access_tokens WHERE account_id = ?`, account).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func addLegacy(t *testing.T, account string) string {
	t.Helper()
	dir := filepath.Join(ConfigRoot(), "legacy_credentials", account)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "adc.json"), []byte(`{"type":"authorized_user"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func (l *loginEnv) states(t *testing.T) map[string]string {
	t.Helper()
	_, out := l.do(t, "GET", "/gcp-login/profiles", "")
	m := map[string]string{}
	ps, _ := out["profiles"].([]any)
	for _, p := range ps {
		row := p.(map[string]any)
		m[row["name"].(string)] = row["state"].(string)
	}
	return m
}

// TestLogoutSignsOutTheAccount: the store is per account, so logging out prod signs out
// every profile selecting prod's account — stg by its login, prod by Settings — and leaves
// another account's profile signed in. The login's selection is cleared, Settings' is kept.
func TestLogoutSignsOutTheAccount(t *testing.T) {
	l := setupLogin(t, prod(), stg(), ops())
	root := ConfigRoot()
	setFakeAccount(root, ConfigName("stg"), "dev@example.com")
	setFakeAccount(root, ConfigName("ops"), "ops@example.com")
	for _, a := range []string{"dev@example.com", "ops@example.com"} {
		addCredential(t, a, "authorized_user")
		addAccessToken(t, a)
		if err := recordLogin(root, a); err != nil {
			t.Fatal(err)
		}
	}
	legacy := addLegacy(t, "dev@example.com")
	otherLegacy := addLegacy(t, "ops@example.com")
	if s := l.states(t); s["prod"] != loginStateSignedIn || s["stg"] != loginStateSignedIn || s["ops"] != loginStateSignedIn {
		t.Fatalf("before: %v", s)
	}

	code, out := l.do(t, "POST", "/gcp-login/profiles/prod/logout", "")
	if code != http.StatusOK || out["account"] != "dev@example.com" || strings.Join(toStrings(out["profiles"]), ",") != "prod,stg" {
		t.Fatalf("logout = %d %v", code, out)
	}
	if s := l.states(t); s["prod"] != loginStateNone || s["stg"] != loginStateNone || s["ops"] != loginStateSignedIn {
		t.Fatalf("after: %v", s)
	}
	if kind, _ := credentialType(root, "dev@example.com"); kind != "" {
		t.Fatalf("the credential is still there: %q", kind)
	}
	if kind, _ := credentialType(root, "ops@example.com"); kind != "authorized_user" {
		t.Fatalf("another account's credential went: %q", kind)
	}
	if accessTokenRows(t, "dev@example.com") != 0 || accessTokenRows(t, "ops@example.com") != 1 {
		t.Fatal("the access-token cache was not cleaned to the account")
	}
	if _, err := os.Stat(legacy); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy credentials left: %v", err)
	}
	if _, err := os.Stat(otherLegacy); err != nil {
		t.Fatalf("another account's legacy credentials went: %v", err)
	}
	if m := readLogins(root); m["dev@example.com"] != "" || m["ops@example.com"] == "" {
		t.Fatalf("login marks = %v", m)
	}
	if got := ConfiguredAccount("stg"); got != "" {
		t.Fatalf("the login's selection stayed: %q", got)
	}
	if got := ConfiguredAccount("prod"); got != "dev@example.com" {
		t.Fatalf("Settings' account went: %q", got)
	}
	// Both configurations are still exactly what a sync of their profile writes.
	for _, p := range []Profile{prod(), stg()} {
		if _, err := syncedAs(root, p); err != nil {
			t.Fatalf("%s after the logout: %v", p.Name, err)
		}
	}
	if _, _, _, err := PlanExec(l.gcloud, hostile(t), execOpts(l.env, prod())); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("a run after the logout: %v", err)
	}
	if !strings.Contains(l.log.String(), "gcp-login: logout profile=prod signed_out=2 relayed=true") {
		t.Fatalf("log: %s", l.log.String())
	}
	if strings.Contains(l.log.String(), "dev@example.com") {
		t.Fatal("the log names the account")
	}
	l.noSecretAnywhere(t)
}

func toStrings(v any) []string {
	var out []string
	xs, _ := v.([]any)
	for _, x := range xs {
		out = append(out, x.(string))
	}
	return out
}

// TestLogoutEndsAWaitingLogin: a login of a profile selecting the account, waiting for the
// member's code, ends with the logout, so a code pasted afterwards stores nothing.
func TestLogoutEndsAWaitingLogin(t *testing.T) {
	l := setupLogin(t, prod(), stg())
	addCredential(t, "dev@example.com", "authorized_user")
	id := l.start(t, "/gcp-login/profiles/prod/start?force=1")
	l.waitPhase(t, "prod", id, cloudlogin.PhaseAuthorize)
	if code, out := l.do(t, "POST", "/gcp-login/profiles/stg/logout", ""); code != http.StatusOK || len(toStrings(out["profiles"])) != 0 {
		t.Fatalf("stg selects nothing: %d %v", code, out)
	}
	if a := logins.Attempt(id); a == nil || a.View().Phase != cloudlogin.PhaseAuthorize {
		t.Fatal("a logout of another account ended the login")
	}
	if code, out := l.do(t, "POST", "/gcp-login/profiles/prod/logout", ""); code != http.StatusOK || strings.Join(toStrings(out["profiles"]), ",") != "prod" {
		t.Fatalf("logout = %d %v", code, out)
	}
	if a := logins.Attempt(id); a != nil && a.View().Phase == cloudlogin.PhaseAuthorize {
		t.Fatal("the login still waits for a code")
	}
	if c, _ := l.submit(t, "prod", id, l.code); c == http.StatusOK {
		t.Fatal("a code was taken after the logout")
	}
	if kind, _ := credentialType(ConfigRoot(), "dev@example.com"); kind != "" {
		t.Fatalf("credential = %q", kind)
	}
}

// TestLogoutRefusals: a name Settings does not define, and a root another login holds.
func TestLogoutRefusals(t *testing.T) {
	l := setupLogin(t, prod())
	addCredential(t, "dev@example.com", "authorized_user")
	if code, out := l.do(t, "POST", "/gcp-login/profiles/nope/logout", ""); code != http.StatusNotFound || errCode(out) != "not_a_settings_profile" {
		t.Fatalf("unknown profile: %d %v", code, out)
	}
	_, unlock, err := lockRoot()
	if err != nil {
		t.Fatal(err)
	}
	old := rootBusyWait
	rootBusyWait = 100 * time.Millisecond
	t.Cleanup(func() { rootBusyWait = old })
	code, out := l.do(t, "POST", "/gcp-login/profiles/prod/logout", "")
	unlock()
	if code != http.StatusConflict || errCode(out) != "busy" {
		t.Fatalf("held root: %d %v", code, out)
	}
	if kind, _ := credentialType(ConfigRoot(), "dev@example.com"); kind != "authorized_user" {
		t.Fatal("a refused logout removed the credential")
	}
}

func errCode(out map[string]any) string {
	e, _ := out["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}
