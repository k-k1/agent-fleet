package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// The name rule is the contract every lane builds against (ADR 0107 decision 1): gcloud
// refuses a configuration name outside `[a-z][a-z0-9-]*`, and the Agent and the Console
// read the name instead of recomputing it, so a drift here renames every profile.
func TestGCPProfileName(t *testing.T) {
	for _, c := range []struct{ id, label, want string }{
		{"row-1", "Prod", "prod"},
		{"row-1", "prod_app", "prod-app"},
		{"row-1", "prod.app", "prod-app"},
		{"row-1", "  Prod  App!! ", "prod-app"},
		{"row-1", "1prod", "p1prod"},
		{"row-1", "--9 lives--", "p9-lives"},
		{"row-1", "dev 本番", "dev"},
		{"row-1", "本番環境 East", "east"},
		// Normalises to nothing: p- + the first 8 hex of sha256("row-1").
		{"row-1", "本番", "p-0f719b1f"},
		{"row-1", "---", "p-0f719b1f"},
		// The Kelvin sign lowercases to an ASCII k under strings.ToLower; ASCII-only
		// folding keeps it out of the name.
		{"row-1", "K", "p-0f719b1f"},
	} {
		if got := gcpProfileName(c.id, c.label); got != c.want {
			t.Errorf("gcpProfileName(%q, %q) = %q, want %q", c.id, c.label, got, c.want)
		}
	}
	if gcpProfileName("row-1", "本番") == gcpProfileName("row-2", "開発") {
		t.Error("two Japanese-only labels on different rows share a hash name")
	}
}

type gcpAPIEnv struct{ mux *http.ServeMux }

// newGCPAPIEnv serves the Settings routes for two members of the default tenant, u@x and
// v@x, through the real membership resolution.
func newGCPAPIEnv(t *testing.T, st *store.SQL) *gcpAPIEnv {
	t.Helper()
	ctx := context.Background()
	dflt, err := st.EnsureDefaultTenant(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, email := range []string{"u@x", "v@x"} {
		ident, err := st.UpsertIdentity(ctx, email, strings.ReplaceAll(email, "@", "-"), "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.EnsureMembership(ctx, ident.ID, dflt.ID, "member"); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	registerGCPRoutes(mux, config{mgr: &manager{store: st, authMode: "proxy", emailHeader: "X-Forwarded-Email", dataRoot: t.TempDir()}})
	return &gcpAPIEnv{mux: mux}
}

func (e *gcpAPIEnv) do(t *testing.T, who, method, path, body string, out any) (int, string) {
	t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
	r.Header.Set("X-Forwarded-Email", who)
	r.Header.Set("X-AF-Tenant", "default")
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, r)
	if out != nil && w.Code < 300 {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatalf("%s %s: decode %q: %v", method, path, w.Body.String(), err)
		}
	}
	return w.Code, w.Body.String()
}

func errCode(body string) string {
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal([]byte(body), &e)
	return e.Error.Code
}

const gcpValidRow = `{"label":"Prod","loginMethod":"google","project":"my-prod-123","account":"me@example.com","region":"asia-northeast1","zone":"asia-northeast1-a","impersonateServiceAccount":"deployer@my-prod-123.iam.gserviceaccount.com"}`

func TestGCPProfilesAPICrud(t *testing.T) {
	for name, st := range ssmAPIStores(t) {
		t.Run(name, func(t *testing.T) {
			e := newGCPAPIEnv(t, st)
			var made gcpProfileDTO
			if c, b := e.do(t, "u@x", "POST", "/api/gcp/profiles", gcpValidRow, &made); c != http.StatusCreated {
				t.Fatalf("create: %d %s", c, b)
			}
			if made.ID == "" || made.Name != "prod" || made.Conflict != nil || made.LoginMethod != "google" || made.QuotaProject != "" {
				t.Fatalf("created row = %+v", made)
			}

			// A Japanese-only label still gets a usable name, from its row id.
			var ja gcpProfileDTO
			if c, b := e.do(t, "u@x", "POST", "/api/gcp/profiles", `{"label":"本番","project":"other-proj-1"}`, &ja); c != http.StatusCreated {
				t.Fatalf("create ja: %d %s", c, b)
			}
			if ja.Name != gcpProfileName(ja.ID, "本番") || !strings.HasPrefix(ja.Name, "p-") || ja.LoginMethod != "google" {
				t.Fatalf("ja row = %+v", ja)
			}

			// A second label with the same name puts BOTH rows in conflict, and the
			// answer to the create already says so.
			var twin gcpProfileDTO
			if c, b := e.do(t, "u@x", "POST", "/api/gcp/profiles", `{"label":"prod","project":"my-prod-456"}`, &twin); c != http.StatusCreated {
				t.Fatalf("create twin: %d %s", c, b)
			}
			if twin.Conflict == nil || twin.Conflict.Reason != "collision" || strings.Join(twin.Conflict.Labels, "|") != "Prod|prod" {
				t.Fatalf("twin conflict = %+v", twin.Conflict)
			}
			var list []gcpProfileDTO
			if c, b := e.do(t, "u@x", "GET", "/api/gcp/profiles", "", &list); c != http.StatusOK || len(list) != 3 {
				t.Fatalf("list: %d %s", c, b)
			}
			for _, p := range list {
				if (p.Name == "prod") != (p.Conflict != nil) {
					t.Errorf("row %q: name %q, conflict %+v", p.Label, p.Name, p.Conflict)
				}
			}
			var raw []map[string]any
			_, b := e.do(t, "u@x", "GET", "/api/gcp/profiles", "", &raw)
			for _, p := range raw {
				if _, has := p["conflict"]; has && p["name"] != "prod" {
					t.Errorf("conflict is not omitted for an exported row: %s", b)
				}
			}

			// Renaming the twin clears the conflict on both; the row sent back as the
			// list showed it (with name and conflict) is accepted.
			edit := strings.Replace(mustJSON(t, twin), `"label":"prod"`, `"label":"Prod EU"`, 1)
			var edited gcpProfileDTO
			if c, b := e.do(t, "u@x", "PUT", "/api/gcp/profiles/"+twin.ID, edit, &edited); c != http.StatusOK {
				t.Fatalf("edit: %d %s", c, b)
			}
			if edited.Name != "prod-eu" || edited.Conflict != nil || edited.ID != twin.ID {
				t.Fatalf("edited = %+v", edited)
			}

			// Another member neither sees, edits nor deletes the row.
			var theirs []gcpProfileDTO
			if _, b := e.do(t, "v@x", "GET", "/api/gcp/profiles", "", &theirs); len(theirs) != 0 {
				t.Fatalf("v@x sees u@x's rows: %s", b)
			}
			if c, _ := e.do(t, "v@x", "PUT", "/api/gcp/profiles/"+made.ID, gcpValidRow, nil); c != http.StatusNotFound {
				t.Errorf("foreign edit: %d, want 404", c)
			}
			if c, _ := e.do(t, "v@x", "DELETE", "/api/gcp/profiles/"+made.ID, "", nil); c != http.StatusNoContent {
				t.Errorf("foreign delete: %d", c)
			}
			if c, _ := e.do(t, "u@x", "DELETE", "/api/gcp/profiles/"+made.ID, "", nil); c != http.StatusNoContent {
				t.Errorf("delete: %d", c)
			}
			if _, b := e.do(t, "u@x", "GET", "/api/gcp/profiles", "", &list); len(list) != 2 {
				t.Fatalf("after delete: %s", b)
			}
		})
	}
}

// Service-account JSON keys are accepted nowhere (ADR 0107 decision 1): neither under a
// field of their own, nor pasted into a field that exists.
func TestGCPProfilesAPIRefusesKeysAndBadShapes(t *testing.T) {
	st := ssmAPIStores(t)["sqlite"]
	e := newGCPAPIEnv(t, st)
	// Built at run time: the repository's secret scan flags a PEM header written as a literal,
	// even a fake one, and that turns every branch's scan red.
	pemHead := "-----" + "BEGIN " + "PRIVATE" + " KEY-----"
	pemTail := "-----" + "END " + "PRIVATE" + " KEY-----"
	saBlob := `{\"type\": \"service_account\", \"private_` + `key\": \"` + pemHead + `\\nAAA\\n` + pemTail + `\\n\"}`
	for name, c := range map[string]struct{ body, code string }{
		"privateKey field":     {`{"label":"a","project":"my-proj-1","privateKey":"x"}`, "key_refused"},
		"credentials field":    {`{"label":"a","project":"my-proj-1","credentials":{"type":"service_account"}}`, "key_refused"},
		"keyFile field":        {`{"label":"a","project":"my-proj-1","keyFile":"/tmp/k.json"}`, "key_refused"},
		"key json in label":    {`{"label":"` + saBlob + `","project":"my-proj-1"}`, "key_refused"},
		"pem in account":       {`{"label":"a","project":"my-proj-1","account":"` + pemHead + `"}`, "key_refused"},
		"unknown field":        {`{"label":"a","project":"my-proj-1","colour":"red"}`, "bad_request"},
		"workforce":            {`{"label":"a","project":"my-proj-1","loginMethod":"workforce"}`, "bad_login_method"},
		"no label":             {`{"project":"my-proj-1"}`, "bad_label"},
		"no project":           {`{"label":"a"}`, "bad_project"},
		"project upper":        {`{"label":"a","project":"My-Proj-1"}`, "bad_project"},
		"project short":        {`{"label":"a","project":"abc"}`, "bad_project"},
		"quota project":        {`{"label":"a","project":"my-proj-1","quotaProject":"x y"}`, "bad_quota_project"},
		"account":              {`{"label":"a","project":"my-proj-1","account":"not-an-email"}`, "bad_account"},
		"sa not a sa":          {`{"label":"a","project":"my-proj-1","impersonateServiceAccount":"me@example.com"}`, "bad_service_account"},
		"sa chain":             {`{"label":"a","project":"my-proj-1","impersonateServiceAccount":"a@p.iam.gserviceaccount.com,b@p.iam.gserviceaccount.com"}`, "bad_service_account"},
		"region":               {`{"label":"a","project":"my-proj-1","region":"Tokyo"}`, "bad_region"},
		"zone":                 {`{"label":"a","project":"my-proj-1","zone":"asia-northeast1"}`, "bad_zone"},
		"not an object":        {`[1]`, "bad_request"},
		"non-string label":     {`{"label":5,"project":"my-proj-1"}`, "bad_request"},
		"label over 100 runes": {`{"label":"` + strings.Repeat("あ", 101) + `","project":"my-proj-1"}`, "bad_label"},
	} {
		code, body := e.do(t, "u@x", "POST", "/api/gcp/profiles", c.body, nil)
		if code != http.StatusBadRequest || errCode(body) != c.code {
			t.Errorf("%s: %d %s, want 400 %s", name, code, body, c.code)
		}
	}
	for name, body := range map[string]string{
		"legacy domain project": `{"label":"a","project":"example.com:my-proj-1"}`,
		"default compute sa":    `{"label":"b","project":"my-proj-1","impersonateServiceAccount":"123456789-compute@developer.gserviceaccount.com"}`,
		"label mentions keys":   `{"label":"key rotation sandbox","project":"my-proj-1"}`,
	} {
		if code, b := e.do(t, "u@x", "POST", "/api/gcp/profiles", body, nil); code != http.StatusCreated {
			t.Errorf("%s: %d %s, want 201", name, code, b)
		}
	}
	var list []gcpProfileDTO
	e.do(t, "u@x", "GET", "/api/gcp/profiles", "", &list)
	if len(list) != 3 {
		t.Fatalf("a refused write stored a row: %d rows", len(list))
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// --- bridge ------------------------------------------------------------------------

func gcpProfilesCall(mgr *manager, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/internal/gcp-profiles", nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	newGCPProfilesBridgeAPI(mgr).list(w, r)
	return w
}

func seedGCPProfile(t *testing.T, st *store.SQL, membershipID, label, project, quota string) store.GCPProfile {
	t.Helper()
	p := store.GCPProfile{
		ID: store.NewID(), MembershipID: membershipID, Label: label, LoginMethod: "google",
		Project: project, QuotaProject: quota, Account: "me@example.com", Region: "asia-northeast1",
		Zone: "asia-northeast1-b", CreatedAt: store.NowTS(), UpdatedAt: store.NowTS(),
	}
	if err := st.CreateGCPProfile(context.Background(), p); err != nil {
		t.Fatalf("seed gcp profile: %v", err)
	}
	return p
}

// The member receives their own rows under the names Settings shows; a colliding pair is
// exported for neither and reported once; the quota project on the wire is the effective
// one; a Japanese-only label arrives under its hash name.
func TestGCPProfilesBridgeListsOwnProfiles(t *testing.T) {
	st, mgr, mv := bridgeEnv(t)
	seedGCPProfile(t, st, mv.MembershipID, "Prod", "my-prod-1", "")
	seedGCPProfile(t, st, mv.MembershipID, "prod", "my-prod-2", "")
	ja := seedGCPProfile(t, st, mv.MembershipID, "本番", "my-prod-3", "billing-proj")
	dev := seedGCPProfile(t, st, mv.MembershipID, "dev", "my-dev-1", "")

	w := gcpProfilesCall(mgr, mintGCPProfilesToken(gcpProfilesSignKey(mgr.tokenSignMaster()), mv.MembershipID))
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	var got gcpProfilesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byName := map[string]gcpProfileWire{}
	for _, p := range got.Profiles {
		byName[p.Name] = p
	}
	if len(got.Profiles) != 2 || byName["dev"].ID != dev.ID || byName[gcpProfileName(ja.ID, "本番")].ID != ja.ID {
		t.Fatalf("profiles = %+v", got.Profiles)
	}
	if len(got.Conflicts) != 1 || got.Conflicts[0].Name != "prod" || strings.Join(got.Conflicts[0].Labels, "|") != "Prod|prod" {
		t.Fatalf("conflicts = %+v", got.Conflicts)
	}
	d := byName["dev"]
	if d.QuotaProject != "my-dev-1" || d.Project != "my-dev-1" || d.LoginMethod != "google" || d.Account != "me@example.com" ||
		d.Region != "asia-northeast1" || d.Zone != "asia-northeast1-b" || d.Label != "dev" {
		t.Fatalf("dev fields = %+v", d)
	}
	if _, err := time.Parse(time.RFC3339, d.UpdatedAt); err != nil {
		t.Errorf("updatedAt %q is not RFC 3339: %v", d.UpdatedAt, err)
	}
	if q := byName[gcpProfileName(ja.ID, "本番")].QuotaProject; q != "billing-proj" {
		t.Errorf("explicit quota project = %q", q)
	}
	// The wire carries exactly the contract's keys.
	var raw struct {
		Profiles  []map[string]any `json:"profiles"`
		Conflicts []map[string]any `json:"conflicts"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &raw)
	want := "account,id,impersonateServiceAccount,label,loginMethod,name,project,quotaProject,region,updatedAt,zone"
	if keys := sortedKeys(raw.Profiles[0]); keys != want {
		t.Errorf("profile keys = %s, want %s", keys, want)
	}
}

func sortedKeys(m map[string]any) string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return strings.Join(ks, ",")
}

// No rows is an empty list and an empty conflicts array, not null: the Agent prunes its
// configurations from this answer.
func TestGCPProfilesBridgeEmptyIsArrays(t *testing.T) {
	_, mgr, mv := bridgeEnv(t)
	w := gcpProfilesCall(mgr, mintGCPProfilesToken(gcpProfilesSignKey(mgr.tokenSignMaster()), mv.MembershipID))
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"profiles":[],"conflicts":[]}` {
		t.Fatalf("empty list: %d %s", w.Code, w.Body.String())
	}
}

// The two profile bridges sign under different labels: the AWS token never opens the
// Google Cloud list and the Google Cloud token never opens the AWS one, even with the
// prefix swapped so only the tag is left to decide.
func TestGCPProfilesBridgeTokenIsItsOwn(t *testing.T) {
	st, mgr, mv := bridgeEnv(t)
	seedGCPProfile(t, st, mv.MembershipID, "mine", "my-proj-1", "")
	seedSSMProfile(t, st, mv.MembershipID, "mine")
	master := mgr.tokenSignMaster()
	awsTok := mintAWSProfilesToken(awsProfilesSignKey(master), mv.MembershipID)
	gcpTok := mintGCPProfilesToken(gcpProfilesSignKey(master), mv.MembershipID)

	for name, tok := range map[string]string{
		"missing":             "",
		"aws token":           awsTok,
		"aws tag, gcp prefix": "afg_" + strings.TrimPrefix(awsTok, "afp_"),
		"forged tag":          mintGCPProfilesToken([]byte("wrong-key"), mv.MembershipID),
		"other bridge":        mintDocsToken(docsSignKey(master), mv.MembershipID),
		"unknown membership":  mintGCPProfilesToken(gcpProfilesSignKey(master), "no-such-membership"),
	} {
		if w := gcpProfilesCall(mgr, tok); w.Code != http.StatusUnauthorized {
			t.Errorf("gcp bridge, %s: code = %d, want 401", name, w.Code)
		}
	}
	for name, tok := range map[string]string{
		"gcp token":           gcpTok,
		"gcp tag, aws prefix": "afp_" + strings.TrimPrefix(gcpTok, "afg_"),
	} {
		if w := awsProfilesCall(mgr, tok); w.Code != http.StatusUnauthorized {
			t.Errorf("aws bridge, %s: code = %d, want 401", name, w.Code)
		}
	}
	if w := gcpProfilesCall(mgr, gcpTok); w.Code != http.StatusOK {
		t.Fatalf("control: own token = %d %s", w.Code, w.Body.String())
	}
}

// Another member's token reads that member's list only; a deactivated and a deleted
// membership stop receiving anything on their next pull.
func TestGCPProfilesBridgeIsScopedToTheLiveMembership(t *testing.T) {
	ctx := context.Background()
	st, mgr, mv := bridgeEnv(t)
	seedGCPProfile(t, st, mv.MembershipID, "mine", "my-proj-1", "")
	other, _ := st.UpsertIdentity(ctx, "other@sub.co.jp", "other-sub-co-jp", "")
	om, err := st.EnsureMembership(ctx, other.ID, mv.TenantID, "member")
	if err != nil {
		t.Fatalf("membership: %v", err)
	}
	seedGCPProfile(t, st, om.ID, "theirs", "their-proj-1", "")
	third, _ := st.UpsertIdentity(ctx, "third@sub.co.jp", "third-sub-co-jp", "")
	tm, err := st.EnsureMembership(ctx, third.ID, mv.TenantID, "member")
	if err != nil {
		t.Fatalf("membership: %v", err)
	}

	key := gcpProfilesSignKey(mgr.tokenSignMaster())
	w := gcpProfilesCall(mgr, mintGCPProfilesToken(key, om.ID))
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"mine"`) || !strings.Contains(w.Body.String(), `"theirs"`) {
		t.Fatalf("other member's pull: %d %s", w.Code, w.Body.String())
	}

	if err := st.SetMembershipStatus(ctx, mv.MembershipID, "inactive"); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if w := gcpProfilesCall(mgr, mintGCPProfilesToken(key, mv.MembershipID)); w.Code != http.StatusUnauthorized {
		t.Fatalf("deactivated membership: code = %d, want 401 (%s)", w.Code, w.Body.String())
	}
	if w := gcpProfilesCall(mgr, mintGCPProfilesToken(key, tm.ID)); w.Code != http.StatusOK {
		t.Fatalf("control: third member before removal = %d", w.Code)
	}
	if err := st.DeleteMembership(ctx, tm.ID); err != nil {
		t.Fatalf("delete membership: %v", err)
	}
	if w := gcpProfilesCall(mgr, mintGCPProfilesToken(key, tm.ID)); w.Code != http.StatusUnauthorized {
		t.Fatalf("removed membership: code = %d, want 401 (%s)", w.Code, w.Body.String())
	}
}

// Deleting a membership deletes its profiles too: nothing else ever would.
func TestDeleteMembershipRemovesGCPProfiles(t *testing.T) {
	ctx := context.Background()
	st, _, mv := bridgeEnv(t)
	seedGCPProfile(t, st, mv.MembershipID, "mine", "my-proj-1", "")
	if err := st.DeleteMembership(ctx, mv.MembershipID); err != nil {
		t.Fatalf("delete membership: %v", err)
	}
	rows, err := st.ListGCPProfiles(ctx, mv.MembershipID)
	if err != nil || len(rows) != 0 {
		t.Fatalf("profiles after membership delete = %v (%v)", rows, err)
	}
}

// The token has to reach the container, or the Agent just sees "bridge off".
func TestWorkspaceEnvCarriesTheGCPProfilesBridgeToken(t *testing.T) {
	_, mgr, mv := bridgeEnv(t)
	mgr.dataRoot = t.TempDir()
	mgr.publicBaseURL = "https://af.example"

	ws := store.Workspace{ID: "ws1", TenantID: mv.TenantID, MembershipID: mv.MembershipID}
	var token string
	for _, kv := range mgr.workspaceExtraEnv(context.Background(), ws) {
		if v, ok := strings.CutPrefix(kv, "AF_GCP_PROFILES_TOKEN="); ok {
			token = v
		}
	}
	mid, ok := verifyGCPProfilesToken(gcpProfilesSignKey(mgr.tokenSignMaster()), token)
	if !ok || mid != mv.MembershipID {
		t.Fatalf("injected token %q does not verify to this membership: (%q,%v)", token, mid, ok)
	}
}
