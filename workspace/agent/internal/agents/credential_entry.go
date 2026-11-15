package agents

import (
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/routepolicy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
)

// Credential entry under the credential policy (ADR 0138 D12 "Credential entry", D14): the Agent
// refuses to start a login, or store a key, whose routes the policy all forbids, and cancels a
// login flow already running when a tighter policy forbids its route. A credential a flow still
// wrote is kept, unusable (D14a): the launch and utility points refuse its route. Removing a
// credential is never refused.

// CredentialEntryRefusal is the refusal of adding a credential that opens routes: nil while the
// policy restricts nothing, or allows at least one of them (a key that opens two routes is
// usable on the one allowed). While the policy cannot be confirmed, nothing new is added.
func CredentialEntryRefusal(opened ...string) *RouteRefusal {
	return entryRefusal(func(p routepolicy.Policy, r string) bool { return p.Allows(r) }, opened)
}

// CredentialEntryRefusalAt is CredentialEntryRefusal for a connection to host about to be stored
// (a member endpoint): a per-connection route is judged on that host, not on the connection it
// replaces (ADR 0138 §2).
func CredentialEntryRefusalAt(host string, opened ...string) *RouteRefusal {
	return entryRefusal(func(p routepolicy.Policy, r string) bool { return p.AllowsAt(r, host) }, opened)
}

func entryRefusal(allows func(routepolicy.Policy, string) bool, opened []string) *RouteRefusal {
	if err := routepolicy.Active().Gate(); err != nil {
		return &RouteRefusal{Status: http.StatusServiceUnavailable, Code: routes.CodePolicyUnconfirmed, Message: err.Error()}
	}
	p, restricted := routepolicy.Hiding()
	if !restricted || len(opened) == 0 || slices.ContainsFunc(opened, func(r string) bool { return allows(p, r) }) {
		return nil
	}
	return &RouteRefusal{Status: http.StatusForbidden, Code: routes.CodePolicyForbidden,
		Message: "the credential policy does not allow " + strings.Join(opened, ", ") + ", so this credential cannot be added"}
}

// guardedLogins are the flow stores whose flows open a route, registered by GuardLogins.
var (
	guardedMu     sync.Mutex
	guardedLogins []guardedStore
)

type guardedStore struct {
	route string
	store *FlowStore
}

// GuardLogins registers s as holding login flows that open route: when a policy that forbids the
// route is taken, every flow in s is closed (the login process killed, its staging discarded)
// and the member has to start again once it is allowed. It returns s, for a declaration.
func (s *FlowStore) GuardLogins(route string) *FlowStore {
	guardedMu.Lock()
	defer guardedMu.Unlock()
	guardedLogins = append(guardedLogins, guardedStore{route: route, store: s})
	return s
}

// GuardedLoginRoutes are the routes of every registered login flow store, for a test that every
// harness's login is guarded.
func GuardedLoginRoutes() []string {
	guardedMu.Lock()
	defer guardedMu.Unlock()
	out := make([]string, 0, len(guardedLogins))
	for _, g := range guardedLogins {
		out = append(out, g.route)
	}
	return out
}

// closeForbiddenLogins is the policy listener: it closes the flows of every guarded store whose
// route p forbids. A policy that restricts nothing closes nothing.
func closeForbiddenLogins(p routepolicy.Policy) {
	if !p.Restricted {
		return
	}
	guardedMu.Lock()
	stores := append([]guardedStore{}, guardedLogins...)
	guardedMu.Unlock()
	for _, g := range stores {
		if !p.Allows(g.route) {
			g.store.CloseAll()
		}
	}
}

func init() { routepolicy.OnChange(closeForbiddenLogins) }

// CloseAll closes and drops every flow in s. Closing runs outside the store's lock: it waits for
// the process to be reaped.
func (s *FlowStore) CloseAll() {
	s.mu.Lock()
	fs := make([]*Flow, 0, len(s.flows))
	for id, f := range s.flows {
		fs = append(fs, f)
		delete(s.flows, id)
	}
	s.mu.Unlock()
	for _, f := range fs {
		f.Close()
	}
}
