package memoryx

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The name is a safe single path segment: underscores and dots are allowed (claude's own files
// use them), but nothing that escapes the directory or reaches the tombstone directory.
func TestAgentMemoryNameTable(t *testing.T) {
	cases := []struct {
		name string
		ok   bool
	}{
		{"project_95-invent", true},
		{"_leading-underscore", true},
		{"v1.2.3-notes", true},
		{"a", true},
		{strings.Repeat("a", 64), true},
		{strings.Repeat("a", 65), false},
		{"", false},
		{"..", false},
		{"a..b", false},
		{".x", false},
		{".forgotten", false},
		{"-x", false},
		{"a/b", false},
		{"a\\b", false},
		{"Upper", false},
		{"sp ace", false},
		{"日本語", false},
	}
	for _, c := range cases {
		if got := agentMemValidName(c.name); got != c.ok {
			t.Errorf("agentMemValidName(%q) = %v, want %v", c.name, got, c.ok)
		}
	}
	// The same rule gates a save, so a name never gets to a path unchecked.
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	for _, n := range []string{"..", ".forgotten", "-x", strings.Repeat("a", 65), "a/b", "Upper"} {
		if _, err := agentMemSave(c, agentMemSaveReq{Name: n, Description: "d", Body: "b"}, time.Now()); agentMemCode(err) != errCodeMemoryBadRequest {
			t.Errorf("save %q: %v, want bad_request", n, err)
		}
	}
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "project_95-invent", Description: "d", Body: "b"}, time.Now()); err != nil {
		t.Fatalf("underscore name: %v", err)
	}
	// ...and the repo path of its commit is read back by the history list.
	ch, err := agentMemListChanges(10)
	if err != nil || len(ch.Changes) != 1 || ch.Changes[0].Name != "project_95-invent" || ch.Withheld != 0 {
		t.Errorf("changes = %+v, %v", ch, err)
	}
	for _, p := range []string{"af/user/a..b.md", "af/user/.x.md", "af/user/-x.md"} {
		if _, _, _, _, ok := agentMemParseRepoPath(p); ok {
			t.Errorf("repo path %q accepted", p)
		}
	}
	if _, _, _, n, ok := agentMemParseRepoPath("af/user/_a.b-c.md"); !ok || n != "_a.b-c" {
		t.Errorf("repo path with _ and . refused")
	}
}

// Description: characters, not bytes; one line; its own limit. Warnings never refuse.
func TestAgentMemoryDescriptionLimitsAndWarnings(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	save := func(name, desc, body string) (agentMemWriteResult, error) {
		return agentMemSave(c, agentMemSaveReq{Name: name, Description: desc, Body: body}, time.Now())
	}

	// 500 characters: saved, with the guidance.
	res, err := save("long-desc", strings.Repeat("a", 500), "b")
	if err != nil || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "500 characters") {
		t.Fatalf("500-char description: %+v, %v", res, err)
	}
	// The 300 boundary: 300 is fine, 301 warns.
	if res, err = save("d300", strings.Repeat("a", 300), "b"); err != nil || len(res.Warnings) != 0 {
		t.Errorf("300 characters: %+v, %v", res, err)
	}
	if res, err = save("d301", strings.Repeat("a", 301), "b"); err != nil || len(res.Warnings) != 1 {
		t.Errorf("301 characters: %+v, %v", res, err)
	}
	// Japanese is counted in characters: 300 of them are 900 bytes and still do not warn.
	if res, err = save("ja300", strings.Repeat("あ", 300), "b"); err != nil || len(res.Warnings) != 0 {
		t.Errorf("300 Japanese characters: %+v, %v", res, err)
	}
	if res, err = save("ja2000", strings.Repeat("あ", 2000), "b"); err != nil || len(res.Warnings) != 1 {
		t.Errorf("2000 Japanese characters: %+v, %v", res, err)
	}
	if _, err = save("ja2001", strings.Repeat("あ", 2001), "b"); agentMemCode(err) != errCodeMemoryBadRequest {
		t.Errorf("2001 characters: %v, want bad_request", err)
	}
	if _, err = save("crlf", "one\rtwo", "b"); agentMemCode(err) != errCodeMemoryBadRequest {
		t.Errorf("CR in description: %v", err)
	}

	// The body warning: 4096 bytes is fine, 4097 warns.
	if res, err = save("b4096", "d", strings.Repeat("x", 4096)); err != nil || len(res.Warnings) != 0 {
		t.Errorf("4096-byte body: %+v, %v", res, err)
	}
	if res, err = save("b4097", "d", strings.Repeat("x", 4097)); err != nil || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "4096 bytes") {
		t.Errorf("4097-byte body: %+v, %v", res, err)
	}
	// Both at once give two warnings.
	if res, err = save("both", strings.Repeat("a", 400), strings.Repeat("x", 5000)); err != nil || len(res.Warnings) != 2 {
		t.Errorf("both: %+v, %v", res, err)
	}
	// The saved text is the full one: a warning never shortens.
	if e, err := agentMemRead(c, "", "long-desc"); err != nil || len(e.Description) != 500 {
		t.Errorf("read back = %d, %v", len(e.Description), err)
	}
}

// Body: 200 KiB is stored; one byte more is too_large. The line is one line on purpose.
func TestAgentMemoryBodyLimit(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	res, err := agentMemSave(c, agentMemSaveReq{Name: "max", Description: "d", Body: strings.Repeat("x", 200<<10)}, time.Now())
	if err != nil || len(res.Warnings) != 1 {
		t.Fatalf("200 KiB body: %+v, %v", res, err)
	}
	if e, err := agentMemRead(c, "", "max"); err != nil || len(e.Body) != 200<<10 {
		t.Errorf("read back: %d, %v", len(e.Body), err)
	}
	_, err = agentMemSave(c, agentMemSaveReq{Name: "over", Description: "d", Body: strings.Repeat("x", 200<<10+1)}, time.Now())
	if agentMemCode(err) != errCodeMemoryTooLarge {
		t.Errorf("200 KiB + 1: %v, want too_large", err)
	}
	// The largest legal memory (body and a 2000-character Japanese description) fits the
	// whole-file bound.
	if _, err = agentMemSave(c, agentMemSaveReq{Name: "full", Description: strings.Repeat("あ", 2000), Body: strings.Repeat("x", 200<<10)}, time.Now()); err != nil {
		t.Errorf("largest legal memory: %v", err)
	}
	if _, err := agentMemRead(c, "", "full"); err != nil {
		t.Errorf("read of the largest legal memory: %v", err)
	}
}

// A line longer than the old 4 KiB limit is stored, and the scan still reads all of it: a key
// past 4 KiB (and past 8 KiB) is found on save and on read.
func TestAgentMemoryLongLinesAreScannedWhole(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	long := strings.Repeat("word ", 2000) // 10 KB on one line
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "wide", Description: "d", Body: long}, time.Now()); err != nil {
		t.Fatalf("long line refused: %v", err)
	}
	for _, pad := range []int{4097, 9000, 150 << 10} {
		body := strings.Repeat("x", pad) + " " + agentMemFakeAWS()
		_, err := agentMemSave(c, agentMemSaveReq{Name: "key", Description: "d", Body: body}, time.Now())
		if _, ok := err.(*agentMemSecretErr); !ok {
			t.Errorf("secret after %d bytes on one line: %v, want a refusal", pad, err)
		}
	}

	// On read: a hand-placed file with the key past 4 KiB is withheld.
	dir := filepath.Join(agentMemDir(), "user")
	memoryMkdirAll(t, dir)
	memoryWrite(t, filepath.Join(dir, "planted.md"),
		"---\nname: planted\ndescription: d\n---\n"+strings.Repeat("x", 5000)+" "+agentMemFakeAWS()+"\n")
	if _, err := agentMemRead(c, "", "planted"); err == nil {
		t.Error("a planted key beyond 4 KiB was shown")
	}
	idx, err := agentMemListIndex(c, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range idx.Entries {
		if e.Name == "planted" {
			t.Error("a planted key beyond 4 KiB was indexed")
		}
	}
	if idx.Withheld < 1 {
		t.Errorf("withheld = %d, want the planted file counted", idx.Withheld)
	}
	if _, err := os.Stat(filepath.Join(dir, "planted.md")); err != nil {
		t.Errorf("withheld file must stay on disk: %v", err)
	}
}

// The HTTP bound is on the JSON, the limit is on the decoded text: a body full of characters
// that JSON escapes is still a body of at most 200 KiB.
func TestAgentMemorySaveHandlerBodyLimitIsOnDecodedText(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	mux := buildMux()
	post := func(name, text string) int {
		body, _ := json.Marshal(agentMemSaveReq{Session: "claude-main", Name: name, Description: "d", Body: text})
		return smokeDo(t, mux, http.MethodPost, "/agents/memory/entries", "", string(body)).Code
	}
	for name, text := range map[string]string{
		"quotes":   strings.Repeat(`"`, 200<<10),
		"controls": strings.Repeat("\x01", 200<<10),
		"newlines": strings.Repeat("a\n", 100<<10),
	} {
		if code := post(name, text); code != http.StatusOK {
			t.Errorf("%s at 200 KiB: status %d, want 200", name, code)
		}
	}
	if code := post("over", strings.Repeat(`"`, 200<<10+1)); code != http.StatusRequestEntityTooLarge {
		t.Errorf("200 KiB + 1: status %d, want 413", code)
	}
}
