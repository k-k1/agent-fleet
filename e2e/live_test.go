//go:build e2e

// Live-credential smoke (L4): the last check that the baked-in claude CLI can actually
// talk to Anthropic. Either credential works:
//   - E2E_ANTHROPIC_API_KEY   … API key (metered) -> injected as ANTHROPIC_API_KEY
//   - E2E_CLAUDE_OAUTH_TOKEN  … OAuth token from `claude setup-token`, which draws on a
//     Max/Pro subscription instead of billing -> injected as CLAUDE_CODE_OAUTH_TOKEN
//
// With neither set the test skips: it spends money or subscription quota, so it is
// opt-in and CI only runs it from the workflow_dispatch job in e2e.yml that holds the
// secrets.
//
// Runs `claude -p` (headless print mode) inside a shell session rather than an
// interactive one, so the TUI onboarding (theme picker, API-key confirmation) can never
// gate the run, and reads the output file back through the fs API. Credentials go in via
// the control plane's WS_ENV so they never appear in keystrokes or logs.
package e2e

import (
	"os"
	"testing"
	"time"
)

func TestClaudeLive(t *testing.T) {
	var env string
	switch {
	case os.Getenv("E2E_ANTHROPIC_API_KEY") != "":
		env = "WS_ENV=ANTHROPIC_API_KEY=" + os.Getenv("E2E_ANTHROPIC_API_KEY")
	case os.Getenv("E2E_CLAUDE_OAUTH_TOKEN") != "":
		env = "WS_ENV=CLAUDE_CODE_OAUTH_TOKEN=" + os.Getenv("E2E_CLAUDE_OAUTH_TOKEN")
	default:
		t.Skip("neither E2E_ANTHROPIC_API_KEY nor E2E_CLAUDE_OAUTH_TOKEN is set - skipping the live-credential smoke (opt-in)")
	}
	base := startFleet(t, "e2e-live", env)

	created := postJSON(t, base+"/api/sessions", map[string]any{"kind": "shell", "title": "e2e-live"}, 201)
	name, _ := created["name"].(string)
	if name == "" {
		t.Fatalf("session create returned no name: %v", created)
	}

	// -p is non-interactive, so no onboarding. Assert only that a fixed token appears in
	// the reply rather than matching the whole response, which would break on any change
	// in the model's phrasing. The exit code is appended so it can be observed too.
	sendPrompt(t, base, name,
		`claude -p 'Reply with exactly: E2E_OK' > live-out.txt 2>&1; echo "exit=$?" >> live-out.txt`)
	waitFileContains(t, base, "live-out.txt", "E2E_OK", 4*time.Minute)

	postJSON(t, base+"/api/sessions/"+name+"/stop", nil, 200)
	postJSON(t, base+"/api/workspace/stop", nil, 200)
}
