package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// readAllBody reads and closes a request body. restoreBody re-attaches a body so a
// downstream handler (the proxy) can read it again after we peeked/rewrote it.
func readAllBody(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	return io.ReadAll(r.Body)
}

func restoreBody(r *http.Request, b []byte) {
	r.Body = io.NopCloser(bytes.NewReader(b))
	r.ContentLength = int64(len(b))
	r.Header.Set("Content-Length", strconv.Itoa(len(b)))
}

// SSM login (docs/history/p3-ssm-session.md). A member pre-registers SSO sessions and
// SSM host bookmarks (personal scope), then opens a kind=ssm session that runs
// `aws sso login` (device-code URL surfaced in the terminal) + `aws ssm start-session`
// inside their workspace container. NO AWS secrets pass through the Control Plane: the
// in-container aws CLI authenticates directly and caches the token in the home volume.

var httpsURLRe = regexp.MustCompile(`^https://[^\s]+$`)

// ssoSessionDTO / ssmHostDTO are the JSON wire shapes (no secrets).
type ssoSessionDTO struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	StartURL  string `json:"startUrl"`
	SSORegion string `json:"ssoRegion"`
	CreatedAt string `json:"createdAt"`
}

type ssmHostDTO struct {
	ID           string `json:"id"`
	Alias        string `json:"alias"`
	SSOSessionID string `json:"ssoSessionId"`
	AccountID    string `json:"accountId"`
	RoleName     string `json:"roleName"`
	Region       string `json:"region"`
	InstanceID   string `json:"instanceId"`
	DocumentName string `json:"documentName"`
	CreatedAt    string `json:"createdAt"`
}

func ssoToDTO(v SSOSession) ssoSessionDTO {
	return ssoSessionDTO{ID: v.ID, Label: v.Label, StartURL: v.StartURL, SSORegion: v.SSORegion, CreatedAt: v.CreatedAt}
}

func hostToDTO(h SSMHost) ssmHostDTO {
	return ssmHostDTO{ID: h.ID, Alias: h.Alias, SSOSessionID: h.SSOSessionID, AccountID: h.AccountID,
		RoleName: h.RoleName, Region: h.Region, InstanceID: h.InstanceID, DocumentName: h.DocumentName, CreatedAt: h.CreatedAt}
}

// membershipFor resolves the caller's identity + active membership without building a
// workspace (lightweight per-member CRUD). 401/403/409 mirror resolvedFor.
func (c config) membershipFor(w http.ResponseWriter, r *http.Request) (Identity, MembershipView, bool) {
	id := c.mgr.resolveIdentity(r)
	if id.key == "" {
		writeAPIErr(w, &apiError{http.StatusUnauthorized, "unauthenticated", "no gateway identity"})
		return Identity{}, MembershipView{}, false
	}
	tenantSel := r.Header.Get("X-AF-Tenant")
	if tenantSel == "" {
		tenantSel = r.URL.Query().Get("tenant")
	}
	ident, mv, aerr := c.mgr.resolveMembership(r.Context(), id.key, id.email, tenantSel)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return Identity{}, MembershipView{}, false
	}
	return ident, mv, true
}

// --- SSO sessions ----------------------------------------------------------------

func (c config) handleSSOSessionsList(w http.ResponseWriter, r *http.Request) {
	_, mv, ok := c.membershipFor(w, r)
	if !ok {
		return
	}
	rows, err := c.mgr.store.ListSSOSessions(r.Context(), mv.MembershipID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	out := make([]ssoSessionDTO, 0, len(rows))
	for _, v := range rows {
		out = append(out, ssoToDTO(v))
	}
	writeJSON(w, http.StatusOK, out)
}

func (c config) handleSSOSessionCreate(w http.ResponseWriter, r *http.Request) {
	_, mv, ok := c.membershipFor(w, r)
	if !ok {
		return
	}
	var in ssoSessionDTO
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid JSON body"})
		return
	}
	in.StartURL = strings.TrimSpace(in.StartURL)
	in.SSORegion = strings.TrimSpace(in.SSORegion)
	if !httpsURLRe.MatchString(in.StartURL) {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_start_url", "startUrl must be an https:// URL"})
		return
	}
	if in.SSORegion == "" {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_region", "ssoRegion is required"})
		return
	}
	v := SSOSession{ID: newID(), MembershipID: mv.MembershipID, Label: strings.TrimSpace(in.Label),
		StartURL: in.StartURL, SSORegion: in.SSORegion, CreatedAt: nowTS()}
	if err := c.mgr.store.CreateSSOSession(r.Context(), v); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusCreated, ssoToDTO(v))
}

func (c config) handleSSOSessionUpdate(w http.ResponseWriter, r *http.Request) {
	_, mv, ok := c.membershipFor(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	var in ssoSessionDTO
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid JSON body"})
		return
	}
	cur, found, err := c.mgr.store.GetSSOSession(r.Context(), id)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !found || cur.MembershipID != mv.MembershipID {
		writeAPIErr(w, &apiError{http.StatusNotFound, "not_found", "sso session not found"})
		return
	}
	in.StartURL = strings.TrimSpace(in.StartURL)
	in.SSORegion = strings.TrimSpace(in.SSORegion)
	if !httpsURLRe.MatchString(in.StartURL) || in.SSORegion == "" {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "startUrl (https) and ssoRegion are required"})
		return
	}
	cur.Label = strings.TrimSpace(in.Label)
	cur.StartURL = in.StartURL
	cur.SSORegion = in.SSORegion
	if err := c.mgr.store.UpdateSSOSession(r.Context(), cur); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, ssoToDTO(cur))
}

func (c config) handleSSOSessionDelete(w http.ResponseWriter, r *http.Request) {
	_, mv, ok := c.membershipFor(w, r)
	if !ok {
		return
	}
	if err := c.mgr.store.DeleteSSOSession(r.Context(), r.PathValue("id"), mv.MembershipID); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- SSM hosts -------------------------------------------------------------------

func (c config) handleSSMHostsList(w http.ResponseWriter, r *http.Request) {
	_, mv, ok := c.membershipFor(w, r)
	if !ok {
		return
	}
	rows, err := c.mgr.store.ListSSMHosts(r.Context(), mv.MembershipID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	out := make([]ssmHostDTO, 0, len(rows))
	for _, h := range rows {
		out = append(out, hostToDTO(h))
	}
	writeJSON(w, http.StatusOK, out)
}

// validateHost trims and checks a host DTO, and verifies any referenced SSO session
// belongs to the caller. Returns a normalized SSMHost (id/created_at unset).
func (c config) validateHost(ctx context.Context, mv MembershipView, in ssmHostDTO) (SSMHost, *apiError) {
	h := SSMHost{
		MembershipID: mv.MembershipID,
		Alias:        strings.TrimSpace(in.Alias),
		SSOSessionID: strings.TrimSpace(in.SSOSessionID),
		AccountID:    strings.TrimSpace(in.AccountID),
		RoleName:     strings.TrimSpace(in.RoleName),
		Region:       strings.TrimSpace(in.Region),
		InstanceID:   strings.TrimSpace(in.InstanceID),
		DocumentName: strings.TrimSpace(in.DocumentName),
	}
	if h.Alias == "" {
		return SSMHost{}, &apiError{http.StatusBadRequest, "bad_alias", "alias is required"}
	}
	if h.InstanceID == "" {
		return SSMHost{}, &apiError{http.StatusBadRequest, "bad_instance", "instanceId is required"}
	}
	if h.SSOSessionID != "" {
		s, found, err := c.mgr.store.GetSSOSession(ctx, h.SSOSessionID)
		if err != nil {
			return SSMHost{}, internalErr(err)
		}
		if !found || s.MembershipID != mv.MembershipID {
			return SSMHost{}, &apiError{http.StatusBadRequest, "bad_sso_session", "unknown ssoSessionId"}
		}
	}
	return h, nil
}

func (c config) handleSSMHostCreate(w http.ResponseWriter, r *http.Request) {
	_, mv, ok := c.membershipFor(w, r)
	if !ok {
		return
	}
	var in ssmHostDTO
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid JSON body"})
		return
	}
	h, aerr := c.validateHost(r.Context(), mv, in)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	h.ID = newID()
	h.CreatedAt = nowTS()
	if err := c.mgr.store.CreateSSMHost(r.Context(), h); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusCreated, hostToDTO(h))
}

func (c config) handleSSMHostUpdate(w http.ResponseWriter, r *http.Request) {
	_, mv, ok := c.membershipFor(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	cur, found, err := c.mgr.store.GetSSMHost(r.Context(), id)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !found || cur.MembershipID != mv.MembershipID {
		writeAPIErr(w, &apiError{http.StatusNotFound, "not_found", "host not found"})
		return
	}
	var in ssmHostDTO
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid JSON body"})
		return
	}
	h, aerr := c.validateHost(r.Context(), mv, in)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	h.ID = cur.ID
	h.CreatedAt = cur.CreatedAt
	if err := c.mgr.store.UpdateSSMHost(r.Context(), h); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, hostToDTO(h))
}

func (c config) handleSSMHostDelete(w http.ResponseWriter, r *http.Request) {
	_, mv, ok := c.membershipFor(w, r)
	if !ok {
		return
	}
	if err := c.mgr.store.DeleteSSMHost(r.Context(), r.PathValue("id"), mv.MembershipID); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ssmProfileRe strips characters unsafe for an ~/.aws/config profile header.
var ssmProfileRe = regexp.MustCompile(`[^A-Za-z0-9._@-]+`)

// ssmProfileName derives a stable aws profile name from a host alias.
func ssmProfileName(alias string) string {
	p := ssmProfileRe.ReplaceAllString(strings.TrimSpace(alias), "-")
	if p == "" {
		p = "ssm"
	}
	return p
}

// rewriteSSMCreate resolves a kind=ssm create request's ssm_host_id server-side and
// rewrites the request body so the Agent receives the full (non-secret) host + SSO
// coordinates. The client only sends {name, kind:"ssm", ssm_host_id}; the host's
// instance/document/region and SSO config stay authoritative in the CP DB and
// ownership is enforced here. Non-ssm requests pass through untouched.
func (c config) rewriteSSMCreate(ctx context.Context, res *resolved, r *http.Request) *apiError {
	var peek struct {
		Name      string `json:"name"`
		Kind      string `json:"kind"`
		SSMHostID string `json:"ssm_host_id"`
	}
	body, err := readAllBody(r)
	if err != nil {
		return &apiError{http.StatusBadRequest, "bad_request", "cannot read body"}
	}
	if err := json.Unmarshal(body, &peek); err != nil {
		return &apiError{http.StatusBadRequest, "bad_request", "invalid JSON body"}
	}
	if peek.Kind != "ssm" {
		restoreBody(r, body) // untouched pass-through
		return nil
	}
	if peek.SSMHostID == "" {
		return &apiError{http.StatusBadRequest, "bad_request", "ssm_host_id is required for kind=ssm"}
	}
	h, found, err := c.mgr.store.GetSSMHost(ctx, peek.SSMHostID)
	if err != nil {
		return internalErr(err)
	}
	if !found || h.MembershipID != res.mv.MembershipID {
		return &apiError{http.StatusNotFound, "not_found", "ssm host not found"}
	}
	var startURL, ssoRegion string
	if h.SSOSessionID != "" {
		s, sok, serr := c.mgr.store.GetSSOSession(ctx, h.SSOSessionID)
		if serr != nil {
			return internalErr(serr)
		}
		if sok && s.MembershipID == res.mv.MembershipID {
			startURL, ssoRegion = s.StartURL, s.SSORegion
		}
	}
	out := map[string]any{
		"name":           peek.Name,
		"kind":           "ssm",
		"ssm_profile":    ssmProfileName(h.Alias),
		"ssm_target":     h.InstanceID,
		"ssm_document":   h.DocumentName,
		"ssm_region":     h.Region,
		"sso_start_url":  startURL,
		"sso_region":     ssoRegion,
		"sso_account_id": h.AccountID,
		"sso_role_name":  h.RoleName,
	}
	nb, err := json.Marshal(out)
	if err != nil {
		return internalErr(err)
	}
	restoreBody(r, nb)
	// Record the intent (no secrets: instance + document + actor). Best-effort.
	_ = c.mgr.store.InsertAudit(ctx, AuditLog{
		ID: newID(), TenantID: res.ws.TenantID, ActorKind: "user", ActorID: res.ident.ID,
		Action: "ssm.start_session", Target: h.InstanceID,
		Detail: "alias=" + h.Alias + " document=" + h.DocumentName, At: nowTS(),
	})
	return nil
}
