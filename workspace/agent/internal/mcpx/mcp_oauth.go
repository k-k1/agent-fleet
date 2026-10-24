package mcpx

// The Agent half of sign-in for registered MCP servers (ADR 0119 decisions 3-5). These routes
// are called by the Control Plane, which owns the public callback and the CSRF state; the
// Console reaches only the status, disconnect and client-id routes, through the Control
// Plane's allowlist. Nothing here returns a token, a code or a verifier.
//
// A path added here also needs registering in control-plane/routes.go (the CP uses an explicit
// allowlist) unless it is CP-internal; start and callback are not proxied: the CP handlers
// call them after binding the request to a member.

import (
	"errors"
	"net/http"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
)

// writeOAuthErr writes the shared error envelope, plus the issuer and registration path the
// refusal concerns when they are known (the Console needs the issuer to ask for a client id).
func writeOAuthErr(w http.ResponseWriter, err error) {
	var oe *mcpreg.OAuthError
	if errors.As(err, &oe) {
		var body oauthErrWire
		body.Error.Code, body.Error.Message = oe.Code, oe.Message
		body.Error.Issuer, body.Error.Registration = oe.Issuer, oe.Registration
		httpx.WriteJSON(w, oe.Status, body)
		return
	}
	writeMCPErr(w, err)
}

// oauthErrWire is httpx.WriteErr's envelope with the two optional fields.
type oauthErrWire struct {
	Error struct {
		Code         string `json:"code"`
		Message      string `json:"message"`
		Issuer       string `json:"issuer,omitempty"`
		Registration string `json:"registration,omitempty"`
	} `json:"error"`
}

// HandleOAuthStart (POST /mcp-servers/{id}/oauth/start) returns the authorization URL.
func HandleOAuthStart(w http.ResponseWriter, r *http.Request) {
	var in mcpreg.StartRequest
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	res, err := mcpreg.StartOAuth(r.Context(), r.PathValue("id"), in)
	if err != nil {
		writeOAuthErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

// HandleOAuthCallback (POST /mcp-servers/{id}/oauth/callback) finishes the sign-in with what
// the browser carried to the Control Plane's callback.
func HandleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	var in mcpreg.CompleteRequest
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	res, err := mcpreg.CompleteOAuth(r.Context(), r.PathValue("id"), in)
	if err != nil {
		writeOAuthErr(w, err)
		return
	}
	MaterializeAll() // a live grant is what puts the server into every kind's config
	httpx.WriteJSON(w, http.StatusOK, res)
}

// HandleOAuthStatus (GET /mcp-servers/{id}/oauth).
func HandleOAuthStatus(w http.ResponseWriter, r *http.Request) {
	if _, err := mcpreg.Get(r.PathValue("id")); err != nil {
		writeMCPErr(w, err)
		return
	}
	st, err := mcpreg.OAuthStatusOf(r.PathValue("id"))
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", "the secret store could not be read")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, st)
}

// HandleOAuthDisconnect (DELETE /mcp-servers/{id}/oauth) revokes at the authorization server
// when it offers revocation and deletes the grant locally either way.
func HandleOAuthDisconnect(w http.ResponseWriter, r *http.Request) {
	if _, err := mcpreg.Get(r.PathValue("id")); err != nil {
		writeMCPErr(w, err)
		return
	}
	res, err := mcpreg.DisconnectOAuth(r.Context(), r.PathValue("id"))
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", "the secret store could not be written")
		return
	}
	MaterializeAll() // and its going takes the server out of them again
	httpx.WriteJSON(w, http.StatusOK, res)
}

type oauthClientReq struct {
	Issuer       string `json:"issuer"`
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret,omitempty"`
}

// HandleOAuthClient (PUT /mcp-servers/{id}/oauth/client) stores the client id (and secret) the
// member registered by hand at one authorization server; an empty clientId removes it.
func HandleOAuthClient(w http.ResponseWriter, r *http.Request) {
	var in oauthClientReq
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	if err := mcpreg.SetPreregisteredClient(r.PathValue("id"), in.Issuer, in.ClientID, in.ClientSecret); err != nil {
		if errors.Is(err, mcpreg.ErrNotFound) {
			writeMCPErr(w, err)
			return
		}
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	HandleOAuthStatus(w, r)
}
