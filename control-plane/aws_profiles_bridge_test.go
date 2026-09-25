package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func awsProfilesCall(mgr *manager, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/internal/aws-profiles", nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	newAWSProfilesBridgeAPI(mgr).list(w, r)
	return w
}

func seedSSMProfile(t *testing.T, st *store.SQL, membershipID, label string) {
	t.Helper()
	err := st.CreateSSMProfile(context.Background(), store.SSMProfile{
		ID: store.NewID(), MembershipID: membershipID, Label: label,
		StartURL: "https://example.awsapps.com/start", SSORegion: "ap-northeast-1",
		AccountID: "123456789012", RoleName: "Dev", Region: "us-west-2", CreatedAt: store.NowTS(),
	})
	if err != nil {
		t.Fatalf("seed profile: %v", err)
	}
}

// The member receives their own profiles under the same names an SSM session uses, and
// a second label that sanitizes to an existing name is left out rather than emitted as a
// duplicate section.
func TestAWSProfilesBridgeListsOwnProfilesWithSessionNames(t *testing.T) {
	st, mgr, mv := bridgeEnv(t)
	seedSSMProfile(t, st, mv.MembershipID, "prod app")
	seedSSMProfile(t, st, mv.MembershipID, "prod-app")
	seedSSMProfile(t, st, mv.MembershipID, "sandbox")

	tok := mintAWSProfilesToken(awsProfilesSignKey(mgr.tokenSignMaster()), mv.MembershipID)
	w := awsProfilesCall(mgr, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	var got struct {
		Profiles []awsProfileWire `json:"profiles"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var names []string
	for _, p := range got.Profiles {
		names = append(names, p.Name)
	}
	if strings.Join(names, ",") != "prod-app,sandbox" {
		t.Fatalf("names = %v, want [prod-app sandbox]", names)
	}
	p := got.Profiles[1]
	if p.StartURL == "" || p.SSORegion != "ap-northeast-1" || p.AccountID != "123456789012" || p.RoleName != "Dev" || p.Region != "us-west-2" {
		t.Fatalf("profile fields not carried: %+v", p)
	}
}

// A forged or foreign-prefixed token is refused outright.
func TestAWSProfilesBridgeRefusesBadTokens(t *testing.T) {
	st, mgr, mv := bridgeEnv(t)
	seedSSMProfile(t, st, mv.MembershipID, "mine")

	for name, tok := range map[string]string{
		"missing":      "",
		"forged tag":   mintAWSProfilesToken([]byte("wrong-key"), mv.MembershipID),
		"other bridge": mintDocsToken(docsSignKey(mgr.tokenSignMaster()), mv.MembershipID),
		"unknown":      mintAWSProfilesToken(awsProfilesSignKey(mgr.tokenSignMaster()), "no-such-membership"),
	} {
		if w := awsProfilesCall(mgr, tok); w.Code != http.StatusUnauthorized {
			t.Errorf("%s: code = %d, want 401", name, w.Code)
		}
	}
}

// Another member's token reads that member's list and never this one's, and a
// deactivated membership stops receiving anything on its next pull.
func TestAWSProfilesBridgeIsScopedToTheLiveMembership(t *testing.T) {
	ctx := context.Background()
	st, mgr, mv := bridgeEnv(t)
	seedSSMProfile(t, st, mv.MembershipID, "mine")
	other, _ := st.UpsertIdentity(ctx, "other@sub.co.jp", "other-sub-co-jp", "")
	om, err := st.EnsureMembership(ctx, other.ID, mv.TenantID, "member")
	if err != nil {
		t.Fatalf("membership: %v", err)
	}
	seedSSMProfile(t, st, om.ID, "theirs")

	key := awsProfilesSignKey(mgr.tokenSignMaster())
	w := awsProfilesCall(mgr, mintAWSProfilesToken(key, om.ID))
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"mine"`) || !strings.Contains(w.Body.String(), `"theirs"`) {
		t.Fatalf("other member's pull: %d %s", w.Code, w.Body.String())
	}

	if err := st.SetMembershipStatus(ctx, mv.MembershipID, "inactive"); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if w := awsProfilesCall(mgr, mintAWSProfilesToken(key, mv.MembershipID)); w.Code != http.StatusUnauthorized {
		t.Fatalf("deactivated membership: code = %d, want 401 (%s)", w.Code, w.Body.String())
	}
}

// The bridge fails silently if the token never reaches the container: the agent just
// sees "bridge off" and exports nothing. Pin the injection next to the other bridges.
func TestWorkspaceEnvCarriesTheAWSProfilesBridgeToken(t *testing.T) {
	_, mgr, mv := bridgeEnv(t)
	mgr.dataRoot = t.TempDir()
	mgr.publicBaseURL = "https://af.example"

	ws := store.Workspace{ID: "ws1", TenantID: mv.TenantID, MembershipID: mv.MembershipID}
	var token string
	for _, kv := range mgr.workspaceExtraEnv(context.Background(), ws) {
		if v, ok := strings.CutPrefix(kv, "AF_AWS_PROFILES_TOKEN="); ok {
			token = v
		}
	}
	mid, ok := verifyAWSProfilesToken(awsProfilesSignKey(mgr.tokenSignMaster()), token)
	if !ok || mid != mv.MembershipID {
		t.Fatalf("injected token %q does not verify to this membership: (%q,%v)", token, mid, ok)
	}
}
