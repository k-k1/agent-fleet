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

// ssmProfileDTO / ssmHostDTO are the JSON wire shapes (no secrets). A profile is the
// COMMON auth bundle; a host references one and adds only per-instance fields.
type ssmProfileDTO struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	StartURL  string `json:"startUrl"`
	SSORegion string `json:"ssoRegion"`
	AccountID string `json:"accountId"`
	RoleName  string `json:"roleName"`
	Region    string `json:"region"`
	CreatedAt string `json:"createdAt"`
}

type ssmHostDTO struct {
	ID           string `json:"id"`
	Alias        string `json:"alias"`
	ProfileID    string `json:"profileId"`
	Region       string `json:"region"` // optional per-host override ("" = profile default)
	InstanceID   string `json:"instanceId"`
	DocumentName string `json:"documentName"`
	CreatedAt    string `json:"createdAt"`
}

func profileToDTO(p SSMProfile) ssmProfileDTO {
	return ssmProfileDTO{ID: p.ID, Label: p.Label, StartURL: p.StartURL, SSORegion: p.SSORegion,
		AccountID: p.AccountID, RoleName: p.RoleName, Region: p.Region, CreatedAt: p.CreatedAt}
}

func hostToDTO(h SSMHost) ssmHostDTO {
	return ssmHostDTO{ID: h.ID, Alias: h.Alias, ProfileID: h.ProfileID, Region: h.Region,
		InstanceID: h.InstanceID, DocumentName: h.DocumentName, CreatedAt: h.CreatedAt}
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

// --- profiles (common auth bundle) -----------------------------------------------

func (c config) handleSSMProfilesList(w http.ResponseWriter, r *http.Request) {
	_, mv, ok := c.membershipFor(w, r)
	if !ok {
		return
	}
	rows, err := c.mgr.store.ListSSMProfiles(r.Context(), mv.MembershipID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	out := make([]ssmProfileDTO, 0, len(rows))
	for _, p := range rows {
		out = append(out, profileToDTO(p))
	}
	writeJSON(w, http.StatusOK, out)
}

// validateProfile trims + checks a profile DTO. Returns a normalized SSMProfile
// (id/created_at unset).
func validateProfile(mv MembershipView, in ssmProfileDTO) (SSMProfile, *apiError) {
	p := SSMProfile{
		MembershipID: mv.MembershipID,
		Label:        strings.TrimSpace(in.Label),
		StartURL:     strings.TrimSpace(in.StartURL),
		SSORegion:    strings.TrimSpace(in.SSORegion),
		AccountID:    strings.TrimSpace(in.AccountID),
		RoleName:     strings.TrimSpace(in.RoleName),
		Region:       strings.TrimSpace(in.Region),
	}
	if p.Label == "" {
		return SSMProfile{}, &apiError{http.StatusBadRequest, "bad_label", "label is required"}
	}
	if !httpsURLRe.MatchString(p.StartURL) {
		return SSMProfile{}, &apiError{http.StatusBadRequest, "bad_start_url", "startUrl must be an https:// URL"}
	}
	if p.SSORegion == "" {
		return SSMProfile{}, &apiError{http.StatusBadRequest, "bad_region", "ssoRegion is required"}
	}
	return p, nil
}

func (c config) handleSSMProfileCreate(w http.ResponseWriter, r *http.Request) {
	_, mv, ok := c.membershipFor(w, r)
	if !ok {
		return
	}
	var in ssmProfileDTO
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid JSON body"})
		return
	}
	p, aerr := validateProfile(mv, in)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	p.ID = newID()
	p.CreatedAt = nowTS()
	if err := c.mgr.store.CreateSSMProfile(r.Context(), p); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusCreated, profileToDTO(p))
}

func (c config) handleSSMProfileUpdate(w http.ResponseWriter, r *http.Request) {
	_, mv, ok := c.membershipFor(w, r)
	if !ok {
		return
	}
	cur, found, err := c.mgr.store.GetSSMProfile(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !found || cur.MembershipID != mv.MembershipID {
		writeAPIErr(w, &apiError{http.StatusNotFound, "not_found", "profile not found"})
		return
	}
	var in ssmProfileDTO
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid JSON body"})
		return
	}
	p, aerr := validateProfile(mv, in)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	p.ID = cur.ID
	p.CreatedAt = cur.CreatedAt
	if err := c.mgr.store.UpdateSSMProfile(r.Context(), p); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, profileToDTO(p))
}

func (c config) handleSSMProfileDelete(w http.ResponseWriter, r *http.Request) {
	_, mv, ok := c.membershipFor(w, r)
	if !ok {
		return
	}
	if err := c.mgr.store.DeleteSSMProfile(r.Context(), r.PathValue("id"), mv.MembershipID); err != nil {
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

// validateHost trims and checks a host DTO, and verifies the referenced profile
// belongs to the caller. Returns a normalized SSMHost (id/created_at unset).
func (c config) validateHost(ctx context.Context, mv MembershipView, in ssmHostDTO) (SSMHost, *apiError) {
	h := SSMHost{
		MembershipID: mv.MembershipID,
		Alias:        strings.TrimSpace(in.Alias),
		ProfileID:    strings.TrimSpace(in.ProfileID),
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
	if h.ProfileID == "" {
		return SSMHost{}, &apiError{http.StatusBadRequest, "bad_profile", "profileId is required"}
	}
	p, found, err := c.mgr.store.GetSSMProfile(ctx, h.ProfileID)
	if err != nil {
		return SSMHost{}, internalErr(err)
	}
	if !found || p.MembershipID != mv.MembershipID {
		return SSMHost{}, &apiError{http.StatusBadRequest, "bad_profile", "unknown profileId"}
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

// ssmProfileName derives a stable aws profile name from a profile label.
func ssmProfileName(label string) string {
	p := ssmProfileRe.ReplaceAllString(strings.TrimSpace(label), "-")
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
		Name          string `json:"name"`
		Title         string `json:"title"`
		Color         string `json:"color"`
		Kind          string `json:"kind"`
		SSMHostID     string `json:"ssm_host_id"`
		SSMForceLogin bool   `json:"ssm_force_login"`
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
	p, pok, err := c.mgr.store.GetSSMProfile(ctx, h.ProfileID)
	if err != nil {
		return internalErr(err)
	}
	if !pok || p.MembershipID != res.mv.MembershipID {
		return &apiError{http.StatusBadRequest, "bad_profile", "host has no valid profile; edit it in 設定 → SSM"}
	}
	// The instance region overrides the profile's default when set.
	region := h.Region
	if region == "" {
		region = p.Region
	}
	out := map[string]any{
		"name":            peek.Name,
		"title":           peek.Title,
		"color":           peek.Color,
		"kind":            "ssm",
		"ssm_profile":     ssmProfileName(p.Label),
		"ssm_target":      h.InstanceID,
		"ssm_document":    h.DocumentName,
		"ssm_region":      region,
		"sso_start_url":   p.StartURL,
		"sso_region":      p.SSORegion,
		"sso_account_id":  p.AccountID,
		"sso_role_name":   p.RoleName,
		"ssm_force_login": peek.SSMForceLogin,
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
