package gcpx

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestMain clears AF_CP_INTERNAL_URL: a workspace that runs these tests may itself carry
// it, and cpurl.Request would then send the tests' requests past their fake CP.
func TestMain(m *testing.M) {
	_ = os.Unsetenv("AF_CP_INTERNAL_URL")
	os.Exit(m.Run())
}

// randHex builds secret-shaped fixtures at run time, never as literals.
func randHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// env is one test's scratch HOME with a member-owned ~/.config/gcloud that nothing may
// touch, and a fake gcloud that records every call.
type env struct {
	home, memberGcloud, fakeDir, gcloud, token string
	memberBefore                               []string
}

const fakeGcloud = `#!/bin/sh
D=__DIR__
{ echo "ARGS $*"; env | sort; echo "END"; } >> "$D/calls"
# Real gcloud writes a DEBUG log under the config root unless file logging is off, and the
# token lands in it (ADR 0107 decision 1, measured).
if [ "$CLOUDSDK_CORE_DISABLE_FILE_LOGGING" != true ]; then
  mkdir -p "$CLOUDSDK_CONFIG/logs"; cat "$D/token" >> "$CLOUDSDK_CONFIG/logs/gcloud.log"
fi
for a; do last=$a; done
case "$1 $2" in
"config config-helper")
  if [ -f "$D/fail" ]; then cat "$D/fail" >&2; exit 1; fi
  if [ -f "$D/fail-sticky" ]; then cat "$D/fail-sticky" >&2; exit 1; fi
  if [ -f "$D/stdout" ]; then cat "$D/stdout"; exit 0; fi
  tok=$(cat "$D/token")
  # An access-token override beats the configuration in real gcloud.
  if [ -n "$CLOUDSDK_AUTH_ACCESS_TOKEN_FILE" ]; then tok=$(cat "$CLOUDSDK_AUTH_ACCESS_TOKEN_FILE"); fi
  printf '{"credential":{"access_token":"%s","token_expiry":"%s"}}\n' "$tok" "$(cat "$D/expiry")"
  ;;
"auth login")
  # Whether the wrapper holds the root's lock while gcloud writes the configuration.
  if command -v flock >/dev/null 2>&1; then
    if flock -n "$CLOUDSDK_CONFIG/.agent-fleet.lock" true; then echo unlocked > "$D/lockstate"; else echo locked > "$D/lockstate"; fi
  fi
  case "$3" in --*) acct=$(cat "$D/login-account");; *) acct=$3;; esac
  echo "You are now logged in as [$acct]." >&2
  # gcloud rewrites the file in its own layout: the comment goes, keys move (measured).
  sed -i -e '/^#/d' -e "s/^\[core\]\$/[core]\naccount = $acct/" "$CLOUDSDK_CONFIG/configurations/config_$last"
  # Like gcloud 587.0.0: without --force a login for an account whose cached access token
  # still has time left reuses it and signs nobody in, so a rejected refresh stays rejected.
  case " $* " in *" --force "*) rm -f "$D/fail";; esac
  ;;
*) echo "fake gcloud: unexpected $*" >&2; exit 9;;
esac
`

func setup(t *testing.T) *env {
	t.Helper()
	e := &env{home: t.TempDir(), fakeDir: t.TempDir()}
	t.Setenv("HOME", e.home)
	t.Setenv("AF_CP_BASE_URL", "")
	t.Setenv("AF_GCP_PROFILES_TOKEN", "")
	// The member's own gcloud directory, with files the Agent must neither read nor change.
	e.memberGcloud = filepath.Join(e.home, ".config", "gcloud")
	if err := os.MkdirAll(filepath.Join(e.memberGcloud, "configurations"), 0o700); err != nil {
		t.Fatal(err)
	}
	for f, body := range map[string]string{
		"active_config":                        "mine\n",
		"configurations/config_mine":           "[core]\naccount = member@example.com\nproject = member-project\n",
		"application_default_credentials.json": `{"type":"authorized_user"}`,
		"configurations/config_af-prod":        "[core]\nproject = not-the-agents\n",
	} {
		if err := os.WriteFile(filepath.Join(e.memberGcloud, f), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	e.memberBefore = snapshot(t, e.memberGcloud)
	e.token = "ya" + "29." + randHex(t, 24)
	e.write(t, "token", e.token)
	e.write(t, "expiry", time.Now().Add(55*time.Minute).UTC().Format(time.RFC3339))
	e.gcloud = filepath.Join(e.fakeDir, "gcloud")
	if err := os.WriteFile(e.gcloud, []byte(strings.ReplaceAll(fakeGcloud, "__DIR__", e.fakeDir)), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", e.fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Cleanup(func() {
		if got := snapshot(t, e.memberGcloud); strings.Join(got, "\n") != strings.Join(e.memberBefore, "\n") {
			t.Errorf("the member's ~/.config/gcloud changed:\nbefore %v\nafter  %v", e.memberBefore, got)
		}
	})
	return e
}

func (e *env) write(t *testing.T, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.fakeDir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// snapshot lists every file under dir with its content and mtime.
func snapshot(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		b, _ := os.ReadFile(p)
		out = append(out, p+" "+fi.ModTime().String()+" "+string(b))
		return nil
	})
	sort.Strings(out)
	return out
}

// calls returns the fake gcloud's recorded invocations: args and environment.
func (e *env) calls(t *testing.T) []struct {
	args string
	env  map[string]string
} {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(e.fakeDir, "calls"))
	var out []struct {
		args string
		env  map[string]string
	}
	for _, block := range strings.Split(string(b), "END\n") {
		lines := strings.Split(strings.TrimSpace(block), "\n")
		if len(lines) == 0 || !strings.HasPrefix(lines[0], "ARGS ") {
			continue
		}
		c := struct {
			args string
			env  map[string]string
		}{args: strings.TrimPrefix(lines[0], "ARGS "), env: map[string]string{}}
		for _, l := range lines[1:] {
			if k, v, ok := strings.Cut(l, "="); ok {
				c.env[k] = v
			}
		}
		out = append(out, c)
	}
	return out
}

// addCredential stores a credential of kind for account in the Agent's store, as gcloud's
// credentials.db holds it.
func addCredential(t *testing.T, account, kind string) {
	t.Helper()
	root := ConfigRoot()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(root, "credentials.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS credentials (account_id TEXT PRIMARY KEY, value BLOB)`); err != nil {
		t.Fatal(err)
	}
	v, _ := json.Marshal(map[string]string{"type": kind, "refresh_token": randHex(t, 16)})
	if _, err := db.Exec(`INSERT OR REPLACE INTO credentials VALUES (?, ?)`, account, string(v)); err != nil {
		t.Fatal(err)
	}
}

func prod() Profile {
	return Profile{ID: "id-1", Name: "prod", Label: "Prod", LoginMethod: LoginGoogle, Project: "prod-project",
		QuotaProject: "billing-project", Account: "dev@example.com", Region: "asia-northeast1", Zone: "asia-northeast1-a"}
}

func configText(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(ConfigRoot(), "configurations", "config_"+ConfigName(name)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func mustApply(t *testing.T, ps ...Profile) SyncResult {
	t.Helper()
	res, err := Apply(ps)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestSyncPullsIntoTheAgentsOwnRoot(t *testing.T) {
	e := setup(t)
	bridgeTok := "afg_" + randHex(t, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/gcp-profiles" || r.Header.Get("Authorization") != "Bearer "+bridgeTok {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"profiles": []Profile{prod()},
			"conflicts": []Conflict{{Name: "dup", Labels: []string{"Dup", "dup"}}}})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("AF_CP_BASE_URL", srv.URL)
	t.Setenv("AF_GCP_PROFILES_TOKEN", bridgeTok)

	res, err := Sync()
	if err != nil {
		t.Fatal(err)
	}
	if !res.Fetched || res.Exported["prod"].Project != "prod-project" || len(res.Conflicts) != 1 {
		t.Fatalf("sync result %+v", res)
	}
	got := configText(t, "prod")
	for _, want := range []string{"account = dev@example.com", "project = prod-project", "quota_project = billing-project",
		"region = asia-northeast1", "zone = asia-northeast1-a"} {
		if !strings.Contains(got, want) {
			t.Errorf("config_af-prod lacks %q:\n%s", want, got)
		}
	}
	if !strings.HasPrefix(ConfigRoot(), filepath.Join(e.home, ".local", "state", "agent-fleet")) {
		t.Errorf("root %s is not under the Agent's state dir", ConfigRoot())
	}
	if _, err := os.Stat(filepath.Join(ConfigRoot(), "active_config")); err == nil {
		t.Error("the sync activated a configuration")
	}
	// The CP cannot be asked: the cache, bound to the token, is applied instead.
	srv.Close()
	if res, err := Sync(); err == nil || !res.FromCache || res.Exported["prod"].Name == "" {
		t.Fatalf("offline sync: %+v %v", res, err)
	}
	t.Setenv("AF_GCP_PROFILES_TOKEN", "afg_"+randHex(t, 8))
	if _, _, ok := CachedSettings(); ok {
		t.Error("another membership's token read the cache")
	}
}

func TestSyncOwnershipRules(t *testing.T) {
	setup(t)
	p := prod()
	p.Account = ""
	p.ImpersonateServiceAccount = "deployer@prod-project.iam.gserviceaccount.com"
	mustApply(t, p)
	if ConfiguredAccount("prod") != "" {
		t.Fatal("a profile without an account got one")
	}
	// A login writes core/account, and someone adds a property by hand.
	cfg := filepath.Join(ConfigRoot(), "configurations", "config_af-prod")
	login := strings.Replace(configText(t, "prod"), "[core]\n", "[core]\naccount = picked@example.com\nverbosity = debug\n", 1) +
		"\n[proxy]\naddress = 10.0.0.1\n"
	if err := os.WriteFile(cfg, []byte(login), 0o600); err != nil {
		t.Fatal(err)
	}
	// An edit that does not touch the selection keeps the login's account and drops the rest.
	p.Project = "prod-project-2"
	mustApply(t, p)
	got := configText(t, "prod")
	if !strings.Contains(got, "account = picked@example.com") || !strings.Contains(got, "project = prod-project-2") {
		t.Fatalf("the login-owned account or the Settings edit was lost:\n%s", got)
	}
	if strings.Contains(got, "verbosity") || strings.Contains(got, "proxy") {
		t.Fatalf("a property Settings does not own survived the sync:\n%s", got)
	}
	if !strings.Contains(got, "impersonate_service_account = deployer@prod-project.iam.gserviceaccount.com") {
		t.Fatalf("impersonation missing:\n%s", got)
	}
	// The profile recreated (another id): the selection is reset.
	p.ID = "id-2"
	mustApply(t, p)
	if ConfiguredAccount("prod") != "" {
		t.Fatal("a recreated profile kept the old login's account")
	}
	// Settings names an account, then stops naming it: the Settings account goes with it.
	p.Account = "named@example.com"
	mustApply(t, p)
	if !strings.Contains(configText(t, "prod"), "account = named@example.com") {
		t.Fatal("the Settings account was not written")
	}
	p.Account = ""
	mustApply(t, p)
	if ConfiguredAccount("prod") != "" {
		t.Fatal("the account Settings no longer names stayed selected")
	}
	// The login method changes: reset too.
	if err := os.WriteFile(cfg, []byte(strings.Replace(configText(t, "prod"), "[core]\n", "[core]\naccount = again@example.com\n", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	mustApply(t, p)
	if !strings.Contains(configText(t, "prod"), "account = again@example.com") {
		t.Fatal("login account lost without a change")
	}
	q := p
	q.LoginMethod = "workforce"
	res := mustApply(t, q)
	if res.Invalid["prod"] == "" {
		t.Fatal("an unsupported login method was exported")
	}
	if _, err := os.Stat(cfg); err == nil {
		t.Fatal("the configuration of a profile no longer exported stayed")
	}
	mustApply(t, p)
	if ConfiguredAccount("prod") != "" {
		t.Fatal("the account survived a login-method change")
	}
}

func TestSyncRemovesGoneAndRefusesInvalid(t *testing.T) {
	setup(t)
	stage := prod()
	stage.ID, stage.Name = "id-s", "stage"
	mustApply(t, prod(), stage)
	dir := filepath.Join(ConfigRoot(), "configurations")
	if err := os.WriteFile(filepath.Join(dir, "config_default"), []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ConfigRoot(), "af-stage_configs.db"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	bad := []Profile{
		{ID: "a", Name: "Prod", LoginMethod: LoginGoogle, Project: "prod-project"},
		{ID: "b", Name: "x_y", LoginMethod: LoginGoogle, Project: "prod-project"},
		{ID: "c", Name: "inj", LoginMethod: LoginGoogle, Project: "prod-project\n[auth]\naccess_token_file = /x"},
		{ID: "d", Name: "inj2", LoginMethod: LoginGoogle, Project: "prod-project", Account: "a@b.co\naccess_token_file=/x"},
		{ID: "e", Name: "inj3", LoginMethod: LoginGoogle, Project: "prod-project", Region: "us-east1%(x)s"},
	}
	res := mustApply(t, append([]Profile{prod()}, bad...)...)
	if len(res.Invalid) != len(bad) {
		t.Fatalf("invalid = %v", res.Invalid)
	}
	ents, _ := os.ReadDir(dir)
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	if strings.Join(names, ",") != "config_af-prod,config_default" {
		t.Fatalf("configurations = %v", names)
	}
	if _, err := os.Stat(filepath.Join(ConfigRoot(), "af-stage_configs.db")); err == nil {
		t.Error("the removed configuration's cache stayed")
	}
}

func execOpts(e *env, p Profile) ExecOptions {
	return ExecOptions{Profile: p.Name, Project: p.Project, Login: "never", Argv: []string{"true"},
		Settings: map[string]Profile{p.Name: p}, Stderr: &strings.Builder{}}
}

// hostile is a caller environment that tries every way of handing gcloud or the child
// another identity, config root or metadata server.
func hostile(t *testing.T) []string {
	other := filepath.Join(t.TempDir(), "other-token")
	_ = os.WriteFile(other, []byte("ya"+"29.inherited-"+randHex(t, 8)), 0o600)
	return []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "KEEP_ME=1",
		"CLOUDSDK_AUTH_ACCESS_TOKEN_FILE=" + other,
		"CLOUDSDK_CONFIG=" + filepath.Join(os.Getenv("HOME"), ".config", "gcloud"),
		"CLOUDSDK_CORE_DISABLE_FILE_LOGGING=false",
		"CLOUDSDK_CORE_CHECK_GCE_METADATA=true",
		"CLOUDSDK_CORE_ACCOUNT=member@example.com",
		"GOOGLE_APPLICATION_CREDENTIALS=" + filepath.Join(os.Getenv("HOME"), ".config", "gcloud", "application_default_credentials.json"),
		"GOOGLE_OAUTH_ACCESS_TOKEN=inherited",
		"GCLOUD_PROJECT=member-project",
		"GCE_METADATA_HOST=127.0.0.1:1", "GCE_METADATA_IP=127.0.0.1:1",
	}
}

func TestPlanExecChildEnvAndCleanMint(t *testing.T) {
	e := setup(t)
	p := prod()
	p.ImpersonateServiceAccount = "deployer@prod-project.iam.gserviceaccount.com"
	mustApply(t, p)
	addCredential(t, p.Account, "authorized_user")
	o := execOpts(e, p)
	stderr := &strings.Builder{}
	o.Stderr = stderr
	prog, argv, env, err := PlanExec(e.gcloud, hostile(t), o)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(prog) != "true" || strings.Join(argv, " ") != "true" {
		t.Fatalf("prog %s argv %v", prog, argv)
	}
	// The mint: the Agent's root, its two settings, impersonation, the 10-minute refresh,
	// and nothing of the caller's.
	calls := e.calls(t)
	if len(calls) != 1 {
		t.Fatalf("gcloud calls: %d", len(calls))
	}
	mint := calls[0]
	root, _ := filepath.EvalSymlinks(ConfigRoot())
	wantArgs := "config config-helper --configuration af-prod --min-expiry 10m --format json(credential.access_token,credential.token_expiry) " +
		"--quiet --impersonate-service-account deployer@prod-project.iam.gserviceaccount.com"
	if mint.args != wantArgs {
		t.Errorf("mint args\n got %s\nwant %s", mint.args, wantArgs)
	}
	if mint.env["CLOUDSDK_CONFIG"] != root || mint.env["CLOUDSDK_CORE_DISABLE_FILE_LOGGING"] != "true" ||
		mint.env["CLOUDSDK_CORE_CHECK_GCE_METADATA"] != "false" {
		t.Errorf("mint env: %v", mint.env)
	}
	for k := range mint.env {
		if k == "CLOUDSDK_AUTH_ACCESS_TOKEN_FILE" || k == "GOOGLE_APPLICATION_CREDENTIALS" || k == "CLOUDSDK_CORE_ACCOUNT" ||
			strings.HasPrefix(k, "GCE_METADATA_") || strings.HasPrefix(k, "GOOGLE_") || strings.HasPrefix(k, "GCLOUD_") {
			t.Errorf("the caller's %s reached the mint", k)
		}
	}
	// The child: exactly decision 2.
	got := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if _, dup := got[k]; dup {
			t.Errorf("duplicate %s in the child env", k)
		}
		got[k] = v
	}
	run := filepath.Dir(got["CLOUDSDK_AUTH_ACCESS_TOKEN_FILE"])
	if b, _ := os.ReadFile(got["CLOUDSDK_AUTH_ACCESS_TOKEN_FILE"]); string(b) != e.token {
		t.Error("the token file does not hold the minted token")
	}
	if ents, err := os.ReadDir(got["CLOUDSDK_CONFIG"]); err != nil || len(ents) != 0 || filepath.Dir(got["CLOUDSDK_CONFIG"]) != run {
		t.Errorf("child CLOUDSDK_CONFIG %s is not an empty private dir (%v)", got["CLOUDSDK_CONFIG"], err)
	}
	if fi, err := os.Stat(run); err != nil || fi.Mode().Perm() != 0o700 || !strings.HasPrefix(run, filepath.Join(e.home, ".local", "state", "agent-fleet", "gcp-exec")) {
		t.Errorf("run dir %s: %v %v", run, fi, err)
	}
	if gac := got["GOOGLE_APPLICATION_CREDENTIALS"]; filepath.Dir(gac) != run {
		t.Errorf("GOOGLE_APPLICATION_CREDENTIALS = %s", gac)
	} else if _, err := os.Stat(gac); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("GOOGLE_APPLICATION_CREDENTIALS path exists: %v", err)
	}
	want := map[string]string{
		"PATH": os.Getenv("PATH"), "HOME": e.home, "KEEP_ME": "1",
		"CLOUDSDK_CONFIG": got["CLOUDSDK_CONFIG"], "CLOUDSDK_AUTH_ACCESS_TOKEN_FILE": got["CLOUDSDK_AUTH_ACCESS_TOKEN_FILE"],
		"CLOUDSDK_CORE_DISABLE_FILE_LOGGING": "true", "GOOGLE_OAUTH_ACCESS_TOKEN": e.token,
		"CLOUDSDK_CORE_PROJECT": "prod-project", "GOOGLE_CLOUD_PROJECT": "prod-project", "GOOGLE_PROJECT": "prod-project",
		"CLOUDSDK_BILLING_QUOTA_PROJECT": "billing-project", "GOOGLE_BILLING_PROJECT": "billing-project", "USER_PROJECT_OVERRIDE": "true",
		"CLOUDSDK_COMPUTE_REGION": "asia-northeast1", "GOOGLE_REGION": "asia-northeast1",
		"CLOUDSDK_COMPUTE_ZONE": "asia-northeast1-a", "GOOGLE_ZONE": "asia-northeast1-a",
		"GOOGLE_APPLICATION_CREDENTIALS": got["GOOGLE_APPLICATION_CREDENTIALS"],
	}
	if len(got) != len(want) {
		t.Errorf("child env has %d variables, want %d: %v", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("child %s = %q, want %q", k, got[k], v)
		}
	}
	// What is printed: account, impersonated principal and minutes; never the token.
	msg := stderr.String()
	if !strings.Contains(msg, "deployer@prod-project.iam.gserviceaccount.com (impersonated by dev@example.com)") ||
		!strings.Contains(msg, "minutes") || strings.Contains(msg, e.token) {
		t.Errorf("stderr: %q", msg)
	}
	if _, err := os.Stat(filepath.Join(ConfigRoot(), "logs")); err == nil {
		t.Error("gcloud wrote logs under the Agent's root")
	}
}

func TestPlanExecRefusals(t *testing.T) {
	e := setup(t)
	p := prod()
	mustApply(t, p)

	// A project mismatch is refused before anything is minted, and is no login problem.
	o := execOpts(e, p)
	o.Project = "other-project"
	if _, _, _, err := PlanExec(e.gcloud, hostile(t), o); err == nil || errors.Is(err, ErrLoginRequired) || !strings.Contains(err.Error(), "prod-project") {
		t.Fatalf("project mismatch: %v", err)
	}
	if len(e.calls(t)) != 0 {
		t.Fatal("gcloud ran for a project mismatch")
	}
	// No credential for the account: login required, nothing minted.
	if _, _, _, err := PlanExec(e.gcloud, hostile(t), execOpts(e, p)); !errors.Is(err, ErrLoginRequired) ||
		!strings.Contains(err.Error(), "af-gcloud-exec --profile 'prod' --project 'prod-project' --login -- true") {
		t.Fatalf("no credential: %v", err)
	}
	if len(e.calls(t)) != 0 {
		t.Fatal("gcloud minted without a credential")
	}
	// A service-account key in the store is never minted from.
	addCredential(t, p.Account, "service_account")
	if _, _, _, err := PlanExec(e.gcloud, hostile(t), execOpts(e, p)); err == nil || errors.Is(err, ErrLoginRequired) {
		t.Fatalf("service account credential: %v", err)
	}
	// No account selected (profile names none, no login yet): login required, no mint, so
	// no fallback to the VM's identity.
	q := p
	q.Account = ""
	mustApply(t, q)
	if _, _, _, err := PlanExec(e.gcloud, hostile(t), execOpts(e, q)); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("no account: %v", err)
	}
	if len(e.calls(t)) != 0 {
		t.Fatal("gcloud ran for a configuration without an account")
	}
	// Unknown and colliding names.
	o = execOpts(e, q)
	o.Profile = "nope"
	if _, _, _, err := PlanExec(e.gcloud, nil, o); err == nil {
		t.Fatal("unknown profile ran")
	}
	o = execOpts(e, q)
	o.Conflicts = []Conflict{{Name: "prod", Labels: []string{"Prod", "prod"}}}
	if _, _, _, err := PlanExec(e.gcloud, nil, o); err == nil || !strings.Contains(err.Error(), "rename") {
		t.Fatalf("colliding profile: %v", err)
	}
}

// The token never leaves through the wrapper's output: not on success, not when gcloud
// fails with it in its stderr, not when its output cannot be used.
func TestTokenNeverPrinted(t *testing.T) {
	e := setup(t)
	p := prod()
	mustApply(t, p)
	addCredential(t, p.Account, "authorized_user")
	b64 := strings.Repeat(randHex(t, 15)+"/+", 4) + "=="
	jwt := "eyJ" + randHex(t, 12) + "." + "eyJ" + randHex(t, 20) + "." + randHex(t, 16) + "_-"
	for _, marker := range []string{e.token, b64, jwt} {
		e.write(t, "token", marker)
		t.Run(marker[:4], func(t *testing.T) { tokenNeverPrinted(t, e, p, marker) })
	}
}

func tokenNeverPrinted(t *testing.T, e *env, p Profile, marker string) {
	cases := map[string]func(){
		"ok":        func() {},
		"short":     func() { e.write(t, "expiry", time.Now().Add(4*time.Minute).UTC().Format(time.RFC3339)) },
		"no expiry": func() { e.write(t, "stdout", `{"credential":{"access_token":"`+marker+`","token_expiry":null}}`) },
		"bad expiry": func() {
			e.write(t, "stdout", `{"credential":{"access_token":"`+marker+`","token_expiry":"`+marker+`"}}`)
		},
		"not json":       func() { e.write(t, "stdout", "access_token: "+marker+"\n") },
		"error":          func() { e.write(t, "fail", "ERROR: (gcloud.config.config-helper) bad token "+marker+"\n") },
		"error reauth":   func() { e.write(t, "fail", "ERROR: Reauthentication failed. "+marker+"\n") },
		"error trailing": func() { e.write(t, "fail", "something "+marker) },
	}
	for name, arrange := range cases {
		t.Run(name, func(t *testing.T) {
			for _, f := range []string{"stdout", "fail"} {
				_ = os.Remove(filepath.Join(e.fakeDir, f))
			}
			e.write(t, "expiry", time.Now().Add(55*time.Minute).UTC().Format(time.RFC3339))
			arrange()
			stderr := &strings.Builder{}
			o := execOpts(e, p)
			o.Stderr = stderr
			_, _, _, err := PlanExec(e.gcloud, hostile(t), o)
			if (err == nil) != (name == "ok") {
				t.Fatalf("err = %v", err)
			}
			out := stderr.String()
			if err != nil {
				out += err.Error()
			}
			if strings.Contains(out, marker) || strings.Contains(out, strings.TrimPrefix(marker, "ya29.")) ||
				strings.Contains(out, marker[len(marker)/2:]) {
				t.Fatalf("the token reached the output: %q", out)
			}
			if name == "error reauth" && !errors.Is(err, ErrLoginRequired) {
				t.Errorf("a reauthentication error is not a login: %v", err)
			}
		})
	}
}

func TestTerminalLoginRunsAgainstTheAgentsRoot(t *testing.T) {
	e := setup(t)
	p := prod()
	p.Account = ""
	mustApply(t, p)
	addCredential(t, "picked@example.com", "authorized_user")
	e.write(t, "login-account", "picked@example.com")
	o := execOpts(e, p)
	o.Login = "always"
	if _, _, _, err := PlanExec(e.gcloud, hostile(t), o); err != nil {
		t.Fatal(err)
	}
	calls := e.calls(t)
	if len(calls) != 2 || calls[0].args != "auth login --no-launch-browser --configuration af-prod" ||
		!strings.HasPrefix(calls[1].args, "config config-helper") {
		t.Fatalf("calls: %+v", calls)
	}
	root, _ := filepath.EvalSymlinks(ConfigRoot())
	if c := calls[0].env; c["CLOUDSDK_CONFIG"] != root || c["CLOUDSDK_CORE_DISABLE_FILE_LOGGING"] != "true" ||
		c["CLOUDSDK_CORE_CHECK_GCE_METADATA"] != "false" || c["CLOUDSDK_AUTH_ACCESS_TOKEN_FILE"] != "" || c["GCE_METADATA_HOST"] != "" {
		t.Errorf("login env: %v", c)
	}
	// The login's account stays selected through the next sync.
	mustApply(t, p)
	if ConfiguredAccount("prod") != "picked@example.com" {
		t.Fatalf("login account lost: %q", configText(t, "prod"))
	}
	// With an account in Settings, the login names it.
	if got := strings.Join(LoginArgs(prod(), false), " "); got != "auth login dev@example.com --no-launch-browser --configuration af-prod" {
		t.Errorf("login args %s", got)
	}
}

func TestSweepRuns(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	for name, age := range map[string]time.Duration{"run-old": 13 * time.Hour, "run-new": time.Hour, "keep": 20 * time.Hour} {
		d := filepath.Join(dir, name)
		_ = os.Mkdir(d, 0o700)
		_ = os.WriteFile(filepath.Join(d, "token"), nil, 0o600)
		_ = os.Chtimes(filepath.Join(d, "token"), now.Add(-age), now.Add(-age))
	}
	sweepRuns(dir, filepath.Join(dir, "run-new"), now)
	for name, want := range map[string]bool{"run-old": false, "run-new": true, "keep": true} {
		if _, err := os.Stat(filepath.Join(dir, name)); (err == nil) != want {
			t.Errorf("%s present=%v", name, err == nil)
		}
	}
}

// A run reads its profile from the sync before its mint; when another sync applies a newer
// version in between, the run must not mint from the newer configuration under the older
// snapshot's checks and messages.
func TestMintRefusesAnotherVersionOfTheProfile(t *testing.T) {
	e := setup(t)
	old := prod()
	old.Account = ""
	addCredential(t, "new@example.com", "authorized_user")
	newer := map[string]func(Profile) Profile{
		"account and impersonation": func(p Profile) Profile {
			p.Account, p.ImpersonateServiceAccount = "new@example.com", "new@prod-project.iam.gserviceaccount.com"
			return p
		},
		"recreated": func(p Profile) Profile { p.ID = "id-9"; return p },
		"project":   func(p Profile) Profile { p.Project = "prod-project-2"; return p },
	}
	for name, change := range newer {
		t.Run(name, func(t *testing.T) {
			_ = os.Remove(filepath.Join(e.fakeDir, "calls"))
			mustApply(t, old)
			// A login selected an account under the old version.
			cfg := filepath.Join(ConfigRoot(), "configurations", "config_af-prod")
			_ = os.WriteFile(cfg, []byte(strings.Replace(configText(t, "prod"), "[core]\n", "[core]\naccount = new@example.com\n", 1)), 0o600)
			mustApply(t, change(old))
			o := execOpts(e, old)
			o.Login = "always"
			_, _, _, err := PlanExec(e.gcloud, hostile(t), o)
			if !errors.Is(err, ErrSettingsChanged) {
				t.Fatalf("err = %v, want ErrSettingsChanged", err)
			}
			if c := e.calls(t); len(c) != 0 {
				t.Fatalf("gcloud ran: %+v", c)
			}
		})
	}
}

func TestLoginHoldsTheLockAndYieldsToALaterReset(t *testing.T) {
	e := setup(t)
	p := prod()
	p.Account = ""
	mustApply(t, p)
	addCredential(t, "picked@example.com", "authorized_user")
	e.write(t, "login-account", "picked@example.com")
	o := execOpts(e, p)
	o.Login = "always"
	if _, _, _, err := PlanExec(e.gcloud, hostile(t), o); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("flock"); err == nil {
		if b, _ := os.ReadFile(filepath.Join(e.fakeDir, "lockstate")); strings.TrimSpace(string(b)) != "locked" {
			t.Errorf("gcloud's login ran without the root's lock (%q)", b)
		}
	}
	// gcloud rewrote the file in its own layout; the selection is still the login's.
	if ConfiguredAccount("prod") != "picked@example.com" {
		t.Fatalf("login account: %q", configText(t, "prod"))
	}
	// The profile was recreated in Settings while the person logged in: the first sync after
	// the login resets the selection.
	q := p
	q.ID = "id-9"
	mustApply(t, q)
	if ConfiguredAccount("prod") != "" {
		t.Fatal("a login for the old version stayed selected after the reset")
	}
	// A login is never started for a version the root no longer holds.
	_ = os.Remove(filepath.Join(e.fakeDir, "calls"))
	if _, _, _, err := PlanExec(e.gcloud, hostile(t), o); !errors.Is(err, ErrSettingsChanged) || len(e.calls(t)) != 0 {
		t.Fatalf("stale login: %v, calls %d", err, len(e.calls(t)))
	}
}

// The CP's name rule has no length limit (labels go up to 100 characters), and neither has
// gcloud's configuration name rule.
func TestLongNamesAreExported(t *testing.T) {
	setup(t)
	p := prod()
	p.Name = "p" + strings.Repeat("a1-", 33) + "z"
	if res := mustApply(t, p); res.Exported[p.Name].Name == "" {
		t.Fatalf("a %d-character name was refused: %v", len(p.Name), res.Invalid)
	}
}

// Fixtures are gcloud 587.0.0's own stderr against a local token endpoint answering each
// OAuth error.
func TestLoginNeededFollowsTheUnderlyingError(t *testing.T) {
	gcloudSays := func(code string) string {
		return "ERROR: (gcloud.config.config-helper) There was a problem refreshing your current auth tokens: ('" + code +
			": synthetic " + code + "', {'error': '" + code + "', 'error_description': 'synthetic " + code + "'})\n" +
			"Please run:\n\n  $ gcloud auth login\n\nto obtain new credentials.\n\n" +
			"If you have already logged in with a different account, run:\n\n  $ gcloud config set account ACCOUNT\n\n" +
			"to select an already authenticated account to use.\n"
	}
	for in, want := range map[string]bool{
		gcloudSays("invalid_grant"):           true,
		gcloudSays("temporarily_unavailable"): false,
		gcloudSays("server_error"):            false,
		"ERROR: (gcloud.config.config-helper) You do not currently have an active account selected.\nPlease run:\n\n  $ gcloud auth login\n": true,
		"ERROR: (gcloud.config.config-helper) Reauthentication failed. cannot prompt during non-interactive execution.\n":                    true,
		"ERROR: (gcloud.config.config-helper) PERMISSION_DENIED: Permission 'iam.serviceAccounts.getAccessToken' denied\n":                   false,
		"ERROR: (gcloud.config.config-helper) something nobody has seen\n":                                                                   false,
	} {
		if got := loginNeeded(in); got != want {
			t.Errorf("loginNeeded(%.90q) = %v, want %v", in, got, want)
		}
	}
}

// Google rejected the stored credential: the login has to be forced, or gcloud reuses the
// cached access token, starts no sign-in, and the same refresh fails again.
func TestRejectedCredentialForcesTheLogin(t *testing.T) {
	e := setup(t)
	p := prod()
	mustApply(t, p)
	addCredential(t, p.Account, "authorized_user")
	e.write(t, "fail", "ERROR: (gcloud.config.config-helper) There was a problem refreshing your current auth tokens: "+
		"('invalid_grant: Bad Request', {'error': 'invalid_grant', 'error_description': 'Bad Request'})\n")
	o := execOpts(e, p)
	o.Login = "always"
	if _, _, _, err := PlanExec(e.gcloud, hostile(t), o); err != nil {
		t.Fatal(err)
	}
	calls := e.calls(t)
	if len(calls) != 3 || calls[1].args != "auth login dev@example.com --no-launch-browser --configuration af-prod --force" {
		t.Fatalf("calls: %+v", calls)
	}
	// No account or no credential at all: nothing to reuse, so no --force.
	_ = os.Remove(filepath.Join(e.fakeDir, "calls"))
	addCredential(t, "picked@example.com", "authorized_user")
	e.write(t, "login-account", "picked@example.com")
	q := p
	q.Account = ""
	mustApply(t, q)
	if _, _, _, err := PlanExec(e.gcloud, hostile(t), execOptsLogin(e, q)); err != nil {
		t.Fatal(err)
	}
	if c := e.calls(t); len(c) != 2 || strings.Contains(c[0].args, "--force") {
		t.Fatalf("calls: %+v", c)
	}
}

func execOptsLogin(e *env, p Profile) ExecOptions {
	o := execOpts(e, p)
	o.Login = "always"
	return o
}

// A login that ran but left no usable credential is a failure (exit 1), not "login needed
// and not started" (exit 3).
func TestFailureAfterTheLoginIsNotLoginRequired(t *testing.T) {
	e := setup(t)
	p := prod()
	mustApply(t, p)
	addCredential(t, p.Account, "authorized_user")
	e.write(t, "fail-sticky", "ERROR: (gcloud.config.config-helper) There was a problem refreshing your current auth tokens: "+
		"('invalid_grant: Bad Request', {'error': 'invalid_grant'})\n")
	_, _, _, err := PlanExec(e.gcloud, hostile(t), execOptsLogin(e, p))
	if err == nil || errors.Is(err, ErrLoginRequired) || !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("err = %v", err)
	}
	if c := e.calls(t); len(c) != 3 {
		t.Fatalf("calls: %+v", c)
	}
}
