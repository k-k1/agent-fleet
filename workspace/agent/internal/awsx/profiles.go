// Package awsx makes the member's SSO profiles (Settings → AWS profiles/SSM, stored in the CP) usable
// by ordinary AWS clients in the workspace, and runs a command under one of them with
// scoped credentials (issue #998).
//
//	agent ──(AF_AWS_PROFILES_TOKEN)──▶ CP GET /internal/aws-profiles
//	                    │
//	    managed block at the end of ~/.aws/config
//
// ~/.aws/config is the member's own file. Only the block between the two marker lines is
// ours; everything outside it is preserved byte for byte, and a profile whose name the
// member already defined outside the block is not exported — their definition wins.
//
// awsx is the AWS backend of the provider-neutral packages: cloudbridge pulls the
// profiles, cloudlogin runs the Console login's requests and attempts, and cloudexec is
// af-aws-exec's skeleton. What reads AWS's own credential store stays here: the SSO token
// cache, its expiry and token hash, what "resolved" means for a request (ADR 0102), the
// expiry warning and the managed block of ~/.aws/config.
package awsx

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/cloudbridge"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
)

// PollInterval is how often the Agent pulls the profiles from the CP.
const PollInterval = cloudbridge.PollInterval

// ErrBridgeOff reports that this deployment injects no bridge (no PUBLIC_BASE_URL). A
// normal state, not a failure: the file is then left exactly as it is.
var ErrBridgeOff = errors.New("the AWS profiles bridge is not configured in this deployment")

const (
	blockBegin = "# >>> agent-fleet: SSO profiles from Settings > SSM (managed; edits inside this block are overwritten) >>>"
	blockEnd   = "# <<< agent-fleet: SSO profiles <<<"
)

// Profile is one exported profile as the CP sends it.
type Profile struct {
	Name      string `json:"name"`
	Label     string `json:"label"`
	StartURL  string `json:"startUrl"`
	SSORegion string `json:"ssoRegion"`
	AccountID string `json:"accountId,omitempty"`
	RoleName  string `json:"roleName,omitempty"`
	Region    string `json:"region,omitempty"`
	// Kind is KindAssumeRole for a profile that assumes RoleARN from the exported SSO profile
	// named SourceProfile (issue #1109), "" for an SSO profile. A chained profile has no portal,
	// and AccountID is its role's. None of these holds a secret.
	Kind            string `json:"kind,omitempty"`
	RoleARN         string `json:"roleArn,omitempty"`
	SourceProfile   string `json:"sourceProfile,omitempty"`
	ExternalID      string `json:"externalId,omitempty"`
	SessionName     string `json:"sessionName,omitempty"`
	DurationSeconds int    `json:"durationSeconds,omitempty"`
}

// KindAssumeRole is Profile.Kind of a role-chaining profile.
const KindAssumeRole = "assume_role"

// Chained reports whether p assumes a role from another Settings profile.
func (p Profile) Chained() bool { return p.Kind == KindAssumeRole }

// SyncResult reports one sync for the log and for `af-aws-exec --list`.
type SyncResult struct {
	Exported []string // profile names now in the managed block
	// Shadowed are profiles not exported because ~/.aws/config or ~/.aws/credentials
	// already defines a profile of that name, or because the name is "default" (see
	// render). A collision with the member's own sso-session is SessionShadowed.
	Shadowed []string
	// Invalid are profiles whose Settings values the INI allowlist refuses
	// (sessionx.RenderSSMConfig), by name, with the allowlist's reason (field names only).
	Invalid map[string]string
	// Incomplete are Settings profiles without both an account and a role, not exported,
	// by name, with the reason (IncompleteReason).
	Incomplete map[string]string
	// SessionShadowed are Settings profiles not exported because the member's own
	// ~/.aws/config has an [sso-session af-<name>] section (the name this profile's
	// sso-session needs) but no profile of that name.
	SessionShadowed []string
	// DefaultClash are profiles not exported because a [DEFAULT] line in ~/.aws/config
	// would make the CLI refuse them or run them as another role, by name, with that
	// line and what it does (a ready-to-print reason).
	DefaultClash map[string]string
	// ChainBroken are role-chaining profiles not exported because the SSO profile they
	// assume from is not in the managed block (it is shadowed, held back, not an SSO
	// profile or gone), by name, with the reason. Exporting them anyway would point
	// source_profile at whatever the member's own files define under that name.
	ChainBroken map[string]string
	Changed     bool
	// Settings is every profile the CP sent, by name, so af-aws-exec can tell a name the
	// member's own ~/.aws definition shadows from the Settings profile of that name.
	Settings map[string]Profile
	// Conflicts are names two or more Settings labels sanitize to; the CP exports none of
	// them.
	Conflicts []Conflict
	// Fetched is true when Settings and Conflicts came from the CP on this call, even if
	// writing ~/.aws/config then failed: fresh answers must not be swapped for the cache.
	Fetched bool
	// FromCache is true when the CP could not be asked and the block was re-applied
	// from the last list it gave.
	FromCache bool
}

// Conflict is a profile name two or more Settings labels map to.
type Conflict = cloudbridge.Conflict

// bridge pulls the AWS profiles from the CP.
var bridge = &cloudbridge.Bridge[Profile]{Path: "/internal/aws-profiles", TokenEnv: "AF_AWS_PROFILES_TOKEN",
	What: "AWS profiles", CacheFile: "aws-settings.json", OwnerLabel: "af-aws-profiles-cache/v1",
	Target: "~/.aws/config", ErrOff: ErrBridgeOff}

// ConfigPath is the file the AWS CLI and SDKs read by default. AWS_CONFIG_FILE is
// deliberately not honoured: a shell that exported it for one session would otherwise
// steer the managed block into that session's private file.
func ConfigPath() string { return filepath.Join(paths.HomeDir(), ".aws", "config") }

// Fetch pulls the member's profiles from the CP, with the names it left out because
// two labels collide.
func Fetch() ([]Profile, []Conflict, error) {
	l, err := bridge.Fetch()
	return l.Profiles, l.Conflicts, err
}

// Sync pulls and applies. When the CP cannot be reached it re-applies the last list the
// CP gave (the cache) under today's rules, so a CP blip never takes a working profile
// away, though a profile the files now break (a new [DEFAULT] line, say) is held back
// offline just as it would be online: the block, --list and af-aws-exec then agree with
// what the files say now. Without a cache the block is left as it is. The lock is the
// one every writer of ~/.aws/config takes (cloudbridge.Pull says why it spans the fetch).
func Sync() (SyncResult, error) {
	var res SyncResult
	target, applied := "", false
	p, err := bridge.Pull(func() (func(), error) {
		unlock, t, lerr := lockConfig(ConfigPath())
		target = t
		return unlock, lerr
	}, func(l cloudbridge.List[Profile]) error {
		var aerr error
		res, aerr = applyLocked(ConfigPath(), target, l.Profiles)
		applied = true
		return aerr
	})
	if p.Have {
		res.Settings = map[string]Profile{}
		for _, sp := range p.Profiles {
			res.Settings[sp.Name] = sp
		}
		res.Conflicts = p.Conflicts
	}
	res.Fetched, res.FromCache = p.Fetched, p.FromCache
	if p.Fetched && applied {
		// An earlier build kept the cache in ~/.aws; remove that one file so no stray
		// agent-fleet file stays in the member's directory.
		_ = os.Remove(filepath.Join(filepath.Dir(ConfigPath()), ".agent-fleet-settings.json"))
	}
	return res, err
}

// Apply rewrites the managed block of the config at path to hold ps. It writes only when
// the content changes, never creates the file just to hold an empty block, and replaces
// the file atomically under a lock so a concurrent sync (the poll and an af-aws-exec)
// cannot interleave a read-modify-write.
func Apply(path string, ps []Profile) (SyncResult, error) {
	unlock, target, err := lockConfig(path)
	if err != nil {
		return SyncResult{}, err
	}
	defer unlock()
	return applyLocked(path, target, ps)
}

// lockConfig resolves the config at path, makes its directory, and takes the lock that
// serializes every writer of it; it returns the unlock and the resolved target.
func lockConfig(path string) (func(), string, error) {
	target, err := resolveLink(path)
	if err != nil {
		return nil, "", err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return nil, "", err
	}
	unlock, err := lockDir(filepath.Dir(target))
	if err != nil {
		return nil, "", err
	}
	return unlock, target, nil
}

// applyLocked is Apply for a caller that already holds lockConfig.
func applyLocked(path, target string, ps []Profile) (SyncResult, error) {
	old, err := os.ReadFile(target)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return SyncResult{}, err
	}
	mode := os.FileMode(0o600)
	if fi, serr := os.Stat(target); serr == nil {
		mode = fi.Mode().Perm()
	}
	// Names in the credentials file count as the member's own: the CLI merges both files
	// per profile, so an SSO block under the same name would turn their working static-key
	// profile into an SSO one.
	// Only a missing file counts as empty: an unreadable one (permissions, say) could hold
	// a profile of the member's own that the block must not shadow, so it stops the sync
	// with the block left as it is.
	creds, cerr := os.ReadFile(filepath.Join(filepath.Dir(path), "credentials"))
	if cerr != nil && !errors.Is(cerr, os.ErrNotExist) {
		return SyncResult{}, fmt.Errorf("cannot read ~/.aws/credentials: %w", cerr)
	}
	next, res, err := render(string(old), string(creds), ps)
	if err != nil {
		return res, err
	}
	if next == string(old) {
		return res, nil
	}
	if err := writeAtomic(target, []byte(next), mode); err != nil {
		return res, err
	}
	res.Changed = true
	return res, nil
}

// resolveLink follows a symlinked config to its target, so the atomic rename replaces
// the target file rather than turning the link into a real file (~/.aws may be linked
// onto durable storage).
func resolveLink(path string) (string, error) {
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return path, nil
	}
	t, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("%s is a symlink that does not resolve: %w", path, err)
	}
	return t, nil
}

func lockDir(dir string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(dir, ".agent-fleet-profiles.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config.af-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// render returns the config text with the managed block replaced by one holding ps.
func render(old, credentials string, ps []Profile) (string, SyncResult, error) {
	var res SyncResult
	user, err := stripBlock(old)
	if err != nil {
		return old, res, err
	}
	profiles, ssoSessions := configNames(user)
	credProfiles := credentialsNames(credentials)
	for n := range credProfiles {
		profiles[n] = true
	}
	// "default" is what every bare aws/SDK call resolves to. Exporting it would move
	// those calls from whatever they use today (the workload role, for one) onto an SSO
	// login, so a Settings profile labelled "default" is never exported.
	profiles["default"] = true
	defaults := map[string]string{}
	scanINI(user, func(l iniLine) {
		if !l.header && !l.bad && l.section == "DEFAULT" {
			defaults[l.key] = l.value
		}
	})
	var body strings.Builder
	// Role-chaining profiles come after the SSO profiles they assume from, once it is known
	// which of those are exported.
	var chained []Profile
	byName := map[string]Profile{}
	for _, p := range ps {
		byName[p.Name] = p
	}
	for _, p := range ps {
		if p.Chained() {
			chained = append(chained, p)
			continue
		}
		if profiles[p.Name] {
			res.Shadowed = append(res.Shadowed, p.Name)
			continue
		}
		if ssoSessions["af-"+p.Name] {
			res.SessionShadowed = append(res.SessionShadowed, p.Name)
			continue
		}
		// Without both an account and a role the profile is not exported: with neither the
		// CLI does not treat it as SSO and the default chain goes on to the container's
		// role (measured), with one it fails. See IncompleteReason.
		if why := IncompleteReason(p); why != "" {
			if res.Incomplete == nil {
				res.Incomplete = map[string]string{}
			}
			res.Incomplete[p.Name] = why
			continue
		}
		// A Settings value the AWS config cannot hold comes before anything about the
		// member's files: it needs fixing in Settings whatever the files say.
		ini, rerr := renderProfile(p)
		if rerr != nil {
			if res.Invalid == nil {
				res.Invalid = map[string]string{}
			}
			res.Invalid[p.Name] = InvalidReason(p, rerr)
			continue
		}
		if why := defaultClash(defaults, p); why != "" {
			if res.DefaultClash == nil {
				res.DefaultClash = map[string]string{}
			}
			res.DefaultClash[p.Name] = why
			continue
		}
		body.WriteString("\n")
		body.WriteString(ini)
		res.Exported = append(res.Exported, p.Name)
	}
	exportedSSO := map[string]bool{}
	for _, n := range res.Exported {
		exportedSSO[n] = true
	}
	for _, p := range chained {
		if profiles[p.Name] {
			res.Shadowed = append(res.Shadowed, p.Name)
			continue
		}
		src, haveSrc := byName[p.SourceProfile]
		if why := chainBrokenReason(p, src, haveSrc, exportedSSO); why != "" {
			if res.ChainBroken == nil {
				res.ChainBroken = map[string]string{}
			}
			res.ChainBroken[p.Name] = why
			continue
		}
		ini, rerr := renderChained(p, src)
		if rerr != nil {
			if res.Invalid == nil {
				res.Invalid = map[string]string{}
			}
			res.Invalid[p.Name] = InvalidReason(p, rerr)
			continue
		}
		if why := defaultClashChained(defaults, p, chainRegion(p, src)); why != "" {
			if res.DefaultClash == nil {
				res.DefaultClash = map[string]string{}
			}
			res.DefaultClash[p.Name] = why
			continue
		}
		body.WriteString("\n")
		body.WriteString(ini)
		res.Exported = append(res.Exported, p.Name)
	}
	if len(res.Exported) == 0 {
		return user, res, nil
	}
	var b strings.Builder
	b.WriteString(user)
	if user != "" {
		if !strings.HasSuffix(user, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	b.WriteString(blockBegin + "\n")
	b.WriteString("# Use: aws --profile <name> ... / af-aws-exec --profile <name> --account <id> -- <command>\n")
	b.WriteString(body.String())
	b.WriteString("\n" + blockEnd + "\n")
	return b.String(), res, nil
}

// chainRegion is the region of a role-chaining profile: its own, else its source's (the SSO
// region when the source names none), as an SSO profile falls back to its SSO region.
func chainRegion(p, src Profile) string {
	switch {
	case p.Region != "":
		return p.Region
	case src.Region != "":
		return src.Region
	}
	return src.SSORegion
}

func renderChained(p, src Profile) (string, error) {
	return sessionx.RenderAssumeRoleProfile(session.SSMMeta{
		Profile: p.Name, SourceProfile: p.SourceProfile, RoleARN: p.RoleARN, ExternalID: p.ExternalID,
		RoleSessionName: p.SessionName, DurationSeconds: p.DurationSeconds, Region: chainRegion(p, src),
	})
}

// chainBrokenReason says why the role-chaining profile p cannot be exported, or "". The
// source must be a Settings SSO profile that this very render writes: a source that is
// shadowed by the member's own definition of the name would hand the role chain to
// whatever that definition holds, long-lived keys included.
func chainBrokenReason(p, src Profile, haveSrc bool, exportedSSO map[string]bool) string {
	switch {
	case p.SourceProfile == "" || !haveSrc:
		return fmt.Sprintf("its source profile %q is not a Settings profile; pick another in Settings > AWS profiles/SSM", p.SourceProfile)
	case src.Chained():
		return fmt.Sprintf("its source profile %q assumes a role itself; a source must be an SSO profile", p.SourceProfile)
	case !exportedSSO[p.SourceProfile]:
		return fmt.Sprintf("its source profile %q is not exported (`af-aws-exec --list` says why)", p.SourceProfile)
	}
	return ""
}

// defaultClashChained says why a [DEFAULT] line in ~/.aws/config would break the exported
// role-chaining profile p, or "". [DEFAULT] lends its keys to every profile, so a second
// way to get credentials there would give the name two meanings, and a value for a key the
// block writes differently would change the role or its parameters.
func defaultClashChained(defaults map[string]string, p Profile, region string) string {
	written := map[string]string{"role_arn": p.RoleARN, "source_profile": p.SourceProfile}
	if p.ExternalID != "" {
		written["external_id"] = p.ExternalID
	}
	if p.SessionName != "" {
		written["role_session_name"] = p.SessionName
	}
	if p.DurationSeconds != 0 {
		written["duration_seconds"] = strconv.Itoa(p.DurationSeconds)
	}
	if region != "" {
		written["region"] = region
	}
	keys := make([]string, 0, len(defaults))
	for k := range defaults {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		switch {
		case strings.HasPrefix(k, "sso_"), k == "credential_source", k == "credential_process", k == "web_identity_token_file",
			k == "mfa_serial", k == "aws_access_key_id", k == "aws_secret_access_key", k == "aws_session_token":
			// Never quote the value: for the key names above it may be a secret.
			return fmt.Sprintf("%s is set in [DEFAULT], which would give this profile a second way to get credentials", k)
		}
		if w, ok := written[k]; ok && defaults[k] != w {
			return fmt.Sprintf("%s = %q differs from this profile's %q", k, defaults[k], w)
		}
	}
	return ""
}

func renderProfile(p Profile) (string, error) {
	return sessionx.RenderSSMConfig(session.SSMMeta{
		Profile: p.Name, StartURL: p.StartURL, SSORegion: p.SSORegion,
		AccountID: p.AccountID, RoleName: p.RoleName, Region: p.Region,
	})
}

// invalidFields maps sessionx's allowlist fields to the words a user knows, the Settings
// value, and what is allowed. The values are the CP's non-secret SSO settings.
var invalidFields = []struct {
	field, words, allowed string
	value                 func(Profile) string
}{
	{"profile", "the profile name (from the Settings label)", "letters, digits and ._@- (at most 64)", func(p Profile) string { return p.Name }},
	{"sso start url", "the start URL", "https:// followed by printable characters without spaces", func(p Profile) string { return p.StartURL }},
	{"sso region", "the SSO region", "lower-case letters, digits and -, at most 32", func(p Profile) string { return p.SSORegion }},
	{"region", "the region", "lower-case letters, digits and -, at most 32", func(p Profile) string { return p.Region }},
	{"account id", "the account", "digits only, at most 20", func(p Profile) string { return p.AccountID }},
	{"role name", "the role name", "letters, digits and +=,.@_-, at most 64", func(p Profile) string { return p.RoleName }},
}

// InvalidReason turns the allowlist's refusal of p (err, from RenderSSMConfig) into a
// sentence naming the Settings field, its value and what an AWS config can hold.
func InvalidReason(p Profile, err error) string {
	msg := err.Error()
	if p.Chained() {
		for _, f := range []struct{ field, words string }{
			{"role arn", "the role ARN"}, {"external id", "the external ID"}, {"role session name", "the session name"},
			{"duration", "the duration"}, {"region", "the region"}, {"source profile", "the source profile"}, {"profile", "the profile name (from the Settings label)"},
		} {
			if strings.HasSuffix(msg, "invalid "+f.field) {
				return fmt.Sprintf("%s of this role-chaining profile is not a value an AWS config can hold; fix it in Settings > AWS profiles/SSM", f.words)
			}
		}
	}
	for _, f := range invalidFields {
		switch {
		case strings.HasSuffix(msg, "invalid "+f.field):
			return fmt.Sprintf("%s %q is not a value an AWS config can hold (allowed: %s); fix it in Settings > AWS profiles/SSM", f.words, f.value(p), f.allowed)
		case strings.HasSuffix(msg, f.field+" is required"):
			return fmt.Sprintf("%s is missing; set it in Settings > AWS profiles/SSM", f.words)
		}
	}
	return "a Settings value cannot be written to the AWS config; check the profile in Settings > AWS profiles/SSM"
}

// IncompleteReason says why a Settings profile without both an account and a role is
// not exported, or "". With neither, the CLI's SSO provider does not claim the profile
// and `aws --profile <name>` falls through to the workspace's own (workload) role
// (measured with aws-cli 2.36.46 against a container-credentials endpoint). With only
// one, the CLI fails ("configured to use SSO but is missing required configuration"),
// which is no fallback but no use either.
func IncompleteReason(p Profile) string {
	if p.Chained() {
		return ""
	}
	switch {
	case p.AccountID == "" && p.RoleName == "":
		return "no account and role in Settings; `aws --profile` would fall back to the workspace's own role"
	case p.RoleName == "":
		return "Settings has an account but no role; set both"
	case p.AccountID == "":
		return "Settings has a role but no account; set both"
	}
	return ""
}

// defaultClash says why a [DEFAULT] line in ~/.aws/config would break the exported
// profile p, or "". [DEFAULT] lends its keys to both the profile and its sso-session:
//   - botocore refuses any key the two give with different values, so a [DEFAULT] value
//     for a key the block writes differently is a clash (measured: [DEFAULT] region under
//     a profile with its own region is "inconsistent between profile and sso-session");
//   - a [DEFAULT] role_arn (unless next to an empty web_identity_token_file) or a
//     web_identity_token_file path takes every profile off SSO: the Settings name would
//     then assume another role, possibly in another account (measured: the CLI goes to
//     AssumeRole). Same rule as checkSSOProfile.
func defaultClash(defaults map[string]string, p Profile) string {
	webID, hasWebID := defaults["web_identity_token_file"]
	if _, ok := defaults["role_arn"]; ok && !(hasWebID && webID == "") {
		return fmt.Sprintf("role_arn = %q would make the AWS CLI assume that role instead of this SSO profile", defaults["role_arn"])
	}
	if hasWebID && webID != "" {
		return fmt.Sprintf("web_identity_token_file = %q would make the AWS CLI use web identity instead of this SSO profile", webID)
	}
	region := p.Region
	if region == "" {
		region = p.SSORegion
	}
	written := map[string]string{
		"sso_session": "af-" + p.Name, "sso_start_url": p.StartURL, "sso_region": p.SSORegion,
		"sso_registration_scopes": "sso:account:access", "region": region,
	}
	if p.AccountID != "" {
		written["sso_account_id"] = p.AccountID
	}
	if p.RoleName != "" {
		written["sso_role_name"] = p.RoleName
	}
	keys := make([]string, 0, len(written))
	for k := range written {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if v, ok := defaults[k]; ok && v != written[k] {
			return fmt.Sprintf("%s = %q differs from this profile's %q, and the AWS CLI refuses a profile whose value differs "+
				"from its sso-session's", k, v, written[k])
		}
	}
	return ""
}

// stripBlock removes the managed block (and the blank line render put before it). A
// begin marker without its end marker means someone edited the block by hand; guessing
// where our part stops could delete their lines, so it is an error instead.
func stripBlock(s string) (string, error) {
	i := strings.Index(s, blockBegin)
	if i < 0 {
		return s, nil
	}
	rest := s[i:]
	j := strings.Index(rest, blockEnd)
	if j < 0 {
		return s, fmt.Errorf("~/.aws/config has the agent-fleet begin marker but no end marker; restore or remove the block by hand")
	}
	after := rest[j+len(blockEnd):]
	after = strings.TrimPrefix(after, "\n")
	return strings.TrimSuffix(s[:i], "\n") + after, nil
}

// configNames lists the profile and sso-session names a config file defines, as
// botocore reads them ([profile "prod"] is prod).
func configNames(s string) (profiles, ssoSessions map[string]bool) {
	profiles, ssoSessions = map[string]bool{}, map[string]bool{}
	scanINI(s, func(l iniLine) {
		if !l.header {
			return
		}
		switch kind, name := configSection(l.section); kind {
		case "profile":
			profiles[name] = true
		case "sso-session":
			ssoSessions[name] = true
		}
	})
	return profiles, ssoSessions
}

// credentialsNames lists the profile names a credentials file defines: there a section
// name is the profile name verbatim.
func credentialsNames(s string) map[string]bool {
	out := map[string]bool{}
	scanINI(s, func(l iniLine) {
		if l.header && l.section != "DEFAULT" {
			out[l.section] = true
		}
	})
	return out
}

// StartSync applies the member's profiles once at agent boot and then keeps polling,
// warning of expiring logins after each pull.
func StartSync() {
	cloudbridge.Poll(func(why string) {
		syncAndLog(why)
		warnExpiringSSO(time.Now())
	})
}

func syncAndLog(why string) {
	res, err := Sync()
	switch {
	case errors.Is(err, ErrBridgeOff):
		return
	case err != nil && res.FromCache:
		log.Printf("aws profiles sync (%s): %v; re-applied the last list from Settings", why, err)
	case err != nil:
		log.Printf("aws profiles sync (%s): %v (keeping ~/.aws/config as is)", why, err)
		return
	}
	if len(res.Shadowed) > 0 {
		log.Printf("aws profiles sync (%s): not exported, name already used in ~/.aws or reserved: %s", why, strings.Join(res.Shadowed, ", "))
	}
	for _, c := range res.Conflicts {
		log.Printf("aws profiles sync (%s): not exported, Settings labels %s all map to %q", why, strings.Join(c.Labels, " / "), c.Name)
	}
	for n, reason := range res.Incomplete {
		log.Printf("aws profiles sync (%s): %q not exported: %s", why, n, reason)
	}
	for _, n := range res.SessionShadowed {
		log.Printf("aws profiles sync (%s): %q not exported: ~/.aws/config has its own [sso-session af-%s]", why, n, n)
	}
	for n, reason := range res.DefaultClash {
		log.Printf("aws profiles sync (%s): %q not exported: [DEFAULT] %s (in ~/.aws/config)", why, n, reason)
	}
	for n, reason := range res.ChainBroken {
		log.Printf("aws profiles sync (%s): %q not exported: %s", why, n, reason)
	}
	for n, reason := range res.Invalid {
		log.Printf("aws profiles sync (%s): %q not exported, a Settings value cannot be written to the AWS config (%s)", why, n, reason)
	}
	if res.Changed {
		log.Printf("aws profiles sync (%s): %d profile(s) in ~/.aws/config", why, len(res.Exported))
	}
}

// ExportedIn lists the profile names currently in the managed block of the config at
// path, for `af-aws-exec --list` when the CP cannot be asked.
func ExportedIn(path string) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	s := string(b)
	i := strings.Index(s, blockBegin)
	if i < 0 {
		return nil
	}
	j := strings.Index(s[i:], blockEnd)
	if j < 0 {
		return nil
	}
	var out []string
	scanINI(s[i:i+j], func(l iniLine) {
		if kind, name := configSection(l.section); l.header && kind == "profile" {
			out = append(out, name)
		}
	})
	return out
}

// settingsCachePath is the cache of the last list the CP sent. The shadow and collision
// checks in af-aws-exec, and the offline re-apply, need it when the CP cannot be reached:
// the block alone cannot serve, since shadowed and colliding names are exactly the ones
// it leaves out.
func settingsCachePath() string { return bridge.CachePath() }

func saveSettingsCache(ps []Profile, conflicts []Conflict) error {
	return bridge.SaveCache(cloudbridge.List[Profile]{Profiles: ps, Conflicts: conflicts})
}

// cachedList is the last list the CP gave, bound to this membership.
func cachedList() ([]Profile, []Conflict, bool) {
	l, ok := bridge.Cached()
	return l.Profiles, l.Conflicts, ok
}

// CachedSettings returns the last list the CP gave (saved whenever a fetch succeeds, even
// if writing the block then failed), for when the CP cannot be asked now; ok is false
// when there is none for this membership.
func CachedSettings() (map[string]Profile, []Conflict, bool) {
	ps, conflicts, ok := cachedList()
	if !ok {
		return nil, nil, false
	}
	m := map[string]Profile{}
	for _, p := range ps {
		m[p.Name] = p
	}
	return m, conflicts, true
}

// DescribeProfile returns the SSO account and role the member's AWS files give name.
func DescribeProfile(name string) (account, role string) {
	k, _ := profileKeys(nil, name)
	if arn, ok := k["role_arn"]; ok && k["sso_account_id"] == "" {
		// A role-chaining profile: the account and role are the ARN's.
		if a, r, ok := roleARNParts(scalar(arn)); ok {
			return a, r
		}
	}
	return k["sso_account_id"], k["sso_role_name"]
}
