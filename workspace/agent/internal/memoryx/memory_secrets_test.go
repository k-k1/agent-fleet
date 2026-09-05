package memoryx

import (
	"strings"
	"testing"
	"time"
)

// The secret scan of docs/log/39 ★4. Three things matter:
//
//	(1) real key shapes are caught (2) examples and prose are not (3) the raw value is
//	never returned.
//
// Lose (3) and a mechanism meant as a defence becomes a route that hands secrets to new
// places - API responses, the UI, the audit log.
func TestMemoryScanContentDetectsAndMasks(t *testing.T) {
	// Fake keys for the test: strings shaped like the real thing, not live credentials.
	const fakeAWS = "AKIAQWERTYUIOPASDFGH"
	const fakeGH = "ghp_" + "zzqqwwrrttyyuuiioopplkjhgfdsamnbvcxz"
	body := strings.Join([]string{
		"# メモ",
		"デプロイ鍵は " + fakeAWS + " を使う",
		"token: 使用量の話（ここは散文なので拾ってはいけない）",
		"手順書の例示: AKIAIOSFODNN7EXAMPLE",
		`password = "short"`,
		"GitHub は " + fakeGH,
		"-----BEGIN OPENSSH PRIVATE KEY-----",
	}, "\n")

	got := memoryScanContent("claude/projects/-x/memory/a.md", []byte(body))
	rules := map[string]bool{}
	for _, f := range got {
		rules[f.Rule] = true
		// (3) the raw value must not appear in any field.
		if strings.Contains(f.Hint, fakeAWS) || strings.Contains(f.Hint, fakeGH) {
			t.Fatalf("finding leaked the raw secret: %+v", f)
		}
		if f.Line <= 0 || f.Path == "" {
			t.Errorf("finding lacks a location: %+v", f)
		}
	}
	for _, want := range []string{"aws-access-key-id", "github-token", "private-key-block"} {
		if !rules[want] {
			t.Errorf("rule %q did not fire: %+v", want, got)
		}
	}
	// (2) examples (anything with EXAMPLE), prose and short values are dropped, checked by
	// line number.
	for _, f := range got {
		if f.Line == 3 || f.Line == 4 || f.Line == 5 {
			t.Errorf("false positive on line %d: %+v", f.Line, f)
		}
	}
	// Binaries are not scanned - anything but md is pure noise.
	if n := memoryScanContent("x.bin", []byte("\x00"+fakeAWS)); len(n) != 0 {
		t.Errorf("binary blob scanned: %+v", n)
	}
}

// A bundle carries the whole history, so the scan reads the history too: a key that was
// written and then deleted is absent from HEAD but still inside the bundle.
func TestMemoryScanAllReachableSeesDeletedSecrets(t *testing.T) {
	_, cfg, slug := memoryTestEnv(t)
	const fake = "AKIAQWERTYUIOPASDFGH"
	keys := memoryProjectMemPath(cfg, slug, "keys.md")

	memoryWrite(t, keys, "old key "+fake+"\n")
	if _, err := memorySnapshot(memoryTriggerManual, time.Now()); err != nil {
		t.Fatal(err)
	}
	memoryWrite(t, keys, "removed\n")
	if _, err := memorySnapshot(memoryTriggerManual, time.Now()); err != nil {
		t.Fatal(err)
	}

	// The tar-side scan, which reads only the HEAD tree, does not see it.
	tree, err := memoryScanRevTree(memoryBranch)
	if err != nil {
		t.Fatalf("scan tree: %v", err)
	}
	if len(tree) != 0 {
		t.Errorf("HEAD tree should be clean now: %+v", tree)
	}
	// The bundle-side scan, which reads the whole history, does (History=true).
	all, err := memoryScanAllReachable()
	if err != nil {
		t.Fatalf("scan all: %v", err)
	}
	if len(all) == 0 {
		t.Fatal("deleted secret not found in history")
	}
	for _, f := range all {
		if !f.History {
			t.Errorf("finding should be marked as history-only: %+v", f)
		}
		if strings.Contains(f.Hint, fake) {
			t.Errorf("history finding leaked the raw secret: %+v", f)
		}
	}
}
