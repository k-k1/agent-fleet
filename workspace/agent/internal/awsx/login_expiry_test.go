package awsx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
)

func writeSSOCacheDoc(t *testing.T, doc map[string]string) {
	t.Helper()
	path := ssoCachePath("af-prod")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(doc)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func profileStates(t *testing.T) (profileLoginStateWire, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	HandleProfileLoginStates(rec, httptest.NewRequest(http.MethodGet, "/aws-login/profiles", nil))
	var out profileLoginStatesWire
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil || len(out.Profiles) != 1 {
		t.Fatalf("states = %d %s", rec.Code, rec.Body.String())
	}
	return out.Profiles[0], rec.Body.String()
}

func TestReadSSOExpiryPicksTheEndTheCacheKnows(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Date(2026, 10, 2, 3, 0, 0, 0, time.UTC)
	access := now.Add(10 * time.Minute)
	reg := now.Add(90 * 24 * time.Hour)
	rfc := func(t time.Time) string { return t.Format(time.RFC3339) }
	for _, c := range []struct {
		name string
		doc  map[string]string
		want time.Time
		ok   bool
	}{
		{"no cache", nil, time.Time{}, false},
		{"access token only", map[string]string{"accessToken": "secret-a", "expiresAt": rfc(access)}, access, true},
		{"older CLI's UTC suffix", map[string]string{"accessToken": "secret-a", "expiresAt": access.Format("2006-01-02T15:04:05") + "UTC"}, access, true},
		{"refresh-capable: the registration bounds it", map[string]string{"accessToken": "secret-a", "expiresAt": rfc(access),
			"refreshToken": "secret-r", "clientId": "c", "clientSecret": "secret-c", "registrationExpiresAt": rfc(reg)}, reg, true},
		// botocore does not refresh without the client registration.
		{"refresh token but no registration", map[string]string{"accessToken": "secret-a", "expiresAt": rfc(access),
			"refreshToken": "secret-r", "registrationExpiresAt": rfc(reg)}, access, true},
		{"registration ends first", map[string]string{"accessToken": "secret-a", "expiresAt": rfc(access),
			"refreshToken": "secret-r", "clientId": "c", "clientSecret": "secret-c", "registrationExpiresAt": rfc(now)}, access, true},
		{"no access token", map[string]string{"expiresAt": rfc(access)}, time.Time{}, false},
		{"unparseable time", map[string]string{"accessToken": "secret-a", "expiresAt": "tomorrow"}, time.Time{}, false},
	} {
		os.Remove(ssoCachePath("af-prod"))
		if c.doc != nil {
			writeSSOCacheDoc(t, c.doc)
		}
		got, ok := readSSOExpiry("af-prod")
		if ok != c.ok || !got.Equal(c.want) {
			t.Errorf("%s: got %v %v, want %v %v", c.name, got, ok, c.want, c.ok)
		}
	}
	// A corrupt file is no expiry, not a failure.
	os.WriteFile(ssoCachePath("af-prod"), []byte("{not json"), 0o600)
	if _, ok := readSSOExpiry("af-prod"); ok {
		t.Error("corrupt cache gave an expiry")
	}
}

func TestProfileStatesListAnExpiringLoginWithoutTokens(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	withSettingsCache(t)
	end := time.Now().Add(10 * time.Minute).UTC().Truncate(time.Second)
	writeSSOCacheDoc(t, map[string]string{"accessToken": "secret-access", "expiresAt": end.Format(time.RFC3339),
		"clientSecret": "secret-client"})
	p, body := profileStates(t)
	if !p.Expiring || p.ExpiresAt != end.Format(time.RFC3339) || p.AccountID != "123456789012" || p.RoleName != "Dev" {
		t.Fatalf("expiring login: %s", body)
	}
	if strings.Contains(body, "secret-") {
		t.Fatalf("a token left the Agent: %s", body)
	}
	// Far from the end: listed with its end, not expiring.
	writeSSOCacheDoc(t, map[string]string{"accessToken": "secret-access", "expiresAt": time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)})
	if p, body := profileStates(t); p.Expiring || p.ExpiresAt == "" {
		t.Fatalf("two hours left: %s", body)
	}
	// Already ended: the login request takes over, the warning does not.
	writeSSOCacheDoc(t, map[string]string{"accessToken": "secret-access", "expiresAt": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)})
	if p, body := profileStates(t); p.Expiring {
		t.Fatalf("ended: %s", body)
	}
	// A refresh token that can still renew for weeks: the hourly access token is no warning.
	writeSSOCacheDoc(t, map[string]string{"accessToken": "secret-access", "expiresAt": end.Format(time.RFC3339),
		"refreshToken": "secret-refresh", "clientId": "c", "clientSecret": "secret-client",
		"registrationExpiresAt": time.Now().Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339)})
	if p, body := profileStates(t); p.Expiring {
		t.Fatalf("renewable: %s", body)
	}
}

func TestWarnExpiringSSOFilesOneNoticePerEnd(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	withSettingsCache(t)
	count := func() (n int, profile any) {
		for _, ev := range notice.List() {
			if ev.Kind == NoticeKindAWSExpiring {
				n++
				profile = ev.Payload["profile"]
			}
		}
		return n, profile
	}
	now := time.Now()
	warnExpiringSSO(now)
	if n, _ := count(); n != 0 {
		t.Fatalf("no cache: %d notices", n)
	}
	writeSSOCacheDoc(t, map[string]string{"accessToken": "secret-access", "expiresAt": now.Add(time.Hour).UTC().Format(time.RFC3339)})
	warnExpiringSSO(now)
	if n, _ := count(); n != 0 {
		t.Fatalf("an hour left: %d notices", n)
	}
	end := now.Add(12 * time.Minute).UTC()
	writeSSOCacheDoc(t, map[string]string{"accessToken": "secret-access", "expiresAt": end.Format(time.RFC3339)})
	warnExpiringSSO(now)
	warnExpiringSSO(now.Add(PollInterval))
	if n, p := count(); n != 1 || p != "prod" {
		t.Fatalf("same end, two polls: %d notices for %v", n, p)
	}
	for _, ev := range notice.List() {
		b, _ := json.Marshal(ev)
		if strings.Contains(string(b), "secret-") {
			t.Fatalf("a token reached the outbox: %s", b)
		}
	}
	// A re-login that again runs short is a new end and warns again.
	writeSSOCacheDoc(t, map[string]string{"accessToken": "secret-access2", "expiresAt": end.Add(time.Minute).Format(time.RFC3339)})
	warnExpiringSSO(now)
	if n, _ := count(); n != 2 {
		t.Fatalf("new end: %d notices", n)
	}
}
