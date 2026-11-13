package auth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// The default IdP client carries client_secret in the code exchange. A token endpoint that
// answers with a redirect must not get the secret re-sent to the target.
func TestDefaultIdPClientRefusesRedirectedCodeExchange(t *testing.T) {
	for _, code := range []int{301, 302, 303, 307, 308} {
		var otherHits atomic.Int32
		var otherBody atomic.Value
		other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			otherHits.Add(1)
			b, _ := io.ReadAll(r.Body)
			otherBody.Store(string(b))
			_, _ = w.Write([]byte(`{"access_token":"stolen"}`))
		}))
		var first atomic.Int32
		idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			first.Add(1)
			http.Redirect(w, r, other.URL+"/stolen", code)
		}))

		oidc := &OIDCProvider{ProviderID: "x", ClientID: "cid", ClientSecret: "client-secret"}
		oidc.seedEndpoints(Endpoints{Authorize: idp.URL + "/authorize", Token: idp.URL + "/token", Userinfo: idp.URL + "/userinfo"})
		if _, err := oidc.Exchange(context.Background(), "code", "https://af.example/cb"); err == nil {
			t.Errorf("oidc %d: a redirected token POST succeeded", code)
		}

		gh := &GitHubProvider{ProviderID: "gh", ClientID: "cid", ClientSecret: "client-secret", WebBase: idp.URL}
		if _, err := gh.ExchangeCode(context.Background(), "code", "https://af.example/cb"); err == nil {
			t.Errorf("github %d: a redirected token POST succeeded", code)
		}

		if n := otherHits.Load(); n != 0 {
			t.Errorf("%d: the second origin received %d request(s): %v", code, n, otherBody.Load())
		}
		if n := first.Load(); n != 2 {
			t.Errorf("%d: the IdP saw %d requests, want 2 (one per provider)", code, n)
		}
		idp.Close()
		other.Close()
	}
}
