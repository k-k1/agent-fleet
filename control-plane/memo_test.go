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

// The memo token round-trips to its membership, rejects tampering, a wrong sign key,
// and a foreign membership's tag (the internal memo-bridge auth, memo_bridge.go).
func TestMemoTokenRoundTrip(t *testing.T) {
	key := memoSignKey([]byte("master-key-under-test"))
	tok := mintMemoToken(key, "mem-123")

	if mid, ok := verifyMemoToken(key, tok); !ok || mid != "mem-123" {
		t.Fatalf("round-trip: got (%q,%v), want (mem-123,true)", mid, ok)
	}
	// Bearer-prefixed value must NOT verify (verify takes the raw token; the handler
	// strips "Bearer " first) — guards against accidentally passing the header verbatim.
	if _, ok := verifyMemoToken(key, "Bearer "+tok); ok {
		t.Fatal("verify accepted a Bearer-prefixed token")
	}
	// A different sign key (wrong master) is rejected.
	if _, ok := verifyMemoToken(memoSignKey([]byte("other-master")), tok); ok {
		t.Fatal("verify accepted a token signed by a different key")
	}
	// A tampered membership id with the original tag is rejected (tag binds the id).
	origTag := tok[strings.LastIndexByte(tok, '.')+1:]
	otherBody := strings.TrimPrefix(mintMemoToken(key, "mem-999"), "afm_")
	forged := "afm_" + otherBody[:strings.LastIndexByte(otherBody, '.')] + "." + origTag
	if _, ok := verifyMemoToken(key, forged); ok {
		t.Fatal("verify accepted a forged membership id")
	}
	// The git token and the memo token do not cross-verify (separate credentials).
	if _, ok := verifyMemoToken(key, mintGitToken(gitSignKey([]byte("master-key-under-test")), "mem-123")); ok {
		t.Fatal("verify accepted a git token as a memo token")
	}
}
