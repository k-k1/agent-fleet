package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/pgtest"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// ssmAPIStores opens SQLite always and Postgres, in a schema of its own, when
// AF_TEST_DATABASE_URL is set.
func ssmAPIStores(t *testing.T) map[string]*store.SQL {
	t.Helper()
	ctx := context.Background()
	lite, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { lite.Close() })
	out := map[string]*store.SQL{"sqlite": lite}
	if url, ok := pgtest.Schema(t); ok {
		pg, err := store.OpenPostgres(url)
		if err != nil {
			t.Fatalf("open postgres schema: %v", err)
		}
		t.Cleanup(func() { pg.Close() })
		out["postgres"] = pg
	}
	for name, st := range out {
		if err := st.Migrate(ctx); err != nil {
			t.Fatalf("migrate %s: %v", name, err)
		}
	}
	return out
}

type ssmAPIEnv struct {
	mux *http.ServeMux
}

func newSSMAPIEnv(t *testing.T, st *store.SQL) *ssmAPIEnv {
	t.Helper()
	ctx := context.Background()
	dflt, err := st.EnsureDefaultTenant(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ident, err := st.UpsertIdentity(ctx, "u@x", "u-x", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsureMembership(ctx, ident.ID, dflt.ID, "member"); err != nil {
		t.Fatal(err)
	}
	api := newSSMConfigAPI(&manager{store: st, authMode: "proxy", emailHeader: "X-Forwarded-Email", dataRoot: t.TempDir()})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/ssm/profiles", api.withMembership(api.createProfile))
	mux.HandleFunc("DELETE /api/ssm/profiles/{id}", api.withMembership(api.deleteProfile))
	mux.HandleFunc("POST /api/ssm/hosts", api.withMembership(api.createHost))
	mux.HandleFunc("PUT /api/ssm/hosts/{id}", api.withMembership(api.updateHost))
	return &ssmAPIEnv{mux: mux}
}

func (e *ssmAPIEnv) do(t *testing.T, method, path string, body any, out any) int {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	r := httptest.NewRequest(method, path, rd)
	r.Header.Set("X-Forwarded-Email", "u@x")
	r.Header.Set("X-AF-Tenant", "default")
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, r)
	if out != nil {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatalf("%s %s: decode %q: %v", method, path, w.Body.String(), err)
		}
	}
	return w.Code
}

// DELETE /api/ssm/profiles/{id} answers 409 ssm_profile_in_use, naming the hosts, while
// any host uses the profile — the Console reads both the code and the list. Host writes
// that name a missing profile are 400 bad_profile.
func TestSSMProfileDeleteAPI(t *testing.T) {
	for name, st := range ssmAPIStores(t) {
		t.Run(name, func(t *testing.T) {
			e := newSSMAPIEnv(t, st)
			var p, spare ssmProfileDTO
			if c := e.do(t, "POST", "/api/ssm/profiles", ssmProfileDTO{Label: "prod", StartURL: "https://example.awsapps.com/start", SSORegion: "us-east-1"}, &p); c != http.StatusCreated {
				t.Fatalf("create profile: %d", c)
			}
			if c := e.do(t, "POST", "/api/ssm/profiles", ssmProfileDTO{Label: "spare", StartURL: "https://example.awsapps.com/start", SSORegion: "us-east-1"}, &spare); c != http.StatusCreated {
				t.Fatalf("create profile: %d", c)
			}
			var web, db ssmHostDTO
			for _, x := range []struct {
				alias string
				out   *ssmHostDTO
			}{{"web", &web}, {"db", &db}} {
				if c := e.do(t, "POST", "/api/ssm/hosts", ssmHostDTO{Alias: x.alias, InstanceID: "i-1", ProfileID: p.ID}, x.out); c != http.StatusCreated {
					t.Fatalf("create host %s: %d", x.alias, c)
				}
			}

			var refused ssmProfileInUseResp
			if c := e.do(t, "DELETE", "/api/ssm/profiles/"+p.ID, nil, &refused); c != http.StatusConflict {
				t.Fatalf("delete of a used profile: %d, want 409", c)
			}
			if refused.Error.Code != "ssm_profile_in_use" || strings.Join(refused.Hosts, ",") != "db,web" {
				t.Fatalf("refusal = %+v", refused)
			}
			if !strings.Contains(refused.Error.Message, "db, web") {
				t.Fatalf("message does not name the hosts: %q", refused.Error.Message)
			}

			// Moving a host onto a profile that does not exist is refused, not stored.
			var bad struct{ Error struct{ Code string } }
			move := web
			move.ProfileID = "gone"
			if c := e.do(t, "PUT", "/api/ssm/hosts/"+web.ID, move, &bad); c != http.StatusBadRequest || bad.Error.Code != "bad_profile" {
				t.Fatalf("update onto a missing profile: %d %+v", c, bad)
			}

			for _, h := range []ssmHostDTO{web, db} {
				h.ProfileID = spare.ID
				if c := e.do(t, "PUT", "/api/ssm/hosts/"+h.ID, h, nil); c != http.StatusOK {
					t.Fatalf("repoint %s: %d", h.Alias, c)
				}
			}
			if c := e.do(t, "DELETE", "/api/ssm/profiles/"+p.ID, nil, nil); c != http.StatusNoContent {
				t.Fatalf("delete of an unused profile: %d", c)
			}
			if c := e.do(t, "POST", "/api/ssm/hosts", ssmHostDTO{Alias: "late", InstanceID: "i-2", ProfileID: p.ID}, &bad); c != http.StatusBadRequest || bad.Error.Code != "bad_profile" {
				t.Fatalf("create on a deleted profile: %d %+v", c, bad)
			}
		})
	}
}
