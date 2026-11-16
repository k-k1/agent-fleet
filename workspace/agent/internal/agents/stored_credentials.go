package agents

import (
	"context"
	"errors"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/credbind"
)

// StoredCredential is one model credential the member keeps in this Workspace, as the removal of
// credentials the policy forbids sees it (ADR 0138 D14a). It carries no secret.
type StoredCredential struct {
	// ID names the credential within its kind ("login", "key:OPENAI_API_KEY"). Stable while the
	// credential exists; RemoveCredential takes it back.
	ID string
	// Routes are every route the credential can serve. It is unusable under a policy that allows
	// none of them.
	Routes []string
	// Binding is the credential's non-secret binding (ADR 0138 D5b), what a removal confirmed
	// earlier is checked against. "" means it cannot be told now, and such a credential is never
	// removed: nothing proves it is the one the member confirmed.
	Binding string
}

// CredentialStore is a kind whose member-owned, model-only credentials can be listed and removed
// (ADR 0138 D14a). It lists only what the member stored through Agent Fleet or a CLI login file
// it reads: a credential in the Agent's own environment cannot be removed and is not listed, and
// neither is a connection with other uses (the GitHub connection copilot rides on) or a source the
// Control Plane or an administrator owns (provided routes, the engine).
type CredentialStore interface {
	StoredCredentials(ctx context.Context) []StoredCredential
	// RemoveCredential removes the credential id names only if, read afresh at the moment of the
	// removal and under the same lock as the store's own writers, its binding is binding and
	// forbidden answers true for its routes. Otherwise it removes nothing and returns
	// ErrCredentialChanged. Any other error means the removal was tried and the credential may
	// still be there; nil means it is gone.
	RemoveCredential(ctx context.Context, id, binding string, forbidden func(routes []string) bool) error
}

// ErrUnknownCredential is RemoveCredential's answer to an id its kind never lists.
var ErrUnknownCredential = errors.New("no such stored credential")

// ErrCredentialChanged is RemoveCredential's answer when the credential is no longer the one
// confirmed: gone, replaced, being changed right now, or no longer forbidden. Nothing was removed.
var ErrCredentialChanged = errors.New("the stored credential changed since it was confirmed")

// ErrStillPresent is RemoveCredential's answer when the removal ran and the credential is still
// there (a CLI logout that failed or did nothing).
var ErrStillPresent = errors.New("the credential is still present after its removal")

// LoginRemoval is a conditional removal of a login a CLI keeps (RemoveLogin).
type LoginRemoval struct {
	// Login is the credbind login the kind's writers begin their changes on.
	Login string
	// Current reads the credential afresh, never from a cache: its routes and binding, and
	// present false when there is none.
	Current func(ctx context.Context) (routes []string, binding string, present bool)
	// Remove runs the kind's own removal (its CLI logout).
	Remove func(ctx context.Context) error
}

// RemoveLogin removes a CLI-kept login only if it is still the confirmed one. It holds the login
// against every Agent writer (credbind.LockLogin, which also refuses while a sign-in flow is in
// flight), reads the login afresh, checks binding and forbidden, records its own change, removes,
// and reads again to prove the login is gone. A login written by hand in a shell is outside the
// lock (ADR 0138 D13): the re-check before the removal is what keeps that window small.
func RemoveLogin(ctx context.Context, r LoginRemoval, binding string, forbidden func([]string) bool) error {
	lk, err := credbind.LockLogin(r.Login)
	if err != nil {
		return ErrCredentialChanged
	}
	defer lk.Unlock()
	routes, cur, present := r.Current(ctx)
	if !present || cur == "" || cur != binding || !forbidden(routes) {
		return ErrCredentialChanged
	}
	op, err := lk.Begin()
	if err != nil {
		return err
	}
	defer op.Finish()
	// The outcome is the login's absence, not the CLI's exit: a logout that exits non-zero with the
	// login gone removed it, one that exits zero with it still there did not.
	rmErr := r.Remove(ctx)
	if _, _, still := r.Current(ctx); still {
		return errors.Join(ErrStillPresent, rmErr)
	}
	return nil
}
