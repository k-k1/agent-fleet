package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

func gcpRandHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// TestGCPLoginRelayCarriesTheCodeOnlyToTheAgent: the press, the attempt poll and the code go
// through the relay; the Agent receives the code body as sent, the body is bounded, and the
// synthetic code, attempt id and URL appear in neither the CP's log nor its audit rows. The
// audit names the profile, the attempt's reference and that the call was relayed.
func TestGCPLoginRelayCarriesTheCodeOnlyToTheAgent(t *testing.T) {
	attempt := gcpRandHex(t, 12)
	code := "4/0" + gcpRandHex(t, 30)
	signIn := "https://accounts.google.com/o/oauth2/auth?client_id=" + gcpRandHex(t, 8) + "&redirect_uri=" +
		url.QueryEscape("https://sdk.cloud.google.com/authcode.html") + "&state=" + gcpRandHex(t, 8)
	var mu sync.Mutex
	var bodies []string
	proxy, res, st, cleanup := newFSProxyTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, r.Method+" "+r.URL.Path+" "+string(b))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/start"):
			_ = json.NewEncoder(w).Encode(map[string]string{"attempt": attempt})
		case strings.HasSuffix(r.URL.Path, "/code"):
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			_ = json.NewEncoder(w).Encode(map[string]string{"phase": "authorize", "url": signIn})
		}
	}))
	defer cleanup()
	logs := &lockedBuf{}
	old := log.Writer()
	log.SetOutput(logs)
	t.Cleanup(func() { log.SetOutput(old) })

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/gcp-login/profiles/{name}/start", func(w http.ResponseWriter, r *http.Request) { proxy.restLoginFlow(w, r, res) })
	mux.HandleFunc("GET /api/gcp-login/profiles/{name}/attempts/{attempt}", func(w http.ResponseWriter, r *http.Request) { proxy.restLoginFlow(w, r, res) })
	mux.HandleFunc("POST /api/gcp-login/profiles/{name}/attempts/{attempt}/code", func(w http.ResponseWriter, r *http.Request) { proxy.gcpLoginCode(w, r, res) })
	do := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}

	if rec := do("POST", "/api/gcp-login/profiles/prod/start", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), attempt) {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do("GET", "/api/gcp-login/profiles/prod/attempts/"+attempt, ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "accounts.google.com") {
		t.Fatalf("poll: %d %s", rec.Code, rec.Body.String())
	}
	body := `{"code":"` + code + `"}`
	if rec := do("POST", "/api/gcp-login/profiles/prod/attempts/"+attempt+"/code", body); rec.Code != 200 {
		t.Fatalf("code: %d %s", rec.Code, rec.Body.String())
	}
	// Over the limit: refused before the Agent is asked.
	if rec := do("POST", "/api/gcp-login/profiles/prod/attempts/"+attempt+"/code", `{"code":"`+strings.Repeat("a", maxGCPLoginCodeBody)+`"}`); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized: %d", rec.Code)
	}
	mu.Lock()
	got := strings.Join(bodies, "\n")
	n := len(bodies)
	mu.Unlock()
	if n != 3 || !strings.Contains(got, "POST /gcp-login/profiles/prod/attempts/"+attempt+"/code "+body) {
		t.Fatalf("the Agent received:\n%s", got)
	}

	rows, err := st.ListAuditByTenant(context.Background(), res.ws.TenantID, 50)
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	audit := ""
	for _, r := range rows {
		b, _ := json.Marshal(r)
		audit += string(b) + "\n"
		actions = append(actions, r.Action+" | "+r.Target+" | "+r.Detail)
	}
	want := map[string]bool{
		"gcp.login.start | profile: prod | via relay":                                         true,
		"gcp.login.code | profile: prod, attempt: " + gcpAttemptRef(attempt) + " | via relay": true,
	}
	if len(actions) != 2 || !want[actions[0]] || !want[actions[1]] {
		t.Fatalf("audit rows: %q", actions)
	}
	// The Agent unreachable: the relay logs the failure, with the attempt's reference only.
	// This is also what shows the log scan below reads a log that has lines in it.
	cleanup()
	if rec := do("POST", "/api/gcp-login/profiles/prod/attempts/"+attempt+"/code", body); rec.Code != http.StatusBadGateway {
		t.Fatalf("code to a stopped Agent: %d", rec.Code)
	}
	if !strings.Contains(logs.String(), "attempts/ref:"+gcpAttemptRef(attempt)+"/code") {
		t.Fatalf("CP log: %q", logs.String())
	}
	for what, s := range map[string]string{"code": code, "attempt id": attempt, "URL": signIn, "URL host+query": "client_id="} {
		if strings.Contains(audit, s) {
			t.Errorf("the %s appears in the audit: %s", what, audit)
		}
		if strings.Contains(logs.String(), s) {
			t.Errorf("the %s appears in the CP log: %s", what, logs.String())
		}
	}
}
