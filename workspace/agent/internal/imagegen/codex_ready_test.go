package imagegen

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Being logged in is not the same as having quota. The plan running out is invisible to a
// readiness check that stops at auth.json, so auto would pick this route, spend nothing and
// fail — instead of stepping aside for a provider that can actually run (ADR 0069).
func TestCodexReadyStepsAsideWhenThePlanIsExhausted(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"auth_mode":"chatgpt"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &codexProvider{model: "m", home: home, exe: "sh"} // any binary on PATH

	old := planExhausted
	t.Cleanup(func() { planExhausted = old })
	for _, tc := range []struct {
		name             string
		exhausted, known bool
		wantReady        bool
	}{
		{name: "the account says it is out of quota", exhausted: true, known: true},
		{name: "the account says it is fine", known: true, wantReady: true},
		// Unknown must NOT be read as exhausted: an endpoint hiccup would otherwise strand
		// the feature even though codex could have served the call.
		{name: "we could not find out", wantReady: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			planExhausted = func(context.Context) (bool, bool) { return tc.exhausted, tc.known }
			if got := p.Ready(context.Background()); got != tc.wantReady {
				t.Fatalf("Ready = %v, want %v", got, tc.wantReady)
			}
		})
	}
}

// The checks that come first still bite: no login is no route, whatever the quota says.
func TestCodexReadyStillNeedsALogin(t *testing.T) {
	old := planExhausted
	t.Cleanup(func() { planExhausted = old })
	planExhausted = func(context.Context) (bool, bool) { return false, true }

	p := &codexProvider{model: "m", home: t.TempDir(), exe: "sh"}
	if p.Ready(context.Background()) {
		t.Fatal("ready with no auth.json")
	}
}
