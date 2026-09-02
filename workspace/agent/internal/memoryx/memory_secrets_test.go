package memoryx

import (
	"strings"
	"testing"
	"time"
)

// ★4 の secret スキャン。見るのは 3 点:
//
//	① 実鍵の形は捕まえる ② 例示・散文は捕まえない ③ **生値を返さない**。
//
// ③ が落ちると、防御のつもりの機構が秘密を新しい場所（API 応答・UI・監査）へ配る経路になる。
func TestMemoryScanContentDetectsAndMasks(t *testing.T) {
	// テスト用の偽鍵。形だけ本物に似せた文字列で、実在の資格情報ではない。
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
		// ③ 生値がどのフィールドにも出ないこと。
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
	// ② 例示（EXAMPLE を含む）と散文・短い値は落ちる。行番号で確認する。
	for _, f := range got {
		if f.Line == 3 || f.Line == 4 || f.Line == 5 {
			t.Errorf("false positive on line %d: %+v", f.Line, f)
		}
	}
	// バイナリは走査しない（md 以外はノイズにしかならない）。
	if n := memoryScanContent("x.bin", []byte("\x00"+fakeAWS)); len(n) != 0 {
		t.Errorf("binary blob scanned: %+v", n)
	}
}

// bundle は全履歴を運ぶので、走査も履歴を見る — 「一度書いて消した鍵」が
// HEAD には無いのに bundle には入っている、を取りこぼさないこと。
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

	// HEAD ツリーだけを見る tar 向けの走査では出ない。
	tree, err := memoryScanRevTree(memoryBranch)
	if err != nil {
		t.Fatalf("scan tree: %v", err)
	}
	if len(tree) != 0 {
		t.Errorf("HEAD tree should be clean now: %+v", tree)
	}
	// 全履歴を見る bundle 向けの走査では出る（History=true）。
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
