package ssm

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/cloudruns"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// The pane's identity line and run row (ADR 0126 decision 3): both come from Settings, say so,
// and carry nothing else of the shell.

// runProgram runs the generated pane program with a fake aws that prints its arguments, and
// returns what the pane printed.
func runProgram(t *testing.T, prog string, env ...string) string {
	t.Helper()
	bin := t.TempDir()
	for name, body := range map[string]string{
		"aws":                    "#!/bin/sh\n[ \"$1\" = sts ] && exit 0\necho \"AWS-ARGS: $*\"\n",
		"session-manager-plugin": "#!/bin/sh\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("sh", "-c", prog)
	cmd.Env = append([]string{"PATH=" + bin + ":/usr/bin:/bin", "HOME=" + t.TempDir()}, env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("program: %v\n%s", err, out)
	}
	return string(out)
}

func recentRows(t *testing.T) []cloudruns.Row {
	t.Helper()
	return cloudruns.Recent(50)
}

func TestPaneNamesTheIdentityFromSettingsAndWritesARow(t *testing.T) {
	const secret = "SENTINEL-s3cr3t-value"
	m := chainMeta()
	m.SSOSession = "af-corp"
	m.Target = `i-0 b'c$SENTINEL "q"` // spaces, a quote and a "$" in the target
	prog, err := buildSSMProgram("identity1", m, false)
	if err != nil {
		t.Fatal(err)
	}
	out := runProgram(t, prog, "SENTINEL="+secret, "AWS_SECRET_ACCESS_KEY="+secret)
	want := "[Agent Fleet] AWS profile deploy (identity from Settings, not confirmed) · account 210987654321 · role deploy · region eu-west-1 · via sso-main · sign-in corp · target " + m.Target
	if !strings.Contains(out, want+"\n") {
		t.Fatalf("pane output lacks the identity line.\nwant: %s\ngot:\n%s", want, out)
	}
	if strings.Contains(out, secret) {
		t.Fatalf("the shell's environment reached the pane line:\n%s", out)
	}
	// The line comes right before the session starts.
	if i, j := strings.Index(out, "[Agent Fleet] AWS profile"), strings.Index(out, "AWS-ARGS: ssm start-session"); i < 0 || j < i {
		t.Fatalf("line must precede start-session:\n%s", out)
	}

	var row *cloudruns.Row
	for _, r := range recentRows(t) {
		if r.Target == m.Target {
			r := r
			row = &r
		}
	}
	if row == nil {
		t.Fatal("no run row for the pane")
	}
	if !row.IdentityFromSettings || row.EndedAt != "" || row.Refused != "" {
		t.Errorf("row must be marked from Settings, with no end: %+v", row)
	}
	if row.Provider != "aws" || row.Profile != "deploy" || row.Account != "210987654321" || row.Role != "deploy" ||
		row.Region != "eu-west-1" || row.Source != "sso-main" || row.SignIn != "corp" ||
		row.Program != "aws" || row.Service != "ssm" || row.Operation != "start-session" {
		t.Errorf("row = %+v", row)
	}
	b, _ := json.Marshal(row)
	if strings.Contains(string(b), secret) {
		t.Errorf("row holds the sentinel: %s", b)
	}
}

func TestPaneLineOfAPlainSSOProfile(t *testing.T) {
	line := identityLine(settingsIdentity(session.SSMMeta{Profile: "dev", Target: "i-0123456789abcdef0", AccountID: "123456789012",
		RoleName: "Dev", SSORegion: "us-east-1", StartURL: "https://x.awsapps.com/start"}).Clean())
	want := "[Agent Fleet] AWS profile dev (identity from Settings, not confirmed) · account 123456789012 · role Dev · region us-east-1 · target i-0123456789abcdef0"
	if line != want {
		t.Fatalf("line = %q", line)
	}
}

func TestPaneLineDropsValuesOutsideTheAllowlistsAndControlCharacters(t *testing.T) {
	id := settingsIdentity(session.SSMMeta{Profile: "dev", Region: "us-east-1\x1b[31m", AccountID: "12", Target: "i-1\x1b]0;pwn\x07"}).Clean()
	line := identityLine(id)
	if strings.ContainsAny(line, "\x1b\x07") || strings.Contains(line, "region") || strings.Contains(line, "account") {
		t.Fatalf("line = %q", line)
	}
}
