package main

import (
	"strings"
	"testing"
)

// buildFlushMessage groups by category with a heading each, numbers restart per
// category, file memos surface the ref path (plus comment), text memos the body.
func TestBuildFlushMessage(t *testing.T) {
	memos := []Memo{
		{Category: "frontend", Kind: "file", RefPath: "~/repos/a/Button.tsx", Body: "余白を詰めて"},
		{Category: "frontend", Kind: "text", Body: "色も直して"},
		{Category: "api", Kind: "text", Body: "エラーハンドリング追加"},
		{Category: "", Kind: "file", RefPath: "~/repos/a/x.go"},
	}
	got := buildFlushMessage(memos)

	for _, want := range []string{
		"以下のメモをまとめて処理して。",
		"## frontend",
		"1. 対象ファイル: ~/repos/a/Button.tsx",
		"   余白を詰めて",
		"2. 色も直して",
		"## api",
		"1. エラーハンドリング追加",
		"## 未分類",
		"1. 対象ファイル: ~/repos/a/x.go",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("message missing %q\n---\n%s", want, got)
		}
	}
	// The api heading restarts numbering at 1 (not continuing frontend's count).
	if strings.Contains(got, "3. エラーハンドリング追加") {
		t.Fatalf("numbering did not restart per category\n%s", got)
	}
}
