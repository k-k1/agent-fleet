package awsx

import (
	"errors"
	"os"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/ssm"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func init() {
	ssm.VetLogin = vetSSMLogin
	ssm.ForgetLogin = forgetSSMLogin
}

// ErrSSMSettingsChanged refuses an SSM pane or discovery whose profile was rendered for another
// sign-in, owner or portal than Settings gives its key now: the CLI would read a token that is
// not its.
var ErrSSMSettingsChanged = errors.New("this SSM profile changed in Settings since the session was created; open a new session")

// ssmLoginProfile is the Settings profile whose login an SSM meta uses: its source for a chain.
func ssmLoginProfile(settings map[string]Profile, s session.SSMMeta) (Profile, bool) {
	name := s.Profile
	if s.RoleARN != "" {
		name = s.SourceProfile
	}
	p, ok := settings[name]
	return p, ok && !p.Chained()
}

// metaOwner is the token owner an SSM meta was rendered for, "" from a Control Plane that sends
// none.
func metaOwner(s session.SSMMeta) string {
	switch {
	case s.SSOOwnerID == "":
		return ""
	case s.SSOSession != "":
		return "sign-in:" + s.SSOOwnerID
	}
	return "profile:" + s.SSOOwnerID
}

// checkSSMMeta compares the key an SSM meta renders (ssm.SSOSessionName) with what Settings
// gives that key now, and returns the key and whether Agent Fleet uses it (only then may its
// cache be vetted). Two questions are kept apart: whom the meta and the key point at is checked
// against every Settings profile, usable or not, so a known owner or portal mismatch is refused
// even while the key is held back; whether the cache may be changed is the usable set's. A meta
// from a Control Plane that sends no owner must render the key its profile renders now, which an
// old wire's af-<profile> does only while the profile is in no sign-in. A key no Settings profile
// renders is the member's, but one whose binding names another portal is still refused.
func checkSSMMeta(settings map[string]Profile, usable []string, s session.SSMMeta) (string, bool, error) {
	key := ssm.SSOSessionName(s)
	url := strings.TrimRight(s.StartURL, "/")
	p, inSettings := ssmLoginProfile(settings, s)
	if inSettings && p.SSOSession() != key {
		// Rendered before the profile joined, left or switched a sign-in (or by a Control Plane
		// that sends no sign-in): the pane would read another login than Settings gives it.
		return key, false, ErrSSMSettingsChanged
	}
	owner, owned := settingsOwner(settings, key)
	if !owned {
		var got keyBinding
		if readJSON(bindingPath(key), &got) && (got.StartURL != url || got.SSORegion != s.SSORegion) {
			return key, false, ErrSSMSettingsChanged
		}
		return key, false, nil
	}
	if !inSettings || owner.StartURL != url || owner.SSORegion != s.SSORegion {
		return key, false, ErrSSMSettingsChanged
	}
	if o := metaOwner(s); o != "" && o != owner.Owner {
		return key, false, ErrSSMSettingsChanged
	}
	_, ours := keyOwner(settings, usable, key)
	if !ours && heldBackElsewhere(key, owner) {
		return key, false, ErrSSMKeyHeldBack
	}
	return key, ours, nil
}

// ErrSSMKeyHeldBack refuses an SSM pane, discovery or forced re-login whose key Settings owns but
// does not use now (the member's own files define it, or a collision holds it back), when what
// the key holds is for another portal or owner: the pane's config would read that token.
var ErrSSMKeyHeldBack = errors.New("your own ~/.aws files use this profile's sso-session for another sign-in; " +
	"rename it there, or open the profile from your own config")

// heldBackElsewhere reports whether key, which Settings gives owner but does not use now, holds
// or is defined for something else: a token or a binding for another portal or owner, or the
// member's own [sso-session] section for another portal (a config that cannot be read counts).
// It only reads: a held-back key's cache is the member's.
func heldBackElsewhere(key string, owner keyBinding) bool {
	if url, region, present := tokenPortal(key); present && (url != owner.StartURL || region != owner.SSORegion) {
		return true
	}
	var got keyBinding
	if readJSON(bindingPath(key), &got) && !got.sameTarget(owner) {
		return true
	}
	keys := map[string]string{}
	if err := readINISection(ConfigPath(), configPicker("sso-session", key), keys); err != nil {
		return true
	}
	if u, ok := keys["sso_start_url"]; ok && strings.TrimRight(u, "/") != owner.StartURL {
		return true
	}
	if r, ok := keys["sso_region"]; ok && r != owner.SSORegion {
		return true
	}
	return false
}

// vetSSMLogin vets the key the pane or discovery is about to read, refusing a meta Settings no
// longer matches and a key that could not be retired.
func vetSSMLogin(s session.SSMMeta) error {
	settings, usable := loginSettings(), UsableNow()
	key, ours, err := checkSSMMeta(settings, usable, s)
	if err != nil || !ours {
		return err
	}
	return vetKey(settings, usable, key)
}

// forgetSSMLogin drops the login an SSM pane is about to log in to again, the key the pane
// renders: the token and the role credentials of every member sharing it, and a chain's assumed
// role, within one section under the login's exclusive lock and the attempts' gate. With a
// shared sign-in this signs every member out; the Console says so before the press.
func forgetSSMLogin(s session.SSMMeta) error {
	settings, usable := loginSettings(), UsableNow()
	session, ours, err := checkSSMMeta(settings, usable, s)
	if err != nil {
		return err
	}
	if _, owned := settingsOwner(settings, session); owned && !ours {
		// Settings owns the key but does not use it now: what it holds is the member's, and a
		// forced re-login must not delete it.
		return ErrSSMKeyHeldBack
	}
	gate := logins.Gate(session)
	gate.Lock()
	defer gate.Unlock()
	endLoginFor(session)
	unlock, err := logins.LockKey(session, false)
	if err != nil {
		return err
	}
	defer unlock()
	if err := os.Remove(ssoCachePath(session)); err != nil && !os.IsNotExist(err) {
		return err
	}
	var got keyBinding
	readJSON(bindingPath(session), &got)
	roles := addRole(got.Roles, s.AccountID, s.RoleName)
	for _, m := range sessionMembers(settings, session) {
		for _, path := range roleCachePaths(m, session) {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	for _, r := range roles {
		if err := os.Remove(ssoRoleCachePath(r[0], r[1], session)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if s.RoleARN != "" {
		if acct, role, ok := roleARNParts(s.RoleARN); ok {
			if err := forgetAssumedRole(acct, role); err != nil {
				return err
			}
		}
	}
	if err := os.Remove(bindingPath(session)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
