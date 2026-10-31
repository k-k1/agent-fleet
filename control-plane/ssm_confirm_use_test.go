package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// "Confirm before use" (ADR 0126 decisions 4 and 8): the API stores it, a PUT without the field
// leaves it alone (an older Console editing a gated profile must not remove the gate), and an
// explicit false removes it. On SQLite and on Postgres.
func TestSSMProfileConfirmUse(t *testing.T) {
	for name, st := range ssmAPIStores(t) {
		t.Run(name, func(t *testing.T) {
			e := newSSMAPIEnv(t, st)
			var made ssmProfileDTO
			on := true
			if c := e.do(t, "POST", "/api/ssm/profiles", ssmProfileDTO{Label: "prod-admin", StartURL: chainSSOStart, SSORegion: "us-east-1",
				AccountID: "123456789012", RoleName: "Admin", ConfirmBeforeUse: &on}, &made); c != http.StatusCreated {
				t.Fatalf("create = %d", c)
			}
			if made.ConfirmBeforeUse == nil || !*made.ConfirmBeforeUse {
				t.Fatalf("created without the gate: %+v", made)
			}
			gated := func() bool {
				t.Helper()
				var list []map[string]any
				if c := e.do(t, "GET", "/api/ssm/profiles", nil, &list); c != http.StatusOK || len(list) != 1 {
					t.Fatalf("list = %d %v", c, list)
				}
				return list[0]["confirmBeforeUse"] == true
			}
			if !gated() {
				t.Fatal("the list does not show the gate")
			}
			// An older Console: the whole profile, without the field.
			old := map[string]any{"label": "prod-admin", "startUrl": chainSSOStart, "ssoRegion": "us-east-1",
				"accountId": "123456789012", "roleName": "Admin", "region": "eu-west-1"}
			if c := e.do(t, "PUT", "/api/ssm/profiles/"+made.ID, old, nil); c != http.StatusOK {
				t.Fatalf("put without the field = %d", c)
			}
			if !gated() {
				t.Fatal("a PUT without confirmBeforeUse removed the gate")
			}
			old["confirmBeforeUse"] = false
			if c := e.do(t, "PUT", "/api/ssm/profiles/"+made.ID, old, nil); c != http.StatusOK {
				t.Fatalf("put false = %d", c)
			}
			if gated() {
				t.Fatal("an explicit false kept the gate")
			}
		})
	}
}

// The bridge carries the profile id and the gate to an Agent that says it understands them,
// and answers with its version. An older Agent, which would write a gated profile to
// ~/.aws/config ungated, gets neither the gated profile nor a chain whose source is gated.
func TestAWSProfilesBridgeGate(t *testing.T) {
	st, mgr, mv := bridgeEnv(t)
	ctx := context.Background()
	admin := store.SSMProfile{ID: store.NewID(), MembershipID: mv.MembershipID, Label: "prod-admin", StartURL: chainSSOStart,
		SSORegion: "us-east-1", AccountID: "123456789012", RoleName: "Admin", CreatedAt: store.NowTS(), ConfirmUse: true}
	if err := st.CreateSSMProfile(ctx, admin); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSSMProfile(ctx, store.SSMProfile{ID: store.NewID(), MembershipID: mv.MembershipID, Label: "deploy",
		Kind: store.SSMKindAssumeRole, SourceProfileID: admin.ID, RoleARN: chainRoleARN, AccountID: "210987654321", CreatedAt: store.NowTS()}); err != nil {
		t.Fatal(err)
	}
	seedSSMProfile(t, st, mv.MembershipID, "sandbox")
	tok := mintAWSProfilesToken(awsProfilesSignKey(mgr.tokenSignMaster()), mv.MembershipID)

	call := func(version string) (map[string]awsProfileWire, string) {
		r := httptest.NewRequest(http.MethodGet, "/internal/aws-profiles", nil)
		r.Header.Set("Authorization", "Bearer "+tok)
		if version != "" {
			r.Header.Set(awsProfilesVersionHeader, version)
		}
		w := httptest.NewRecorder()
		newAWSProfilesBridgeAPI(mgr).list(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("list: %d %s", w.Code, w.Body.String())
		}
		var got awsProfilesResponse
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		m := map[string]awsProfileWire{}
		for _, p := range got.Profiles {
			m[p.Name] = p
		}
		return m, w.Header().Get(awsProfilesVersionHeader)
	}

	m, v := call(awsProfilesVersion)
	if v != awsProfilesVersion {
		t.Fatalf("answer version = %q", v)
	}
	if p := m["prod-admin"]; !p.ConfirmBeforeUse || p.ID != admin.ID {
		t.Fatalf("gated profile = %+v", p)
	}
	if _, ok := m["deploy"]; !ok || m["sandbox"].ID == "" || m["sandbox"].ConfirmBeforeUse {
		t.Fatalf("versioned list = %+v", m)
	}

	m, _ = call("")
	if _, ok := m["prod-admin"]; ok {
		t.Fatal("an older Agent got the gated profile")
	}
	if _, ok := m["deploy"]; ok {
		t.Fatal("an older Agent got a chain whose source is gated")
	}
	if _, ok := m["sandbox"]; !ok {
		t.Fatal("an older Agent lost an ungated profile")
	}
}

// racingSSMStore commits another write of the same profile's gate right after the PUT handler has
// read the row, before its UPDATE: the order two concurrent PUTs can take.
type racingSSMStore struct {
	store.SSMStore
	sql   *store.SQL
	flip  *bool // the value the other write commits; nil = no race
	fired bool
}

func (s *racingSSMStore) GetSSMProfile(ctx context.Context, id string) (store.SSMProfile, bool, error) {
	p, ok, err := s.SSMStore.GetSSMProfile(ctx, id)
	if err == nil && ok && s.flip != nil && !s.fired {
		s.fired = true
		other := p
		other.ConfirmUse = *s.flip
		if werr := s.sql.UpdateSSMProfile(ctx, other); werr != nil {
			return p, ok, werr
		}
	}
	return p, ok, err
}

// G8: a PUT without confirmBeforeUse never undoes a gate change another write committed after
// it read the row, enabling or disabling, on SQLite and on Postgres; its answer shows the row as
// committed.
func TestAbsentConfirmKeepsAConcurrentChange(t *testing.T) {
	for name, st := range ssmAPIStores(t) {
		for _, to := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/to-%t", name, to), func(t *testing.T) {
				ctx := context.Background()
				e := newSSMAPIEnv(t, st)
				var made ssmProfileDTO
				from := !to
				if c := e.do(t, "POST", "/api/ssm/profiles", ssmProfileDTO{Label: fmt.Sprintf("p-%s-%t", name, to), StartURL: chainSSOStart,
					SSORegion: "us-east-1", AccountID: "123456789012", RoleName: "Admin", ConfirmBeforeUse: &from}, &made); c != http.StatusCreated {
					t.Fatalf("create = %d", c)
				}
				rs := &racingSSMStore{SSMStore: st, sql: st, flip: &to}
				api := newSSMConfigAPI(&manager{store: st, authMode: "proxy", emailHeader: "X-Forwarded-Email", dataRoot: t.TempDir()})
				api.store = rs
				mux := http.NewServeMux()
				mux.HandleFunc("PUT /api/ssm/profiles/{id}", api.withMembership(api.updateProfile))
				body, _ := json.Marshal(map[string]any{"label": made.Label, "startUrl": chainSSOStart, "ssoRegion": "us-east-1",
					"accountId": "123456789012", "roleName": "Admin", "region": "eu-west-1"})
				r := httptest.NewRequest("PUT", "/api/ssm/profiles/"+made.ID, bytes.NewReader(body))
				r.Header.Set("X-Forwarded-Email", "u@x")
				r.Header.Set("X-AF-Tenant", "default")
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, r)
				if w.Code != http.StatusOK || !rs.fired {
					t.Fatalf("put = %d (race fired %t) %s", w.Code, rs.fired, w.Body.String())
				}
				got, _, err := st.GetSSMProfile(ctx, made.ID)
				if err != nil || got.ConfirmUse != to || got.Region != "eu-west-1" {
					t.Fatalf("stored = %+v, %v; want the concurrent %t kept and the PUT's region", got, err, to)
				}
				var answer ssmProfileDTO
				json.Unmarshal(w.Body.Bytes(), &answer)
				if (answer.ConfirmBeforeUse != nil && *answer.ConfirmBeforeUse) != to {
					t.Fatalf("answer = %+v, want confirmBeforeUse %t as committed", answer.ConfirmBeforeUse, to)
				}
			})
		}
	}
}
