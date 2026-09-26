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

// readSection adds the top-level key = value pairs of every section named header in
// the INI file at path to keys (a repeated section merges, as in botocore). Indented
// lines are sub-settings of the key above them (s3 = ...) and comments start with #
// or ;. The config parser in botocore compares the section name after collapsing
// whitespace, so this does too.
func readSection(path, header string, keys map[string]string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	in := false
	for _, line := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		if m := sectionRe.FindStringSubmatch(line); m != nil {
			in = strings.Join(strings.Fields(m[1]), " ") == header
			continue
		}
		if !in || line == "" || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		t := strings.TrimSpace(line)
		if t == "" || t[0] == '#' || t[0] == ';' {
			continue
		}
		k, v, ok := strings.Cut(t, "=")
		if !ok {
			continue
		}
		if k, v = strings.TrimSpace(k), strings.TrimSpace(v); v != "" {
			keys[strings.ToLower(k)] = v
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
	env := steerIsolatedConfig(baseEnv(environ))
	aws := awsRunner{bin: awsBin, env: env}
	keys := profileKeys(env, o.Profile)
	if err := checkSSOProfile(keys, o.Profile); err != nil {
		return "", nil, nil, err
	}

	creds, err := exportCreds(aws, o.Profile)
	if err != nil {
		login := o.Login == "always" || (o.Login != "never" && o.Interactive)
		if !login {
			return "", nil, nil, fmt.Errorf("%w for profile %q: %v\nlog in with: aws sso login --profile %s --use-device-code --no-browser",
				ErrLoginRequired, o.Profile, err, o.Profile)
		}
		if lerr := deviceLogin(awsBin, env, o.Profile, o.Stderr); lerr != nil {
			return "", nil, nil, fmt.Errorf("aws sso login --profile %s: %w", o.Profile, lerr)
		}
		if creds, err = exportCreds(aws, o.Profile); err != nil {
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

	// Ask with the exported credentials themselves, so the principal printed is the one
	// the child will actually use rather than a second resolution of the profile.
	if !o.Quiet && o.Stderr != nil {
		who := awsRunner{bin: awsBin, env: env}
		if arn, err := who.out("sts", "get-caller-identity", "--query", "Arn", "--output", "text"); err == nil {
			exp := ""
			if creds.Expiration != "" {
				exp = " (expires " + creds.Expiration + ")"
			}
			fmt.Fprintf(o.Stderr, "af-aws-exec: running as %s%s\n", arn, exp)
		}
	}

	prog, err := exec.LookPath(o.Argv[0])
	if err != nil {
		return "", nil, nil, err
	}
	return prog, o.Argv, env, nil
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
