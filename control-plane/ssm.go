package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
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

// SSM login (docs/log/p3-ssm-session.md). A member pre-registers SSO sessions and
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
	// Kind is "sso" (own Identity Center login) or "assume_role" (role chaining from the sso
	// profile SourceProfileID, issue #1109). The remaining fields belong to assume_role only;
	// accountId is then derived from roleArn and never taken from the request.
	Kind            string `json:"kind"`
	SourceProfileID string `json:"sourceProfileId,omitempty"`
	RoleARN         string `json:"roleArn,omitempty"`
	ExternalID      string `json:"externalId,omitempty"`
	SessionName     string `json:"sessionName,omitempty"`
	DurationSeconds int    `json:"durationSeconds,omitempty"`
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

func profileToDTO(p store.SSMProfile) ssmProfileDTO {
	return ssmProfileDTO{ID: p.ID, Label: p.Label, StartURL: p.StartURL, SSORegion: p.SSORegion,
		AccountID: p.AccountID, RoleName: p.RoleName, Region: p.Region, CreatedAt: p.CreatedAt,
		Kind: ssmKindOf(p), SourceProfileID: p.SourceProfileID, RoleARN: p.RoleARN,
		ExternalID: p.ExternalID, SessionName: p.SessionName, DurationSeconds: p.DurationSeconds}
}

// ssmKindOf reads a row's kind with the empty string as sso (a row from a store that
// predates the column).
func ssmKindOf(p store.SSMProfile) string {
	if p.Kind == "" {
		return store.SSMKindSSO
	}
	return p.Kind
}

func hostToDTO(h store.SSMHost) ssmHostDTO {
	return ssmHostDTO{ID: h.ID, Alias: h.Alias, ProfileID: h.ProfileID, Region: h.Region,
		InstanceID: h.InstanceID, DocumentName: h.DocumentName, CreatedAt: h.CreatedAt}
}

// ssmConfigAPI holds the SSM login-configuration handlers. Identity resolution comes from
// the embedded memberAuth (registration wraps these in withMembership) and the store is
// only the narrow SSMStore view. The name avoids ssmAPI, which runtime_ecs.go already uses
// for its AWS SSM client interface.
type ssmConfigAPI struct {
	memberAuth
	store store.SSMStore
}

func newSSMConfigAPI(m *manager) ssmConfigAPI { return ssmConfigAPI{memberAuth{m}, m.store} }

// --- profiles (common auth bundle) -----------------------------------------------

func (a ssmConfigAPI) listProfiles(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	rows, err := a.store.ListSSMProfiles(r.Context(), mv.MembershipID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, profileListWire(rows))
}

func profileListWire(rows []store.SSMProfile) []ssmProfileListDTO {
	counts := map[string]int{}
	for _, p := range rows {
		counts[ssmProfileName(p.Label)]++
	}
	out := make([]ssmProfileListDTO, 0, len(rows))
	for _, p := range rows {
		n := ssmProfileName(p.Label)
		out = append(out, ssmProfileListDTO{ssmProfileDTO: profileToDTO(p), Name: n, NameCollides: counts[n] > 1})
	}
	return out
}

// ssmProfileListDTO is a profile as the Settings list shows it: with the ~/.aws profile
// name the workspace knows it by, which the row's "Log in" (#1028) sends, and whether
// another label maps to that name, in which case awsProfilesWire exports neither.
// Read-only: create and import take ssmProfileDTO.
type ssmProfileListDTO struct {
	ssmProfileDTO
	Name         string `json:"name"`
	NameCollides bool   `json:"nameCollides,omitempty"`
}

// Allowlists for the assume-role fields. They are what an exported ~/.aws/config line may
// hold, so a value that passes cannot carry a newline or a second key; the Agent checks the
// same shapes again before it writes (sessionx.validateSSMMeta).
var (
	ssmRoleARNRe     = regexp.MustCompile(`^arn:aws[a-z-]*:iam::[0-9]{12}:role/[A-Za-z0-9+=,.@_/-]{1,512}$`)
	ssmExternalIDRe  = regexp.MustCompile(`^[A-Za-z0-9+=,.@:/_-]{2,}$`)
	ssmSessionNameRe = regexp.MustCompile(`^[A-Za-z0-9+=,.@_-]{2,64}$`)
	ssmRegionOnlyRe  = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
)

// ssmRoleARNAccount returns the account of a valid role ARN ("" when arn is not one). The
// role name (the last path segment) is at most 64 characters, as IAM allows.
func ssmRoleARNAccount(arn string) string {
	if !ssmRoleARNRe.MatchString(arn) || strings.HasSuffix(arn, "/") || strings.Contains(arn, "//") {
		return ""
	}
	if i := strings.LastIndexByte(arn, '/'); len(arn)-i-1 > 64 {
		return ""
	}
	return strings.Split(arn, ":")[4]
}

// validateProfile trims + checks a profile DTO. Returns a normalized SSMProfile
// (id/created_at unset). Whether an assume-role profile's source exists, is the caller's and
// is an sso profile is the store's call, made under the source's row lock.
func validateProfile(mv store.MembershipView, in ssmProfileDTO) (store.SSMProfile, *apiError) {
	p := store.SSMProfile{
		MembershipID: mv.MembershipID,
		Label:        strings.TrimSpace(in.Label),
		StartURL:     strings.TrimSpace(in.StartURL),
		SSORegion:    strings.TrimSpace(in.SSORegion),
		AccountID:    strings.TrimSpace(in.AccountID),
		RoleName:     strings.TrimSpace(in.RoleName),
		Region:       strings.TrimSpace(in.Region),
		Kind:         strings.TrimSpace(in.Kind),
	}
	if p.Label == "" {
		return store.SSMProfile{}, &apiError{http.StatusBadRequest, "bad_label", "label is required"}
	}
	switch p.Kind {
	case "", store.SSMKindSSO:
		p.Kind = store.SSMKindSSO
		if in.SourceProfileID != "" || in.RoleARN != "" || in.ExternalID != "" || in.SessionName != "" || in.DurationSeconds != 0 {
			return store.SSMProfile{}, &apiError{http.StatusBadRequest, "bad_kind", "sourceProfileId, roleArn, externalId, sessionName and durationSeconds belong to kind assume_role"}
		}
		if !httpsURLRe.MatchString(p.StartURL) {
			return store.SSMProfile{}, &apiError{http.StatusBadRequest, "bad_start_url", "startUrl must be an https:// URL"}
		}
		if p.SSORegion == "" {
			return store.SSMProfile{}, &apiError{http.StatusBadRequest, "bad_region", "ssoRegion is required"}
		}
		return p, nil
	case store.SSMKindAssumeRole:
	default:
		return store.SSMProfile{}, &apiError{http.StatusBadRequest, "bad_kind", "kind must be sso or assume_role"}
	}
	if p.StartURL != "" || p.SSORegion != "" || p.RoleName != "" {
		return store.SSMProfile{}, &apiError{http.StatusBadRequest, "bad_kind", "an assume_role profile has no portal of its own: leave startUrl, ssoRegion and roleName empty"}
	}
	p.RoleARN = strings.TrimSpace(in.RoleARN)
	acct := ssmRoleARNAccount(p.RoleARN)
	if acct == "" {
		return store.SSMProfile{}, &apiError{http.StatusBadRequest, "bad_role_arn", "roleArn must be an IAM role ARN (arn:aws:iam::<12-digit account>:role/<name>)"}
	}
	if p.AccountID != "" && p.AccountID != acct {
		return store.SSMProfile{}, &apiError{http.StatusBadRequest, "bad_role_arn", "accountId differs from the account in roleArn; leave it empty"}
	}
	p.AccountID = acct
	p.SourceProfileID = strings.TrimSpace(in.SourceProfileID)
	if p.SourceProfileID == "" {
		return store.SSMProfile{}, &apiError{http.StatusBadRequest, "bad_source", "sourceProfileId is required"}
	}
	p.ExternalID = strings.TrimSpace(in.ExternalID)
	if p.ExternalID != "" && !(len(p.ExternalID) <= 1224 && ssmExternalIDRe.MatchString(p.ExternalID)) {
		return store.SSMProfile{}, &apiError{http.StatusBadRequest, "bad_external_id", "externalId must be 2-1224 characters of letters, digits and +=,.@:/_-"}
	}
	p.SessionName = strings.TrimSpace(in.SessionName)
	if p.SessionName != "" && !ssmSessionNameRe.MatchString(p.SessionName) {
		return store.SSMProfile{}, &apiError{http.StatusBadRequest, "bad_session_name", "sessionName must be 2-64 characters of letters, digits and +=,.@_-"}
	}
	if in.DurationSeconds != 0 && (in.DurationSeconds < 900 || in.DurationSeconds > 43200) {
		return store.SSMProfile{}, &apiError{http.StatusBadRequest, "bad_duration", "durationSeconds must be between 900 and 43200 (the role's maximum session duration may be lower)"}
	}
	p.DurationSeconds = in.DurationSeconds
	if p.Region != "" && !ssmRegionOnlyRe.MatchString(p.Region) {
		return store.SSMProfile{}, &apiError{http.StatusBadRequest, "bad_region", "region must be lower-case letters, digits and -"}
	}
	return p, nil
}

// profileWriteErr maps a store error from CreateSSMProfile / UpdateSSMProfile.
func profileWriteErr(w http.ResponseWriter, err error) {
	var inUse *store.SSMProfileInUseError
	switch {
	case errors.Is(err, store.ErrSSMSourceInvalid):
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_source", "sourceProfileId must name one of your own sso profiles (not an assume_role profile)"})
	case errors.As(err, &inUse):
		writeJSON(w, http.StatusConflict, profileInUseBody(inUse.Hosts, inUse.Dependents))
	default:
		writeAPIErr(w, internalErr(err))
	}
}

func (a ssmConfigAPI) createProfile(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
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
	p.ID = store.NewID()
	p.CreatedAt = store.NowTS()
	if err := a.store.CreateSSMProfile(r.Context(), p); err != nil {
		profileWriteErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, profileToDTO(p))
}

func (a ssmConfigAPI) updateProfile(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	cur, found, err := a.store.GetSSMProfile(r.Context(), r.PathValue("id"))
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
	if err := a.store.UpdateSSMProfile(r.Context(), p); err != nil {
		profileWriteErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, profileToDTO(p))
}

func (a ssmConfigAPI) deleteProfile(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	err := a.store.DeleteSSMProfile(r.Context(), r.PathValue("id"), mv.MembershipID)
	var inUse *store.SSMProfileInUseError
	if errors.As(err, &inUse) {
		writeJSON(w, http.StatusConflict, profileInUseBody(inUse.Hosts, inUse.Dependents))
		return
	}
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ssmProfileInUseResp is the 409 for deleting a profile that hosts still use, or for turning
// one that assume-role profiles use as their source into an assume-role profile itself. Written by
// hand rather than through writeAPIErr because the Console names the users, and only the
// server knows them at the moment of the refusal: its own list may be stale. `error` keeps
// the shared {code, message} shape, so a caller that reads only that still gets a sentence.
type ssmProfileInUseResp struct {
	Error apiErrorBody `json:"error"`
	// Hosts are the aliases of the hosts that reference the profile, ordered by alias.
	Hosts []string `json:"hosts"`
	// Profiles are the labels of the assume-role profiles that take it as their source.
	Profiles []string `json:"profiles,omitempty"`
}

type apiErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func profileInUseBody(hosts []store.SSMHost, dependents []store.SSMProfile) ssmProfileInUseResp {
	aliases := make([]string, 0, len(hosts))
	for _, h := range hosts {
		aliases = append(aliases, h.Alias)
	}
	var labels []string
	for _, d := range dependents {
		labels = append(labels, d.Label)
	}
	msg := fmt.Sprintf("the profile is used by %d host(s): %s; point them at another profile or delete them first",
		len(aliases), strings.Join(aliases, ", "))
	if len(labels) > 0 {
		msg = fmt.Sprintf("the profile is the source of %d assume-role profile(s): %s", len(labels), strings.Join(labels, ", "))
		if len(aliases) > 0 {
			msg += fmt.Sprintf(", and is used by %d host(s): %s", len(aliases), strings.Join(aliases, ", "))
		}
		msg += "; point them elsewhere or delete them first"
	}
	return ssmProfileInUseResp{
		Error:    apiErrorBody{Code: "ssm_profile_in_use", Message: msg},
		Hosts:    aliases,
		Profiles: labels,
	}
}

// hostWriteErr maps a store error from CreateSSMHost / UpdateSSMHost, where a profile the
// member does not have (never had, or just deleted) is refused.
func hostWriteErr(err error) *apiError {
	if errors.Is(err, store.ErrSSMProfileNotFound) {
		return &apiError{http.StatusBadRequest, "bad_profile", "unknown profileId"}
	}
	return internalErr(err)
}

// --- SSM hosts -------------------------------------------------------------------

func (a ssmConfigAPI) listHosts(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	rows, err := a.store.ListSSMHosts(r.Context(), mv.MembershipID)
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

// validateHost trims and checks a host DTO. Returns a normalized SSMHost (id/created_at
// unset). Whether the profile exists and is the caller's is the store's call, made in the
// same transaction as the write (hostWriteErr).
func (a ssmConfigAPI) validateHost(mv store.MembershipView, in ssmHostDTO) (store.SSMHost, *apiError) {
	h := store.SSMHost{
		MembershipID: mv.MembershipID,
		Alias:        strings.TrimSpace(in.Alias),
		ProfileID:    strings.TrimSpace(in.ProfileID),
		Region:       strings.TrimSpace(in.Region),
		InstanceID:   strings.TrimSpace(in.InstanceID),
		DocumentName: strings.TrimSpace(in.DocumentName),
	}
	if h.Alias == "" {
		return store.SSMHost{}, &apiError{http.StatusBadRequest, "bad_alias", "alias is required"}
	}
	if h.InstanceID == "" {
		return store.SSMHost{}, &apiError{http.StatusBadRequest, "bad_instance", "instanceId is required"}
	}
	if h.ProfileID == "" {
		return store.SSMHost{}, &apiError{http.StatusBadRequest, "bad_profile", "profileId is required"}
	}
	return h, nil
}

func (a ssmConfigAPI) createHost(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	var in ssmHostDTO
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid JSON body"})
		return
	}
	h, aerr := a.validateHost(mv, in)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	h.ID = store.NewID()
	h.CreatedAt = store.NowTS()
	if err := a.store.CreateSSMHost(r.Context(), h); err != nil {
		writeAPIErr(w, hostWriteErr(err))
		return
	}
	writeJSON(w, http.StatusCreated, hostToDTO(h))
}

func (a ssmConfigAPI) updateHost(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	id := r.PathValue("id")
	cur, found, err := a.store.GetSSMHost(r.Context(), id)
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
	h, aerr := a.validateHost(mv, in)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	h.ID = cur.ID
	h.CreatedAt = cur.CreatedAt
	if err := a.store.UpdateSSMHost(r.Context(), h); err != nil {
		writeAPIErr(w, hostWriteErr(err))
		return
	}
	writeJSON(w, http.StatusOK, hostToDTO(h))
}

func (a ssmConfigAPI) deleteHost(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	if err := a.store.DeleteSSMHost(r.Context(), r.PathValue("id"), mv.MembershipID); err != nil {
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

// ssmLoginSource returns the profile whose Identity Center login p runs on: p itself for an
// sso profile, the source for an assume-role profile (nil source error when it is gone or no
// longer an sso profile, which the store keeps from happening but a stale read could show).
func ssmLoginSource(ctx context.Context, st store.SSMStore, p store.SSMProfile) (store.SSMProfile, *apiError) {
	if ssmKindOf(p) != store.SSMKindAssumeRole {
		return p, nil
	}
	src, ok, err := st.GetSSMProfile(ctx, p.SourceProfileID)
	if err != nil {
		return store.SSMProfile{}, internalErr(err)
	}
	if !ok || src.MembershipID != p.MembershipID || ssmKindOf(src) != store.SSMKindSSO {
		return store.SSMProfile{}, &apiError{http.StatusBadRequest, "bad_profile", "the profile's source profile is missing; edit it in 設定 → SSM"}
	}
	return src, nil
}

// ssmChainJSON adds the assume-role coordinates of p to a request body bound for the Agent.
// The sso_* / StartURL keys of that body describe the SOURCE profile (ssmLoginSource); these
// say what to assume from it. No-op for an sso profile.
func ssmChainJSON(p, src store.SSMProfile, m map[string]any, keys [5]string) {
	if ssmKindOf(p) != store.SSMKindAssumeRole {
		return
	}
	m[keys[0]] = ssmProfileName(src.Label)
	m[keys[1]] = p.RoleARN
	m[keys[2]] = p.ExternalID
	m[keys[3]] = p.SessionName
	m[keys[4]] = p.DurationSeconds
}

// rewriteSSMCreate resolves a kind=ssm create request's ssm_host_id server-side and
// rewrites the request body so the Agent receives the full (non-secret) host + SSO
// coordinates. The client only sends {name, kind:"ssm", ssm_host_id}; the host's
// instance/document/region and SSO config stay authoritative in the CP DB and
// ownership is enforced here. Non-ssm requests pass through untouched.
// The only caller is workspaceAPI.sessionCreate, hence the receiver.
func (a workspaceAPI) rewriteSSMCreate(ctx context.Context, res *resolved, r *http.Request) *apiError {
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
	h, found, err := a.mgr.store.GetSSMHost(ctx, peek.SSMHostID)
	if err != nil {
		return internalErr(err)
	}
	if !found || h.MembershipID != res.mv.MembershipID {
		return &apiError{http.StatusNotFound, "not_found", "ssm host not found"}
	}
	p, pok, err := a.mgr.store.GetSSMProfile(ctx, h.ProfileID)
	if err != nil {
		return internalErr(err)
	}
	if !pok || p.MembershipID != res.mv.MembershipID {
		return &apiError{http.StatusBadRequest, "bad_profile", "host has no valid profile; edit it in 設定 → SSM"}
	}
	src, aerr := ssmLoginSource(ctx, a.mgr.store, p)
	if aerr != nil {
		return aerr
	}
	// The instance region overrides the profile's default when set.
	region := h.Region
	if region == "" {
		region = p.Region
	}
	// Default session-name base = the host alias (e.g. "mng@g3prod-mon01"). The Agent
	// appends " @MMDD-HHMM" when the client sent no title. Only when another registered
	// host shares this alias do we disambiguate with the profile (falls back to the SSO
	// account id) — so the common case stays a clean bare alias.
	nameBase := h.Alias
	if others, lerr := a.mgr.store.ListSSMHosts(ctx, res.mv.MembershipID); lerr == nil {
		for _, o := range others {
			if o.ID != h.ID && strings.EqualFold(strings.TrimSpace(o.Alias), strings.TrimSpace(h.Alias)) {
				disambig := ssmProfileName(p.Label)
				if disambig == "" {
					disambig = p.AccountID
				}
				if disambig != "" {
					nameBase = h.Alias + " (" + disambig + ")"
				}
				break
			}
		}
	}
	out := map[string]any{
		"name":            peek.Name,
		"title":           peek.Title,
		"color":           peek.Color,
		"kind":            "ssm",
		"ssm_alias":       nameBase,
		"ssm_profile":     ssmProfileName(p.Label),
		"ssm_target":      h.InstanceID,
		"ssm_document":    h.DocumentName,
		"ssm_region":      region,
		"sso_start_url":   src.StartURL,
		"sso_region":      src.SSORegion,
		"sso_account_id":  src.AccountID,
		"sso_role_name":   src.RoleName,
		"ssm_force_login": peek.SSMForceLogin,
	}
	ssmChainJSON(p, src, out, [5]string{"ssm_source_profile", "ssm_role_arn", "ssm_external_id", "ssm_role_session_name", "ssm_duration_seconds"})
	nb, err := json.Marshal(out)
	if err != nil {
		return internalErr(err)
	}
	restoreBody(r, nb)
	// Record the intent (no secrets: instance + document + actor). Best-effort.
	_ = a.mgr.store.InsertAudit(ctx, store.AuditLog{
		ID: store.NewID(), TenantID: res.ws.TenantID, ActorKind: "user", ActorID: res.ident.ID,
		Action: "ssm.start_session", Target: h.InstanceID,
		Detail: "alias=" + h.Alias + " document=" + h.DocumentName, At: store.NowTS(),
	})
	return nil
}
