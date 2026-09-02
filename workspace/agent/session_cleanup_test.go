package main

import (
	"strings"
	"testing"
)

func TestClassifySessionCleanup(t *testing.T) {
	// Live → skipped entirely (working, not clutter).
	if _, _, _, ok := classifySessionCleanup(false, false, true, false); ok {
		t.Fatal("live session must not be a cleanup candidate")
	}
	// Stopped, not archived → propose archive, but review (stopped ≠ finished).
	action, safety, _, ok := classifySessionCleanup(false, false, false, false)
	if !ok || action != "archive_session" || safety != "review" {
		t.Fatalf("stopped: action=%q safety=%q ok=%v", action, safety, ok)
	}
	// Already archived → delete_session reclaims it (TTL-exempt, so it accumulates).
	action, safety, _, ok = classifySessionCleanup(false, true, false, false)
	if !ok || action != "delete_session" || safety != "review" {
		t.Fatalf("archived: action=%q safety=%q ok=%v", action, safety, ok)
	}
	// Stopped shell/ssm: no conversation to archive — propose deletion, and safe
	// (delete_session still bundles the meta to the recoverable archive).
	action, safety, reasonKey, ok := classifySessionCleanup(false, false, false, true)
	if !ok || action != "delete_session" || safety != "safe" || reasonKey != cleanReasonEphemeral {
		t.Fatalf("ephemeral: action=%q safety=%q reason=%q ok=%v", action, safety, reasonKey, ok)
	}
	// 削除ロック（docs/log/45）: listed, but keep and with no action — the operator must
	// never propose a tool call that the Agent will refuse with 403.
	for _, archived := range []bool{false, true} {
		action, safety, reasonKey, ok := classifySessionCleanup(true, archived, false, false)
		if !ok || action != "" || safety != "keep" || reasonKey != cleanReasonLocked {
			t.Fatalf("locked(archived=%v): action=%q safety=%q reason=%q ok=%v", archived, action, safety, reasonKey, ok)
		}
	}
}

func TestClassifyWorktreeCleanup(t *testing.T) {
	cases := []struct {
		name       string
		locked     bool
		lockedSess int
		liveCount  int
		ahead      int
		dirty      bool
		relation   string
		wantAction string
		wantSafety string
	}{
		{"live session blocks", false, 0, 1, 0, false, "contained", "", "keep"},
		{"dirty is protected", false, 0, 0, 0, true, "contained", "", "keep"},
		{"ahead is protected", false, 0, 0, 2, false, "contained", "", "keep"},
		{"merged clean is safe", false, 0, 0, 0, false, "contained", "delete_worktree", "safe"},
		{"same as parent is safe", false, 0, 0, 0, false, "same", "delete_worktree", "safe"},
		{"clean unmerged needs review", false, 0, 0, 0, false, "unmerged", "delete_worktree", "review"},
		{"clean diverged needs review", false, 0, 0, 0, false, "diverged", "delete_worktree", "review"},
		{"unknown relation reviews", false, 0, 0, 0, false, "", "delete_worktree", "review"},
		// 削除ロック（docs/log/45）は「安全に消せる」条件を満たしていても keep で止める。
		{"locked beats safe", true, 0, 0, 0, false, "contained", "", "keep"},
		// 削除ロック済みセッションが住む WT も同様 — handleDeleteRepo が 403 で拒むので
		// safe と提案してはならない（停止中/アーカイブ済みのロックも数える）。
		{"locked session inside beats safe", false, 1, 0, 0, false, "contained", "", "keep"},
	}
	for _, c := range cases {
		action, safety, reasonKey := classifyWorktreeCleanup(c.locked, c.lockedSess, c.liveCount, c.ahead, c.dirty, c.relation)
		if action != c.wantAction || safety != c.wantSafety {
			t.Errorf("%s: action=%q safety=%q (want %q/%q)", c.name, action, safety, c.wantAction, c.wantSafety)
		}
		// A classifier must return a KNOWN key — an ad-hoc sentence would reach the Console
		// untranslated (ADR 0033) and cleanupReasonText would echo it back as its own text.
		if _, known := cleanupReasonJA[reasonKey]; !known {
			t.Errorf("%s: reason key %q is not in cleanupReasonJA", c.name, reasonKey)
		}
	}
}

// 掃除候補の理由キーは Console のカタログで訳される（ADR 0033）。Go 側でキーを足して
// カタログに入れ忘れると、英語 Console だけが静かに ja フォールバックへ落ちて気づけない。
func TestCleanupReasonKeysExistInConsoleCatalogs(t *testing.T) {
	for _, locale := range []string{"ja", "en"} {
		catalog := consoleCatalog(t, locale)
		for key := range cleanupReasonJA {
			if !consoleCatalogHasKey(catalog, key) {
				t.Errorf("%s catalog is missing %q", locale, key)
			}
			// The Console also renders each reason as a state badge + hint (row line 2,
			// clean.reason_badge.* / clean.reason_hint.*). A missing pair degrades to the
			// full sentence silently — keep the split catalogs complete instead.
			suffix := strings.TrimPrefix(key, "clean.reason.")
			for _, derived := range []string{"clean.reason_badge." + suffix, "clean.reason_hint." + suffix} {
				if !consoleCatalogHasKey(catalog, derived) {
					t.Errorf("%s catalog is missing %q", locale, derived)
				}
			}
		}
	}
}
