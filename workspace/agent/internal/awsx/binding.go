package awsx

import (
	"crypto/sha1"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// A token key is bound to its owner and its portal (ADR 0125 decision 10). Neither of the
// CLI's caches is checked against the config: botocore takes whatever token sits under
// sha1(sso-session name), and the role credentials under sha1(account, role, session). A key
// that changes hands (a sign-in renamed, a portal changed, a name freed and taken by another
// owner) would be used as it is. So the Agent records, per key, which Settings owner and which
// portal its token was written for, and accepts a key only when both still hold. A key that
// fails is retired: its token is moved aside and the members' role credentials are deleted, so
// no reader finds them, the SSM pane and a plain aws included.

// keyBinding is what one sso-session key's token was written for.
type keyBinding struct {
	Session   string `json:"session"`
	Owner     string `json:"owner"`
	StartURL  string `json:"startUrl"`
	SSORegion string `json:"ssoRegion"`
}

func bindingPath(session string) string {
	sum := sha1.Sum([]byte(session))
	return filepath.Join(paths.AgentStateDir(), "aws-sso-bindings", hex.EncodeToString(sum[:])+".json")
}

// keyOwner is who and which portal Settings gives session now, from its first SSO member;
// ok is false when no Settings SSO profile logs in through session.
func keyOwner(settings map[string]Profile, session string) (keyBinding, bool) {
	ms := sessionMembers(settings, session)
	if len(ms) == 0 {
		return keyBinding{}, false
	}
	p := ms[0]
	return keyBinding{Session: session, Owner: p.TokenOwner(), StartURL: strings.TrimRight(p.StartURL, "/"), SSORegion: p.SSORegion}, true
}

// tokenPortal is the start URL and region the cached token of session says it is for.
func tokenPortal(session string) (url, region string, present bool) {
	var doc struct {
		StartURL string `json:"startUrl"`
		Region   string `json:"region"`
	}
	if _, err := os.Stat(ssoCachePath(session)); err != nil {
		return "", "", false
	}
	readJSON(ssoCachePath(session), &doc)
	return strings.TrimRight(doc.StartURL, "/"), doc.Region, true
}

// verdict is what vetting a key found.
type verdict int

const (
	keyNoToken  verdict = iota // nothing cached: nothing to accept or retire
	keyUsable                  // bound to the current owner and portal
	keyAdopt                   // unbound, and the token is for the current owner's portal
	keyHandOver                // bound to a profile that is now a member of the sign-in owning the key
	keyUnusable                // another owner, another portal, or a token for another portal
)

// judgeKey decides session's key against Settings, reading only.
func judgeKey(settings map[string]Profile, session string) (verdict, keyBinding) {
	want, ok := keyOwner(settings, session)
	url, region, present := tokenPortal(session)
	if !present {
		return keyNoToken, want
	}
	if !ok || url != want.StartURL || region != want.SSORegion {
		return keyUnusable, want
	}
	var got keyBinding
	if !readJSON(bindingPath(session), &got) {
		return keyAdopt, want
	}
	if got.StartURL != want.StartURL || got.SSORegion != want.SSORegion {
		return keyUnusable, want
	}
	if got.Owner == want.Owner {
		return keyUsable, want
	}
	// The one legitimate hand-over: profile P becomes a member of sign-in S, which took P's name,
	// so S renders the key P's token sits under (decision D).
	if pid, isProfile := strings.CutPrefix(got.Owner, "profile:"); isProfile && strings.HasPrefix(want.Owner, "sign-in:") {
		for _, m := range sessionMembers(settings, session) {
			if m.ID == pid && m.TokenOwner() == want.Owner {
				return keyHandOver, want
			}
		}
	}
	return keyUnusable, want
}

// vetKeyHook lets a test watch the key's lock being held.
var vetKeyHook = func() {}

// vetKey accepts or retires session's key under its exclusive lock, and reports whether its
// token may be used. Adopting an unbound token and the hand-over write the binding. It runs
// after every Settings sync and before every use Agent Fleet starts (an export, a login
// attempt's landing, the SSM pane and the instance list).
func vetKey(settings map[string]Profile, session string) bool {
	unlock, err := logins.LockKey(session, false)
	if err != nil {
		return false
	}
	defer unlock()
	vetKeyHook()
	v, want := judgeKey(settings, session)
	switch v {
	case keyNoToken, keyUsable:
		return true
	case keyAdopt, keyHandOver:
		return writeJSONAtomic(bindingPath(session), want) == nil
	}
	retireKey(settings, session)
	return false
}

// retireKey moves the token aside (kept for a later revoke) and deletes the role credentials
// of the key's current members. The caller holds the key's exclusive lock.
func retireKey(settings map[string]Profile, session string) {
	path := ssoCachePath(session)
	_ = os.Rename(path, path+".af-retired")
	for _, m := range sessionMembers(settings, session) {
		for _, p := range roleCachePaths(m, session) {
			_ = os.Remove(p)
		}
	}
	_ = os.Remove(bindingPath(session))
}

// keyLoginState is profileLoginState for sp's key, as decision 10 reads it: a key that vetting
// would retire is "none". A read moves no file.
func keyLoginState(sp Profile, settings map[string]Profile, now time.Time) string {
	if v, _ := judgeKey(settings, sp.SSOSession()); v == keyUnusable {
		return loginStateNone
	}
	return profileLoginState(sp.SSOSession(), now)
}

// VetKeyOf vets the key the profile name logs in through (its source's for a chain); for
// af-aws-exec right before it fetches credentials. A name Settings does not have is the
// member's own profile, which this does not judge.
func VetKeyOf(settings map[string]Profile, name string) bool {
	p, ok := settings[name]
	if !ok {
		return true
	}
	if p.Chained() {
		if p, ok = settings[p.SourceProfile]; !ok {
			return true
		}
	}
	return vetKey(settings, p.SSOSession())
}

// vetAll vets every key Settings renders, after a sync: a portal change or a rename is acted on
// within one poll even when nothing runs. A login attempt whose target changed is ended.
func vetAll(settings map[string]Profile) {
	done := map[string]bool{}
	for _, p := range settings {
		if p.Chained() || done[p.SSOSession()] {
			continue
		}
		done[p.SSOSession()] = true
		vetKey(settings, p.SSOSession())
		if t, ok := attemptTargetOf(p.SSOSession()); ok {
			if want, ok := keyOwner(settings, p.SSOSession()); !ok || want != t {
				endLoginFor(p.SSOSession())
			}
		}
	}
}

// attemptTargets records, per running login attempt's key, the owner and portal it started
// for: its landing binds only that, and a sync that sees them change ends it.
var attemptTargets sync.Map // session -> keyBinding

func attemptTargetOf(session string) (keyBinding, bool) {
	v, ok := attemptTargets.Load(session)
	if !ok {
		return keyBinding{}, false
	}
	return v.(keyBinding), true
}

// bindLanding binds session's freshly written token to target, if Settings still names the
// same owner and portal and the token is for that portal. It reports whether it did.
func bindLanding(session string, target keyBinding) bool {
	unlock, err := logins.LockKey(session, false)
	if err != nil {
		return false
	}
	defer unlock()
	want, ok := keyOwner(loginSettings(), session)
	url, region, present := tokenPortal(session)
	if !ok || want != target || !present || url != target.StartURL || region != target.SSORegion {
		return false
	}
	return writeJSONAtomic(bindingPath(session), target) == nil
}

// forgetLeftovers revokes and deletes the per-profile tokens the members of session kept from
// before they joined it under another name (ADR 0125 decision C), but only a key provably left
// by that member: no current Settings profile or sign-in renders it, the member's own files
// define no sso-session of that name, and its binding names that member's profile. Anything
// else is left alone: where it came from cannot be shown.
func forgetLeftovers(settings map[string]Profile, members []Profile, session string) {
	rendered := map[string]bool{}
	for _, p := range settings {
		if !p.Chained() {
			rendered[p.SSOSession()] = true
		}
	}
	b, _ := os.ReadFile(ConfigPath())
	user, err := stripBlock(string(b))
	if err != nil {
		return
	}
	_, own := configNames(user)
	for _, m := range members {
		key := "af-" + m.Name
		if key == session || rendered[key] || own[key] || m.ID == "" {
			continue
		}
		forgetLeftover(m, key)
	}
}

func forgetLeftover(m Profile, key string) {
	unlock, err := logins.LockKey(key, false)
	if err != nil {
		return
	}
	defer unlock()
	var got keyBinding
	if !readJSON(bindingPath(key), &got) || got.Owner != "profile:"+m.ID {
		return
	}
	for _, path := range []string{ssoCachePath(key), ssoCachePath(key) + ".af-retired"} {
		snap, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		_ = os.Remove(path)
		_, _ = revokeSSOToken(snap, m.SSORegion)
	}
	for _, p := range roleCachePaths(m, key) {
		_ = os.Remove(p)
	}
	_ = os.Remove(bindingPath(key))
}
