package userinstr

import (
	"strings"
	"testing"
)

// The guidance rides in every session of every kind on every turn, so its size is pinned:
// growing it past the cap has to be a decision, not a drift (0042 decision 7's reasoning).
func TestMemoryGuideStaysUnderItsOwnCap(t *testing.T) {
	if n := len(MemoryGuide); n > MemoryGuideMaxBytes {
		t.Fatalf("MemoryGuide is %d bytes, cap %d", n, MemoryGuideMaxBytes)
	}
	// A cap far above the text would pin nothing.
	if n := len(MemoryGuide); n < MemoryGuideMaxBytes/2 {
		t.Fatalf("MemoryGuide is %d bytes against a cap of %d: lower the cap", n, MemoryGuideMaxBytes)
	}
}

// The block names the tools and when to call the two that matter; it holds no memories.
func TestMemoryGuideNamesTheToolsAndWhenToCallThem(t *testing.T) {
	for _, want := range []string{"memory_index", "memory_search", "memory_read", "memory_save", "memory_forget", "start work", "re-derive"} {
		if !strings.Contains(MemoryGuide, want) {
			t.Errorf("MemoryGuide does not mention %q", want)
		}
	}
	if strings.Contains(MemoryGuide, "<!--") {
		t.Error("MemoryGuide must not carry block markers; mdblock adds them")
	}
}
