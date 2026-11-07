package httpx

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

// The access log never carries a login attempt id (a capability) or a login route's query.
func TestLogRequestsRedactsLoginAttempts(t *testing.T) {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	id := hex.EncodeToString(b)
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old) })
	h := LogRequests(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	for _, target := range []string{
		"/gcp-login/profiles/prod/attempts/" + id,
		"/gcp-login/profiles/prod/attempts/" + id + "/code",
		"/aws-login/0123456789abcdef01234567/attempts/" + id,
		"/gcp-login/profiles/prod/start?force=1&x=" + id,
	} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", target, nil))
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/sessions?all=1", nil))
	out := buf.String()
	if strings.Contains(out, id) {
		t.Fatalf("the attempt id reached the access log:\n%s", out)
	}
	if !strings.Contains(out, "/gcp-login/profiles/prod/attempts/ref:") || !strings.Contains(out, "/code") ||
		!strings.Contains(out, "/sessions?all=1") || !strings.Contains(out, "/gcp-login/profiles/prod/start?…") {
		t.Fatalf("access log:\n%s", out)
	}
}

// A create's idempotency key never reaches the access log: a handoff launch's key carries the
// recipient's take token (issue #2040).
func TestLogRequestsRedactsIdempotencyKeys(t *testing.T) {
	const key = "handoff-offer-private-take-token"
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old) })
	h := LogRequests(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	for _, target := range []string{
		"/sessions-idempotency/" + key,
		"/sessions-idempotency/" + key + "/reserve",
		"/sessions-idempotency/" + key + "/abandon",
	} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", target, nil))
	}
	out := buf.String()
	if strings.Contains(out, "private-take-token") {
		t.Fatalf("the key reached the access log:\n%s", out)
	}
	if strings.Count(out, "/sessions-idempotency/ref:") != 3 || !strings.Contains(out, "/reserve") || !strings.Contains(out, "/abandon") {
		t.Fatalf("access log:\n%s", out)
	}
}

func loadSecretQueryKeyFixture(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile("../../../../scripts/fixtures/secret-query-keys.txt")
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(b))
}

// The Agent redacts the same parameter names as the control plane; the shared fixture
// is what keeps the two lists from drifting apart.
func TestSecretQueryKeysMatchFixture(t *testing.T) {
	want := loadSecretQueryKeyFixture(t)
	if len(want) != len(secretQueryKeys) {
		t.Fatalf("fixture has %d names, httpx has %d", len(want), len(secretQueryKeys))
	}
	for _, k := range want {
		if !secretQueryKeys[k] {
			t.Errorf("httpx.secretQueryKeys lacks %q", k)
		}
	}
}

// Credential-named parameters are redacted anywhere; a preview relay query is dropped whole.
func TestLogURIRedactsQueries(t *testing.T) {
	for _, k := range loadSecretQueryKeyFixture(t) {
		u, _ := url.Parse("/sessions?x=1&" + k + "=SECRETVALUE")
		got := LogURI(u)
		if strings.Contains(got, "SECRETVALUE") || !strings.Contains(got, "x=1") {
			t.Errorf("%s: %q", k, got)
		}
	}
	u, _ := url.Parse("/proxy/3000/auth/callback?code=SECRETVALUE&page=2")
	if got := LogURI(u); got != "/proxy/3000/auth/callback?<redacted>" {
		t.Errorf("proxy: %q", got)
	}
	u, _ = url.Parse("/sessions?all=1")
	if got := LogURI(u); got != "/sessions?all=1" {
		t.Errorf("plain: %q", got)
	}
}
