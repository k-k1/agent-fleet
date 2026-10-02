package httpx

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"log"
	"net/http"
	"net/http/httptest"
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
