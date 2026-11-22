// deps.go — what the engine remote cluster needs from the Control Plane, on this side of the seam.
//
// The cluster (remote.go, remote_catalog.go, remote_token.go) reads another deployment's
// catalogue and adopts what it offers as rows. The rows live in main's `engineRegistry`, whose
// state (`engineRuntimeState`: controller, box, catalogue cache) cannot cross the boundary, so the
// four things this package does to that registry are named here and nothing else is reachable.
//
// An interface rather than a struct of func fields: a member left unset in a struct stays nil and
// becomes a silent no-op, while a missing interface member is a compile error (see
// internal/tenantsrv/deps.go). The implementation is control-plane/engine_wiring.go's
// `engineRemoteHost`; main also hands in the HTTP client, because the gateway's upstream client
// is shared with every other engine call and has deliberately no overall timeout.
package engine

import (
	"context"
	"net/http"
)

// RemoteHost is the registry side of borrowing. A nil RemoteHost means "no registry", and every
// entry point treats that as a no-op.
type RemoteHost interface {
	// Served reports the row this process already serves under key, if any, and a function that
	// drops THAT row's catalogue cache so the next read sees the mirror. The function is bound to
	// the instance Served saw: a row the registry swaps in afterwards must not be the one
	// invalidated.
	Served(key string) (def Def, invalidate func(), ok bool)
	// Adopt registers a role the far catalogue declares and this process does not serve yet. It
	// reports whether the role was taken on.
	Adopt(d Def) bool
	// NotifyCatalogChanged tells every running workspace that this role's catalogue moved. It
	// returns at once: the implementation does the fan-out in its own goroutine, and reads any
	// stubbable function value before starting it.
	NotifyCatalogChanged(ctx context.Context, key string)
}

// HTTPDoer is the upstream client both far-side calls go through.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}
