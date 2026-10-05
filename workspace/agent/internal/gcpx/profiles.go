// Package gcpx makes the member's Google Cloud profiles (Settings > Google Cloud, stored in
// the CP) usable through `af-gcloud-exec`, and runs a command with one profile's access
// token (ADR 0107 decisions 1 and 2).
//
//	agent ──(AF_GCP_PROFILES_TOKEN)──▶ CP GET /internal/gcp-profiles
//	                    │
//	    the Agent's own gcloud config root: paths.AgentStateDir()/gcloud
//	    one configuration af-<name> per exported profile
//
// The member's ~/.config/gcloud is never read or written: nothing here names it, and every
// gcloud this package runs gets CLOUDSDK_CONFIG pointed at the Agent's root, so the member's
// logins, active configuration and application default credentials stay theirs.
//
// gcpx is the Google Cloud backend of the provider-neutral packages: cloudbridge pulls the
// profiles and cloudexec is af-gcloud-exec's skeleton. What reads gcloud's own store stays
// here: the configurations, whether an account holds a user credential, and which gcloud
// errors mean "log in".
package gcpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/cloudbridge"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/cloudexec"
)

// ErrBridgeOff reports that this deployment injects no bridge. A normal state, not a
// failure: there are then no profiles, and the Agent's root is left as it is.
var ErrBridgeOff = errors.New("the Google Cloud profiles bridge is not configured in this deployment")

// LoginGoogle is the only login method of the first version (ADR 0107 decision 1);
// `workforce` is #1487.
const LoginGoogle = "google"

// Profile is one exported profile as the CP sends it. QuotaProject is the effective value:
// the CP fills in the project when the row leaves it empty.
type Profile struct {
	ID                        string `json:"id"`
	Name                      string `json:"name"`
	Label                     string `json:"label"`
	LoginMethod               string `json:"loginMethod"`
	Project                   string `json:"project"`
	QuotaProject              string `json:"quotaProject,omitempty"`
	Account                   string `json:"account,omitempty"`
	Region                    string `json:"region,omitempty"`
	Zone                      string `json:"zone,omitempty"`
	ImpersonateServiceAccount string `json:"impersonateServiceAccount,omitempty"`
	UpdatedAt                 string `json:"updatedAt,omitempty"`
}

// Conflict is a profile name two or more Settings labels map to.
type Conflict = cloudbridge.Conflict

// bridge pulls the Google Cloud profiles from the CP.
var bridge = &cloudbridge.Bridge[Profile]{Path: "/internal/gcp-profiles", TokenEnv: "AF_GCP_PROFILES_TOKEN",
	What: "Google Cloud profiles", CacheFile: "gcp-settings.json", OwnerLabel: "af-gcp-profiles-cache/v1",
	Target: "the Agent's gcloud configurations", ErrOff: ErrBridgeOff}

// ConfigRoot is the Agent's own gcloud config root (its CLOUDSDK_CONFIG). It holds the
// credentials of every profile's login, so it is only ever used through PrivateDir.
func ConfigRoot() string { return cloudexec.StateDir("gcloud") }

// ConfigName is the gcloud configuration of the profile called name.
func ConfigName(name string) string { return "af-" + name }

const (
	// stateFile records, per configuration, what the last sync wrote from Settings, so the
	// next one can tell whether a core/account in the file is the login's to keep.
	stateFile = ".agent-fleet-profiles.json"
	// lockFile serialises the sync, the mint and the account check across processes.
	lockFile = ".agent-fleet.lock"
)

// SyncResult reports one sync for the log and for `af-gcloud-exec --list`.
type SyncResult struct {
	// Exported are the profiles that now have a configuration, by name.
	Exported map[string]Profile
	// Invalid are profiles left without a configuration because a Settings value cannot be
	// written into one, by name, with the reason.
	Invalid map[string]string
	// Conflicts are names two or more Settings labels map to; the CP exports none of them.
	Conflicts []Conflict
	Changed   bool
	// Fetched is true when the list came from the CP on this call; FromCache when the CP
	// could not be asked and the last list it gave was applied.
	Fetched   bool
	FromCache bool
}

// Sync pulls the profiles and applies them to the Agent's root, under the root's lock
// (cloudbridge.Pull says why the lock spans the fetch). Without the CP it applies the
// cached list; without either the root is left as it is.
func Sync() (SyncResult, error) { return SyncNotify(nil) }

// SyncNotify is Sync that calls waiting first when another process holds the root's lock
// (a terminal login holds it for as long as the person takes): the wrapper's own sync says
// why it does not start, while the background poll stays silent.
func SyncNotify(waiting func()) (SyncResult, error) {
	return syncLocking(func() (string, func(), error) { return lockRootNotify(waiting) })
}

// syncLocking is Sync with lock as the way to take the root's lock.
func syncLocking(lock func() (string, func(), error)) (SyncResult, error) {
	var res SyncResult
	root := ""
	p, err := bridge.Pull(func() (func(), error) {
		r, unlock, lerr := lock()
		root = r
		return unlock, lerr
	}, func(l cloudbridge.List[Profile]) error {
		var aerr error
		res, aerr = applyLocked(root, l.Profiles)
		return aerr
	})
	res.Conflicts = p.Conflicts
	res.Fetched, res.FromCache = p.Fetched, p.FromCache
	return res, err
}

// lockRoot makes the Agent's root private and takes its lock; it returns the resolved root.
func lockRoot() (string, func(), error) { return lockRootNotify(nil) }

// lockRootNotify is lockRoot that calls waiting first when another process holds the lock
// (a terminal login holds it for as long as the person takes), so a wrapper can say why it
// does not start.
func lockRootNotify(waiting func()) (string, func(), error) {
	root, f, err := openRootLock()
	if err != nil {
		return "", nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if waiting != nil {
			waiting()
		}
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
			f.Close()
			return "", nil, err
		}
	}
	return root, func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// openRootLock makes the Agent's root private and opens its lock file (close-on-exec, so no
// gcloud the Agent starts inherits the lock).
func openRootLock() (string, *os.File, error) {
	root, err := cloudexec.PrivateDir(ConfigRoot())
	if err != nil {
		return "", nil, err
	}
	f, err := os.OpenFile(filepath.Join(root, lockFile), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return "", nil, err
	}
	return root, f, nil
}

// Apply writes ps into the Agent's root under its lock. Sync is the normal entry; this is
// for a caller that already has the list.
func Apply(ps []Profile) (SyncResult, error) {
	root, unlock, err := lockRoot()
	if err != nil {
		return SyncResult{}, err
	}
	defer unlock()
	return applyLocked(root, ps)
}

// syncedProfile is what one sync wrote from Settings for a configuration.
type syncedProfile struct {
	ID          string `json:"id"`
	LoginMethod string `json:"loginMethod"`
	Account     string `json:"account,omitempty"`
}

// Validation of what goes into a configuration. Each value is a single INI value, so none
// may hold whitespace, a line break or '%' (gcloud reads the files with Python's
// configparser); beyond that each is held to what Google accepts for it.
var (
	// gcloud refuses a configuration name that does not start with a lowercase letter or
	// holds anything outside a-z, 0-9 and '-' (measured on 587.0.0: af-Prod, af-prod_app,
	// af-prod.app are refused). The CP's name rule already produces this shape; it is
	// checked again because the configuration file name is built from it.
	nameRe = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	// A project id, or a legacy domain-scoped one (example.com:proj).
	projectRe = regexp.MustCompile(`^([a-z0-9][a-z0-9.-]{0,252}:)?[a-z][a-z0-9-]{4,28}[a-z0-9]$`)
	emailRe   = regexp.MustCompile(`^[A-Za-z0-9._+'-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$`)
	locRe     = regexp.MustCompile(`^[a-z][a-z0-9-]{1,62}$`)
)

// InvalidReason says why p cannot become a configuration, or "".
func InvalidReason(p Profile) string {
	switch {
	case !nameRe.MatchString(p.Name):
		return fmt.Sprintf("the name %q cannot be a gcloud configuration name (lowercase letter first, then a-z, 0-9 and -)", p.Name)
	case p.LoginMethod != LoginGoogle:
		return fmt.Sprintf("login method %q is not supported (only %q)", p.LoginMethod, LoginGoogle)
	case !projectRe.MatchString(p.Project):
		return fmt.Sprintf("the project %q is not a Google Cloud project id", p.Project)
	case p.QuotaProject != "" && !projectRe.MatchString(p.QuotaProject):
		return fmt.Sprintf("the quota project %q is not a Google Cloud project id", p.QuotaProject)
	case p.Account != "" && !emailRe.MatchString(p.Account):
		return fmt.Sprintf("the account %q is not an email address", p.Account)
	case p.Region != "" && !locRe.MatchString(p.Region):
		return fmt.Sprintf("the region %q is not a region name", p.Region)
	case p.Zone != "" && !locRe.MatchString(p.Zone):
		return fmt.Sprintf("the zone %q is not a zone name", p.Zone)
	case p.ImpersonateServiceAccount != "" && !emailRe.MatchString(p.ImpersonateServiceAccount):
		return fmt.Sprintf("the service account to impersonate %q is not an email address", p.ImpersonateServiceAccount)
	}
	return ""
}

// applyLocked is Apply for a caller that holds the root's lock (root is resolved).
//
// The Agent owns the root outright (decision 1): a configuration af-<name> whose profile is
// gone is removed, and each remaining one is rewritten whole, so any property other than
// Settings' own and the login's core/account disappears. The configurations are written as
// files rather than through `gcloud config configurations create --no-activate`: the poll
// then needs no gcloud installed and costs no gcloud start per property (~1 s each), and
// active_config is never touched, which is what --no-activate is for.
func applyLocked(root string, ps []Profile) (SyncResult, error) {
	res := SyncResult{Exported: map[string]Profile{}}
	dir := filepath.Join(root, "configurations")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return res, err
	}
	prev := readState(root)
	next := map[string]syncedProfile{}
	for _, p := range ps {
		if why := InvalidReason(p); why != "" {
			if res.Invalid == nil {
				res.Invalid = map[string]string{}
			}
			res.Invalid[p.Name] = why
			continue
		}
		cfg := configPath(root, p.Name)
		account := p.Account
		if account == "" {
			// The login owns core/account only while nothing that selected it changed: the
			// same profile (id), the same login method, and Settings still naming no
			// account. Anything else resets the selection, so the next run asks which
			// account to use instead of carrying one over.
			if old, ok := prev[p.Name]; ok && old.ID == p.ID && old.LoginMethod == p.LoginMethod && old.Account == "" {
				account = readProperty(cfg, "core", "account")
				if account != "" && !emailRe.MatchString(account) {
					account = ""
				}
			}
		}
		changed, err := writeIfChanged(cfg, renderConfig(p, account))
		if err != nil {
			return res, err
		}
		res.Changed = res.Changed || changed
		res.Exported[p.Name] = p
		next[p.Name] = syncedProfile{ID: p.ID, LoginMethod: p.LoginMethod, Account: p.Account}
	}
	// Remove the af- configurations whose profile is gone, with gcloud's per-configuration
	// cache next to them. Other configurations (gcloud's own `default`) are left alone.
	ents, err := os.ReadDir(dir)
	if err != nil {
		return res, err
	}
	for _, e := range ents {
		name, ok := strings.CutPrefix(e.Name(), "config_af-")
		if !ok || res.Exported[name].Name != "" {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			return res, err
		}
		_ = os.Remove(filepath.Join(root, ConfigName(name)+"_configs.db"))
		res.Changed = true
	}
	if err := writeState(root, next); err != nil {
		return res, err
	}
	return res, nil
}

// configProps is every property a configuration of p holds, as section/key: Settings'
// own and the account.
func configProps(p Profile, account string) map[string]string {
	m := map[string]string{"core/project": p.Project, "billing/quota_project": p.QuotaProject}
	if p.QuotaProject == "" {
		m["billing/quota_project"] = p.Project
	}
	for k, v := range map[string]string{"core/account": account, "compute/region": p.Region, "compute/zone": p.Zone,
		"auth/impersonate_service_account": p.ImpersonateServiceAccount} {
		if v != "" {
			m[k] = v
		}
	}
	return m
}

// renderConfig is the whole configuration file: Settings' properties and the account.
func renderConfig(p Profile, account string) string {
	props := configProps(p, account)
	var b strings.Builder
	b.WriteString("# Written by the agent-fleet Agent from Settings > Google Cloud; rewritten on every sync.\n")
	for i, section := range []string{"core", "billing", "compute", "auth"} {
		keys := []string{}
		for k := range props {
			if sec, key, _ := strings.Cut(k, "/"); sec == section {
				keys = append(keys, key)
			}
		}
		if len(keys) == 0 {
			continue
		}
		sort.Strings(keys)
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString("[" + section + "]\n")
		for _, k := range keys {
			b.WriteString(k + " = " + props[section+"/"+k] + "\n")
		}
	}
	return b.String()
}

// readProps reads every property of a gcloud configuration file as section/key. gcloud
// rewrites the file in its own layout when it sets a property (the login's core/account:
// measured on 587.0.0, the comment line goes and the key order changes), so files are
// compared by their properties, never byte for byte.
func readProps(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	cur := ""
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";"):
		case strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]"):
			cur = strings.TrimSpace(line[1 : len(line)-1])
		default:
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				k, v, ok = strings.Cut(line, ":")
			}
			if !ok {
				// A line that is no property (a continuation, say) makes the file one this
				// package does not understand; report it as a property nobody owns.
				k, v = line, ""
			}
			out[cur+"/"+strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out, nil
}

// readProperty reads one property of a gcloud configuration file ("" when absent).
func readProperty(path, section, key string) string {
	m, _ := readProps(path)
	return m[section+"/"+key]
}

func readState(root string) map[string]syncedProfile {
	out := map[string]syncedProfile{}
	b, err := os.ReadFile(filepath.Join(root, stateFile))
	if err != nil {
		return out
	}
	_ = json.Unmarshal(b, &out)
	return out
}

func writeState(root string, s map[string]syncedProfile) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	_, err = writeIfChanged(filepath.Join(root, stateFile), string(b)+"\n")
	return err
}

func writeIfChanged(path, content string) (bool, error) {
	if old, err := os.ReadFile(path); err == nil && string(old) == content {
		return false, nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".af-*")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	return true, os.Rename(tmp.Name(), path)
}

// ErrSettingsChanged means the profile a run read from Settings is no longer what the
// Agent's root holds: a sync between the run's own sync and its mint applied another
// version. Minting or logging in anyway would mix the run's snapshot (what it checks and
// prints) with another version's configuration (what gcloud uses).
var ErrSettingsChanged = errors.New("the profile changed in Settings while this run started; run it again")

// configPath is the configuration file of the profile called name under root.
func configPath(root, name string) string {
	return filepath.Join(root, "configurations", "config_"+ConfigName(name))
}

// syncedAs returns the account p's configuration selects, after checking, under the root's
// lock, that the configuration and the sync record are exactly what a sync of p wrote: the
// same id, login method and Settings account, and a file that holds p's properties and
// nothing else. Anything else is ErrSettingsChanged.
func syncedAs(root string, p Profile) (string, error) {
	cfg := configPath(root, p.Name)
	props, err := readProps(cfg)
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("%w (its gcloud configuration %s is gone)", ErrSettingsChanged, ConfigName(p.Name))
	} else if err != nil {
		return "", err
	}
	account := props["core/account"]
	st, ok := readState(root)[p.Name]
	if !ok || st != (syncedProfile{ID: p.ID, LoginMethod: p.LoginMethod, Account: p.Account}) ||
		!maps.Equal(props, configProps(p, account)) || (p.Account != "" && account != p.Account) ||
		(account != "" && !emailRe.MatchString(account)) {
		return "", ErrSettingsChanged
	}
	return account, nil
}

// ConfiguredAccount is the account the profile's configuration selects ("" for none).
func ConfiguredAccount(name string) string {
	return readProperty(configPath(ConfigRoot(), name), "core", "account")
}

// CachedSettings returns the last list the CP gave, for when it cannot be asked now; ok is
// false when there is none for this membership.
func CachedSettings() (map[string]Profile, []Conflict, bool) {
	l, ok := bridge.Cached()
	if !ok {
		return nil, nil, false
	}
	m := map[string]Profile{}
	for _, p := range l.Profiles {
		m[p.Name] = p
	}
	return m, l.Conflicts, true
}

// StartSync applies the member's profiles once at Agent boot and then keeps polling.
func StartSync() {
	cloudbridge.Poll(syncAndLog)
}

func syncAndLog(why string) {
	res, err := Sync()
	switch {
	case errors.Is(err, ErrBridgeOff):
		return
	case err != nil && res.FromCache:
		log.Printf("gcp profiles sync (%s): %v; re-applied the last list from Settings", why, err)
	case err != nil:
		log.Printf("gcp profiles sync (%s): %v (keeping the gcloud configurations as they are)", why, err)
		return
	}
	for _, c := range res.Conflicts {
		log.Printf("gcp profiles sync (%s): not exported, Settings labels %s all map to %q", why, strings.Join(c.Labels, " / "), c.Name)
	}
	names := make([]string, 0, len(res.Invalid))
	for n := range res.Invalid {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		log.Printf("gcp profiles sync (%s): %q has no configuration: %s", why, n, res.Invalid[n])
	}
	if res.Changed {
		log.Printf("gcp profiles sync (%s): %d gcloud configuration(s)", why, len(res.Exported))
	}
}
