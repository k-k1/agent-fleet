package awsx

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeAWS writes an aws stand-in. It answers the calls PlanExec makes; `loggedIn`
// (a file) decides whether export-credentials succeeds, and `sso login` creates it.
// It also records whether it ever saw a workload-role variable.
func fakeAWS(t *testing.T, sso bool) (bin, state string) {
	t.Helper()
	dir := t.TempDir()
	state = filepath.Join(dir, "state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	ssoVal := ""
	if sso {
		ssoVal = "af-prod"
	}
	script := `#!/bin/sh
S="` + state + `"
env | grep -E '^AWS_(CONTAINER_|ACCESS_KEY_ID|WEB_IDENTITY)' >> "$S/leaked"
[ "$AWS_EC2_METADATA_DISABLED" = true ] || echo imds >> "$S/leaked"
case "$1 $2" in
"configure get")
  case "$3" in
  sso_session) [ -n "` + ssoVal + `" ] && echo "` + ssoVal + `" ;;
  region) echo us-west-2 ;;
  esac
  exit 0 ;;
"configure export-credentials")
  [ -f "$S/loggedIn" ] || { echo "Error loading SSO Token: Token for af-prod does not exist" >&2; exit 255; }
  echo '{"Version":1,"AccessKeyId":"ASIAFAKE","SecretAccessKey":"sekret","SessionToken":"tok","Expiration":"2030-01-01T00:00:00+00:00"}'
  exit 0 ;;
"sso login")
  echo "$*" > "$S/loginArgs"; touch "$S/loggedIn"; exit 0 ;;
"sts get-caller-identity")
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
	bin, state := fakeAWS(t, true)
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
	bin, state := fakeAWS(t, true)
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
	bin, state := fakeAWS(t, true)
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

func TestPlanExecRefusesANonSSOProfile(t *testing.T) {
	bin, _ := fakeAWS(t, false)
	_, _, _, err := PlanExec(bin, workloadEnv, ExecOptions{Profile: "static", Login: "never", Argv: []string{"true"}})
	if err == nil || !strings.Contains(err.Error(), "not an SSO profile") {
		t.Fatalf("err = %v", err)
	}
}
