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
// gives that key now, and returns the key. A key no usable Settings profile renders is not
// Agent Fleet's to judge, but one whose binding names another portal is still refused. A meta
// from a Control Plane that sends no owner must render the key its profile renders now, which
// an old wire's af-<profile> does only while the profile is in no sign-in.
func checkSSMMeta(settings map[string]Profile, usable []string, s session.SSMMeta) (string, bool, error) {
	key := ssm.SSOSessionName(s)
	url := strings.TrimRight(s.StartURL, "/")
	p, inSettings := ssmLoginProfile(settings, s)
	if inSettings && p.SSOSession() != key {
		// Rendered before the profile joined, left or switched a sign-in (or by a Control Plane
		// that sends no sign-in): the pane would read another login than Settings gives it.
		return key, false, ErrSSMSettingsChanged
	}
	want, ours := keyOwner(settings, usable, key)
	if !ours {
		var got keyBinding
		if readJSON(bindingPath(key), &got) && (got.StartURL != url || got.SSORegion != s.SSORegion) {
			return key, false, ErrSSMSettingsChanged
		}
		return key, false, nil
	}
	if !inSettings || want.StartURL != url || want.SSORegion != s.SSORegion {
		return key, true, ErrSSMSettingsChanged
	}
	if o := metaOwner(s); o != "" && o != want.Owner {
		return key, true, ErrSSMSettingsChanged
	}
	return key, true, nil
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
	session, _, err := checkSSMMeta(settings, usable, s)
	if err != nil {
		return err
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
