package claude

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"unicode"
)

func TestStrippedCopy(t *testing.T) {
	sp := unicode.IsSpace
	cases := []struct {
		name, typed, shown string
		want               bool
	}{
		{"identical", "a b", "a b", true},
		{"whitespace aside", "a b\nc", "ab c", true},
		{"claude dropped a ZWSP, a BOM and a soft hyphen", "a\u200bb\ufeffc\u00add", "abcd", true},
		{"claude kept a joiner inside an emoji", "👩\u200d💻 ok", "👩\u200d💻 ok", true},
		{"claude kept one and dropped another", "❤\ufe0f\u200bx", "❤\ufe0fx", true},
		{"trailing invisible dropped", "ab\u200b", "ab", true},
		{"a joiner the typed text lacks", "👩💻", "👩\u200d💻", false},
		{"a variation selector the typed text lacks", "❤", "❤\ufe0f", false},
		{"a visible character missing", "abc", "ac", false},
		{"a visible character added", "ac", "abc", false},
		{"a visible tail missing", "abc", "ab", false},
		{"shown longer", "ab", "ab\u200b", false},
	}
	for _, c := range cases {
		if got := StrippedCopy(c.typed, c.shown, sp); got != c.want {
			t.Errorf("%s: StrippedCopy(%+q, %+q) = %v, want %v", c.name, c.typed, c.shown, got, c.want)
		}
	}
}

// claudeInvisible and the Console's CLAUDE_INVISIBLE_RE describe one set: the Agent compares a
// restored box with it and the Console an echo with claude's queue. A character one side drops
// and the other keeps makes the same prompt match on one side only. The Console source is read
// off disk; a checkout without console/ skips.
func TestWithoutInvisibleMatchesTheConsoleSet(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "..", "console", "src", "lib", "pastedImages.ts"))
	if os.IsNotExist(err) {
		t.Skip("console sources not available")
	}
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`const CLAUDE_INVISIBLE_RE =\s*/\[([^\]]+)\]/gu;`).FindSubmatch(raw)
	if m == nil {
		t.Fatal("CLAUDE_INVISIBLE_RE not found in pastedImages.ts (renamed or reshaped? then update this test)")
	}
	esc := `\\u(?:\{([0-9a-fA-F]+)\}|([0-9a-fA-F]{4}))`
	item := regexp.MustCompile(esc + `(?:-` + esc + `)?`)
	class := string(m[1])
	var ranges [][2]rune
	for _, g := range item.FindAllStringSubmatch(class, -1) {
		lo := hexRune(t, g[1]+g[2])
		hi := lo
		if g[3]+g[4] != "" {
			hi = hexRune(t, g[3]+g[4])
		}
		ranges = append(ranges, [2]rune{lo, hi})
	}
	if rest := item.ReplaceAllString(class, ""); rest != "" || len(ranges) < 20 {
		t.Fatalf("could not read the class: %d ranges, unparsed %q", len(ranges), rest)
	}
	inConsole := func(r rune) bool {
		for _, rg := range ranges {
			if r >= rg[0] && r <= rg[1] {
				return true
			}
		}
		return false
	}
	for r := rune(0); r <= 0x10ffff; r++ {
		if claudeInvisible(r) != inConsole(r) {
			t.Errorf("U+%04X: Agent %v, Console %v", r, claudeInvisible(r), inConsole(r))
		}
	}
}

func hexRune(t *testing.T, h string) rune {
	n, err := strconv.ParseUint(h, 16, 32)
	if err != nil {
		t.Fatal(err)
	}
	return rune(n)
}
