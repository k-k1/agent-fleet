package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// fakeNode creates ~/.nvm/versions/node/<v>/bin/node, with install-node's marker when
// marked.
func fakeNode(t *testing.T, v string, marked bool) {
	t.Helper()
	dir := filepath.Join(nvmNodeRoot(), v)
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bin", "node"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if marked {
		if err := os.WriteFile(filepath.Join(dir, nodeInstallMarker), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func nodeDirs(t *testing.T) []string {
	t.Helper()
	ents, _ := os.ReadDir(nvmNodeRoot())
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func TestPruneSupersededNodesKeepsNewestAndNvmInstalls(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fakeNode(t, "v22.9.0", true)   // superseded, ours → removed
	fakeNode(t, "v22.20.0", false) // superseded, nvm's (an exact pin) → kept
	fakeNode(t, "v22.23.4", true)  // newest of 22 → kept
	fakeNode(t, "v24.1.0", true)   // only patch of 24 → kept
	removed, err := pruneSupersededNodes(nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(removed, ",") != "v22.9.0" {
		t.Fatalf("removed %v, want [v22.9.0]", removed)
	}
	if got := strings.Join(nodeDirs(t), ","); got != "v22.20.0,v22.23.4,v24.1.0" {
		t.Fatalf("left %s", got)
	}
}

// The newest is chosen numerically (v22.9.0 < v22.23.2), and a newer nvm install still
// supersedes our older one.
func TestPruneSupersededNodesNumericAndNvmNewest(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fakeNode(t, "v22.23.2", true)
	fakeNode(t, "v22.30.0", false)
	removed, _ := pruneSupersededNodes(nil)
	if strings.Join(removed, ",") != "v22.23.2" {
		t.Fatalf("removed %v, want [v22.23.2]", removed)
	}
	t.Setenv("HOME", t.TempDir())
	fakeNode(t, "v22.9.0", false)
	fakeNode(t, "v22.23.2", true)
	if removed, _ := pruneSupersededNodes(nil); len(removed) != 0 {
		t.Fatalf("removed %v: v22.23.2 is the newest, not v22.9.0", removed)
	}
}

// A tree without bin/node is neither the newest nor removed.
func TestPruneSupersededNodesIgnoresBrokenTrees(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fakeNode(t, "v22.1.0", true)
	broken := filepath.Join(nvmNodeRoot(), "v22.99.0")
	if err := os.MkdirAll(broken, 0o755); err != nil {
		t.Fatal(err)
	}
	if removed, _ := pruneSupersededNodes(nil); len(removed) != 0 {
		t.Fatalf("removed %v: a broken tree must not supersede a working one", removed)
	}
	if _, err := os.Stat(broken); err != nil {
		t.Fatalf("broken tree removed: %v", err)
	}
}

func TestPruneSupersededNodesNoNvmDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if removed, err := pruneSupersededNodes(nil); err != nil || len(removed) != 0 {
		t.Fatalf("removed %v err %v", removed, err)
	}
}

func TestInstalledNodeVersions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fakeNode(t, "v22.9.0", false)
	fakeNode(t, "v22.23.4", true)
	fakeNode(t, "v24.1.0", true)
	got := installedNodeVersions()
	if got["22"] != "v22.23.4" || got["24"] != "v24.1.0" || len(got) != 2 {
		t.Fatalf("installedNodeVersions = %v", got)
	}
}

// A POST refused because another major's download runs reports that job as
// "installing", even if it ended right after the refusal.
func TestNodeInstallBusyReportsRunningJob(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	nodeInstaller.mu.Lock()
	nodeInstaller.state, nodeInstaller.major, nodeInstaller.err = "installing", "24", ""
	nodeInstaller.mu.Unlock()
	t.Cleanup(func() {
		nodeInstaller.mu.Lock()
		nodeInstaller.state, nodeInstaller.major, nodeInstaller.err = "", "", ""
		nodeInstaller.mu.Unlock()
	})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/env/node-install", strings.NewReader(`{"major":"22"}`))
	r.Header.Set("Content-Type", "application/json")
	handleNodeInstall(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"state":"installing"`) || !strings.Contains(w.Body.String(), `"major":"24"`) {
		t.Fatalf("code %d body %s", w.Code, w.Body.String())
	}
}

// pruneLeft runs the prune with keep and returns what is left, comma-joined.
func pruneLeft(t *testing.T, keep ...string) string {
	t.Helper()
	if _, err := pruneSupersededNodes(keep); err != nil {
		t.Fatal(err)
	}
	return strings.Join(nodeDirs(t), ",")
}

func writePinFile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The marker says who installed a tree, not that nobody uses it: nvm reuses a Fleet tree
// as is, so a pinned marked patch must stay.
func TestPruneKeepsPinnedMarkedPatches(t *testing.T) {
	cases := []struct {
		name string
		pin  func(home string)
	}{
		{"nvm alias", func(h string) { writePinFile(t, filepath.Join(h, ".nvm", "alias", "proj"), "v22.23.4\n") }},
		{"nvm lts alias", func(h string) { writePinFile(t, filepath.Join(h, ".nvm", "alias", "lts", "jod"), "22.23.4") }},
		{"home .nvmrc", func(h string) { writePinFile(t, filepath.Join(h, ".nvmrc"), "22.23.4\n") }},
		{"repo .nvmrc", func(h string) { writePinFile(t, filepath.Join(h, "repos", "app", ".nvmrc"), "v22.23.4\n") }},
		// The entrypoint runs `nvm use` on an exact selection under set -e.
		{"exact selection", func(h string) {
			writePinFile(t, filepath.Join(h, ".config", "agent-fleet", "toolchains.json"), `{"node":"22.23.4"}`)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			fakeNode(t, "v22.23.4", true)
			fakeNode(t, "v22.23.5", true)
			c.pin(home)
			if got := pruneLeft(t); got != "v22.23.4,v22.23.5" {
				t.Fatalf("left %s: the pinned v22.23.4 was removed", got)
			}
		})
	}
}

// A bare major pins nothing: any patch satisfies it.
func TestPruneMajorPinDoesNotKeep(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	fakeNode(t, "v22.23.4", true)
	fakeNode(t, "v22.23.5", true)
	writePinFile(t, filepath.Join(home, ".nvm", "alias", "default"), "22\n")
	writePinFile(t, filepath.Join(home, ".nvmrc"), "lts/*\n")
	if got := pruneLeft(t); got != "v22.23.5" {
		t.Fatalf("left %s", got)
	}
}

// --keep protects the tree install-node just returned, even when a newer local tree
// makes it superseded.
func TestPruneKeepsTheTreeTheEntrypointUses(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fakeNode(t, "v22.23.4", true)
	fakeNode(t, "v22.23.5", false)
	bin := filepath.Join(nvmNodeRoot(), "v22.23.4", "bin")
	if got := pruneLeft(t, bin); got != "v22.23.4,v22.23.5" {
		t.Fatalf("left %s", got)
	}
}

// The newest must be a working node of its own; otherwise nothing of that major goes.
func TestPruneSkipsMajorWhenNewestIsNotSelfContained(t *testing.T) {
	cases := []struct {
		name  string
		setup func(root string)
	}{
		{"bin/node symlink into the old tree", func(root string) {
			if err := os.MkdirAll(filepath.Join(root, "v22.23.5", "bin"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("../../v22.23.4/bin/node", filepath.Join(root, "v22.23.5", "bin", "node")); err != nil {
				t.Fatal(err)
			}
		}},
		{"bin symlink into the old tree", func(root string) {
			if err := os.MkdirAll(filepath.Join(root, "v22.23.5"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("../v22.23.4/bin", filepath.Join(root, "v22.23.5", "bin")); err != nil {
				t.Fatal(err)
			}
		}},
		{"not executable", func(root string) {
			writePinFile(t, filepath.Join(root, "v22.23.5", "bin", "node"), "")
		}},
		{"bin/node is a directory", func(root string) {
			if err := os.MkdirAll(filepath.Join(root, "v22.23.5", "bin", "node"), 0o755); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			fakeNode(t, "v22.23.4", true)
			c.setup(nvmNodeRoot())
			if got := pruneLeft(t); got != "v22.23.4,v22.23.5" {
				t.Fatalf("left %s: v22.23.4 removed although v22.23.5 is not a working node of its own", got)
			}
		})
	}
}

// A tree another tree's node resolves into stays, even when the newest is healthy.
func TestPruneKeepsTreesOthersDependOn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fakeNode(t, "v22.23.4", true)
	fakeNode(t, "v22.23.6", true)
	root := nvmNodeRoot()
	if err := os.MkdirAll(filepath.Join(root, "v22.23.5", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../v22.23.4/bin/node", filepath.Join(root, "v22.23.5", "bin", "node")); err != nil {
		t.Fatal(err)
	}
	if got := pruneLeft(t); got != "v22.23.4,v22.23.5,v22.23.6" {
		t.Fatalf("left %s", got)
	}
}

func nodeSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// A chain newest → old → newest resolves inside the newest, yet removing the old tree
// breaks it. Any link leaving its own tree keeps the whole major.
func TestPruneSkipsMajorOnChainThroughOldTree(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := nvmNodeRoot()
	writePinFile(t, filepath.Join(root, "v22.23.4", nodeInstallMarker), "")
	nodeSymlink(t, "../../v22.23.5/lib/node", filepath.Join(root, "v22.23.4", "bin", "node"))
	if err := os.MkdirAll(filepath.Join(root, "v22.23.5", "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "v22.23.5", "lib", "node"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	nodeSymlink(t, "../v22.23.4/bin", filepath.Join(root, "v22.23.5", "bin"))
	if got := pruneLeft(t, filepath.Join(root, "v22.23.5", "bin")); got != "v22.23.4,v22.23.5" {
		t.Fatalf("left %s", got)
	}
}

// Three trees chained (6 → 5 → 4): nothing of the major goes.
func TestPruneSkipsMajorOnLongerChain(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := nvmNodeRoot()
	fakeNode(t, "v22.23.4", true)
	writePinFile(t, filepath.Join(root, "v22.23.5", nodeInstallMarker), "")
	nodeSymlink(t, "../../v22.23.4/bin/node", filepath.Join(root, "v22.23.5", "bin", "node"))
	if err := os.MkdirAll(filepath.Join(root, "v22.23.6"), 0o755); err != nil {
		t.Fatal(err)
	}
	nodeSymlink(t, "../v22.23.5/bin", filepath.Join(root, "v22.23.6", "bin"))
	if got := pruneLeft(t); got != "v22.23.4,v22.23.5,v22.23.6" {
		t.Fatalf("left %s", got)
	}
}

// A top-level entry that is a symlink (v22.23.5 -> v22.23.4) keeps the major too.
func TestPruneSkipsMajorWithSymlinkedEntry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := nvmNodeRoot()
	fakeNode(t, "v22.23.4", true)
	fakeNode(t, "v22.23.6", true)
	nodeSymlink(t, "v22.23.4", filepath.Join(root, "v22.23.5"))
	if got := pruneLeft(t); got != "v22.23.4,v22.23.5,v22.23.6" {
		t.Fatalf("left %s", got)
	}
}

// Links that stay inside their own tree (npm, npx, corepack in a real tarball) do not
// stop the prune.
func TestPruneAllowsInTreeSymlinks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := nvmNodeRoot()
	fakeNode(t, "v22.23.4", true)
	fakeNode(t, "v22.23.5", true)
	for _, v := range []string{"v22.23.4", "v22.23.5"} {
		writePinFile(t, filepath.Join(root, v, "lib", "node_modules", "npm", "bin", "npm-cli.js"), "")
		nodeSymlink(t, "../lib/node_modules/npm/bin/npm-cli.js", filepath.Join(root, v, "bin", "npm"))
	}
	if got := pruneLeft(t); got != "v22.23.5" {
		t.Fatalf("left %s", got)
	}
}

// .nvmrc is read the way nvm reads it: comments, blank lines and key=value lines skipped.
func TestPruneKeepsCommentedNvmrcPins(t *testing.T) {
	for _, body := range []string{
		"# project runtime\n22.23.4\n",
		"\n\n22.23.4 # project runtime\n",
		"some_option=true\nv22.23.4\n",
	} {
		home := t.TempDir()
		t.Setenv("HOME", home)
		fakeNode(t, "v22.23.4", true)
		fakeNode(t, "v22.23.5", true)
		writePinFile(t, filepath.Join(home, "repos", "app", ".nvmrc"), body)
		if got := pruneLeft(t); got != "v22.23.4,v22.23.5" {
			t.Fatalf(".nvmrc %q: left %s", body, got)
		}
	}
}

func TestNvmrcVersion(t *testing.T) {
	for in, want := range map[string]string{
		"22.23.4\n":               "22.23.4",
		"# c\n\n v22.23.4  # c\n": "v22.23.4",
		"a=b\nlts/*\n":            "lts/*",
		"# only a comment\n":      "",
		"22.23.4\n24.1.0\n":       "22.23.4",
	} {
		if got := nvmrcVersion([]byte(in)); got != want {
			t.Errorf("nvmrcVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

// nvm reads alias files with a plain file test, so a symlinked alias file, and a
// symlinked alias directory, still pin. A link cycle must not hang the walk.
func TestPruneFollowsSymlinkedAliases(t *testing.T) {
	cases := map[string]func(home string){
		"alias file symlink": func(h string) {
			writePinFile(t, filepath.Join(h, "dotfiles", "proj"), "v22.23.4\n")
			nodeSymlink(t, filepath.Join(h, "dotfiles", "proj"), filepath.Join(h, ".nvm", "alias", "proj"))
		},
		"alias dir symlink": func(h string) {
			writePinFile(t, filepath.Join(h, "dotfiles", "alias", "proj"), "22.23.4")
			if err := os.MkdirAll(filepath.Join(h, ".nvm"), 0o755); err != nil {
				t.Fatal(err)
			}
			nodeSymlink(t, filepath.Join(h, "dotfiles", "alias"), filepath.Join(h, ".nvm", "alias"))
		},
		"cycle": func(h string) {
			writePinFile(t, filepath.Join(h, ".nvm", "alias", "proj"), "22.23.4")
			nodeSymlink(t, "..", filepath.Join(h, ".nvm", "alias", "loop"))
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			fakeNode(t, "v22.23.4", true)
			fakeNode(t, "v22.23.5", true)
			setup(home)
			if got := pruneLeft(t); got != "v22.23.4,v22.23.5" {
				t.Fatalf("left %s", got)
			}
		})
	}
}

// "../" after a directory link escapes what a lexical check sees: inside -> . makes
// ../inside/../v22.23.4/bin/node look in-tree while the OS lands in the old tree.
func TestPruneSkipsOnDirectoryLinkEscape(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := nvmNodeRoot()
	fakeNode(t, "v22.23.4", true)
	nodeSymlink(t, ".", filepath.Join(root, "v22.23.5", "inside"))
	nodeSymlink(t, "../inside/../v22.23.4/bin/node", filepath.Join(root, "v22.23.5", "bin", "node"))
	if got := pruneLeft(t, filepath.Join(root, "v22.23.5", "bin")); got != "v22.23.4,v22.23.5" {
		t.Fatalf("left %s", got)
	}
}

// One major's link into another's tree protects that tree: nothing is pruned.
func TestPruneSkipsOnCrossMajorLinks(t *testing.T) {
	cases := map[string]func(root string){
		"file link from another major": func(root string) {
			fakeNode(t, "v24.1.0", false)
			writePinFile(t, filepath.Join(root, "v22.23.4", "lib", "node_modules", "npm", "bin", "npm-cli.js"), "")
			nodeSymlink(t, "../../v22.23.4/lib/node_modules/npm/bin/npm-cli.js", filepath.Join(root, "v24.1.0", "bin", "npm"))
		},
		"top-level link from another major": func(root string) {
			nodeSymlink(t, "v22.23.4", filepath.Join(root, "v24.0.0"))
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			fakeNode(t, "v22.23.4", true)
			fakeNode(t, "v22.23.5", true)
			setup(nvmNodeRoot())
			left := pruneLeft(t)
			if !strings.Contains(left, "v22.23.4") {
				t.Fatalf("left %s: v22.23.4 removed although another major links into it", left)
			}
		})
	}
}

// Any directory link stops the pass, even one that stays in its own tree.
func TestPruneSkipsOnAnyDirectoryLink(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := nvmNodeRoot()
	fakeNode(t, "v22.23.4", true)
	fakeNode(t, "v22.23.5", true)
	if err := os.MkdirAll(filepath.Join(root, "v22.23.5", "lib", "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	nodeSymlink(t, "real", filepath.Join(root, "v22.23.5", "lib", "alias"))
	if got := pruneLeft(t); got != "v22.23.4,v22.23.5" {
		t.Fatalf("left %s", got)
	}
}

// A target that steps out to the root, passes a directory link outside every tree and
// comes back reads as in-tree once cleaned, yet the OS lands in the old tree.
func TestPruneSkipsOnDetourThroughOutsideLink(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := nvmNodeRoot()
	fakeNode(t, "v22.23.4", true)
	if err := os.MkdirAll(filepath.Join(root, "v22.23.4", "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "v22.23.4", "v22.23.5", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "v22.23.4", "v22.23.5", "bin", "node"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	nodeSymlink(t, "v22.23.4/subdir", filepath.Join(root, "shared"))
	nodeSymlink(t, "../../shared/../v22.23.5/bin/node", filepath.Join(root, "v22.23.5", "bin", "node"))
	if got := pruneLeft(t, filepath.Join(root, "v22.23.5", "bin")); !strings.Contains(got, "v22.23.4") {
		t.Fatalf("left %s: v22.23.4 removed although the newest node resolves into it", got)
	}
}

// Absolute targets are refused, even ones that point into their own tree.
func TestPruneSkipsOnAbsoluteLink(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := nvmNodeRoot()
	fakeNode(t, "v22.23.4", true)
	fakeNode(t, "v22.23.5", true)
	writePinFile(t, filepath.Join(root, "v22.23.5", "lib", "cli.js"), "")
	nodeSymlink(t, filepath.Join(root, "v22.23.5", "lib", "cli.js"), filepath.Join(root, "v22.23.5", "bin", "cli"))
	if got := pruneLeft(t); got != "v22.23.4,v22.23.5" {
		t.Fatalf("left %s", got)
	}
}
