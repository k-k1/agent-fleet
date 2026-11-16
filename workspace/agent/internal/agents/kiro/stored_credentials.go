package kiro

import (
	"context"
	"errors"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/credbind"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"io/fs"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

var _ agents.CredentialStore = agentImpl{}

// StoredCredentials and RemoveCredential implement agents.CredentialStore (ADR 0138 D14a).
// kiro owns one login. LoggedIn warms the status probe Login's binding is read from.
func (agentImpl) StoredCredentials(context.Context) []agents.StoredCredential {
	if !LoggedIn() {
		return nil
	}
	l := Login()
	return []agents.StoredCredential{{ID: "login", Routes: []string{l.Route}, Binding: l.Binding}}
}

func (agentImpl) RemoveCredential(ctx context.Context, id, binding string, forbidden agents.Forbidden) error {
	if id != "login" {
		return agents.ErrUnknownCredential
	}
	return agents.RemoveLogin(ctx, agents.LoginRemoval{
		Login: routes.RouteKiroLogin,
		// The status probe's own run, not its cache: the login must be read as it is now.
		Current: func(context.Context) ([]string, string, agents.LoginState) {
			return currentLogin()
		},
		Remove: clearLogin,
	}, binding, forbidden)
}

// currentLogin reads kiro's login afresh. whoami exits non-zero both when signed out and when it
// fails, so its failure proves nothing: the login's rows in kiro's database decide. No token row
// (or no database) is absent; rows that cannot be read are unknown; a token row with whoami
// failing is unknown too, since its account cannot be told.
func currentLogin() ([]string, string, agents.LoginState) {
	if st, ok := freshWhoami(); ok && st.loggedIn {
		return []string{routes.RouteKiroLogin}, credbind.AccountOrGeneration(routes.RouteKiroLogin, st.email), agents.LoginPresent
	}
	switch hasToken, err := tokenRowPresent(); {
	case err != nil:
		return nil, "", agents.LoginUnknown
	case hasToken:
		return nil, "", agents.LoginUnknown
	}
	return nil, "", agents.LoginAbsent
}

// freshWhoami is the status probe's own run, never its cache. A var for tests.
var freshWhoami = func() (statusResult, bool) { return statusProbe.Probe() }

// tokenRowPresent reports whether kiro's database holds its sign-in token row. A var for tests.
var tokenRowPresent = func() (bool, error) {
	rows, err := readAuthRows(dataDB(paths.HomeDir()))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, r := range rows {
		if r.key == tokenRowKey {
			return true, nil
		}
	}
	return false, nil
}

// tokenRowKey is the auth_kv row a kiro sign-in writes its token under (stage.go).
const tokenRowKey = "kirocli:odic:token"
