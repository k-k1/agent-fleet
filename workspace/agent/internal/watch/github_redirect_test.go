package watch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// The GitHub token must not follow a redirect off the API origin.
func TestGitHubDoRefusesRedirectOffOrigin(t *testing.T) {
	var otherHits atomic.Int32
	var otherAuth atomic.Value
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherHits.Add(1)
		otherAuth.Store(r.Header.Get("Authorization"))
	}))
	defer other.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/stolen", http.StatusTemporaryRedirect)
	}))
	defer api.Close()
	g := NewGitHub(func() (string, error) { return "gh-secret", nil }, nil)
	g.API = api.URL
	if resp, err := g.do(context.Background(), "/repos/o/r/pulls", "", "gh-secret"); err == nil {
		resp.Body.Close()
		t.Fatalf("redirect off the API origin was followed (status %d)", resp.StatusCode)
	}
	if n := otherHits.Load(); n != 0 {
		t.Errorf("the other origin received %d request(s), Authorization=%v", n, otherAuth.Load())
	}
}
