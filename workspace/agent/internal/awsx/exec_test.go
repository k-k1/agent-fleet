package awsx

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// ssoProfile is what `aws configure get` reports for a complete SSO profile.
var ssoProfile = map[string]string{
	"sso_session": "af-prod", "sso_account_id": "123456789012", "sso_role_name": "Dev", "region": "us-west-2",
}

// writeProfile writes cfg as the [profile prod] section of an INI config at path, the
// way the CLI would find it. Keys prefixed "creds:" go to the [prod] section of the
// credentials file beside it instead, and a key prefixed "raw:" is written verbatim as a
// line of the config section (the value is ignored).
func writeProfile(t *testing.T, path string, cfg map[string]string) {
	t.Helper()
	var conf, creds strings.Builder
	conf.WriteString("[default]\nrole_arn = arn:aws:iam::1:role/default\ncredential_source = EcsContainer\n\n[profile prod]\n")
	creds.WriteString("[default]\naws_access_key_id = AKIADEFAULT\n\n[prod]\n")
	for k, v := range cfg {
		if line, ok := strings.CutPrefix(k, "raw:"); ok {
			conf.WriteString(line + "\n")
			continue
		}
		if ck, ok := strings.CutPrefix(k, "creds:"); ok {
			creds.WriteString(ck + " = " + v + "\n")
			continue
		}
		conf.WriteString(k + " = " + v + "\n")
	}
	conf.WriteString("s3 =\n  role_arn = nested-not-a-profile-key\n")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(conf.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "credentials"), []byte(creds.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

// fakeAWS writes the profile into $HOME/.aws and an aws stand-in. `loggedIn` (a file)
// decides whether export-credentials succeeds, and `sso login` creates it. It records
// whether it ever saw a workload-role variable, the AWS_CONFIG_FILE it got, and how many
// times it was started.
func fakeAWS(t *testing.T, cfg map[string]string) (bin, state string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	writeProfile(t, filepath.Join(os.Getenv("HOME"), ".aws", "config"), cfg)
	dir := t.TempDir()
	state = filepath.Join(dir, "state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
S="` + state + `"
env | grep -E '^AWS_(CONTAINER_|WEB_IDENTITY)' >> "$S/leaked"
[ "$AWS_EC2_METADATA_DISABLED" = true ] || echo imds >> "$S/leaked"
echo "$AWS_CONFIG_FILE" > "$S/configFile"
echo x >> "$S/calls"
case "$1 $2" in
"configure export-credentials")
  env | grep -E '^AWS_ACCESS_KEY_ID' >> "$S/leaked"
  [ -f "$S/loggedIn" ] || { echo "Error loading SSO Token: Token for af-prod does not exist" >&2; exit 255; }
  echo '{"Version":1,"AccessKeyId":"ASIAFAKE","SecretAccessKey":"sekret","SessionToken":"tok","Expiration":"2030-01-01T00:00:00+00:00"}'
  exit 0 ;;
"sso login")
  echo "$*" > "$S/loginArgs"; touch "$S/loggedIn"; exit 0 ;;
"sts get-caller-identity")
  [ "$AWS_ACCESS_KEY_ID" = ASIAFAKE ] || exit 255
  echo "arn:aws:sts::123456789012:assumed-role/Dev/me"; exit 0 ;;
esac
exit 1
`
	bin = filepath.Join(dir, "aws")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, state
}

var workloadEnv = []string{
	"PATH=" + os.Getenv("PATH"),
	"HOME=/home/dev",
	"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI=/v2/credentials/abc",
	"AWS_ACCESS_KEY_ID=AKIAWORKLOAD",
	"AWS_SECRET_ACCESS_KEY=workload",
	"AWS_PROFILE=something-else",
}

func envMap(env []string) map[string]string {
	m := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v // last wins, as for execve consumers that scan in order
	}
	return m
}

func TestPlanExecPassesOnlySSOCredentialsToTheChild(t *testing.T) {
	bin, state := fakeAWS(t, ssoProfile)
	if err := os.WriteFile(filepath.Join(state, "loggedIn"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	prog, argv, env, err := PlanExec(bin, workloadEnv, ExecOptions{
		Profile: "prod", Login: "never", Argv: []string{"sh", "-c", "true"}, Stderr: &stderr,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(prog, "/sh") || strings.Join(argv, " ") != "sh -c true" {
		t.Fatalf("prog=%q argv=%v", prog, argv)
	}
	m := envMap(env)
	if m["AWS_ACCESS_KEY_ID"] != "ASIAFAKE" || m["AWS_SESSION_TOKEN"] != "tok" || m["AWS_REGION"] != "us-west-2" {
		t.Fatalf("child env = %v", m)
	}
	for _, k := range []string{"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "AWS_PROFILE"} {
		if _, ok := m[k]; ok {
			t.Fatalf("%s reached the child", k)
		}
	}
	if m["AWS_EC2_METADATA_DISABLED"] != "true" {
		t.Fatal("IMDS is not disabled for the child")
	}
	// Exactly one AWS_ACCESS_KEY_ID: a duplicate would let a consumer that takes the
	// first match run as the workload identity.
	n := 0
	for _, kv := range env {
		if strings.HasPrefix(kv, "AWS_ACCESS_KEY_ID=") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d AWS_ACCESS_KEY_ID entries", n)
	}
	if b, _ := os.ReadFile(filepath.Join(state, "leaked")); len(b) != 0 {
		t.Fatalf("the aws CLI calls saw workload credentials:\n%s", b)
	}
	if !strings.Contains(stderr.String(), "assumed-role/Dev/me") || strings.Contains(stderr.String(), "sekret") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestPlanExecWithoutLoginFailsInsteadOfFallingBack(t *testing.T) {
	bin, state := fakeAWS(t, ssoProfile)
	_, _, _, err := PlanExec(bin, workloadEnv, ExecOptions{Profile: "prod", Login: "auto", Argv: []string{"true"}})
	if !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("err = %v, want ErrLoginRequired", err)
	}
	if !strings.Contains(err.Error(), "--use-device-code") {
		t.Fatalf("the error does not name the device-code login: %v", err)
	}
	if _, serr := os.Stat(filepath.Join(state, "loginArgs")); serr == nil {
		t.Fatal("a login was started without a terminal")
	}
}

func TestPlanExecLogsInWithTheDeviceCode(t *testing.T) {
	bin, state := fakeAWS(t, ssoProfile)
	var stderr bytes.Buffer
	_, _, env, err := PlanExec(bin, workloadEnv, ExecOptions{
		Profile: "prod", Login: "always", Argv: []string{"true"}, Stderr: &stderr, Quiet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(filepath.Join(state, "loginArgs"))
	if !strings.Contains(string(args), "--use-device-code") || !strings.Contains(string(args), "--profile prod") {
		t.Fatalf("login args = %q", args)
	}
	if envMap(env)["AWS_ACCESS_KEY_ID"] != "ASIAFAKE" {
		t.Fatal("no credentials after login")
	}
}

// Each profile shape here lets the CLI resolve credentials through something other than
// the SSO login (measured for the missing-account case with aws-cli 2.36.46), so none
// may reach export-credentials.
func TestPlanExecRefusesProfilesTheCLIWouldNotResolveThroughSSO(t *testing.T) {
	with := func(extra map[string]string, drop ...string) map[string]string {
		m := map[string]string{}
		for k, v := range ssoProfile {
			m[k] = v
		}
		for _, k := range drop {
			delete(m, k)
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	for name, cfg := range map[string]map[string]string{
		"static keys only":            {"aws_access_key_id": "AKIA", "region": "us-west-2"},
		"no account":                  with(nil, "sso_account_id"),
		"no role":                     with(nil, "sso_role_name"),
		"credential_process":          with(map[string]string{"credential_process": "/bin/echo"}, "sso_account_id"),
		"role_arn":                    with(map[string]string{"role_arn": "arn:aws:iam::1:role/x"}),
		"web identity":                with(map[string]string{"web_identity_token_file": "/tmp/t"}),
		"credential_source":           with(map[string]string{"credential_source": "EcsContainer"}),
		"static keys and sso":         with(map[string]string{"aws_access_key_id": "AKIA"}),
		"process in credentials file": with(map[string]string{"creds:credential_process": "/bin/echo"}),
		"role_arn with a colon":       with(map[string]string{"raw:role_arn: arn:aws:iam::1:role/x": "", "raw:source_profile:other": ""}),
		"upper-case key":              with(map[string]string{"raw:Credential_Process = /bin/echo": ""}),
	} {
		bin, state := fakeAWS(t, cfg)
		if err := os.WriteFile(filepath.Join(state, "loggedIn"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, env, err := PlanExec(bin, workloadEnv, ExecOptions{Profile: "prod", Login: "always", Argv: []string{"true"}})
		if err == nil {
			t.Errorf("%s: admitted, child env %v", name, envMap(env))
		}
	}
}

// An SSM session pane exports its own isolated AWS_CONFIG_FILE; the exported profiles
// live in ~/.aws/config, so the wrapper must look there. A file the person chose is kept.
func TestPlanExecLooksPastAnSSMSessionsIsolatedConfig(t *testing.T) {
	bin, state := fakeAWS(t, ssoProfile)
	if err := os.WriteFile(filepath.Join(state, "loggedIn"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	home := os.Getenv("HOME")
	mine := filepath.Join(home, "mine", "config")
	writeProfile(t, mine, ssoProfile)
	// ~/.aws reached through a symlink must match too.
	if err := os.Symlink(filepath.Join(home, ".aws"), filepath.Join(home, "aws-link")); err != nil {
		t.Fatal(err)
	}
	for isolated, want := range map[string]string{
		filepath.Join(home, ".aws", "af-sessions", "x.config"):                 filepath.Join(home, ".aws", "config"),
		filepath.Join(home, ".aws", ".", "af-ops", "..", "af-ops", "a.config"): filepath.Join(home, ".aws", "config"),
		filepath.Join(home, "aws-link", "af-sessions", "x.config"):             filepath.Join(home, ".aws", "config"),
		mine: mine,
	} {
		if err := os.MkdirAll(filepath.Join(home, ".aws", "af-sessions"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(home, ".aws", "af-ops"), 0o700); err != nil {
			t.Fatal(err)
		}
		_, _, env, err := PlanExec(bin, append(workloadEnv, "AWS_CONFIG_FILE="+isolated),
			ExecOptions{Profile: "prod", Login: "never", Argv: []string{"true"}, Quiet: true})
		if err != nil {
			t.Fatalf("AWS_CONFIG_FILE=%s: %v", isolated, err)
		}
		got, _ := os.ReadFile(filepath.Join(state, "configFile"))
		if strings.TrimSpace(string(got)) != want || envMap(env)["AWS_CONFIG_FILE"] != want {
			t.Errorf("AWS_CONFIG_FILE=%s: aws saw %q, child %q, want %q", isolated, got, envMap(env)["AWS_CONFIG_FILE"], want)
		}
	}
}

// Each CLI start costs most of a second; a successful run needs export-credentials and
// the identity check and nothing else.
func TestPlanExecStartsTheCLITwice(t *testing.T) {
	bin, state := fakeAWS(t, ssoProfile)
	if err := os.WriteFile(filepath.Join(state, "loggedIn"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	if _, _, _, err := PlanExec(bin, workloadEnv, ExecOptions{Profile: "prod", Login: "never", Argv: []string{"true"}, Stderr: &stderr}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(state, "calls"))
	if n := strings.Count(string(b), "x"); n != 2 {
		t.Fatalf("aws was started %d times, want 2", n)
	}
}

// profileKeys stands in for the AWS CLI's own parser, so check it against the real one on
// the shapes that have bitten: ":" delimiters, key case, comments, indented sub-settings,
// repeated sections, and keys on [default] that must not leak into another profile. Skipped
// where no aws CLI is installed.
func TestProfileKeysAgreesWithTheRealAWSCLI(t *testing.T) {
	aws, err := exec.LookPath("aws")
	if err != nil {
		t.Skip("aws CLI not installed")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	conf := `[default]
role_arn = arn:aws:iam::1:role/default
credential_source = EcsContainer

[profile prod]
sso_session = af-prod
# role_arn = commented-out
; credential_process = commented-out
sso_account_id: 123456789012
SSO_Role_Name = Dev
s3 =
  role_arn = nested

[profile   prod]
region:us-west-2
source_profile: other

[profile other]
credential_process = /bin/false
`
	creds := "[prod]\naws_access_key_id: AKIAEXAMPLE\n\n[default]\ncredential_process = /bin/true\n"
	if err := os.MkdirAll(filepath.Join(home, ".aws"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".aws", "config"), []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".aws", "credentials"), []byte(creds), 0o600); err != nil {
		t.Fatal(err)
	}
	env := []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
	ours := profileKeys(env, "prod")

	keys := []string{"sso_session", "sso_account_id", "sso_role_name", "region", "role_arn", "source_profile",
		"credential_source", "credential_process", "web_identity_token_file", "aws_access_key_id"}
	got := make([]string, len(keys))
	var wg sync.WaitGroup
	for i, k := range keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(aws, "configure", "get", k, "--profile", "prod")
			cmd.Env = env
			out, _ := cmd.Output()
			got[i] = strings.TrimSpace(string(out))
		}()
	}
	wg.Wait()
	for i, k := range keys {
		if got[i] != ours[k] {
			t.Errorf("%s: aws CLI says %q, profileKeys says %q", k, got[i], ours[k])
		}
	}
}
