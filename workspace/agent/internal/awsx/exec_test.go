package awsx

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ssoProfile is what `aws configure get` reports for a complete SSO profile.
var ssoProfile = map[string]string{
	"sso_session": "af-prod", "sso_account_id": "123456789012", "sso_role_name": "Dev", "region": "us-west-2",
}

// fakeAWS writes an aws stand-in. `configure get <key>` answers from cfg, the way the
// real CLI reads the merged config + credentials entry of the profile; `loggedIn` (a
// file) decides whether export-credentials succeeds, and `sso login` creates it. It
// records whether it ever saw a workload-role variable, and the AWS_CONFIG_FILE it got.
func fakeAWS(t *testing.T, cfg map[string]string) (bin, state string) {
	t.Helper()
	dir := t.TempDir()
	state = filepath.Join(dir, "state")
	if err := os.MkdirAll(filepath.Join(state, "cfg"), 0o700); err != nil {
		t.Fatal(err)
	}
	for k, v := range cfg {
		if err := os.WriteFile(filepath.Join(state, "cfg", k), []byte(v+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script := `#!/bin/sh
S="` + state + `"
env | grep -E '^AWS_(CONTAINER_|WEB_IDENTITY)' >> "$S/leaked"
[ "$AWS_EC2_METADATA_DISABLED" = true ] || echo imds >> "$S/leaked"
echo "$AWS_CONFIG_FILE" > "$S/configFile"
case "$1 $2" in
"configure get")
  [ -f "$S/cfg/$3" ] && cat "$S/cfg/$3"
  exit 0 ;;
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
		"static keys only":    {"aws_access_key_id": "AKIA", "region": "us-west-2"},
		"no account":          with(nil, "sso_account_id"),
		"no role":             with(nil, "sso_role_name"),
		"credential_process":  with(map[string]string{"credential_process": "/bin/echo"}, "sso_account_id"),
		"role_arn":            with(map[string]string{"role_arn": "arn:aws:iam::1:role/x"}),
		"web identity":        with(map[string]string{"web_identity_token_file": "/tmp/t"}),
		"credential_source":   with(map[string]string{"credential_source": "EcsContainer"}),
		"static keys and sso": with(map[string]string{"aws_access_key_id": "AKIA"}),
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
	home := t.TempDir()
	t.Setenv("HOME", home)
	for isolated, want := range map[string]string{
		filepath.Join(home, ".aws", "af-sessions", "x.config"): filepath.Join(home, ".aws", "config"),
		filepath.Join(home, "mine.config"):                     filepath.Join(home, "mine.config"),
	} {
		bin, state := fakeAWS(t, ssoProfile)
		if err := os.WriteFile(filepath.Join(state, "loggedIn"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, env, err := PlanExec(bin, append(workloadEnv, "AWS_CONFIG_FILE="+isolated),
			ExecOptions{Profile: "prod", Login: "never", Argv: []string{"true"}, Quiet: true})
		if err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(filepath.Join(state, "configFile"))
		if strings.TrimSpace(string(got)) != want || envMap(env)["AWS_CONFIG_FILE"] != want {
			t.Errorf("AWS_CONFIG_FILE=%s: aws saw %q, child %q, want %q", isolated, got, envMap(env)["AWS_CONFIG_FILE"], want)
		}
	}
}
