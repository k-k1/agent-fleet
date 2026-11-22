package bootinstall

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type suFixture struct {
	env     Env
	home    string
	pathDir string // first on PATH: stubs of what the entrypoint's PATH resolves
	npmDir  string
	out     *bytes.Buffer
	mu      sync.Mutex
	hits    map[string]int
}

func (f *suFixture) hit(name string) int { f.mu.Lock(); defer f.mu.Unlock(); return f.hits[name] }

// newSU serves every upstream the self-update asks: rtk's latest page and release, agy's
// manifest and install script, cursor's install script. agyScript and cursorScript are the
// bodies of the install scripts ("" = a script that fails).
func newSU(t *testing.T, rtkScript, rtkSum, agyScript, cursorScript string) *suFixture {
	t.Helper()
	f := &suFixture{hits: map[string]int{}}
	tb := rtkTarball(t, rtkScript)
	if rtkSum == "" {
		h := sha256.Sum256(tb)
		rtkSum = hex.EncodeToString(h[:])
	}
	mux := http.NewServeMux()
	hit := func(name string, h http.HandlerFunc) {
		mux.HandleFunc("/"+name, func(w http.ResponseWriter, r *http.Request) { f.mu.Lock(); f.hits[name]++; f.mu.Unlock(); h(w, r) })
	}
	srvURL := ""
	hit("rtk-latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srvURL+"/rtk/tag/v2.0.0", http.StatusFound)
	})
	hit("rtk-dl/"+rtkAssetAMD64, func(w http.ResponseWriter, _ *http.Request) { w.Write(tb) })
	hit("rtk-dl/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", rtkSum, rtkAssetAMD64)
	})
	hit("agy-manifest", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"version": "7.7.7"}`) })
	hit("agy-install.sh", func(w http.ResponseWriter, _ *http.Request) {
		if agyScript == "" {
			fmt.Fprint(w, "exit 1\n")
			return
		}
		fmt.Fprint(w, agyScript)
	})
	hit("cursor-install", func(w http.ResponseWriter, _ *http.Request) {
		if cursorScript == "" {
			fmt.Fprint(w, "exit 1\n")
			return
		}
		fmt.Fprint(w, cursorScript)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	srvURL = srv.URL

	f.home = t.TempDir()
	f.pathDir = t.TempDir()
	f.npmDir = t.TempDir()
	// Nothing of the host's: a fake HOME and a PATH of the stubs, the fake ~/.local/bin and a
	// directory of links to just the base tools the stubs use, so no real CLI of the machine
	// running the test is ever called.
	tools := t.TempDir()
	for _, n := range []string{"sh", "bash", "cat", "tr", "wc", "cp", "mkdir", "chmod", "dirname", "printf", "echo", "head", "env", "timeout", "grep", "uname", "install", "rm", "mv", "ln", "touch"} {
		for _, d := range []string{"/usr/bin", "/bin"} {
			if _, err := os.Stat(filepath.Join(d, n)); err == nil {
				_ = os.Symlink(filepath.Join(d, n), filepath.Join(tools, n))
				break
			}
		}
	}
	t.Setenv("HOME", f.home)
	t.Setenv("PATH", strings.Join([]string{f.pathDir, filepath.Join(f.home, ".local", "bin"), tools}, string(os.PathListSeparator)))
	// The shared npm stub, plus `view` answering $D/view.<name-with-slashes-as-_>.
	view := "view) f=\"$D/view.$(echo \"$2\" | tr '/' '_')\"; [ -f \"$f\" ] && cat \"$f\"; exit 0 ;;\n"
	stub := strings.Replace(npmStub, "ls)", view+"ls)", 1)
	if err := os.WriteFile(filepath.Join(f.npmDir, "npm"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	f.out = &bytes.Buffer{}
	f.env = Env{
		Home: f.home, Pins: allPins, NPM: filepath.Join(f.npmDir, "npm"), BakedBin: t.TempDir(),
		Arch: "amd64", Client: srv.Client(), FreeMiB: func(string) (int64, bool) { return 100000, true },
		AgyEnv: func() []string { return nil }, Stdout: f.out, Stderr: f.out, RunTimeout: 10 * time.Second,
		RTKLatestPage: srv.URL + "/rtk-latest", RTKLatestDownload: srv.URL + "/rtk-dl",
		AgyManifestURL: srv.URL + "/agy-manifest", AgyInstallURL: srv.URL + "/agy-install.sh",
		CursorInstallURL: srv.URL + "/cursor-install",
	}
	return f
}

func (f *suFixture) write(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func (f *suFixture) local(n string) string { return filepath.Join(f.home, ".local", "bin", n) }

func (f *suFixture) read(p string) string { b, _ := os.ReadFile(p); return string(b) }

func TestSelfUpdateNPMSkipsWhenLatest(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	var ls strings.Builder
	ls.WriteString(`{"dependencies":{`)
	for i, c := range npmCLIs {
		if i > 0 {
			ls.WriteString(",")
		}
		fmt.Fprintf(&ls, `%q:{"version":"5.0.0"}`, c.Pkg)
		f.write(t, filepath.Join(f.npmDir, "view."+strings.ReplaceAll(c.Pkg, "/", "_")), "5.0.0\n")
	}
	ls.WriteString("}}")
	f.write(t, filepath.Join(f.npmDir, "ls.json"), ls.String())
	f.env.selfUpdateNPM()
	if !strings.Contains(f.out.String(), "already latest; skip") {
		t.Fatalf("not skipped:\n%s", f.out)
	}
	if _, err := os.Stat(filepath.Join(f.npmDir, "npm.log")); err == nil {
		t.Fatal("npm install ran although every package is at latest")
	}
}

func TestSelfUpdateNPMInstallsLatestAndVerifies(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	f.write(t, filepath.Join(f.npmDir, "ls.json"), `{"dependencies":{"@anthropic-ai/claude-code":{"version":"1.0.0"}}}`)
	f.write(t, filepath.Join(f.npmDir, "view.@anthropic-ai_claude-code"), "5.0.0\n")
	f.env.selfUpdateNPM()
	log := f.read(filepath.Join(f.npmDir, "npm.log"))
	for _, c := range npmCLIs {
		if !strings.Contains(log, c.Pkg+"@latest") {
			t.Fatalf("npm install lacks %s@latest: %s", c.Pkg, log)
		}
	}
	for _, c := range npmCLIs {
		if !usable(f.local(c.Name)) {
			t.Fatalf("%s not installed", c.Name)
		}
	}
}

func TestSelfUpdateNPMFailureKeepsInstalledAndRepairs(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	f.write(t, filepath.Join(f.npmDir, "fail"), "npm ERR! network boom\n")
	f.write(t, f.local("claude"), "#!/bin/sh\necho 1.0.0\n")
	f.env.selfUpdateNPM()
	if !strings.Contains(f.out.String(), "WARN: agent CLI update failed (kept the installed versions)") {
		t.Fatalf("no warning:\n%s", f.out)
	}
	if !strings.Contains(f.read(f.local("claude")), "1.0.0") {
		t.Fatal("working claude was replaced after a failed update")
	}
}

func TestSelfUpdateRTK(t *testing.T) {
	t.Run("skip when PATH's rtk is latest", func(t *testing.T) {
		f := newSU(t, goodRTK, "", "", "")
		f.write(t, filepath.Join(f.pathDir, "rtk"), "#!/bin/sh\necho rtk 2.0.0\n")
		f.env.selfUpdateRTK(context.Background())
		if !strings.Contains(f.out.String(), "rtk already latest (2.0.0); skip") || f.hit("rtk-dl/"+rtkAssetAMD64) != 0 {
			t.Fatalf("downloaded although latest:\n%s", f.out)
		}
	})
	t.Run("installs latest", func(t *testing.T) {
		f := newSU(t, goodRTK, "", "", "")
		f.env.selfUpdateRTK(context.Background())
		if !strings.Contains(f.out.String(), "rtk updated: rtk 1.2.3") || !usable(f.local("rtk")) {
			t.Fatalf("not installed:\n%s", f.out)
		}
	})
	t.Run("wrong checksum installs nothing", func(t *testing.T) {
		f := newSU(t, goodRTK, strings.Repeat("0", 64), "", "")
		f.write(t, f.local("rtk"), "#!/bin/sh\necho rtk 0.1.0\n")
		f.env.selfUpdateRTK(context.Background())
		if !strings.Contains(f.out.String(), "WARN: rtk update failed") || !strings.Contains(f.read(f.local("rtk")), "0.1.0") {
			t.Fatalf("a mismatching artifact reached ~/.local:\n%s", f.out)
		}
	})
	t.Run("a candidate that does not run keeps the working copy", func(t *testing.T) {
		f := newSU(t, "#!/bin/sh\nexit 1\n", "", "", "")
		f.write(t, f.local("rtk"), "#!/bin/sh\necho rtk 0.1.0\n")
		f.env.selfUpdateRTK(context.Background())
		if !strings.Contains(f.read(f.local("rtk")), "0.1.0") {
			t.Fatal("working rtk displaced by a broken one")
		}
	})
}

const agyInstaller = `d="$2"; printf '#!/bin/sh\necho 7.7.7\n' >"$d/agy"; chmod +x "$d/agy"` + "\n"

func TestSelfUpdateAGY(t *testing.T) {
	t.Run("installs latest and writes the marker", func(t *testing.T) {
		f := newSU(t, goodRTK, "", agyInstaller, "")
		f.env.selfUpdateAGY(context.Background())
		if f.read(f.local("agy.version")) != "" || strings.TrimSpace(f.read(f.local(agyMarker))) != "7.7.7" || !usable(f.local("agy")) {
			t.Fatalf("not installed:\n%s", f.out)
		}
	})
	t.Run("skip when the binary is latest", func(t *testing.T) {
		f := newSU(t, goodRTK, "", agyInstaller, "")
		f.write(t, f.local("agy"), "#!/bin/sh\necho 7.7.7\n")
		f.env.selfUpdateAGY(context.Background())
		if !strings.Contains(f.out.String(), "agy already latest (7.7.7); skip") || f.hit("agy-install.sh") != 0 {
			t.Fatalf("re-fetched:\n%s", f.out)
		}
	})
	t.Run("a stale marker does not make a latest binary skip-proof the other way", func(t *testing.T) {
		f := newSU(t, goodRTK, "", agyInstaller, "")
		f.write(t, f.local("agy"), "#!/bin/sh\necho 7.7.7\n")
		f.write(t, f.local(agyMarker), "1.1.1\n")
		f.env.selfUpdateAGY(context.Background())
		if f.hit("agy-install.sh") != 0 {
			t.Fatal("the marker, not the binary, decided")
		}
	})
	t.Run("a failing script keeps the previous copy", func(t *testing.T) {
		f := newSU(t, goodRTK, "", "", "")
		f.write(t, f.local("agy"), "#!/bin/sh\necho 1.1.1\n")
		f.env.selfUpdateAGY(context.Background())
		if !strings.Contains(f.out.String(), "WARN: agy update failed (using previous version)") || !strings.Contains(f.read(f.local("agy")), "1.1.1") {
			t.Fatalf("previous copy lost:\n%s", f.out)
		}
	})
	t.Run("a candidate that prints no version is not installed", func(t *testing.T) {
		f := newSU(t, goodRTK, "", `d="$2"; printf '#!/bin/sh\nexit 0\n' >"$d/agy"; chmod +x "$d/agy"`+"\n", "")
		f.write(t, f.local("agy"), "#!/bin/sh\necho 1.1.1\n")
		f.env.selfUpdateAGY(context.Background())
		if !strings.Contains(f.read(f.local("agy")), "1.1.1") {
			t.Fatal("a silent agy displaced a working one")
		}
	})
}

// cursorBundle is the upstream layout: a version tree under share/cursor-agent/versions plus the
// bin link, all under $HOME (which the self-update points at a private directory).
func cursorBundle(body string) string {
	return `v="$HOME/.local/share/cursor-agent/versions/2026.2.0-abc123"; mkdir -p "$v" "$HOME/.local/bin"; ` +
		`printf '` + body + `' >"$v/cursor-agent"; chmod +x "$v/cursor-agent"; ` +
		`ln -sf "$v/cursor-agent" "$HOME/.local/bin/cursor-agent"` + "\n"
}

const cursorHeader = "# https://downloads.cursor.com/lab/2026.2.0-abc123/linux/x64/agent-cli-package.tar.gz\n"

var cursorInstaller = cursorHeader + cursorBundle(`#!/bin/sh\necho 2026.2.0-abc123\n`)

func TestSelfUpdateCursor(t *testing.T) {
	t.Run("installs latest", func(t *testing.T) {
		f := newSU(t, goodRTK, "", "", cursorInstaller)
		f.env.selfUpdateCursor(context.Background())
		if !strings.Contains(f.out.String(), "cursor updated:") || !usable(f.local("cursor-agent")) {
			t.Fatalf("not installed:\n%s", f.out)
		}
	})
	t.Run("skip when PATH's cursor-agent is latest", func(t *testing.T) {
		f := newSU(t, goodRTK, "", "", cursorInstaller)
		f.write(t, filepath.Join(f.pathDir, "cursor-agent"), "#!/bin/sh\necho 2026.2.0-abc123\n")
		f.env.selfUpdateCursor(context.Background())
		if !strings.Contains(f.out.String(), "cursor already latest") || usable(f.local("cursor-agent")) {
			t.Fatalf("re-installed:\n%s", f.out)
		}
	})
	t.Run("a failing script reports and keeps going", func(t *testing.T) {
		f := newSU(t, goodRTK, "", "", "")
		f.env.selfUpdateCursor(context.Background())
		if !strings.Contains(f.out.String(), "WARN: cursor update failed (using baked version)") {
			t.Fatalf("no warning:\n%s", f.out)
		}
	})
}

// plantCursor makes a working cursor install at version v linked from ~/.local/bin.
func (f *suFixture) plantCursor(t *testing.T, v string) string {
	t.Helper()
	d := filepath.Join(f.home, ".local", "share", "cursor-agent", "versions", v)
	f.write(t, filepath.Join(d, "cursor-agent"), "#!/bin/sh\necho "+v+"\n")
	if err := os.MkdirAll(filepath.Dir(f.local("cursor-agent")), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(d, "cursor-agent"), f.local("cursor-agent")); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestSelfUpdateCursorIsolatedInstall(t *testing.T) {
	t.Run("the script runs against a private HOME: a script that deletes the link and fails leaves the real one", func(t *testing.T) {
		f := newSU(t, goodRTK, "", "", cursorHeader+`rm -f "$HOME/.local/bin/cursor-agent"; exit 1`+"\n")
		f.plantCursor(t, "1.0.0")
		f.env.selfUpdateCursor(context.Background())
		if out, _, err := f.env.probeArgs(f.local("cursor-agent"), nil, nil); err != nil || out != "1.0.0" {
			t.Fatalf("working copy lost: %q %v\n%s", out, err, f.out)
		}
		if !strings.Contains(f.out.String(), "WARN: cursor update failed (using previous version)") {
			t.Fatalf("no warning:\n%s", f.out)
		}
	})
	t.Run("a bundle that does not run is never swapped in", func(t *testing.T) {
		f := newSU(t, goodRTK, "", "", cursorHeader+cursorBundle(`#!/bin/sh\nexit 1\n`))
		f.plantCursor(t, "1.0.0")
		f.env.selfUpdateCursor(context.Background())
		if out, _, err := f.env.probeArgs(f.local("cursor-agent"), nil, nil); err != nil || out != "1.0.0" {
			t.Fatalf("a broken bundle displaced the working one: %q %v", out, err)
		}
		if _, err := os.Stat(filepath.Join(f.home, ".local", "share", "cursor-agent", "versions", "2026.2.0-abc123")); err == nil {
			t.Fatal("the broken version was published")
		}
	})
	t.Run("a bundle with only the execute bit is refused", func(t *testing.T) {
		f := newSU(t, goodRTK, "", "", cursorHeader+cursorBundle(`not a program\n`))
		f.plantCursor(t, "1.0.0")
		f.env.selfUpdateCursor(context.Background())
		if !strings.Contains(f.out.String(), "WARN: cursor update failed") || !strings.Contains(f.read(filepath.Join(f.home, ".local", "share", "cursor-agent", "versions", "1.0.0", "cursor-agent")), "1.0.0") {
			t.Fatalf("working copy not kept:\n%s", f.out)
		}
	})
	t.Run("a bundle that exits 0 and prints nothing is refused", func(t *testing.T) {
		f := newSU(t, goodRTK, "", "", cursorHeader+cursorBundle(`#!/bin/sh\nexit 0\n`))
		f.plantCursor(t, "1.0.0")
		f.env.selfUpdateCursor(context.Background())
		if out, _, err := f.env.probeArgs(f.local("cursor-agent"), nil, nil); err != nil || out != "1.0.0" {
			t.Fatalf("a silent bundle displaced the working one: %q %v", out, err)
		}
	})
	t.Run("a candidate that runs inside its tree but not through the bin link rolls everything back, a real-file link included", func(t *testing.T) {
		f := newSU(t, goodRTK, "", "", cursorHeader+cursorBundle(`#!/bin/sh\ncase "$0" in */versions/*) echo 2026.2.0-abc123;; *) exit 1;; esac\n`))
		f.write(t, f.local("cursor-agent"), "#!/bin/sh\necho realfile\n")
		f.env.selfUpdateCursor(context.Background())
		if out, _, err := f.env.probeArgs(f.local("cursor-agent"), nil, nil); err != nil || out != "realfile" {
			t.Fatalf("the real file was not restored: %q %v\n%s", out, err, f.out)
		}
		if _, err := os.Stat(filepath.Join(f.home, ".local", "share", "cursor-agent", "versions", "2026.2.0-abc123")); err == nil {
			t.Fatal("the failed version stayed published")
		}
	})
	t.Run("a healthy tree of the same version is kept and only relinked", func(t *testing.T) {
		f := newSU(t, goodRTK, "", "", cursorInstaller)
		d := f.plantCursor(t, "2026.2.0-abc123")
		f.write(t, filepath.Join(d, "keep.txt"), "x")
		os.Remove(f.local("cursor-agent"))
		f.env.selfUpdateCursor(context.Background())
		if _, err := os.Stat(filepath.Join(d, "keep.txt")); err != nil || !usable(f.local("cursor-agent")) {
			t.Fatalf("healthy tree replaced or link missing:\n%s", f.out)
		}
	})
	t.Run("a first install also gets the agent alias, an existing foreign one is left", func(t *testing.T) {
		withAlias := cursorInstaller + `ln -sf "$HOME/.local/share/cursor-agent/versions/2026.2.0-abc123/cursor-agent" "$HOME/.local/bin/agent"` + "\n"
		f := newSU(t, goodRTK, "", "", withAlias)
		f.env.selfUpdateCursor(context.Background())
		if tg, err := os.Readlink(f.local("agent")); err != nil || !strings.Contains(tg, "2026.2.0-abc123") {
			t.Fatalf("alias not published: %q %v", tg, err)
		}
		g := newSU(t, goodRTK, "", "", withAlias)
		g.write(t, g.local("agent"), "#!/bin/sh\necho foreign\n")
		g.env.selfUpdateCursor(context.Background())
		if !strings.Contains(g.read(g.local("agent")), "foreign") {
			t.Fatal("another tool's agent was replaced")
		}
	})
	t.Run("publishes the new version, repoints the link and leaves no stage", func(t *testing.T) {
		f := newSU(t, goodRTK, "", "", cursorInstaller)
		f.plantCursor(t, "1.0.0")
		f.env.selfUpdateCursor(context.Background())
		if out, _, err := f.env.probeArgs(f.local("cursor-agent"), nil, nil); err != nil || out != "2026.2.0-abc123" {
			t.Fatalf("not updated: %q %v\n%s", out, err, f.out)
		}
		ents, _ := os.ReadDir(filepath.Join(f.home, ".local", "share", "cursor-agent", "versions"))
		for _, de := range ents {
			if strings.HasPrefix(de.Name(), ".af-up-") {
				t.Fatalf("stage left behind: %s", de.Name())
			}
		}
	})
}

func TestCursorSweepStaleUsesTheEntrysOwnName(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	// A HOME whose own path contains the reserved marker must not change where a tree goes back to.
	f.env.Home = filepath.Join(f.home, "x.af-old.y")
	versions := f.env.cursorShare()
	f.write(t, filepath.Join(versions, "1.0.0.af-old.9", "cursor-agent"), "#!/bin/sh\necho 1.0.0\n")
	f.write(t, filepath.Join(versions, "2.0.0", "cursor-agent"), "#!/bin/sh\necho 2.0.0\n")
	f.write(t, filepath.Join(versions, "2.0.0.af-old.9", "cursor-agent"), "#!/bin/sh\necho stale\n")
	os.MkdirAll(filepath.Join(versions, ".af-up-zzz"), 0o755)
	f.env.cursorSweepStale()
	if f.read(filepath.Join(versions, "1.0.0", "cursor-agent")) == "" {
		t.Fatal("the aside tree did not go back to its own name")
	}
	if !strings.Contains(f.read(filepath.Join(versions, "2.0.0", "cursor-agent")), "2.0.0") {
		t.Fatal("a present version was replaced by its stale copy")
	}
	for _, n := range []string{"2.0.0.af-old.9", ".af-up-zzz"} {
		if _, err := os.Lstat(filepath.Join(versions, n)); err == nil {
			t.Fatalf("%s left", n)
		}
	}
}

func TestCursorSweepStaleIgnoresGlobCharactersInHome(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	root := t.TempDir()
	f.env.Home = filepath.Join(root, "h[a]")
	sibling := filepath.Join(root, "ha") // what the pattern "h[a]" would match
	f.write(t, filepath.Join(sibling, ".local", "share", "cursor-agent", "versions", ".af-up-x", "f"), "x")
	f.write(t, filepath.Join(sibling, ".local", "share", "cursor-agent", "versions", "1.0.0.af-old.5", "cursor-agent"), "x")
	f.write(t, filepath.Join(f.env.cursorShare(), "1.0.0.af-old.7", "cursor-agent"), "x")
	f.write(t, filepath.Join(f.env.cursorShare(), "1.0.0.af-old.mine", "cursor-agent"), "x") // not our form
	f.env.cursorSweepStale()
	sv := filepath.Join(sibling, ".local", "share", "cursor-agent", "versions")
	for _, n := range []string{".af-up-x", "1.0.0.af-old.5"} {
		if _, err := os.Lstat(filepath.Join(sv, n)); err != nil {
			t.Fatalf("a sibling's %s was touched", n)
		}
	}
	if f.read(filepath.Join(f.env.cursorShare(), "1.0.0", "cursor-agent")) == "" {
		t.Fatal("own aside tree not restored")
	}
	if _, err := os.Lstat(filepath.Join(f.env.cursorShare(), "1.0.0.af-old.mine")); err != nil {
		t.Fatal("an entry not of our form was moved")
	}
}

func TestCursorSweepStaleSparesATreeALauncherPointsAt(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	d := f.plantCursor(t, "1.0.0.af-old.7") // a member's own version whose name looks like ours
	f.env.cursorSweepStale()
	if !usable(f.local("cursor-agent")) {
		t.Fatalf("the live tree %s was swept", d)
	}
}

func TestCursorSweepStaleSparesALiveTreeBehindALinkedVersionsDir(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	other := t.TempDir() // "another volume"
	share := filepath.Join(f.home, ".local", "share", "cursor-agent")
	if err := os.MkdirAll(share, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, filepath.Join(share, "versions")); err != nil {
		t.Fatal(err)
	}
	live := filepath.Join(other, "1.0.0.af-old.7")
	f.write(t, filepath.Join(live, "cursor-agent"), "#!/bin/sh\necho live\n")
	f.write(t, filepath.Join(other, "1.0.0", "cursor-agent"), "#!/bin/sh\necho plain\n")
	if err := os.MkdirAll(filepath.Dir(f.local("cursor-agent")), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(live, "cursor-agent"), f.local("cursor-agent")); err != nil { // the physical path
		t.Fatal(err)
	}
	f.env.cursorSweepStale()
	if out, _, err := f.env.probeArgs(f.local("cursor-agent"), nil, nil); err != nil || out != "live" {
		t.Fatalf("the live tree was swept: %q %v", out, err)
	}
}

func TestSelfUpdateNPMSkipsWhileAnEarlierUpdateIsUnsettled(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	f := newSU(t, goodRTK, "", "", "")
	tree := f.plantNPMTree(t, "@anthropic-ai/claude-code", "claude", "2.0.0")
	bak := tree + npmBakSuffix
	f.write(t, filepath.Join(bak, "VERSION"), "1.0.0")
	f.write(t, f.env.npmMarker(), "accepted\nclaude\n")
	parent := filepath.Dir(bak)
	if err := os.Chmod(parent, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(parent, 0o755) })
	f.env.selfUpdateNPM()
	// The verify pass that now precedes the discard may turn the marker into a pending one (the
	// copy cannot be probed behind the locked parent); either way the record and backup stay.
	if _, err := os.Lstat(f.env.npmMarker()); err != nil {
		t.Fatalf("marker dropped: %v", err)
	}
	if f.read(filepath.Join(f.npmDir, "npm.log")) != "" {
		t.Fatal("npm ran over unsettled backups")
	}
	os.Chmod(parent, 0o755)
	f.env.npmRecoverAside(false)
	if _, err := os.Lstat(bak); err == nil {
		t.Fatal("backup not swept once the permission is back")
	}
}

func TestNPMMarkerWrite(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	t.Run("a member's file at the old fixed temp name survives", func(t *testing.T) {
		f.write(t, f.env.npmMarker()+".new", "mine")
		if err := f.env.npmWriteMarker("pending", []string{"claude"}); err != nil {
			t.Fatal(err)
		}
		if f.read(f.env.npmMarker()+".new") != "mine" {
			t.Fatal("the member's file was overwritten")
		}
	})
}

func TestNPMDiscardAsideUnknownStateIsNotGone(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	f := newSU(t, goodRTK, "", "", "")
	tree := f.plantNPMTree(t, "@anthropic-ai/claude-code", "claude", "2.0.0")
	bak := tree + npmBakSuffix
	f.write(t, filepath.Join(bak, "VERSION"), "1.0.0")
	parent := filepath.Dir(bak)
	if err := os.Chmod(parent, 0o000); err != nil { // neither unlinking nor even looking is possible
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(parent, 0o755) })
	if npmDiscardAside([]npmAside{{orig: tree, bak: bak}}) {
		t.Fatal("reported gone although the backup could not even be looked at")
	}
	os.Chmod(parent, 0o755)
	if !npmDiscardAside([]npmAside{{orig: tree, bak: bak}}) {
		t.Fatal("not swept once the permission is back")
	}
}

func TestNPMAcceptedCleanupFailureKeepsTheMarker(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	f := newSU(t, goodRTK, "", "", "")
	tree := f.plantNPMTree(t, "@anthropic-ai/claude-code", "claude", "2.0.0")
	bak := tree + npmBakSuffix
	f.write(t, filepath.Join(bak, "VERSION"), "1.0.0")
	f.write(t, f.env.npmMarker(), "accepted\nclaude\n")
	parent := filepath.Dir(bak)
	if err := os.Chmod(parent, 0o555); err != nil { // the backup cannot be unlinked from here
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(parent, 0o755) })
	f.env.npmRecoverAside(false)
	if _, err := os.Lstat(f.env.npmMarker()); err != nil {
		t.Fatal("marker dropped although a backup is left")
	}
	os.Chmod(parent, 0o755)
	f.env.npmRecoverAside(false)
	if _, err := os.Lstat(bak); err == nil {
		t.Fatal("backup not swept on the next start")
	}
	if _, err := os.Lstat(f.env.npmMarker()); err == nil {
		t.Fatal("marker left after a clean sweep")
	}
}

func TestScriptOverSizeLimitFailsAndDoesNotRun(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	big := "touch " + marker + "\n" + strings.Repeat("#"+strings.Repeat("x", 998)+"\n", maxScriptBytes/1000+10)
	f := newSU(t, goodRTK, "", "", big)
	err := f.env.runScript(context.Background(), f.env.CursorInstallURL)
	if !errors.Is(err, ErrScriptTooLarge) {
		t.Fatalf("err=%v, want ErrScriptTooLarge", err)
	}
	if _, serr := os.Stat(marker); serr == nil {
		t.Fatal("a truncated script was run")
	}
	if got := f.env.fetchText(context.Background(), f.env.CursorInstallURL); got != "" {
		t.Fatalf("fetchText returned %d bytes of an over-long body", len(got))
	}
	f.plantCursor(t, "1.0.0")
	f.env.selfUpdateCursor(context.Background())
	if _, serr := os.Stat(marker); serr == nil {
		t.Fatal("selfUpdateCursor ran an over-long script")
	}
	t.Run("a script exactly at the limit is accepted", func(t *testing.T) {
		body := "true\n"
		body += strings.Repeat("#", maxScriptBytes-len(body)-1) + "\n"
		b, err := readCapped(strings.NewReader(body), maxScriptBytes)
		if err != nil || len(b) != maxScriptBytes {
			t.Fatalf("len=%d err=%v", len(b), err)
		}
	})
}

// plantNPMTree makes the lib tree and npm's relative bin link of a CLI the way npm lays them out.
func (f *suFixture) plantNPMTree(t *testing.T, pkg, name, version string) string {
	t.Helper()
	tree := filepath.Join(f.home, ".local", "lib", "node_modules", filepath.FromSlash(pkg))
	f.write(t, filepath.Join(tree, "VERSION"), version)
	f.write(t, filepath.Join(tree, "cli.sh"), "#!/bin/sh\necho "+version+"\n")
	if err := os.MkdirAll(filepath.Dir(f.local(name)), 0o755); err != nil {
		t.Fatal(err)
	}
	rel, _ := filepath.Rel(filepath.Dir(f.local(name)), filepath.Join(tree, "cli.sh"))
	if err := os.Symlink(rel, f.local(name)); err != nil {
		t.Fatal(err)
	}
	return tree
}

func TestSelfUpdateNPMFailureRestoresTreeAndBin(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	tree := f.plantNPMTree(t, "@anthropic-ai/claude-code", "claude", "1.0.0")
	// npm dies after writing a half tree and a broken bin entry.
	f.write(t, filepath.Join(f.npmDir, "failafter"), "npm ERR! ENOSPC\n")
	f.write(t, filepath.Join(f.npmDir, "body.claude"), brokenCLI)
	stub := strings.Replace(f.read(filepath.Join(f.npmDir, "npm")), `mkdir -p "$prefix/bin"`, `mkdir -p "$prefix/bin" "$prefix/lib/node_modules/@anthropic-ai/claude-code"; echo half >"$prefix/lib/node_modules/@anthropic-ai/claude-code/VERSION"`, 1)
	f.write(t, filepath.Join(f.npmDir, "npm"), stub)
	f.env.selfUpdateNPM()
	if got := f.read(filepath.Join(tree, "VERSION")); got != "1.0.0" {
		t.Fatalf("tree not restored: %q\n%s", got, f.out)
	}
	if out, _, err := f.env.probe(f.local("claude")); err != nil || out != "1.0.0" {
		t.Fatalf("bin not restored: %q %v", out, err)
	}
	for _, p := range []string{tree + npmBakSuffix, f.local("claude") + npmBakSuffix} {
		if _, err := os.Lstat(p); err == nil {
			t.Fatalf("backup left: %s", p)
		}
	}
}

func TestNPMRecoverAside(t *testing.T) {
	setup := func(t *testing.T, state string) (*suFixture, string) {
		f := newSU(t, goodRTK, "", "", "")
		tree := f.plantNPMTree(t, "@anthropic-ai/claude-code", "claude", "1.0.0")
		for _, p := range []string{tree, f.local("claude")} {
			if err := os.Rename(p, p+npmBakSuffix); err != nil {
				t.Fatal(err)
			}
		}
		if state != "" {
			f.write(t, f.env.npmMarker(), state+"\nclaude\n")
		}
		return f, tree
	}
	t.Run("pending and nothing new: the backup goes back", func(t *testing.T) {
		f, tree := setup(t, "pending")
		f.env.npmRecoverAside(false)
		if f.read(filepath.Join(tree, "VERSION")) != "1.0.0" || !usable(f.local("claude")) {
			t.Fatal("not restored")
		}
		if _, err := os.Stat(f.env.npmMarker()); err == nil {
			t.Fatal("marker left")
		}
	})
	t.Run("pending and a new copy that runs is still replaced by the previous one", func(t *testing.T) {
		f, tree := setup(t, "pending")
		f.write(t, filepath.Join(tree, "VERSION"), "2.0.0") // npm got this far before the kill
		f.write(t, f.local("claude"), "#!/bin/sh\necho 2.0.0\n")
		f.env.npmRecoverAside(false)
		if out, _, err := f.env.probe(f.local("claude")); err != nil || out != "1.0.0" || f.read(filepath.Join(tree, "VERSION")) != "1.0.0" {
			t.Fatalf("mixed state: %q %v", out, err)
		}
	})
	t.Run("only the bin entry still backed up (killed while restoring)", func(t *testing.T) {
		f, tree := setup(t, "pending")
		if err := os.Rename(tree+npmBakSuffix, tree); err != nil {
			t.Fatal(err)
		}
		f.write(t, f.local("claude"), "#!/bin/sh\necho half\n")
		f.env.npmRecoverAside(false)
		if out, _, err := f.env.probe(f.local("claude")); err != nil || out != "1.0.0" {
			t.Fatalf("bin not restored: %q %v", out, err)
		}
	})
	t.Run("accepted: the listed backups go, the accepted copy stays", func(t *testing.T) {
		f, tree := setup(t, "accepted")
		f.write(t, filepath.Join(tree, "VERSION"), "2.0.0")
		f.env.npmRecoverAside(false)
		if f.read(filepath.Join(tree, "VERSION")) != "2.0.0" {
			t.Fatal("the accepted copy was replaced")
		}
		if _, err := os.Lstat(tree + npmBakSuffix); err == nil {
			t.Fatal("stale backup kept")
		}
	})
	t.Run("no marker: a *.af-bak is the member's own and is left alone", func(t *testing.T) {
		f, tree := setup(t, "")
		f.env.npmRecoverAside(false)
		if _, err := os.Lstat(tree + npmBakSuffix); err != nil {
			t.Fatal("a backup the update did not make was touched")
		}
		if _, err := os.Lstat(f.local("claude") + npmBakSuffix); err != nil {
			t.Fatal("a backup the update did not make was touched")
		}
	})
	t.Run("an update does not overwrite a backup name that is already taken", func(t *testing.T) {
		f := newSU(t, goodRTK, "", "", "")
		f.plantNPMTree(t, "@anthropic-ai/claude-code", "claude", "1.0.0")
		f.write(t, f.local("claude")+npmBakSuffix, "mine")
		f.env.selfUpdateNPM()
		if f.read(f.local("claude")+npmBakSuffix) != "mine" || f.read(filepath.Join(f.npmDir, "npm.log")) != "" {
			t.Fatalf("the member's file was overwritten or npm ran anyway:\n%s", f.out)
		}
	})
	t.Run("a self-update start recovers first", func(t *testing.T) {
		f, tree := setup(t, "pending")
		f.write(t, filepath.Join(f.npmDir, "fail"), "npm ERR! network boom\n")
		f.env.selfUpdateNPM()
		if f.read(filepath.Join(tree, "VERSION")) != "1.0.0" || !usable(f.local("claude")) {
			t.Fatalf("not restored:\n%s", f.out)
		}
	})
}

// realNPM makes the stub lay out what npm does: the package tree and a relative bin link, and
// refuse (EEXIST) to overwrite a bin that is not a link.
func (f *suFixture) realNPM(t *testing.T) {
	t.Helper()
	stub := f.read(filepath.Join(f.npmDir, "npm"))
	old := `          if [ -f "$f" ]; then cp "$f" "$prefix/bin/$cli"; else printf '#!/bin/sh\necho 1.0.0\n' >"$prefix/bin/$cli"; fi
          chmod +x "$prefix/bin/$cli"`
	if !strings.Contains(stub, old) {
		t.Fatal("npm stub changed")
	}
	repl := `          [ -e "$prefix/bin/$cli" ] && [ ! -L "$prefix/bin/$cli" ] && { echo "npm ERR! EEXIST $prefix/bin/$cli" >&2; exit 1; }
          pkg=${1%@*}; tree="$prefix/lib/node_modules/$pkg"; mkdir -p "$tree"
          echo "new-$cli" >"$tree/VERSION"
          if [ -f "$f" ]; then cp "$f" "$tree/cli.sh"; else printf '#!/bin/sh\necho 9.9.9\n' >"$tree/cli.sh"; fi
          chmod +x "$tree/cli.sh"; rm -f "$prefix/bin/$cli"; ln -s "../lib/node_modules/$pkg/cli.sh" "$prefix/bin/$cli"`
	f.write(t, filepath.Join(f.npmDir, "npm"), strings.Replace(stub, old, repl, 1))
}

func TestSelfUpdateNPMSuccessInstallsNewTreeAndDropsBackups(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	f.realNPM(t)
	tree := f.plantNPMTree(t, "@anthropic-ai/claude-code", "claude", "1.0.0")
	f.env.selfUpdateNPM()
	if f.read(filepath.Join(tree, "VERSION")) != "new-claude\n" {
		t.Fatalf("new tree missing: %q\n%s", f.read(filepath.Join(tree, "VERSION")), f.out)
	}
	if out, _, err := f.env.probe(f.local("claude")); err != nil || out != "9.9.9" {
		t.Fatalf("new claude not in place: %q %v", out, err)
	}
	// The backups outlive the update itself: the start's final verify pass settles them.
	if st, _, ok, _ := f.env.npmReadMarker(); !ok || st != "accepted" {
		t.Fatalf("marker state %q ok=%v, want accepted", st, ok)
	}
	f.env.npmRecoverAside(false)
	for _, p := range []string{tree + npmBakSuffix, f.local("claude") + npmBakSuffix, f.env.npmMarker()} {
		if _, err := os.Lstat(p); err == nil {
			t.Fatalf("left behind after a good update: %s", p)
		}
	}
}

func TestSelfUpdateNPMLeavesAnUnmanagedBinAlone(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	f.realNPM(t)
	f.write(t, f.local("claude"), "#!/bin/sh\necho mine\n") // not npm's link
	f.env.selfUpdateNPM()
	if out, _, err := f.env.probe(f.local("claude")); err != nil || out != "mine" {
		t.Fatalf("the member's own claude was replaced or moved: %q %v\n%s", out, err, f.out)
	}
	if _, err := os.Lstat(f.local("claude") + npmBakSuffix); err == nil {
		t.Fatal("unmanaged bin left as a backup")
	}
}

func TestSelfUpdateNPMOneSlowProbeDoesNotCostThePreviousCopyOrTriggerAPinRepair(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	f.realNPM(t)
	tree := f.plantNPMTree(t, "@anthropic-ai/claude-code", "claude", "1.0.0")
	// The new copy answers once; any later probe fails (a slow start under load). A pin that
	// differs from the installed latest must not make the verify pass reinstall over it.
	cnt := filepath.Join(f.npmDir, "cnt")
	f.write(t, filepath.Join(f.npmDir, "body.claude"), "#!/bin/sh\nn=$(cat "+cnt+" 2>/dev/null || echo 0); echo $((n+1)) >"+cnt+"; [ \"$n\" -lt 1 ] || exit 1; echo 2.0.0\n")
	f.env.Pins = map[string]string{"claude": "0.9.0"}
	f.env.selfUpdateNPM()
	if strings.Contains(f.read(filepath.Join(f.npmDir, "npm.log")), "claude-code@0.9.0") {
		t.Fatal("the verify pass reinstalled the pin over an accepted update")
	}
	if f.read(filepath.Join(tree, "VERSION")) != "new-claude\n" {
		t.Fatalf("the update was not kept: %q", f.read(filepath.Join(tree, "VERSION")))
	}
}

func TestSelfUpdateNPMNewCopyThatDoesNotRunGoesBack(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	f.realNPM(t)
	tree := f.plantNPMTree(t, "@anthropic-ai/claude-code", "claude", "1.0.0")
	f.write(t, filepath.Join(f.npmDir, "body.claude"), brokenCLI)
	f.env.Pins = map[string]string{}
	f.env.selfUpdateNPM()
	if out, _, err := f.env.probe(f.local("claude")); err != nil || out != "1.0.0" {
		t.Fatalf("previous claude not kept: %q %v\n%s", out, err, f.out)
	}
	if f.read(filepath.Join(tree, "VERSION")) != "1.0.0" {
		t.Fatal("previous tree not kept")
	}
	if !strings.Contains(f.out.String(), "updated claude does not run; kept the previous copy") {
		t.Fatalf("no report:\n%s", f.out)
	}
}

func TestSelfUpdateAlwaysExitsZero(t *testing.T) {
	f := newSU(t, goodRTK, strings.Repeat("0", 64), "", "")
	if rc := SelfUpdate(context.Background(), f.env); rc != ExitOK {
		t.Fatalf("rc=%d: a failed step must not fail the start", rc)
	}
}

func TestSelfUpdateNPMLsFailureForcesUpdate(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	var ls strings.Builder
	ls.WriteString(`{"dependencies":{`)
	for i, c := range npmCLIs {
		if i > 0 {
			ls.WriteString(",")
		}
		fmt.Fprintf(&ls, `%q:{"version":"5.0.0"}`, c.Pkg)
		f.write(t, filepath.Join(f.npmDir, "view."+strings.ReplaceAll(c.Pkg, "/", "_")), "5.0.0\n")
	}
	ls.WriteString("}}")
	f.write(t, filepath.Join(f.npmDir, "ls.json"), ls.String())
	// Versions all match, but npm ls exits non-zero (a broken tree): the shell updated.
	stub := strings.Replace(f.read(filepath.Join(f.npmDir, "npm")), "ls) [ -f \"$D/ls.json\" ] && cat \"$D/ls.json\"; exit 0 ;;", "ls) [ -f \"$D/ls.json\" ] && cat \"$D/ls.json\"; exit 1 ;;", 1)
	f.write(t, filepath.Join(f.npmDir, "npm"), stub)
	f.env.selfUpdateNPM()
	if f.read(filepath.Join(f.npmDir, "npm.log")) == "" {
		t.Fatalf("skipped on a failing npm ls:\n%s", f.out)
	}
}

func TestSelfUpdateNPMFailureRepairsBrokenLink(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	// The failed install leaves a claude that does not run; the verify pass must reinstall the pin.
	// Only the first install fails (after writing a claude that does not run); the repair works.
	stub := strings.Replace(f.read(filepath.Join(f.npmDir, "npm")), `if [ -f "$D/failafter" ]`, `if [ -f "$D/failafter" ] && [ "$n" -eq 1 ]`, 1)
	f.write(t, filepath.Join(f.npmDir, "npm"), stub)
	f.write(t, filepath.Join(f.npmDir, "failafter"), "npm ERR! ENOSPC\n")
	f.write(t, filepath.Join(f.npmDir, "body.claude"), brokenCLI)
	f.write(t, filepath.Join(f.npmDir, "body2.claude"), "#!/bin/sh\necho 1.0.0\n")
	f.env.selfUpdateNPM()
	log := f.read(filepath.Join(f.npmDir, "npm.log"))
	if !strings.Contains(log, "@anthropic-ai/claude-code@1.0.0") {
		t.Fatalf("no pin reinstall after the failed update: %s", log)
	}
	if out, _, err := f.env.probe(f.local("claude")); err != nil || out != "1.0.0" {
		t.Fatalf("claude not repaired to the pin: %q %v", out, err)
	}
}

func TestSelfUpdateStepsAreIndependent(t *testing.T) {
	t.Run("npm alone failing still installs rtk, agy and cursor", func(t *testing.T) {
		f := newSU(t, goodRTK, "", agyInstaller, cursorInstaller)
		f.write(t, filepath.Join(f.npmDir, "fail"), "npm ERR! network boom\n")
		SelfUpdate(context.Background(), f.env)
		if !usable(f.local("rtk")) || !usable(f.local("agy")) || !usable(f.local("cursor-agent")) {
			t.Fatalf("a failed npm step stopped later ones:\n%s", f.out)
		}
	})
	t.Run("rtk alone failing still installs cursor", func(t *testing.T) {
		f := newSU(t, goodRTK, strings.Repeat("0", 64), agyInstaller, cursorInstaller)
		SelfUpdate(context.Background(), f.env)
		if usable(f.local("rtk")) || !usable(f.local("agy")) || !usable(f.local("cursor-agent")) {
			t.Fatalf("rtk failure handling wrong:\n%s", f.out)
		}
		if !strings.Contains(f.out.String(), "cursor updated: 2026.2.0-abc123") {
			t.Fatalf("cursor's new version not reported:\n%s", f.out)
		}
	})
}

func TestNPMRecoverRefusesAMarkerThatIsNotOurs(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	tree := f.plantNPMTree(t, "@anthropic-ai/claude-code", "claude", "1.0.0")
	if err := os.Rename(tree, tree+npmBakSuffix); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "victim")
	f.write(t, outside+npmBakSuffix, "keep")
	f.write(t, outside, "keep")
	for _, body := range []string{"accepted\nclaude\n" + outside + "\n", "pending\n../../etc\n", "weird\nclaude\n"} {
		f.write(t, f.env.npmMarker(), body)
		f.env.npmRecoverAside(false)
		if _, err := os.Lstat(tree + npmBakSuffix); err != nil {
			t.Fatalf("marker %q: a backup was touched", body)
		}
		if f.read(outside) != "keep" || f.read(outside+npmBakSuffix) != "keep" {
			t.Fatalf("marker %q: a path outside the npm tree was touched", body)
		}
	}
}

func TestNPMRecoverKeepAcceptedLeavesBackupsForTheFinalPass(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	tree := f.plantNPMTree(t, "@anthropic-ai/claude-code", "claude", "2.0.0")
	f.write(t, filepath.Join(tree+npmBakSuffix, "VERSION"), "1.0.0")
	f.write(t, f.env.npmMarker(), "accepted\nclaude\n")
	f.env.npmRecoverAside(true)
	if _, err := os.Lstat(tree + npmBakSuffix); err != nil {
		t.Fatal("backup discarded although the final pass may need it")
	}
	f.env.npmRecoverAside(false)
	if _, err := os.Lstat(tree + npmBakSuffix); err == nil {
		t.Fatal("backup not settled")
	}
}

func TestNPMVerifyPresentRestoresAnAcceptedCopyThatStopsRunning(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	tree := f.plantNPMTree(t, "@anthropic-ai/claude-code", "claude", "2.0.0")
	f.write(t, filepath.Join(tree, "cli.sh"), "#!/bin/sh\nexit 1\n") // the new copy does not run
	f.write(t, filepath.Join(tree+npmBakSuffix, "cli.sh"), "#!/bin/sh\necho 1.0.0\n")
	f.write(t, f.env.npmMarker(), "accepted\nclaude\n")
	f.env.npmRecoverAside(true)
	if rc := f.env.NPMVerifyPresent(); rc != ExitOK {
		t.Fatalf("rc=%d\n%s", rc, f.out)
	}
	if out, _, err := f.env.probe(f.local("claude")); err != nil || out != "1.0.0" {
		t.Fatalf("previous copy not back: %q %v\n%s", out, err, f.out)
	}
	if f.read(filepath.Join(f.npmDir, "npm.log")) != "" {
		t.Fatal("the pin was reinstalled instead of restoring the previous copy")
	}
}

func TestSelfUpdateNPMKeepsTheMarkerWhenARestoreFails(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	f := newSU(t, goodRTK, "", "", "")
	tree := f.plantNPMTree(t, "@anthropic-ai/claude-code", "claude", "1.0.0")
	parent := filepath.Dir(tree)
	// npm writes half a tree and dies; the parent then refuses the restore's unlink and rename.
	f.write(t, filepath.Join(f.npmDir, "failafter"), "npm ERR! ENOSPC\n")
	f.write(t, filepath.Join(f.npmDir, "body.claude"), brokenCLI)
	stub := strings.Replace(f.read(filepath.Join(f.npmDir, "npm")), `mkdir -p "$prefix/bin"`, `mkdir -p "$prefix/bin" "$prefix/lib/node_modules/@anthropic-ai/claude-code"; echo half >"$prefix/lib/node_modules/@anthropic-ai/claude-code/VERSION"; chmod 555 `+parent, 1)
	f.write(t, filepath.Join(f.npmDir, "npm"), stub)
	t.Cleanup(func() { os.Chmod(parent, 0o755) })
	f.env.selfUpdateNPM()
	os.Chmod(parent, 0o755)
	if _, err := os.Lstat(tree + npmBakSuffix); err != nil {
		t.Fatalf("setup: the restore did not fail:\n%s", f.out)
	}
	state, clis, ok, bad := f.env.npmReadMarker()
	if !ok || bad || state != "pending" || len(clis) != 1 || clis[0] != "claude" {
		t.Fatalf("marker = %q %v ok=%v bad=%v while the backup is still on disk", state, clis, ok, bad)
	}
	if !strings.Contains(f.out.String(), tree+npmBakSuffix) {
		t.Fatalf("the old tree's location is not in the output:\n%s", f.out)
	}
	f.env.npmRecoverAside(false) // the next start puts it back
	if f.read(filepath.Join(tree, "VERSION")) != "1.0.0" {
		t.Fatal("not restored on the next start")
	}
}

func TestSelfUpdateNPMRefusesWhenOnlyTheBinBackupNameIsTaken(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	f.realNPM(t)
	tree := f.plantNPMTree(t, "@anthropic-ai/claude-code", "claude", "1.0.0")
	if err := os.Remove(f.local("claude")); err != nil { // no npm link: only the tree would move
		t.Fatal(err)
	}
	f.write(t, f.local("claude")+npmBakSuffix, "mine")
	f.env.selfUpdateNPM()
	f.env.npmRecoverAside(false)
	if f.read(f.local("claude")+npmBakSuffix) != "mine" {
		t.Fatalf("the member's file was touched:\n%s", f.out)
	}
	if f.read(filepath.Join(tree, "VERSION")) != "1.0.0" {
		t.Fatal("the update ran over a name recovery would treat as ours")
	}
}

func TestNPMMarkerFromAnEarlierFormatIsUnderstood(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	tree := f.plantNPMTree(t, "@anthropic-ai/claude-code", "claude", "1.0.0")
	for _, p := range []string{tree, f.local("claude")} {
		if err := os.Rename(p, p+npmBakSuffix); err != nil {
			t.Fatal(err)
		}
	}
	f.write(t, f.env.npmMarker(), "pending\n"+tree+"\n"+f.local("claude")+"\n")
	f.env.npmRecoverAside(false)
	if f.read(filepath.Join(tree, "VERSION")) != "1.0.0" || !usable(f.local("claude")) {
		t.Fatal("an absolute-path marker was not recovered")
	}
	f.write(t, f.env.npmMarker(), "pending\n/etc/passwd\n")
	f.env.npmRecoverAside(false)
	if _, err := os.Lstat(f.env.npmMarker()); err != nil {
		t.Fatal("a marker naming a foreign path was dropped instead of refused")
	}
}

func TestNPMVerifyPresentRestoresWhenTheAcceptedLinkVanished(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	tree := f.plantNPMTree(t, "@anthropic-ai/claude-code", "claude", "2.0.0")
	f.write(t, filepath.Join(tree+npmBakSuffix, "cli.sh"), "#!/bin/sh\necho 1.0.0\n")
	if err := os.Chmod(filepath.Join(tree+npmBakSuffix, "cli.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	rel, _ := filepath.Rel(filepath.Dir(f.local("claude")), filepath.Join(tree+npmBakSuffix, "cli.sh"))
	_ = rel
	if err := os.Symlink(filepath.Join(tree, "cli.sh"), f.local("claude")+npmBakSuffix); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.local("claude")); err != nil { // the new link is gone
		t.Fatal(err)
	}
	f.write(t, f.env.npmMarker(), "accepted\nclaude\n")
	f.env.npmRecoverAside(true)
	f.env.NPMVerifyPresent()
	f.env.npmRecoverAside(false)
	if f.read(filepath.Join(tree, "cli.sh")) != "#!/bin/sh\necho 1.0.0\n" {
		t.Fatalf("previous tree not back:\n%s", f.out)
	}
}

func TestNPMVerifyPresentKeepsAFailedRestoreForTheNextStart(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	f := newSU(t, goodRTK, "", "", "")
	tree := f.plantNPMTree(t, "@anthropic-ai/claude-code", "claude", "2.0.0")
	f.write(t, filepath.Join(tree, "cli.sh"), "#!/bin/sh\nexit 1\n")
	f.write(t, filepath.Join(tree+npmBakSuffix, "cli.sh"), "#!/bin/sh\necho 1.0.0\n")
	if err := os.Chmod(filepath.Join(tree+npmBakSuffix, "cli.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.write(t, f.env.npmMarker(), "accepted\nclaude\n")
	parent := filepath.Dir(tree)
	t.Cleanup(func() { os.Chmod(parent, 0o755) })
	f.env.npmRecoverAside(true)
	if err := os.Chmod(parent, 0o000); err != nil { // one transient fault: nothing in the scope dir is reachable
		t.Fatal(err)
	}
	f.env.NPMVerifyPresent()
	f.env.npmRecoverAside(false)
	os.Chmod(parent, 0o755)
	if _, err := os.Lstat(tree + npmBakSuffix); err != nil {
		t.Fatalf("the previous tree was discarded after a failed restore:\n%s", f.out)
	}
	if st, _, ok, _ := f.env.npmReadMarker(); !ok || st != "pending" {
		t.Fatalf("marker state %q ok=%v, want pending", st, ok)
	}
	f.env.npmRecoverAside(false) // permission is back: the next start restores it
	if f.read(filepath.Join(tree, "cli.sh")) != "#!/bin/sh\necho 1.0.0\n" {
		t.Fatal("previous tree not restored on the next start")
	}
}

func TestNPMLegacyMarkerRecoversOnlyTheRecordedSide(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	tree := f.plantNPMTree(t, "@anthropic-ai/claude-code", "claude", "2.0.0")
	f.write(t, filepath.Join(tree+npmBakSuffix, "VERSION"), "1.0.0")
	f.write(t, f.local("claude")+npmBakSuffix, "mine") // never recorded: the member's own
	f.write(t, f.env.npmMarker(), "accepted\n"+tree+"\n")
	f.env.npmRecoverAside(false)
	if _, err := os.Lstat(tree + npmBakSuffix); err == nil {
		t.Fatal("the recorded backup was not discarded")
	}
	if f.read(f.local("claude")+npmBakSuffix) != "mine" {
		t.Fatal("an unrecorded backup name was swept")
	}
}

// stuckAcceptedFixture: claude's new copy does not run and its previous copy cannot be put back
// (its scope directory is unreadable); codex's new copy is healthy.
func stuckAcceptedFixture(t *testing.T) (f *suFixture, claude, codex string) {
	f = newSU(t, goodRTK, "", "", "")
	claude = f.plantNPMTree(t, "@anthropic-ai/claude-code", "claude", "2.0.0")
	codex = f.plantNPMTree(t, "@openai/codex", "codex", "2.0.0")
	f.write(t, filepath.Join(claude, "cli.sh"), "#!/bin/sh\nexit 1\n")
	f.write(t, filepath.Join(claude+npmBakSuffix, "cli.sh"), "#!/bin/sh\necho 1.0.0\n")
	if err := os.Chmod(filepath.Join(claude+npmBakSuffix, "cli.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.write(t, filepath.Join(codex+npmBakSuffix, "VERSION"), "1.0.0")
	f.write(t, f.env.npmMarker(), "accepted\nclaude\ncodex\n")
	return f, claude, codex
}

func TestNPMVerifyPresentStepMixedKeepsOnlyTheStuckCLIAndFails(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	f, claude, codex := stuckAcceptedFixture(t)
	f.env.StatusPath = filepath.Join(t.TempDir(), "status.tsv")
	f.write(t, f.env.StatusPath, "")
	parent := filepath.Dir(claude)
	t.Cleanup(func() { os.Chmod(parent, 0o755) })
	if err := os.Chmod(parent, 0o000); err != nil {
		t.Fatal(err)
	}
	if rc := f.env.NPMVerifyPresentStep(); rc != ExitFailed {
		t.Fatalf("rc=%d, want %d for a copy that does not run and cannot be restored\n%s", rc, ExitFailed, f.out)
	}
	if f.env.lastStatus("claude") != statusFailed {
		t.Fatalf("claude status %q, want failed", f.env.lastStatus("claude"))
	}
	os.Chmod(parent, 0o755)
	if _, err := os.Lstat(codex + npmBakSuffix); err == nil {
		t.Fatal("the healthy CLI's backup was not settled")
	}
	st, ents, ok, bad := f.env.npmReadMarker()
	if !ok || bad || st != "pending" || len(ents) != 1 || ents[0] != "claude" {
		t.Fatalf("marker = %q %v; want pending [claude]", st, ents)
	}
	if _, err := os.Lstat(claude + npmBakSuffix); err != nil {
		t.Fatal("the stuck CLI's previous tree is gone")
	}
}

func TestNPMStuckRestoreSurvivesASecondStartWithTheSameFault(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	f, claude, _ := stuckAcceptedFixture(t)
	// The old bin link went back with the first attempt; the tree could not.
	if err := os.Rename(f.local("claude")+npmBakSuffix, f.local("claude")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	parent := filepath.Dir(claude)
	t.Cleanup(func() { os.Chmod(parent, 0o755) })
	if err := os.Chmod(parent, 0o000); err != nil {
		t.Fatal(err)
	}
	f.env.NPMVerifyPresentStep()
	f.env.NPMVerifyPresentStep() // the same fault on the next start
	if _, err := os.Lstat(f.local("claude")); err != nil {
		t.Fatalf("the link being restored was removed by the pin repair:\n%s", f.out)
	}
	if f.read(filepath.Join(f.npmDir, "npm.log")) != "" {
		t.Fatal("npm reinstalled over a CLI whose restore is pending")
	}
	os.Chmod(parent, 0o755)
	f.env.NPMVerifyPresentStep() // permission is back
	if out, _, err := f.env.probe(f.local("claude")); err != nil || out != "1.0.0" {
		t.Fatalf("previous copy not back: %q %v\n%s", out, err, f.out)
	}
}

func TestNPMVerifyPresentStepRestoresBeforeSettlingBackups(t *testing.T) {
	f, claude, codex := stuckAcceptedFixture(t)
	if rc := f.env.NPMVerifyPresentStep(); rc != ExitOK {
		t.Fatalf("rc=%d\n%s", rc, f.out)
	}
	if out, _, err := f.env.probe(f.local("claude")); err != nil || out != "1.0.0" {
		t.Fatalf("previous claude not back: %q %v\n%s", out, err, f.out)
	}
	for _, p := range []string{claude + npmBakSuffix, codex + npmBakSuffix, f.env.npmMarker()} {
		if _, err := os.Lstat(p); err == nil {
			t.Fatalf("left after the final pass: %s", p)
		}
	}
	if f.read(filepath.Join(f.npmDir, "npm.log")) != "" {
		t.Fatal("the pin was reinstalled instead of restoring the previous copy")
	}
	if f.read(filepath.Join(codex, "VERSION")) != "2.0.0" {
		t.Fatal("the healthy accepted copy was replaced")
	}
}

func TestSelfUpdateNPMMixedKeepsOnlyTheStuckCLIPending(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	f := newSU(t, goodRTK, "", "", "")
	f.realNPM(t)
	claude := f.plantNPMTree(t, "@anthropic-ai/claude-code", "claude", "1.0.0")
	codex := f.plantNPMTree(t, "@openai/codex", "codex", "1.0.0")
	f.write(t, filepath.Join(f.npmDir, "body.claude"), brokenCLI) // the new claude does not run
	// After npm lays claude's tree down its scope directory turns read-only, so putting the
	// previous tree back fails; codex's new copy is healthy.
	stub := f.read(filepath.Join(f.npmDir, "npm"))
	stub = strings.Replace(stub, `chmod +x "$tree/cli.sh"; rm -f`, `chmod +x "$tree/cli.sh"; [ "$cli" = claude ] && chmod 555 "$(dirname "$tree")"; rm -f`, 1)
	f.write(t, filepath.Join(f.npmDir, "npm"), stub)
	parent := filepath.Dir(claude)
	t.Cleanup(func() { os.Chmod(parent, 0o755) })
	f.env.selfUpdateNPM()
	os.Chmod(parent, 0o755)
	if _, err := os.Lstat(claude + npmBakSuffix); err != nil {
		t.Fatalf("setup: claude's previous tree was not left behind:\n%s", f.out)
	}
	st, ents, ok, bad := f.env.npmReadMarker()
	if !ok || bad || st != "pending" || len(ents) != 1 || ents[0] != "claude" {
		t.Fatalf("marker = %q %v; want pending [claude]", st, ents)
	}
	if _, err := os.Lstat(codex + npmBakSuffix); err == nil {
		t.Fatal("the accepted CLI's backup was kept although only claude is pending")
	}
	if f.read(filepath.Join(codex, "VERSION")) != "new-codex\n" {
		t.Fatal("codex's accepted copy was replaced")
	}
}

func TestSelfUpdateNPMUnreadableScopeKeepsTheRestoredLinkUntilRecovery(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	f := newSU(t, goodRTK, "", "", "")
	f.realNPM(t)
	claude := f.plantNPMTree(t, "@anthropic-ai/claude-code", "claude", "1.0.0")
	// The new claude is healthy; only its scope directory turns unreadable right after npm wrote
	// it, so the probe and the look at the previous tree both fail: one fault, nothing else.
	stub := f.read(filepath.Join(f.npmDir, "npm"))
	stub = strings.Replace(stub, `chmod +x "$tree/cli.sh"; rm -f`, `chmod +x "$tree/cli.sh"; [ "$cli" = claude ] && chmod 000 "$(dirname "$tree")"; rm -f`, 1)
	f.write(t, filepath.Join(f.npmDir, "npm"), stub)
	parent := filepath.Dir(claude)
	t.Cleanup(func() { os.Chmod(parent, 0o755) })
	f.env.selfUpdateNPM()
	if _, err := os.Lstat(f.local("claude")); err != nil {
		t.Fatalf("the restored link was removed in the same update:\n%s", f.out)
	}
	os.Chmod(parent, 0o755)
	if st, ents, ok, bad := f.env.npmReadMarker(); !ok || bad || st != "pending" || len(ents) != 1 || ents[0] != "claude" {
		t.Fatalf("marker = %q %v ok=%v", st, ents, ok)
	}
	f.env.npmRecoverAside(false)
	if out, _, err := f.env.probe(f.local("claude")); err != nil || out != "1.0.0" {
		t.Fatalf("the previous claude does not run after recovery: %q %v\n%s", out, err, f.out)
	}
}

func TestSelfUpdateNPMFailedInstallHoldsOnlyTheCLIThatDidNotGoBack(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	f := newSU(t, goodRTK, "", "", "")
	f.env.StatusPath = filepath.Join(t.TempDir(), "status.tsv")
	f.write(t, f.env.StatusPath, "")
	claude := f.plantNPMTree(t, "@anthropic-ai/claude-code", "claude", "1.0.0")
	codex := f.plantNPMTree(t, "@openai/codex", "codex", "1.0.0")
	// One fault: npm writes claude's tree, turns its scope unreadable and exits non-zero.
	f.write(t, filepath.Join(f.npmDir, "failafter"), "npm ERR! EACCES\n")
	stub := strings.Replace(f.read(filepath.Join(f.npmDir, "npm")), `mkdir -p "$prefix/bin"`, `mkdir -p "$prefix/bin" "$prefix/lib/node_modules/@anthropic-ai/claude-code"; echo half >"$prefix/lib/node_modules/@anthropic-ai/claude-code/VERSION"; chmod 000 `+filepath.Dir(claude), 1)
	f.write(t, filepath.Join(f.npmDir, "npm"), stub)
	t.Cleanup(func() { os.Chmod(filepath.Dir(claude), 0o755) })
	f.env.selfUpdateNPM()
	os.Chmod(filepath.Dir(claude), 0o755)
	if _, err := os.Lstat(claude + npmBakSuffix); err != nil {
		t.Fatalf("setup: claude's previous tree was not left behind:\n%s", f.out)
	}
	if st, ents, ok, bad := f.env.npmReadMarker(); !ok || bad || st != "pending" || len(ents) != 1 || ents[0] != "claude" {
		t.Fatalf("marker = %q %v; want pending [claude]", st, ents)
	}
	if out, _, err := f.env.probe(f.local("codex")); err != nil || out != "1.0.0" {
		t.Fatalf("codex was restored but does not run: %q %v", out, err)
	}
	if _, err := os.Lstat(codex + npmBakSuffix); err == nil {
		t.Fatal("codex's backup is still there")
	}
	if f.env.lastStatus("codex") == statusFailed {
		t.Fatal("codex was recorded failed although its previous copy is back")
	}
}

func TestNPMMarkerDiscardGroupIsRestoredAndSwept(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	claude := f.plantNPMTree(t, "@anthropic-ai/claude-code", "claude", "2.0.0")
	f.write(t, filepath.Join(claude+npmBakSuffix, "VERSION"), "1.0.0")
	opencode := f.plantNPMTree(t, "@opencode/cli", "opencode", "2.0.0")
	f.write(t, filepath.Join(opencode+npmBakSuffix, "VERSION"), "1.0.0")
	if err := f.env.npmWriteMarkerDiscard("pending", []string{"claude"}, []string{"opencode"}); err != nil {
		t.Fatal(err)
	}
	state, entries, discard, ok, bad := f.env.npmReadMarkerFull()
	if !ok || bad || state != "pending" || len(entries) != 1 || len(discard) != 1 || discard[0] != "opencode" {
		t.Fatalf("marker = %q %v %v ok=%v bad=%v", state, entries, discard, ok, bad)
	}
	f.env.npmRecoverAside(false)
	if f.read(filepath.Join(claude, "VERSION")) != "1.0.0" {
		t.Fatal("the pending CLI was not restored")
	}
	if _, err := os.Lstat(opencode + npmBakSuffix); err == nil {
		t.Fatal("the discard group's backup was not deleted")
	}
	if f.read(filepath.Join(opencode, "VERSION")) != "2.0.0" {
		t.Fatal("the accepted copy was replaced by its backup")
	}
	if _, err := os.Lstat(f.env.npmMarker()); err == nil {
		t.Fatal("marker left after both groups settled")
	}
}

func TestNPMMarkerDiscardGroupOnlyInPending(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	for _, body := range []string{"accepted\nclaude\naccepted\nopencode\n", "pending\nclaude\naccepted\nopencode\naccepted\ncodex\n"} {
		f.write(t, f.env.npmMarker(), body)
		if _, _, _, ok, bad := f.env.npmReadMarkerFull(); !ok || !bad {
			t.Fatalf("%q: not refused", body)
		}
	}
}

func TestNPMSettleAsideVerifiesAnAcceptedUpdateBeforeDiscarding(t *testing.T) {
	f := newSU(t, goodRTK, "", "", "")
	tree := f.plantNPMTree(t, "@anthropic-ai/claude-code", "claude", "2.0.0")
	f.write(t, filepath.Join(tree, "cli.sh"), "#!/bin/sh\nexit 1\n") // killed before the final verify: the new copy is broken
	f.write(t, filepath.Join(tree+npmBakSuffix, "cli.sh"), "#!/bin/sh\necho 1.0.0\n")
	f.write(t, f.env.npmMarker(), "accepted\nclaude\n")
	f.env.npmSettleAside()
	if out, _, err := f.env.probe(f.local("claude")); err != nil || out != "1.0.0" {
		t.Fatalf("previous copy not back: %q %v\n%s", out, err, f.out)
	}
	if _, err := os.Lstat(f.env.npmMarker()); err == nil {
		t.Fatal("marker left after settling")
	}
}
