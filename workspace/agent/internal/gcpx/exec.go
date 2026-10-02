package gcpx

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/cloudexec"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	_ "modernc.org/sqlite"
)

// ErrLoginRequired means the profile has no usable login and none was started.
var ErrLoginRequired = errors.New("Google Cloud login required")

// errCredentialRejected marks an ErrLoginRequired where a stored credential exists but
// Google refused it (invalid_grant, reauthentication). A login for it must be forced:
// without --force, `gcloud auth login <account>` reuses a cached access token with more than
// about five minutes left (SDK 587.0.0, ShouldUseCachedCredentials) and starts no sign-in,
// so the refresh that failed fails again after it.
var errCredentialRejected = errors.New("the stored credential was rejected")

// MinRemaining is the shortest token life a command is started with (decision 2): the
// wrapper does not refresh during the command, so less would hand over a token about to end.
const MinRemaining = 10 * time.Minute

// ExecOptions is one `af-gcloud-exec` invocation.
type ExecOptions struct {
	Profile string
	// Project must equal the profile's project (decision 2).
	Project string
	// Login: "auto" logs in only when a person is at a terminal, "always" whenever the
	// profile has no usable login, "never" exits 3 with the command to run instead.
	Login string
	Quiet bool
	Argv  []string
	// Settings and Conflicts come from the sync that preceded the run (nil when neither the
	// CP nor the cache could say).
	Settings  map[string]Profile
	Conflicts []Conflict
	Invalid   map[string]string

	Stderr      io.Writer
	Interactive bool // stdin and stderr are terminals
	// Now is the clock for the remaining-life check (time.Now when nil).
	Now func() time.Time
}

// WaitingMessage is what a run prints when another process holds the Agent's gcloud root.
const WaitingMessage = "af-gcloud-exec: waiting for another af-gcloud-exec or a profile sync (a Google Cloud login in a terminal holds it) ..."

// ExecDir holds the private per-run directories of af-gcloud-exec's children.
func ExecDir() string { return cloudexec.StateDir("gcp-exec") }

// dropGoogle says which caller variables never reach a gcloud the Agent runs nor the
// child: anything that could select another config root, token, account, credential file
// or metadata server. An inherited CLOUDSDK_AUTH_ACCESS_TOKEN_FILE would otherwise replace
// the profile's identity before the profile is consulted.
var dropGoogle = cloudexec.DropPrefixes("CLOUDSDK_", "GOOGLE_", "GCLOUD_", "GCE_METADATA_")

// AgentEnv is the environment of the Agent's own gcloud runs against its root: the
// caller's minus dropGoogle, then the root and the settings of decision 1, appended after
// the removal so nothing a caller sets can undo them. File logging off keeps the token, the
// login URL and the code exchange out of the root's logs/; the metadata check off keeps a
// configuration without an account from falling back to the VM's or node's identity.
func AgentEnv(environ []string, root string) []string {
	return cloudexec.SetEnv(cloudexec.Scrub(environ, dropGoogle),
		"CLOUDSDK_CONFIG="+root,
		"CLOUDSDK_CORE_DISABLE_FILE_LOGGING=true",
		"CLOUDSDK_CORE_CHECK_GCE_METADATA=false",
		"CLOUDSDK_CORE_DISABLE_USAGE_REPORTING=true",
		"CLOUDSDK_COMPONENT_MANAGER_DISABLE_UPDATE_CHECK=true",
	)
}

// Token is one minted access token. Value is never printed.
type Token struct {
	Value  string
	Expiry time.Time
}

// PlanExec mints a token for o.Profile and returns the program, argv and environment to
// exec. The token leaves this process only in the returned environment and the run's
// private token file; nothing here prints or logs it.
func PlanExec(gcloudBin string, environ []string, o ExecOptions) (string, []string, []string, error) {
	if o.Profile == "" {
		return "", nil, nil, errors.New("--profile is required")
	}
	if o.Project == "" {
		return "", nil, nil, errors.New("--project is required")
	}
	if len(o.Argv) == 0 {
		return "", nil, nil, errors.New("no command given after --")
	}
	p, err := pickProfile(o)
	if err != nil {
		return "", nil, nil, err
	}
	// A Google user token is not bound to a project, so this checks that the caller and
	// the profile agree on where the command points by default, not what the token can
	// reach (decision 2).
	if o.Project != p.Project {
		return "", nil, nil, fmt.Errorf("profile %q is for project %q, not %q; --project must be the profile's project "+
			"(see `af-gcloud-exec --list`)", p.Name, p.Project, o.Project)
	}
	stderr := o.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	agentEnv := AgentEnv(environ, ConfigRoot())

	waiting := func() { fmt.Fprintln(stderr, WaitingMessage) }
	tok, account, err := mintLocked(gcloudBin, agentEnv, p, waiting)
	if errors.Is(err, ErrLoginRequired) {
		hint := fmt.Sprintf("af-gcloud-exec --profile %s --project %s --login -- true", session.ShellQuote(p.Name), session.ShellQuote(p.Project))
		switch {
		case o.Login == "always" || (o.Login != "never" && o.Interactive):
			tok, account, err = loginAndMint(gcloudBin, agentEnv, p, stderr, waiting, errors.Is(err, errCredentialRejected))
		default:
			return "", nil, nil, fmt.Errorf("profile %q: %w\nlog in from a terminal with: %s", p.Name, err, hint)
		}
	}
	if err != nil {
		return "", nil, nil, fmt.Errorf("profile %q: %w", p.Name, err)
	}
	left := tok.Expiry.Sub(now())
	if left < MinRemaining {
		return "", nil, nil, fmt.Errorf("profile %q: the token gcloud minted is valid for only %s, under the %s a command is started with; "+
			"try again in a minute", p.Name, left.Round(time.Second), MinRemaining)
	}
	if !o.Quiet {
		as := account
		if p.ImpersonateServiceAccount != "" {
			as = p.ImpersonateServiceAccount + " (impersonated by " + account + ")"
		}
		fmt.Fprintf(stderr, "af-gcloud-exec: profile %s runs as %s in project %s; the token is valid for %d more minutes\n",
			p.Name, as, p.Project, int(left/time.Minute))
	}

	prog, err := exec.LookPath(o.Argv[0])
	if err != nil {
		return "", nil, nil, err
	}
	run, err := cloudexec.RunDir(ExecDir(), "run-")
	if err != nil {
		return "", nil, nil, err
	}
	sweepRuns(filepath.Dir(run), run, now())
	env, err := ChildEnv(environ, run, p, tok.Value)
	if err != nil {
		_ = os.RemoveAll(run)
		return "", nil, nil, err
	}
	return prog, o.Argv, env, nil
}

// pickProfile finds o.Profile among the Settings profiles, or says why it cannot run.
func pickProfile(o ExecOptions) (Profile, error) {
	for _, c := range o.Conflicts {
		if c.Name == o.Profile {
			return Profile{}, fmt.Errorf("profile %q is not exported: Settings labels %s all map to this name; rename all but one",
				o.Profile, strings.Join(c.Labels, " / "))
		}
	}
	if why, ok := o.Invalid[o.Profile]; ok {
		return Profile{}, fmt.Errorf("profile %q is not exported: %s (Settings > Google Cloud)", o.Profile, why)
	}
	p, ok := o.Settings[o.Profile]
	if !ok {
		if o.Settings == nil {
			return Profile{}, fmt.Errorf("profile %q: the Google Cloud profiles could not be read from Settings, and there is no earlier copy", o.Profile)
		}
		return Profile{}, fmt.Errorf("no Google Cloud profile %q in Settings (see `af-gcloud-exec --list`)", o.Profile)
	}
	if why := InvalidReason(p); why != "" {
		return Profile{}, fmt.Errorf("profile %q is not exported: %s (Settings > Google Cloud)", o.Profile, why)
	}
	return p, nil
}

// mintLocked mints under the root's lock, so a sync or a login cannot change the
// configuration between the checks and the mint.
func mintLocked(gcloudBin string, env []string, p Profile, waiting func()) (Token, string, error) {
	root, unlock, err := lockRootNotify(waiting)
	if err != nil {
		return Token{}, "", err
	}
	defer unlock()
	return mintHeld(gcloudBin, env, root, p)
}

// mintHeld mints for a caller that holds the root's lock (root is resolved). The
// configuration must be exactly the version of p this run read from Settings (syncedAs), so
// the account checked, the impersonation passed and what is printed are the ones gcloud uses.
func mintHeld(gcloudBin string, env []string, root string, p Profile) (Token, string, error) {
	env = cloudexec.SetEnv(env, "CLOUDSDK_CONFIG="+root)
	account, err := syncedAs(root, p)
	if err != nil {
		return Token{}, "", err
	}
	if account == "" {
		return Token{}, "", fmt.Errorf("%w: no account is selected for it yet", ErrLoginRequired)
	}
	// Only a user credential is minted from; the VM's or node's identity, a service-account
	// key or an external account activated into this root by hand never is (decision 2).
	kind, err := credentialType(root, account)
	if err != nil {
		return Token{}, "", err
	}
	switch kind {
	case "":
		return Token{}, "", fmt.Errorf("%w: %s has no credential in the Agent's gcloud store", ErrLoginRequired, account)
	case "authorized_user":
	default:
		return Token{}, "", fmt.Errorf("%s holds a %q credential in the Agent's gcloud store, not a user login; af-gcloud-exec only mints "+
			"from a user login", account, kind)
	}
	tok, err := mint(gcloudBin, env, p)
	return tok, account, err
}

// loginAndMint runs the terminal login and then mints, holding the root's lock from before
// the login until after the mint. gcloud writes the login's core/account into the
// configuration itself, so while it runs nothing else may read or rewrite that
// configuration: a mint would take the half-finished selection, and a sync would either
// lose it or keep an account chosen for a version of the profile Settings has since reset.
// A Settings change made during the login is applied by the first sync after it, which then
// resets the selection as decision 1 says. Other runs wait (and say so) meanwhile.
//
// Once the login has started, a run that still has no usable credential is a failure (exit
// 1), not ErrLoginRequired: exit 3 means a login is needed and was not started.
func loginAndMint(gcloudBin string, env []string, p Profile, stderr io.Writer, waiting func(), force bool) (Token, string, error) {
	root, unlock, err := lockRootNotify(waiting)
	if err != nil {
		return Token{}, "", err
	}
	defer unlock()
	if _, err := syncedAs(root, p); err != nil {
		return Token{}, "", err
	}
	if err := terminalLogin(gcloudBin, cloudexec.SetEnv(env, "CLOUDSDK_CONFIG="+root), p, stderr, force); err != nil {
		return Token{}, "", fmt.Errorf("gcloud auth login: %w", err)
	}
	tok, account, err := mintHeld(gcloudBin, env, root, p)
	if errors.Is(err, ErrLoginRequired) {
		return Token{}, "", fmt.Errorf("the login finished but still gave no usable credential: %s", err.Error())
	} else if err != nil {
		return Token{}, "", fmt.Errorf("after the login: %w", err)
	}
	return tok, account, nil
}

// credentialType is the "type" of account's credential in gcloud's credentials.db ("" when
// there is none). Only the type is selected, so the refresh token never enters this process.
func credentialType(root, account string) (string, error) {
	path := filepath.Join(root, "credentials.db")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(3000)")
	if err != nil {
		return "", err
	}
	defer db.Close()
	var kind sql.NullString
	err = db.QueryRow(`SELECT json_extract(value, '$.type') FROM credentials WHERE account_id = ?`, account).Scan(&kind)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("reading the Agent's gcloud credential store: %w", err)
	}
	if !kind.Valid || kind.String == "" {
		return "unknown", nil
	}
	return kind.String, nil
}

// mint asks gcloud for the profile's token. `config config-helper` rather than `auth
// print-access-token`: it returns the expiry with the token, also for an impersonated one,
// and --min-expiry makes gcloud refresh a cached token with less than that left, which
// print-access-token does only inside its own ~5 minute window (measured on 587.0.0; ADR
// 0107 note of 2026-10-02).
func mint(gcloudBin string, env []string, p Profile) (Token, error) {
	args := []string{"config", "config-helper", "--configuration", ConfigName(p.Name),
		"--min-expiry", fmt.Sprintf("%dm", int(MinRemaining/time.Minute)),
		"--format", "json(credential.access_token,credential.token_expiry)", "--quiet"}
	if p.ImpersonateServiceAccount != "" {
		args = append(args, "--impersonate-service-account", p.ImpersonateServiceAccount)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, gcloudBin, args...)
	cmd.Env = env
	cmd.Stdin = nil
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := gcloudError(errb.String())
		if loginNeeded(errb.String()) {
			return Token{}, fmt.Errorf("%w: %w: %s", ErrLoginRequired, errCredentialRejected, msg)
		}
		return Token{}, fmt.Errorf("gcloud could not mint a token: %s", msg)
	}
	var doc struct {
		Credential struct {
			AccessToken string `json:"access_token"`
			TokenExpiry string `json:"token_expiry"`
		} `json:"credential"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil || doc.Credential.AccessToken == "" {
		return Token{}, errors.New("gcloud returned no access token")
	}
	exp, err := time.Parse(time.RFC3339, doc.Credential.TokenExpiry)
	if err != nil {
		return Token{}, errors.New("gcloud returned no usable token expiry, so how long the token lasts is unknown")
	}
	return Token{Value: doc.Credential.AccessToken, Expiry: exp}, nil
}

// loginNeeded says whether gcloud's error means "log in" rather than a refusal a login
// cannot fix. gcloud wraps every failed refresh in the same "There was a problem
// refreshing your current auth tokens … Please run: gcloud auth login" text, a temporary
// outage of Google's token endpoint included (measured on 587.0.0 with a local token
// endpoint answering temporarily_unavailable), so that text decides nothing: the
// underlying error does. An outage, a permission or API error or the network is never a
// login; only an explicit grant or reauthentication failure, or no account at all, is.
// Anything unrecognised is a refusal (exit 1), which reports gcloud's message, rather than
// a login prompt that cannot help.
func loginNeeded(stderr string) bool {
	s := strings.ToLower(stderr)
	for _, m := range []string{
		"temporarily_unavailable", "server_error", "internal_failure", "backenderror",
		"permission_denied", "permission denied", "iam_permission_denied", "forbidden",
		"service_disabled", "has not been used in project", "is disabled",
		"unable to find the server", "failed to establish a new connection", "connection refused",
		"connection reset", "timed out", "name or service not known", "temporary failure in name resolution",
		"network is unreachable", "ssl", "proxy",
	} {
		if strings.Contains(s, m) {
			return false
		}
	}
	for _, m := range []string{
		"invalid_grant",
		"invalid_rapt",
		"reauthentication",
		"reauth related error",
		"token has been expired or revoked",
		"you do not currently have an active account selected",
	} {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// tokenLike matches an OAuth access token (ya29.…) and any long unbroken run of token
// characters (base64 with '/' and '+', base64url, a JWT's dots), so a message built from
// gcloud's stderr cannot carry an opaque token or an id_token even if a future gcloud put
// one there. A long path is redacted too; that is the price.
var tokenLike = regexp.MustCompile(`ya29\.[A-Za-z0-9_.+/=-]+|[A-Za-z0-9_.+/=-]{40,}`)

// gcloudError keeps gcloud's ERROR lines (falling back to its last line), bounded and with
// anything token-shaped removed, for a message.
func gcloudError(stderr string) string {
	stderr = tokenLike.ReplaceAllString(stderr, "<redacted>")
	var keep []string
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	for _, l := range lines {
		if strings.HasPrefix(l, "ERROR:") {
			keep = append(keep, strings.TrimSpace(l))
		}
	}
	if len(keep) == 0 && len(lines) > 0 {
		keep = lines[len(lines)-1:]
	}
	s := strings.Join(keep, " ")
	if len(s) > 600 {
		s = s[:600] + "…"
	}
	if s == "" {
		s = "(no message)"
	}
	return s
}

// terminalLogin runs gcloud's login with the person's terminal attached, in env (the clean
// environment, pointed at the Agent's root), for a caller that holds the root's lock. When
// the profile names an account it is passed, so gcloud refuses any other sign-in before
// storing it. Its stdout goes to stderr so the wrapped command's stdout stays clean for
// pipes.
func terminalLogin(gcloudBin string, env []string, p Profile, stderr io.Writer, force bool) error {
	fmt.Fprintln(stderr, "af-gcloud-exec: Google Cloud login needed for profile "+p.Name+". Paste back only a code from a sign-in you started yourself just now.")
	cmd := exec.Command(gcloudBin, LoginArgs(p, force)...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, stderr, stderr
	return cmd.Run()
}

// LoginArgs is `gcloud auth login [<account>] --no-launch-browser --configuration af-<name>`,
// with --force when Google rejected the stored credential (errCredentialRejected).
func LoginArgs(p Profile, force bool) []string {
	args := []string{"auth", "login"}
	if p.Account != "" {
		args = append(args, p.Account)
	}
	args = append(args, "--no-launch-browser", "--configuration", ConfigName(p.Name))
	if force {
		args = append(args, "--force")
	}
	return args
}

// noCredentials is the GOOGLE_APPLICATION_CREDENTIALS of the child: a path in its private
// directory that is never created. Go's oauth2/google and auth, Python's google-auth and
// Node's google-auth-library all stop there with an error, without reading the member's
// well-known ADC file or asking the metadata server (ADR 0107 note of 2026-10-02).
const noCredentials = "no-application-default-credentials.json"

// ChildEnv is the command's environment (decision 2): the caller's with every
// CLOUDSDK_*, GOOGLE_*, GCLOUD_* (and GCE_METADATA_*) variable removed, then the token,
// the profile's project, quota project, region and zone, and an application-default
// credential path that holds nothing. run is the private directory of this run; the token
// file and the child's empty config root are written there.
func ChildEnv(environ []string, run string, p Profile, token string) ([]string, error) {
	cfg := filepath.Join(run, "config")
	if err := os.Mkdir(cfg, 0o700); err != nil {
		return nil, err
	}
	tokFile := filepath.Join(run, "token")
	if err := os.WriteFile(tokFile, []byte(token), 0o600); err != nil {
		return nil, err
	}
	quota := p.QuotaProject
	if quota == "" {
		quota = p.Project
	}
	kvs := []string{
		"CLOUDSDK_CONFIG=" + cfg,
		"CLOUDSDK_AUTH_ACCESS_TOKEN_FILE=" + tokFile,
		"CLOUDSDK_CORE_DISABLE_FILE_LOGGING=true",
		"GOOGLE_OAUTH_ACCESS_TOKEN=" + token,
		"CLOUDSDK_CORE_PROJECT=" + p.Project,
		"GOOGLE_CLOUD_PROJECT=" + p.Project,
		"GOOGLE_PROJECT=" + p.Project,
		"CLOUDSDK_BILLING_QUOTA_PROJECT=" + quota,
		"GOOGLE_BILLING_PROJECT=" + quota,
		"USER_PROJECT_OVERRIDE=true",
		"GOOGLE_APPLICATION_CREDENTIALS=" + filepath.Join(run, noCredentials),
	}
	if p.Region != "" {
		kvs = append(kvs, "CLOUDSDK_COMPUTE_REGION="+p.Region, "GOOGLE_REGION="+p.Region)
	}
	if p.Zone != "" {
		kvs = append(kvs, "CLOUDSDK_COMPUTE_ZONE="+p.Zone, "GOOGLE_ZONE="+p.Zone)
	}
	return cloudexec.SetEnv(cloudexec.Scrub(environ, dropGoogle), kvs...), nil
}

// runKeep is how long a run's directory outlives its start. The wrapper execs into the
// command, so nothing removes the directory when the command ends; a later run sweeps it.
// The token in it lasts at most an hour (no --lifetime is asked for), so after this the
// directory holds nothing usable, while a command still running keeps its config root.
const runKeep = 12 * time.Hour

// sweepRuns removes the run directories under dir older than runKeep, except keep.
func sweepRuns(dir, keep string, now time.Time) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		path := filepath.Join(dir, e.Name())
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "run-") || path == keep {
			continue
		}
		if fi, err := os.Stat(filepath.Join(path, "token")); err == nil && now.Sub(fi.ModTime()) < runKeep {
			continue
		} else if err != nil {
			if di, derr := e.Info(); derr != nil || now.Sub(di.ModTime()) < runKeep {
				continue
			}
		}
		_ = os.RemoveAll(path)
	}
}
