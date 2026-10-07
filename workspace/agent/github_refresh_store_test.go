package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

func putGitHubConn(t *testing.T, body string) {
	t.Helper()
	req := httptest.NewRequest("PUT", "/connections/git/github.com", strings.NewReader(body))
	req.SetPathValue("host", "github.com")
	rec := httptest.NewRecorder()
	handlePutGitConn(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
}

// The Control Plane hands over the refresh token next to the access token; both land in the
// same encrypted entry, with absolute expiries and the app's client id (no secret).
func TestPutGitHubConnStoresRenewalData(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SECRET_KEY", "")
	putGitHubConn(t, `{"token":"fake-access","refresh_token":"fake-refresh","expires_in":28800,"refresh_token_expires_in":15811200,"client_id":"fake-client-id"}`)
	s, _ := secrets.Load()
	e := s.Git["github.com"]
	if e.Token != "fake-access" || e.RefreshToken != "fake-refresh" || e.ClientID != "fake-client-id" {
		t.Fatalf("entry: %+v", e)
	}
	if e.Expiry < time.Now().Add(7*time.Hour).Unix() || e.RefreshExpiry < time.Now().Add(180*24*time.Hour).Unix() {
		t.Fatalf("expiries: %+v", e)
	}

	// A later paste of a plain token replaces the pair outright: nothing of the old renewal
	// state may survive into a token that cannot be renewed.
	putGitHubConn(t, `{"token":"fake-pat"}`)
	s, _ = secrets.Load()
	if e := s.Git["github.com"]; e.RefreshToken != "" || e.Expiry != 0 || e.ClientID != "" || e.Token != "fake-pat" {
		t.Fatalf("stale renewal state kept: %+v", e)
	}
}

// Without the client id the grant cannot be asked for; storing the refresh token anyway
// would only look renewable.
func TestPutGitHubConnWithoutClientIDStoresAPlainToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SECRET_KEY", "")
	putGitHubConn(t, `{"token":"fake-access","refresh_token":"fake-refresh","expires_in":28800}`)
	s, _ := secrets.Load()
	if e := s.Git["github.com"]; e.RefreshToken != "" || e.Expiry != 0 {
		t.Fatalf("entry: %+v", e)
	}
}

func TestGitHubStatusShowsReconnectNeeded(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SECRET_KEY", "")
	if err := secrets.Update(func(s *secrets.Data) error {
		s.Git["github.com"] = secrets.GitEntry{User: "x-access-token", Token: "fake-access", RefreshToken: "fake-refresh",
			ClientID: "fake-client-id", ReconnectNeeded: true, Login: "octo", Email: "o@example.invalid"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s, _ := secrets.Load()
	m := gitConnStatus(s, "github.com")
	if m["connected"] != true || m["reconnect_needed"] != true {
		t.Fatalf("status: %v", m)
	}
	raw, _ := json.Marshal(m)
	if bytes.Contains(raw, []byte("fake-")) {
		t.Fatalf("status must carry no token: %s", raw)
	}

	// A healthy connection reports no such flag.
	if err := secrets.Update(func(s *secrets.Data) error {
		s.Git["github.com"] = secrets.GitEntry{User: "x-access-token", Token: "fake-access", Login: "octo", Email: "o@example.invalid"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s, _ = secrets.Load()
	if _, has := gitConnStatus(s, "github.com")["reconnect_needed"]; has {
		t.Fatal("reconnect_needed on a healthy connection")
	}
}

// git must get no credential once the connection cannot be renewed, rather than a dead token
// it would send and be refused for.
func TestCredHelperOffersNothingForAConnectionNeedingReconnect(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SECRET_KEY", "")
	seed := func(reconnect bool) {
		if err := secrets.Update(func(s *secrets.Data) error {
			s.Git["github.com"] = secrets.GitEntry{User: "x-access-token", Token: "fake-access", RefreshToken: "fake-refresh",
				ClientID: "fake-client-id", Expiry: time.Now().Add(time.Hour).Unix(), ReconnectNeeded: reconnect}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	seed(false)
	credHelperGet(strings.NewReader("protocol=https\nhost=github.com\n\n"), &out)
	if want := "username=x-access-token\npassword=fake-access\n"; out.String() != want {
		t.Fatalf("helper answered %q", out.String())
	}
	out.Reset()
	seed(true)
	credHelperGet(strings.NewReader("protocol=https\nhost=github.com\n\n"), &out)
	if out.Len() != 0 {
		t.Fatalf("helper answered %q", out.String())
	}
}
