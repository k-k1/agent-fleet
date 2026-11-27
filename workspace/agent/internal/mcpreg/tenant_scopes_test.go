package mcpreg

// Definition scopes as the Control Plane distributes them (ADR 0146 decision 2). Both files under
// testdata are shared with the Control Plane's tests (control-plane/internal/mcpsrv), which is what
// holds the two modules to one rule and one wire shape: a definition the Agent refuses is dropped,
// and a dropped OAuth definition ends every member's sign-in to it.

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

type defScopesCase struct {
	Name    string   `json:"name"`
	Scopes  []string `json:"scopes"`
	TokenOf int      `json:"tokenOf"`
	Valid   bool     `json:"valid"`
}

func (c defScopesCase) list() []string {
	out := append([]string{}, c.Scopes...)
	if c.TokenOf > 0 {
		out = append(out, strings.Repeat("a", c.TokenOf))
	}
	return out
}

func TestValidDefScopesSharedCases(t *testing.T) {
	raw, err := os.ReadFile("testdata/def-scopes-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Cases []defScopesCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &f); err != nil || len(f.Cases) == 0 {
		t.Fatalf("cases: %v (%d)", err, len(f.Cases))
	}
	for _, c := range f.Cases {
		if got := validDefScopes(c.list()); got != c.Valid {
			t.Errorf("%s: validDefScopes = %v, want %v", c.Name, got, c.Valid)
		}
	}
}

func TestFetchTenantKeepsDistributedOAuthOptions(t *testing.T) {
	withTempHome(t)
	raw, err := os.ReadFile("testdata/cp-tenant-distribution.json")
	if err != nil {
		t.Fatal(err)
	}
	serveTenant(t, json.RawMessage(raw))
	res, err := FetchTenant()
	if err != nil {
		t.Fatal(err)
	}
	if res.Dropped != 0 || res.Servers != 2 {
		t.Fatalf("fetch: %+v, want both definitions kept", res)
	}
	byName := map[string]ServerDef{}
	for _, d := range loadTenantCache().Servers {
		byName[d.Name] = d
	}
	gmail := byName["gmail"]
	if want := []string{"https://www.googleapis.com/auth/gmail.readonly", "openid"}; !reflect.DeepEqual(gmail.Scopes, want) {
		t.Fatalf("gmail scopes = %q, want %q", gmail.Scopes, want)
	}
	if gmail.OAuthClient == nil || gmail.OAuthClient.ClientID != "corp-agent" || !gmail.OAuthClient.Confidential {
		t.Fatalf("gmail client = %+v", gmail.OAuthClient)
	}
	if gmail.Elicitation != ElicitationRelay {
		t.Fatalf("gmail elicitation = %q, want %q", gmail.Elicitation, ElicitationRelay)
	}
	if plain := byName["wiki"]; plain.Auth != "" || len(plain.Scopes) != 0 || plain.Elicitation != "" {
		t.Fatalf("wiki = %+v, want a static-header definition without OAuth options", plain)
	}
}
