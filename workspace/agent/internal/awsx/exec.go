package awsx

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// ExecOptions is one `af-aws-exec` invocation.
type ExecOptions struct {
	Profile string
	Region  string // "" = the profile's own region, unless the caller already exported one
	// Login: "auto" logs in only when a person is at a terminal to approve the code,
	// "always" logs in whenever the cached SSO login is not usable, "never" fails with
	// the command to run instead. An agent's shell has no terminal, and blocking it for
	// the whole device-code expiry would look like a hang.
	Login string
	Quiet bool
	Argv  []string

	Stderr      io.Writer
	Interactive bool // stdin and stderr are terminals
}

// ErrLoginRequired means the SSO login is missing or expired and no login was attempted.
var ErrLoginRequired = errors.New("SSO login required")

// scrubbedEnv lists every variable through which the default credential chain could
// pick an identity other than the requested SSO profile. The container can have a
// workload role (ECS task role via the container credentials endpoint, or the EC2
// instance role via IMDS); a missing or expired SSO login must fail, not quietly run
// the command as that role.
var scrubbedEnv = []string{
	"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_SECURITY_TOKEN",
	"AWS_CREDENTIAL_EXPIRATION", "AWS_PROFILE", "AWS_DEFAULT_PROFILE",
	"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "AWS_CONTAINER_CREDENTIALS_FULL_URI",
	"AWS_CONTAINER_AUTHORIZATION_TOKEN", "AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE",
	"AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_ROLE_ARN", "AWS_ROLE_SESSION_NAME",
	"AWS_EC2_METADATA_DISABLED", "AWS_CREDENTIAL_FILE", "BOTO_CONFIG",
}

// baseEnv is environ minus scrubbedEnv, with IMDS turned off for every SDK that honours
// the variable (the CLI, boto3, the Go/JS/Java SDKs).
func baseEnv(environ []string) []string {
	drop := map[string]bool{}
	for _, k := range scrubbedEnv {
		drop[k] = true
	}
	out := make([]string, 0, len(environ)+1)
	for _, kv := range environ {
		k, _, _ := strings.Cut(kv, "=")
		if !drop[k] {
			out = append(out, kv)
		}
	}
	return append(out, "AWS_EC2_METADATA_DISABLED=true")
}

// steerIsolatedConfig replaces an AWS_CONFIG_FILE that points at one of the Agent's own
// isolated files (an SSM session pane exports its per-session config) with the default
// config, where the exported profiles live. A config file the person chose themselves
// is kept.
func steerIsolatedConfig(env []string) []string {
	aws := filepath.Dir(ConfigPath())
	for i, kv := range env {
		v, ok := strings.CutPrefix(kv, "AWS_CONFIG_FILE=")
		if !ok {
			continue
		}
		dir := realPath(filepath.Dir(expandHome(v)))
		for _, d := range []string{"af-sessions", "af-ops"} {
			if dir == realPath(filepath.Join(aws, d)) {
				env[i] = "AWS_CONFIG_FILE=" + ConfigPath()
			}
		}
	}
	return env
}

// realPath resolves symlinks where it can, so a ~/.aws linked onto other storage still
// matches whichever spelling of the path a caller exported.
func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// expandHome expands a leading "~/" the way the AWS CLI does for its file variables.
func expandHome(p string) string {
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		return filepath.Join(paths.HomeDir(), rest)
	}
	return p
}

func envValue(env []string, key string) string {
	v := ""
	for _, kv := range env {
		if k, val, _ := strings.Cut(kv, "="); k == key {
			v = val // last wins, as the child would see it
		}
	}
	return v
}

// profileKeys returns the keys the CLI would merge for profile: its section of the
// config file ([profile X], or [default]) and its section of the credentials file
// ([X]), both located the way the CLI locates them from env. Read here rather than
// through `aws configure get`: each of those is a CLI start (~0.8 s measured), and ten
// of them put eight seconds in front of every af-aws-exec.
func profileKeys(env []string, profile string) map[string]string {
	cfg := envValue(env, "AWS_CONFIG_FILE")
	if cfg == "" {
		cfg = ConfigPath()
	}
	creds := envValue(env, "AWS_SHARED_CREDENTIALS_FILE")
	if creds == "" {
		creds = filepath.Join(filepath.Dir(ConfigPath()), "credentials")
	}
	keys := map[string]string{}
	header := "profile " + profile
	if profile == "default" {
		header = "default"
	}
	readSection(expandHome(cfg), header, keys)
	readSection(expandHome(creds), profile, keys)
	return keys
}

// readSection adds the top-level key/value pairs of the section named header in the
// INI file at path to keys. Headers are compared after collapsing whitespace, and when
// several headers name the same profile the last one replaces the others rather than
// merging with them (both as the AWS CLI does; checked against the real CLI in
// TestProfileKeysAgreesWithTheRealAWSCLI). Indented lines are sub-settings of the key
// above them (s3 = ...) and comments start with # or ;.
func readSection(path, header string, keys map[string]string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	// configparser's [DEFAULT] (exactly that spelling) lends its keys to every section
	// of the same file, so a role_arn there is live in each profile.
	var sect map[string]string
	defaults := map[string]string{}
	defer func() {
		if sect == nil {
			return
		}
		for k, v := range defaults {
			keys[k] = v
		}
		for k, v := range sect {
			keys[k] = v
		}
	}()
	in, inDefault := false, false
	for _, line := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		if m := sectionRe.FindStringSubmatch(line); m != nil {
			in = strings.Join(strings.Fields(m[1]), " ") == header
			inDefault = m[1] == "DEFAULT"
			if in {
				sect = map[string]string{}
			}
			continue
		}
		if (!in && !inDefault) || line == "" || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		t := strings.TrimSpace(line)
		if t == "" || t[0] == '#' || t[0] == ';' {
			continue
		}
		// configparser (which the AWS CLI reads these files with) accepts ":" as well as
		// "=" and splits at whichever comes first; `role_arn: x` is as live as
		// `role_arn = x` (verified with aws-cli 2.36.46).
		i := strings.IndexAny(t, "=:")
		if i < 0 {
			continue
		}
		if k, v := strings.TrimSpace(t[:i]), strings.TrimSpace(t[i+1:]); v != "" {
			if inDefault {
				defaults[strings.ToLower(k)] = v
			} else {
				sect[strings.ToLower(k)] = v
			}
		}
	}
}

// checkSSOProfile admits only a profile the CLI will resolve through its SSO provider
// and nothing else. Having sso_session alone is not enough: botocore's SSO provider
// only claims a profile that also names the account and role, and otherwise the chain
// falls through to ~/.aws/credentials, credential_process and the rest (measured with
// aws-cli 2.36.46: sso_session without an account plus a credential_process in
// ~/.aws/credentials handed the child the process's keys). The assume-role and
// web-identity providers run before SSO, so a profile carrying their keys is refused
// too.
// IsSSORoleARN reports whether arn is a session of the IAM Identity Center role for
// permission set role in account. Identity Center names that role
// AWSReservedSSO_<permission set>_<16 hex> (permission set names are at most 32
// characters, so the name is never truncated), and a session of it reads
// arn:<partition>:sts::<account>:assumed-role/<role>/<session>.
func IsSSORoleARN(arn, account, role string) bool {
	if account == "" || role == "" {
		return false
	}
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) != 6 || parts[0] != "arn" || !strings.HasPrefix(parts[1], "aws") || parts[2] != "sts" || parts[4] != account {
		return false
	}
	res := strings.Split(parts[5], "/")
	if len(res) != 3 || res[0] != "assumed-role" || res[2] == "" {
		return false
	}
	suffix, ok := strings.CutPrefix(res[1], "AWSReservedSSO_"+role+"_")
	if !ok || len(suffix) != 16 {
		return false
	}
	for _, c := range suffix {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

func checkSSOProfile(keys map[string]string, profile string) error {
	if keys["sso_session"] == "" && keys["sso_start_url"] == "" {
		return fmt.Errorf("profile %q is not an SSO profile in the AWS config (af-aws-exec only passes SSO credentials; see `af-aws-exec --list`)", profile)
	}
	if keys["sso_account_id"] == "" || keys["sso_role_name"] == "" {
		return fmt.Errorf("profile %q has no SSO account and role; set both on the profile in Settings > SSM", profile)
	}
	for _, k := range []string{"role_arn", "source_profile", "credential_source", "credential_process", "web_identity_token_file", "aws_access_key_id"} {
		if keys[k] != "" {
			return fmt.Errorf("profile %q also sets %s, so the AWS CLI would not use its SSO login; af-aws-exec refuses it", profile, k)
		}
	}
	return nil
}

func envHas(environ []string, key string) bool {
	for _, kv := range environ {
		if k, v, _ := strings.Cut(kv, "="); k == key && v != "" {
			return true
		}
	}
	return false
}

// awsRunner runs the aws CLI with env and returns stdout. Stderr is captured for the
// error message; the CLI never prints credentials there.
type awsRunner struct {
	bin string
	env []string
}

func (r awsRunner) out(args ...string) (string, error) {
	cmd := exec.Command(r.bin, args...)
	cmd.Env = r.env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New(msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}

type processCreds struct {
	Version         int    `json:"Version"`
	AccessKeyID     string `json:"AccessKeyId"`
	SecretAccessKey string `json:"SecretAccessKey"`
	SessionToken    string `json:"SessionToken"`
	Expiration      string `json:"Expiration"`
}

// PlanExec resolves the SSO credentials for o.Profile and returns the program, argv and
// environment to exec. The credentials exist only in the returned env: nothing here
// writes them to a file or prints them.
func PlanExec(awsBin string, environ []string, o ExecOptions) (string, []string, []string, error) {
	if o.Profile == "" {
		return "", nil, nil, errors.New("--profile is required")
	}
	if len(o.Argv) == 0 {
		return "", nil, nil, errors.New("no command given after --")
	}
	// The CLI resolves "default" from [profile default] when that exists and from
	// [default] otherwise, and every bare command already uses it; a Settings profile is
	// never exported under that name, so there is nothing here to pick it for.
	if o.Profile == "default" {
		return "", nil, nil, errors.New("af-aws-exec does not run under the default profile; name the SSO profile from Settings (`af-aws-exec --list`)")
	}
	env := steerIsolatedConfig(baseEnv(environ))
	keys := profileKeys(env, o.Profile)
	if err := checkSSOProfile(keys, o.Profile); err != nil {
		return "", nil, nil, err
	}

	// Credentials come from a config written here that holds nothing but the profile's
	// validated SSO fields, with the credentials file and every endpoint override
	// switched off. Asking the CLI to resolve the member's own profile would make this
	// only as safe as profileKeys' imitation of the CLI's parser, and each review round
	// found another divergence that let assume-role or a process provider win.
	ini, err := ssoOnlyConfig(env, keys)
	if err != nil {
		return "", nil, nil, fmt.Errorf("profile %q: %w", o.Profile, err)
	}
	dir, err := os.MkdirTemp("", "af-aws-exec-")
	if err != nil {
		return "", nil, nil, err
	}
	defer os.RemoveAll(dir)
	cfg := filepath.Join(dir, "config")
	if err := os.WriteFile(cfg, []byte(ini), 0o600); err != nil {
		return "", nil, nil, err
	}
	aws := awsRunner{bin: awsBin, env: verifierEnv(env, cfg)}

	creds, err := exportCreds(aws, ssoOnlyProfile)
	if err != nil {
		login := o.Login == "always" || (o.Login != "never" && o.Interactive)
		if !login {
			return "", nil, nil, fmt.Errorf("%w for profile %q: %v\nlog in with: aws sso login --profile %s --use-device-code --no-browser",
				ErrLoginRequired, o.Profile, err, o.Profile)
		}
		if lerr := deviceLogin(awsBin, aws.env, ssoOnlyProfile, o.Stderr); lerr != nil {
			return "", nil, nil, fmt.Errorf("aws sso login for profile %s: %w", o.Profile, lerr)
		}
		if creds, err = exportCreds(aws, ssoOnlyProfile); err != nil {
			return "", nil, nil, fmt.Errorf("credentials for profile %q after login: %w", o.Profile, err)
		}
	}

	region := o.Region
	if region == "" && !envHas(env, "AWS_REGION") && !envHas(env, "AWS_DEFAULT_REGION") {
		region = keys["region"]
	}
	if region != "" {
		env = append(env, "AWS_REGION="+region, "AWS_DEFAULT_REGION="+region)
	}
	env = append(env,
		"AWS_ACCESS_KEY_ID="+creds.AccessKeyID,
		"AWS_SECRET_ACCESS_KEY="+creds.SecretAccessKey,
		"AWS_SESSION_TOKEN="+creds.SessionToken)
	if creds.Expiration != "" {
		env = append(env, "AWS_CREDENTIAL_EXPIRATION="+creds.Expiration)
	}

	// Confirm the credentials are a session of the profile's permission-set role in its
	// account. With the SSO-only config this is a consistency check, not the proof of
	// provenance (an ARN shows a role name and account, not that Identity Center issued
	// it). It runs with the verifier env so no endpoint override can answer in AWS's place.
	who := awsRunner{bin: awsBin, env: verifierEnv(env, cfg)}
	who.env = append(who.env,
		"AWS_ACCESS_KEY_ID="+creds.AccessKeyID,
		"AWS_SECRET_ACCESS_KEY="+creds.SecretAccessKey,
		"AWS_SESSION_TOKEN="+creds.SessionToken)
	arn, err := who.out("sts", "get-caller-identity", "--query", "Arn", "--output", "text")
	if err != nil {
		return "", nil, nil, fmt.Errorf("could not confirm the identity of the credentials for profile %q: %v", o.Profile, err)
	}
	if !IsSSORoleARN(arn, keys["sso_account_id"], keys["sso_role_name"]) {
		return "", nil, nil, fmt.Errorf("profile %q resolved to %s, not its SSO role %s in account %s; af-aws-exec refuses it",
			o.Profile, arn, keys["sso_role_name"], keys["sso_account_id"])
	}
	if !o.Quiet && o.Stderr != nil {
		exp := ""
		if creds.Expiration != "" {
			exp = " (expires " + creds.Expiration + ")"
		}
		fmt.Fprintf(o.Stderr, "af-aws-exec: running as %s%s\n", arn, exp)
	}

	prog, err := exec.LookPath(o.Argv[0])
	if err != nil {
		return "", nil, nil, err
	}
	return prog, o.Argv, env, nil
}

// ssoOnlyProfile is the one profile name in the SSO-only config; the member's profile
// name never enters that file.
const ssoOnlyProfile = "sso"

var (
	iniValueRe   = regexp.MustCompile(`^[!-~]+$`)
	iniSessionRe = regexp.MustCompile(`^[A-Za-z0-9._@+ -]+$`)
)

// ssoOnlyConfig renders the minimal config the CLI needs to turn the profile's SSO
// login into role credentials, and nothing else. The sso-session keeps its own name so
// the CLI finds the token an ordinary `aws sso login` cached for it. Values come from
// single parsed lines, but are still held to printable, space-free ASCII so none can
// add a key.
func ssoOnlyConfig(env []string, keys map[string]string) (string, error) {
	var b strings.Builder
	var startURL, ssoRegion string
	session := keys["sso_session"]
	if session != "" {
		sess := map[string]string{}
		cfg := envValue(env, "AWS_CONFIG_FILE")
		if cfg == "" {
			cfg = ConfigPath()
		}
		readSection(expandHome(cfg), "sso-session "+session, sess)
		startURL, ssoRegion = sess["sso_start_url"], sess["sso_region"]
		if !iniSessionRe.MatchString(session) {
			return "", fmt.Errorf("sso-session name %q is not usable", session)
		}
		fmt.Fprintf(&b, "[sso-session %s]\n", session)
		if sc := sess["sso_registration_scopes"]; sc != "" {
			if !iniValueRe.MatchString(strings.ReplaceAll(sc, " ", "")) {
				return "", errors.New("sso_registration_scopes is not usable")
			}
			fmt.Fprintf(&b, "sso_registration_scopes = %s\n", sc)
		}
	} else {
		startURL, ssoRegion = keys["sso_start_url"], keys["sso_region"]
	}
	fields := [][2]string{
		{"sso_start_url", startURL}, {"sso_region", ssoRegion},
		{"sso_account_id", keys["sso_account_id"]}, {"sso_role_name", keys["sso_role_name"]},
	}
	for _, f := range fields {
		if !iniValueRe.MatchString(f[1]) {
			return "", fmt.Errorf("%s is missing or not usable", f[0])
		}
	}
	if session != "" {
		fmt.Fprintf(&b, "sso_start_url = %s\nsso_region = %s\n\n[profile %s]\nsso_session = %s\n", startURL, ssoRegion, ssoOnlyProfile, session)
	} else {
		fmt.Fprintf(&b, "[profile %s]\nsso_start_url = %s\nsso_region = %s\n", ssoOnlyProfile, startURL, ssoRegion)
	}
	fmt.Fprintf(&b, "sso_account_id = %s\nsso_role_name = %s\n", keys["sso_account_id"], keys["sso_role_name"])
	return b.String(), nil
}

// verifierEnv is env for the CLI calls that obtain and check credentials: the SSO-only
// config, no credentials file, and no endpoint override from the environment or any
// config. An AWS_ENDPOINT_URL left in a shell for a local emulator would otherwise
// receive the SSO and STS calls (and the session token) and could answer as AWS
// (verified with aws-cli 2.36.46: a local server's forged ARN came back with exit 0).
// The child command keeps the member's own settings.
func verifierEnv(env []string, cfg string) []string {
	out := make([]string, 0, len(env)+3)
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if k == "AWS_CONFIG_FILE" || k == "AWS_SHARED_CREDENTIALS_FILE" || k == "AWS_IGNORE_CONFIGURED_ENDPOINT_URLS" ||
			strings.HasPrefix(k, "AWS_ENDPOINT_URL") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "AWS_CONFIG_FILE="+cfg, "AWS_SHARED_CREDENTIALS_FILE="+os.DevNull,
		"AWS_IGNORE_CONFIGURED_ENDPOINT_URLS=true")
}

func exportCreds(aws awsRunner, profile string) (processCreds, error) {
	out, err := aws.out("configure", "export-credentials", "--profile", profile, "--format", "process")
	if err != nil {
		return processCreds{}, err
	}
	var c processCreds
	if err := json.Unmarshal([]byte(out), &c); err != nil {
		// Never echo out: it is the credential document.
		return processCreds{}, errors.New("aws configure export-credentials returned something that is not credentials JSON")
	}
	if c.AccessKeyID == "" || c.SecretAccessKey == "" || c.SessionToken == "" {
		return processCreds{}, errors.New("aws configure export-credentials returned no temporary credentials")
	}
	return c, nil
}

// deviceLogin runs the device-code login with the person's terminal attached. Its
// stdout goes to stderr so the wrapped command's stdout stays clean for pipes. The
// default authorization-code flow redirects the browser to a 127.0.0.1 listener inside
// this container, which the person's browser cannot reach.
func deviceLogin(awsBin string, env []string, profile string, stderr io.Writer) error {
	if stderr == nil {
		stderr = os.Stderr
	}
	fmt.Fprintln(stderr, "af-aws-exec: SSO login needed. Approve only a code you started yourself just now.")
	cmd := exec.Command(awsBin, "sso", "login", "--profile", profile, "--use-device-code", "--no-browser")
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, stderr, stderr
	return cmd.Run()
}
