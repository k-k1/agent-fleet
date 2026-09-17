package statemig

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A store added under paths.AgentStateDir and forgotten in Entries would work perfectly in
// development and silently drop that store's history on the one boot where it mattered —
// there is no error, just an empty store on an existing Workspace. So the list is checked
// against the source rather than against anyone's memory.
//
// The three shapes a state path is spelled in, all of them with the subdirectory as a
// literal: the fstore constructors take the resolver as a function, filepath.Join calls it,
// and the per-agent stores name the subdirectory at their own constructor.
var stateDirSpellings = []*regexp.Regexp{
	regexp.MustCompile(`paths\.AgentStateDir,\s*"([^"]+)"`),
	regexp.MustCompile(`paths\.AgentStateDir\(\),\s*"([^"]+)"`),
	regexp.MustCompile(`agents\.New(?:SidStore|MsgLedger)\("([^"]+)"\)`),
}

// operatorStore / threadStore name their file in a struct literal instead, one layer away
// from the resolver (internal/bridge).
var bridgeFileSpelling = regexp.MustCompile(`(?:operatorStore|threadStore)\{file:\s*"([^"]+)"`)

func TestEveryStateStoreIsInEntries(t *testing.T) {
	root := moduleRoot(t)
	listed := map[string]bool{}
	for _, e := range Entries {
		listed[e] = true
	}
	found := map[string]string{} // subdir -> where it was seen

	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		for _, re := range append(stateDirSpellings, bridgeFileSpelling) {
			for _, m := range re.FindAllStringSubmatch(string(b), -1) {
				found[m[1]] = rel
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) == 0 {
		t.Fatal("scanned the module and found no state stores at all — the spellings above have drifted")
	}
	for name, where := range found {
		if !listed[name] {
			t.Errorf("%q (%s) resolves under the state dir but is not in statemig.Entries: "+
				"existing Workspaces will start with it empty", name, where)
		}
	}
}

// moduleRoot walks up to workspace/agent (the directory holding go.mod).
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("go.mod not found above the test's working directory")
	return ""
}
