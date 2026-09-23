package imagegen

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestStripStudioSignal(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"背景を夜にして\n[studio v4 · 下書きが変わった · 新しい結果 2 → get_image_studio]", "背景を夜にして"},
		{"背景を夜にして\n\n[studio v1 → get_image_studio]\n", "背景を夜にして"},
		{"[studio v1 → get_image_studio]", ""},
		// The member's own words, not the signal: not the last line, or not a whole line.
		{"[studio v1 → get_image_studio]\nこれは本文", "[studio v1 → get_image_studio]\nこれは本文"},
		{"本文 [studio v1]", "本文 [studio v1]"},
		{"本文\n[studio v1 はまだ閉じていない", "本文\n[studio v1 はまだ閉じていない"},
		{"[agent-fleet:peer from=a] 直して", "[agent-fleet:peer from=a] 直して"},
		{"", ""},
	} {
		if got := StripStudioSignal(c.in); got != c.want {
			t.Errorf("StripStudioSignal(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The Console strips the same line in the transcript model; the two prefixes are one decision.
func TestStudioSignalPrefixMatchesTheConsole(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "console", "src", "features", "mirror", "transcript", "model.ts"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`export const STUDIO_SIGNAL_PREFIX = "([^"]*)";`).FindSubmatch(src)
	if m == nil {
		t.Fatal("STUDIO_SIGNAL_PREFIX not found in model.ts")
	}
	if string(m[1]) != StudioSignalPrefix {
		t.Fatalf("Console prefix %q, Go prefix %q", m[1], StudioSignalPrefix)
	}
	if StudioSignalPrefix[0] == '<' {
		t.Fatal("the prefix starts with '<', which the mirror's isNoise hides as a system line")
	}
}
