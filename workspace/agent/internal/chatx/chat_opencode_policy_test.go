package chatx

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routepolicy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// recordingOpencode puts an `opencode` on PATH that records the environment of every `run` and
// answers with one text event; it returns the recorded environment of the last run.
func recordingOpencode(t *testing.T) func() []string {
	t.Helper()
	fakeOpencode(t, "@opencode/cli", "2.0.28")
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "env")
	script := "#!/bin/sh\nif [ \"$1\" = run ]; then env > '" + log + "'; " +
		`echo '{"type":"text","sessionID":"ses_1","part":{"id":"p1","type":"text","text":"a title"}}'; fi` + "\n"
	if err := os.WriteFile(filepath.Join(bin, "opencode"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() []string {
		b, err := os.ReadFile(log)
		if err != nil {
			return nil
		}
		return strings.Split(strings.TrimSpace(string(b)), "\n")
	}
}

// A title one-shot on opencode, with a retained opencode.ai login (its account state unknown, so
// it might be signed in) beside an allowed OpenRouter key, runs isolated under a restriction: its
// pin lists only openrouter, so opencode's own small-model and title calls cannot reach opencode.ai,
// and an inherited opencode.ai key is not passed. A one-shot named on opencode.ai is refused before
// it runs, with no ledger row. With no restriction the run is not pinned.
func TestOpencodeOneShotCannotReachARetainedLogin(t *testing.T) {
	writePrefs(t, `{"aiShortModels":{"opencode":"openrouter/some-model"}}`) // sets HOME
	t.Setenv("AF_SECRET_KEY", "")
	t.Setenv("AF_USAGE_DIR", t.TempDir())
	t.Setenv("OPENCODE_API_KEY", "inherited")
	if err := secrets.Update(func(s *secrets.Data) error {
		s.Opencode = map[string]string{"OPENROUTER_API_KEY": "or"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	prevPref := opencode.UsagePref
	opencode.UsagePref = func() string { return opencode.UsageZen }
	t.Cleanup(func() { opencode.UsagePref = prevPref })
	env := recordingOpencode(t)

	useUtilityPolicy(t, routepolicy.Policy{Revision: 1, Allowed: []string{}})
	if _, _, _, err := oneShotRunOn(context.Background(), session.KindOpencode, "title", OneShotShort, "", "hi", ""); err != nil {
		t.Fatalf("control: unrestricted one-shot: %v", err)
	}
	if got := env(); slices.ContainsFunc(got, func(e string) bool { return strings.Contains(e, "enabled_providers") }) {
		t.Error("control: the unrestricted run was pinned")
	}

	useUtilityPolicy(t, routepolicy.Policy{Revision: 2, Restricted: true, Allowed: []string{routes.RouteOpenRouterAPI}})
	if _, _, _, err := oneShotRunOn(context.Background(), session.KindOpencode, "title", OneShotShort, "", "hi", ""); err != nil {
		t.Fatalf("a one-shot on the allowed route: %v", err)
	}
	got := env()
	if slices.Contains(got, "OPENCODE_API_KEY=inherited") {
		t.Error("the inherited opencode.ai key reached the run")
	}
	var cfg struct {
		Enabled []string `json:"enabled_providers"`
	}
	for _, e := range got {
		if v, ok := strings.CutPrefix(e, "OPENCODE_CONFIG_CONTENT="); ok {
			if err := json.Unmarshal([]byte(v), &cfg); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !slices.Equal(cfg.Enabled, []string{"openrouter"}) {
		t.Errorf("enabled_providers = %v, want only openrouter", cfg.Enabled)
	}

	before := len(usagex.ReadRows())
	if err := os.WriteFile(uiprefs.Path(), []byte(`{"aiShortModels":{"opencode":"opencode/a-model"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := oneShotRunOn(context.Background(), session.KindOpencode, "title", OneShotShort, "", "hi", ""); !errors.Is(err, ErrRoutePolicy) {
		t.Errorf("a one-shot named on opencode.ai ran: %v", err)
	}
	if n := len(usagex.ReadRows()); n != before {
		t.Errorf("the refused one-shot left %d ledger rows", n-before)
	}
}

// The assistant chat on opencode runs isolated the same way, and a global config that would widen
// the pin refuses it under a restriction as a policy refusal, before any ledger row.
func TestOpencodeChatRunsIsolatedUnderThePolicy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SECRET_KEY", "")
	t.Setenv("AF_USAGE_DIR", t.TempDir())
	t.Setenv("OPENCODE_API_KEY", "inherited")
	if err := secrets.Update(func(s *secrets.Data) error {
		s.Opencode = map[string]string{"OPENROUTER_API_KEY": "or"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	prevPref := opencode.UsagePref
	opencode.UsagePref = func() string { return opencode.UsageZen }
	t.Cleanup(func() { opencode.UsagePref = prevPref })
	env := recordingOpencode(t)
	useUtilityPolicy(t, routepolicy.Policy{Revision: 2, Restricted: true, Allowed: []string{routes.RouteOpenRouterAPI}})
	c := &ChatConversation{ID: "0b6f4c55-7c1f-4f4e-9a59-1d2f3e4a5b6c", Agent: session.KindOpencode, Model: "openrouter/some-model"}
	if _, err := (opencodeChat{}).Send(context.Background(), c, "hi"); err != nil {
		t.Fatalf("a chat on the allowed route: %v", err)
	}
	got := env()
	if slices.Contains(got, "OPENCODE_API_KEY=inherited") || !slices.ContainsFunc(got, func(e string) bool {
		return strings.HasPrefix(e, "OPENCODE_CONFIG_CONTENT=") && strings.Contains(e, `"enabled_providers":["openrouter"]`)
	}) {
		t.Errorf("the chat run was not isolated:\n%s", strings.Join(got, "\n"))
	}

	g := filepath.Join(paths.OpencodeConfigDir(), "opencode.json")
	if err := os.MkdirAll(filepath.Dir(g), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(g, []byte(`{"enabled_providers":["opencode"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	before := len(usagex.ReadRows())
	if _, err := (opencodeChat{}).Send(context.Background(), c, "hi"); !errors.Is(err, ErrRoutePolicy) {
		t.Errorf("a chat over a config that widens the pin: %v", err)
	}
	if n := len(usagex.ReadRows()); n != before {
		t.Errorf("the refused chat left %d ledger rows", n-before)
	}
}
