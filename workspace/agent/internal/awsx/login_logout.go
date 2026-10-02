package awsx

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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
// ~/.aws/cli/cache whatever --profile says (measured with aws-cli 2.36.46), which would
// sign the member out of every other profile too. So the CLI is run against a throwaway
// HOME that holds this profile's token alone, and the Agent deletes this profile's two
// cache files itself.

// ssoLogoutTimeout bounds the revoke call. A logout that cannot reach AWS still signs out
// here; it only leaves the portal session to run to its end.
const ssoLogoutTimeout = 30 * time.Second

type profileLogoutWire struct {
	// Revoked is true once AWS accepted the revoke of the cached token.
	Revoked bool `json:"revoked"`
	// NoToken: there was no cached token, so there was nothing to revoke.
	NoToken bool   `json:"noToken,omitempty"`
	Message string `json:"message,omitempty"`
}

// revokeSSOToken runs `aws sso logout` with HOME pointed at a directory holding only the
// token at cachePath, so the CLI revokes that token and nothing else. Overridden by tests.
var revokeSSOToken = func(bin, cachePath string) error {
	home, err := os.MkdirTemp("", "af-aws-logout-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(home)
	b, err := os.ReadFile(cachePath)
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".aws", "sso", "cache")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, filepath.Base(cachePath)), b, 0o600); err != nil {
		return err
	}
	cfg := filepath.Join(home, "config")
	if err := os.WriteFile(cfg, nil, 0o600); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), ssoLogoutTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "sso", "logout")
	cmd.Env = withHome(verifierEnv(baseEnv(os.Environ()), cfg), home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// The CLI's own error (an unreachable endpoint, a token AWS no longer knows); the
		// token is never printed by it.
		msg := strings.TrimSpace(string(out))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		if msg == "" {
			msg = err.Error()
		}
		return errors.New(msg)
	}
	return nil
}

// withHome replaces HOME in env.
func withHome(env []string, home string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, "HOME=") {
			out = append(out, kv)
		}
	}
	return append(out, "HOME="+home)
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
	if a := logins.Current(ssoSession); a != nil {
		a.End(cloudlogin.PhaseCancelled, "logged out")
	}

	var out profileLogoutWire
	token := ssoCachePath(ssoSession)
	if _, err := os.Stat(token); err != nil {
		out.NoToken = true
	} else {
		bin, err := LoginAWSBin()
		if err == nil {
			err = revokeSSOToken(bin, token)
		}
		if err != nil {
			out.Message = err.Error()
		} else {
			out.Revoked = true
		}
	}
	for _, p := range []string{token, ssoRoleCachePath(sp.AccountID, sp.RoleName, ssoSession)} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			httpx.WriteErr(w, http.StatusInternalServerError, "remove_failed", err.Error())
			return
		}
	}
	log.Printf("aws-login: logout profile=%s revoked=%t no_token=%t relayed=%t", sp.Name, out.Revoked, out.NoToken, cloudlogin.RelayedByCP(r))
	httpx.WriteJSON(w, http.StatusOK, out)
}
