package claudeaudit

import (
	"strings"
	"testing"
)

const tok = "ghp_" + "0123456789abcdefghijklmnop"

// Default: only a known program and a known subcommand are recorded; no general argument,
// quoted assignment or truncated line can put a secret in the ledger.
func TestProgramTargetDefault(t *testing.T) {
	for _, c := range []struct{ info, want string }{
		{"go test ./...", "go test"},
		{"git push origin main", "git push"},
		{"/usr/bin/git push origin main", "git push"},
		{"curl -H 'Authorization: Bearer " + tok + "' https://x.test", "curl"},
		{"API_KEY=" + tok + " npm run build", "npm run"},
		{"deploy --token=" + tok, "deploy"},
		{"echo hunter2", "echo"},
		{"printf %s hunter2", "printf"},
		{"git hunter2", "git"},
		{"PASSWORD='first second' curl https://example.test", "curl"},
		{`PASSWORD="first second" curl https://example.test`, "curl"},
		{"PASSWORD='first sec", ""},
		{"TOKEN=abc", ""},
		{"", ""},
		{"$(echo hunter2)", ""},
	} {
		got := ProgramTarget(c.info)
		if got != c.want {
			t.Errorf("ProgramTarget(%q) = %q, want %q", c.info, got, c.want)
		}
		if strings.Contains(got, tok) || strings.Contains(got, "hunter2") || strings.Contains(got, "second") {
			t.Errorf("secret leaked: %q", got)
		}
		if BashTarget(c.info, false) != got {
			t.Errorf("BashTarget default differs")
		}
	}
}

// Opt-in full lines are still masked, quoted forms and cut-off lines included.
func TestMaskCommandSecrets(t *testing.T) {
	for _, c := range []struct{ info, want string }{
		{"API_KEY=" + tok + " npm run build", "API_KEY=*** npm run build"},
		{"curl -H 'Authorization: Bearer " + tok + "' https://x.test", "curl -H 'Authorization: Bearer ***' https://x.test"},
		{"deploy --token " + tok + " --env prod", "deploy --token *** --env prod"},
		{"deploy --password=hunter2", "deploy --password=***"},
		{"deploy --password 'hunter2'", "deploy --password ***"},
		{`deploy --password "hunter 2" --x`, "deploy --password *** --x"},
		{`PASSWORD="first second" curl https://x.test`, "PASSWORD=*** curl https://x.test"},
		{"PASSWORD='first second' curl https://x.test", "PASSWORD=*** curl https://x.test"},
		{`PASSWORD="first sec`, "PASSWORD=***"},
		{"deploy --password 'hunt", "deploy --password ***"},
		{"git clone https://me:" + tok + "@host.test/r.git", "git clone https://me:***@host.test/r.git"},
		{"echo " + tok, "echo ***"},
		{"go test ./...", "go test ./..."},
	} {
		got := MaskCommandSecrets(c.info)
		if got != c.want {
			t.Errorf("MaskCommandSecrets(%q) = %q, want %q", c.info, got, c.want)
		}
		if strings.Contains(got, tok) || strings.Contains(got, "hunter") || strings.Contains(got, "second") || strings.Contains(got, "first") {
			t.Errorf("secret leaked: %q", got)
		}
	}
}

// Backslash escapes and concatenated quoted / unquoted runs are one shell word: neither the
// default target nor the masked line may split a secret at an escaped space or quote.
func TestShellEscapesAndConcatenation(t *testing.T) {
	for _, c := range []struct{ info, def, full string }{
		{`PASSWORD=first\ second curl https://example.test`, "curl", "PASSWORD=*** curl https://example.test"},
		{`PASSWORD="first \" second third" curl https://example.test`, "curl", "PASSWORD=*** curl https://example.test"},
		{`deploy --password first\ second`, "deploy", "deploy --password ***"},
		{`deploy --password "first \" second third"`, "deploy", "deploy --password ***"},
		{`PASSWORD='first'"second" curl https://example.test`, "curl", "PASSWORD=*** curl https://example.test"},
		{`PASSWORD='ab'\''cd' curl https://example.test`, "curl", "PASSWORD=*** curl https://example.test"},
		{`PASSWORD=ab\`, "", "PASSWORD=***"},
		{`PASSWORD="first \" sec`, "", "PASSWORD=***"},
		{`PASSWORD='ab'\''c`, "", "PASSWORD=***"},
	} {
		if got := ProgramTarget(c.info); got != c.def {
			t.Errorf("ProgramTarget(%q) = %q, want %q", c.info, got, c.def)
		}
		got := MaskCommandSecrets(c.info)
		if got != c.full {
			t.Errorf("MaskCommandSecrets(%q) = %q, want %q", c.info, got, c.full)
		}
		for _, leak := range []string{"first", "second", "third", "ab", "cd"} {
			if strings.Contains(got, leak) && !strings.Contains(c.full, leak) {
				t.Errorf("%q leaked %q: %q", c.info, leak, got)
			}
		}
	}
}

// An expansion is not parsed: the word runs to the end of the line, so the default target
// omits it and the full line masks from the value to the end.
func TestShellExpansionsFailSafe(t *testing.T) {
	for _, c := range []struct{ info, def, full string }{
		{`PASSWORD=$(echo hunter2 | base64) curl https://example.test`, "", "PASSWORD=***"},
		{`PASSWORD=$(printf first second) curl https://example.test`, "", "PASSWORD=***"},
		{`deploy --password $(echo hunter2)`, "deploy", "deploy --password ***"},
		{`PASSWORD="$(printf "%s" "my verysecret")" curl https://example.test`, "", "PASSWORD=***"},
		{"PASSWORD=`echo first second` curl https://example.test", "", "PASSWORD=***"},
		{`PASSWORD=${SECRET:-first second} curl https://example.test`, "", "PASSWORD=***"},
		{`PASSWORD=$((1 + 2)) curl https://example.test`, "", "PASSWORD=***"},
		{`echo $(cat secret-file hunter2)`, "echo", `echo $(cat secret-file hunter2)`},
	} {
		if got := ProgramTarget(c.info); got != c.def {
			t.Errorf("ProgramTarget(%q) = %q, want %q", c.info, got, c.def)
		}
		got := MaskCommandSecrets(c.info)
		if got != c.full {
			t.Errorf("MaskCommandSecrets(%q) = %q, want %q", c.info, got, c.full)
		}
		for _, leak := range []string{"hunter2", "first", "second", "verysecret"} {
			if strings.Contains(got, leak) && !strings.Contains(c.full, leak) {
				t.Errorf("%q leaked %q: %q", c.info, leak, got)
			}
		}
	}
}
