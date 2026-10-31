package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Shared sign-ins (ADR 0125), on SQLite and on Postgres.

const otherStart = "https://other.awsapps.com/start"

func strp(s string) *string { return &s }

// signInEnv is newSSMAPIEnv plus the sign-in routes, and the store and member to look at.
type signInEnv struct {
	*ssmAPIEnv
	st  *store.SQL
	mid string
}

func newSignInEnv(t *testing.T, st *store.SQL) signInEnv {
	t.Helper()
	e := newSSMAPIEnv(t, st)
	api := newSSMConfigAPI(&manager{store: st, authMode: "proxy", emailHeader: "X-Forwarded-Email", dataRoot: t.TempDir()})
	e.mux.HandleFunc("GET /api/ssm/sign-ins", api.withMembership(api.listSignIns))
	e.mux.HandleFunc("PUT /api/ssm/sign-ins/{id}", api.withMembership(api.updateSignIn))
	ctx := context.Background()
	ident, _ := st.UpsertIdentity(ctx, "u@x", "u-x", "")
	dflt, _ := st.EnsureDefaultTenant(ctx)
	mem, err := st.EnsureMembership(ctx, ident.ID, dflt.ID, "member")
	if err != nil {
		t.Fatal(err)
	}
	return signInEnv{e, st, mem.ID}
}

func (e signInEnv) create(t *testing.T, label string, signIn *string, start, region string) (ssmProfileDTO, int) {
	t.Helper()
	var out ssmProfileDTO
	c := e.do(t, "POST", "/api/ssm/profiles", ssmProfileDTO{Label: label, StartURL: start, SSORegion: region,
		AccountID: "123456789012", RoleName: "Dev", SignIn: signIn}, &out)
	return out, c
}

func (e signInEnv) signIns(t *testing.T) []ssmSignInDTO {
	t.Helper()
	var out []ssmSignInDTO
	if c := e.do(t, "GET", "/api/ssm/sign-ins", nil, &out); c != http.StatusOK {
		t.Fatalf("list sign-ins = %d", c)
	}
	return out
}

func signInErr(t *testing.T, e signInEnv, method, path string, body any) (int, string) {
	t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(b))
	r.Header.Set("X-Forwarded-Email", "u@x")
	r.Header.Set("X-AF-Tenant", "default")
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, r)
	var doc struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	json.Unmarshal(w.Body.Bytes(), &doc)
	return w.Code, doc.Error.Code
}

func TestSSMSignInAPI(t *testing.T) {
	for name, st := range ssmAPIStores(t) {
		t.Run(name, func(t *testing.T) {
			e := newSignInEnv(t, st)
			// Create a sign-in from the first profile's portal; a second joins it without one.
			a, c := e.create(t, "prod", strp("corp"), chainSSOStart, "us-east-1")
			if c != http.StatusCreated || a.SignIn == nil || *a.SignIn != "corp" {
				t.Fatalf("create with a new sign-in = %d %+v", c, a)
			}
			b, c := e.create(t, "stg", strp("corp"), "", "")
			if c != http.StatusCreated || b.StartURL != chainSSOStart || b.SSORegion != "us-east-1" {
				t.Fatalf("join without a portal = %d %+v (the sign-in's portal is copied)", c, b)
			}
			// Another portal under that name is refused.
			if c, code := signInErr(t, e, "POST", "/api/ssm/profiles", ssmProfileDTO{Label: "x", StartURL: otherStart, SSORegion: "us-east-1",
				AccountID: "1", RoleName: "R", SignIn: strp("corp")}); c != http.StatusConflict || code != "sign_in_mismatch" {
				t.Fatalf("mismatched portal = %d %s", c, code)
			}
			// A sign-in may not take the name of a profile with a sign-in of its own...
			e.create(t, "solo", nil, otherStart, "us-east-1")
			if c, code := signInErr(t, e, "POST", "/api/ssm/profiles", ssmProfileDTO{Label: "y", StartURL: otherStart, SSORegion: "us-east-1",
				AccountID: "1", RoleName: "R", SignIn: strp("solo")}); c != http.StatusConflict || code != "sign_in_name_taken" {
				t.Fatalf("sign-in named like an ungrouped profile = %d %s", c, code)
			}
			// ...nor a new ungrouped profile the name of a sign-in.
			if c, code := signInErr(t, e, "POST", "/api/ssm/profiles", ssmProfileDTO{Label: "corp", StartURL: otherStart, SSORegion: "us-east-1",
				AccountID: "1", RoleName: "R"}); c != http.StatusConflict || code != "sign_in_name_taken" {
				t.Fatalf("profile named like a sign-in = %d %s", c, code)
			}
			// ...but it may take the name of one of its own members: the member's login stays.
			own, c := e.create(t, "keep", strp("keep"), otherStart, "us-east-1")
			if c != http.StatusCreated || own.SignIn == nil || *own.SignIn != "keep" {
				t.Fatalf("a sign-in named after its own member = %d", c)
			}
			// While another member keeps the sign-in, that member cannot leave under the same name.
			// (As the last member it could: the sign-in goes with it.)
			if _, c := e.create(t, "keep-2", strp("keep"), "", ""); c != http.StatusCreated {
				t.Fatalf("second member = %d", c)
			}
			if c, code := signInErr(t, e, "PUT", "/api/ssm/profiles/"+own.ID, ssmProfileDTO{Label: "keep", StartURL: otherStart,
				SSORegion: "us-east-1", AccountID: "1", RoleName: "R", SignIn: strp("")}); c != http.StatusConflict || code != "sign_in_name_taken" {
				t.Fatalf("leaving a sign-in of its own name = %d %s", c, code)
			}
			// An older Console's PUT (no signIn) keeps the profile in its sign-in.
			old := map[string]any{"label": "stg", "startUrl": chainSSOStart, "ssoRegion": "us-east-1", "accountId": "1", "roleName": "R", "region": "eu-west-1"}
			var got ssmProfileDTO
			if c := e.do(t, "PUT", "/api/ssm/profiles/"+b.ID, old, &got); c != http.StatusOK || got.SignIn == nil || *got.SignIn != "corp" {
				t.Fatalf("PUT without signIn = %d %+v", c, got)
			}
			// ...and refuses another portal there.
			old["startUrl"] = otherStart
			if c, code := signInErr(t, e, "PUT", "/api/ssm/profiles/"+b.ID, old); c != http.StatusConflict || code != "sign_in_mismatch" {
				t.Fatalf("older PUT with another portal = %d %s", c, code)
			}
			// An assume-role profile has no sign-in.
			if c, code := signInErr(t, e, "POST", "/api/ssm/profiles", ssmProfileDTO{Kind: "assume_role", Label: "deploy", SourceProfileID: a.ID,
				RoleARN: chainRoleARN, SignIn: strp("corp")}); c != http.StatusBadRequest || code != "bad_sign_in" {
				t.Fatalf("assume_role with a sign-in = %d %s", c, code)
			}
			// The list shows the members; a rename and a portal change rewrite their copies.
			var corp ssmSignInDTO
			for _, si := range e.signIns(t) {
				if si.Name == "corp" {
					corp = si
				}
			}
			if len(corp.Profiles) != 2 {
				t.Fatalf("corp members = %v", corp.Profiles)
			}
			if c := e.do(t, "PUT", "/api/ssm/sign-ins/"+corp.ID, ssmSignInDTO{Name: "corp2", StartURL: otherStart, SSORegion: "eu-west-1"}, nil); c != http.StatusOK {
				t.Fatalf("rename = %d", c)
			}
			p, _, _ := st.GetSSMProfile(context.Background(), a.ID)
			if p.SignInName != "corp2" || p.StartURL != otherStart || p.SSORegion != "eu-west-1" {
				t.Fatalf("member after the sign-in's edit = %+v", p)
			}
			if c, code := signInErr(t, e, "PUT", "/api/ssm/sign-ins/"+corp.ID, ssmSignInDTO{Name: "solo", StartURL: otherStart, SSORegion: "eu-west-1"}); c != http.StatusConflict || code != "sign_in_name_taken" {
				t.Fatalf("rename onto an ungrouped profile's name = %d %s", c, code)
			}
			if c, code := signInErr(t, e, "PUT", "/api/ssm/sign-ins/"+corp.ID, ssmSignInDTO{Name: "keep", StartURL: otherStart, SSORegion: "eu-west-1"}); c != http.StatusConflict || code != "sign_in_exists" {
				t.Fatalf("rename onto another sign-in = %d %s", c, code)
			}
			// The last member leaving, or being deleted, takes the sign-in with it.
			if c := e.do(t, "PUT", "/api/ssm/profiles/"+a.ID, map[string]any{"label": "prod", "startUrl": otherStart, "ssoRegion": "eu-west-1",
				"accountId": "1", "roleName": "R", "signIn": ""}, nil); c != http.StatusOK {
				t.Fatalf("leave = %d", c)
			}
			if c := e.do(t, "DELETE", "/api/ssm/profiles/"+b.ID, nil, nil); c != http.StatusNoContent {
				t.Fatalf("delete = %d", c)
			}
			for _, si := range e.signIns(t) {
				if si.ID == corp.ID {
					t.Fatalf("an empty sign-in stayed: %+v", si)
				}
			}
		})
	}
}

// Concurrent writers can never leave a sign-in named like an ungrouped profile, a profile
// pointing at a sign-in that is gone, or an empty sign-in; each pair ends in one success and
// one refusal, or two consistent successes.
func TestSSMSignInConcurrentWrites(t *testing.T) {
	for name, st := range ssmAPIStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			e := newSignInEnv(t, st)
			for i := 0; i < 15; i++ {
				n := fmt.Sprintf("race%d", i)
				var wg sync.WaitGroup
				codes := make([]int, 2)
				wg.Add(2)
				go func() { defer wg.Done(); _, codes[0] = e.create(t, n+"-member", strp(n), chainSSOStart, "us-east-1") }()
				go func() { defer wg.Done(); _, codes[1] = e.create(t, n, nil, chainSSOStart, "us-east-1") }()
				wg.Wait()
				if codes[0] == http.StatusCreated && codes[1] == http.StatusCreated {
					t.Fatalf("round %d: a sign-in and an ungrouped profile of one name were both created", i)
				}

				// The last member leaving while another joins.
				m, c := e.create(t, n+"-solo", strp(n+"-g"), chainSSOStart, "us-east-1")
				if c != http.StatusCreated {
					t.Fatalf("create = %d", c)
				}
				wg.Add(2)
				go func() {
					defer wg.Done()
					e.do(t, "PUT", "/api/ssm/profiles/"+m.ID, map[string]any{"label": n + "-solo", "startUrl": chainSSOStart,
						"ssoRegion": "us-east-1", "accountId": "1", "roleName": "R", "signIn": ""}, nil)
				}()
				go func() { defer wg.Done(); e.create(t, n+"-joiner", strp(n+"-g"), chainSSOStart, "us-east-1") }()
				wg.Wait()
				checkSignInState(ctx, t, st, e.mid)
			}
		})
	}
}

// checkSignInState fails on any state the store must never commit.
func checkSignInState(ctx context.Context, t *testing.T, st *store.SQL, mid string) {
	t.Helper()
	sis, _ := st.ListSSMSignIns(ctx, mid)
	ps, _ := st.ListSSMProfiles(ctx, mid)
	ids, names := map[string]bool{}, map[string]bool{}
	for _, si := range sis {
		ids[si.ID], names[si.Name] = true, true
	}
	members := map[string]int{}
	for _, p := range ps {
		if p.SignInID != "" {
			if !ids[p.SignInID] {
				t.Fatalf("profile %s points at sign-in %s, which is gone", p.Label, p.SignInID)
			}
			members[p.SignInID]++
		} else if names[store.ProfileName(p.Label)] {
			t.Fatalf("sign-in named like the ungrouped profile %s", p.Label)
		}
	}
	for _, si := range sis {
		if members[si.ID] == 0 {
			t.Fatalf("sign-in %s has no member", si.Name)
		}
	}
}

// racingSignInStore moves the profile into a sign-in right after the PUT handler has read the
// row: an older Console's PUT (no signIn) must not take it back out (the lesson of ADR 0126's
// G8, applied to sign_in_id).
type racingSignInStore struct {
	store.SSMStore
	sql   *store.SQL
	join  string
	fired bool
}

func (s *racingSignInStore) GetSSMProfile(ctx context.Context, id string) (store.SSMProfile, bool, error) {
	p, ok, err := s.SSMStore.GetSSMProfile(ctx, id)
	if err == nil && ok && !s.fired {
		s.fired = true
		other := p
		other.SignIn = store.SignInWrite{Set: true, Name: s.join}
		if werr := s.sql.UpdateSSMProfile(ctx, other); werr != nil {
			return p, ok, werr
		}
	}
	return p, ok, err
}

func TestAbsentSignInKeepsAConcurrentJoin(t *testing.T) {
	for name, st := range ssmAPIStores(t) {
		t.Run(name, func(t *testing.T) {
			e := newSignInEnv(t, st)
			e.create(t, "anchor", strp("team"), chainSSOStart, "us-east-1")
			p, c := e.create(t, "late", nil, chainSSOStart, "us-east-1")
			if c != http.StatusCreated {
				t.Fatal(c)
			}
			rs := &racingSignInStore{SSMStore: st, sql: st, join: "team"}
			api := newSSMConfigAPI(&manager{store: st, authMode: "proxy", emailHeader: "X-Forwarded-Email", dataRoot: t.TempDir()})
			api.store = rs
			mux := http.NewServeMux()
			mux.HandleFunc("PUT /api/ssm/profiles/{id}", api.withMembership(api.updateProfile))
			body, _ := json.Marshal(map[string]any{"label": "late", "startUrl": chainSSOStart, "ssoRegion": "us-east-1",
				"accountId": "123456789012", "roleName": "Dev", "region": "eu-west-1"})
			r := httptest.NewRequest("PUT", "/api/ssm/profiles/"+p.ID, bytes.NewReader(body))
			r.Header.Set("X-Forwarded-Email", "u@x")
			r.Header.Set("X-AF-Tenant", "default")
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != http.StatusOK || !rs.fired {
				t.Fatalf("put = %d (race fired %t) %s", w.Code, rs.fired, w.Body.String())
			}
			got, _, _ := st.GetSSMProfile(context.Background(), p.ID)
			if got.SignInName != "team" || got.Region != "eu-west-1" {
				t.Fatalf("stored = %+v: the concurrent join was undone, or the PUT's region lost", got)
			}
		})
	}
}

// The bridge carries the sign-in, and leaves out every profile whose sso-session name another
// owner renders, whatever the portals; members of one sign-in are no collision.
func TestAWSProfilesWireSignIns(t *testing.T) {
	rows := []store.SSMProfile{
		{ID: "a", Label: "a", StartURL: chainSSOStart, SSORegion: "us-east-1", AccountID: "1", RoleName: "R", Kind: "sso", SignInID: "g1", SignInName: "team"},
		{ID: "b", Label: "b", StartURL: chainSSOStart, SSORegion: "us-east-1", AccountID: "1", RoleName: "R", Kind: "sso", SignInID: "g1", SignInName: "team"},
		// An ungrouped profile named like the sign-in "x" (rows an older binary could leave).
		{ID: "c", Label: "x", StartURL: chainSSOStart, SSORegion: "us-east-1", AccountID: "1", RoleName: "R", Kind: "sso"},
		{ID: "d", Label: "d", StartURL: chainSSOStart, SSORegion: "us-east-1", AccountID: "1", RoleName: "R", Kind: "sso", SignInID: "g2", SignInName: "x"},
		{ID: "e", Label: "chain-on-d", Kind: "assume_role", SourceProfileID: "d", RoleARN: chainRoleARN, AccountID: "210987654321"},
	}
	ps, conflicts := awsProfilesWire(rows)
	names := []string{}
	for _, p := range ps {
		names = append(names, p.Name)
		if p.Name == "a" && (p.SignIn != "team" || p.SignInID != "g1") {
			t.Fatalf("sign-in not carried: %+v", p)
		}
	}
	if strings.Join(names, ",") != "a,b" {
		t.Fatalf("exported %v, want a,b", names)
	}
	// S1: every profile left out is in conflicts under its own name, so an Agent refuses a run
	// of it (checkAmbiguous matches the profile name); the chain on a left-out source too.
	var got []string
	for _, c := range conflicts {
		got = append(got, c.Name)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != "chain-on-d,d,x" {
		t.Fatalf("conflict names = %v, want every left-out profile", got)
	}
}

// S2: an sso-session collision is found over every SSO row, also when one side is left out
// for another reason first. Here the sign-in "g" has one member labelled "dup", whose label
// another profile shares (a label collision), and an older binary made an ungrouped "g": the
// ungrouped "g" must not be exported.
func TestAWSProfilesWireCollisionBehindALabelCollision(t *testing.T) {
	rows := []store.SSMProfile{
		{ID: "m", Label: "dup", StartURL: chainSSOStart, SSORegion: "us-east-1", AccountID: "1", RoleName: "R", Kind: "sso", SignInID: "g1", SignInName: "g"},
		{ID: "o", Label: "dup", StartURL: chainSSOStart, SSORegion: "us-east-1", AccountID: "1", RoleName: "R", Kind: "sso"},
		{ID: "u", Label: "g", StartURL: chainSSOStart, SSORegion: "us-east-1", AccountID: "1", RoleName: "R", Kind: "sso"},
	}
	ps, conflicts := awsProfilesWire(rows)
	if len(ps) != 0 {
		t.Fatalf("exported %+v, want nothing", ps)
	}
	var got []string
	for _, c := range conflicts {
		got = append(got, c.Name)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != "dup,g" {
		t.Fatalf("conflict names = %v", got)
	}
}

// S3: a profile that joins or keeps a sign-in may leave out either part of the portal (the
// sign-in supplies it); one that ends up with a sign-in of its own needs both. S4: the sign-in
// update answers with its members, as the list does.
func TestSSMSignInPartialPortalAndUpdateAnswer(t *testing.T) {
	for name, st := range ssmAPIStores(t) {
		t.Run(name, func(t *testing.T) {
			e := newSignInEnv(t, st)
			a, c := e.create(t, "a", strp("grp"), chainSSOStart, "us-east-1")
			if c != http.StatusCreated {
				t.Fatal(c)
			}
			if _, c := e.create(t, "b", strp("grp"), chainSSOStart, ""); c != http.StatusCreated {
				t.Fatalf("join with the start URL and no region = %d", c)
			}
			if _, c := e.create(t, "c", strp("grp"), "", "us-east-1"); c != http.StatusCreated {
				t.Fatalf("join with the region and no start URL = %d", c)
			}
			var got ssmProfileDTO
			if c := e.do(t, "PUT", "/api/ssm/profiles/"+a.ID, map[string]any{"label": "a", "accountId": "1", "roleName": "R", "region": "eu-west-1"}, &got); c != http.StatusOK || got.SignIn == nil || got.StartURL != chainSSOStart {
				t.Fatalf("absent signIn and no portal on a member = %d %+v", c, got)
			}
			if c, code := signInErr(t, e, "POST", "/api/ssm/profiles", ssmProfileDTO{Label: "own", SSORegion: "us-east-1", AccountID: "1", RoleName: "R"}); c != http.StatusBadRequest || code != "bad_start_url" {
				t.Fatalf("own sign-in without a start URL = %d %s", c, code)
			}
			if c, code := signInErr(t, e, "POST", "/api/ssm/profiles", ssmProfileDTO{Label: "own", StartURL: chainSSOStart, AccountID: "1", RoleName: "R"}); c != http.StatusBadRequest || code != "bad_region" {
				t.Fatalf("own sign-in without a region = %d %s", c, code)
			}
			if c, code := signInErr(t, e, "PUT", "/api/ssm/profiles/"+a.ID, map[string]any{"label": "a", "accountId": "1", "roleName": "R", "signIn": ""}); c != http.StatusBadRequest || code != "bad_start_url" {
				t.Fatalf("leaving without a portal = %d %s", c, code)
			}
			var grp ssmSignInDTO
			for _, si := range e.signIns(t) {
				if si.Name == "grp" {
					grp = si
				}
			}
			var answer ssmSignInDTO
			if c := e.do(t, "PUT", "/api/ssm/sign-ins/"+grp.ID, ssmSignInDTO{Name: "grp2", StartURL: chainSSOStart, SSORegion: "us-east-1"}, &answer); c != http.StatusOK {
				t.Fatalf("rename = %d", c)
			}
			if len(answer.Profiles) != 3 || answer.Name != "grp2" {
				t.Fatalf("update answer = %+v, want its 3 members", answer)
			}
		})
	}
}

// After a rollback, an older binary can change a member's kind or portal without knowing the
// sign-in. The reconcile at start takes such members out and deletes sign-ins left empty.
func TestReconcileSSMSignIns(t *testing.T) {
	for name, st := range ssmAPIStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			e := newSignInEnv(t, st)
			a, _ := e.create(t, "a", strp("rc"), chainSSOStart, "us-east-1")
			b, _ := e.create(t, "b", strp("rc"), chainSSOStart, "us-east-1")
			lone, _ := e.create(t, "lone", strp("rc-lone"), chainSSOStart, "us-east-1")
			// What an older binary's update writes: the portal and the kind, not sign_in_id.
			if _, err := st.DB().ExecContext(ctx, `UPDATE ssm_profile SET start_url=? WHERE id=?`, otherStart, a.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := st.DB().ExecContext(ctx, `UPDATE ssm_profile SET kind='assume_role' WHERE id=?`, lone.ID); err != nil {
				t.Fatal(err)
			}
			changes, err := st.ReconcileSSMSignIns(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(changes) != 3 {
				t.Fatalf("changes = %+v", changes)
			}
			pa, _, _ := st.GetSSMProfile(ctx, a.ID)
			pb, _, _ := st.GetSSMProfile(ctx, b.ID)
			if pa.SignInID != "" || pa.StartURL != otherStart || pb.SignInName != "rc" {
				t.Fatalf("after reconcile: a %+v, b %+v", pa, pb)
			}
			checkSignInState(ctx, t, st, e.mid)
			if again, _ := st.ReconcileSSMSignIns(ctx); len(again) != 0 {
				t.Fatalf("a second reconcile changed %+v", again)
			}
		})
	}
}

// The SSM session rewrite and the instance list carry the token's owner, and the sso-session
// of a profile in a shared sign-in.
func TestSSMSignInJSON(t *testing.T) {
	m := map[string]any{}
	ssmSignInJSON(store.SSMProfile{ID: "p1"}, m, "sso_session", "sso_owner_id")
	if m["sso_owner_id"] != "p1" || m["sso_session"] != nil {
		t.Fatalf("own sign-in = %v", m)
	}
	m = map[string]any{}
	ssmSignInJSON(store.SSMProfile{ID: "p1", SignInID: "g1", SignInName: "team"}, m, "ssoSession", "ssoOwnerId")
	if m["ssoOwnerId"] != "g1" || m["ssoSession"] != "af-team" {
		t.Fatalf("shared sign-in = %v", m)
	}
}
