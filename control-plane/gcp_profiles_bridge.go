package main

// Google Cloud profiles bridge (ADR 0107 decision 1) — the pull face of Settings → Google
// Cloud, the counterpart of aws_profiles_bridge.go.
//
//	member's container ──(AF_GCP_PROFILES_TOKEN)──▶ CP GET /internal/gcp-profiles
//	                                                    │ the member's Google Cloud profiles
//	                                     configurations af-<name> in the Agent's gcloud root
//
// The response is non-secret (project, account, impersonation target): the Agent's gcloud
// logs in by itself and no credential passes through here. The token is still a credential
// of its own, signed under its own derivation label, so neither bridge's token opens the
// other's list and a leak of this one reads this member's profiles and nothing else.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strings"
	"time"
)

func gcpProfilesSignKey(master32 []byte) []byte {
	mac := hmac.New(sha256.New, master32)
	mac.Write([]byte("af-gcp-profiles-token-sign/v1"))
	return mac.Sum(nil)
}

// mintGCPProfilesToken returns the deterministic bridge token for a membership:
// "afg_" + b64url(membershipID) + "." + tag. Deterministic, so re-injecting it on every
// container start is idempotent, as for the other bridge tokens.
func mintGCPProfilesToken(signKey []byte, membershipID string) string {
	return "afg_" + base64.RawURLEncoding.EncodeToString([]byte(membershipID)) + "." + gcpProfilesTokenTag(signKey, membershipID)
}

func gcpProfilesTokenTag(signKey []byte, membershipID string) string {
	mac := hmac.New(sha256.New, signKey)
	mac.Write([]byte(membershipID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:16])
}

// verifyGCPProfilesToken checks the tag and returns the embedded membership id. It does
// NOT resolve the membership; the caller looks it up live, so a removed membership stops
// receiving profiles on its next pull.
func verifyGCPProfilesToken(signKey []byte, token string) (membershipID string, ok bool) {
	body, hasPrefix := strings.CutPrefix(strings.TrimSpace(token), "afg_")
	if !hasPrefix {
		return "", false
	}
	dot := strings.LastIndexByte(body, '.')
	if dot < 0 {
		return "", false
	}
	idRaw, err := base64.RawURLEncoding.DecodeString(body[:dot])
	if err != nil || len(idRaw) == 0 {
		return "", false
	}
	mid := string(idRaw)
	if !hmac.Equal([]byte(body[dot+1:]), []byte(gcpProfilesTokenTag(signKey, mid))) {
		return "", false
	}
	return mid, true
}

// gcpProfileWire is one exported profile. QuotaProject is the EFFECTIVE value (the project
// when the row leaves it empty), so the Agent never has to know the default. ID lets the
// Agent tell a recreated profile from an edited one, which resets its login selection.
type gcpProfileWire struct {
	ID                        string `json:"id"`
	Name                      string `json:"name"`
	Label                     string `json:"label"`
	LoginMethod               string `json:"loginMethod"`
	Project                   string `json:"project"`
	QuotaProject              string `json:"quotaProject"`
	Account                   string `json:"account"`
	Region                    string `json:"region"`
	Zone                      string `json:"zone"`
	ImpersonateServiceAccount string `json:"impersonateServiceAccount"`
	UpdatedAt                 string `json:"updatedAt"`
}

// gcpProfileConflict names a profile name two or more labels map to, and those labels.
type gcpProfileConflict struct {
	Name   string   `json:"name"`
	Labels []string `json:"labels"`
}

// gcpProfilesResponse is the body of GET /internal/gcp-profiles. Conflicts is always an
// array, so an Agent can tell "no conflicts" from an older CP that never sent the field.
type gcpProfilesResponse struct {
	Profiles  []gcpProfileWire     `json:"profiles"`
	Conflicts []gcpProfileConflict `json:"conflicts"`
}

type gcpProfilesBridgeAPI struct{ mgr *manager }

func newGCPProfilesBridgeAPI(m *manager) gcpProfilesBridgeAPI { return gcpProfilesBridgeAPI{m} }

// list (GET /internal/gcp-profiles) returns the caller's own profiles. The membership comes
// from the token, never from the request.
func (a gcpProfilesBridgeAPI) list(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	mid, ok := verifyGCPProfilesToken(gcpProfilesSignKey(a.mgr.tokenSignMaster()), tok)
	if !ok {
		writeAPIErr(w, &apiError{http.StatusUnauthorized, "unauthenticated", "invalid gcp profiles token"})
		return
	}
	mv, ok, err := a.mgr.store.GetMembershipByID(r.Context(), mid)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok {
		writeAPIErr(w, &apiError{http.StatusUnauthorized, "unauthenticated", "membership not active"})
		return
	}
	rows, err := a.mgr.store.ListGCPProfiles(r.Context(), mv.MembershipID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, gcpProfilesWire(gcpNameProfiles(rows)))
}

// gcpProfilesWire maps named rows to the bridge body: a row whose name collides is left
// out and its name reported once, with every label that maps to it.
func gcpProfilesWire(named []gcpNamedProfile) gcpProfilesResponse {
	out := gcpProfilesResponse{Profiles: []gcpProfileWire{}, Conflicts: []gcpProfileConflict{}}
	reported := map[string]bool{}
	for _, p := range named {
		if len(p.Colliders) > 0 {
			if !reported[p.Name] {
				reported[p.Name] = true
				out.Conflicts = append(out.Conflicts, gcpProfileConflict{Name: p.Name, Labels: p.Colliders})
			}
			continue
		}
		quota := p.QuotaProject
		if quota == "" {
			quota = p.Project
		}
		out.Profiles = append(out.Profiles, gcpProfileWire{
			ID: p.ID, Name: p.Name, Label: p.Label, LoginMethod: p.LoginMethod,
			Project: p.Project, QuotaProject: quota, Account: p.Account,
			Region: p.Region, Zone: p.Zone, ImpersonateServiceAccount: p.ImpersonateServiceAccount,
			UpdatedAt: rfc3339(p.UpdatedAt),
		})
	}
	return out
}

// rfc3339 re-emits a stored timestamp in RFC 3339. Rows are written with store.NowTS,
// which already is; this only guards against a row written some other way.
func rfc3339(ts string) string {
	if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	return ts
}
