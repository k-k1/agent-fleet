package main

// AWS profiles bridge — the PULL face of the member's SSO profiles (issue #998).
//
//	member's container ──(AF_AWS_PROFILES_TOKEN)──▶ CP GET /internal/aws-profiles
//	                                                    │ the member's SSM profiles
//	                                     managed block in ~/.aws/config
//
// Why this exists. The profiles a member registers in Settings → SSM live in this
// database, and the Agent only ever saw them inside a single SSM session launch or an
// ops connection, each written to an isolated AWS_CONFIG_FILE. A plain `aws --profile
// <name>`, an SDK or a build tool reads ~/.aws/config and found nothing, so members
// re-typed the same non-secret settings by hand. The container pulls the list instead,
// because the CP cannot push while the workspace is stopped, which is exactly when the
// settings page is usually edited.
//
// The response is non-secret (start URL, SSO region, account id, role name, region):
// SSO credentials are obtained by the aws CLI inside the container and never pass
// through here. The token is still a SEPARATE credential from the other bridges, so a
// leak reads this member's profile list and grants nothing else.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func awsProfilesSignKey(master32 []byte) []byte {
	mac := hmac.New(sha256.New, master32)
	mac.Write([]byte("af-aws-profiles-token-sign/v1"))
	return mac.Sum(nil)
}

// mintAWSProfilesToken returns the deterministic bridge token for a membership. Format:
// "afp_" + b64url(membershipID) + "." + tag. Deterministic, so re-injecting it on every
// container start is idempotent (same as the other bridge tokens).
func mintAWSProfilesToken(signKey []byte, membershipID string) string {
	return "afp_" + base64.RawURLEncoding.EncodeToString([]byte(membershipID)) + "." + awsProfilesTokenTag(signKey, membershipID)
}

func awsProfilesTokenTag(signKey []byte, membershipID string) string {
	mac := hmac.New(sha256.New, signKey)
	mac.Write([]byte(membershipID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:16])
}

// verifyAWSProfilesToken checks the tag and returns the embedded membership id. It does
// NOT resolve the membership; the caller does a live lookup so a revoked membership
// stops receiving profiles on the next pull.
func verifyAWSProfilesToken(signKey []byte, token string) (membershipID string, ok bool) {
	body, hasPrefix := strings.CutPrefix(strings.TrimSpace(token), "afp_")
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
	if !hmac.Equal([]byte(body[dot+1:]), []byte(awsProfilesTokenTag(signKey, mid))) {
		return "", false
	}
	return mid, true
}

// awsProfileWire is one exported profile. Name is the aws profile name the Agent writes,
// derived here with ssmProfileName so it is the same name an SSM session's isolated
// config uses — and therefore the same sso-session, whose cached login is shared.
type awsProfileWire struct {
	Name      string `json:"name"`
	Label     string `json:"label"`
	StartURL  string `json:"startUrl"`
	SSORegion string `json:"ssoRegion"`
	AccountID string `json:"accountId,omitempty"`
	RoleName  string `json:"roleName,omitempty"`
	Region    string `json:"region,omitempty"`
}

// awsProfilesResponse is the body of GET /internal/aws-profiles.
type awsProfilesResponse struct {
	Profiles []awsProfileWire `json:"profiles"`
}

type awsProfilesBridgeAPI struct{ mgr *manager }

func newAWSProfilesBridgeAPI(m *manager) awsProfilesBridgeAPI { return awsProfilesBridgeAPI{m} }

// list (GET /internal/aws-profiles) returns the caller's own profiles. The membership
// comes from the token, never from the request, so there is no request shape that reads
// another member's list.
func (a awsProfilesBridgeAPI) list(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	mid, ok := verifyAWSProfilesToken(awsProfilesSignKey(a.mgr.tokenSignMaster()), tok)
	if !ok {
		writeAPIErr(w, &apiError{http.StatusUnauthorized, "unauthenticated", "invalid aws profiles token"})
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
	rows, err := a.mgr.store.ListSSMProfiles(r.Context(), mv.MembershipID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, awsProfilesResponse{Profiles: awsProfilesWire(rows)})
}

// awsProfilesWire maps rows to the wire list. Two labels can sanitize to the same
// profile name ("prod app" / "prod-app"); the first row wins and the rest are left out,
// because two [profile x] sections would make the aws CLI silently merge them.
func awsProfilesWire(rows []store.SSMProfile) []awsProfileWire {
	out := make([]awsProfileWire, 0, len(rows))
	seen := map[string]bool{}
	for _, p := range rows {
		name := ssmProfileName(p.Label)
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, awsProfileWire{
			Name: name, Label: p.Label, StartURL: p.StartURL, SSORegion: p.SSORegion,
			AccountID: p.AccountID, RoleName: p.RoleName, Region: p.Region,
		})
	}
	return out
}
