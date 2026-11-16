package agents

import (
	"context"
	"errors"
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
	// RemoveCredential removes the credential id names, as the kind's own disconnect would. Removing
	// one that is already gone is not an error.
	RemoveCredential(ctx context.Context, id string) error
}

// ErrUnknownCredential is RemoveCredential's answer to an id its kind never lists.
var ErrUnknownCredential = errors.New("no such stored credential")
