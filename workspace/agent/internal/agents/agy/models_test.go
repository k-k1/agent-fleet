package agy

import "testing"

func TestParseModels(t *testing.T) {
	out := []byte("Gemini 3.5 Flash (Medium)\nGemini 3.1 Pro (High)\n\nClaude Sonnet 4.6 (Thinking)\n")
	list := parseModels(out)
	if len(list) != 3 {
		t.Fatalf("got %d models: %+v", len(list), list)
	}
	if list[0].ID != "Gemini 3.5 Flash (Medium)" || list[0].Label != list[0].ID {
		t.Fatalf("id/label mismatch: %+v", list[0])
	}
}

func TestParseModelsSkipsSignInNoise(t *testing.T) {
	if got := parseModels([]byte("Error: Please sign in to use Antigravity\n")); got != nil {
		t.Fatalf("sign-in error leaked into catalog: %+v", got)
	}
}
