package runtime

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// The default /healthz probe client must not follow a redirect.
func TestHealthzClientRefusesRedirects(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()
	req, _ := http.NewRequest(http.MethodGet, redirector.URL, nil)
	resp, err := healthzClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || hits.Load() != 0 {
		t.Fatalf("status=%d hits=%d", resp.StatusCode, hits.Load())
	}
}
