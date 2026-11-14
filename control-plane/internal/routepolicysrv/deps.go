package routepolicysrv

import (
	"encoding/json"
	"io"
	"log"
	"net/http"

	"github.com/k-k1/agent-fleet/control-plane/internal/errref"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// CP is what this package needs from the control plane: the store and the CP's own role gates,
// which resolve the caller and answer a refusal themselves. The implementation is the adapter in
// control-plane/tenant_wiring.go. An interface rather than function fields, as internal/tenantsrv
// explains: a missing member is a compile error.
type CP interface {
	Store() store.Store
	// SuperAdminFor resolves the caller and requires super_admin, answering the refusal itself.
	SuperAdminFor(w http.ResponseWriter, r *http.Request) (store.Identity, bool)
	// TenantAdminFor resolves the caller and the tenant by slug and requires an administrator of
	// it, answering the refusal itself.
	TenantAdminFor(w http.ResponseWriter, r *http.Request, slug string) (store.Identity, store.Tenant, bool)
}

// withSuperAdmin gates a handler on super_admin.
func (a admin) withSuperAdmin(h func(http.ResponseWriter, *http.Request, store.Identity)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if ident, ok := a.cp.SuperAdminFor(w, r); ok {
			h(w, r, ident)
		}
	}
}

// APIError carries an HTTP status and machine code, mirroring the CP's apiError (manager.go) as
// internal/tenantsrv's does; the response shape is the contract.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func internalErr(err error) *APIError {
	return &APIError{Status: http.StatusInternalServerError, Code: "internal", Message: errref.Message(errref.Log(err))}
}

// writeJSON and writeAPIErr mirror httpapi.go, as internal/tenantsrv and internal/mcpsrv do.
func writeJSON(w http.ResponseWriter, status int, v any) {
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeAPIErr(w http.ResponseWriter, e *APIError) {
	// A 5xx body may carry only a fixed message; the log keeps the status and code so the
	// operator sees what was answered (internalErr logs the cause under the same ref).
	if e.Status >= 500 {
		log.Printf("api error %d %s: %q", e.Status, e.Code, e.Message)
	}
	writeJSON(w, e.Status, errorBody{Error: errorDetail{Code: e.Code, Message: e.Message}})
}

// errorBody is the CP's error envelope, {"error":{"code":…,"message":…}}.
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// decodeBody reads one JSON object with no unknown fields and nothing after it, as the CP's
// decodeAdminBody does.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil || dec.More() {
		msg := "invalid body: more than one JSON value"
		if err != nil {
			msg = "invalid body: " + err.Error()
		}
		writeAPIErr(w, &APIError{http.StatusBadRequest, "bad_request", msg})
		return false
	}
	return true
}
