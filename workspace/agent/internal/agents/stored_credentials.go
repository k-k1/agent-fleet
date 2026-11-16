package agents

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/credbind"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
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
	// still be there; nil means it is proven gone.
	RemoveCredential(ctx context.Context, id, binding string, forbidden Forbidden) error
}

// Forbidden is the policy's answer at the moment of a removal: whether it allows none of routes
// for a credential whose endpoint is host ("" for a credential with no endpoint of its own). It
// judges on host as given and never reads a connection itself, so a store may call it while it
// holds the lock that connection's reader would take.
type Forbidden func(routes []string, host string) bool

// ErrUnknownCredential is RemoveCredential's answer to an id its kind never lists.
var ErrUnknownCredential = errors.New("no such stored credential")

// ErrCredentialChanged is RemoveCredential's answer when the credential is no longer the one
// confirmed: gone, replaced, being changed right now, or no longer forbidden. Nothing was removed.
var ErrCredentialChanged = errors.New("the stored credential changed since it was confirmed")

// ErrStillPresent is RemoveCredential's answer when the removal ran and the credential is still
// there (a CLI logout that failed or did nothing).
var ErrStillPresent = errors.New("the credential is still present after its removal")

// ErrAbsenceUnknown is RemoveCredential's answer when the removal ran and whether the credential
// is gone cannot be told (a status probe that failed): a failure, never a removal.
var ErrAbsenceUnknown = errors.New("whether the credential is gone cannot be told after its removal")

// LoginState is what a fresh read of a CLI login found.
type LoginState int

const (
	// LoginAbsent: proven not there.
	LoginAbsent LoginState = iota
	// LoginPresent: there, with the routes and binding read alongside.
	LoginPresent
	// LoginUnknown: the read failed (a CLI that errs or times out, a file that cannot be read).
	LoginUnknown
)

// FileLoginState is the state of a login kept in one file: absent when the file does not exist,
// unknown when it cannot be checked, present otherwise.
func FileLoginState(path string) LoginState {
	switch _, err := os.Stat(path); {
	case err == nil:
		return LoginPresent
	case errors.Is(err, fs.ErrNotExist):
		return LoginAbsent
	default:
		return LoginUnknown
	}
}

// LoginRemoval is a conditional removal of a login a CLI keeps (RemoveLogin).
type LoginRemoval struct {
	// Login is the credbind login the kind's writers begin their changes on.
	Login string
	// Current reads the credential afresh, never from a cache: whether it is there, and when it
	// is, its routes and binding.
	Current func(ctx context.Context) (routes []string, binding string, state LoginState)
	// Remove runs the kind's own removal (its CLI logout).
	Remove func(ctx context.Context) error
}

// RemoveLogin removes a CLI-kept login only if it is still the confirmed one. It holds the login
// against every Agent writer (credbind.LockLogin, which also refuses while a sign-in flow is in
// flight), reads the login afresh, checks binding and forbidden, records its own change, removes,
// and reads again to prove the login is gone. A login written by hand in a shell is outside the
// lock (ADR 0138 D13): the re-check before the removal is what keeps that window small.
func RemoveLogin(ctx context.Context, r LoginRemoval, binding string, forbidden Forbidden) error {
	lk, err := credbind.LockLogin(r.Login)
	if err != nil {
		return ErrCredentialChanged
	}
	defer lk.Unlock()
	routes, cur, state := r.Current(ctx)
	if state != LoginPresent || cur == "" || cur != binding || !forbidden(routes, "") {
		return ErrCredentialChanged
	}
	op, err := lk.Begin()
	if err != nil {
		return err
	}
	defer op.Finish()
	// The outcome is the login's absence, not the CLI's exit: a logout that exits non-zero with the
	// login gone removed it, one that exits zero with it still there did not.
	// Only a login proven absent is removed: one still there, or one whose state the read cannot
	// tell, is a failure.
	rmErr := r.Remove(ctx)
	switch _, _, after := r.Current(ctx); after {
	case LoginAbsent:
		return nil
	case LoginPresent:
		return errors.Join(ErrStillPresent, rmErr)
	default:
		return errors.Join(ErrAbsenceUnknown, rmErr)
	}
}

// JSONLoginState reads a login kept in one JSON file through read, the kind's own parser:
// absent when the file does not exist, or is valid JSON naming no credential; unknown when it
// cannot be read or is not valid JSON (a write caught halfway); present otherwise.
func JSONLoginState(path string, read func([]byte) routes.Launch) ([]string, string, LoginState) {
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, "", LoginAbsent
	case err != nil || !json.Valid(b):
		return nil, "", LoginUnknown
	}
	l := read(b)
	if l.Route == "" {
		return nil, "", LoginAbsent
	}
	return []string{l.Route}, l.Binding, LoginPresent
}
