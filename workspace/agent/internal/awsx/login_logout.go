package awsx

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/cloudlogin"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// Logging out of one Settings profile. `aws sso logout` cannot be pointed at one profile:
// it revokes and deletes every token in ~/.aws/sso/cache and every SSO role credential in
// ~/.aws/cli/cache whatever --profile says (measured with aws-cli 2.36.46), and it exits 0
// when AWS refuses the revoke, so its status says nothing. The Agent therefore calls the
// portal's Logout API for this profile's token itself and deletes this profile's cache files.

// ssoLogoutTimeout bounds the revoke call. A logout that cannot reach AWS still signs out
// here; it only leaves the session at AWS to run to its end.
const ssoLogoutTimeout = 30 * time.Second

// attemptExitWait bounds how long a logout waits for an ended login's CLI to exit. The kill
// is SIGKILL to its process group, so this is only scheduling latency.
const attemptExitWait = 5 * time.Second

type profileLogoutWire struct {
	// Revoked is true once AWS answered the Logout call with success.
	Revoked bool `json:"revoked"`
	// NoToken: there was no cached token, so there was nothing to revoke.
	NoToken bool   `json:"noToken,omitempty"`
	Message string `json:"message,omitempty"`
}

// ssoRegionRe is what a token's region must look like before it names a host the token is
// sent to: the cache file is writable by every agent.
var ssoRegionRe = regexp.MustCompile(`^[a-z]{2}(-gov)?-[a-z]+-[0-9]+$`)

// ssoPortalURL is the portal endpoint of region; overridden by tests.
var ssoPortalURL = func(region string) string {
	host := "portal.sso." + region + ".amazonaws.com"
	if strings.HasPrefix(region, "cn-") {
		host += ".cn"
	}
	return "https://" + host
}

// revokeSSOToken asks AWS to end the session the cached login doc belongs to (the SSO
// portal's Logout: POST /logout with the access token in x-amz-sso_bearer_token).
// fallbackRegion is used when the cache records none. The token never leaves this function
// except in that header.
func revokeSSOToken(cached []byte, fallbackRegion string) error {
	var doc struct {
		AccessToken string `json:"accessToken"`
		Region      string `json:"region"`
	}
	if json.Unmarshal(cached, &doc) != nil || doc.AccessToken == "" {
		return fmt.Errorf("the cached login holds no access token")
	}
	region := doc.Region
	if region == "" {
		region = fallbackRegion
	}
	if !ssoRegionRe.MatchString(region) {
		return fmt.Errorf("the cached login names no usable region (%q)", region)
	}
	ctx, cancel := context.WithTimeout(context.Background(), ssoLogoutTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ssoPortalURL(region)+"/logout", nil)
	if err != nil {
		return err
	}
	req.Header.Set("x-amz-sso_bearer_token", doc.AccessToken)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach AWS: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusOK {
		return nil
	}
	// An expired access token is refused (401) even while the session behind it could still
	// be renewed: that session then runs to its end.
	body, _ := io.ReadAll(io.LimitReader(res.Body, 300))
	return fmt.Errorf("AWS answered %d: %s", res.StatusCode, strings.TrimSpace(string(body)))
}

// ssoRoleCachePath is where the CLI keeps the role credentials it got for an sso-session
// profile: the SHA-1 of the sorted, compact JSON of account, role and session name, as
// botocore's json.dumps writes it (verified against the file aws-cli 2.36.46 wrote).
func ssoRoleCachePath(accountID, roleName, ssoSession string) string {
	key := `{"accountId":` + pyJSONString(accountID) + `,"roleName":` + pyJSONString(roleName) +
		`,"sessionName":` + pyJSONString(ssoSession) + `}`
	sum := sha1.Sum([]byte(key))
	return filepath.Join(paths.HomeDir(), ".aws", "cli", "cache", hex.EncodeToString(sum[:])+".json")
}

// pyJSONString quotes s the way Python's json.dumps does with its default ensure_ascii:
// everything outside printable ASCII becomes \uXXXX (UTF-16, so surrogate pairs above the
// BMP). encoding/json differs: it keeps UTF-8 as is and escapes <, > and &, so the key
// would not match.
func pyJSONString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			switch {
			case r < 0x20 || (r > 0x7e && r < 0x10000):
				fmt.Fprintf(&b, `\u%04x`, r)
			case r >= 0x10000:
				hi, lo := utf16.EncodeRune(r)
				fmt.Fprintf(&b, `\u%04x\u%04x`, hi, lo)
			default:
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// roleCachePaths are the role-credential files the profile's logins may have written. The
// managed block in ~/.aws/config and Settings can disagree for a while (Settings is cached
// before the block is rewritten, and a write can fail), and af-aws-exec keys its cache by
// Settings while a plain `aws --profile` keys it by the block, so both go.
func roleCachePaths(sp Profile, ssoSession string) []string {
	out := []string{ssoRoleCachePath(sp.AccountID, sp.RoleName, ssoSession)}
	keys := map[string]string{}
	if err := readINISection(ConfigPath(), configPicker("profile", sp.Name), keys); err == nil &&
		keys["sso_session"] == ssoSession && keys["sso_account_id"] != "" && keys["sso_role_name"] != "" {
		if p := ssoRoleCachePath(keys["sso_account_id"], keys["sso_role_name"], ssoSession); !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// endLoginFor ends the login attempt running for ssoSession, if any, and waits for its CLI
// to exit so the token it might be writing cannot land after the logout. A login started
// after this point is a new login, and it wins.
func endLoginFor(ssoSession string) {
	a := logins.Current(ssoSession)
	if a == nil {
		return
	}
	a.End(cloudlogin.PhaseCancelled, "logged out")
	if done := a.Exited(); done != nil {
		select {
		case <-done:
		case <-time.After(attemptExitWait):
		}
	}
}

// beforePutBack lets a test land a login between the read and the put-back.
var beforePutBack = func() {}

// takeOffToken deletes the token file at path if it still holds snap. A login that landed
// since snap was read wrote a newer token, which is put back unless a newer one still is
// already there.
func takeOffToken(path string, snap []byte) error {
	tmp := path + ".af-logout"
	if err := os.Rename(path, tmp); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	b, err := os.ReadFile(tmp)
	beforePutBack()
	if err == nil && !bytes.Equal(b, snap) {
		// Link, not rename: a still newer token written at path meanwhile must not be replaced.
		if lerr := os.Link(tmp, path); lerr != nil && !os.IsExist(lerr) {
			return lerr
		}
	}
	return os.Remove(tmp)
}

// HandleProfileLogout is POST /aws-login/profiles/{name}/logout: the "Log out" of a Settings
// profile. It ends a running login for the profile, revokes the cached token with AWS, and
// deletes the token and the role credentials cached for it. Other profiles' caches are not
// touched. Role credentials a command already holds stay valid until they expire: AWS has no
// call that revokes them.
func HandleProfileLogout(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	sp, ok := loginSettings()[name]
	switch {
	case !ok:
		httpx.WriteErr(w, http.StatusNotFound, "not_a_settings_profile", "no Settings profile with that name reached this workspace")
		return
	case !slices.Contains(ExportedIn(ConfigPath()), name):
		// The member's own ~/.aws may define an sso-session af-<name>; its token is theirs.
		httpx.WriteErr(w, http.StatusConflict, "not_exported", "this profile is not in ~/.aws/config; `af-aws-exec --list` says why")
		return
	}
	ssoSession := "af-" + sp.Name
	// From ending the running login until the old token is off disk, no new login starts
	// (cloudlogin's Gate); the revoke after that runs without it.
	gate := logins.Gate(ssoSession)
	gate.Lock()
	gateHeld := true
	defer func() {
		if gateHeld {
			gate.Unlock()
		}
	}()
	endLoginFor(ssoSession)
	// Held across the deletes and the revoke: an af-aws-exec run already turning the token
	// into role credentials finishes first, and the next one finds nothing. Without the lock
	// that race is open again, so no lock means no logout.
	unlock, err := logins.LockKey(ssoSession, false)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "lock_failed", err.Error())
		return
	}
	defer unlock()

	var out profileLogoutWire
	token := ssoCachePath(ssoSession)
	// The login is read once, taken off disk, and only then revoked from memory: the revoke
	// can take seconds, and a login the member starts meanwhile is a new one whose token
	// must survive.
	snap, err := os.ReadFile(token)
	switch {
	case errors.Is(err, os.ErrNotExist):
		out.NoToken = true
	case err != nil:
		httpx.WriteErr(w, http.StatusInternalServerError, "remove_failed", err.Error())
		return
	}
	for _, p := range roleCachePaths(sp, ssoSession) {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			httpx.WriteErr(w, http.StatusInternalServerError, "remove_failed", err.Error())
			return
		}
	}
	if !out.NoToken {
		if err := takeOffToken(token, snap); err != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, "remove_failed", err.Error())
			return
		}
	}
	gate.Unlock()
	gateHeld = false
	if !out.NoToken {
		if err := revokeSSOToken(snap, sp.SSORegion); err != nil {
			out.Message = err.Error()
		} else {
			out.Revoked = true
		}
	}
	log.Printf("aws-login: logout profile=%s revoked=%t no_token=%t relayed=%t", sp.Name, out.Revoked, out.NoToken, cloudlogin.RelayedByCP(r))
	httpx.WriteJSON(w, http.StatusOK, out)
}
