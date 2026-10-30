package main

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// mainFilesAllowlist is the checked-in list of non-test .go files allowed
// directly under the module directory (package main). ADR 0124 Phase 0: main
// regrew after ADR 0067 because nothing asked new code to go anywhere else,
// and a line budget was rejected because every feature PR would edit it.
const mainFilesAllowlist = "testdata/main-files.txt"

// TestMainFilesAllowlist fails on a package-main file that is not listed and
// on a listed file that no longer exists, so the list only ever shrinks.
func TestMainFilesAllowlist(t *testing.T) {
	// The test runs in the package directory, which is the module root.
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	onDisk := map[string]bool{}
	for _, e := range ents {
		n := e.Name()
		if e.Type().IsRegular() && strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
			onDisk[n] = true
		}
	}

	f, err := os.Open(filepath.FromSlash(mainFilesAllowlist))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	listed := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if listed[line] {
			t.Errorf("%s lists %s twice", mainFilesAllowlist, line)
		}
		listed[line] = true
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(listed) == 0 {
		t.Fatalf("%s lists no files; the scan would accept nothing", mainFilesAllowlist)
	}

	var added, gone []string
	for n := range onDisk {
		if !listed[n] {
			added = append(added, n)
		}
	}
	for n := range listed {
		if !onDisk[n] {
			gone = append(gone, n)
		}
	}
	sort.Strings(added)
	sort.Strings(gone)
	for _, n := range added {
		t.Errorf("new package-main file %s: new code belongs in internal/<pkg>, not in package main.\n"+
			"If this file really has to live in main, add it to %s and give the reason in the PR description (ADR 0124 Phase 0).",
			n, mainFilesAllowlist)
	}
	for _, n := range gone {
		t.Errorf("%s lists %s, which no longer exists: delete the line so the list only shrinks.", mainFilesAllowlist, n)
	}
}
