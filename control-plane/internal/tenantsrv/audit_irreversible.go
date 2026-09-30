package tenantsrv

import (
	"net/http"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// beginIrreversible records the request for an irreversible admin action before it runs
// (store.BeginIrreversible), and refuses with 503 audit_unavailable when that record cannot
// be written. Refusing is the choice for every action here: each one is an administrator's
// deliberate click that can simply be retried, and an erase nobody can attribute is the one
// outcome the audit log exists to prevent.
func (a Admin) beginIrreversible(w http.ResponseWriter, r *http.Request, e store.AuditLog) (*store.AuditIntent, bool) {
	in, err := store.BeginIrreversible(r.Context(), a.cp.Store(), e)
	if err != nil {
		writeAPIErr(w, &APIError{http.StatusServiceUnavailable, "audit_unavailable",
			"nothing was done: " + err.Error()})
		return nil, false
	}
	return in, true
}

// refuseIrreversible answers e and records it as the outcome of in, so a request that was
// refused or failed is not left looking like one whose outcome was lost. The detail does not
// claim "nothing happened": an internal error can come from halfway through the action.
func refuseIrreversible(w http.ResponseWriter, r *http.Request, in *store.AuditIntent, e *APIError) {
	in.Done(r.Context(), "error "+e.Code+": "+e.Message, e.Status)
	writeAPIErr(w, e)
}
