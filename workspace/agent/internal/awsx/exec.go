package awsx

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
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
	// Account, when set, must be the profile's SSO account; anything else is refused
	// before a login or a credential is fetched. For scripts and agents that know which
	// account a command belongs to.
	Account string
	// KeepConfig hands the child the member's own AWS config and credentials files. By
	// default the child gets empty ones, so a tool that names a profile of its own
	// (Terraform's `profile =`, a CDK --profile, AWS_PROFILE in a script) fails with
	// "profile not found" instead of quietly running under that other profile.
	KeepConfig bool
	// CredentialHelper is the credential_process command of the child's one-profile
	// config (see childEnv); "" gives the child an empty config instead.
	CredentialHelper string
	// Settings and Conflicts come from the sync that preceded the run (nil when the CP
	// could not be asked). They let a Settings name that the member's own ~/.aws
	// definition shadows, or that two Settings labels share, be refused.
	Settings  map[string]Profile
	Conflicts []Conflict

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
	"AWS_EC2_METADATA_DISABLED", "AWS_CREDENTIAL_FILE", "BOTO_CONFIG", execKeyIDVar,
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
func steerIsolatedConfig(env []string) ([]string, bool) {
	aws := filepath.Dir(ConfigPath())
	steered, fromChild := false, false
	out := env[:0:0]
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if k != "AWS_CONFIG_FILE" {
			out = append(out, kv)
			continue
		}
		// /dev/null and ChildConfigDir are af-aws-exec's own child isolation: a nested
		// af-aws-exec (a Makefile target run under an outer one) should still find the
		// profiles.
		dir := realPath(filepath.Dir(expandHome(v)))
		switch {
		case v == os.DevNull || dir == realPath(ChildConfigDir()):
			kv, steered, fromChild = "AWS_CONFIG_FILE="+ConfigPath(), true, true
		case dir == realPath(filepath.Join(aws, "af-sessions")) || dir == realPath(filepath.Join(aws, "af-ops")):
			kv, steered = "AWS_CONFIG_FILE="+ConfigPath(), true
		}
		out = append(out, kv)
	}
	// The outer run's AWS_SHARED_CREDENTIALS_FILE=/dev/null goes with its config. Next
	// to a config file the caller chose, a /dev/null credentials file is also their
	// choice: dropping it would let ~/.aws/credentials add keys to their profile.
	if fromChild {
		kept := out[:0]
		for _, kv := range out {
			if kv != "AWS_SHARED_CREDENTIALS_FILE="+os.DevNull {
				kept = append(kept, kv)
			}
		}
		out = kept
	}
	return out, steered
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

// envValue is getenv over env: the FIRST entry of a duplicated key, as C, Python and
// Go's own os.Getenv resolve it, so this reads what the aws CLI will read.
func envValue(env []string, key string) string {
	for _, kv := range env {
		if k, val, _ := strings.Cut(kv, "="); k == key {
			return val
		}
	}
	return ""
}

// profileKeys returns the keys the CLI would merge for profile: its section of the
// config file ([profile X], or [default]) and its section of the credentials file
// ([X]), both located the way the CLI locates them from env. Read here rather than
// through `aws configure get`: each of those is a CLI start (~0.8 s measured), and ten
// of them put eight seconds in front of every af-aws-exec.
func profileKeys(env []string, profile string) (map[string]string, error) {
	cfg := envValue(env, "AWS_CONFIG_FILE")
	if cfg == "" {
		cfg = ConfigPath()
	}
	creds := envValue(env, "AWS_SHARED_CREDENTIALS_FILE")
	if creds == "" {
		creds = filepath.Join(filepath.Dir(ConfigPath()), "credentials")
	}
	keys := map[string]string{}
	if err := readINISection(expandHome(cfg), configPicker("profile", profile), keys); err != nil {
		return nil, err
	}
	if err := readINISection(expandHome(creds), credentialsPicker(profile), keys); err != nil {
		return nil, err
	}
	return keys, nil
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

// setEnv sets each KEY=value in env, removing every earlier entry for that key. Never
// append a variable that may already be there: getenv in C, Python and the AWS CLI
// returns the FIRST entry of a duplicated key, so an appended AWS_REGION loses to the
// caller's (measured with aws-cli 2.36.46: --region was ignored).
func setEnv(env []string, kvs ...string) []string {
	drop := map[string]bool{}
	for _, kv := range kvs {
		k, _, _ := strings.Cut(kv, "=")
		drop[k] = true
	}
	out := make([]string, 0, len(env)+len(kvs))
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); !drop[k] {
			out = append(out, kv)
		}
	}
	return append(out, kvs...)
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
	Expiration      string `json:"Expiration,omitempty"`
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
	env, steered := steerIsolatedConfig(baseEnv(environ))
	keys, err := profileKeys(env, o.Profile)
	if err != nil {
		return "", nil, nil, err
	}
	if err := checkAmbiguous(o); err != nil {
		return "", nil, nil, err
	}
	if len(keys) == 0 {
		return "", nil, nil, notDefined(env, o)
	}
	// Identity first: a colliding or shadowed name is the more useful answer even when
	// the definition found is not an SSO profile at all.
	sso, err := resolveSSO(env, keys)
	if err != nil {
		return "", nil, nil, err
	}
	if err := checkIdentity(sso, o); err != nil {
		return "", nil, nil, err
	}
	if err := checkSSOProfile(keys, o.Profile); err != nil {
		return "", nil, nil, err
	}

	// Credentials come from a config written here that holds nothing but the profile's
	// validated SSO fields, with the credentials file and every endpoint override
	// switched off. Asking the CLI to resolve the member's own profile would make this
	// only as safe as profileKeys' imitation of the CLI's parser, and each review round
	// found another divergence that let assume-role or a process provider win.
	ini, err := ssoOnlyConfig(sso)
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
			// The hint runs in the caller's shell, where AWS_CONFIG_FILE may still be the
			// SSM pane's own file that does not define this profile.
			prefix := ""
			if steered {
				prefix = "AWS_CONFIG_FILE=~/.aws/config "
			}
			// Quoted: the hint is meant to be pasted into a shell, and a profile name can
			// hold anything a quoted INI header can.
			return "", nil, nil, fmt.Errorf("%w for profile %q: %v\nlog in with: %saws sso login --profile %s --use-device-code --no-browser",
				ErrLoginRequired, o.Profile, err, prefix, session.ShellQuote(o.Profile))
		}
		if lerr := deviceLogin(awsBin, aws.env, ssoOnlyProfile, o.Stderr); lerr != nil {
			return "", nil, nil, fmt.Errorf("aws sso login for profile %s: %w", o.Profile, lerr)
		}
		if creds, err = exportCreds(aws, ssoOnlyProfile); err != nil {
			return "", nil, nil, fmt.Errorf("credentials for profile %q after login: %w", o.Profile, err)
		}
	}

	// --region, then a region the caller exported (as for any aws command), then the
	// profile's. AWS_DEFAULT_REGION alone is copied to AWS_REGION: the JS SDK and CDK
	// read only the latter, and the child's config carries no region to fall back on.
	region := o.Region
	switch {
	case region != "" || envHas(env, "AWS_REGION"):
	case envHas(env, "AWS_DEFAULT_REGION"):
		region = envValue(env, "AWS_DEFAULT_REGION")
	default:
		region = keys["region"]
	}
	if region != "" {
		env = setEnv(env, "AWS_REGION="+region, "AWS_DEFAULT_REGION="+region)
	}
	if !o.KeepConfig {
		var warn string
		if env, warn, err = childEnv(env, o.Profile, o.CredentialHelper); err != nil {
			return "", nil, nil, err
		}
		if warn != "" && o.Stderr != nil {
			fmt.Fprintln(o.Stderr, warn)
		}
	}
	env = setEnv(env,
		execKeyIDVar+"="+creds.AccessKeyID,
		"AWS_ACCESS_KEY_ID="+creds.AccessKeyID,
		"AWS_SECRET_ACCESS_KEY="+creds.SecretAccessKey,
		"AWS_SESSION_TOKEN="+creds.SessionToken)
	if creds.Expiration != "" {
		env = setEnv(env, "AWS_CREDENTIAL_EXPIRATION="+creds.Expiration)
	}

	// Confirm the credentials are a session of the profile's permission-set role in its
	// account. With the SSO-only config this is a consistency check, not the proof of
	// provenance (an ARN shows a role name and account, not that Identity Center issued
	// it). It runs with the verifier env so no endpoint override can answer in AWS's place.
	who := awsRunner{bin: awsBin, env: verifierEnv(env, cfg)}
	who.env = setEnv(who.env,
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
		reg := envValue(env, "AWS_REGION")
		if reg == "" {
			reg = "(none)"
		}
		fmt.Fprintf(o.Stderr, "af-aws-exec: running as %s in region %s%s\n", arn, reg, exp)
	}

	prog, err := exec.LookPath(o.Argv[0])
	if err != nil {
		return "", nil, nil, err
	}
	return prog, o.Argv, env, nil
}

// checkAmbiguous refuses a name two or more Settings labels map to.
func checkAmbiguous(o ExecOptions) error {
	for _, c := range o.Conflicts {
		if c.Name == o.Profile {
			return fmt.Errorf("profile %q is ambiguous: Settings labels %s all map to it; rename all but one in Settings > SSM",
				o.Profile, strings.Join(quoteAll(c.Labels), ", "))
		}
	}
	return nil
}

// checkIdentity refuses the remaining ways a correct-looking name can still mean the
// wrong account: the member's own definition of a Settings name pointing somewhere else,
// and a caller-pinned account that does not match.
func checkIdentity(sso ssoInfo, o ExecOptions) error {
	sp, listed := o.Settings[o.Profile]
	if listed && !sameSSO(sp, sso) {
		mine := "not an SSO profile with an account and role"
		if sso.Account != "" || sso.Role != "" {
			mine = fmt.Sprintf("account %s, role %s, portal %s (%s)", orNone(sso.Account), orNone(sso.Role), orNone(sso.StartURL), orNone(sso.Region))
		}
		return fmt.Errorf("profile %q in your own AWS config is %s, but the Settings profile %q is account %s, role %s, portal %s (%s); "+
			"rename one of them so the name means one account", o.Profile, mine, sp.Label, sp.AccountID, sp.RoleName, sp.StartURL, sp.SSORegion)
	}
	// A name that is not a Settings profile is one the member (or a typo) picked from
	// their own files; without --account nothing says which account it was meant to be.
	if !listed && o.Account == "" {
		return fmt.Errorf("profile %q is not one of your Settings profiles; name the account it must be with --account <id> "+
			"(it is account %s), or use a Settings profile (`af-aws-exec --list`)", o.Profile, orNone(sso.Account))
	}
	if o.Account != "" && o.Account != sso.Account {
		return fmt.Errorf("profile %q is account %s, not the %s given with --account", o.Profile, orNone(sso.Account), o.Account)
	}
	return nil
}

// sameSSO compares a Settings profile with what the files say, on every field that
// picks the Identity Center session and role. The same account and role through a
// different portal is a different sign-in, so the portal counts too.
func sameSSO(sp Profile, sso ssoInfo) bool {
	trim := func(u string) string { return strings.TrimRight(u, "/") }
	return sp.AccountID == sso.Account && sp.RoleName == sso.Role &&
		trim(sp.StartURL) == trim(sso.StartURL) && sp.SSORegion == sso.Region
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

// notDefined explains a profile that neither file defines, naming the files actually
// read, so the fix is aimed at the right place rather than at Settings.
func notDefined(env []string, o ExecOptions) error {
	cfg := envValue(env, "AWS_CONFIG_FILE")
	if cfg == "" {
		cfg = ConfigPath()
	}
	creds := envValue(env, "AWS_SHARED_CREDENTIALS_FILE")
	if creds == "" {
		creds = filepath.Join(filepath.Dir(ConfigPath()), "credentials")
	}
	msg := fmt.Sprintf("profile %q is not defined in %s or %s", o.Profile, cfg, creds)
	if _, ok := o.Settings[o.Profile]; ok {
		msg += " although Settings has it; check that AWS_CONFIG_FILE is not pointing elsewhere"
	} else {
		msg += "; see `af-aws-exec --list` for the Settings profiles"
	}
	return errors.New(msg)
}

func quoteAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = strconv.Quote(s)
	}
	return out
}

// childConfigName is the profile names childEnv will write a one-profile config for
// (the characters an exported Settings profile can have; see sessionx).
var childConfigName = regexp.MustCompile(`^[A-Za-z0-9._@+ -]{1,64}$`)

// childEnv gives the child the AWS files and endpoints af-aws-exec vouches for, not the
// member's. Its config defines exactly one profile, the selected one, whose
// credential_process hands back the credentials already in the child's environment
// (helper is that command, `workspace-agent aws-env-credentials`): so a tool that names
// this same profile works, and a tool that names any other profile fails with "could
// not be found" instead of quietly switching. The file holds no secret and does not
// depend on the run, so concurrent runs can share it; the region travels in AWS_REGION.
// Endpoint overrides are dropped: an AWS_ENDPOINT_URL left in the shell would receive
// the new credentials with every signed call (verified with aws-cli 2.36.46).
func childEnv(env []string, profile, helper string) ([]string, string, error) {
	out := make([]string, 0, len(env)+2)
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if k == "AWS_CONFIG_FILE" || k == "AWS_SHARED_CREDENTIALS_FILE" || k == "AWS_IGNORE_CONFIGURED_ENDPOINT_URLS" ||
			strings.HasPrefix(k, "AWS_ENDPOINT_URL") {
			continue
		}
		out = append(out, kv)
	}
	cfg, warn := os.DevNull, ""
	if helper != "" {
		// Without a private place for it the child gets an empty config instead: it
		// stays isolated and only loses "a tool naming this same profile works". Failing
		// the run would leave --keep-aws-config as the only way through.
		var err error
		if cfg, err = writeChildConfig(profile, helper); err != nil {
			cfg, warn = os.DevNull, fmt.Sprintf("af-aws-exec: %v; the command gets an empty AWS config instead, "+
				"so a tool that names profile %q itself will not find it", err, profile)
		}
	}
	return append(out, "AWS_CONFIG_FILE="+cfg, "AWS_SHARED_CREDENTIALS_FILE="+os.DevNull), warn, nil
}

// ChildConfigDir holds the one-profile configs of af-aws-exec's children. It is in the
// Agent's state directory, not under ~/.aws, so a ~/.aws linked onto other storage is
// never followed to write it.
func ChildConfigDir() string { return filepath.Join(paths.AgentStateDir(), "aws-exec") }

// writeChildConfig writes the one-profile config for profile and returns its path. The
// header is quoted (botocore shlex-splits it), so a name with spaces works; the
// character set leaves nothing for the quotes to escape. The file name is the escaped
// profile name, one file per profile; the content does not depend on the run.
func writeChildConfig(profile, helper string) (string, error) {
	if !childConfigName.MatchString(profile) || !iniValueRe.MatchString(strings.ReplaceAll(helper, " ", "")) {
		return "", fmt.Errorf("profile name %q cannot be written into an AWS config header", profile)
	}
	dir, err := privateDir(ChildConfigDir())
	if err != nil {
		return "", err
	}
	cfg := filepath.Join(dir, url.PathEscape(profile)+".config")
	ini := fmt.Sprintf("# Written by af-aws-exec for the command it runs; regenerated on each run.\n[profile \"%s\"]\ncredential_process = %s\n", profile, helper)
	if old, err := os.ReadFile(cfg); err != nil || string(old) != ini {
		if err := writeAtomic(cfg, []byte(ini), 0o600); err != nil {
			return "", err
		}
	}
	return cfg, nil
}

// privateDir makes dir (0700) and insists it is a real directory owned by this user,
// reached through directories nobody else can change: the child's credential_process
// is read from it, so if another user could replace it (a group-writable directory, or
// one under a world-writable parent that ~/.aws links to) they would decide what the
// child runs and receive its credentials. It returns the resolved path, so the writes
// that follow do not pass through a link again.
func privateDir(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() || !ok || int(st.Uid) != os.Getuid() {
		return "", fmt.Errorf("%s must be a directory of your own, not a link; remove it and run again", dir)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return "", err
		}
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	// Every directory above must be this user's or root's (an owner can always give
	// themselves write access and rename what is inside), and writable by others only
	// with the sticky bit (as /tmp is), under which they cannot rename what they do not
	// own.
	for p := filepath.Dir(real); ; p = filepath.Dir(p) {
		pi, err := os.Stat(p)
		if err != nil {
			return "", err
		}
		ps, ok := pi.Sys().(*syscall.Stat_t)
		if !ok || (int(ps.Uid) != os.Getuid() && ps.Uid != 0) {
			return "", fmt.Errorf("%s belongs to another user, so %s under it is not private", p, dir)
		}
		if pi.Mode().Perm()&0o022 != 0 && pi.Mode()&os.ModeSticky == 0 {
			return "", fmt.Errorf("%s is writable by its group or other users, so %s under it is not private", p, dir)
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	return real, nil
}

// execKeyIDVar pins the child's profile to the credentials af-aws-exec handed over: it
// holds their access key id (an identifier, not a secret). EnvCredentials refuses once
// AWS_ACCESS_KEY_ID no longer matches it, so a script that exports another account's
// keys (after an assume-role, say) does not silently turn `--profile prod` into that
// account.
const execKeyIDVar = "AF_AWS_EXEC_KEY_ID"

// EnvCredentials is the credential_process document for the credentials in environ,
// for `workspace-agent aws-env-credentials`.
func EnvCredentials(environ []string) ([]byte, error) {
	c := processCreds{Version: 1,
		AccessKeyID: envValue(environ, "AWS_ACCESS_KEY_ID"), SecretAccessKey: envValue(environ, "AWS_SECRET_ACCESS_KEY"),
		SessionToken: envValue(environ, "AWS_SESSION_TOKEN"), Expiration: envValue(environ, "AWS_CREDENTIAL_EXPIRATION")}
	pinned := envValue(environ, execKeyIDVar)
	if pinned == "" || c.AccessKeyID == "" || c.SecretAccessKey == "" || c.SessionToken == "" {
		return nil, errors.New("no af-aws-exec credentials in the environment; run the command under af-aws-exec")
	}
	if c.AccessKeyID != pinned {
		return nil, errors.New("AWS_ACCESS_KEY_ID was changed after af-aws-exec started this command, so it is no longer the " +
			"profile af-aws-exec ran it under; use the new credentials without --profile, or run that step under its own af-aws-exec")
	}
	return json.Marshal(c)
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
func ssoOnlyConfig(sso ssoInfo) (string, error) {
	var b strings.Builder
	if sso.Session != "" {
		if !iniSessionRe.MatchString(sso.Session) {
			return "", fmt.Errorf("sso-session name %q is not usable", sso.Session)
		}
		fmt.Fprintf(&b, "[sso-session \"%s\"]\n", sso.Session)
		if sso.Scopes != "" {
			if !iniValueRe.MatchString(strings.ReplaceAll(sso.Scopes, " ", "")) {
				return "", errors.New("sso_registration_scopes is not usable")
			}
			fmt.Fprintf(&b, "sso_registration_scopes = %s\n", sso.Scopes)
		}
	}
	fields := [][2]string{
		{"sso_start_url", sso.StartURL}, {"sso_region", sso.Region},
		{"sso_account_id", sso.Account}, {"sso_role_name", sso.Role},
	}
	for _, f := range fields {
		if !iniValueRe.MatchString(f[1]) {
			return "", fmt.Errorf("%s is missing or not usable", f[0])
		}
	}
	if sso.Session != "" {
		fmt.Fprintf(&b, "sso_start_url = %s\nsso_region = %s\n\n[profile %s]\nsso_session = %s\n", sso.StartURL, sso.Region, ssoOnlyProfile, sso.Session)
	} else {
		fmt.Fprintf(&b, "[profile %s]\nsso_start_url = %s\nsso_region = %s\n", ssoOnlyProfile, sso.StartURL, sso.Region)
	}
	fmt.Fprintf(&b, "sso_account_id = %s\nsso_role_name = %s\n", sso.Account, sso.Role)
	return b.String(), nil
}

// ssoInfo is everything that decides which Identity Center session and role a profile
// yields: the portal (start URL + SSO region), the account and the role.
type ssoInfo struct {
	Session, Scopes  string
	StartURL, Region string
	Account, Role    string
}

// resolveSSO reads the profile's SSO settings the way the CLI does: through its
// sso-session section when it names one, from the profile itself otherwise.
func resolveSSO(env []string, keys map[string]string) (ssoInfo, error) {
	sso := ssoInfo{Session: keys["sso_session"], Account: keys["sso_account_id"], Role: keys["sso_role_name"]}
	if sso.Session == "" {
		sso.StartURL, sso.Region = keys["sso_start_url"], keys["sso_region"]
		return sso, nil
	}
	sess := map[string]string{}
	cfg := envValue(env, "AWS_CONFIG_FILE")
	if cfg == "" {
		cfg = ConfigPath()
	}
	if err := readINISection(expandHome(cfg), configPicker("sso-session", sso.Session), sess); err != nil {
		return ssoInfo{}, err
	}
	sso.StartURL, sso.Region, sso.Scopes = sess["sso_start_url"], sess["sso_region"], sess["sso_registration_scopes"]
	return sso, nil
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
