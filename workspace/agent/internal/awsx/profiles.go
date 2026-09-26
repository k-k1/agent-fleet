// Package awsx makes the member's SSO profiles (Settings → SSM, stored in the CP) usable
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
package awsx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
)

// PollInterval matches the other CP-backed pulls: an edit in Settings lands without
// anyone asking, and `af-aws-exec` pulls on demand for the "use it right now" case.
const PollInterval = 5 * time.Minute

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
}

// SyncResult reports one sync for the log and for `af-aws-exec --list`.
type SyncResult struct {
	Exported []string // profile names now in the managed block
	// Shadowed are profiles not exported because ~/.aws/config or ~/.aws/credentials
	// already defines the same profile or sso-session name, or because the name is
	// "default" (see render).
	Shadowed []string
	// Invalid are profiles refused by the INI allowlist (sessionx.RenderSSMConfig).
	Invalid []string
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
	Changed      bool
	// Settings is every profile the CP sent, by name, so af-aws-exec can tell a name the
	// member's own ~/.aws definition shadows from the Settings profile of that name.
	Settings map[string]Profile
	// Conflicts are names two or more Settings labels sanitize to; the CP exports none of
	// them.
	Conflicts []Conflict
	// Fetched is true when Settings and Conflicts came from the CP on this call, even if
	// writing ~/.aws/config then failed: fresh answers must not be swapped for the cache.
	Fetched bool
}

// Conflict is a profile name two or more Settings labels map to.
type Conflict struct {
	Name   string   `json:"name"`
	Labels []string `json:"labels"`
}

// ConfigPath is the file the AWS CLI and SDKs read by default. AWS_CONFIG_FILE is
// deliberately not honoured: a shell that exported it for one session would otherwise
// steer the managed block into that session's private file.
func ConfigPath() string { return filepath.Join(paths.HomeDir(), ".aws", "config") }

// Fetch pulls the member's profiles from the CP, with the names it left out because
// two labels collide.
func Fetch() ([]Profile, []Conflict, error) {
	base := strings.TrimRight(os.Getenv("AF_CP_BASE_URL"), "/")
	token := os.Getenv("AF_AWS_PROFILES_TOKEN")
	if base == "" || token == "" {
		return nil, nil, ErrBridgeOff
	}
	req, err := http.NewRequest(http.MethodGet, base+"/internal/aws-profiles", nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return nil, nil, fmt.Errorf("CP AWS profiles API error (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var wire struct {
		Profiles  []Profile  `json:"profiles"`
		Conflicts []Conflict `json:"conflicts"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, nil, fmt.Errorf("CP AWS profiles response is not JSON: %w", err)
	}
	return wire.Profiles, wire.Conflicts, nil
}

// Sync pulls and applies. Fail-open: when the CP cannot be reached the file keeps the
// previous block, so a CP blip never takes working profiles away.
func Sync() (SyncResult, error) {
	ps, conflicts, err := Fetch()
	if err != nil {
		return SyncResult{}, err
	}
	res, err := Apply(ConfigPath(), ps)
	res.Settings = map[string]Profile{}
	for _, p := range ps {
		res.Settings[p.Name] = p
	}
	res.Conflicts = conflicts
	res.Fetched = true
	if err == nil {
		saveSettingsCache(ps, conflicts)
	}
	return res, err
}

// Apply rewrites the managed block of the config at path to hold ps. It writes only when
// the content changes, never creates the file just to hold an empty block, and replaces
// the file atomically under a lock so a concurrent sync (the poll and an af-aws-exec)
// cannot interleave a read-modify-write.
func Apply(path string, ps []Profile) (SyncResult, error) {
	target, err := resolveLink(path)
	if err != nil {
		return SyncResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return SyncResult{}, err
	}
	unlock, err := lockDir(filepath.Dir(target))
	if err != nil {
		return SyncResult{}, err
	}
	defer unlock()

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
	creds, _ := os.ReadFile(filepath.Join(filepath.Dir(path), "credentials"))
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
	for _, p := range ps {
		if profiles[p.Name] {
			res.Shadowed = append(res.Shadowed, p.Name)
			continue
		}
		if ssoSessions["af-"+p.Name] {
			res.SessionShadowed = append(res.SessionShadowed, p.Name)
			continue
		}
		// Without an account and a role the CLI does not treat the profile as SSO at all
		// and the default chain goes on to the container's role (measured with aws-cli
		// 2.36.46 against a container-credentials endpoint: the rendered profile returned
		// the workload credentials). Exporting the name would hand users that trap.
		if why := IncompleteReason(p); why != "" {
			if res.Incomplete == nil {
				res.Incomplete = map[string]string{}
			}
			res.Incomplete[p.Name] = why
			continue
		}
		if why := defaultClash(defaults, p); why != "" {
			if res.DefaultClash == nil {
				res.DefaultClash = map[string]string{}
			}
			res.DefaultClash[p.Name] = why
			continue
		}
		ini, rerr := sessionx.RenderSSMConfig(session.SSMMeta{
			Profile: p.Name, StartURL: p.StartURL, SSORegion: p.SSORegion,
			AccountID: p.AccountID, RoleName: p.RoleName, Region: p.Region,
		})
		if rerr != nil {
			res.Invalid = append(res.Invalid, p.Name)
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
	b.WriteString("# Use: aws --profile <name> ... / AWS_PROFILE=<name> / af-aws-exec --profile <name> -- <command>\n")
	b.WriteString(body.String())
	b.WriteString("\n" + blockEnd + "\n")
	return b.String(), res, nil
}

// IncompleteReason says why a Settings profile without both an account and a role is
// not exported, or "". With neither, the CLI's SSO provider does not claim the profile
// and `aws --profile <name>` falls through to the workspace's own (workload) role
// (measured with aws-cli 2.36.46 against a container-credentials endpoint). With only
// one, the CLI fails ("configured to use SSO but is missing required configuration"),
// which is no fallback but no use either.
func IncompleteReason(p Profile) string {
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

// StartSync applies the member's profiles once at agent boot and then keeps polling.
// The first pull is in the goroutine too: boot must not wait on the CP.
func StartSync() {
	go func() {
		syncAndLog("agent boot")
		for range time.Tick(PollInterval) {
			syncAndLog("poll")
		}
	}()
}

func syncAndLog(why string) {
	res, err := Sync()
	switch {
	case errors.Is(err, ErrBridgeOff):
		return
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
	if len(res.Invalid) > 0 {
		log.Printf("aws profiles sync (%s): not exported, refused by validation: %s", why, strings.Join(res.Invalid, ", "))
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

// settingsCachePath keeps the last list the CP sent (non-secret, like the block), so the
// shadow and collision checks in af-aws-exec still have something to check against when
// the CP cannot be reached. The block alone cannot serve: shadowed and colliding names
// are exactly the ones it leaves out.
func settingsCachePath() string {
	return filepath.Join(filepath.Dir(ConfigPath()), ".agent-fleet-settings.json")
}

type settingsCache struct {
	Profiles  []Profile  `json:"profiles"`
	Conflicts []Conflict `json:"conflicts,omitempty"`
}

func saveSettingsCache(ps []Profile, conflicts []Conflict) {
	b, err := json.Marshal(settingsCache{Profiles: ps, Conflicts: conflicts})
	if err != nil {
		return
	}
	path := settingsCachePath()
	if old, rerr := os.ReadFile(path); rerr == nil && string(old) == string(b) {
		return
	}
	_ = writeAtomic(path, b, 0o600)
}

// CachedSettings returns the list saved by the last successful sync, for when the CP
// cannot be asked now; ok is false when there is none.
func CachedSettings() (map[string]Profile, []Conflict, bool) {
	b, err := os.ReadFile(settingsCachePath())
	if err != nil {
		return nil, nil, false
	}
	var c settingsCache
	if json.Unmarshal(b, &c) != nil {
		return nil, nil, false
	}
	m := map[string]Profile{}
	for _, p := range c.Profiles {
		m[p.Name] = p
	}
	return m, c.Conflicts, true
}

// DescribeProfile returns the SSO account and role the member's AWS files give name.
func DescribeProfile(name string) (account, role string) {
	k, _ := profileKeys(nil, name)
	return k["sso_account_id"], k["sso_role_name"]
}

// NotExported lists the Settings names (sorted) missing from exported: the ones the
// last sync left out, for --list when the CP cannot be asked now.
func NotExported(settings map[string]Profile, exported []string) []string {
	in := map[string]bool{}
	for _, n := range exported {
		in[n] = true
	}
	var out []string
	for n := range settings {
		if !in[n] {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}
