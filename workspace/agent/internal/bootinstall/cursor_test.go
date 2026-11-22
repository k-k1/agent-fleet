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
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
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
	// "./dist-package/f" and "dist-package/f" are different members: stripping one component, as
	// tar does, drops the "." from the first and "dist-package" from the second, so they land at
	// different names. Directory modes survive the umask; a hard link to another member is kept.
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
		t.Errorf("f=%q, want the member without ./", b)
	}
	// "./dist-package/f" loses only the "." component, so it lands at dist-package/f.
	if b, _ := os.ReadFile(filepath.Join(d, "dist-package", "f")); string(b) != "one" {
		t.Errorf("dist-package/f=%q: ./ must count as the stripped component", b)
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

func tarSymlinkChain() []tarMember {
	// d/x -> .. is inside (the tree root); d/y -> x/.. is lexically d, but through the link it
	// is the parent of the tree. Neither is used by a later member.
	return []tarMember{
		{name: "p/d/", typ: tar.TypeDir, mode: 0o755},
		{name: "p/d/x", typ: tar.TypeSymlink, link: ".."},
		{name: "p/d/y", typ: tar.TypeSymlink, link: "x/.."},
	}
}

func TestCursorRefusesUnusedLinkChainResolvingOutside(t *testing.T) {
	f := newCursor(t, append(goodCursorMembers(), tarSymlinkChain()...), "")
	if rc := Cursor(t.Context(), f.env); rc != ExitFailed {
		t.Fatalf("rc=%d out=%s", rc, f.out)
	}
	if _, err := os.Stat(f.tree("2026.1.1-abc")); err == nil {
		t.Error("tree with an escaping link chain published")
	}
}

func TestCursorAcceptsInternalLinks(t *testing.T) {
	f := newCursor(t, append(goodCursorMembers(),
		tarMember{name: "p/sub/", typ: tar.TypeDir, mode: 0o755},
		tarMember{name: "p/sub/real", body: "r", mode: 0o644},
		tarMember{name: "p/ln", typ: tar.TypeSymlink, link: "sub/real"},
		tarMember{name: "p/sub/up", typ: tar.TypeSymlink, link: "../ln"},
	), "")
	if rc := Cursor(t.Context(), f.env); rc != ExitOK {
		t.Fatalf("rc=%d out=%s", rc, f.out)
	}
	if b, _ := os.ReadFile(filepath.Join(f.tree("2026.1.1-abc"), "sub", "up")); string(b) != "r" {
		t.Errorf("internal chain: %q", b)
	}
}

func TestCursorLeftoverStageIsNotReused(t *testing.T) {
	// A read-only leftover under a predictable name cannot be deleted by a normal user; the
	// published tree must still be built in a fresh stage.
	f := newCursor(t, goodCursorMembers(), "")
	share := filepath.Dir(f.tree("x"))
	os.MkdirAll(share, 0o755)
	var stale []string
	for _, n := range []string{"2026.1.1-abc.af-new.1", "2026.1.1-abc.af-new." + strconv.Itoa(os.Getpid())} {
		p := filepath.Join(share, n)
		os.MkdirAll(p, 0o755)
		os.WriteFile(filepath.Join(p, "stale"), []byte("old"), 0o644)
		os.Chmod(p, 0o555)
		stale = append(stale, p)
	}
	defer func() {
		for _, p := range stale {
			os.Chmod(p, 0o755)
		}
	}()
	if rc := Cursor(t.Context(), f.env); rc != ExitOK {
		t.Fatalf("rc=%d out=%s", rc, f.out)
	}
	if _, err := os.Stat(filepath.Join(f.tree("2026.1.1-abc"), "stale")); err == nil {
		t.Error("a stale file from a leftover stage reached the published tree")
	}
}

func TestCursorUnpackTempNameNeverReplacesArchiveMember(t *testing.T) {
	// Members named like the unpack's temporary files must all survive.
	f := newCursor(t, append(goodCursorMembers(),
		tarMember{name: "p/.af-w0", body: "A", mode: 0o644},
		tarMember{name: "p/.af-w1", body: "B", mode: 0o644},
		tarMember{name: "p/file.af-w", body: "D", mode: 0o644},
		tarMember{name: "p/file.af-w0", body: "E", mode: 0o644},
		tarMember{name: "p/file", body: "C", mode: 0o644},
	), "")
	if rc := Cursor(t.Context(), f.env); rc != ExitOK {
		t.Fatalf("rc=%d out=%s", rc, f.out)
	}
	d := f.tree("2026.1.1-abc")
	for n, want := range map[string]string{".af-w0": "A", ".af-w1": "B", "file": "C", "file.af-w": "D", "file.af-w0": "E"} {
		if b, _ := os.ReadFile(filepath.Join(d, n)); string(b) != want {
			t.Errorf("%s=%q, want %q", n, b, want)
		}
	}
}

func TestCursorLinkCheckFollowsRealResolution(t *testing.T) {
	// A link to the tree root is fine; so are good links on the way. Only the real escape
	// (d/y: through d/up -> .. and one more ..) is refused.
	ok := newCursor(t, append(goodCursorMembers(),
		tarMember{name: "p/d/", typ: tar.TypeDir, mode: 0o755},
		tarMember{name: "p/d/root", typ: tar.TypeSymlink, link: ".."},
		tarMember{name: "p/d/viaroot", typ: tar.TypeSymlink, link: "root/index.js"},
	), "")
	if rc := Cursor(t.Context(), ok.env); rc != ExitOK {
		t.Fatalf("legitimate link to the root refused: rc=%d out=%s", rc, ok.out)
	}
	bad := newCursor(t, append(goodCursorMembers(),
		tarMember{name: "p/d/", typ: tar.TypeDir, mode: 0o755},
		tarMember{name: "p/d/root", typ: tar.TypeSymlink, link: ".."},
		tarMember{name: "p/d/good", typ: tar.TypeSymlink, link: "root/index.js"},
		tarMember{name: "p/d/esc", typ: tar.TypeSymlink, link: "root/../outside"},
	), "")
	if rc := Cursor(t.Context(), bad.env); rc != ExitFailed {
		t.Fatalf("escape accepted: rc=%d", rc)
	}
}

func TestCursorRefusesDanglingLinkThroughOutsideDirAndInsideRestrictedDir(t *testing.T) {
	for name, ms := range map[string][]tarMember{
		"dangling via an outside hop": {
			{name: "p/d/", typ: tar.TypeDir, mode: 0o755},
			{name: "p/d/up", typ: tar.TypeSymlink, link: ".."},
			{name: "p/d/y", typ: tar.TypeSymlink, link: "up/../outside-new"},
		},
		"hidden in an execute-only dir": {
			{name: "p/d/", typ: tar.TypeDir, mode: 0o111},
			{name: "p/d/up", typ: tar.TypeSymlink, link: ".."},
			{name: "p/d/y", typ: tar.TypeSymlink, link: "up/../outside-new"},
		},
	} {
		f := newCursor(t, append(goodCursorMembers(), ms...), "")
		if rc := Cursor(t.Context(), f.env); rc != ExitFailed {
			t.Errorf("%s: rc=%d", name, rc)
		}
		if _, err := os.Stat(f.tree("2026.1.1-abc")); err == nil {
			t.Errorf("%s: tree published", name)
		}
	}
}

func TestCursorLeavesAnotherToolsAgentAlias(t *testing.T) {
	for name, mk := range map[string]func(home string, f *cursorFixture) string{
		"lookalike path": func(home string, f *cursorFixture) string {
			return filepath.Join(home, "work", "cursor-agent", "versions", "9.9", "agent")
		},
		"another file in a cursor version dir": func(home string, f *cursorFixture) string {
			return filepath.Join(f.tree("2026.0.9-old"), "agent")
		},
	} {
		f := newCursor(t, goodCursorMembers(), "")
		f.plant(t, "2026.1.1-abc", goodCursor, true)
		other := mk(f.home, f)
		os.MkdirAll(filepath.Dir(other), 0o755)
		os.WriteFile(other, []byte("#!/bin/sh\n"), 0o755)
		alias := filepath.Join(f.home, ".local", "bin", "agent")
		os.Symlink(other, alias)
		f.env.Repin = true
		if rc := Cursor(t.Context(), f.env); rc != ExitOK || f.hits.Load() != 0 || !strings.Contains(f.out.String(), "already present") {
			t.Fatalf("%s: rc=%d hits=%d out=%s", name, rc, f.hits.Load(), f.out)
		}
		f.plant(t, "2026.2.2-def", goodCursor, true) // a real relink happens now
		if rc := Cursor(t.Context(), f.env); rc != ExitOK {
			t.Fatalf("%s: relink rc=%d", name, rc)
		}
		if got, _ := os.Readlink(alias); got != other {
			t.Errorf("%s: another tool's agent alias was replaced: %s", name, got)
		}
	}
}

func TestCursorRetriesStaleCursorAlias(t *testing.T) {
	f := newCursor(t, goodCursorMembers(), "")
	f.plant(t, "2026.1.1-abc", goodCursor, true)
	f.plant(t, "2026.2.2-def", goodCursor, true)
	os.Remove(f.link())
	os.Symlink(filepath.Join(f.tree("2026.1.1-abc"), "cursor-agent"), f.link())
	alias := filepath.Join(f.home, ".local", "bin", "agent")
	os.Symlink("../share/cursor-agent/versions/2026.2.2-def/cursor-agent", alias) // relative, cursor's own
	f.env.Repin = true
	if rc := Cursor(t.Context(), f.env); rc != ExitOK {
		t.Fatalf("rc=%d", rc)
	}
	if got, _ := os.Readlink(alias); !strings.Contains(got, "2026.1.1-abc") {
		t.Errorf("stale cursor alias not retried: %s", got)
	}
}

func TestCursorAliasLookupFailureStopsBeforeAnyLinkMoves(t *testing.T) {
	f := newCursor(t, goodCursorMembers(), "")
	f.plant(t, "2026.1.1-abc", goodCursor, true)
	f.plant(t, "2026.2.2-def", goodCursor, true)
	bin := filepath.Join(f.home, ".local", "bin")
	os.Symlink(filepath.Join(f.tree("2026.2.2-def"), "cursor-agent"), filepath.Join(bin, "agent"))
	f.env.Repin = true
	// A bin directory that cannot be searched makes the alias Lstat fail with something
	// other than "not found"; the main link must stay where it was.
	orig := lstatAlias
	defer func() { lstatAlias = orig }()
	lstatAlias = func(string) (os.FileInfo, error) { return nil, errors.New("injected I/O error") }
	if rc := Cursor(t.Context(), f.env); rc != ExitFailed {
		t.Fatalf("rc=%d", rc)
	}
	if got, _ := os.Readlink(f.link()); !strings.Contains(got, "2026.2.2-def") {
		t.Errorf("main link moved although the alias could not be checked: %s", got)
	}
}

func TestCursorHardLinkKeepsEarlierContent(t *testing.T) {
	f := newCursor(t, append(goodCursorMembers(),
		tarMember{name: "p/f", body: "first", mode: 0o644},
		tarMember{name: "p/h", typ: tar.TypeLink, link: "p/f"},
		tarMember{name: "p/f", body: "last", mode: 0o644},
	), "")
	if rc := Cursor(t.Context(), f.env); rc != ExitOK {
		t.Fatalf("rc=%d out=%s", rc, f.out)
	}
	d := f.tree("2026.1.1-abc")
	a, _ := os.ReadFile(filepath.Join(d, "h"))
	b, _ := os.ReadFile(filepath.Join(d, "f"))
	if string(a) != "first" || string(b) != "last" {
		t.Errorf("h=%q f=%q", a, b)
	}
}

func TestCursorModesFollowUmaskAndHeaderOrder(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	// The child directory is listed before its parent, the parent has no traverse permission
	// in the end, and the same directory appears twice (the last mode wins).
	f := newCursor(t, []tarMember{
		{name: "p/cursor-agent", body: goodCursor, mode: 0o755},
		{name: "p/index.js", body: "//js\n", mode: 0o644},
		{name: "p/a/b/", typ: tar.TypeDir, mode: 0o755},
		{name: "p/a/b/f", body: "f", mode: 0o755},
		{name: "p/a/", typ: tar.TypeDir, mode: 0o000},
		{name: "p/d/", typ: tar.TypeDir, mode: 0o700},
		{name: "p/d/", typ: tar.TypeDir, mode: 0o500},
	}, "")
	if rc := Cursor(t.Context(), f.env); rc != ExitOK {
		t.Fatalf("rc=%d out=%s", rc, f.out)
	}
	d := f.tree("2026.1.1-abc")
	defer os.Chmod(filepath.Join(d, "a"), 0o700)
	defer os.Chmod(filepath.Join(d, "d"), 0o700)
	for p, want := range map[string]os.FileMode{".": 0o700, "d": 0o500, "cursor-agent": 0o700} {
		if st, err := os.Stat(filepath.Join(d, p)); err != nil || st.Mode().Perm() != want {
			t.Errorf("%s: %v %v, want %o", p, st, err, want)
		}
	}
	if st, err := os.Lstat(filepath.Join(d, "a")); err != nil || st.Mode().Perm() != 0 {
		t.Errorf("a: %v %v", st, err)
	}
}

func TestCursorBadCandidateWithReadOnlySubtreeRestoresOldTree(t *testing.T) {
	f := newCursor(t, []tarMember{
		{name: "p/cursor-agent", body: "#!/bin/sh\nexit 1\n", mode: 0o755},
		{name: "p/ro/", typ: tar.TypeDir, mode: 0o555},
		{name: "p/ro/f", body: "x", mode: 0o644},
	}, "")
	d := f.tree("2026.1.1-abc")
	os.MkdirAll(d, 0o755)
	os.WriteFile(filepath.Join(d, "cursor-agent"), []byte("#!/bi"), 0o755)
	os.WriteFile(filepath.Join(d, "marker"), []byte("old"), 0o644)
	os.MkdirAll(filepath.Dir(f.link()), 0o755)
	os.Symlink(filepath.Join(d, "cursor-agent"), f.link())
	if rc := Cursor(t.Context(), f.env); rc != ExitFailed {
		t.Fatalf("rc=%d out=%s", rc, f.out)
	}
	if b, err := os.ReadFile(filepath.Join(d, "marker")); err != nil || string(b) != "old" {
		t.Errorf("old tree not restored: %v %q", err, b)
	}
	if _, err := os.Stat(filepath.Join(d, "ro")); err == nil {
		t.Error("candidate left at the final path")
	}
	if m, _ := filepath.Glob(d + ".af-*"); len(m) != 0 {
		t.Errorf("leftovers %v", m)
	}
}

func TestCursorRepinReadsVersionAcrossRepeatedVersionsDirs(t *testing.T) {
	re := cursorVersionInLink
	got := re.FindStringSubmatch("/h/.local/share/cursor-agent/versions/team/versions/2026.1.1-abc/cursor-agent")
	if got == nil || got[1] != "2026.1.1-abc" {
		t.Errorf("got %v", got)
	}
}

func TestCursorPinnedTreeIsProbedOncePerRun(t *testing.T) {
	f := newCursor(t, goodCursorMembers(), "")
	counting := "#!/bin/sh\nd=\"$(dirname \"$(readlink -f \"$0\")\")\"\necho x >>\"$d/calls\"\n[ \"$(wc -l <\"$d/calls\")\" -le 1 ] || exit 1\n[ -f \"$d/index.js\" ] || exit 1\necho v\n"
	f.plant(t, "2026.0.9-old", goodCursor, true)
	f.plant(t, "2026.1.1-abc", counting, true)
	os.Remove(f.link())
	os.Symlink(filepath.Join(f.tree("2026.0.9-old"), "cursor-agent"), f.link())
	f.env.Repin = true
	f.env.FreeMiB = func(string) (int64, bool) { return 1, true }
	if rc := Cursor(t.Context(), f.env); rc != ExitOK || f.hits.Load() != 0 {
		t.Fatalf("rc=%d hits=%d out=%s", rc, f.hits.Load(), f.out)
	}
	b, _ := os.ReadFile(filepath.Join(f.tree("2026.1.1-abc"), "calls"))
	if n := strings.Count(string(b), "x"); n != 1 {
		t.Errorf("pinned tree probed %d times, want once", n)
	}
}

func TestCursorUnwritableTempDirFailsBeforeAnyRequest(t *testing.T) {
	f := newCursor(t, goodCursorMembers(), "")
	f.plant(t, "2026.1.1-abc", "#!/bi", false)
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	if rc := Cursor(t.Context(), f.env); rc != ExitFailed {
		t.Fatalf("rc=%d", rc)
	}
	if f.hits.Load() != 0 || f.statusText() != "cursor\tfailed\tother\n" {
		t.Errorf("hits=%d status=%q", f.hits.Load(), f.statusText())
	}
	if b, _ := os.ReadFile(filepath.Join(f.tree("2026.1.1-abc"), "cursor-agent")); string(b) != "#!/bi" {
		t.Error("existing tree touched")
	}
}

func TestCursorMainLinkFailureRestoresAliasAndAliasDriftIsRetried(t *testing.T) {
	f := newCursor(t, goodCursorMembers(), "")
	f.plant(t, "2026.1.1-abc", goodCursor, true)
	f.plant(t, "2026.2.2-def", goodCursor, true)
	alias := filepath.Join(f.home, ".local", "bin", "agent")
	oldT := filepath.Join(f.tree("2026.2.2-def"), "cursor-agent")
	os.Symlink(oldT, alias)
	f.env.Repin = true
	orig := relinkRename
	defer func() { relinkRename = orig }()
	relinkRename = func(from, to string) error {
		if to == f.link() {
			return errors.New("injected")
		}
		return os.Rename(from, to)
	}
	if rc := Cursor(t.Context(), f.env); rc != ExitFailed {
		t.Fatalf("rc=%d", rc)
	}
	if got, _ := os.Readlink(alias); got != oldT {
		t.Errorf("alias not restored after the main link failed: %s", got)
	}
	// Main link already on the pin, alias still on another version: the next start fixes it.
	relinkRename = orig
	os.Remove(f.link())
	os.Symlink(filepath.Join(f.tree("2026.1.1-abc"), "cursor-agent"), f.link())
	if rc := Cursor(t.Context(), f.env); rc != ExitOK {
		t.Fatalf("rc=%d", rc)
	}
	if got, _ := os.Readlink(alias); !strings.Contains(got, "2026.1.1-abc") {
		t.Errorf("alias drift not retried: %s", got)
	}
}

func TestCursorAliasReadlinkFailureStopsBeforeAnyLinkMoves(t *testing.T) {
	f := newCursor(t, goodCursorMembers(), "")
	f.plant(t, "2026.1.1-abc", goodCursor, true)
	f.plant(t, "2026.2.2-def", goodCursor, true)
	bin := filepath.Join(f.home, ".local", "bin")
	oldT := filepath.Join(f.tree("2026.2.2-def"), "cursor-agent")
	os.Symlink(oldT, filepath.Join(bin, "agent"))
	f.env.Repin = true
	orig := readlinkAlias
	defer func() { readlinkAlias = orig }()
	readlinkAlias = func(string) (string, error) { return "", errors.New("injected I/O error") }
	if rc := Cursor(t.Context(), f.env); rc != ExitFailed {
		t.Fatalf("rc=%d", rc)
	}
	if got, _ := os.Readlink(f.link()); !strings.Contains(got, "2026.2.2-def") {
		t.Errorf("main link moved: %s", got)
	}
	if got, _ := os.Readlink(filepath.Join(bin, "agent")); got != oldT {
		t.Errorf("alias moved: %s", got)
	}
	if !strings.Contains(f.statusText(), "cursor\tfailed\t") {
		t.Errorf("status=%q", f.statusText())
	}
}

func TestCursorDanglingLinkNeedsAnExistingParentDirectory(t *testing.T) {
	// A link whose whole path is not found passes only when the directory that would hold its
	// final element resolves inside the tree. The raw path is judged: "gone/../later" has a
	// missing middle component even though it cleans to "later".
	dirs := []tarMember{
		{name: "p/nested/", typ: tar.TypeDir, mode: 0o755},
		{name: "p/nested/inner/", typ: tar.TypeDir, mode: 0o755},
		{name: "p/nested/keep/", typ: tar.TypeDir, mode: 0o755},
		{name: "p/jump", typ: tar.TypeSymlink, link: "nested/inner"},
	}
	for name, c := range map[string]struct {
		link string
		ok   bool
	}{
		"missing middle":          {"gone/../later", false},
		"missing parent":          {"gone/x", false},
		"ends in dot-dot":         {"nested/gone/..", false},
		"final element missing":   {"later", true},
		"trailing slash":          {"later/", true},
		"parent through a link":   {"jump/../keep/later", true},
		"missing under a subdir":  {"nested/later", true},
		"missing past a link dir": {"jump/later", true},
	} {
		ms := append(append(goodCursorMembers(), dirs...), tarMember{name: "p/l", typ: tar.TypeSymlink, link: c.link})
		f := newCursor(t, ms, "")
		rc := Cursor(t.Context(), f.env)
		if (rc == ExitOK) != c.ok {
			t.Errorf("%s (%s): rc=%d out=%s", name, c.link, rc, f.out)
		}
	}
}

func TestCursorAliasInSymlinkedBinDirIsNotOurs(t *testing.T) {
	f := newCursor(t, goodCursorMembers(), "")
	f.plant(t, "2026.1.1-abc", goodCursor, true)
	// ~/.local/bin is a link to another tree whose own cursor install holds a relative alias.
	other := filepath.Join(f.home, "other")
	otherVer := filepath.Join(other, "share", "cursor-agent", "versions", "9.9")
	os.MkdirAll(otherVer, 0o755)
	os.WriteFile(filepath.Join(otherVer, "cursor-agent"), []byte("#!/bin/sh\n"), 0o755)
	os.MkdirAll(filepath.Join(other, "bin"), 0o755)
	os.Symlink("../share/cursor-agent/versions/9.9/cursor-agent", filepath.Join(other, "bin", "agent"))
	bin := filepath.Join(f.home, ".local", "bin")
	os.Rename(bin, filepath.Join(f.home, "oldbin"))
	os.Symlink(filepath.Join(other, "bin"), bin)
	os.Symlink(filepath.Join(f.tree("2026.1.1-abc"), "cursor-agent"), f.link())
	if v, ok, err := f.env.aliasVersion(filepath.Join(bin, "agent"), "../share/cursor-agent/versions/9.9/cursor-agent"); ok || err != nil {
		t.Errorf("another tree's alias read as ours: %q", v)
	}
}

func TestCursorAliasReadlinkOneOffFailureAtDecisionIsFailed(t *testing.T) {
	f := newCursor(t, goodCursorMembers(), "")
	f.plant(t, "2026.1.1-abc", goodCursor, true)
	f.plant(t, "2026.2.2-def", goodCursor, true)
	os.Remove(f.link())
	os.Symlink(filepath.Join(f.tree("2026.1.1-abc"), "cursor-agent"), f.link()) // main link on the pin
	bin := filepath.Join(f.home, ".local", "bin")
	oldT := filepath.Join(f.tree("2026.2.2-def"), "cursor-agent")
	os.Symlink(oldT, filepath.Join(bin, "agent")) // alias on another version
	f.env.Repin = true
	orig := readlinkAlias
	defer func() { readlinkAlias = orig }()
	calls := 0
	readlinkAlias = func(p string) (string, error) {
		if calls++; calls == 1 {
			return "", errors.New("injected I/O error")
		}
		return os.Readlink(p)
	}
	if rc := Cursor(t.Context(), f.env); rc != ExitFailed || !strings.Contains(f.statusText(), "cursor\tfailed\t") {
		t.Fatalf("rc=%d status=%q", rc, f.statusText())
	}
	if rc := Cursor(t.Context(), f.env); rc != ExitOK {
		t.Fatalf("retry rc=%d", rc)
	}
	if got, _ := os.Readlink(filepath.Join(bin, "agent")); !strings.Contains(got, "2026.1.1-abc") {
		t.Errorf("alias not repaired on the retry: %s", got)
	}
}

func TestCursorAliasThroughLinkedVersionsRootAndMissingOldVersionIsOurs(t *testing.T) {
	f := newCursor(t, goodCursorMembers(), "")
	f.plant(t, "2026.1.1-abc", goodCursor, true)
	// versions is a link to another volume and the old version's directory is already gone.
	versions := filepath.Dir(f.tree("x"))
	moved := filepath.Join(f.home, "vol", "versions")
	os.MkdirAll(filepath.Dir(moved), 0o755)
	if err := os.Rename(versions, moved); err != nil {
		t.Fatal(err)
	}
	os.Symlink(moved, versions)
	bin := filepath.Join(f.home, ".local", "bin")
	alias := filepath.Join(bin, "agent")
	os.Symlink("../share/cursor-agent/versions/2026.0.0-old/cursor-agent", alias)
	f.env.Repin = true
	if rc := Cursor(t.Context(), f.env); rc != ExitOK {
		t.Fatalf("rc=%d out=%s", rc, f.out)
	}
	if got, _ := os.Readlink(alias); !strings.Contains(got, "2026.1.1-abc") {
		t.Errorf("our alias on a vanished version was not repaired: %s", got)
	}
}

func TestCursorAliasDotDotAfterLinkIsResolvedInOSOrder(t *testing.T) {
	f := newCursor(t, goodCursorMembers(), "")
	f.plant(t, "2026.1.1-abc", goodCursor, true)
	other, third := filepath.Join(f.home, "other"), filepath.Join(f.home, "third")
	os.MkdirAll(filepath.Join(third, "bin"), 0o755)
	os.MkdirAll(filepath.Join(third, "shortcut", "9.9"), 0o755)
	os.MkdirAll(other, 0o755)
	os.Symlink(filepath.Dir(f.tree("x")), filepath.Join(other, "shortcut")) // would be ours
	os.Symlink(filepath.Join(third, "bin"), filepath.Join(other, "hop"))
	// hop/.. is "third", not "other": the target is third/shortcut/9.9/cursor-agent.
	target := other + "/hop/../shortcut/9.9/cursor-agent"
	if _, ok, err := f.env.aliasVersion(filepath.Join(f.home, ".local", "bin", "agent"), target); ok || err != nil {
		t.Errorf("another tree's alias read as ours (ok=%v err=%v)", ok, err)
	}
}

func TestCursorRepinLstatOneOffFailureWithPinnedMainLinkIsFailed(t *testing.T) {
	f := newCursor(t, goodCursorMembers(), "")
	f.plant(t, "2026.1.1-abc", goodCursor, true)
	f.plant(t, "2026.2.2-def", goodCursor, true)
	os.Remove(f.link())
	os.Symlink(filepath.Join(f.tree("2026.1.1-abc"), "cursor-agent"), f.link())
	bin := filepath.Join(f.home, ".local", "bin")
	oldT := filepath.Join(f.tree("2026.2.2-def"), "cursor-agent")
	os.Symlink(oldT, filepath.Join(bin, "agent"))
	f.env.Repin = true
	orig := lstatAlias
	defer func() { lstatAlias = orig }()
	calls := 0
	lstatAlias = func(p string) (os.FileInfo, error) {
		if calls++; calls == 1 {
			return nil, errors.New("injected I/O error")
		}
		return os.Lstat(p)
	}
	if rc := Cursor(t.Context(), f.env); rc != ExitFailed || !strings.Contains(f.statusText(), "cursor\tfailed\t") {
		t.Fatalf("rc=%d status=%q", rc, f.statusText())
	}
	if got, _ := os.Readlink(filepath.Join(bin, "agent")); got != oldT {
		t.Errorf("alias moved: %s", got)
	}
	if rc := Cursor(t.Context(), f.env); rc != ExitOK {
		t.Fatalf("retry rc=%d", rc)
	}
	if got, _ := os.Readlink(filepath.Join(bin, "agent")); !strings.Contains(got, "2026.1.1-abc") {
		t.Errorf("alias not repaired on the retry: %s", got)
	}
}

func TestCursorUnpackRetriesWhenTheFirstTempNameIsTaken(t *testing.T) {
	// The first member is extracted under the name ".af-wtaken" (its own final name); the next
	// file's first candidate is that same name and must be retried, not overwrite the member.
	names := []string{".af-wtaken", ".af-wtaken", ".af-wfree"}
	orig := unpackTmpName
	defer func() { unpackTmpName = orig }()
	unpackTmpName = func() (string, error) {
		n := names[0]
		if len(names) > 1 {
			names = names[1:]
		}
		return n, nil
	}
	f := newCursor(t, append([]tarMember{
		{name: "dist-package/.af-wtaken", body: "keep", mode: 0o644},
	}, goodCursorMembers()...), "")
	if rc := Cursor(t.Context(), f.env); rc != ExitOK {
		t.Fatalf("rc=%d out=%s", rc, f.out)
	}
	d := f.tree("2026.1.1-abc")
	if b, _ := os.ReadFile(filepath.Join(d, ".af-wtaken")); string(b) != "keep" {
		t.Errorf("the archive's own member was replaced: %q", b)
	}
	if _, err := os.Stat(filepath.Join(d, "index.js")); err != nil {
		t.Errorf("index.js: %v", err)
	}
}
