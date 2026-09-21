package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

func translateCall(t *testing.T, name, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/sessions/"+name+"/translate", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.SetPathValue("name", name)
	w := httptest.NewRecorder()
	handleSessionTranslate(w, r)
	return w
}

func translationsCall(t *testing.T, name, query string) *httptest.ResponseRecorder {
	t.Helper()
	url := "/sessions/" + name + "/translations"
	if query != "" {
		url += "?" + query
	}
	r := httptest.NewRequest(http.MethodGet, url, nil)
	r.SetPathValue("name", name)
	w := httptest.NewRecorder()
	handleSessionTranslations(w, r)
	return w
}

type translateResp struct {
	Lang  string `json:"lang"`
	Parts []struct {
		Hash   string `json:"hash"`
		Text   string `json:"text"`
		Cached bool   `json:"cached"`
	} `json:"parts"`
}

func decodeTranslate(t *testing.T, w *httptest.ResponseRecorder) translateResp {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp translateResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, w.Body.String())
	}
	return resp
}

// stubTranslate replaces the generation seam and counts the runs, which is the only way to tell
// "served from the cache" from "ran a model again" — the two produce the same JSON otherwise.
// The stub always reports "claude" as the backend that ran: that is also what
// translateCachePin predicts in this test binary (nothing is authenticated, so
// PreferredAssistAgent falls back to DefaultHeadlessOrder[0]), which is what makes a second
// press actually hit the cache instead of missing on a kind mismatch.
func stubTranslate(t *testing.T, reply func(text, lang string) string) *int {
	t.Helper()
	calls := 0
	prev := translateOneShot
	translateOneShot = func(_ context.Context, text, lang string) (string, string, string, error) {
		calls++
		return reply(text, lang), "claude", "stub-model", nil
	}
	t.Cleanup(func() { translateOneShot = prev })
	return &calls
}

func seedTranslateSession(t *testing.T, name string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude})
}

// The hash is the cache key on BOTH sides of the wire, so the Go and the TypeScript
// implementations have to agree byte for byte. These vectors are the contract; the twin
// assertion is console/src/features/mirror/translate.test.ts.
//
// The Japanese and emoji vectors are the ones that matter: a JS implementation that hashes
// UTF-16 code units instead of UTF-8 bytes passes every ASCII case and silently keys a
// different cache entry for every real answer.
func TestTranslateHashVectors(t *testing.T) {
	want := map[string]string{
		"":                    "811c9dc51f2be47c",
		"hello":               "4f9f2cabc425dbfc",
		"こんにちは":               "1cfa9ccd6dcd6ab2",
		"🙂":                   "57a37a4beec1d01e",
		"# Heading\n\nbody\n": "0f79ab9eda6468f3",
	}
	for in, exp := range want {
		if got := translateHash(in); got != exp {
			t.Errorf("translateHash(%q) = %s, want %s (did the JS twin change?)", in, got, exp)
		}
	}
	// Different inputs must not collide on the visible prefix either: the Console shows the
	// hash nowhere, but a truncated comparison would reintroduce the collision the two halves
	// are there to avoid.
	if translateHash("a") == translateHash("b") {
		t.Fatal("distinct inputs hashed the same")
	}
}

func TestTranslateCachesBySourceHash(t *testing.T) {
	const name = "tr1"
	seedTranslateSession(t, name)
	calls := stubTranslate(t, func(text, lang string) string { return "訳:" + text })

	first := decodeTranslate(t, translateCall(t, name, `{"to":"ja","parts":[{"text":"Done. All green."}]}`))
	if len(first.Parts) != 1 || first.Parts[0].Text != "訳:Done. All green." {
		t.Fatalf("unexpected first reply: %+v", first)
	}
	if first.Parts[0].Cached {
		t.Fatal("first press reported cached")
	}
	if *calls != 1 {
		t.Fatalf("model ran %d times on the first press", *calls)
	}

	// Same source, same language: no model run at all. This is what makes re-pressing, a pane
	// re-open and a second reader free.
	second := decodeTranslate(t, translateCall(t, name, `{"to":"ja","parts":[{"text":"Done. All green."}]}`))
	if !second.Parts[0].Cached || second.Parts[0].Text != first.Parts[0].Text {
		t.Fatalf("second press did not hit the cache: %+v", second)
	}
	if *calls != 1 {
		t.Fatalf("cache hit still ran the model (%d calls)", *calls)
	}

	// The target language is part of the key: the same answer into another language is another
	// entry, not the first one handed back in the wrong language.
	en := decodeTranslate(t, translateCall(t, name, `{"to":"en","parts":[{"text":"Done. All green."}]}`))
	if en.Lang != "en" || en.Parts[0].Cached || *calls != 2 {
		t.Fatalf("language is not part of the cache key: %+v calls=%d", en, *calls)
	}
	if en.Parts[0].Hash != first.Parts[0].Hash {
		t.Fatal("the hash is of the SOURCE, so it must not change with the target language")
	}
}

// The WIRE reply must not name a model: anything this handler could put there is the REQUEST,
// not the outcome — ADR 0029 §1's rule for `kind`, one level up in the display. The STORE is the
// opposite since docs/log/103 decision 8: it must carry the backend/model that actually
// translated the text, because a feature can now be pinned to one, and pinning a different one
// must not silently serve up a translation the new pin never produced (session_translate.go's
// lookupTranslation comment).
//
// Measured on a live host (2026-09-13, pre-103): the wire reply claimed "sonnet" while the
// ledger recorded the run as agy, because that workspace lists agy first in Settings > AI
// assistance — the reason the wire reply still says nothing about the model.
func TestTranslateReplyClaimsNoModel(t *testing.T) {
	const name = "tr8"
	seedTranslateSession(t, name)
	stubTranslate(t, func(text, lang string) string { return "訳" })

	body := translateCall(t, name, `{"to":"ja","parts":[{"text":"hello there"}]}`).Body.String()
	if strings.Contains(body, "model") {
		t.Fatalf("the reply names a model, which it cannot know: %s", body)
	}
	stored, err := os.ReadFile(sessionTranslationsPath(name))
	if err != nil {
		t.Fatal(err)
	}
	// Kind always lands (it never drifts from what the run reports). Model does NOT — nothing
	// pinned this feature or its tier to a concrete model here, and 103-impl-review 重大4 is
	// exactly the bug where the store recorded the RUN's model (the stub's "stub-model") instead
	// of the setting-derived value the next read will compare against.
	if !strings.Contains(string(stored), `"kind":"claude"`) {
		t.Fatalf("the store must keep the backend that actually ran: %s", stored)
	}
	if strings.Contains(string(stored), "stub-model") || strings.Contains(string(stored), `"model"`) {
		t.Fatalf("an unconfigured feature must not write a model into the store: %s", stored)
	}
}

// The ledger records who pressed. An automatic press (docs/log/97 §97.12) spends without a
// reader asking, so filing it as "manual" would put a run nobody made under the reader's own
// hand — the same class of untruth ADR 0029 §1 forbids for `kind`.
func TestTranslateRecordsWhichPressAsked(t *testing.T) {
	const name = "tr9"
	seedTranslateSession(t, name)
	var seen []string
	prev := translateOneShot
	translateOneShot = func(ctx context.Context, text, lang string) (string, string, string, error) {
		tag, _ := usagex.TagOf(ctx)
		seen = append(seen, tag.Feature+"/"+tag.Trigger)
		return "訳:" + text, "claude", "stub-model", nil
	}
	t.Cleanup(func() { translateOneShot = prev })

	decodeTranslate(t, translateCall(t, name, `{"to":"ja","parts":[{"text":"pressed by hand"}]}`))
	decodeTranslate(t, translateCall(t, name, `{"to":"ja","trigger":"auto","parts":[{"text":"pressed on completion"}]}`))
	// Unknown values are the reader's own press: a typo must not invent a third kind of run.
	decodeTranslate(t, translateCall(t, name, `{"to":"ja","trigger":"nonsense","parts":[{"text":"pressed oddly"}]}`))

	want := []string{"translate.mirror/manual", "translate.mirror/auto", "translate.mirror/manual"}
	if len(seen) != len(want) {
		t.Fatalf("runs=%v want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("run %d tagged %q, want %q", i, seen[i], want[i])
		}
	}
}

// Two readers on the same answer at the same moment — one at the desk, one on a phone, or simply
// two panes with automatic translation on, which makes the collision the normal case rather than
// a coincidence. The store only dedupes once a run has FINISHED (17 s, measured), so without the
// in-flight join both would pay for the same text.
func TestTranslateRunsOnceForSimultaneousAsks(t *testing.T) {
	const name = "tr10"
	seedTranslateSession(t, name)
	release := make(chan struct{})
	entered := make(chan struct{}, 4)
	var mu sync.Mutex
	calls := 0
	prev := translateOneShot
	translateOneShot = func(_ context.Context, text, lang string) (string, string, string, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		entered <- struct{}{}
		<-release
		return "訳:" + text, "claude", "stub-model", nil
	}
	t.Cleanup(func() { translateOneShot = prev })

	const body = `{"to":"ja","trigger":"auto","parts":[{"text":"the very same answer"}]}`
	results := make(chan translateResp, 2)
	ask := func() {
		w := translateCall(t, name, body)
		var resp translateResp
		if w.Code == http.StatusOK {
			_ = json.Unmarshal(w.Body.Bytes(), &resp)
		}
		results <- resp
	}
	go ask()
	// The second ask is made only once the first is provably inside the model — that window is
	// the only thing this test is about.
	<-entered
	go ask()
	select {
	case <-entered:
		t.Fatal("a second model run started while the first was still in flight")
	case <-time.After(100 * time.Millisecond):
		// Nothing started. The second ask is either waiting on the flight or about to be served
		// by the store the leader is about to write; both are one run.
	}
	close(release)

	a, b := <-results, <-results
	if calls != 1 {
		t.Fatalf("model runs=%d, want 1", calls)
	}
	for _, r := range []translateResp{a, b} {
		if len(r.Parts) != 1 || r.Parts[0].Text != "訳:the very same answer" {
			t.Fatalf("a waiter got %+v, want the leader's translation", r)
		}
	}
	// One of the two answers says it paid for nothing, and exactly one did run.
	if a.Parts[0].Cached == b.Parts[0].Cached {
		t.Fatalf("cached=%v/%v: exactly one of the two asks ran a model", a.Parts[0].Cached, b.Parts[0].Cached)
	}
}

// A turn's parts are cached one by one, so appending a paragraph and pressing again re-runs the
// model for the new part only.
func TestTranslateReusesUnchangedParts(t *testing.T) {
	const name = "tr2"
	seedTranslateSession(t, name)
	calls := stubTranslate(t, func(text, lang string) string { return "訳:" + text })

	decodeTranslate(t, translateCall(t, name, `{"to":"ja","parts":[{"text":"first"}]}`))
	got := decodeTranslate(t, translateCall(t, name, `{"to":"ja","parts":[{"text":"first"},{"text":"second"}]}`))
	if len(got.Parts) != 2 || !got.Parts[0].Cached || got.Parts[1].Cached {
		t.Fatalf("per-part caching broken: %+v", got)
	}
	if *calls != 2 {
		t.Fatalf("want 2 model runs (first, second), got %d", *calls)
	}
}

func TestTranslateRejectsUnusableInput(t *testing.T) {
	const name = "tr3"
	seedTranslateSession(t, name)
	calls := stubTranslate(t, func(text, lang string) string { return "訳" })

	cases := []struct {
		name, body, code string
	}{
		{"no parts", `{"to":"ja","parts":[]}`, errCodeTranslateEmpty},
		{"blank part", `{"to":"ja","parts":[{"text":"   "}]}`, errCodeTranslateEmpty},
		{"too long", `{"to":"ja","parts":[{"text":"` + strings.Repeat("x", translateMaxPartBytes+1) + `"}]}`, errCodeTranslateTooLong},
		{"too many parts", `{"to":"ja","parts":[` + strings.Repeat(`{"text":"x"},`, translateMaxParts) + `{"text":"x"}]}`, errCodeTranslateTooLong},
	}
	for _, c := range cases {
		w := translateCall(t, name, c.body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status=%d body=%s", c.name, w.Code, w.Body.String())
			continue
		}
		var resp struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		if resp.Error.Code != c.code {
			t.Errorf("%s: code=%q want %q (body=%s)", c.name, resp.Error.Code, c.code, w.Body.String())
		}
	}
	if *calls != 0 {
		t.Fatalf("a rejected request still spent %d model runs", *calls)
	}
}

// Settings > AI assistance can turn the feature off; then the endpoint refuses before anything
// is spent. The Console hides the button on the same setting, so this is the second gate.
func TestTranslateHonoursTheSetting(t *testing.T) {
	const name = "tr4"
	seedTranslateSession(t, name)
	calls := stubTranslate(t, func(text, lang string) string { return "訳" })

	prefs := filepath.Join(os.Getenv("HOME"), ".config", "agent-fleet")
	if err := os.MkdirAll(prefs, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prefs, "ui-prefs.json"), []byte(`{"mirrorTranslateEnabled":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	w := translateCall(t, name, `{"to":"ja","parts":[{"text":"hello"}]}`)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), errCodeTranslateDisabled) {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if *calls != 0 {
		t.Fatalf("the disabled feature still ran the model %d times", *calls)
	}
}

// What the pane asks for when it opens: everything already translated into the language it
// reads in, and nothing from the other one.
func TestTranslationsListPerLanguage(t *testing.T) {
	const name = "tr5"
	seedTranslateSession(t, name)
	stubTranslate(t, func(text, lang string) string { return lang + ":" + text })

	ja := decodeTranslate(t, translateCall(t, name, `{"to":"ja","parts":[{"text":"alpha"}]}`))
	decodeTranslate(t, translateCall(t, name, `{"to":"en","parts":[{"text":"ベータ"}]}`))

	var got struct {
		Lang    string            `json:"lang"`
		Entries map[string]string `json:"entries"`
	}
	w := translationsCall(t, name, "lang=ja")
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Lang != "ja" || len(got.Entries) != 1 || got.Entries[ja.Parts[0].Hash] != "ja:alpha" {
		t.Fatalf("ja listing wrong: %+v", got)
	}
	if got.Entries[translateHash("ベータ")] != "" {
		t.Fatal("an entry from the other language leaked into the listing")
	}
}

func TestCleanTranslation(t *testing.T) {
	cases := []struct {
		name, reply, source, want string
		wantErr                   bool
	}{
		{name: "plain", reply: "  訳文  ", source: "text", want: "訳文"},
		{
			// Models wrap the whole answer in a fence however firmly the persona forbids it.
			name: "whole answer fenced", reply: "```markdown\n# 見出し\n本文\n```", source: "# Heading\nbody", want: "# 見出し\n本文",
		},
		{
			// The source WAS code, so the fence is content: unwrapping would turn the reader's
			// code block into prose.
			name: "source is a code block", reply: "```go\nfmt.Println()\n```", source: "```go\nfmt.Println()\n```", want: "```go\nfmt.Println()\n```",
		},
		{
			// A translated answer that contains its own code block keeps every fence.
			name: "inner fences", reply: "説明\n\n```sh\nls\n```\n\nおわり", source: "text\n\n```sh\nls\n```\n\nend", want: "説明\n\n```sh\nls\n```\n\nおわり",
		},
		{name: "empty", reply: "   ", source: "text", wantErr: true},
	}
	for _, c := range cases {
		got, err := cleanTranslation(c.reply, c.source)
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: want an error, got %q", c.name, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

// Eviction keeps the newest: a translation is regenerable, so losing the oldest costs one press,
// while an uncapped file grows for the life of the session.
func TestTranslationStoreEvictsOldest(t *testing.T) {
	const name = "tr6"
	seedTranslateSession(t, name)
	list := make([]*sessionTranslation, 0, translateStoreMaxEntries+5)
	for i := 0; i < translateStoreMaxEntries+5; i++ {
		list = append(list, &sessionTranslation{Hash: translateHash(string(rune('a'+i%26)) + string(rune(i))), Lang: "ja", Text: "t"})
	}
	trimmed := trimTranslations(list)
	if len(trimmed) != translateStoreMaxEntries {
		t.Fatalf("entry cap not applied: %d", len(trimmed))
	}
	if trimmed[len(trimmed)-1] != list[len(list)-1] {
		t.Fatal("eviction dropped the newest entry instead of the oldest")
	}

	big := []*sessionTranslation{
		{Hash: "a", Lang: "ja", Text: strings.Repeat("x", translateStoreMaxBytes)},
		{Hash: "b", Lang: "ja", Text: strings.Repeat("y", translateStoreMaxBytes)},
	}
	if got := trimTranslations(big); len(got) != 1 || got[0].Hash != "b" {
		t.Fatalf("byte cap not applied: %+v", got)
	}
}

// Session names are slot names and get reused, so the store has to go with the session.
func TestTranslationsRemovedWithTheSession(t *testing.T) {
	const name = "tr7"
	seedTranslateSession(t, name)
	stubTranslate(t, func(text, lang string) string { return "訳" })
	decodeTranslate(t, translateCall(t, name, `{"to":"ja","parts":[{"text":"hello"}]}`))
	if _, err := os.Stat(sessionTranslationsPath(name)); err != nil {
		t.Fatalf("nothing was stored: %v", err)
	}

	removeSessionSideFiles(name)

	if _, err := os.Stat(sessionTranslationsPath(name)); !os.IsNotExist(err) {
		t.Fatalf("translation store survived deletion, stat err=%v", err)
	}
}

// A pre-docs/log/103 entry has no Kind/Model at all — that dimension did not exist when it was
// written. It must still answer a press today (docs/log/103 §103.9 migration 1); otherwise an
// upgrade alone empties every existing translation and "same text again is free"
// (aiassist.note_mirror_translate) breaks for every reader, not just the ones who touch the new
// per-feature setting.
func TestTranslateReadsLegacyKeyEntries(t *testing.T) {
	const name = "tr11"
	seedTranslateSession(t, name)
	calls := stubTranslate(t, func(text, lang string) string { return "新訳:" + text })

	hash := translateHash("legacy text")
	if err := writeSessionTranslations(name, []*sessionTranslation{
		{Hash: hash, Lang: "ja", Text: "旧訳", CreatedAt: time.Now().UnixMilli()},
	}); err != nil {
		t.Fatal(err)
	}

	resp := decodeTranslate(t, translateCall(t, name, `{"to":"ja","parts":[{"text":"legacy text"}]}`))
	if !resp.Parts[0].Cached || resp.Parts[0].Text != "旧訳" {
		t.Fatalf("the pre-103 entry was not served as a hit: %+v", resp)
	}
	if *calls != 0 {
		t.Fatalf("a legacy hit must not run the model (%d calls)", *calls)
	}
}

// TestLookupTranslationDefersResolveForLegacyHits pins 103-impl-review 軽12 at the unit level:
// a legacy (pre-103) entry must answer WITHOUT ever calling resolve — resolve is exactly what
// can shell out (translateCacheModel -> oneShotKind -> headlessAgentAvailable's exec fallback
// on a cold cache), so a press that only touches legacy entries (the common case right after an
// upgrade) must cost no more than it did before this feature existed.
func TestLookupTranslationDefersResolveForLegacyHits(t *testing.T) {
	seedTranslateSession(t, "tr-legacy-defer")
	const name = "tr-legacy-defer"
	hash := translateHash("legacy only")
	if err := writeSessionTranslations(name, []*sessionTranslation{
		{Hash: hash, Lang: "ja", Text: "旧訳", CreatedAt: time.Now().UnixMilli()},
	}); err != nil {
		t.Fatal(err)
	}
	panicResolve := func() (string, string, bool) {
		t.Fatal("resolve was called for a legacy-only hit — it must never run")
		return "", "", false
	}
	if got := lookupTranslation(name, hash, "ja", panicResolve); got == nil || got.Text != "旧訳" {
		t.Fatalf("legacy entry not returned: %+v", got)
	}
}

// TestTranslationsPrefetchNeverShellsOutOnAColdCache is 103-final-review 軽6: the prefetch (GET
// /sessions/{name}/translations) used to resolve the cache key's kind through the SAME path the
// press uses (translateCacheModel -> chatx.ResolveOneShot -> oneShotKind), which can shell out
// to a vendor CLI on a cold 1-minute availability cache. Opening a pane must not cost what
// pressing the translate button costs — a stored NON-legacy entry (the case that needs a
// resolution at all) with the availability cache forced cold must still answer without ever
// calling the real check.
func TestTranslationsPrefetchNeverShellsOutOnAColdCache(t *testing.T) {
	const name = "tr-prefetch-cold"
	seedTranslateSession(t, name)
	hash := translateHash("prefetch cold test")
	if err := writeSessionTranslations(name, []*sessionTranslation{
		{Hash: hash, Lang: "ja", Kind: session.KindClaude, Model: "sonnet", Text: "訳あり", CreatedAt: time.Now().UnixMilli()},
	}); err != nil {
		t.Fatal(err)
	}
	// Clear every candidate kind, not just claude: headlessAvailAt/headlessAvail are package
	// globals shared by the whole test binary, and oneShotKindCached scans the WHOLE priority
	// order — any OTHER test that warmed a different kind's cache and outlived its own t.Cleanup
	// would make this resolution "known" through that kind instead, defeating the point.
	for _, k := range chatx.DefaultHeadlessOrder {
		t.Cleanup(chatx.ClearHeadlessAvailableForTest(k))
	}
	t.Cleanup(chatx.SetHeadlessAvailCheckForTest(func(kind string) bool {
		t.Fatalf("headlessAvailCheck ran for %q — the prefetch must never shell out (軽6)", kind)
		return false
	}))

	w := translationsCall(t, name, "lang=ja")
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var got translationsReply
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Unknown resolution (cache cold) degrades to showing the stored entry anyway
	// (translateCacheModelPeek's doc), rather than guessing at a match OR paying to find out.
	if got.Entries[hash] != "訳あり" {
		t.Fatalf("a cold-cache prefetch should still show the stored entry: %+v", got.Entries)
	}
}

// Pinning a feature to a different concrete model is decision 8's one case where a cached
// translation must go stale: "what translated it" is now part of its identity. Leaving the
// feature UNPINNED (the default) must not behave this way — that path is covered by every
// other cache test in this file re-using the same "claude" prediction across presses.
// TestTranslatePinRoutesToPinnedKindAndInvalidatesOnModelChange replaces
// TestTranslateModelPinChangeMissesCache (103-impl-review 中7): the old test pinned
// translate.mirror to "claude", which is ALSO DefaultHeadlessOrder's own first (and, in this
// test binary, only reachable) choice — nothing is authenticated, so headlessAgentAvailable
// returns false for every kind and preferredFrom falls through to order[0], which happens to
// be "claude" too. The pin and the fallback were indistinguishable: a mutation that deleted the
// pin branch entirely left this test green (confirmed with SetHeadlessAvailableForTest reproducing
// the same mutation chat_resolve_test.go's TestOneShotKindPinBeatsPriorityOrder catches).
//
// This version forces claude AND codex both "available", pins to codex — the one the plain
// priority order would NOT pick — and checks the STORED entry's Kind to prove the pin, not the
// fallback, is what actually ran.
func TestTranslatePinRoutesToPinnedKindAndInvalidatesOnModelChange(t *testing.T) {
	const name = "tr12"
	seedTranslateSession(t, name)
	t.Cleanup(chatx.SetHeadlessAvailableForTest("claude", true))
	t.Cleanup(chatx.SetHeadlessAvailableForTest("codex", true))

	calls := 0
	prev := translateOneShot
	translateOneShot = func(_ context.Context, text, lang string) (string, string, string, error) {
		calls++
		return "訳:" + text, "codex", "", nil
	}
	t.Cleanup(func() { translateOneShot = prev })

	prefsDir := filepath.Join(os.Getenv("HOME"), ".config", "agent-fleet")
	if err := os.MkdirAll(prefsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	pin := func(model string) {
		body := `{"aiFeatureAgents":{"translate.mirror":"codex"},` +
			`"aiFeatureModels":{"translate.mirror":{"codex":"` + model + `"}}}`
		if err := os.WriteFile(filepath.Join(prefsDir, "ui-prefs.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	pin("model-a")
	decodeTranslate(t, translateCall(t, name, `{"to":"ja","parts":[{"text":"pin test"}]}`))
	if calls != 1 {
		t.Fatalf("first press: calls=%d, want 1", calls)
	}
	stored, err := os.ReadFile(sessionTranslationsPath(name))
	if err != nil {
		t.Fatal(err)
	}
	// The pin names codex, the plain priority order would pick claude first — so seeing codex
	// here is proof the PIN decided this, not the fallback (the bug this test replaces).
	if !strings.Contains(string(stored), `"kind":"codex"`) {
		t.Fatalf("the store must record the PINNED kind, not the priority order's own pick: %s", stored)
	}

	same := decodeTranslate(t, translateCall(t, name, `{"to":"ja","parts":[{"text":"pin test"}]}`))
	if !same.Parts[0].Cached || calls != 1 {
		t.Fatalf("re-pressing under the same pin must hit the cache: cached=%v calls=%d", same.Parts[0].Cached, calls)
	}

	pin("model-b")
	changed := decodeTranslate(t, translateCall(t, name, `{"to":"ja","parts":[{"text":"pin test"}]}`))
	if changed.Parts[0].Cached || calls != 2 {
		t.Fatalf("changing the pin's model must not reuse the old model's translation: cached=%v calls=%d",
			changed.Parts[0].Cached, calls)
	}
}

// TestTranslateWriteKeyUsesResolvedKindNotRunKind is the regression test for 103-final-review
// 中1 / commit 9465d035: the write side must take Kind from resolveCacheModel(), the SAME
// resolution the next lookup will compare against — never from what the run itself reported.
// Before that fix, Kind alone still took the run's value while Model already took the resolved
// value, so a run that reported a different kind than the current resolution wrote a row no
// future lookup could ever match: every press of the same text ran the model again, forever
// (103-final-review measured this as "3 presses, 3 runs" via agy's agyChatModel silently
// dropping a catalog-miss id to ""; the stub below reproduces the same shape — run and
// resolution disagreeing on kind — without needing a live CLI).
func TestTranslateWriteKeyUsesResolvedKindNotRunKind(t *testing.T) {
	const name = "tr-kind-drift"
	seedTranslateSession(t, name)

	calls := 0
	prev := translateOneShot
	translateOneShot = func(_ context.Context, text, lang string) (string, string, string, error) {
		calls++
		// The run claims codex; nothing is pinned and nothing is authenticated in this test
		// binary, so resolution falls through to DefaultHeadlessOrder[0] ("claude") — the drift
		// this test exists to catch.
		return "訳:" + text, "codex", "", nil
	}
	t.Cleanup(func() { translateOneShot = prev })

	body := `{"to":"ja","parts":[{"text":"kind drift test"}]}`
	decodeTranslate(t, translateCall(t, name, body))
	if calls != 1 {
		t.Fatalf("first press: calls=%d, want 1", calls)
	}

	stored, err := os.ReadFile(sessionTranslationsPath(name))
	if err != nil {
		t.Fatal(err)
	}
	// The row must carry the RESOLVED kind (claude), never the run's own claim (codex) — a row
	// keyed on a kind no lookup will ever predict is written once and never read again.
	if !strings.Contains(string(stored), `"kind":"claude"`) {
		t.Fatalf("stored row must use the resolved kind, not the run's reported kind: %s", stored)
	}
	if strings.Contains(string(stored), `"kind":"codex"`) {
		t.Fatalf("stored row must not carry the run's own reported kind: %s", stored)
	}

	second := decodeTranslate(t, translateCall(t, name, body))
	if !second.Parts[0].Cached || calls != 1 {
		t.Fatalf("re-pressing the same text must hit the cache the first write made: cached=%v calls=%d",
			second.Parts[0].Cached, calls)
	}

	third := decodeTranslate(t, translateCall(t, name, body))
	if !third.Parts[0].Cached || calls != 1 {
		t.Fatalf("third press must still hit the cache, not run a third time: cached=%v calls=%d",
			third.Parts[0].Cached, calls)
	}
}

// TestTranslateTierModelChangeMissesCache is the permanent version of the probe
// 103-impl-review ran by hand (中8): ② (the shared tier default, aiProseModels — no per-feature
// pin at all) is also part of the cache key, not just ① (an explicit pin). Without this,
// translationMatches/translateCacheModel could be narrowed back to "only when pinned" by a
// future reader who trusts the old (wrong) comments/names more than the code, and this
// regression would go uncaught.
func TestTranslateTierModelChangeMissesCache(t *testing.T) {
	const name = "tr13"
	seedTranslateSession(t, name)
	t.Cleanup(chatx.SetHeadlessAvailableForTest("claude", true))

	calls := 0
	prev := translateOneShot
	translateOneShot = func(_ context.Context, text, lang string) (string, string, string, error) {
		calls++
		return "訳:" + text, "claude", "", nil
	}
	t.Cleanup(func() { translateOneShot = prev })

	prefsDir := filepath.Join(os.Getenv("HOME"), ".config", "agent-fleet")
	if err := os.MkdirAll(prefsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	tier := func(model string) {
		body := `{"aiProseModels":{"claude":"` + model + `"}}`
		if err := os.WriteFile(filepath.Join(prefsDir, "ui-prefs.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	tier("tier-a")
	decodeTranslate(t, translateCall(t, name, `{"to":"ja","parts":[{"text":"tier test"}]}`))
	if calls != 1 {
		t.Fatalf("first press: calls=%d, want 1", calls)
	}

	tier("tier-b")
	changed := decodeTranslate(t, translateCall(t, name, `{"to":"ja","parts":[{"text":"tier test"}]}`))
	if changed.Parts[0].Cached || calls != 2 {
		t.Fatalf("changing the TIER default model (no pin involved) must miss the cache: cached=%v calls=%d",
			changed.Parts[0].Cached, calls)
	}
}

// TestTranslatePrefetchMatchesPress is 103-impl-review 重大1: GET /translations (the prefetch
// the mirror uses when a pane opens) must apply the SAME kind/model match POST /translate does.
// Before this fix it returned the whole session's hash->text map regardless of Kind/Model, so a
// feature re-pinned to a different agent/model could show one translation on open and a
// DIFFERENT one on press, for the identical source text.
func TestTranslatePrefetchMatchesPress(t *testing.T) {
	const name = "tr14"
	seedTranslateSession(t, name)
	t.Cleanup(chatx.SetHeadlessAvailableForTest("claude", true))
	t.Cleanup(chatx.SetHeadlessAvailableForTest("codex", true))

	prefsDir := filepath.Join(os.Getenv("HOME"), ".config", "agent-fleet")
	if err := os.MkdirAll(prefsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	pin := func(kind, model string) {
		body := `{"aiFeatureAgents":{"translate.mirror":"` + kind + `"},` +
			`"aiFeatureModels":{"translate.mirror":{"` + kind + `":"` + model + `"}}}`
		if err := os.WriteFile(filepath.Join(prefsDir, "ui-prefs.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	prev := translateOneShot
	t.Cleanup(func() { translateOneShot = prev })

	// A -> B -> A: by the third step, the store holds TWO entries for this hash — A's (written
	// first) and B's (written second, when the pin briefly moved away). A naive "last entry in
	// append order wins" reader (the bug) would show B's here, even though the pin is back on A
	// and the press itself correctly serves A's cached entry.
	pin("claude", "model-a")
	translateOneShot = func(_ context.Context, text, lang string) (string, string, string, error) {
		return "PIN-A-TRANSLATION", "claude", "", nil
	}
	first := decodeTranslate(t, translateCall(t, name, `{"to":"ja","parts":[{"text":"prefetch test"}]}`))
	hash := first.Parts[0].Hash

	pin("codex", "model-b")
	translateOneShot = func(_ context.Context, text, lang string) (string, string, string, error) {
		return "PIN-B-TRANSLATION", "codex", "", nil
	}
	decodeTranslate(t, translateCall(t, name, `{"to":"ja","parts":[{"text":"prefetch test"}]}`))

	pin("claude", "model-a")
	back := decodeTranslate(t, translateCall(t, name, `{"to":"ja","parts":[{"text":"prefetch test"}]}`))
	if !back.Parts[0].Cached || back.Parts[0].Text != "PIN-A-TRANSLATION" {
		t.Fatalf("returning to pin A must hit ITS cached entry: %+v", back)
	}

	// The prefetch, read at this same moment (pinned to A again), must agree with the press.
	list := translationsCall(t, name, "lang=ja")
	var got translationsReply
	if err := json.Unmarshal(list.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Entries[hash] != "PIN-A-TRANSLATION" {
		t.Fatalf("prefetch disagrees with the press: prefetch=%q want %q", got.Entries[hash], "PIN-A-TRANSLATION")
	}
}

// TestTranslatePrefetchMatchesPressAfterASingleRepin is 103-final-review 中2:
// TestTranslatePrefetchMatchesPress's A->B->A round trip is satisfiable by a naive "first entry
// in append order wins" prefetch reader alone, with translationMatches never actually consulted
// — by the third press the pin is back on A, and A also happens to be the FIRST entry the store
// ever wrote for this hash, so the two shortcuts (order, and the current pin) agree by
// coincidence (confirmed by mutation: disabling translationMatches in handleSessionTranslations
// left that test green). A->B, without returning to A, is the more ordinary case a reader hits
// (re-pin once, keep working) and it is NOT satisfiable by order alone: B is the SECOND entry
// appended, so only an actual kind/model match — not append order — can make the prefetch agree
// with a press made while pinned to B.
func TestTranslatePrefetchMatchesPressAfterASingleRepin(t *testing.T) {
	const name = "tr15"
	seedTranslateSession(t, name)
	t.Cleanup(chatx.SetHeadlessAvailableForTest("claude", true))
	t.Cleanup(chatx.SetHeadlessAvailableForTest("codex", true))

	prefsDir := filepath.Join(os.Getenv("HOME"), ".config", "agent-fleet")
	if err := os.MkdirAll(prefsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	pin := func(kind, model string) {
		body := `{"aiFeatureAgents":{"translate.mirror":"` + kind + `"},` +
			`"aiFeatureModels":{"translate.mirror":{"` + kind + `":"` + model + `"}}}`
		if err := os.WriteFile(filepath.Join(prefsDir, "ui-prefs.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	prev := translateOneShot
	t.Cleanup(func() { translateOneShot = prev })

	pin("claude", "model-a")
	translateOneShot = func(_ context.Context, text, lang string) (string, string, string, error) {
		return "PIN-A-TRANSLATION", "claude", "", nil
	}
	first := decodeTranslate(t, translateCall(t, name, `{"to":"ja","parts":[{"text":"single repin test"}]}`))
	hash := first.Parts[0].Hash

	// Re-pin once and stop — A's entry (written above) is still the FIRST row in the store for
	// this hash, so an order-only reader would keep answering with it.
	pin("codex", "model-b")
	translateOneShot = func(_ context.Context, text, lang string) (string, string, string, error) {
		return "PIN-B-TRANSLATION", "codex", "", nil
	}
	press := decodeTranslate(t, translateCall(t, name, `{"to":"ja","parts":[{"text":"single repin test"}]}`))
	if press.Parts[0].Text != "PIN-B-TRANSLATION" {
		t.Fatalf("press under the new pin must answer with B's translation: %+v", press)
	}

	list := translationsCall(t, name, "lang=ja")
	var got translationsReply
	if err := json.Unmarshal(list.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Entries[hash] != "PIN-B-TRANSLATION" {
		t.Fatalf("prefetch disagrees with the press after a single re-pin: prefetch=%q want %q",
			got.Entries[hash], "PIN-B-TRANSLATION")
	}
}
