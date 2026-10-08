package awsx

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Role chaining (issue #1109): a Settings profile that assumes a role from another Settings
// profile. The properties under test are that the managed block holds no secret, that the
// source of a chain is always the Settings SSO profile and never something the member's own
// files define under that name, and that af-aws-exec runs the chain as that profile.

const chainRoleARN = "arn:aws:iam::210987654321:role/deploy"

func chainSrc() Profile { return prof("src") }

func chainProf() Profile {
	return Profile{Name: "deploy", Label: "deploy", Kind: KindAssumeRole, SourceProfile: "src", RoleARN: chainRoleARN,
		AccountID: "210987654321", ExternalID: "ext-1", SessionName: "af", DurationSeconds: 1800}
}

// chainHome gives the test an empty ~/.aws and the fake aws CLI.
func chainHome(t *testing.T) (bin, state string) {
	t.Helper()
	bin, state = fakeAWS(t, ssoProfile)
	aws := filepath.Join(os.Getenv("HOME"), ".aws")
	for _, f := range []string{"config", "credentials"} {
		if err := os.WriteFile(filepath.Join(aws, f), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return bin, state
}

func TestApplyExportsARoleChainedProfileWithoutASecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	res, err := Apply(path, []Profile{chainProf(), chainSrc()})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(res.Exported, ",") != "src,deploy" {
		t.Fatalf("exported %v: the source must come first", res.Exported)
	}
	b, _ := os.ReadFile(path)
	got := string(b)
	want := "[profile deploy]\nrole_arn = arn:aws:iam::210987654321:role/deploy\nsource_profile = src\n" +
		"external_id = ext-1\nrole_session_name = af\nduration_seconds = 1800\nregion = us-west-2\n"
	if !strings.Contains(got, want) {
		t.Fatalf("block lacks the chained profile:\n%s\nwant\n%s", got, want)
	}
	for _, bad := range []string{"aws_access_key_id", "aws_secret_access_key", "aws_session_token", "credential_process"} {
		if strings.Contains(got, bad) {
			t.Fatalf("the block holds %q:\n%s", bad, got)
		}
	}
	// An account-less chain is not "incomplete": its account is its role's.
	if IncompleteReason(chainProf()) != "" {
		t.Fatal("a chained profile was called incomplete")
	}
}

// The failure this guards against: the member's own [profile src] with long-lived keys.
// Exporting the chain would make its source_profile read those keys.
func TestApplyHoldsBackAChainWhoseSourceIsNotTheSettingsProfile(t *testing.T) {
	for name, tc := range map[string]struct {
		config, creds string
		ps            []Profile
		want          string
	}{
		"member defines src in config":      {"[profile src]\nsso_session = mine\n", "", []Profile{chainSrc(), chainProf()}, "not exported"},
		"member defines src in credentials": {"", "[src]\naws_access_key_id = AKIAMINE\naws_secret_access_key = x\n", []Profile{chainSrc(), chainProf()}, "not exported"},
		"source is not a Settings profile":  {"", "", []Profile{chainProf()}, "not a Settings profile"},
		"source is itself chained": {"", "", []Profile{chainSrc(), chainProf(), func() Profile {
			p := chainProf()
			p.Name, p.Label, p.SourceProfile = "nested", "nested", "deploy"
			return p
		}()}, "assumes a role itself"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config")
			os.WriteFile(path, []byte(tc.config), 0o600)
			os.WriteFile(filepath.Join(dir, "credentials"), []byte(tc.creds), 0o600)
			res, err := Apply(path, tc.ps)
			if err != nil {
				t.Fatal(err)
			}
			held := "deploy"
			if name == "source is itself chained" {
				held = "nested"
			}
			for _, n := range res.Exported {
				if n == held {
					t.Fatalf("%s was exported with a source that is not the Settings SSO profile", n)
				}
			}
			why := res.ChainBroken[held]
			if !strings.Contains(why, tc.want) {
				t.Fatalf("ChainBroken = %v, want a reason containing %q", res.ChainBroken, tc.want)
			}
			if b, _ := os.ReadFile(path); strings.Contains(string(b), "[profile "+held+"]") && !strings.Contains(tc.config, "[profile "+held+"]") {
				t.Fatalf("[profile %s] was written:\n%s", held, b)
			}
		})
	}
}

func TestApplyRefusesChainValuesTheConfigCannotHold(t *testing.T) {
	for name, mut := range map[string]func(p *Profile){
		"role arn":     func(p *Profile) { p.RoleARN = chainRoleARN + "\ncredential_process = /bin/sh" },
		"external id":  func(p *Profile) { p.ExternalID = "a\nb" },
		"session name": func(p *Profile) { p.SessionName = "a\nb" },
		"duration":     func(p *Profile) { p.DurationSeconds = 5 },
		"region":       func(p *Profile) { p.Region = "x\ny" },
		"not a role":   func(p *Profile) { p.RoleARN = "arn:aws:iam::210987654321:user/bob" },
	} {
		t.Run(name, func(t *testing.T) {
			p := chainProf()
			mut(&p)
			path := filepath.Join(t.TempDir(), "config")
			res, err := Apply(path, []Profile{chainSrc(), p})
			if err != nil {
				t.Fatal(err)
			}
			if res.Invalid["deploy"] == "" {
				t.Fatalf("not reported invalid: %+v", res)
			}
			if b, _ := os.ReadFile(path); strings.Contains(string(b), "credential_process") || strings.Contains(string(b), "[profile deploy]") {
				t.Fatalf("the invalid value reached the config:\n%s", b)
			}
		})
	}
}

func TestApplyHoldsBackAChainADEFAULTKeyBreaks(t *testing.T) {
	for name, tc := range map[string]string{
		"second way to get credentials": "[DEFAULT]\nsso_session = other\n",
		"keys in default":               "[DEFAULT]\naws_access_key_id = AKIA\n",
		"other role in default":         "[DEFAULT]\nrole_arn = arn:aws:iam::1:role/other\n",
		"other session name":            "[DEFAULT]\nrole_session_name = mine\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config")
			os.WriteFile(path, []byte(tc), 0o600)
			res, err := Apply(path, []Profile{chainSrc(), chainProf()})
			if err != nil {
				t.Fatal(err)
			}
			// Either the chain itself is held back, or its source is (a [DEFAULT] key that breaks
			// the SSO profile takes the chain with it).
			if res.DefaultClash["deploy"] == "" && res.ChainBroken["deploy"] == "" {
				t.Fatalf("a [DEFAULT] key that breaks the chain was not reported: %+v", res)
			}
			for _, n := range res.Exported {
				if n == "deploy" {
					t.Fatalf("deploy was exported under a [DEFAULT] that breaks it")
				}
			}
			if strings.Contains(res.DefaultClash["deploy"], "AKIA") {
				t.Fatalf("the reason quotes a credential value: %s", res.DefaultClash["deploy"])
			}
		})
	}
}

// chainOpts is a run of the chained profile: nothing at a terminal, the way an agent runs it.
func chainOpts(stderr *bytes.Buffer) ExecOptions {
	return ExecOptions{Profile: "deploy", Login: "never", Argv: []string{"true"}, Quiet: true, Stderr: stderr,
		Settings: map[string]Profile{"src": chainSrc(), "deploy": chainProf()}, Exported: []string{"src", "deploy"}}
}

func applyChain(t *testing.T) {
	t.Helper()
	if _, err := Apply(ConfigPath(), []Profile{chainSrc(), chainProf()}); err != nil {
		t.Fatal(err)
	}
}

func TestPlanExecRunsAChainedSettingsProfileWithoutAnAccountFlag(t *testing.T) {
	bin, state := chainHome(t)
	applyChain(t)
	os.WriteFile(filepath.Join(state, "loggedIn"), nil, 0o600)
	os.WriteFile(filepath.Join(state, "arn"), []byte("arn:aws:sts::210987654321:assumed-role/deploy/botocore-session-1"), 0o600)
	var stderr bytes.Buffer
	_, _, env, err := PlanExec(bin, workloadEnv, chainOpts(&stderr))
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr.String())
	}
	if envMap(env)["AWS_ACCESS_KEY_ID"] != "ASIAFAKE" {
		t.Fatal("no credentials")
	}
	// The export resolved the chained profile by name, from the member's own files.
	if b, _ := os.ReadFile(filepath.Join(state, "calls")); !strings.Contains(string(b), "x") {
		t.Fatal("the CLI never ran")
	}

	// A pinned account that is not the role's is refused before anything is fetched.
	bin2, _ := chainHome(t)
	applyChain(t)
	o := chainOpts(&stderr)
	o.Account = "999999999999"
	if _, _, _, err := PlanExec(bin2, workloadEnv, o); err == nil || !strings.Contains(err.Error(), "not the 999999999999") {
		t.Fatalf("a wrong --account: %v", err)
	}
}

func TestPlanExecRefusesAChainedNameTheMemberRedefined(t *testing.T) {
	bin, state := chainHome(t)
	os.WriteFile(filepath.Join(state, "loggedIn"), nil, 0o600)
	// The member's own definitions win over Settings (Apply then exports neither): the same
	// role and source_profile as Settings, but a [src] holding long-lived keys.
	mine := "[profile deploy]\nrole_arn = " + chainRoleARN + "\nsource_profile = src\n"
	os.WriteFile(ConfigPath(), []byte(mine), 0o600)
	os.WriteFile(filepath.Join(filepath.Dir(ConfigPath()), "credentials"), []byte("[src]\naws_access_key_id = AKIAMINE\naws_secret_access_key = x\n"), 0o600)
	res, err := Apply(ConfigPath(), []Profile{chainSrc(), chainProf()})
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	o := chainOpts(&stderr)
	o.Exported = res.Exported // what the sync really exported: nothing of the chain
	if _, _, _, err := PlanExec(bin, workloadEnv, o); err == nil || !strings.Contains(err.Error(), "managed block") {
		t.Fatalf("err = %v, want a refusal: the member's own chain is not a Settings chain", err)
	}
	// Even claiming both exported, the files are checked: keys under the source's name.
	o.Exported = []string{"src", "deploy"}
	if _, _, _, err := PlanExec(bin, workloadEnv, o); err == nil || !strings.Contains(err.Error(), "not the Settings profile") {
		t.Fatalf("err = %v, want a refusal naming the clash", err)
	}
	// A source swapped for another SSO profile under the same name is refused too.
	chainHome(t)
	applyChain(t)
	b, _ := os.ReadFile(ConfigPath())
	swapped := strings.Replace(string(b), "sso_account_id = 123456789012", "sso_account_id = 999999999999", 1)
	os.WriteFile(ConfigPath(), []byte(swapped), 0o600)
	if _, _, _, err := PlanExec(bin, workloadEnv, chainOpts(&stderr)); err == nil || !strings.Contains(err.Error(), "not defined as Settings defines it") {
		t.Fatalf("a swapped source: %v", err)
	}
}

func TestPlanExecSaysWhyAHeldBackChainIsNotRunnable(t *testing.T) {
	bin, _ := chainHome(t)
	var stderr bytes.Buffer
	o := chainOpts(&stderr)
	o.ChainBroken = map[string]string{"deploy": "its source profile \"src\" is not exported"}
	_, _, _, err := PlanExec(bin, workloadEnv, o)
	if err == nil || !strings.Contains(err.Error(), "its source profile") {
		t.Fatalf("err = %v", err)
	}
}

func TestPlanExecNamesTheSourceWhenTheChainNeedsALogin(t *testing.T) {
	bin, _ := chainHome(t)
	applyChain(t)
	var stderr bytes.Buffer
	_, _, _, err := PlanExec(bin, workloadEnv, chainOpts(&stderr))
	if !errors.Is(err, ErrLoginRequired) || !strings.Contains(err.Error(), `"src"`) || !strings.Contains(err.Error(), "--profile 'src'") {
		t.Fatalf("err = %v, want a login for the source profile src", err)
	}
}

// ADR 0102: the Console login is asked under the SOURCE's name, since that is the Settings
// profile the member signs in to; the run is released once the source's token lands.
func TestConsoleLoginIsAskedForTheSourceOfAChain(t *testing.T) {
	bin, state := chainHome(t)
	applyChain(t)
	fastPoll(t)
	helper := make(chan struct{})
	go func() {
		defer close(helper)
		if path := logins.RequestPath("af-src"); !fileAppears(path) {
			t.Errorf("%s never appeared: the request was not filed under the source's sso-session", path)
			return
		}
		path := ssoCachePath("af-src")
		os.MkdirAll(filepath.Dir(path), 0o700)
		doc, _ := json.Marshal(map[string]string{"accessToken": "fresh", "expiresAt": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
		os.WriteFile(path, doc, 0o600)
		os.WriteFile(filepath.Join(state, "loggedIn"), nil, 0o600)
		os.WriteFile(filepath.Join(state, "arn"), []byte("arn:aws:sts::210987654321:assumed-role/deploy/s"), 0o600)
	}()
	t.Cleanup(func() { <-helper })
	var stderr bytes.Buffer
	o := chainOpts(&stderr)
	o.Login, o.ConsoleLogin, o.ConsoleWait, o.Waiter = "auto", true, 5*time.Second, LoginWaiter{Session: "s1", Command: "terraform"}
	if _, _, _, err := PlanExec(bin, workloadEnv, o); err != nil {
		t.Fatalf("%v\n%s", err, stderr.String())
	}
	r, ok := logins.Read("af-src")
	if !ok || r.Profile != "src" {
		t.Fatalf("request = %+v ok=%v, want one for profile src", r, ok)
	}
	if _, err := os.Stat(filepath.Join(state, "loginArgs")); err == nil {
		t.Fatal("the CLI started a device login itself; only the member's press may")
	}
}

func TestDescribeProfileReadsAChainsAccountFromItsRole(t *testing.T) {
	chainHome(t)
	applyChain(t)
	if a, r := DescribeProfile("deploy"); a != "210987654321" || r != "deploy" {
		t.Fatalf("account %q role %q, want the role's", a, r)
	}
}

// A chained profile has no login of its own, so the row's press and the logout are
// refused with the source named, and its state follows the source's.
func TestProfileLoginOfAChainPointsAtItsSource(t *testing.T) {
	old := syncForLogin
	syncForLogin = func() (SyncResult, error) {
		return SyncResult{Settings: map[string]Profile{"src": chainSrc(), "deploy": chainProf()},
			Exported: []string{"src", "deploy"}, Fetched: true}, nil
	}
	t.Cleanup(func() { syncForLogin = old })
	rec := profileStart("deploy")
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), `"chained_profile"`) || !strings.Contains(rec.Body.String(), "src") {
		t.Fatalf("start = %d %s", rec.Code, rec.Body.String())
	}
	if logins.Current("af-deploy") != nil {
		t.Fatal("a press on the chain started an attempt")
	}
}

func TestProfileLoginStateOfAChainFollowsItsSource(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_AWS_PROFILES_TOKEN", "test-token")
	if err := saveSettingsCache([]Profile{chainSrc(), chainProf()}, nil); err != nil {
		t.Fatal(err)
	}
	states := func() map[string]string {
		rec := httptest.NewRecorder()
		HandleProfileLoginStates(rec, httptest.NewRequest(http.MethodGet, "/aws-login/profiles", nil))
		var out profileLoginStatesWire
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		m := map[string]string{}
		for _, p := range out.Profiles {
			m[p.Name] = p.State
		}
		return m
	}
	if s := states(); s["deploy"] != loginStateNone || s["src"] != loginStateNone {
		t.Fatalf("no login: %v", s)
	}
	path := ssoCachePath("af-src")
	os.MkdirAll(filepath.Dir(path), 0o700)
	doc, _ := json.Marshal(map[string]string{"accessToken": "t", "expiresAt": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	os.WriteFile(path, doc, 0o600)
	if s := states(); s["deploy"] != loginStateSignedIn || s["src"] != loginStateSignedIn {
		t.Fatalf("source signed in: %v", s)
	}
}
