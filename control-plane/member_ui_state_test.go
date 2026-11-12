package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// emptyUIState is the answer for a member who never saved anything in a tenant with no defaults.
const emptyUIState = `{"defaults":{"agent":"","launch":{"agent":"","model":"","driver":"","permission_mode":"","budget_usd":null},"repos":[],"ops":{"grafana_url":"","aws_sso_start_url":"","aws_account_id":"","aws_role_name":""}},"effective":{"launch":{"agent":"","model":"","driver":"","permission_mode":"","budget_usd":null},"repos":[],"ops":{"grafana_url":"","aws_sso_start_url":"","aws_account_id":"","aws_role_name":""}}}`

type uiStateEnv struct {
	st  *store.SQL
	mux *http.ServeMux
	mv  string // the default member's membership id
}

func newUIStateEnv(t *testing.T, st *store.SQL) *uiStateEnv {
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
	m, err := st.EnsureMembership(ctx, ident.ID, dflt.ID, "member")
	if err != nil {
		t.Fatal(err)
	}
	api := newMemberUIStateAPI(&manager{store: st, authMode: "proxy", emailHeader: "X-Forwarded-Email", dataRoot: t.TempDir()})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/me/ui-state", api.withMembership(api.get))
	mux.HandleFunc("PATCH /api/me/ui-state", api.withMembership(api.patch))
	return &uiStateEnv{st: st, mux: mux, mv: m.ID}
}

func (e *uiStateEnv) do(t *testing.T, method, body, email string) (int, string) {
	t.Helper()
	r := httptest.NewRequest(method, "/api/me/ui-state", bytes.NewReader([]byte(body)))
	r.Header.Set("X-Forwarded-Email", email)
	r.Header.Set("X-AF-Tenant", "default")
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, r)
	return w.Code, strings.TrimSpace(w.Body.String())
}

// A member who never saved anything reads an empty document; a PATCH stores what it carries,
// and a later PATCH with no onboarding key leaves the stored one alone.
func TestMemberUIStateRoundTrip(t *testing.T) {
	for name, st := range ssmAPIStores(t) {
		t.Run(name, func(t *testing.T) {
			e := newUIStateEnv(t, st)
			if code, body := e.do(t, "GET", "", "u@x"); code != 200 || body != emptyUIState {
				t.Fatalf("fresh GET = %d %s, want 200 %s", code, body, emptyUIState)
			}
			want := `{"onboarding":{"dismissed":true,"track":"dev"},"defaults":{"agent":"","launch":{"agent":"","model":"","driver":"","permission_mode":"","budget_usd":null},"repos":[],"ops":{"grafana_url":"","aws_sso_start_url":"","aws_account_id":"","aws_role_name":""}},"effective":{"launch":{"agent":"","model":"","driver":"","permission_mode":"","budget_usd":null},"repos":[],"ops":{"grafana_url":"","aws_sso_start_url":"","aws_account_id":"","aws_role_name":""}}}`
			if code, body := e.do(t, "PATCH", `{"onboarding":{"dismissed":true,"track":"dev"}}`, "u@x"); code != 200 || body != want {
				t.Fatalf("PATCH = %d %s, want 200 %s", code, body, want)
			}
			if code, body := e.do(t, "PATCH", `{}`, "u@x"); code != 200 || body != want {
				t.Fatalf("empty PATCH = %d %s, want the stored document %s", code, body, want)
			}
			if code, body := e.do(t, "GET", "", "u@x"); code != 200 || body != want {
				t.Fatalf("GET after PATCH = %d %s, want %s", code, body, want)
			}
			row, ok, err := st.GetMemberUIState(context.Background(), e.mv)
			if err != nil || !ok || row.Version != 2 {
				t.Fatalf("stored row = %+v ok=%v err=%v, want version 2 after two writes", row, ok, err)
			}
		})
	}
}

// What the strict decoder refuses: unknown keys (a newer client or a typo would otherwise read
// as a setting that does not stick), a bad enum, trailing data and an oversized body.
func TestMemberUIStateRefusesBadPatches(t *testing.T) {
	st := ssmAPIStores(t)["sqlite"]
	e := newUIStateEnv(t, st)
	for _, tc := range []struct {
		name, body string
		code       int
	}{
		{"unknown top-level key", `{"theme":"dark"}`, 400},
		{"unknown nested key", `{"onboarding":{"dismissed":true,"extra":1}}`, 400},
		{"bad track", `{"onboarding":{"track":"ops"}}`, 400},
		{"trailing data", `{} {}`, 400},
		{"trailing bracket", `{}]`, 400},
		{"trailing brace", `{"onboarding":{"dismissed":true,"track":""}}}`, 400},
		{"not json", `nope`, 400},
		{"too large", `{"onboarding":{"track":"` + strings.Repeat("x", memberUIStateMaxBody) + `"}}`, 413},
	} {
		if code, body := e.do(t, "PATCH", tc.body, "u@x"); code != tc.code {
			t.Errorf("%s: PATCH = %d %s, want %d", tc.name, code, body, tc.code)
		}
	}
	if _, ok, _ := st.GetMemberUIState(context.Background(), e.mv); ok {
		t.Fatal("a refused PATCH stored a row")
	}
}

// The optimistic write: a stale version loses instead of overwriting, and an insert loses to a
// row that appeared since the read. This is what keeps two tabs from undoing each other.
func TestMemberUIStatePutIsOptimistic(t *testing.T) {
	for name, st := range ssmAPIStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			e := newUIStateEnv(t, st)
			row := store.MemberUIState{MembershipID: e.mv, State: `{}`, UpdatedAt: "2026-10-13T00:00:00Z"}
			if ok, err := st.PutMemberUIState(ctx, row, 0); err != nil || !ok {
				t.Fatalf("first insert ok=%v err=%v", ok, err)
			}
			if ok, err := st.PutMemberUIState(ctx, row, 0); err != nil || ok {
				t.Fatalf("second insert ok=%v err=%v, want a lost race", ok, err)
			}
			if ok, err := st.PutMemberUIState(ctx, row, 7); err != nil || ok {
				t.Fatalf("stale update ok=%v err=%v, want a lost race", ok, err)
			}
			if ok, err := st.PutMemberUIState(ctx, row, 1); err != nil || !ok {
				t.Fatalf("current update ok=%v err=%v", ok, err)
			}
		})
	}
}

// A stored document that no longer decodes (a newer CP wrote a key this one does not know, then
// was rolled back) reads as empty rather than failing the Console's boot, and the next PATCH
// replaces it.
func TestMemberUIStateUndecodableRowReadsEmpty(t *testing.T) {
	st := ssmAPIStores(t)["sqlite"]
	e := newUIStateEnv(t, st)
	ctx := context.Background()
	if ok, err := st.PutMemberUIState(ctx, store.MemberUIState{MembershipID: e.mv, State: `{"future":1}`, UpdatedAt: "x"}, 0); err != nil || !ok {
		t.Fatalf("seed ok=%v err=%v", ok, err)
	}
	if code, body := e.do(t, "GET", "", "u@x"); code != 200 || body != emptyUIState {
		t.Fatalf("GET = %d %s, want 200 %s", code, body, emptyUIState)
	}
	if code, _ := e.do(t, "PATCH", `{"onboarding":{"dismissed":true,"track":""}}`, "u@x"); code != 200 {
		t.Fatalf("PATCH over an undecodable row = %d", code)
	}
	row, _, _ := st.GetMemberUIState(ctx, e.mv)
	var got memberUIStateWire
	if err := json.Unmarshal([]byte(row.State), &got); err != nil || got.Onboarding == nil || !got.Onboarding.Dismissed {
		t.Fatalf("stored %q, want the patched onboarding", row.State)
	}
}

// Each member reads only their own document, and deleting the membership takes the row.
func TestMemberUIStateIsPerMembership(t *testing.T) {
	for name, st := range ssmAPIStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			e := newUIStateEnv(t, st)
			dflt, err := st.EnsureDefaultTenant(ctx)
			if err != nil {
				t.Fatal(err)
			}
			other, err := st.UpsertIdentity(ctx, "v@x", "v-x", "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.EnsureMembership(ctx, other.ID, dflt.ID, "member"); err != nil {
				t.Fatal(err)
			}
			if code, _ := e.do(t, "PATCH", `{"onboarding":{"dismissed":true,"track":""}}`, "u@x"); code != 200 {
				t.Fatalf("PATCH = %d", code)
			}
			if code, body := e.do(t, "GET", "", "v@x"); code != 200 || body != emptyUIState {
				t.Fatalf("other member GET = %d %s, want their own empty document", code, body)
			}
			if err := st.DeleteMembership(ctx, e.mv); err != nil {
				t.Fatal(err)
			}
			if _, ok, err := st.GetMemberUIState(ctx, e.mv); err != nil || ok {
				t.Fatalf("row after DeleteMembership ok=%v err=%v, want gone", ok, err)
			}
		})
	}
}

// if_absent fills a key only when it is not stored yet, inside the optimistic loop. It is what
// keeps a late localStorage migration from undoing a "later" another tab already saved.
func TestMemberUIStateIfAbsent(t *testing.T) {
	for name, st := range ssmAPIStores(t) {
		t.Run(name, func(t *testing.T) {
			e := newUIStateEnv(t, st)
			patch := func(query, body string) string {
				r := httptest.NewRequest("PATCH", "/api/me/ui-state"+query, bytes.NewReader([]byte(body)))
				r.Header.Set("X-Forwarded-Email", "u@x")
				r.Header.Set("X-AF-Tenant", "default")
				w := httptest.NewRecorder()
				e.mux.ServeHTTP(w, r)
				if w.Code != 200 {
					t.Fatalf("PATCH%s = %d %s", query, w.Code, w.Body.String())
				}
				return strings.TrimSpace(w.Body.String())
			}
			// Nothing stored: the migration's value lands.
			if got := patch("?if_absent=1", `{"onboarding":{"dismissed":false,"track":"dev"}}`); !strings.Contains(got, `"track":"dev"`) {
				t.Fatalf("if_absent on an empty document = %s", got)
			}
			// Another tab says "later"; a second, late migration must not undo it.
			patch("", `{"onboarding":{"dismissed":true,"track":"dev"}}`)
			if got := patch("?if_absent=1", `{"onboarding":{"dismissed":false,"track":""}}`); !strings.Contains(got, `"dismissed":true,"track":"dev"`) {
				t.Fatalf("a late migration overwrote the stored value: %s", got)
			}
		})
	}
}
