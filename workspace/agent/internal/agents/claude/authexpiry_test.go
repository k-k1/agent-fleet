package claude

// Credential expiry classification (docs/log/47 §4-8). Both the classification itself (two
// epochs → alive/expired) and the wiring that reads it out of the file are pinned here.
//
// The damage from a false positive is asymmetric, so the boundary leans towards saying "not
// expired": calling a live login expired refuses every send on a working session.

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func ms(t time.Time) int64 { return t.UnixMilli() }

func credsAt(access, refresh time.Time) credsFile {
	var c credsFile
	c.ClaudeAiOauth.AccessToken = "sk-ant-oat01-dummy"
	c.ClaudeAiOauth.ExpiresAt = ms(access)
	c.ClaudeAiOauth.RefreshTokenExpiresAt = ms(refresh)
	return c
}

func TestExpiryClassification(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour

	cases := []struct {
		name         string
		access       time.Time
		refresh      time.Time
		envToken     bool
		wantKnown    bool
		wantDead     bool
		wantSoon     bool
		wantDaysLeft int
	}{
		{
			name: "normal (25 days left)", access: now.Add(8 * time.Hour), refresh: now.Add(25 * day),
			wantKnown: true, wantDaysLeft: 25,
		},
		{
			// Exactly the condition for the CLI's startup hint (a day or less). The Console
			// starts showing it three days out.
			name: "1 day left", access: now.Add(2 * time.Hour), refresh: now.Add(20 * time.Hour),
			wantKnown: true, wantSoon: true, wantDaysLeft: 1,
		},
		{
			name: "exactly 3 days left", access: now.Add(time.Hour), refresh: now.Add(3 * day),
			wantKnown: true, wantSoon: true, wantDaysLeft: 3,
		},
		{
			// The refresh has expired but the access token is still alive: nothing can renew
			// it any more, yet turns keep running until this token expires. It must not call
			// itself dead (refusing sends would stop a session that still works). The grace is
			// only hours, though, so the card warns "soon" — Soon is set.
			name: "refresh expired, access alive", access: now.Add(3 * time.Hour), refresh: now.Add(-time.Hour),
			wantKnown: true, wantSoon: true, wantDaysLeft: 0,
		},
		{
			// The shape that actually happens: the last access token issued (taken just before
			// the refresh deadline) has expired too.
			name: "both expired", access: now.Add(-8*day + 8*time.Hour), refresh: now.Add(-8 * day),
			wantKnown: true, wantDead: true,
		},
		{
			// While running on a token from the environment, this credentials file is not used.
			name: "running on an env token", access: now.Add(-8*day + 8*time.Hour), refresh: now.Add(-8 * day),
			envToken: true,
		},
		{
			// The shape where claude itself declines to judge (access beyond refresh + 3d).
			name: "not the subscription OAuth shape", access: now.Add(30 * day), refresh: now.Add(2 * day),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := expiryOf(credsAt(c.access, c.refresh), c.envToken)
			if e.Known != c.wantKnown {
				t.Fatalf("Known = %v, want %v", e.Known, c.wantKnown)
			}
			if got := e.Dead(now); got != c.wantDead {
				t.Errorf("Dead = %v, want %v", got, c.wantDead)
			}
			if got := e.Soon(now); got != c.wantSoon {
				t.Errorf("Soon = %v, want %v", got, c.wantSoon)
			}
			if c.wantKnown {
				if got := e.DaysLeft(now); got != c.wantDaysLeft {
					t.Errorf("DaysLeft = %d, want %d", got, c.wantDaysLeft)
				}
			}
		})
	}
}

// A record without refreshTokenExpiresAt (claude itself declines to judge on the same
// condition) is "unknown", not "not expired" — it must not be used to decide.
func TestExpiryNoRefreshDeadline(t *testing.T) {
	var c credsFile
	c.ClaudeAiOauth.AccessToken = "x"
	c.ClaudeAiOauth.ExpiresAt = ms(time.Now().Add(-time.Hour))
	if e := expiryOf(c, false); e.Known || e.Dead(time.Now()) {
		t.Fatalf("Known=%v Dead=%v — judging with nothing to judge on", e.Known, e.Dead(time.Now()))
	}
}

// writeCredsExpiry puts a credentials file into an isolated CLAUDE_CONFIG_DIR.
func writeCredsExpiry(t *testing.T, dir string, access, refresh time.Time) {
	t.Helper()
	body := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"tok","refreshToken":"rt","expiresAt":%d,`+
		`"refreshTokenExpiresAt":%d,"subscriptionType":"max"}}`, ms(access), ms(refresh))
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	resetCredCache()
}

// isolateClaudeConfig points ConfigDir() at a temp dir so the real fleet's credentials are never
// read (a test touching the real CLI's config has happened once in this repository).
func isolateClaudeConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	resetCredCache()
	t.Cleanup(resetCredCache)
	return dir
}

func TestCredentialExpiryFromFile(t *testing.T) {
	dir := isolateClaudeConfig(t)

	// No file = not connected. With nothing to judge on, Known=false (= sends are not refused).
	if e := CredentialExpiry(); e.Known {
		t.Fatalf("Known=true with no credentials: %+v", e)
	}
	if AuthExpired() {
		t.Fatal("claiming expired auth merely because there are no credentials")
	}

	now := time.Now()
	writeCredsExpiry(t, dir, now.Add(-10*24*time.Hour+8*time.Hour), now.Add(-10*24*time.Hour))
	if !AuthExpired() {
		t.Fatal("credentials with both deadlines past are judged alive")
	}

	// Credentials written back by a re-login must take effect at once (the stat cache must not
	// stick).
	writeCredsExpiry(t, dir, now.Add(8*time.Hour), now.Add(30*24*time.Hour))
	if AuthExpired() {
		t.Fatal("still expired after a re-login — the cache is holding the stale verdict")
	}
	if got := oauthToken(); got != "tok" {
		t.Errorf("oauthToken = %q, want %q (the same read must be shared)", got, "tok")
	}
}
