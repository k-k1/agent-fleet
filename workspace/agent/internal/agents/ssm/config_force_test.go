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
// and role credentials and nothing of another profile's. A legacy profile with an expired
// token still works from its role credentials, so those must survive as well as its token.
func TestSSMForceLoginKeepsOtherProfiles(t *testing.T) {
	if _, err := exec.LookPath("sha1sum"); err != nil {
		t.Skip("sha1sum not installed")
	}
	sha := func(s string) string { sum := sha1.Sum([]byte(s)); return hex.EncodeToString(sum[:]) }

	// A stand-in aws that answers only `configure get <key> [--sso-session <name>]`, from
	// keys/<profile>/<key> or keys/sso-session.<name>/<key>, and fails like the CLI when the
	// section does not set the key.
	bin := t.TempDir()
	keys := t.TempDir()
	fake := "#!/bin/sh\n[ \"$1 $2\" = 'configure get' ] || exit 1\nd=\"" + keys + "/$AWS_PROFILE\"\n" +
		"[ \"$4\" = --sso-session ] && d=\"" + keys + "/sso-session.$5\"\n[ -f \"$d/$3\" ] && exec cat \"$d/$3\"\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "aws"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	set := func(section string, kv map[string]string) {
		os.MkdirAll(filepath.Join(keys, section), 0o755)
		for k, v := range kv {
			os.WriteFile(filepath.Join(keys, section, k), []byte(v+"\n"), 0o644)
		}
	}
	// The role is set on the sso-session section only, which botocore inherits.
	set("p1", map[string]string{"sso_session": "af-p1", "sso_account_id": "111111111111"})
	set("sso-session.af-p1", map[string]string{"sso_role_name": "R"})
	set("legacy", map[string]string{"sso_start_url": "https://example.invalid/legacy", "sso_account_id": "222222222222", "sso_role_name": "L"})
	// A key the JSON would escape: the shell cannot hash it, so every SSO role cache goes.
	set("team", map[string]string{"sso_session": "チーム", "sso_account_id": "333333333333", "sso_role_name": "T"})

	// Role-credential keys as aws-cli 2.36.46 wrote them for p1 and legacy.
	const p1Role = "a06b6ab6dc4ed12c8f306b4e97a614066a604a50"
	const legacyRole = "84703169645f2ec62e4fc0bb5f4df05c39e442a1"
	for _, tc := range []struct {
		profile, token, role string
		allRoles             bool
	}{
		{"p1", sha("af-p1"), p1Role, false},
		{"legacy", sha("https://example.invalid/legacy"), legacyRole, false},
		{"team", sha("チーム"), "", true},
	} {
		t.Run(tc.profile, func(t *testing.T) {
			home := t.TempDir()
			tokens := filepath.Join(home, ".aws", "sso", "cache")
			roles := filepath.Join(home, ".aws", "cli", "cache")
			os.MkdirAll(tokens, 0o700)
			os.MkdirAll(roles, 0o700)
			// Role-credential files as aws-cli 2.36.46 writes them.
			ssoRole := `{"ProviderType": "sso", "Credentials": {"AccessKeyId": "AKIAFAKE"}}`
			otherRole := filepath.Join(roles, strings.Repeat("0", 40)+".json")
			files := map[string]string{
				filepath.Join(tokens, tc.token+".json"):     "{}",
				filepath.Join(tokens, sha("af-p2")+".json"): "{}", // another profile's login
				filepath.Join(tokens, sha("own")+".json"):   "{}", // the member's own sso-session
				otherRole:                                ssoRole, // another SSO profile's role credentials
				filepath.Join(roles, "assume-role.json"): `{"Credentials": {"AccessKeyId": "AKIAFAKE"}}`,
				filepath.Join(roles, "session.db"):       "",
			}
			gone := map[string]bool{filepath.Join(tokens, tc.token+".json"): true}
			if tc.role != "" {
				files[filepath.Join(roles, tc.role+".json")] = ssoRole
				gone[filepath.Join(roles, tc.role+".json")] = true
			}
			if tc.allRoles {
				gone[otherRole] = true
			}
			for f, body := range files {
				if err := os.WriteFile(f, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
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
