package main

// AWS profiles bridge — the PULL face of the member's SSO profiles (issue #998).
//
//	member's container ──(AF_AWS_PROFILES_TOKEN)──▶ CP GET /internal/aws-profiles
//	                                                    │ the member's SSM profiles
//	                                     managed block in ~/.aws/config
//
// Why this exists. The profiles a member registers in Settings → AWS profiles/SSM live in this
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
	"sort"
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
	// ID is the Settings row's id: a confirmation grant is keyed by it, never by the name a
	// relabel or a recreated profile can reuse (ADR 0126 decision 5).
	ID        string `json:"id,omitempty"`
	Name      string `json:"name"`
	Label     string `json:"label"`
	StartURL  string `json:"startUrl"`
	SSORegion string `json:"ssoRegion"`
	AccountID string `json:"accountId,omitempty"`
	RoleName  string `json:"roleName,omitempty"`
	Region    string `json:"region,omitempty"`
	// Kind is "assume_role" for a profile that assumes RoleARN from the exported SSO profile
	// SourceProfile (issue #1109), and absent for an sso profile, so the bytes an sso profile
	// exports do not change. A chained profile has no portal: StartURL and SSORegion are empty
	// and AccountID is the role ARN's. ExternalID, SessionName and DurationSeconds are
	// optional assume-role parameters; none of them is a secret.
	Kind            string `json:"kind,omitempty"`
	RoleARN         string `json:"roleArn,omitempty"`
	SourceProfile   string `json:"sourceProfile,omitempty"`
	ExternalID      string `json:"externalId,omitempty"`
	SessionName     string `json:"sessionName,omitempty"`
	DurationSeconds int    `json:"durationSeconds,omitempty"`
	// ConfirmBeforeUse: af-aws-exec asks the member in the Console before the first use in a
	// session, and the profile is not written to ~/.aws/config (ADR 0126 decisions 4 to 6).
	ConfirmBeforeUse bool `json:"confirmBeforeUse,omitempty"`
	// SignIn and SignInID name the shared sign-in an SSO profile is in (ADR 0125): its
	// sso-session is af-<SignIn>. Absent for a sign-in of its own. An Agent that predates them
	// renders af-<Name> with the copied portal: a separate login, never another member's.
	SignIn   string `json:"signIn,omitempty"`
	SignInID string `json:"signInId,omitempty"`
}

// awsProfilesVersionHeader names the bridge version on both sides (ADR 0126 decision 8). An
// Agent that sends it understands confirmBeforeUse; the CP answers with it, so an Agent can
// tell this answer from an older CP's, which carries no gates.
const (
	awsProfilesVersionHeader = "X-AF-AWS-Profiles"
	awsProfilesVersion       = "2"
)

// awsProfilesResponse is the body of GET /internal/aws-profiles.
type awsProfilesResponse struct {
	Profiles  []awsProfileWire     `json:"profiles"`
	Conflicts []awsProfileConflict `json:"conflicts,omitempty"`
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
	ps, conflicts := awsProfilesWire(rows)
	if r.Header.Get(awsProfilesVersionHeader) != awsProfilesVersion {
		// An Agent that predates the gate would ignore confirmBeforeUse and write the profile
		// to ~/.aws/config ungated. It gets none of the gated profiles instead: unusable there,
		// never ungated.
		ps = withoutGated(ps)
	}
	w.Header().Set(awsProfilesVersionHeader, awsProfilesVersion)
	writeJSON(w, http.StatusOK, awsProfilesResponse{Profiles: ps, Conflicts: conflicts})
}

// withoutGated drops the gated profiles, and the chains whose source is gated: such a chain
// runs on its source's role, which is what the gate protects.
func withoutGated(ps []awsProfileWire) []awsProfileWire {
	gated := map[string]bool{}
	for _, p := range ps {
		if p.ConfirmBeforeUse {
			gated[p.Name] = true
		}
	}
	out := ps[:0:0]
	for _, p := range ps {
		if p.ConfirmBeforeUse || (p.SourceProfile != "" && gated[p.SourceProfile]) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// awsProfileConflict names a profile name that two or more Settings labels sanitize to
// ("prod app" / "prod-app"), and those labels.
type awsProfileConflict struct {
	Name   string   `json:"name"`
	Labels []string `json:"labels"`
}

// awsProfilesWire maps rows to the wire list. When two labels sanitize to the same
// profile name, NEITHER is exported: keeping one would hand `--profile prod-app` to
// whichever label happens to sort first, which may be the other account. The collision
// is reported instead so the member can rename one.
func awsProfilesWire(rows []store.SSMProfile) ([]awsProfileWire, []awsProfileConflict) {
	labels := map[string][]string{}
	byID := make(map[string]store.SSMProfile, len(rows))
	for _, p := range rows {
		byID[p.ID] = p
		n := ssmProfileName(p.Label)
		labels[n] = append(labels[n], p.Label)
	}
	out := make([]awsProfileWire, 0, len(rows))
	var conflicts []awsProfileConflict
	for _, p := range rows {
		name := ssmProfileName(p.Label)
		if ls := labels[name]; len(ls) > 1 {
			if ls[0] == p.Label {
				conflicts = append(conflicts, awsProfileConflict{Name: name, Labels: ls})
			}
			continue
		}
		if ssmKindOf(p) == store.SSMKindAssumeRole {
			// A source that is gone or is not an sso profile exports nothing: the Agent could
			// not tell a typo from a chain it must refuse, so the chained profile is left out
			// rather than written with a source_profile that names something else.
			src, ok := byID[p.SourceProfileID]
			if !ok || ssmKindOf(src) != store.SSMKindSSO {
				continue
			}
			out = append(out, awsProfileWire{
				ID: p.ID, Name: name, Label: p.Label, AccountID: p.AccountID, Region: p.Region,
				Kind: store.SSMKindAssumeRole, RoleARN: p.RoleARN, SourceProfile: ssmProfileName(src.Label),
				ExternalID: p.ExternalID, SessionName: p.SessionName, DurationSeconds: p.DurationSeconds,
				ConfirmBeforeUse: p.ConfirmUse,
			})
			continue
		}
		out = append(out, awsProfileWire{
			ID: p.ID, Name: name, Label: p.Label, StartURL: p.StartURL, SSORegion: p.SSORegion,
			AccountID: p.AccountID, RoleName: p.RoleName, Region: p.Region, ConfirmBeforeUse: p.ConfirmUse,
			SignIn: p.SignInName, SignInID: p.SignInID,
		})
	}
	return withoutSessionCollisions(out, conflicts)
}

// withoutSessionCollisions leaves out every SSO profile whose sso-session name another owner
// renders too (ADR 0125 decision 2): a sign-in named like a profile with a sign-in of its own.
// The store refuses that state, but rows an older binary wrote after a rollback can hold it.
// It holds whatever the portals: two equal portals under one name would share a login nobody
// asked to share. Members of one sign-in render one name by design and are no collision. A
// chain whose source is left out goes too.
func withoutSessionCollisions(ps []awsProfileWire, conflicts []awsProfileConflict) ([]awsProfileWire, []awsProfileConflict) {
	type owners struct {
		ids    map[string]bool
		labels []string
	}
	bySession := map[string]*owners{}
	for _, p := range ps {
		if p.Kind == store.SSMKindAssumeRole {
			continue
		}
		session, owner := p.Name, "profile:"+p.ID
		if p.SignIn != "" {
			session, owner = p.SignIn, "sign-in:"+p.SignInID
		}
		o := bySession[session]
		if o == nil {
			o = &owners{ids: map[string]bool{}}
			bySession[session] = o
		}
		o.ids[owner] = true
		o.labels = append(o.labels, p.Label)
	}
	drop := map[string]bool{}
	for session, o := range bySession {
		if len(o.ids) > 1 {
			drop[session] = true
			conflicts = append(conflicts, awsProfileConflict{Name: session, Labels: o.labels})
		}
	}
	if len(drop) == 0 {
		return ps, conflicts
	}
	gone := map[string]bool{}
	out := ps[:0:0]
	for _, p := range ps {
		session := p.Name
		if p.SignIn != "" {
			session = p.SignIn
		}
		if p.Kind != store.SSMKindAssumeRole && drop[session] {
			gone[p.Name] = true
			continue
		}
		out = append(out, p)
	}
	final := out[:0:0]
	for _, p := range out {
		if p.Kind == store.SSMKindAssumeRole && gone[p.SourceProfile] {
			continue
		}
		final = append(final, p)
	}
	sort.Slice(conflicts, func(i, j int) bool { return conflicts[i].Name < conflicts[j].Name })
	return final, conflicts
}
