package chatx

// The test helpers this package owns: copies of main's helpers that only this family uses and
// that are pure. Rebuilding a helper changes how a test is DRIVEN, so README §4 applies — the
// same mutation goes on both the old and the new form to show they are equivalent (results in
// the PR body). Helpers that are not pure stay in main and are not copied here.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

// writeUIPrefs writes ui-prefs.json into an isolated HOME (the same shape as main's
// ui_prefs_test.go).
func writeUIPrefs(t *testing.T, body string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	dir := filepath.Dir(uiprefs.Path())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(uiprefs.Path(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// japaneseRanges / firstJapaneseRune / hasJapanese are a copy of main's prompt_lang_test.go,
// range table and all (all four ranges). Rewriting it with `unicode.Hiragana/Katakana/Han` drops
// CJK symbols and punctuation (0x3000-0x303F) and fullwidth alphanumerics and symbols
// (0xFF01-0xFF60) out of the decision, weakening the check. A rewrite looks like it does the same
// thing while covering a different set, so the table is carried over as is (the first attempt
// rewrote it, lost two ranges, and was caught before the mutation run).
var japaneseRanges = []*unicode.RangeTable{
	{R16: []unicode.Range16{
		{Lo: 0x3000, Hi: 0x303F, Stride: 1}, // CJK symbols and punctuation
		{Lo: 0x3040, Hi: 0x30FF, Stride: 1}, // hiragana and katakana
		{Lo: 0x4E00, Hi: 0x9FFF, Stride: 1}, // CJK unified ideographs
		{Lo: 0xFF01, Hi: 0xFF60, Stride: 1}, // fullwidth alphanumerics and symbols
	}},
}

func firstJapaneseRune(s string) rune {
	for _, r := range s {
		if unicode.IsOneOf(japaneseRanges, r) {
			return r
		}
	}
	return 0
}

func hasJapanese(s string) bool { return firstJapaneseRune(s) != 0 }

// stubChatProvider swaps the provider for one kind (the same shape as main's
// bridge_operator_test.go).
func stubChatProvider(t *testing.T, kind string, p ChatProvider) {
	t.Helper()
	old, had := ChatProviders[kind]
	ChatProviders[kind] = p
	t.Cleanup(func() {
		if had {
			ChatProviders[kind] = old
		} else {
			delete(ChatProviders, kind)
		}
	})
}

// consoleLocalesDir is the ONE path expression both consoleCatalog and
// TestConsoleCatalogIsReachable look through, so that when the depth is wrong "there is no
// catalogue" can be told apart from "we are looking in the wrong place".
//
// Only the depth of the relative path differs from the original (main's console_catalog_test.go,
// which reads the Console i18n catalogue as the bundle of per-domain files it is): there it is
// `../../console/...` from workspace/agent, here `../../../../console/...` from
// workspace/agent/internal/chatx. Forget to fix the depth and `os.ReadDir` misses, consoleCatalog
// skips through t.Skipf and the check disappears without a word — the classic way a move
// silences a test. TestConsoleCatalogIsReachable is what stops that.
func consoleLocalesDir(locale string) string {
	return filepath.Join("..", "..", "..", "..", "console", "src", "lib", "i18n", "locales", locale)
}

func consoleCatalog(t *testing.T, locale string) string {
	t.Helper()
	dir := consoleLocalesDir(locale)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("catalog not available (%v)", err)
	}
	var b strings.Builder
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".ts") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s/%s: %v", dir, e.Name(), err)
		}
		b.Write(raw)
		b.WriteString("\n")
		n++
	}
	if n == 0 {
		t.Fatalf("no .ts file at all under %s (did the catalogue move?)", dir)
	}
	return b.String()
}

func consoleCatalogHasKey(catalog, key string) bool {
	return strings.Contains(catalog, `"`+key+`"`)
}

// TestConsoleCatalogIsReachable checks that consoleCatalog has NOT fallen through to Skip. Get
// the relative-path depth wrong and the two catalogue-key checks skip with "there is no
// catalogue" and vanish quietly; this is the one test that turns that red.
//
// It must not go through `consoleCatalog`. Doing so lets `t.Skipf` skip THIS check as well when
// the depth is wrong, leaving open exactly the gap it guards (it was written that way at first
// and caught in review: SKIP went from 5 to 8 while `go test` stayed green). So it reads
// `os.ReadDir` itself and calls `t.Fatalf`.
func TestConsoleCatalogIsReachable(t *testing.T) {
	dir := consoleLocalesDir("ja")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("cannot read the catalogue directory: %s: %v (suspect the relative-path depth; "+
			"with this unreadable the NoticeKeys / ReportKeys checks skip silently)", dir, err)
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".ts") {
			n++
		}
	}
	if n == 0 {
		t.Fatalf("no .ts file at all under %s (did the catalogue move?)", dir)
	}
}

// stubAbortResumeHolds replaces only the seam that answers "is this session mid auto-resume".
//
// The earlier form wrote state straight into main's `abortResumeStates` and exercised the REAL
// decision. chatx cannot reach a var in main, so this injects the INPUT to that decision instead;
// the decision itself is covered by main's abort_resume_test.go. That changes how the test is
// driven, so per README §4 the same mutation was applied to both forms to confirm they are
// equivalent (results, and where it was applied, in the PR body).
func stubAbortResumeHolds(t *testing.T, name string, hold bool) {
	t.Helper()
	old := deps.AbortResumeHolds
	deps.AbortResumeHolds = func(n string, a claude.Abort, now time.Time) bool {
		if n == name {
			return hold
		}
		return old(n, a, now)
	}
	t.Cleanup(func() { deps.AbortResumeHolds = old })
}

// TestJapaneseRangesMatchesTheOriginal pins that this copy stays byte-identical to the original
// (RECLAIM-C, debt 2).
//
// The original is main's `prompt_lang_test.go`. Why they were not unified into one: both are
// test-only, and while production does detect Japanese it does so with DIFFERENT range tables
// (`session_title.go`'s subject formatting, the CP's `tts.go`), so there is no shared home to put
// this in. Adding a production package for a test-only table would create production code with
// zero callers. The copy is allowed; in exchange, a divergence goes red.
//
// Why the table is copied rather than rewritten: rewriting it with
// `unicode.Hiragana/Katakana/Han` drops CJK symbols and punctuation (0x3000-0x303F) and fullwidth
// alphanumerics (0xFF01-0xFF60), silently weakening the check (AG-CHAT2 actually hit this). The
// assertions and the branches taken stay identical and only the input coverage shrinks, which is
// the class of defect a two-sided mutation run cannot catch either.
func TestJapaneseRangesMatchesTheOriginal(t *testing.T) {
	const origin = "../../prompt_lang_test.go"
	extract := func(path string) string {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v (did it move?)", path, err)
		}
		s := string(b)
		i := strings.Index(s, "var japaneseRanges = ")
		if i < 0 {
			t.Fatalf("no japaneseRanges declaration found in %s: this check has gone silent", path)
		}
		j := strings.Index(s[i:], "\n}\n")
		if j < 0 {
			t.Fatalf("no end of the japaneseRanges declaration found in %s", path)
		}
		return s[i : i+j]
	}
	a, b := extract(origin), extract("helpers_test.go")
	if a != b {
		t.Fatalf("the japaneseRanges copy diverged from the original.\noriginal(%s):\n%s\ncopy:\n%s", origin, a, b)
	}
}
