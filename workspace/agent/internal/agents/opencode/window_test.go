package opencode

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// writeTestConfig puts a config file where engineConfigPath() will find it, for a HOME the
// caller has already pointed at a temp dir (engineTestHome).
func writeTestConfig(t *testing.T, body string) {
	t.Helper()
	path := engineConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	invalidateWindows()
}

// The number the mirror shows has to be the number the engine was started with. For a
// self-hosted model that is ONE value travelling from store.EngineModel.ContextTokens to
// llama-server's --ctx-size and to opencode's limit.context, so reading it back out of the
// config is reading the same declaration — and the turn stops falling back to
// usagex.WindowGuess, which reads a self-hosted id as an unknown non-Claude model.
func TestModelWindowIsTheDeclaredEngineWindow(t *testing.T) {
	engineTestHome(t)
	_, _, err := WriteEngineProviders([]EngineProvider{{
		Key: "llm", Provider: "llamacpp",
		BaseURL: "https://cp.example.com/engine/llm/v1/",
		Models:  []string{"qwen3.8-27b-ud-iq3_s"},
		Windows: map[string]EngineModelWindow{
			"qwen3.8-27b-ud-iq3_s": {ContextTokens: 32768, MaxOutputTokens: 8192},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	invalidateWindows() // WriteEngineProviders does this through InvalidateModels; be explicit

	win := modelWindowLookup()
	if got := win("llamacpp", "qwen3.8-27b-ud-iq3_s"); got != 32768 {
		t.Errorf("window = %d, want 32768 (llama-server's --ctx-size)", got)
	}
	// The guess this replaces, so the test says out loud what the regression looked like:
	// a 25k conversation on a 32k engine rendered as 13% full.
	if guess := usagex.WindowGuess("qwen3.8-27b-ud-iq3_s", 25000); guess != 200_000 {
		t.Errorf("WindowGuess = %d, want the 200000 fallback this fix stops using", guess)
	}
}

// A provider af did not write (anthropic, bedrock, …) has no declaration here: opencode
// knows those windows from its own catalogue. 0 must keep the Console's guess rather than
// render every such turn against a window of zero.
func TestModelWindowUnknownProviderStaysZero(t *testing.T) {
	engineTestHome(t)
	writeTestConfig(t, `{"provider":{"llamacpp":{"models":{"m1":{"limit":{"context":8192,"output":2048}}}}}}`)

	win := modelWindowLookup()
	for _, c := range []struct{ p, m string }{
		{"anthropic", "claude-opus-5"},
		{"llamacpp", "not-declared"},
		{"", "m1"},
		{"llamacpp", ""},
	} {
		if got := win(c.p, c.m); got != 0 {
			t.Errorf("window(%q,%q) = %d, want 0", c.p, c.m, got)
		}
	}
	if got := win("llamacpp", "m1"); got != 8192 {
		t.Errorf("window(llamacpp,m1) = %d, want 8192", got)
	}
}

// A model whose window nobody declared is written WITHOUT a limit (engineProviderEntry:
// both numbers or neither), and a zero must not be reported as a window — opencode reads a
// context of 0 as "auto-compaction off", and a gauge against 0 is a division by zero.
func TestModelWindowZeroIsNotAWindow(t *testing.T) {
	engineTestHome(t)
	writeTestConfig(t, `{"provider":{"llamacpp":{"models":{"m1":{"limit":{"context":0,"output":0}},"m2":{}}}}}`)

	win := modelWindowLookup()
	if got := win("llamacpp", "m1"); got != 0 {
		t.Errorf("window(m1) = %d, want 0", got)
	}
	if got := win("llamacpp", "m2"); got != 0 {
		t.Errorf("window(m2) = %d, want 0", got)
	}
}

// The poll must survive every shape a config file can be in. opencode.jsonc may legally
// carry comments encoding/json cannot read, and a workspace with no engines has no file at
// all — neither is an error worth failing a transcript read over.
func TestModelWindowUnreadableConfigIsNotAnError(t *testing.T) {
	engineTestHome(t)
	if got := len(readModelWindows(engineConfigPath())); got != 0 {
		t.Errorf("no file: %d entries, want 0", got)
	}
	writeTestConfig(t, "{\n  // a comment opencode allows and encoding/json does not\n  \"provider\": {}\n}")
	if got := len(readModelWindows(engineConfigPath())); got != 0 {
		t.Errorf("jsonc: %d entries, want 0", got)
	}
}

// End to end: an assistant message carrying providerID + modelID comes out of the
// transcript reader with the window recorded, which is what makes AggregateUsage report
// WindowSource "recorded" instead of "estimated".
func TestReadSessionRecordsTheDeclaredWindow(t *testing.T) {
	engineTestHome(t)
	writeTestConfig(t, `{"provider":{"llamacpp":{"models":{"qwen3.8-27b-ud-iq3_s":{"limit":{"context":32768,"output":8192}}}}}}`)

	db := newOpencodeTestDB(t)
	ses := "ses_w"
	insMsg(t, db, "m1", ses, 1000, `{"role":"assistant","modelID":"qwen3.8-27b-ud-iq3_s","providerID":"llamacpp",`+
		`"tokens":{"input":1000,"output":20,"cache":{"read":24000,"write":0}},"time":{"created":1000,"completed":2000}}`)
	insPart(t, db, "p1", "m1", ses, 1, `{"type":"text","text":"done"}`)
	// A turn from a provider with no declaration keeps 0, in the same conversation.
	insMsg(t, db, "m2", ses, 3000, `{"role":"assistant","modelID":"claude-opus-5","providerID":"anthropic",`+
		`"tokens":{"input":10,"output":5,"cache":{"read":0,"write":0}},"time":{"created":3000,"completed":4000}}`)
	insPart(t, db, "p2", "m2", ses, 1, `{"type":"text","text":"also done"}`)

	turns := readSession(db, ses)
	if len(turns) != 2 {
		t.Fatalf("want 2 turns, got %d", len(turns))
	}
	if turns[0].CtxWindow != 32768 {
		t.Errorf("engine turn CtxWindow = %d, want 32768", turns[0].CtxWindow)
	}
	if turns[1].CtxWindow != 0 {
		t.Errorf("anthropic turn CtxWindow = %d, want 0 (the Console guesses)", turns[1].CtxWindow)
	}
}
