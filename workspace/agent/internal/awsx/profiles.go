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
	"regexp"
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
	// Shadowed are profiles not exported because ~/.aws/config already defines the same
	// profile or sso-session name outside the block.
	Shadowed []string
	// Invalid are profiles refused by the INI allowlist (sessionx.RenderSSMConfig).
	Invalid []string
	Changed bool
}

// ConfigPath is the file the AWS CLI and SDKs read by default. AWS_CONFIG_FILE is
// deliberately not honoured: a shell that exported it for one session would otherwise
// steer the managed block into that session's private file.
func ConfigPath() string { return filepath.Join(paths.HomeDir(), ".aws", "config") }

// Fetch pulls the member's profiles from the CP.
func Fetch() ([]Profile, error) {
	base := strings.TrimRight(os.Getenv("AF_CP_BASE_URL"), "/")
	token := os.Getenv("AF_AWS_PROFILES_TOKEN")
	if base == "" || token == "" {
		return nil, ErrBridgeOff
	}
	req, err := http.NewRequest(http.MethodGet, base+"/internal/aws-profiles", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("CP AWS profiles API error (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var wire struct {
		Profiles []Profile `json:"profiles"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, fmt.Errorf("CP AWS profiles response is not JSON: %w", err)
	}
	return wire.Profiles, nil
}

// Sync pulls and applies. Fail-open: when the CP cannot be reached the file keeps the
// previous block, so a CP blip never takes working profiles away.
func Sync() (SyncResult, error) {
	ps, err := Fetch()
	if err != nil {
		return SyncResult{}, err
	}
	return Apply(ConfigPath(), ps)
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
	next, res, err := render(string(old), ps)
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

var sectionRe = regexp.MustCompile(`^\s*\[\s*([^\]]*?)\s*\]`)

// render returns the config text with the managed block replaced by one holding ps.
func render(old string, ps []Profile) (string, SyncResult, error) {
	var res SyncResult
	user, err := stripBlock(old)
	if err != nil {
		return old, res, err
	}
	profiles, ssoSessions := userSections(user)
	var body strings.Builder
	for _, p := range ps {
		if profiles[p.Name] || ssoSessions["af-"+p.Name] {
			res.Shadowed = append(res.Shadowed, p.Name)
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

// userSections lists the profile and sso-session names the member defined themselves.
func userSections(s string) (profiles, ssoSessions map[string]bool) {
	profiles, ssoSessions = map[string]bool{}, map[string]bool{}
	for _, line := range strings.Split(s, "\n") {
		m := sectionRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		f := strings.Fields(m[1])
		switch {
		case len(f) == 1:
			profiles[f[0]] = true // [default] and friends
		case len(f) == 2 && f[0] == "profile":
			profiles[f[1]] = true
		case len(f) == 2 && f[0] == "sso-session":
			ssoSessions[f[1]] = true
		}
	}
	return profiles, ssoSessions
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
		log.Printf("aws profiles sync (%s): not exported, already defined in ~/.aws/config: %s", why, strings.Join(res.Shadowed, ", "))
	}
	if len(res.Invalid) > 0 {
		log.Printf("aws profiles sync (%s): not exported, refused by validation: %s", why, strings.Join(res.Invalid, ", "))
	}
	if res.Changed {
		log.Printf("aws profiles sync (%s): %d profile(s) in ~/.aws/config", why, len(res.Exported))
	}
}
