package main

// Google Cloud profiles in Settings (ADR 0107 decision 1). A member defines which project,
// account and impersonation target a command is pointed at; the Agent pulls the rows over
// the bridge (gcp_profiles_bridge.go) and keeps every credential in its own gcloud store.
// Nothing secret is stored here, and a service-account key is refused in any field.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// gcpProfileDTO is the Settings API's row. Name and Conflict are computed by the CP on
// every answer and ignored on input: the Console and the Agent read them, never recompute.
type gcpProfileDTO struct {
	ID                        string           `json:"id"`
	Label                     string           `json:"label"`
	LoginMethod               string           `json:"loginMethod"`
	Project                   string           `json:"project"`
	QuotaProject              string           `json:"quotaProject"`
	Account                   string           `json:"account"`
	Region                    string           `json:"region"`
	Zone                      string           `json:"zone"`
	ImpersonateServiceAccount string           `json:"impersonateServiceAccount"`
	Name                      string           `json:"name"`
	Conflict                  *gcpNameConflict `json:"conflict,omitempty"`
}

// gcpNameConflict says why a row is not exported. "collision" is the only reason today.
type gcpNameConflict struct {
	Reason string   `json:"reason"`
	Labels []string `json:"labels"`
}

const gcpLoginMethodGoogle = "google"

// gcpProfileName is the one implementation of a profile's name (ADR 0107 decision 1): the
// Settings API, the bridge and through the bridge the Agent's `--list` all read it from
// here. The gcloud configuration is `af-<name>`, and gcloud accepts only a lowercase letter
// followed by `a-z0-9-`, so the rule is: ASCII-lowercase, every run outside `a-z0-9` → `-`,
// trim `-`, prefix `p` before a leading digit, and a label that leaves nothing (`本番`)
// gets `p-` + 8 hex of sha256(id) so it is still usable and stable across renames.
func gcpProfileName(id, label string) string {
	var b strings.Builder
	dash := false
	for i := 0; i < len(label); i++ {
		c := label[i]
		// Byte-wise and ASCII only: strings.ToLower would fold the Kelvin sign to `k`
		// and let a non-ASCII label pass as a different ASCII name.
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		if ('a' <= c && c <= 'z') || ('0' <= c && c <= '9') {
			b.WriteByte(c)
			dash = false
			continue
		}
		if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	n := strings.Trim(b.String(), "-")
	if n == "" {
		sum := sha256.Sum256([]byte(id))
		return "p-" + hex.EncodeToString(sum[:])[:8]
	}
	if n[0] >= '0' && n[0] <= '9' {
		n = "p" + n
	}
	return n
}

// gcpNamedProfile is one row with its computed name and, when its name collides with
// another row's, every label that maps to that name (sorted as the rows are).
type gcpNamedProfile struct {
	store.GCPProfile
	Name      string
	Colliders []string
}

// gcpNameProfiles applies the name and the collision rule to a member's whole list. Two
// rows with one name are BOTH left unexported: keeping either would hand `--profile prod`
// to whichever sorted first, which may be the other project.
func gcpNameProfiles(rows []store.GCPProfile) []gcpNamedProfile {
	labels := map[string][]string{}
	out := make([]gcpNamedProfile, 0, len(rows))
	for _, p := range rows {
		n := gcpProfileName(p.ID, p.Label)
		labels[n] = append(labels[n], p.Label)
		out = append(out, gcpNamedProfile{GCPProfile: p, Name: n})
	}
	for i := range out {
		if ls := labels[out[i].Name]; len(ls) > 1 {
			out[i].Colliders = ls
		}
	}
	return out
}

func gcpProfileToDTO(p gcpNamedProfile) gcpProfileDTO {
	d := gcpProfileDTO{
		ID: p.ID, Label: p.Label, LoginMethod: p.LoginMethod, Project: p.Project,
		QuotaProject: p.QuotaProject, Account: p.Account, Region: p.Region, Zone: p.Zone,
		ImpersonateServiceAccount: p.ImpersonateServiceAccount, Name: p.Name,
	}
	if len(p.Colliders) > 0 {
		d.Conflict = &gcpNameConflict{Reason: "collision", Labels: p.Colliders}
	}
	return d
}

var (
	// A project id: 6–30 of `a-z0-9-`, starting with a letter and not ending with `-`,
	// optionally behind a legacy `domain:` prefix (example.com:my-project).
	gcpProjectRe = regexp.MustCompile(`^(?:[a-z0-9][a-z0-9.-]*\.[a-z]{2,}:)?[a-z][a-z0-9-]{4,28}[a-z0-9]$`)
	gcpEmailRe   = regexp.MustCompile(`^[^\s@,;"'<>]+@[a-zA-Z0-9-]+(?:\.[a-zA-Z0-9-]+)+$`)
	// A service account: user-managed (…@<project>.iam.gserviceaccount.com) or a
	// Google-managed default (…@developer.gserviceaccount.com, …@appspot.gserviceaccount.com).
	gcpServiceAccountRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,99}@(?:[a-z0-9:.-]+\.)?gserviceaccount\.com$`)
	gcpRegionRe         = regexp.MustCompile(`^[a-z]+(?:-[a-z]+)+[0-9]+$`)
	gcpZoneRe           = regexp.MustCompile(`^[a-z]+(?:-[a-z]+)+[0-9]+-[a-z]$`)
	// What a service-account JSON key, or a private key pasted on its own, looks like.
	gcpKeyMaterialRe = regexp.MustCompile(`(?i)"?private_key(?:_id)?"?\s*:|"type"\s*:\s*"service_account"|-----BEGIN`)
	// A request field named like a place for a key. Refused with its own code so the
	// Console can say "keys are not accepted" rather than "unknown field".
	gcpKeyFieldRe = regexp.MustCompile(`(?i)key|credential|secret|token|json|password`)
)

// gcpProfileInputFields are the fields a write may carry. Name and conflict are what the
// list returns, so a Console that sends a row back unchanged is accepted.
var gcpProfileInputFields = map[string]bool{
	"id": true, "label": true, "loginMethod": true, "project": true, "quotaProject": true,
	"account": true, "region": true, "zone": true, "impersonateServiceAccount": true,
	"name": true, "conflict": true,
}

const gcpLabelMaxLen = 100

// decodeGCPProfile reads a write body. Unknown fields are refused outright instead of
// being dropped, so a key sent under any name never reaches a log line or a row by way of
// a later field added without thought.
func decodeGCPProfile(r *http.Request) (gcpProfileDTO, *apiError) {
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 64<<10)).Decode(&raw); err != nil || raw == nil {
		return gcpProfileDTO{}, &apiError{http.StatusBadRequest, "bad_request", "invalid JSON body"}
	}
	for k := range raw {
		if gcpProfileInputFields[k] {
			continue
		}
		if gcpKeyFieldRe.MatchString(k) {
			return gcpProfileDTO{}, gcpKeyRefused()
		}
		return gcpProfileDTO{}, &apiError{http.StatusBadRequest, "bad_request", "unknown field " + strings.TrimSpace(k)}
	}
	delete(raw, "conflict")
	b, _ := json.Marshal(raw)
	var in gcpProfileDTO
	if err := json.Unmarshal(b, &in); err != nil {
		return gcpProfileDTO{}, &apiError{http.StatusBadRequest, "bad_request", "invalid JSON body"}
	}
	return in, nil
}

func gcpKeyRefused() *apiError {
	return &apiError{http.StatusBadRequest, "key_refused",
		"service-account keys are not accepted; use impersonateServiceAccount with a logged-in account"}
}

// validateGCPProfile trims and checks a write. Returns the row without id or timestamps.
func validateGCPProfile(mv store.MembershipView, in gcpProfileDTO) (store.GCPProfile, *apiError) {
	p := store.GCPProfile{
		MembershipID:              mv.MembershipID,
		Label:                     strings.TrimSpace(in.Label),
		LoginMethod:               strings.TrimSpace(in.LoginMethod),
		Project:                   strings.TrimSpace(in.Project),
		QuotaProject:              strings.TrimSpace(in.QuotaProject),
		Account:                   strings.TrimSpace(in.Account),
		Region:                    strings.TrimSpace(in.Region),
		Zone:                      strings.TrimSpace(in.Zone),
		ImpersonateServiceAccount: strings.TrimSpace(in.ImpersonateServiceAccount),
	}
	for _, v := range []string{p.Label, p.LoginMethod, p.Project, p.QuotaProject, p.Account, p.Region, p.Zone, p.ImpersonateServiceAccount} {
		if gcpKeyMaterialRe.MatchString(v) {
			return store.GCPProfile{}, gcpKeyRefused()
		}
	}
	bad := func(code, msg string) (store.GCPProfile, *apiError) {
		return store.GCPProfile{}, &apiError{http.StatusBadRequest, code, msg}
	}
	if p.Label == "" || len([]rune(p.Label)) > gcpLabelMaxLen {
		return bad("bad_label", "label is required (at most 100 characters)")
	}
	if p.LoginMethod == "" {
		p.LoginMethod = gcpLoginMethodGoogle
	}
	if p.LoginMethod != gcpLoginMethodGoogle {
		return bad("bad_login_method", `loginMethod must be "google"`)
	}
	if !gcpProjectRe.MatchString(p.Project) {
		return bad("bad_project", "project must be a Google Cloud project id")
	}
	if p.QuotaProject != "" && !gcpProjectRe.MatchString(p.QuotaProject) {
		return bad("bad_quota_project", "quotaProject must be a Google Cloud project id")
	}
	if p.Account != "" && (len(p.Account) > 254 || !gcpEmailRe.MatchString(p.Account)) {
		return bad("bad_account", "account must be an email address")
	}
	if p.ImpersonateServiceAccount != "" && !gcpServiceAccountRe.MatchString(p.ImpersonateServiceAccount) {
		return bad("bad_service_account", "impersonateServiceAccount must be a service account email (…gserviceaccount.com)")
	}
	if p.Region != "" && !gcpRegionRe.MatchString(p.Region) {
		return bad("bad_region", "region must look like asia-northeast1")
	}
	if p.Zone != "" && !gcpZoneRe.MatchString(p.Zone) {
		return bad("bad_zone", "zone must look like asia-northeast1-a")
	}
	return p, nil
}

// gcpConfigAPI holds the Settings handlers. Registration wraps them in withMembership.
type gcpConfigAPI struct {
	memberAuth
	store store.GCPProfileStore
}

func newGCPConfigAPI(m *manager) gcpConfigAPI { return gcpConfigAPI{memberAuth{m}, m.store} }

func (a gcpConfigAPI) listProfiles(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	rows, err := a.store.ListGCPProfiles(r.Context(), mv.MembershipID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	named := gcpNameProfiles(rows)
	out := make([]gcpProfileDTO, 0, len(named))
	for _, p := range named {
		out = append(out, gcpProfileToDTO(p))
	}
	writeJSON(w, http.StatusOK, out)
}

// answerRow writes one row as the list would show it: its name and conflict depend on
// the member's other rows, so the answer to a create or an edit is computed from the list.
func (a gcpConfigAPI) answerRow(w http.ResponseWriter, r *http.Request, mv store.MembershipView, id string, status int) {
	rows, err := a.store.ListGCPProfiles(r.Context(), mv.MembershipID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	for _, p := range gcpNameProfiles(rows) {
		if p.ID == id {
			writeJSON(w, status, gcpProfileToDTO(p))
			return
		}
	}
	writeAPIErr(w, &apiError{http.StatusNotFound, "not_found", "profile not found"})
}

func (a gcpConfigAPI) createProfile(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	in, aerr := decodeGCPProfile(r)
	if aerr == nil {
		var p store.GCPProfile
		if p, aerr = validateGCPProfile(mv, in); aerr == nil {
			p.ID = store.NewID()
			p.CreatedAt = store.NowTS()
			p.UpdatedAt = p.CreatedAt
			if err := a.store.CreateGCPProfile(r.Context(), p); err != nil {
				writeAPIErr(w, internalErr(err))
				return
			}
			a.answerRow(w, r, mv, p.ID, http.StatusCreated)
			return
		}
	}
	writeAPIErr(w, aerr)
}

func (a gcpConfigAPI) updateProfile(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	cur, found, err := a.store.GetGCPProfile(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !found || cur.MembershipID != mv.MembershipID {
		writeAPIErr(w, &apiError{http.StatusNotFound, "not_found", "profile not found"})
		return
	}
	in, aerr := decodeGCPProfile(r)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	p, aerr := validateGCPProfile(mv, in)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	p.ID = cur.ID
	p.CreatedAt = cur.CreatedAt
	p.UpdatedAt = store.NowTS()
	if err := a.store.UpdateGCPProfile(r.Context(), p); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	a.answerRow(w, r, mv, p.ID, http.StatusOK)
}

func (a gcpConfigAPI) deleteProfile(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	if err := a.store.DeleteGCPProfile(r.Context(), r.PathValue("id"), mv.MembershipID); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
