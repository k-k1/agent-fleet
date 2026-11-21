package bootinstall

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const cursorObject = "/2026.1.1-abc/linux/x64/agent-cli-package.tar.gz"

// goodCursor answers only when the bundle's index.js sits beside it, as the real one needs.
const goodCursor = "#!/bin/sh\n[ \"$1 $2\" = \"--disable-auto-update --version\" ] || exit 2\n[ -f \"$(dirname \"$(readlink -f \"$0\")\")/index.js\" ] || exit 1\necho 2026.1.1-abc\n"

type tarMember struct {
	name, body, link string
	mode             int64
	typ              byte
}

func cursorTarball(t *testing.T, members []tarMember) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, m := range members {
		typ := m.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		h := &tar.Header{Name: m.name, Mode: m.mode, Typeflag: typ, Linkname: m.link}
		if typ == tar.TypeReg {
			h.Size = int64(len(m.body))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			tw.Write([]byte(m.body))
		}
	}
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

func goodCursorMembers() []tarMember {
	return []tarMember{
		{name: "dist-package/", typ: tar.TypeDir, mode: 0o755},
		{name: "dist-package/cursor-agent", body: goodCursor, mode: 0o755},
		{name: "dist-package/index.js", body: "//js\n", mode: 0o644},
	}
}

type cursorFixture struct {
	env    Env
	home   string
	status string
	out    *bytes.Buffer
	hits   *atomic.Int32
}

func newCursor(t *testing.T, members []tarMember, sum string) *cursorFixture {
	t.Helper()
	tb := cursorTarball(t, members)
	if sum == "" {
		h := sha256.Sum256(tb)
		sum = hex.EncodeToString(h[:])
	}
	hits := &atomic.Int32{}
	mux := http.NewServeMux()
	mux.HandleFunc(cursorObject, func(w http.ResponseWriter, _ *http.Request) { hits.Add(1); w.Write(tb) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	home := t.TempDir()
	status := filepath.Join(t.TempDir(), "install.tsv")
	if err := os.WriteFile(status, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	return &cursorFixture{home: home, status: status, out: out, hits: hits, env: Env{
		Home: home, StatusPath: status, BakedBin: t.TempDir(),
		Pins: map[string]string{"cursor": "2026.1.1-abc", "cursor_sha256": sum},
		Arch: "amd64", CursorURL: srv.URL, Client: srv.Client(),
		FreeMiB: func(string) (int64, bool) { return 100000, true },
		Stdout:  out, Stderr: out, RunTimeout: 10 * time.Second,
	}}
}

func (f *cursorFixture) link() string { return filepath.Join(f.home, ".local", "bin", "cursor-agent") }
func (f *cursorFixture) tree(v string) string {
	return filepath.Join(f.home, ".local", "share", "cursor-agent", "versions", v)
}
func (f *cursorFixture) statusText() string {
	b, _ := os.ReadFile(f.status)
	return string(b)
}

// plant makes a tree for version v whose cursor-agent has body, linked from ~/.local/bin.
func (f *cursorFixture) plant(t *testing.T, v, body string, withJS bool) {
	t.Helper()
	d := f.tree(v)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(d, "cursor-agent"), []byte(body), 0o755)
	if withJS {
		os.WriteFile(filepath.Join(d, "index.js"), []byte("//js\n"), 0o644)
	}
	if err := os.MkdirAll(filepath.Dir(f.link()), 0o755); err != nil {
		t.Fatal(err)
	}
	os.Remove(f.link())
	if err := os.Symlink(filepath.Join(d, "cursor-agent"), f.link()); err != nil {
		t.Fatal(err)
	}
}

func TestCursorInstallsAndLinks(t *testing.T) {
	f := newCursor(t, goodCursorMembers(), "")
	if rc := Cursor(t.Context(), f.env); rc != ExitOK {
		t.Fatalf("rc=%d out=%s", rc, f.out)
	}
	if !f.env.cursorRuns(f.link()) {
		t.Fatal("linked cursor-agent does not run")
	}
	if got, _ := os.Readlink(f.link()); got != filepath.Join(f.tree("2026.1.1-abc"), "cursor-agent") {
		t.Errorf("link -> %s", got)
	}
	if !strings.Contains(f.out.String(), "boot-install cursor 2026.1.1-abc") || f.statusText() != "cursor\tok\t\n" {
		t.Errorf("out=%q status=%q", f.out, f.statusText())
	}
	// A healthy one is left alone.
	f.out.Reset()
	f.hits.Store(0)
	if rc := Cursor(t.Context(), f.env); rc != ExitOK || f.hits.Load() != 0 || !strings.Contains(f.out.String(), "already present") {
		t.Errorf("second run rc=%d hits=%d out=%q", rc, f.hits.Load(), f.out)
	}
}

func TestCursorWrongChecksumInstallsNothing(t *testing.T) {
	f := newCursor(t, goodCursorMembers(), strings.Repeat("0", 64))
	if rc := Cursor(t.Context(), f.env); rc != ExitFailed {
		t.Fatalf("rc=%d", rc)
	}
	if _, err := os.Lstat(f.link()); err == nil {
		t.Error("link created after a checksum mismatch")
	}
	if _, err := os.Stat(f.tree("2026.1.1-abc")); err == nil {
		t.Error("tree created after a checksum mismatch")
	}
	if f.statusText() != "cursor\tfailed\tchecksum\n" {
		t.Errorf("status=%q", f.statusText())
	}
}

func TestCursorTruncatedTreeIsReplaced(t *testing.T) {
	for name, withJS := range map[string]bool{"truncated executable": false, "missing bundle": false} {
		f := newCursor(t, goodCursorMembers(), "")
		body := "#!/bi"
		if name == "missing bundle" {
			body = goodCursor // executable and well-formed, index.js absent
		}
		f.plant(t, "2026.1.1-abc", body, withJS)
		if rc := Cursor(t.Context(), f.env); rc != ExitOK {
			t.Fatalf("%s: rc=%d out=%s", name, rc, f.out)
		}
		if !f.env.cursorRuns(f.link()) {
			t.Errorf("%s: not repaired", name)
		}
		if f.hits.Load() != 1 {
			t.Errorf("%s: downloads=%d", name, f.hits.Load())
		}
		if m, _ := filepath.Glob(f.tree("2026.1.1-abc") + ".af-*"); len(m) != 0 {
			t.Errorf("%s: leftovers %v", name, m)
		}
	}
}

func TestCursorPartialUnpackLeavesNothing(t *testing.T) {
	// A tarball cut off after the executable: the tree must not appear at its final name.
	full := cursorTarball(t, goodCursorMembers())
	cut := full[:len(full)-20]
	f := newCursor(t, goodCursorMembers(), "")
	h := sha256.Sum256(cut)
	f.env.Pins["cursor_sha256"] = hex.EncodeToString(h[:])
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write(cut) }))
	defer srv.Close()
	f.env.CursorURL, f.env.Client = srv.URL, srv.Client()
	if rc := Cursor(t.Context(), f.env); rc != ExitFailed {
		t.Fatalf("rc=%d", rc)
	}
	if _, err := os.Stat(f.tree("2026.1.1-abc")); err == nil {
		t.Error("partial tree left at its final name")
	}
	if m, _ := filepath.Glob(f.tree("2026.1.1-abc") + ".af-*"); len(m) != 0 {
		t.Errorf("stage left behind: %v", m)
	}
	if _, err := os.Lstat(f.link()); err == nil {
		t.Error("link created")
	}
}

func TestCursorBadCandidateKeepsWorkingTree(t *testing.T) {
	// The downloaded bundle does not run: the tree already at the pinned name is put back.
	f := newCursor(t, []tarMember{{name: "p/cursor-agent", body: "#!/bin/sh\nexit 1\n", mode: 0o755}}, "")
	f.plant(t, "2026.1.1-abc", goodCursor, true)
	// Make the existing tree fail the up-front check once (so a download is tried) by a slow
	// probe being unnecessary: break only its link, not the tree.
	os.Remove(f.link())
	if rc := Cursor(t.Context(), f.env); rc != ExitOK {
		// The healthy tree at the pin is reused, so no download and no failure.
		t.Fatalf("rc=%d out=%s", rc, f.out)
	}
	if f.hits.Load() != 0 {
		t.Errorf("downloaded although the pinned tree runs: %d", f.hits.Load())
	}
	if !f.env.cursorRuns(f.link()) {
		t.Error("link not restored")
	}
}

func TestCursorBadDownloadRestoresOldTree(t *testing.T) {
	f := newCursor(t, []tarMember{{name: "p/cursor-agent", body: "#!/bin/sh\nexit 1\n", mode: 0o755}}, "")
	d := f.tree("2026.1.1-abc")
	os.MkdirAll(d, 0o755)
	os.WriteFile(filepath.Join(d, "cursor-agent"), []byte("#!/bi"), 0o755) // fails the check: download is tried
	os.WriteFile(filepath.Join(d, "marker"), []byte("old"), 0o644)
	os.MkdirAll(filepath.Dir(f.link()), 0o755)
	os.Symlink(filepath.Join(d, "cursor-agent"), f.link())
	if rc := Cursor(t.Context(), f.env); rc != ExitFailed {
		t.Fatalf("rc=%d out=%s", rc, f.out)
	}
	if b, err := os.ReadFile(filepath.Join(d, "marker")); err != nil || string(b) != "old" {
		t.Errorf("old tree not put back: %v %q", err, b)
	}
	if m, _ := filepath.Glob(d + ".af-*"); len(m) != 0 {
		t.Errorf("leftovers %v", m)
	}
}

func TestCursorRepinRelinksWithoutDownloadOrSpace(t *testing.T) {
	f := newCursor(t, goodCursorMembers(), "")
	f.plant(t, "2026.1.1-abc", goodCursor, true) // the pinned tree is still there
	f.plant(t, "2026.2.2-def", goodCursor, true) // the self-update moved the link ahead
	alias := filepath.Join(f.home, ".local", "bin", "agent")
	os.Symlink(filepath.Join(f.tree("2026.2.2-def"), "cursor-agent"), alias)
	f.env.Repin = true
	f.env.FreeMiB = func(string) (int64, bool) { return 1, true }
	if rc := Cursor(t.Context(), f.env); rc != ExitOK {
		t.Fatalf("rc=%d out=%s", rc, f.out)
	}
	want := filepath.Join(f.tree("2026.1.1-abc"), "cursor-agent")
	for _, l := range []string{f.link(), alias} {
		if got, _ := os.Readlink(l); got != want {
			t.Errorf("%s -> %s, want %s", l, got, want)
		}
	}
	if f.hits.Load() != 0 {
		t.Error("downloaded on a relink")
	}
	// Without Repin the moved-ahead link stays.
	f.plant(t, "2026.2.2-def", goodCursor, true)
	f.env.Repin = false
	Cursor(t.Context(), f.env)
	if got, _ := os.Readlink(f.link()); !strings.Contains(got, "2026.2.2-def") {
		t.Errorf("link moved without repin: %s", got)
	}
}

func TestCursorNoSpaceKeepsWorkingOldCopy(t *testing.T) {
	f := newCursor(t, goodCursorMembers(), "")
	f.env.FreeMiB = func(string) (int64, bool) { return 100, true }
	if rc := Cursor(t.Context(), f.env); rc != ExitNoSpace {
		t.Fatalf("rc=%d", rc)
	}
	if f.hits.Load() != 0 || f.statusText() != "cursor\tfailed\tno_space\n" {
		t.Errorf("hits=%d status=%q", f.hits.Load(), f.statusText())
	}
	// An old working tree linked there: skipped, not failed.
	os.WriteFile(f.status, nil, 0o600)
	f.plant(t, "2026.0.9-old", goodCursor, true)
	f.env.Repin = true
	if rc := Cursor(t.Context(), f.env); rc != ExitNoSpace || f.statusText() != "cursor\tskipped\tno_space\n" {
		t.Errorf("rc=%d status=%q", rc, f.statusText())
	}
}

func TestCursorRefusesPathsOutsideTheTree(t *testing.T) {
	for _, m := range []tarMember{
		{name: "../../evil", body: "x", mode: 0o644},
		{name: "p/l", typ: tar.TypeSymlink, link: "../../../etc/passwd"},
		{name: "p/l", typ: tar.TypeSymlink, link: "/etc/passwd"},
	} {
		f := newCursor(t, append(goodCursorMembers(), m), "")
		if rc := Cursor(t.Context(), f.env); rc != ExitFailed {
			t.Errorf("%+v: rc=%d", m, rc)
		}
		if _, err := os.Stat(f.tree("2026.1.1-abc")); err == nil {
			t.Errorf("%+v: tree installed", m)
		}
	}
}

func TestCursorWithoutPinDoesNothing(t *testing.T) {
	f := newCursor(t, goodCursorMembers(), "")
	delete(f.env.Pins, "cursor_sha256")
	if rc := Cursor(t.Context(), f.env); rc != ExitOK || f.hits.Load() != 0 || f.statusText() != "" {
		t.Errorf("rc=%d hits=%d status=%q", rc, f.hits.Load(), f.statusText())
	}
}

func TestCursorStripMatchesTar(t *testing.T) {
	// "./dist/f" and "dist/f" are the same member after stripping one component, as in tar;
	// directory modes survive the umask; a hard link to another member is kept.
	f := newCursor(t, append(goodCursorMembers(),
		tarMember{name: "./dist-package/f", body: "one", mode: 0o644},
		tarMember{name: "dist-package/f", body: "two", mode: 0o644},
		tarMember{name: "dist-package/sub/", typ: tar.TypeDir, mode: 0o700},
		tarMember{name: "dist-package/sub/g", body: "g", mode: 0o600},
		tarMember{name: "dist-package/hl", typ: tar.TypeLink, link: "dist-package/sub/g"},
	), "")
	if rc := Cursor(t.Context(), f.env); rc != ExitOK {
		t.Fatalf("rc=%d out=%s", rc, f.out)
	}
	d := f.tree("2026.1.1-abc")
	if b, _ := os.ReadFile(filepath.Join(d, "f")); string(b) != "two" {
		t.Errorf("f=%q, want the later member", b)
	}
	if st, err := os.Stat(filepath.Join(d, "sub")); err != nil || st.Mode().Perm() != 0o700 {
		t.Errorf("sub: %v %v", st, err)
	}
	if b, err := os.ReadFile(filepath.Join(d, "hl")); err != nil || string(b) != "g" {
		t.Errorf("hard link: %q %v", b, err)
	}
}

func TestCursorRefusesChainedSymlinkEscape(t *testing.T) {
	// a -> sub (inside), then a/../../x would resolve outside only through the link chain;
	// and a file written through a symlinked directory must not leave the tree.
	for _, ms := range [][]tarMember{
		{{name: "p/a", typ: tar.TypeSymlink, link: ".."}, {name: "p/a/evil", body: "x", mode: 0o644}},
		{{name: "p/d", typ: tar.TypeSymlink, link: "."}, {name: "p/d/d/../../../evil", body: "x", mode: 0o644}},
		{{name: "p/hl", typ: tar.TypeLink, link: "../../etc/passwd"}},
	} {
		f := newCursor(t, append(goodCursorMembers(), ms...), "")
		if rc := Cursor(t.Context(), f.env); rc != ExitFailed {
			t.Errorf("%+v: rc=%d", ms, rc)
		}
		if _, err := os.Stat(filepath.Join(f.home, ".local", "share", "cursor-agent", "evil")); err == nil {
			t.Errorf("%+v: wrote outside the stage", ms)
		}
		if _, err := os.Stat(f.tree("2026.1.1-abc")); err == nil {
			t.Errorf("%+v: tree installed", ms)
		}
	}
}

func TestCursorRefusesUnsupportedMemberType(t *testing.T) {
	f := newCursor(t, append(goodCursorMembers(), tarMember{name: "p/fifo", typ: tar.TypeFifo, mode: 0o644}), "")
	if rc := Cursor(t.Context(), f.env); rc != ExitFailed {
		t.Errorf("rc=%d", rc)
	}
}

func TestCursorMissingGzipFooterFails(t *testing.T) {
	full := cursorTarball(t, goodCursorMembers())
	cut := full[:len(full)-8] // the CRC and length footer; the tar stream itself is whole
	f := newCursor(t, goodCursorMembers(), "")
	h := sha256.Sum256(cut)
	f.env.Pins["cursor_sha256"] = hex.EncodeToString(h[:])
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write(cut) }))
	defer srv.Close()
	f.env.CursorURL, f.env.Client = srv.URL, srv.Client()
	if rc := Cursor(t.Context(), f.env); rc != ExitFailed {
		t.Fatalf("rc=%d out=%s", rc, f.out)
	}
	if _, err := os.Stat(f.tree("2026.1.1-abc")); err == nil {
		t.Error("tree published from a truncated gzip")
	}
}

func TestCursorRepinReadsLastVersionComponent(t *testing.T) {
	// A HOME that itself contains /versions/<x>/ must not be read as the version.
	f := newCursor(t, goodCursorMembers(), "")
	f.plant(t, "2026.1.1-abc", goodCursor, true)
	f.env.Repin = true
	home := filepath.Join(f.home, "versions", "team", "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(f.home, ".local"), filepath.Join(home, ".local")); err != nil {
		t.Fatal(err)
	}
	f.env.Home = home
	d := filepath.Join(home, ".local", "share", "cursor-agent", "versions", "2026.1.1-abc", "cursor-agent")
	os.Remove(filepath.Join(home, ".local", "bin", "cursor-agent"))
	os.Symlink(d, filepath.Join(home, ".local", "bin", "cursor-agent"))
	if rc := Cursor(t.Context(), f.env); rc != ExitOK || f.hits.Load() != 0 || !strings.Contains(f.out.String(), "already present") {
		t.Errorf("rc=%d hits=%d out=%q", rc, f.hits.Load(), f.out)
	}
}

func TestCursorAliasRenameFailureLeavesMainLinkForRetry(t *testing.T) {
	f := newCursor(t, goodCursorMembers(), "")
	f.plant(t, "2026.1.1-abc", goodCursor, true)
	f.plant(t, "2026.2.2-def", goodCursor, true)
	alias := filepath.Join(f.home, ".local", "bin", "agent")
	os.Symlink(filepath.Join(f.tree("2026.2.2-def"), "cursor-agent"), alias)
	f.env.Repin = true
	orig := relinkRename
	defer func() { relinkRename = orig }()
	relinkRename = func(from, to string) error {
		if to == alias {
			return errors.New("injected")
		}
		return os.Rename(from, to)
	}
	if rc := Cursor(t.Context(), f.env); rc != ExitFailed {
		t.Fatalf("rc=%d", rc)
	}
	if got, _ := os.Readlink(f.link()); !strings.Contains(got, "2026.2.2-def") {
		t.Fatalf("main link moved before the alias: %s", got)
	}
	relinkRename = orig
	if rc := Cursor(t.Context(), f.env); rc != ExitOK {
		t.Fatalf("retry rc=%d", rc)
	}
	for _, l := range []string{f.link(), alias} {
		if got, _ := os.Readlink(l); !strings.Contains(got, "2026.1.1-abc") {
			t.Errorf("%s -> %s", l, got)
		}
	}
	if m, _ := filepath.Glob(filepath.Join(f.home, ".local", "bin", "*.af-new.*")); len(m) != 0 {
		t.Errorf("leftovers %v", m)
	}
}

func TestCursorPinnedTreeIsProbedOnce(t *testing.T) {
	// A pinned tree that answers the first probe is reused whatever space says afterwards.
	f := newCursor(t, goodCursorMembers(), "")
	f.plant(t, "2026.0.9-old", goodCursor, true)
	f.plant(t, "2026.1.1-abc", goodCursor, true)
	os.Remove(f.link())
	os.Symlink(filepath.Join(f.tree("2026.0.9-old"), "cursor-agent"), f.link())
	f.env.Repin = true
	f.env.FreeMiB = func(string) (int64, bool) { return 1, true }
	if rc := Cursor(t.Context(), f.env); rc != ExitOK || f.hits.Load() != 0 {
		t.Errorf("rc=%d hits=%d", rc, f.hits.Load())
	}
}

func TestCursorCandidateThatDoesNotRunRestoresRunningTree(t *testing.T) {
	// fetchCursorTree on a healthy tree (as when its probe flaked): a candidate that cannot run
	// must leave the original tree and its link exactly as they were.
	f := newCursor(t, []tarMember{{name: "p/cursor-agent", body: "#!/bin/sh\nexit 1\n", mode: 0o755}}, "")
	f.plant(t, "2026.1.1-abc", goodCursor, true)
	dir := f.tree("2026.1.1-abc")
	if err := f.env.fetchCursorTree(t.Context(), "2026.1.1-abc", f.env.Pins["cursor_sha256"], dir); err == nil {
		t.Fatal("candidate that exits 1 was accepted")
	}
	if f.hits.Load() != 1 {
		t.Fatalf("candidate was not fetched: %d", f.hits.Load())
	}
	if !f.env.cursorRuns(f.link()) {
		t.Error("original tree/link no longer runs")
	}
	if m, _ := filepath.Glob(dir + ".af-*"); len(m) != 0 {
		t.Errorf("leftovers %v", m)
	}
}

func TestCursorDownloadFailureKeepsTreeAndRecordsNetwork(t *testing.T) {
	f := newCursor(t, goodCursorMembers(), "")
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	f.env.CursorURL, f.env.Client = srv.URL, srv.Client()
	f.plant(t, "2026.1.1-abc", "#!/bi", false) // broken: a download is needed
	if rc := Cursor(t.Context(), f.env); rc != ExitFailed {
		t.Fatalf("rc=%d", rc)
	}
	if f.statusText() != "cursor\tfailed\tnetwork\n" {
		t.Errorf("status=%q", f.statusText())
	}
	if b, _ := os.ReadFile(filepath.Join(f.tree("2026.1.1-abc"), "cursor-agent")); string(b) != "#!/bi" {
		t.Error("existing tree touched by a failed download")
	}
}
