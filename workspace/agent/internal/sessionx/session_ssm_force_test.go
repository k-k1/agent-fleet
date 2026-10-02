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

// TestSSMForceLoginKeepsOtherProfiles: a forced re-login drops the SSM profile's own token
// and every SSO role credential, and nothing else. Another profile's token is what keeps
// that profile signed in, so it must survive.
func TestSSMForceLoginKeepsOtherProfiles(t *testing.T) {
	if _, err := exec.LookPath("sha1sum"); err != nil {
		t.Skip("sha1sum not installed")
	}
	sha := func(s string) string { sum := sha1.Sum([]byte(s)); return hex.EncodeToString(sum[:]) }

	// A stand-in aws that answers only `configure get <key>` for $AWS_PROFILE, from
	// keys/<profile>/<key>, and fails like the CLI when the profile does not set the key.
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
	set("p1", map[string]string{"sso_session": "af-p1"})
	set("legacy", map[string]string{"sso_start_url": "https://example.invalid/legacy"})
	// The CLI accepts a non-ASCII session name and hashes its UTF-8 bytes.
	set("team", map[string]string{"sso_session": "チーム"})

	for _, tc := range []struct{ profile, token string }{
		{"p1", sha("af-p1")},
		{"legacy", sha("https://example.invalid/legacy")},
		{"team", sha("チーム")},
	} {
		t.Run(tc.profile, func(t *testing.T) {
			home := t.TempDir()
			tokens := filepath.Join(home, ".aws", "sso", "cache")
			roles := filepath.Join(home, ".aws", "cli", "cache")
			os.MkdirAll(tokens, 0o700)
			os.MkdirAll(roles, 0o700)
			// Role-credential files as aws-cli 2.36.46 writes them.
			ssoRole := `{"ProviderType": "sso", "Credentials": {"AccessKeyId": "AKIAFAKE"}}`
			files := map[string]string{
				filepath.Join(tokens, tc.token+".json"):     "{}",
				filepath.Join(tokens, sha("af-p2")+".json"): "{}", // another profile's login
				filepath.Join(tokens, sha("own")+".json"):   "{}", // the member's own sso-session
				filepath.Join(roles, "mine.json"):           ssoRole,
				filepath.Join(roles, "other-sso.json"):      ssoRole,
				filepath.Join(roles, "assume-role.json"):    `{"Credentials": {"AccessKeyId": "AKIAFAKE"}}`,
				filepath.Join(roles, "session.db"):          "",
			}
			for f, body := range files {
				if err := os.WriteFile(f, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			gone := map[string]bool{
				filepath.Join(tokens, tc.token+".json"): true,
				filepath.Join(roles, "mine.json"):       true,
				filepath.Join(roles, "other-sso.json"):  true,
			}
			cmd := exec.Command("sh", "-c", ssmForgetLogin)
			cmd.Env = append(os.Environ(), "HOME="+home, "AWS_PROFILE="+tc.profile,
				"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("snippet: %v\n%s", err, out)
			}
			for f := range files {
				_, err := os.Stat(f)
				switch {
				case gone[f] && !os.IsNotExist(err):
					t.Errorf("%s survived the forced re-login", f)
				case !gone[f] && err != nil:
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
