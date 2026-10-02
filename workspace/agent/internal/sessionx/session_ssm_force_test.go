package sessionx

import (
	"crypto/sha1"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// TestSSMForceLoginKeepsOtherProfiles: a forced re-login must drop only the SSM profile's own
// token and role credentials. A bare `aws sso logout` deleted every SSO login in the home, so
// one SSM launch signed the member out of all their other profiles.
func TestSSMForceLoginKeepsOtherProfiles(t *testing.T) {
	if _, err := exec.LookPath("sha1sum"); err != nil {
		t.Skip("sha1sum not installed")
	}
	sha := func(s string) string { sum := sha1.Sum([]byte(s)); return hex.EncodeToString(sum[:]) }
	// Role-credential keys as aws-cli 2.36.46 wrote them for these two profiles (an
	// sso_session profile and a legacy one), so a drift in the JSON the snippet hashes shows.
	const p1Role = "a06b6ab6dc4ed12c8f306b4e97a614066a604a50"
	const legacyRole = "84703169645f2ec62e4fc0bb5f4df05c39e442a1"

	// A stand-in aws that answers only `configure get <key>` for $AWS_PROFILE, from
	// keys/<profile>/<key>, and fails like the CLI when the key is not set.
	bin := t.TempDir()
	keys := t.TempDir()
	fake := "#!/bin/sh\n[ \"$1 $2\" = 'configure get' ] && [ -f \"" + keys + "/$AWS_PROFILE/$3\" ] && exec cat \"" + keys + "/$AWS_PROFILE/$3\"\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "aws"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	set := func(profile string, kv map[string]string) {
		os.MkdirAll(filepath.Join(keys, profile), 0o755)
		for k, v := range kv {
			os.WriteFile(filepath.Join(keys, profile, k), []byte(v+"\n"), 0o644)
		}
	}
	set("p1", map[string]string{"sso_session": "af-p1", "sso_account_id": "111111111111", "sso_role_name": "R"})
	set("legacy", map[string]string{"sso_start_url": "https://example.invalid/legacy", "sso_account_id": "222222222222", "sso_role_name": "L"})

	for _, tc := range []struct{ profile, token, role string }{
		{"p1", sha("af-p1"), p1Role},
		{"legacy", sha("https://example.invalid/legacy"), legacyRole},
	} {
		t.Run(tc.profile, func(t *testing.T) {
			home := t.TempDir()
			tokens := filepath.Join(home, ".aws", "sso", "cache")
			roles := filepath.Join(home, ".aws", "cli", "cache")
			os.MkdirAll(tokens, 0o700)
			os.MkdirAll(roles, 0o700)
			others := []string{
				filepath.Join(tokens, sha("af-p2")+".json"),           // another profile's login
				filepath.Join(tokens, sha("member-own")+".json"),      // the member's own sso-session
				filepath.Join(roles, strings.Repeat("0", 40)+".json"), // another role's credentials
				filepath.Join(roles, "session.db"),
			}
			mine := []string{filepath.Join(tokens, tc.token+".json"), filepath.Join(roles, tc.role+".json")}
			for _, f := range append(append([]string{}, others...), mine...) {
				if err := os.WriteFile(f, []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command("sh", "-c", ssmForgetLogin)
			cmd.Env = append(os.Environ(), "HOME="+home, "AWS_PROFILE="+tc.profile,
				"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("snippet: %v\n%s", err, out)
			}
			for _, f := range mine {
				if _, err := os.Stat(f); !os.IsNotExist(err) {
					t.Errorf("%s survived the forced re-login", f)
				}
			}
			for _, f := range others {
				if _, err := os.Stat(f); err != nil {
					t.Errorf("%s was removed: %v", f, err)
				}
			}
		})
	}

	p, err := buildSSMProgram("ssmforce", session.SSMMeta{Target: "i-0123456789abcdef0", Profile: "p1"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p, "sso logout") || !strings.Contains(p, ssmForgetLogin) {
		t.Fatalf("forced program does not use the per-profile reset:\n%s", p)
	}
}
