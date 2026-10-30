package awsx

import (
	"os"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/ssm"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func init() {
	ssm.VetLogin = vetSSMLogin
	ssm.ForgetLogin = forgetSSMLogin
}

// ssmLoginProfile is the Settings profile whose login an SSM meta uses: its source for a chain.
func ssmLoginProfile(settings map[string]Profile, s session.SSMMeta) (Profile, bool) {
	name := s.Profile
	if s.RoleARN != "" {
		name = s.SourceProfile
	}
	p, ok := settings[name]
	return p, ok && !p.Chained()
}

func vetSSMLogin(s session.SSMMeta) {
	settings := loginSettings()
	if p, ok := ssmLoginProfile(settings, s); ok {
		vetKey(settings, p.SSOSession())
	}
}

// forgetSSMLogin drops the login an SSM pane is about to log in to again: the token and the
// role credentials of every member sharing it, and a chain's assumed role, within one section
// under the login's exclusive lock and the attempts' gate. With a shared sign-in this signs every
// member out; the Console says so before the press.
func forgetSSMLogin(s session.SSMMeta) error {
	settings := loginSettings()
	p, ok := ssmLoginProfile(settings, s)
	session := ssm.SSOSessionName(s)
	if ok {
		session = p.SSOSession()
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
	for _, m := range sessionMembers(settings, session) {
		for _, path := range roleCachePaths(m, session) {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	if !ok {
		// Not a Settings profile now: its own key's role credentials, as the shell step did.
		if s.AccountID != "" && s.RoleName != "" {
			_ = os.Remove(ssoRoleCachePath(s.AccountID, s.RoleName, session))
		}
	}
	if s.RoleARN != "" {
		if acct, role, ok := roleARNParts(s.RoleARN); ok {
			if err := forgetAssumedRole(acct, role); err != nil {
				return err
			}
		}
	}
	_ = os.Remove(bindingPath(session))
	return nil
}
