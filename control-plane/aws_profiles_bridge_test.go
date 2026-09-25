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

// Another membership's token reads that membership's list, never this one's; a forged
// or foreign-prefixed token is refused outright.
func TestAWSProfilesBridgeRefusesBadTokens(t *testing.T) {
	st, mgr, mv := bridgeEnv(t)
	seedSSMProfile(t, st, mv.MembershipID, "mine")

	for name, tok := range map[string]string{
		"missing":      "",
		"forged tag":   mintAWSProfilesToken([]byte("wrong-key"), mv.MembershipID),
		"other bridge": mintDocsToken(docsSignKey(mgr.tokenSignMaster()), mv.MembershipID),
	} {
		if w := awsProfilesCall(mgr, tok); w.Code != http.StatusUnauthorized {
			t.Errorf("%s: code = %d, want 401", name, w.Code)
		}
	}

	other := mintAWSProfilesToken(awsProfilesSignKey(mgr.tokenSignMaster()), "no-such-membership")
	if w := awsProfilesCall(mgr, other); w.Code != http.StatusUnauthorized {
		t.Fatalf("unknown membership: code = %d, want 401", w.Code)
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
