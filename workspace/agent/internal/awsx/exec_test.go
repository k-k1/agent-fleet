package awsx

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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
	conf.WriteString("[default]\nrole_arn = arn:aws:iam::1:role/default\ncredential_source = EcsContainer\n\n" +
		"[sso-session af-prod]\nsso_start_url = https://example.awsapps.com/start\nsso_region = ap-northeast-1\n" +
		"sso_registration_scopes = sso:account:access\n\n[profile prod]\n")
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

// prodSettings says "prod" is the Settings profile writeProfile describes.
var prodSettings = map[string]Profile{"prod": {Name: "prod", Label: "prod", AccountID: "123456789012", RoleName: "Dev",
	StartURL: "https://example.awsapps.com/start", SSORegion: "ap-northeast-1"}}

// with returns ssoProfile plus extra and minus drop.
func with(extra map[string]string, drop ...string) map[string]string {
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
cat "$AWS_CONFIG_FILE" > "$S/seenConfig.$1-$2" 2>/dev/null
env | grep -E '^AWS_(ENDPOINT_URL|IGNORE_CONFIGURED|SHARED_CREDENTIALS_FILE|CONFIG_FILE)' | sort > "$S/seenEnv.$1-$2"
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
  if [ -f "$S/arn" ]; then cat "$S/arn"; else echo "arn:aws:sts::123456789012:assumed-role/AWSReservedSSO_Dev_0123456789abcdef/me"; fi
  exit 0 ;;
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

// envMap maps env for assertions and panics on a duplicated key: getenv returns the
// first entry, so a duplicate means the child may see a different value than the
// last one (which is what a map would silently keep).
func envMap(env []string) map[string]string {
	m := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if _, dup := m[k]; dup {
			panic("duplicated environment variable " + k)
		}
		m[k] = v
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
		Profile: "prod", Settings: prodSettings, Login: "never", Argv: []string{"sh", "-c", "true"}, Stderr: &stderr,
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
	if !strings.Contains(stderr.String(), "assumed-role/AWSReservedSSO_Dev_0123456789abcdef/me") || strings.Contains(stderr.String(), "sekret") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestPlanExecWithoutLoginFailsInsteadOfFallingBack(t *testing.T) {
	bin, state := fakeAWS(t, ssoProfile)
	_, _, _, err := PlanExec(bin, workloadEnv, ExecOptions{Profile: "prod", Settings: prodSettings, Login: "auto", Argv: []string{"true"}})
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
		Profile: "prod", Settings: prodSettings, Login: "always", Argv: []string{"true"}, Stderr: &stderr, Quiet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(filepath.Join(state, "loginArgs"))
	// The login runs against the SSO-only config, whose sso-session keeps the member's
	// session name, so the token lands where `aws sso login --profile prod` would put it.
	if !strings.Contains(string(args), "--use-device-code") || !strings.Contains(string(args), "--profile "+ssoOnlyProfile) {
		t.Fatalf("login args = %q", args)
	}
	if cfg, _ := os.ReadFile(filepath.Join(state, "seenConfig.sso-login")); !strings.Contains(string(cfg), `[sso-session "af-prod"]`) {
		t.Fatalf("login config:\n%s", cfg)
	}
	if envMap(env)["AWS_ACCESS_KEY_ID"] != "ASIAFAKE" {
		t.Fatal("no credentials after login")
	}
}

// Each profile shape here lets the CLI resolve credentials through something other than
// the SSO login (measured for the missing-account case with aws-cli 2.36.46), so none
// may reach export-credentials.
func TestPlanExecRefusesProfilesTheCLIWouldNotResolveThroughSSO(t *testing.T) {
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
		_, _, env, err := PlanExec(bin, workloadEnv, ExecOptions{Profile: "prod", Settings: prodSettings, Login: "always", Argv: []string{"true"}})
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
			ExecOptions{Profile: "prod", Settings: prodSettings, Login: "never", Argv: []string{"true"}, Quiet: true, KeepConfig: true})
		if err != nil {
			t.Fatalf("AWS_CONFIG_FILE=%s: %v", isolated, err)
		}
		if got := envMap(env)["AWS_CONFIG_FILE"]; got != want {
			t.Errorf("AWS_CONFIG_FILE=%s: child got %q, want %q", isolated, got, want)
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
	if _, _, _, err := PlanExec(bin, workloadEnv, ExecOptions{Profile: "prod", Settings: prodSettings, Login: "never", Argv: []string{"true"}, Stderr: &stderr}); err != nil {
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
	conf := `[DEFAULT]
web_identity_token_file = /tmp/inherited

[default]
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

[profile "q1"]
sso_account_id = 111111111111
[profile q1]
sso_role_name = Later

[profile 'q2']
role_arn = arn:aws:iam::2:role/q2

[profiles q3]
region = ap-south-1

[profile nb]
region = us-east-1
<NBSP>[profile decoy]
sso_account_id = 222222222222

[profile c0]
<FS>role_arn = arn:aws:iam::5:role/c0
sso_account_id = 555555555555

[profile nb2]
 foo = bar
<NBSP>role_arn = arn:aws:iam::4:role/nb2

[profile cont]
region = us-east-1
credential_process = /bin/a
  [profile hidden]
  role_arn = arn:aws:iam::3:role/hidden
`
	// A line led by a no-break space (pasted from a web page) is a continuation for
	// configparser; spelled out here to keep the byte visible in the source.
	conf = strings.ReplaceAll(conf, "<NBSP>", "\u00a0")
	// U+001C is whitespace to Python's strip() (not to Go's unicode.IsSpace).
	conf = strings.ReplaceAll(conf, "<FS>", "\x1c")
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

	all := []string{"sso_session", "sso_account_id", "sso_role_name", "region", "role_arn", "source_profile",
		"credential_source", "credential_process", "web_identity_token_file", "aws_access_key_id"}
	few := []string{"sso_account_id", "sso_role_name", "region", "role_arn", "credential_process"}
	type ask struct{ profile, key string }
	var asks []ask
	for _, k := range all {
		asks = append(asks, ask{"prod", k})
	}
	for _, p := range []string{"q1", "q2", "q3", "cont", "hidden", "nb", "decoy", "nb2", "c0"} {
		for _, k := range few {
			asks = append(asks, ask{p, k})
		}
	}
	got := make([]string, len(asks))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6) // each CLI start is ~0.8 s of CPU on a shared host
	for i, a := range asks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			cmd := exec.Command(aws, "configure", "get", a.key, "--profile", a.profile)
			cmd.Env = env
			out, _ := cmd.Output()
			got[i] = strings.TrimSpace(string(out))
		}()
	}
	wg.Wait()
	for i, a := range asks {
		if k, _ := profileKeys(env, a.profile); got[i] != k[a.key] {
			t.Errorf("%s.%s: aws CLI says %q, profileKeys says %q", a.profile, a.key, got[i], k[a.key])
		}
	}
}

func TestIsSSORoleARN(t *testing.T) {
	ok := "arn:aws:sts::123456789012:assumed-role/AWSReservedSSO_Dev_0123456789abcdef/me@example.com"
	for arn, want := range map[string]bool{
		ok: true,
		"arn:aws-cn:sts::123456789012:assumed-role/AWSReservedSSO_Dev_0123456789abcdef/me": true,
		"arn:aws:sts::999999999999:assumed-role/AWSReservedSSO_Dev_0123456789abcdef/me":    false, // other account
		"arn:aws:sts::123456789012:assumed-role/AWSReservedSSO_Admin_0123456789abcdef/me":  false, // other permission set
		"arn:aws:sts::123456789012:assumed-role/AWSReservedSSO_Dev_x_0123456789abcdef/me":  false, // Dev_x is another set
		"arn:aws:sts::123456789012:assumed-role/Dev/me":                                    false, // plain assume-role
		"arn:aws:sts::123456789012:assumed-role/ecsTaskRole/abc":                           false, // workload role
		"arn:aws:iam::123456789012:user/me":                                                false,
		"arn:aws:sts::123456789012:assumed-role/AWSReservedSSO_Dev_0123456789ABCDEF/me":    false,
		"arn:aws:sts::123456789012:assumed-role/AWSReservedSSO_Dev_0123456789abcdef":       false,
		"arn:aws:sts::123456789012:assumed-role/AWSReservedSSO_Dev_0123456789abcdef/me/x":  false,
	} {
		if got := IsSSORoleARN(arn, "123456789012", "Dev"); got != want {
			t.Errorf("IsSSORoleARN(%s) = %v, want %v", arn, got, want)
		}
	}
	if IsSSORoleARN(ok, "", "Dev") || IsSSORoleARN(ok, "123456789012", "") {
		t.Error("an empty account or role matched")
	}
}

// Whatever the static check admits, the credentials must turn out to be the profile's own
// SSO role; anything else (another provider the parser did not foresee) is refused.
func TestPlanExecRefusesCredentialsThatAreNotTheSSORole(t *testing.T) {
	bin, state := fakeAWS(t, ssoProfile)
	if err := os.WriteFile(filepath.Join(state, "loggedIn"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "arn"), []byte("arn:aws:sts::123456789012:assumed-role/deployer/botocore-session-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := PlanExec(bin, workloadEnv, ExecOptions{Profile: "prod", Settings: prodSettings, Login: "never", Argv: []string{"true"}, Quiet: true})
	if err == nil || !strings.Contains(err.Error(), "not its SSO role") {
		t.Fatalf("err = %v", err)
	}
}

func TestPlanExecRefusesTheDefaultProfile(t *testing.T) {
	bin, _ := fakeAWS(t, ssoProfile)
	_, _, _, err := PlanExec(bin, workloadEnv, ExecOptions{Profile: "default", Login: "never", Argv: []string{"true"}})
	if err == nil || !strings.Contains(err.Error(), "default profile") {
		t.Fatalf("err = %v", err)
	}
}

// configparser lends [DEFAULT] keys to every section of the same file, in the config
// and the credentials file alike (verified with aws-cli 2.36.46: an SSO profile plus a
// [DEFAULT] role_arn/source_profile resolves through assume-role).
func TestPlanExecSeesKeysInheritedFromDEFAULT(t *testing.T) {
	for name, file := range map[string]string{"config": "config", "credentials": "credentials"} {
		bin, state := fakeAWS(t, ssoProfile)
		if err := os.WriteFile(filepath.Join(state, "loggedIn"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(os.Getenv("HOME"), ".aws", file)
		b, _ := os.ReadFile(path)
		b = append([]byte("[DEFAULT]\nrole_arn = arn:aws:iam::1:role/x\nsource_profile = src\n\n"), b...)
		if err := os.WriteFile(path, b, 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, _, err := PlanExec(bin, workloadEnv, ExecOptions{Profile: "prod", Settings: prodSettings, Login: "never", Argv: []string{"true"}, Quiet: true})
		if err == nil || !strings.Contains(err.Error(), "role_arn") {
			t.Errorf("[DEFAULT] in %s: err = %v", name, err)
		}
	}
}

// The CLI calls that obtain and check credentials see only the SSO-only config: no
// credentials file, no endpoint override (an AWS_ENDPOINT_URL left for a local emulator
// could otherwise answer as STS), and none of the member's other keys. The child does
// not get the endpoint overrides either (see TestPlanExecIsolatesTheChild).
func TestPlanExecObtainsCredentialsThroughAnSSOOnlyConfig(t *testing.T) {
	bin, state := fakeAWS(t, with(map[string]string{"raw:endpoint_url = http://127.0.0.1:1": "", "region": "eu-west-1"}))
	if err := os.WriteFile(filepath.Join(state, "loggedIn"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	environ := append(workloadEnv, "AWS_ENDPOINT_URL_STS=http://127.0.0.1:2", "AWS_ENDPOINT_URL=http://127.0.0.1:3",
		"AWS_SHARED_CREDENTIALS_FILE=/somewhere/credentials")
	_, _, env, err := PlanExec(bin, environ, ExecOptions{Profile: "prod", Settings: prodSettings, Login: "never", Argv: []string{"true"}, Quiet: true})
	if err != nil {
		t.Fatal(err)
	}
	want := "[sso-session \"af-prod\"]\nsso_registration_scopes = sso:account:access\nsso_start_url = https://example.awsapps.com/start\n" +
		"sso_region = ap-northeast-1\n\n[profile sso]\nsso_session = af-prod\nsso_account_id = 123456789012\nsso_role_name = Dev\n"
	for _, call := range []string{"configure-export-credentials", "sts-get-caller-identity"} {
		cfg, _ := os.ReadFile(filepath.Join(state, "seenConfig."+call))
		if string(cfg) != want {
			t.Fatalf("%s saw config:\n%s\nwant:\n%s", call, cfg, want)
		}
		seen, _ := os.ReadFile(filepath.Join(state, "seenEnv."+call))
		for _, bad := range []string{"AWS_ENDPOINT_URL", "/somewhere/credentials"} {
			if strings.Contains(string(seen), bad) {
				t.Fatalf("%s saw %s:\n%s", call, bad, seen)
			}
		}
		if !strings.Contains(string(seen), "AWS_IGNORE_CONFIGURED_ENDPOINT_URLS=true") || !strings.Contains(string(seen), "AWS_SHARED_CREDENTIALS_FILE="+os.DevNull) {
			t.Fatalf("%s env:\n%s", call, seen)
		}
	}
	if m := envMap(env); m["AWS_ENDPOINT_URL_STS"] != "" || m["AWS_ENDPOINT_URL"] != "" || m["AWS_REGION"] != "eu-west-1" {
		t.Fatalf("child env: want the profile's region and no endpoint override: %v", m)
	}
}

// Against the real CLI: with the verifier env, an endpoint override in the environment
// or in the config never receives the STS call. The same call without it does reach the
// fake server, which shows the probe works. Skipped where no aws CLI is installed.
func TestVerifierEnvKeepsSTSOffEndpointOverrides(t *testing.T) {
	aws, err := exec.LookPath("aws")
	if err != nil {
		t.Skip("aws CLI not installed")
	}
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "no", http.StatusForbidden)
	}))
	defer srv.Close()
	home := t.TempDir()
	cfg := filepath.Join(home, "config")
	if err := os.WriteFile(cfg, []byte("[profile sso]\naws_access_key_id = AKIAFAKE\naws_secret_access_key = fake\nendpoint_url = "+srv.URL+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "AWS_REGION=us-east-1",
		"AWS_MAX_ATTEMPTS=1", "AWS_EC2_METADATA_DISABLED=true"}
	run := func(env []string) int32 {
		hits.Store(0)
		cmd := exec.Command(aws, "sts", "get-caller-identity", "--profile", "sso", "--cli-connect-timeout", "3", "--cli-read-timeout", "3")
		cmd.Env = env
		_ = cmd.Run()
		return hits.Load()
	}
	for name, env := range map[string][]string{
		"environment": append(base, "AWS_ENDPOINT_URL_STS="+srv.URL, "AWS_CONFIG_FILE="+cfg),
		"config file": append(base, "AWS_CONFIG_FILE="+cfg),
	} {
		if run(env) == 0 {
			t.Fatalf("control (%s): the override was not used even without the verifier env", name)
		}
		if n := run(verifierEnv(env, cfg)); n != 0 {
			t.Fatalf("%s: the verifier env still sent %d request(s) to the override", name, n)
		}
	}
}

// By default the child gets a config that defines only the selected profile (whose
// credential_process returns the credentials in its environment), no credentials file
// and no endpoint override; --keep-aws-config keeps the member's own.
func TestPlanExecIsolatesTheChild(t *testing.T) {
	bin, state := fakeAWS(t, ssoProfile)
	if err := os.WriteFile(filepath.Join(state, "loggedIn"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	environ := append(workloadEnv, "AWS_SHARED_CREDENTIALS_FILE=/mine/credentials", "AWS_ENDPOINT_URL_STS=http://127.0.0.1:2")
	o := ExecOptions{Profile: "prod", Settings: prodSettings, Login: "never", Argv: []string{"true"}, Quiet: true,
		CredentialHelper: "/usr/local/bin/workspace-agent aws-env-credentials"}
	_, _, env, err := PlanExec(bin, environ, o)
	if err != nil {
		t.Fatal(err)
	}
	m := envMap(env)
	want := filepath.Join(ChildConfigDir(), "prod.config")
	if m["AWS_CONFIG_FILE"] != want || m["AWS_SHARED_CREDENTIALS_FILE"] != os.DevNull || m["AWS_REGION"] != "us-west-2" {
		t.Fatalf("child env = %v", m)
	}
	if _, ok := m["AWS_ENDPOINT_URL_STS"]; ok {
		t.Fatal("an endpoint override reached the child")
	}
	// The child's profile answers with exactly the credentials it was given.
	if b, err := EnvCredentials(env); err != nil || !strings.Contains(string(b), `"AccessKeyId":"ASIAFAKE"`) {
		t.Fatalf("the child's credential_process: %s %v", b, err)
	}
	cfg, _ := os.ReadFile(want)
	if !strings.Contains(string(cfg), "[profile \"prod\"]\ncredential_process = /usr/local/bin/workspace-agent aws-env-credentials\n") ||
		strings.Count(string(cfg), "[") != 1 || strings.Contains(string(cfg), "ASIA") {
		t.Fatalf("child config:\n%s", cfg)
	}

	o.CredentialHelper = ""
	if _, _, env, err = PlanExec(bin, environ, o); err != nil || envMap(env)["AWS_CONFIG_FILE"] != os.DevNull {
		t.Fatalf("no helper: %v %v", err, envMap(env))
	}

	o.KeepConfig = true
	if _, _, env, err = PlanExec(bin, environ, o); err != nil {
		t.Fatal(err)
	}
	if m := envMap(env); m["AWS_SHARED_CREDENTIALS_FILE"] != "/mine/credentials" || m["AWS_ENDPOINT_URL_STS"] == "" {
		t.Fatalf("--keep-aws-config child env = %v", m)
	}
}

func TestEnvCredentials(t *testing.T) {
	env := []string{"AF_AWS_EXEC_KEY_ID=ASIA1", "AWS_ACCESS_KEY_ID=ASIA1", "AWS_SECRET_ACCESS_KEY=s", "AWS_SESSION_TOKEN=t"}
	b, err := EnvCredentials(env)
	if err != nil || string(b) != `{"Version":1,"AccessKeyId":"ASIA1","SecretAccessKey":"s","SessionToken":"t"}` {
		t.Fatalf("%s %v", b, err)
	}
	for name, e := range map[string][]string{
		"no session token": {"AF_AWS_EXEC_KEY_ID=AKIA", "AWS_ACCESS_KEY_ID=AKIA", "AWS_SECRET_ACCESS_KEY=s"},
		// Plain env credentials (a workload's, say) outside af-aws-exec.
		"not from af-aws-exec": {"AWS_ACCESS_KEY_ID=ASIA1", "AWS_SECRET_ACCESS_KEY=s", "AWS_SESSION_TOKEN=t"},
		// A script exported another account's keys after af-aws-exec started it.
		"key changed": setEnv(env, "AWS_ACCESS_KEY_ID=ASIAOTHER"),
	} {
		if b, err := EnvCredentials(e); err == nil {
			t.Errorf("%s: handed out %s", name, b)
		}
	}
}

// Against the real CLI, in the child env: the selected profile resolves to the
// credentials in the environment through the credential_process, and any other
// profile is "not found" rather than read from the member's files. Skipped where no
// aws CLI is installed.
func TestChildEnvResolvesOnlyTheSelectedProfile(t *testing.T) {
	aws, err := exec.LookPath("aws")
	if err != nil {
		t.Skip("aws CLI not installed")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".aws"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".aws", "config"), []byte("[profile staging]\nregion = eu-west-1\naws_access_key_id = AKIASTAGING\naws_secret_access_key = x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(home, "helper.sh")
	script := "#!/bin/sh\nprintf '{\"Version\":1,\"AccessKeyId\":\"%s\",\"SecretAccessKey\":\"%s\",\"SessionToken\":\"%s\"}' \"$AWS_ACCESS_KEY_ID\" \"$AWS_SECRET_ACCESS_KEY\" \"$AWS_SESSION_TOKEN\"\n"
	if err := os.WriteFile(helper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	base := []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "AWS_EC2_METADATA_DISABLED=true",
		"AWS_ACCESS_KEY_ID=ASIACHILD", "AWS_SECRET_ACCESS_KEY=s", "AWS_SESSION_TOKEN=t"}
	child, _, err := childEnv(base, "prod", helper)
	if err != nil {
		t.Fatal(err)
	}
	export := func(env []string, profile string) (string, error) {
		cmd := exec.Command(aws, "configure", "export-credentials", "--profile", profile, "--format", "process")
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := export(base, "staging"); err != nil || !strings.Contains(out, "AKIASTAGING") {
		t.Fatalf("control: %s %v", out, err)
	}
	if out, err := export(child, "staging"); err == nil || !strings.Contains(out, "could not be found") {
		t.Fatalf("the child still resolved profile staging: %s %v", out, err)
	}
	if out, err := export(child, "prod"); err != nil || !strings.Contains(out, "ASIACHILD") {
		t.Fatalf("the child's own profile: %s %v", out, err)
	}
}

func TestPlanExecRefusesAnAmbiguousOrMismatchedName(t *testing.T) {
	bin, state := fakeAWS(t, ssoProfile)
	if err := os.WriteFile(filepath.Join(state, "loggedIn"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	same := prodSettings["prod"]
	other, role, portal, region := same, same, same, same
	other.AccountID, role.RoleName, portal.StartURL, region.SSORegion = "999999999999", "Admin", "https://other.awsapps.com/start", "us-east-1"
	slash := same
	slash.StartURL += "/"
	for name, c := range map[string]struct {
		o    ExecOptions
		want string // "" = admitted
	}{
		"matches Settings":                  {ExecOptions{Settings: map[string]Profile{"prod": same}}, ""},
		"matches, start URL with a slash":   {ExecOptions{Settings: map[string]Profile{"prod": slash}}, ""},
		"not a Settings profile":            {ExecOptions{Settings: map[string]Profile{}}, "--account"},
		"no Settings at all":                {ExecOptions{}, "--account"},
		"not in Settings, account pinned":   {ExecOptions{Settings: map[string]Profile{}, Account: "123456789012"}, ""},
		"account differs from Settings":     {ExecOptions{Settings: map[string]Profile{"prod": other}}, "rename one"},
		"role differs from Settings":        {ExecOptions{Settings: map[string]Profile{"prod": role}}, "rename one"},
		"portal differs from Settings":      {ExecOptions{Settings: map[string]Profile{"prod": portal}}, "rename one"},
		"SSO region differs from Settings":  {ExecOptions{Settings: map[string]Profile{"prod": region}}, "rename one"},
		"two labels":                        {ExecOptions{Conflicts: []Conflict{{Name: "prod", Labels: []string{"prod", "Prod"}}}}, "ambiguous"},
		"two labels, none in ~/.aws":        {ExecOptions{Conflicts: []Conflict{{Name: "gone", Labels: []string{"gone", "Gone"}}}}, "ambiguous"},
		"Settings, account pinned, matches": {ExecOptions{Settings: prodSettings, Account: "123456789012"}, ""},
		"Settings, account pinned, differs": {ExecOptions{Settings: prodSettings, Account: "999999999999"}, "--account"},
	} {
		o := c.o
		o.Profile, o.Login, o.Argv, o.Quiet = "prod", "never", []string{"true"}, true
		if len(o.Conflicts) > 0 {
			o.Profile = o.Conflicts[0].Name
		}
		_, _, _, err := PlanExec(bin, workloadEnv, o)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: refused: %v", name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
}

func TestShlexSplit(t *testing.T) {
	for in, want := range map[string]string{
		`profile prod`:          "profile|prod",
		`profile "prod"`:        "profile|prod",
		`profile 'my prod'`:     "profile|my prod",
		`profile "a\"b"`:        `profile|a"b`,
		`profile a\ b`:          "profile|a b",
		`  profile   prod  `:    "profile|prod",
		`profile "unterminated`: "ERR",
		`profile 'x`:            "ERR",
	} {
		w, err := shlexSplit(in)
		got := strings.Join(w, "|")
		if err != nil {
			got = "ERR"
		}
		if got != want {
			t.Errorf("shlexSplit(%q) = %q, want %q", in, got, want)
		}
	}
}

// A profile nested af-aws-exec is asked for must still be found: an outer run left
// AWS_CONFIG_FILE at the child's one-profile file or /dev/null, not at ~/.aws/config.
func TestPlanExecNestedUnderAnotherRun(t *testing.T) {
	bin, state := fakeAWS(t, ssoProfile)
	if err := os.WriteFile(filepath.Join(state, "loggedIn"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, outer := range []string{os.DevNull, filepath.Join(ChildConfigDir(), "other.config")} {
		environ := append(workloadEnv, "AWS_CONFIG_FILE="+outer, "AWS_SHARED_CREDENTIALS_FILE="+os.DevNull)
		if _, _, _, err := PlanExec(bin, environ, ExecOptions{Profile: "prod", Settings: prodSettings, Login: "never", Argv: []string{"true"}, Quiet: true}); err != nil {
			t.Errorf("outer AWS_CONFIG_FILE=%s: %v", outer, err)
		}
	}
}

// A profile neither file defines is reported as such, naming the files read, rather
// than as a mismatch with Settings (which would send the member to rename a profile
// that is fine).
func TestPlanExecReportsAnUndefinedProfile(t *testing.T) {
	bin, _ := fakeAWS(t, ssoProfile)
	mine := filepath.Join(t.TempDir(), "project.config")
	if err := os.WriteFile(mine, []byte("[profile other]\nregion = us-east-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := PlanExec(bin, append(workloadEnv, "AWS_CONFIG_FILE="+mine),
		ExecOptions{Profile: "prod", Settings: prodSettings, Login: "never", Argv: []string{"true"}})
	if err == nil || !strings.Contains(err.Error(), "not defined in "+mine) || strings.Contains(err.Error(), "rename") {
		t.Fatalf("err = %v", err)
	}
}

// Names with spaces: the generated headers are quoted, so the real CLI finds the
// sso-session and the child's profile (it said "session missing" before). Skipped
// where no aws CLI is installed.
func TestGeneratedConfigsKeepNamesWithSpaces(t *testing.T) {
	aws, err := exec.LookPath("aws")
	if err != nil {
		t.Skip("aws CLI not installed")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	ini, err := ssoOnlyConfig(ssoInfo{Session: "my session", StartURL: "https://example.awsapps.com/start", Region: "ap-northeast-1",
		Account: "123456789012", Role: "Dev"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(home, "sso.config")
	if err := os.WriteFile(cfg, []byte(ini), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(aws, "configure", "export-credentials", "--profile", ssoOnlyProfile)
	cmd.Env = verifierEnv([]string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}, cfg)
	out, _ := cmd.CombinedOutput()
	if !strings.Contains(string(out), "Token for my session does not exist") {
		t.Fatalf("the CLI did not reach the sso-session's token:\n%s", out)
	}

	helper := filepath.Join(home, "helper.sh")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf '{\"Version\":1,\"AccessKeyId\":\"ASIASPACE\",\"SecretAccessKey\":\"s\",\"SessionToken\":\"t\"}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	child, _, err := childEnv([]string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}, "my prod", helper)
	if err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command(aws, "configure", "export-credentials", "--profile", "my prod")
	cmd.Env = child
	if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "ASIASPACE") {
		t.Fatalf("child profile with a space: %s %v", out, err)
	}
	if env, warn, err := childEnv(nil, `bad"name`, helper); err != nil || warn == "" || envMap(env)["AWS_CONFIG_FILE"] != os.DevNull {
		t.Fatalf("a name the header cannot hold: %v %q %v", envMap(env), warn, err)
	}
}

// The directory the child's credential_process is read from must be the user's own and
// private. When it cannot be, the child still runs isolated, with an empty config and a
// warning, rather than the run failing and leaving --keep-aws-config as the way out.
func TestChildEnvFallsBackWhenTheDirectoryIsNotPrivate(t *testing.T) {
	for name, setup := range map[string]func(t *testing.T, home string){
		"linked directory": func(t *testing.T, home string) {
			shared := filepath.Join(home, "shared")
			mustMkdir(t, shared, 0o700)
			mustMkdir(t, filepath.Dir(ChildConfigDir()), 0o700)
			if err := os.Symlink(shared, ChildConfigDir()); err != nil {
				t.Fatal(err)
			}
		},
		"group-writable home": func(t *testing.T, home string) {
			if err := os.Chmod(home, 0o775); err != nil {
				t.Fatal(err)
			}
		},
		"state dir linked onto a world-writable directory": func(t *testing.T, home string) {
			shared := filepath.Join(home, "shared")
			mustMkdir(t, shared, 0o777)
			mustMkdir(t, filepath.Join(home, ".local"), 0o700)
			if err := os.Symlink(shared, filepath.Join(home, ".local", "state")); err != nil {
				t.Fatal(err)
			}
		},
	} {
		home := t.TempDir()
		t.Setenv("HOME", home)
		setup(t, home)
		env, warn, err := childEnv(nil, "prod", "/bin/true")
		if err != nil || !strings.Contains(warn, "empty AWS config") || envMap(env)["AWS_CONFIG_FILE"] != os.DevNull ||
			(name == "group-writable home" && !strings.Contains(warn, "chmod go-w "+home)) {
			t.Errorf("%s: %v %q %v", name, envMap(env), warn, err)
		}
	}

	// A private directory that was left open is tightened and used.
	home := t.TempDir()
	t.Setenv("HOME", home)
	mustMkdir(t, ChildConfigDir(), 0o777)
	env, warn, err := childEnv(nil, "prod", "/bin/true")
	if err != nil || warn != "" || envMap(env)["AWS_CONFIG_FILE"] != filepath.Join(ChildConfigDir(), "prod.config") {
		t.Fatalf("%v %q %v", envMap(env), warn, err)
	}
	if fi, _ := os.Stat(ChildConfigDir()); fi.Mode().Perm() != 0o700 {
		t.Fatalf("mode = %v", fi.Mode().Perm())
	}
}

func mustMkdir(t *testing.T, dir string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, mode); err != nil {
		t.Fatal(err)
	}
}

// Only the outer run's own isolation is undone for a nested run; a caller's own config
// keeps the /dev/null credentials file they paired it with.
func TestSteerKeepsACallersNullCredentialsFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	env, _ := steerIsolatedConfig([]string{"AWS_CONFIG_FILE=/work/project.config", "AWS_SHARED_CREDENTIALS_FILE=" + os.DevNull})
	if m := envMap(env); m["AWS_CONFIG_FILE"] != "/work/project.config" || m["AWS_SHARED_CREDENTIALS_FILE"] != os.DevNull {
		t.Fatalf("custom config: %v", m)
	}
	env, steered := steerIsolatedConfig([]string{"AWS_CONFIG_FILE=" + os.DevNull, "AWS_SHARED_CREDENTIALS_FILE=" + os.DevNull})
	if m := envMap(env); !steered || m["AWS_CONFIG_FILE"] != ConfigPath() || m["AWS_SHARED_CREDENTIALS_FILE"] != "" {
		t.Fatalf("nested: %v", m)
	}
}

// A file the CLI refuses to parse is refused here too, instead of guessing from it.
func TestPlanExecRefusesAConfigTheCLICannotParse(t *testing.T) {
	for name, extra := range map[string]string{
		"duplicate section": "\n[profile other]\nregion = a\n[profile other]\nregion = b\n",
		"duplicate key":     "\n[profile other]\nregion = a\nREGION = b\n",
	} {
		bin, state := fakeAWS(t, ssoProfile)
		if err := os.WriteFile(filepath.Join(state, "loggedIn"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(os.Getenv("HOME"), ".aws", "config")
		b, _ := os.ReadFile(path)
		if err := os.WriteFile(path, append(b, extra...), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, _, err := PlanExec(bin, workloadEnv, ExecOptions{Profile: "prod", Settings: prodSettings, Login: "never", Argv: []string{"true"}, Quiet: true})
		if err == nil || !strings.Contains(err.Error(), "cannot read") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	// What configparser allows: [DEFAULT] twice, a key continued over several lines.
	if err := iniStrict("[DEFAULT]\na = 1\n[DEFAULT]\nb = 2\n[profile x]\ns3 =\n  a = 1\n  a = 2\n"); err != nil {
		t.Fatalf("false positive: %v", err)
	}
}

// Nested runs and a caller's own AWS_REGION: the child env holds each variable once, so
// getenv (first entry wins) sees the values af-aws-exec set.
func TestPlanExecNeverDuplicatesAChildVariable(t *testing.T) {
	bin, state := fakeAWS(t, ssoProfile)
	if err := os.WriteFile(filepath.Join(state, "loggedIn"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	environ := append(workloadEnv, "AWS_REGION=us-east-1", "AWS_DEFAULT_REGION=us-east-1",
		"AF_AWS_EXEC_KEY_ID=ASIAOUTER", "AWS_SESSION_TOKEN=outer", "AWS_CREDENTIAL_EXPIRATION=2020-01-01T00:00:00Z")
	for _, keep := range []bool{false, true} {
		_, _, env, err := PlanExec(bin, environ, ExecOptions{Profile: "prod", Settings: prodSettings, Region: "eu-central-1",
			Login: "never", Argv: []string{"true"}, Quiet: true, KeepConfig: keep})
		if err != nil {
			t.Fatal(err)
		}
		m := envMap(env) // panics on a duplicate
		if m["AWS_REGION"] != "eu-central-1" || m["AWS_DEFAULT_REGION"] != "eu-central-1" || m["AF_AWS_EXEC_KEY_ID"] != "ASIAFAKE" {
			t.Fatalf("keep=%v: child env %v", keep, m)
		}
		if b, err := EnvCredentials(env); err != nil || !strings.Contains(string(b), "ASIAFAKE") {
			t.Fatalf("keep=%v: nested credential_process: %s %v", keep, b, err)
		}
	}
}

// A ~/.aws linked onto other storage (the workspace allows it) is never followed to
// write the child's config: that lives in the Agent's state directory.
func TestChildConfigIsNotWrittenThroughALinkedAWSDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	shared := filepath.Join(home, "shared")
	mustMkdir(t, shared, 0o777)
	if err := os.Symlink(shared, filepath.Join(home, ".aws")); err != nil {
		t.Fatal(err)
	}
	env, warn, err := childEnv(nil, "prod", "/bin/true")
	if err != nil || warn != "" || envMap(env)["AWS_CONFIG_FILE"] != filepath.Join(ChildConfigDir(), "prod.config") {
		t.Fatalf("%v %q %v", envMap(env), warn, err)
	}
	if entries, _ := os.ReadDir(shared); len(entries) != 0 {
		t.Fatalf("wrote into the linked ~/.aws: %v", entries)
	}
}

// Region precedence: --region, then what the caller exported, then the profile's; a lone
// AWS_DEFAULT_REGION is copied to AWS_REGION for the SDKs that read only that.
func TestPlanExecRegionPrecedence(t *testing.T) {
	bin, state := fakeAWS(t, ssoProfile) // profile region us-west-2
	if err := os.WriteFile(filepath.Join(state, "loggedIn"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		flag string
		env  []string
		want string
	}{
		"profile":                              {"", nil, "us-west-2"},
		"flag beats env":                       {"eu-central-1", []string{"AWS_REGION=us-east-1"}, "eu-central-1"},
		"exported beats profile":               {"", []string{"AWS_REGION=us-east-1"}, "us-east-1"},
		"only AWS_DEFAULT_REGION":              {"", []string{"AWS_DEFAULT_REGION=ap-south-1"}, "ap-south-1"},
		"AWS_REGION beats a different default": {"", []string{"AWS_REGION=us-east-1", "AWS_DEFAULT_REGION=ap-south-1"}, "us-east-1"},
	} {
		_, _, env, err := PlanExec(bin, append(workloadEnv, c.env...), ExecOptions{Profile: "prod", Settings: prodSettings,
			Region: c.flag, Login: "never", Argv: []string{"true"}, Quiet: true})
		if err != nil {
			t.Fatal(err)
		}
		if m := envMap(env); m["AWS_REGION"] != c.want || m["AWS_DEFAULT_REGION"] != c.want {
			t.Errorf("%s: AWS_REGION = %q, AWS_DEFAULT_REGION = %q, want %q", name, m["AWS_REGION"], m["AWS_DEFAULT_REGION"], c.want)
		}
	}
}

// The login hint is pasted into a shell; a profile name is quoted in it.
func TestLoginHintQuotesTheProfileName(t *testing.T) {
	bin, _ := fakeAWS(t, ssoProfile)
	path := filepath.Join(os.Getenv("HOME"), ".aws", "config")
	b, _ := os.ReadFile(path)
	b = append(b, "\n[profile \"p; echo X\"]\nsso_session = af-prod\nsso_account_id = 123456789012\nsso_role_name = Dev\n"...)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := PlanExec(bin, workloadEnv, ExecOptions{Profile: "p; echo X", Account: "123456789012", Settings: prodSettings,
		Login: "never", Argv: []string{"true"}})
	if !errors.Is(err, ErrLoginRequired) || !strings.Contains(err.Error(), "--profile 'p; echo X' --use-device-code") {
		t.Fatalf("err = %v", err)
	}
}

// Files the CLI refuses to read are refused here too, whatever profile follows the
// broken part. Each case is checked against iniStrict and, where installed, against
// the real CLI (which must fail on it as well, or the case is wrong).
func TestINIStrictRefusesWhatTheCLIRefuses(t *testing.T) {
	good := "[profile p]\nsso_session = s\nregion = us-east-1\n"
	cases := map[string]string{
		"byte-order mark":      "\ufeff" + good,
		"invalid UTF-8":        "[profile x]\nregion = \xff\n" + good,
		"bare line":            "[profile x]\njust words\n" + good,
		"key before a section": "region = us-east-1\n" + good,
		"empty header":         "[]\n" + good,
	}
	aws, lookErr := exec.LookPath("aws")
	for name, text := range cases {
		if err := iniStrict(text); err == nil {
			t.Errorf("%s: iniStrict accepted it", name)
		}
		if lookErr != nil {
			continue
		}
		home := t.TempDir()
		mustMkdir(t, filepath.Join(home, ".aws"), 0o700)
		if err := os.WriteFile(filepath.Join(home, ".aws", "config"), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(aws, "configure", "get", "region", "--profile", "p")
		cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
		if out, err := cmd.CombinedOutput(); err == nil {
			t.Errorf("%s: the real CLI accepted it (%q), so refusing it would be a false positive", name, out)
		}
	}
	if err := iniStrict(good); err != nil {
		t.Fatalf("false positive: %v", err)
	}
}
