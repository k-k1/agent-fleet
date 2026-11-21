package bootinstall

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	// Nothing of the host's: a fake HOME and a PATH of the stubs plus the base tools, so no real
	// CLI of the machine running the test is ever called.
	t.Setenv("HOME", f.home)
	t.Setenv("PATH", f.pathDir+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
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

const cursorInstaller = "# https://downloads.cursor.com/lab/2026.2.0-abc123/linux/x64/agent-cli-package.tar.gz\n" +
	`mkdir -p "$HOME/.local/bin"; printf '#!/bin/sh\necho 2026.2.0-abc123\n' >"$HOME/.local/bin/cursor-agent"; chmod +x "$HOME/.local/bin/cursor-agent"` + "\n"

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
	f.write(t, filepath.Join(f.npmDir, "failafter"), "npm ERR! ENOSPC\n")
	f.write(t, filepath.Join(f.npmDir, "body.claude"), brokenCLI)
	f.write(t, filepath.Join(f.npmDir, "body2.claude"), "#!/bin/sh\necho 1.0.0\n")
	f.env.selfUpdateNPM()
	log := f.read(filepath.Join(f.npmDir, "npm.log"))
	if !strings.Contains(log, "@anthropic-ai/claude-code@1.0.0") {
		t.Fatalf("no pin reinstall after the failed update: %s", log)
	}
}

func TestSelfUpdateLaterStepsRunAfterEarlierFailure(t *testing.T) {
	// rtk's checksum fails, npm fails: agy and cursor are still updated.
	f := newSU(t, goodRTK, strings.Repeat("0", 64), agyInstaller, cursorInstaller)
	f.write(t, filepath.Join(f.npmDir, "fail"), "npm ERR! network boom\n")
	if rc := SelfUpdate(context.Background(), f.env); rc != ExitOK {
		t.Fatalf("rc=%d", rc)
	}
	if !usable(f.local("agy")) || !usable(f.local("cursor-agent")) {
		t.Fatalf("a failed step stopped the later ones:\n%s", f.out)
	}
}
