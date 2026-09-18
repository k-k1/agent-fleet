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
func stubTranslate(t *testing.T, reply func(text, lang string) string) *int {
	t.Helper()
	calls := 0
	prev := translateOneShot
	translateOneShot = func(_ context.Context, text, lang string) (string, error) {
		calls++
		return reply(text, lang), nil
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

// The reply must not name a model. Which backend and model actually ran is chosen inside
// OneShotHeadless and never returned, so anything this handler could put here is the REQUEST,
// not the outcome — ADR 0029 §1's rule for `kind`, one level up in the display.
//
// Measured on a live host (2026-09-13): the reply claimed "sonnet" while the ledger recorded the
// run as agy, because that workspace lists agy first in Settings > AI assistance.
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
	if strings.Contains(string(stored), "model") || strings.Contains(string(stored), "kind") {
		t.Fatalf("the store keeps a model/kind it cannot know: %s", stored)
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
	translateOneShot = func(ctx context.Context, text, lang string) (string, error) {
		tag, _ := usagex.TagOf(ctx)
		seen = append(seen, tag.Feature+"/"+tag.Trigger)
		return "訳:" + text, nil
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
	translateOneShot = func(_ context.Context, text, lang string) (string, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		entered <- struct{}{}
		<-release
		return "訳:" + text, nil
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
