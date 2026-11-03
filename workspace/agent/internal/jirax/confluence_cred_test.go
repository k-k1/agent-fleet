package jirax

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

func confluenceCredBoth() (string, string) {
	var out, errb bytes.Buffer
	confluenceCredGet(&out, &errb)
	return out.String(), errb.String()
}

func TestConfluenceCredServesAnAPITokenConnectionAsBasicOnTheSite(t *testing.T) {
	seedJiraStore(t, func(s *secrets.Data) {
		s.Jira = &secrets.JiraCreds{Site: "https://acme.atlassian.net", Email: "me@example.com", Token: "fake-api-token"}
	})
	want := "site=https://acme.atlassian.net\ndomain=acme.atlassian.net\napipath=/wiki/rest/api\nauth=basic\n" +
		"login=me@example.com\ntoken=fake-api-token\n"
	if got, why := confluenceCredBoth(); got != want || why != "" {
		t.Fatalf("answered %q (stderr %q), want %q", got, why, want)
	}
}

// A 3LO token is refused by the site's own host: the CLI must be pointed at api.atlassian.com
// with the /ex/confluence/<cloudId> prefix, while site stays the human-facing base for links.
func TestConfluenceCredServesAnOAuthTokenAsBearerOnTheCloudRoute(t *testing.T) {
	seedJiraStore(t, func(s *secrets.Data) {
		s.Jira = &secrets.JiraCreds{AuthKind: "oauth", Site: "https://acme.atlassian.net", CloudID: "cid-1",
			Login: "me@example.com", AccessToken: "fake-at", RefreshToken: "fake-rt",
			Expiry: time.Now().Add(time.Hour).Unix(),
			Sites: []secrets.JiraSite{{CloudID: "cid-1", URL: "https://acme.atlassian.net",
				Scopes: []string{"read:jira-work", "read:confluence-content.all"}}}}
	})
	want := "site=https://acme.atlassian.net\ndomain=api.atlassian.com\n" +
		"apipath=/ex/confluence/cid-1/wiki/rest/api\nauth=bearer\ntoken=fake-at\n"
	if got, why := confluenceCredBoth(); got != want || why != "" {
		t.Fatalf("answered %q (stderr %q), want %q", got, why, want)
	}
}

// An authorization made before the Confluence scopes were requested gets a reason and no token,
// instead of a Confluence 401 that reads like a broken connection. Jira keeps working on it.
func TestConfluenceCredRefusesAnAuthorizationWithoutConfluenceScopes(t *testing.T) {
	seedJiraStore(t, func(s *secrets.Data) {
		s.Jira = &secrets.JiraCreds{AuthKind: "oauth", Site: "https://acme.atlassian.net", CloudID: "cid-1",
			AccessToken: "fake-at", Expiry: time.Now().Add(time.Hour).Unix(),
			Sites: []secrets.JiraSite{{CloudID: "cid-1", URL: "https://acme.atlassian.net",
				Scopes: []string{"read:jira-work", "offline_access"}}}}
	})
	got, why := confluenceCredBoth()
	if got != "" || !strings.HasPrefix(why, "unusable=") || !strings.Contains(why, "Confluence") {
		t.Fatalf("answered %q, stderr %q: want no credential and an unusable= reason", got, why)
	}
	if strings.Contains(why, "fake-at") {
		t.Fatalf("token in the reason: %q", why)
	}
	if jiraOut := jiraCredOut(); !strings.HasSuffix(jiraOut, "token=fake-at\n") {
		t.Fatalf("jira-cred must keep serving this connection, got %q", jiraOut)
	}
}

// Unknown scopes (a store from before they were recorded, a failed lookup) are not a refusal.
func TestConfluenceCredServesWhenScopesAreUnknown(t *testing.T) {
	stubAccessibleResources(t, `nope`, 500)
	seedLegacyOAuth(t)
	got, why := confluenceCredBoth()
	if !strings.HasSuffix(got, "token=fake-at\n") || why != "" {
		t.Fatalf("answered %q (stderr %q)", got, why)
	}
}

// The scopes of an older connection are looked up on the way, so one that never had Confluence
// is told so without reconnecting first.
func TestConfluenceCredBackfillsScopesBeforeDeciding(t *testing.T) {
	stubAccessibleResources(t, `[{"id":"cid-1","url":"https://acme.atlassian.net","scopes":["read:jira-work"]}]`, 200)
	seedLegacyOAuth(t)
	got, why := confluenceCredBoth()
	if got != "" || !strings.HasPrefix(why, "unusable=") {
		t.Fatalf("answered %q, stderr %q", got, why)
	}
}

func TestConfluenceCredServesNothingWithoutAConnection(t *testing.T) {
	seedJiraStore(t, func(s *secrets.Data) {})
	if got, why := confluenceCredBoth(); got != "" || why != "" {
		t.Fatalf("answered %q (stderr %q), want nothing at all", got, why)
	}
}

func TestConfluenceCredRejectsALineBreakInTheToken(t *testing.T) {
	seedJiraStore(t, func(s *secrets.Data) {
		s.Jira = &secrets.JiraCreds{Site: "https://acme.atlassian.net", Email: "me@example.com", Token: "fake\ntoken=other"}
	})
	got, why := confluenceCredBoth()
	if got != "" || !strings.HasPrefix(why, "unusable=") {
		t.Fatalf("answered %q, stderr %q", got, why)
	}
}

// jira-cred and confluence-cred notice the same expiry at the same moment: the rotating refresh
// token must be exchanged once, and both must hand out the pair that exchange produced.
func TestConfluenceAndJiraCredShareOneRefreshExchange(t *testing.T) {
	var mu sync.Mutex
	var grants []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RefreshToken string `json:"refresh_token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		grants = append(grants, body.RefreshToken)
		mu.Unlock()
		time.Sleep(100 * time.Millisecond)
		_, _ = w.Write([]byte(`{"access_token":"fake-at2","refresh_token":"fake-rt2","expires_in":3600}`))
	}))
	defer srv.Close()
	seedJiraStore(t, func(s *secrets.Data) {
		s.GitOAuthBridge = &secrets.CPBridge{BaseURL: srv.URL, Token: "afo_x"}
		s.Jira = &secrets.JiraCreds{AuthKind: "oauth", Site: "https://acme.atlassian.net", CloudID: "cid-1",
			AccessToken: "fake-at", RefreshToken: "fake-rt", Expiry: time.Now().Add(time.Minute).Unix(),
			Sites: []secrets.JiraSite{{CloudID: "cid-1", URL: "https://acme.atlassian.net",
				Scopes: []string{"read:confluence-content.all"}}}}
	})
	var wg sync.WaitGroup
	var jiraOut, confOut string
	wg.Add(2)
	go func() { defer wg.Done(); jiraOut = jiraCredOut() }()
	go func() { defer wg.Done(); confOut, _ = confluenceCredBoth() }()
	wg.Wait()
	if len(grants) != 1 || grants[0] != "fake-rt" {
		t.Fatalf("grants = %v, want exactly one exchange of fake-rt", grants)
	}
	if !strings.HasSuffix(jiraOut, "token=fake-at2\n") || !strings.HasSuffix(confOut, "token=fake-at2\n") {
		t.Fatalf("jira %q, confluence %q: both must carry the renewed token", jiraOut, confOut)
	}
}

// accessible-resources lists a site once per product under the same id: Jira's row holding no
// Confluence scope says nothing about Confluence's. The verdict is the union, in either order,
// for both the Confluence and the Jira Software check.
func TestScopeVerdictsUnionRowsOfTheSameSite(t *testing.T) {
	jiraRow := secrets.JiraSite{CloudID: "c1", URL: "https://a.atlassian.net", Scopes: []string{"read:jira-work"}}
	confRow := secrets.JiraSite{CloudID: "c1", URL: "https://a.atlassian.net", Scopes: append([]string{"read:confluence-content.all"}, confluencePageScopeList...)}
	boardRow := secrets.JiraSite{CloudID: "c1", URL: "https://a.atlassian.net", Scopes: []string{jiraBoardScope}}
	mk := func(rows ...secrets.JiraSite) *secrets.JiraCreds {
		return &secrets.JiraCreds{AuthKind: "oauth", CloudID: "c1", Sites: rows}
	}
	for name, c := range map[string]*secrets.JiraCreds{"jira first": mk(jiraRow, confRow), "confluence first": mk(confRow, jiraRow)} {
		if got := ConfluenceScopes(c); got != "granted" {
			t.Errorf("%s: ConfluenceScopes = %q, want granted", name, got)
		}
	}
	for name, c := range map[string]*secrets.JiraCreds{"jira first": mk(jiraRow, boardRow), "board first": mk(boardRow, jiraRow)} {
		if got := SoftwareScopes(c); got != "granted" {
			t.Errorf("%s: SoftwareScopes = %q, want granted", name, got)
		}
	}
	if got := ConfluenceScopes(mk(jiraRow, jiraRow)); got != "missing" {
		t.Errorf("no Confluence in any row: %q, want missing", got)
	}
	if got := ConfluenceScopes(mk(secrets.JiraSite{CloudID: "c1"}, jiraRow)); got != "missing" {
		t.Errorf("an unrecorded row beside a recorded one: %q, want missing", got)
	}
	if got := ConfluenceScopes(mk(secrets.JiraSite{CloudID: "c1"})); got != "" {
		t.Errorf("nothing recorded: %q, want unknown", got)
	}
}

func TestConfluenceCredServesWhenAnotherRowOfTheSiteHoldsTheScope(t *testing.T) {
	seedJiraStore(t, func(s *secrets.Data) {
		s.Jira = &secrets.JiraCreds{AuthKind: "oauth", Site: "https://a.atlassian.net", CloudID: "c1",
			AccessToken: "fake-at", Expiry: time.Now().Add(time.Hour).Unix(),
			Sites: []secrets.JiraSite{
				{CloudID: "c1", URL: "https://a.atlassian.net", Scopes: []string{"read:confluence-content.all"}},
				{CloudID: "c1", URL: "https://a.atlassian.net", Scopes: []string{"read:jira-work"}}}}
	})
	got, why := confluenceCredBoth()
	if !strings.HasSuffix(got, "token=fake-at\n") || why != "" {
		t.Fatalf("answered %q (stderr %q)", got, why)
	}
}

// The lookup keeps every row's scopes for an id instead of the last one winning.
func TestBackfillKeepsTheUnionOfRowsWithOneID(t *testing.T) {
	stubAccessibleResources(t, `[{"id":"cid-1","url":"https://acme.atlassian.net","scopes":["read:confluence-content.all"]},`+
		`{"id":"cid-1","url":"https://acme.atlassian.net","scopes":["read:jira-work"]}]`, 200)
	seedLegacyOAuth(t)
	got, why := confluenceCredBoth()
	if !strings.HasSuffix(got, "token=fake-at\n") || why != "" {
		t.Fatalf("answered %q (stderr %q): the second row's scopes were lost", got, why)
	}
}
