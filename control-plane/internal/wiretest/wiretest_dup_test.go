// wiretest_dup_test.go keeps the two copies of this package — control-plane and
// workspace/agent — from drifting apart, mechanically.
//
// Two copies exist because a Go package shared across the modules was ruled out
// (ADR 0012 decision 3): the duplication folds into one copy within a module, but the
// module boundary does not fold.
//
// Hand-written duplicates drift (operating kit 0.5). Fix one side only and the other keeps
// running the old implementation while both modules' tests stay independently green.
//
// Two checks, the second closing a structural hole in the first:
//
//	① byte comparison of the shared region — not one byte may differ below the sentinel
//	② equal file-name sets — adding a file to one side alone turns it red
//
// ① guards a region, not a package: a shared tool added outside the sentinel or in a new
// file drifts while everything stays green. ② closes that road, because here the unit of
// sharing is exactly one package.
//
// Above the sentinel (package clause and imports) the copies cannot match, because the
// module paths differ. A sentinel placed below the imports rather than normalisation:
// normalisation buries the judgement of what may be ignored inside code, while a sentinel
// puts the boundary in the source where it can be seen.
//
// This file is itself byte-identical in both copies — it searches for its peer from the
// repository root instead of holding the peer's path in a constant. Placed on one side
// only, it would fail ② on itself.
package wiretest

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ===== shared region starts here (byte-identical in control-plane and workspace/agent) =====
// Below this line the two modules must not differ by a single byte; wiretest_dup_test.go
// checks it. Above the sentinel are only the package clause and imports, which cannot match
// because the module paths differ.

// wiretestSentinel is the line that opens the shared region. It appears exactly once per file.
//
// Written as a concatenation to break the self-reference: as a single literal this line is
// itself counted as a second sentinel and the check fails (measured). The checker's own
// string would otherwise be part of what it checks, so it is split in the source.
const wiretestSentinel = "// ===== shared region starts here" + " (byte-identical in control-plane and workspace/agent) ====="

// wiretestCopies locates the two copies relative to the repository root.
var wiretestCopies = []string{
	filepath.Join("control-plane", "internal", "wiretest"),
	filepath.Join("workspace", "agent", "internal", "wiretest"),
}

func TestWiretestCopiesDoNotDrift(t *testing.T) {
	self, peer := selfAndPeer(t)
	for _, name := range goFiles(t, self) {
		a := mustShared(t, filepath.Join(self, name))
		b := mustShared(t, filepath.Join(peer, name))
		if a != b {
			t.Errorf("%s の共有区間が一致しない（%d バイト 対 %d バイト）。\n"+
				"  片方だけ直すと、両モジュールのテストは独立に緑のまま漂流する。\n"+
				"  どちらかへ揃えること: %s / %s", name, len(a), len(b), self, peer)
		}
	}
}

// TestWiretestCopiesHaveSameFiles closes ①'s hole: the byte comparison guards the region
// fenced by the sentinel, not the package. Add one new file to a single side and ① stays
// entirely green while the copies drift.
func TestWiretestCopiesHaveSameFiles(t *testing.T) {
	self, peer := selfAndPeer(t)
	got, want := goFiles(t, self), goFiles(t, peer)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("2 つの写しのファイル構成が違う。\n  %s: %v\n  %s: %v\n"+
			"  片方だけにファイルを足すと、byte 比較は全部緑のまま漂流する。", self, got, peer, want)
	}
	if len(got) == 0 {
		t.Fatal(".go が 1 枚も見つからない（走査が壊れている）")
	}
}

// selfAndPeer walks up to the repository root and returns the real paths of the two copies.
//
// The peer's location is deliberately not a constant: that would make this file differ
// between the copies, so the copy placed to satisfy ② (equal file-name sets) would break
// ① (byte equality).
func selfAndPeer(t *testing.T) (string, string) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := wd
	for i := 0; i < 8; i++ {
		a, b := filepath.Join(dir, wiretestCopies[0]), filepath.Join(dir, wiretestCopies[1])
		if isDir(a) && isDir(b) {
			if sameDir(t, wd, a) {
				return a, b
			}
			return b, a
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	// Fatal, not Skip: skipping when nothing is found makes "not checked" green while it
	// reads as "not drifting".
	t.Fatalf("リポジトリ根（%v の両方を持つ階層）が %s から見つからない。"+
		"移動したならこの表を直すこと——見つからないまま緑にはしない。", wiretestCopies, wd)
	return "", ""
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func sameDir(t *testing.T, a, b string) bool {
	t.Helper()
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(ai, bi)
}

func goFiles(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var out []string
	for _, e := range es {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// mustShared returns everything below the sentinel. A missing or repeated sentinel is
// treated as a failure of the check itself.
func mustShared(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	s := string(b)
	if n := strings.Count(s, wiretestSentinel); n != 1 {
		t.Fatalf("%s の番兵が %d 個（1 個であるべき）。"+
			"番兵が消えると比較範囲が変わり、**検査が黙って別のものを見る**。", path, n)
	}
	return s[strings.Index(s, wiretestSentinel)+len(wiretestSentinel):]
}
