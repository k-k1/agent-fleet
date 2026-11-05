package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// The "model route" mark (ADR 0128 decision 11, option C2): the API stores it, a PUT without the
// field leaves it alone (a Console that predates the mark must not clear it), and an explicit
// false removes it. On SQLite and on Postgres.
func TestSSMProfileModelRoute(t *testing.T) {
	for name, st := range ssmAPIStores(t) {
		t.Run(name, func(t *testing.T) {
			e := newSSMAPIEnv(t, st)
			var made ssmProfileDTO
			on := true
			if c := e.do(t, "POST", "/api/ssm/profiles", ssmProfileDTO{Label: "bedrock-invoke", StartURL: chainSSOStart, SSORegion: "us-east-1",
				AccountID: "123456789012", RoleName: "BedrockInvoke", ModelRoute: &on}, &made); c != http.StatusCreated {
				t.Fatalf("create = %d", c)
			}
			if made.ModelRoute == nil || !*made.ModelRoute {
				t.Fatalf("created without the mark: %+v", made)
			}
			marked := func() bool {
				t.Helper()
				var list []map[string]any
				if c := e.do(t, "GET", "/api/ssm/profiles", nil, &list); c != http.StatusOK || len(list) != 1 {
					t.Fatalf("list = %d %v", c, list)
				}
				return list[0]["modelRoute"] == true
			}
			if !marked() {
				t.Fatal("the list does not show the mark")
			}
			old := map[string]any{"label": "bedrock-invoke", "startUrl": chainSSOStart, "ssoRegion": "us-east-1",
				"accountId": "123456789012", "roleName": "BedrockInvoke", "region": "us-east-1"}
			if c := e.do(t, "PUT", "/api/ssm/profiles/"+made.ID, old, nil); c != http.StatusOK {
				t.Fatalf("put without the field = %d", c)
			}
			if !marked() {
				t.Fatal("a PUT without modelRoute cleared the mark")
			}
			old["modelRoute"] = false
			if c := e.do(t, "PUT", "/api/ssm/profiles/"+made.ID, old, nil); c != http.StatusOK {
				t.Fatalf("put false = %d", c)
			}
			if marked() {
				t.Fatal("an explicit false kept the mark")
			}
		})
	}
}

// The bridge carries the mark to the Agent, only for a profile that has it.
func TestAWSProfilesBridgeModelRoute(t *testing.T) {
	st, mgr, mv := bridgeEnv(t)
	ctx := context.Background()
	if err := st.CreateSSMProfile(ctx, store.SSMProfile{ID: store.NewID(), MembershipID: mv.MembershipID, Label: "bedrock-invoke",
		StartURL: chainSSOStart, SSORegion: "us-east-1", AccountID: "123456789012", RoleName: "BedrockInvoke",
		CreatedAt: store.NowTS(), ModelRoute: true}); err != nil {
		t.Fatal(err)
	}
	seedSSMProfile(t, st, mv.MembershipID, "sandbox")
	tok := mintAWSProfilesToken(awsProfilesSignKey(mgr.tokenSignMaster()), mv.MembershipID)
	r := httptest.NewRequest(http.MethodGet, "/internal/aws-profiles", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	r.Header.Set(awsProfilesVersionHeader, awsProfilesVersion)
	w := httptest.NewRecorder()
	newAWSProfilesBridgeAPI(mgr).list(w, r)
	var got awsProfilesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	m := map[string]awsProfileWire{}
	for _, p := range got.Profiles {
		m[p.Name] = p
	}
	if !m["bedrock-invoke"].ModelRoute || m["sandbox"].ModelRoute {
		t.Fatalf("model route marks on the wire = %+v", m)
	}
}
