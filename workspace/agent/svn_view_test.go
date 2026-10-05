package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

func TestParseSvnLogXML(t *testing.T) {
	const x = `<?xml version="1.0" encoding="UTF-8"?>
<log>
<logentry revision="3">
<author>alice</author>
<date>2026-10-01T09:00:00.000000Z</date>
<paths>
<path action="M" prop-mods="false" text-mods="true" kind="file">/trunk/a.txt</path>
<path action="A" kind="file" copyfrom-path="/trunk/a.txt" copyfrom-rev="2">/branches/b/a.txt</path>
<path action="D" kind="file">/trunk/日本語 &amp; x.txt</path>
</paths>
<msg>fix &lt;thing&gt;

details here
</msg>
</logentry>
<logentry revision="2">
<date>2026-09-30T09:00:00.000000Z</date>
<msg></msg>
</logentry>
</log>`
	got, err := parseSvnLogXML([]byte(x))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Rev != 3 || got[1].Rev != 2 {
		t.Fatalf("entries = %+v", got)
	}
	if got[0].Author != "alice" || got[0].Message != "fix <thing>\n\ndetails here" {
		t.Errorf("entry 0 = %+v", got[0])
	}
	want := []svnLogPath{
		{Action: "M", Path: "/trunk/a.txt", Kind: "file"},
		{Action: "A", Path: "/branches/b/a.txt", Kind: "file", CopyFromPath: "/trunk/a.txt", CopyFromRev: "2"},
		{Action: "D", Path: "/trunk/日本語 & x.txt", Kind: "file"},
	}
	if !reflect.DeepEqual(got[0].Paths, want) {
		t.Errorf("paths = %+v, want %+v", got[0].Paths, want)
	}
	if got[1].Author != "" || got[1].Message != "" || got[1].Paths == nil {
		t.Errorf("anonymous entry = %+v (Paths must be a non-nil slice for the JSON)", got[1])
	}
	if _, err := parseSvnLogXML([]byte("<log><logentry")); err == nil {
		t.Error("truncated XML parsed without error")
	}
}

func TestParseSvnStatusXML(t *testing.T) {
	const x = `<?xml version="1.0" encoding="UTF-8"?>
<status>
<target path=".">
<entry path="a.txt"><wc-status props="none" item="modified" revision="2"></wc-status></entry>
<entry path="sub/new file.txt"><wc-status props="none" item="added" revision="-1"></wc-status></entry>
<entry path="gone.txt"><wc-status props="none" item="missing" revision="2"></wc-status></entry>
<entry path="u.txt"><wc-status props="none" item="unversioned"></wc-status></entry>
<entry path="conf.txt"><wc-status props="none" item="conflicted" revision="2"></wc-status></entry>
<entry path="propsonly"><wc-status props="modified" item="normal" revision="2"></wc-status></entry>
<entry path="clean.txt"><wc-status props="none" item="normal" revision="2"></wc-status></entry>
<entry path="ext"><wc-status props="none" item="external"></wc-status></entry>
<entry path="."><wc-status props="none" item="normal" revision="2"></wc-status></entry>
</target>
<changelist name="cl">
<entry path="cl.txt"><wc-status props="none" item="deleted" revision="2"></wc-status></entry>
</changelist>
</status>`
	got, err := parseSvnStatusXML([]byte(x))
	if err != nil {
		t.Fatal(err)
	}
	var sum []string
	for _, c := range got {
		sum = append(sum, c.Status+" "+c.Path)
	}
	want := []string{"M a.txt", "A sub/new file.txt", "! gone.txt", "? u.txt", "C conf.txt", "M propsonly", "D cl.txt"}
	if !reflect.DeepEqual(sum, want) {
		t.Errorf("entries = %q, want %q", sum, want)
	}
	for _, c := range got {
		if c.Path == "u.txt" && !c.Untracked {
			t.Error("unversioned entry not flagged untracked")
		}
		if c.Path == "conf.txt" && !c.Conflict {
			t.Error("conflicted entry not flagged")
		}
	}
}

func TestParseSvnStatusRootProps(t *testing.T) {
	const x = `<status><target path=".">
<entry path="."><wc-status props="modified" item="normal" revision="2"></wc-status></entry>
</target></status>`
	got, err := parseSvnStatusXML([]byte(x))
	if err != nil || len(got) != 1 || got[0].Path != "." || got[0].Status != "M" {
		t.Fatalf("root props change = %+v, %v", got, err)
	}
	const c = `<status><target path=".">
<entry path="."><wc-status props="conflicted" item="normal" revision="2"></wc-status></entry>
</target></status>`
	got, _ = parseSvnStatusXML([]byte(c))
	if len(got) != 1 || !got[0].Conflict {
		t.Fatalf("root props conflict = %+v", got)
	}
}

func TestParseSvnStatusTreeConflict(t *testing.T) {
	const x = `<status><target path=".">
<entry path="added.txt"><wc-status props="none" item="added" revision="-1" tree-conflicted="true"></wc-status></entry>
<entry path="normal.txt"><wc-status props="none" item="normal" revision="3" tree-conflicted="true"></wc-status></entry>
</target></status>`
	got, err := parseSvnStatusXML([]byte(x))
	if err != nil || len(got) != 2 {
		t.Fatalf("tree conflicts = %+v, %v", got, err)
	}
	if got[0].Status != "A" || !got[0].Conflict {
		t.Errorf("added + tree conflict = %+v", got[0])
	}
	if got[1].Status != "C" || !got[1].Conflict {
		t.Errorf("normal + tree conflict = %+v", got[1])
	}
}

func TestSvnURLPathUnder(t *testing.T) {
	cases := [][3]string{
		{"svn://h/r", "svn://h/r", "/"},
		{"svn://h/r/", "svn://h/r/trunk/", "/trunk"},
		{"svn://h/r", "svn://h/r/trunk%20space/%E6%97%A5%E6%9C%AC%20100%25", "/trunk space/日本 100%"},
	}
	for _, c := range cases {
		if got := svnURLPathUnder(c[0], c[1]); got != c[2] {
			t.Errorf("svnURLPathUnder(%q,%q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}

// TestSvnShowEncodedCheckoutPath: a working copy checked out from a URL with a space, Japanese
// and '%' in it must still diff with a path filter (the info URL is percent-encoded, the log's
// paths are not; joining them unconverted double-encoded the diff URL and failed E160013).
func TestSvnShowEncodedCheckoutPath(t *testing.T) {
	if !svnAvailable() {
		t.Skip("svn not installed")
	}
	if _, err := exec.LookPath("svnadmin"); err != nil {
		t.Skip("svnadmin not installed")
	}
	t.Setenv("HOME", t.TempDir())
	repo := filepath.Join(t.TempDir(), "repo")
	if out, err := exec.Command("svnadmin", "create", repo).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin: %v: %s", err, out)
	}
	base := "file://" + repo
	dirName := "trunk space/日本 100%"
	scratch := filepath.Join(t.TempDir(), "s")
	svnRun(t, "", "checkout", base, scratch)
	if err := os.MkdirAll(filepath.Join(scratch, dirName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scratch, dirName, "a.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	svnRun(t, scratch, "add", "--parents", "trunk space")
	svnRun(t, scratch, "commit", "-m", "add")
	wc := filepath.Join(gitx.ReposRoot(), "enc")
	svnRun(t, "", "checkout", svnURLJoin(base, dirName), wc)

	var show svnShowResp
	if rec := svnView(t, handleSvnShow, "enc", "rev=1&path=a.txt", &show); rec.Code != http.StatusOK {
		t.Fatalf("svn-show = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(show.Diff, "+hi") || len(show.Paths) != 1 || show.Paths[0].Path != "/"+dirName+"/a.txt" {
		t.Errorf("show = %+v", show)
	}
}

func TestSvnRelPath(t *testing.T) {
	ok := map[string]string{"": "", ".": "", "/": "", "a/b.txt": "a/b.txt", "/a//b/../c": "a/c", "日本語/x": "日本語/x", "a@b": "a@b"}
	for in, want := range ok {
		if got, valid := svnRelPath(in); !valid || got != want {
			t.Errorf("svnRelPath(%q) = %q,%v want %q,true", in, got, valid, want)
		}
	}
	for _, in := range []string{"..", "../x", "a/../../x", "-r", "-", "a/.svn/x", ".svn", "a\x00b"} {
		if _, valid := svnRelPath(in); valid {
			t.Errorf("svnRelPath(%q) accepted", in)
		}
	}
}

func TestParseSvnRev(t *testing.T) {
	for in, want := range map[string]int{"1": 1, "42": 42, " 7 ": 7} {
		if got, ok := parseSvnRev(in); !ok || got != want {
			t.Errorf("parseSvnRev(%q) = %d,%v", in, got, ok)
		}
	}
	for _, in := range []string{"", "0", "-1", "HEAD", "r5", "5:9", "{2026-01-01}", "1e3", "1234567890", "+3"} {
		if _, ok := parseSvnRev(in); ok {
			t.Errorf("parseSvnRev(%q) accepted", in)
		}
	}
}

func TestSvnDiffToGit(t *testing.T) {
	in := "Index: a.txt\n===================================================================\n--- a.txt\t(revision 4)\n+++ a.txt\t(working copy)\n@@ -1 +1,2 @@\n one\n+Index: not a header\n" +
		"Index: dir/b b.txt\n===================================================================\n--- dir/b b.txt\t(nonexistent)\n+++ dir/b b.txt\t(revision 5)\n@@ -0,0 +1 @@\n+x\n"
	want := "diff --git a/a.txt b/a.txt\n--- a/a.txt\t(revision 4)\n+++ b/a.txt\t(working copy)\n@@ -1 +1,2 @@\n one\n+Index: not a header\n" +
		"diff --git a/dir/b b.txt b/dir/b b.txt\n--- a/dir/b b.txt\t(nonexistent)\n+++ b/dir/b b.txt\t(revision 5)\n@@ -0,0 +1 @@\n+x\n"
	if got := svnDiffToGit(in); got != want {
		t.Errorf("svnDiffToGit =\n%q\nwant\n%q", got, want)
	}
	if got := svnDiffToGit(""); got != "" {
		t.Errorf("empty diff = %q", got)
	}
}

func TestSvnURLJoin(t *testing.T) {
	if got := svnURLJoin("svn://h/r/", "trunk/a b@c/日本"); got != "svn://h/r/trunk/a%20b%40c/%E6%97%A5%E6%9C%AC" {
		t.Errorf("svnURLJoin = %q", got)
	}
	if got := svnURLJoin("svn://h/r/", ""); got != "svn://h/r" {
		t.Errorf("svnURLJoin root = %q", got)
	}
}

// svnView issues one GET against a view handler and decodes the JSON body.
func svnView(t *testing.T, h http.HandlerFunc, name, query string, into any) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/repos/"+name+"/x?"+query, nil)
	req.SetPathValue("name", name)
	h(rec, req)
	if into != nil && rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), into); err != nil {
			t.Fatalf("decode: %v: %s", err, rec.Body.String())
		}
	}
	return rec
}

func svnRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := svnCmd(nil, append([]string{"--non-interactive", "--no-auth-cache"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("svn %v: %v: %s", args, err, out)
	}
	return string(out)
}

// TestSvnViewEndToEnd drives the four view routes against a real, authenticating svnserve.
// The positive control comes first: with no stored credential svn-log and svn-show must fail
// as svn_auth_required (otherwise a server that never asks would pass everything below), while
// the LOCAL pair keeps working without one.
func TestSvnViewEndToEnd(t *testing.T) {
	if !svnAvailable() {
		t.Skip("svn not installed")
	}
	if _, err := exec.LookPath("svnadmin"); err != nil {
		t.Skip("svnadmin not installed")
	}
	const user, pass = "alice", "s3cret"
	srvRoot := t.TempDir()
	base := startSvnserve(t, srvRoot, user, pass)
	trunk := base + "/trunk"
	creds := &secrets.SVNCred{URLPrefix: base, Username: user, Password: pass}

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SECRET_KEY", "")

	// Seed: r1 import, r2 edit a.txt, r3 add sub/b.txt, r4 delete a.txt — through a scratch wc.
	scratch := filepath.Join(t.TempDir(), "scratch")
	if out, err := runSvnAuthed(t.Context(), creds, "mkdir", "-m", "layout", base+"/trunk"); err != nil {
		t.Fatalf("mkdir trunk: %v: %s", err, out)
	}
	if out, err := runSvnAuthed(t.Context(), creds, "checkout", trunk, scratch); err != nil {
		t.Fatalf("scratch checkout: %v: %s", err, out)
	}
	commit := func(msg string) {
		t.Helper()
		cmd := svnCmd(t.Context(), "--non-interactive", "--no-auth-cache", "--username", user, "--password-from-stdin", "commit", "-m", msg)
		cmd.Dir = scratch
		cmd.Stdin = strings.NewReader(pass + "\n")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("commit: %v: %s", err, out)
		}
	}
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(scratch, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.txt", "one\n")
	svnRun(t, scratch, "add", "a.txt")
	commit("add a") // r2
	write("a.txt", "one\ntwo\n")
	commit("edit a\n\nsecond line of the message") // r3
	write("sub/b.txt", "bee\n")
	svnRun(t, scratch, "add", "sub")
	commit("add sub/b") // r4

	// The working copy under test sits at r4 and is named with an '@' (folder names may).
	wcName := "docs@wc"
	wc := filepath.Join(gitx.ReposRoot(), wcName)
	if out, err := runSvnAuthed(t.Context(), creds, "checkout", trunk, wc); err != nil {
		t.Fatalf("checkout: %v: %s", err, out)
	}
	if c := svnCredsFor(trunk); c != nil {
		t.Fatalf("expected an empty store, got %+v", c)
	}
	// A revision the working copy has not seen yet: r5 deletes a.txt.
	svnRun(t, scratch, "rm", "a.txt")
	commit("remove a") // r5

	// ---- positive control: no credential → svn_auth_required, not a raw error.
	rec := svnView(t, handleSvnLog, wcName, "", nil)
	if rec.Code != http.StatusUnauthorized || errCodeOf(t, rec) != errCodeSvnAuth {
		t.Fatalf("svn-log without creds = %d %s, want 401 %s", rec.Code, rec.Body.String(), errCodeSvnAuth)
	}
	rec = svnView(t, handleSvnShow, wcName, "rev=3", nil)
	if rec.Code != http.StatusUnauthorized || errCodeOf(t, rec) != errCodeSvnAuth {
		t.Fatalf("svn-show without creds = %d %s, want 401 %s", rec.Code, rec.Body.String(), errCodeSvnAuth)
	}

	// ---- the local pair needs no credential.
	write2 := func(rel, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(wc, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write2("a.txt", "one\ntwo\nthree\n")
	write2("untracked.txt", "u\n")
	write2("sub/new.txt", "n\n")
	svnRun(t, wc, "add", "sub/new.txt")
	var changes struct {
		Changes []svnChange `json:"changes"`
	}
	if rec := svnView(t, handleSvnChanges, wcName, "", &changes); rec.Code != http.StatusOK {
		t.Fatalf("svn-changes = %d: %s", rec.Code, rec.Body.String())
	}
	got := map[string]string{}
	for _, c := range changes.Changes {
		got[c.Path] = c.Status
	}
	if !reflect.DeepEqual(got, map[string]string{"a.txt": "M", "untracked.txt": "?", "sub/new.txt": "A"}) {
		t.Errorf("changes = %v", got)
	}
	var diff struct {
		Diff string `json:"diff"`
	}
	if rec := svnView(t, handleSvnDiff, wcName, "path=a.txt", &diff); rec.Code != http.StatusOK {
		t.Fatalf("svn-diff = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.HasPrefix(diff.Diff, "diff --git a/a.txt b/a.txt\n--- a/a.txt\t(revision 4)\n+++ b/a.txt\t(working copy)\n") || !strings.Contains(diff.Diff, "+three") {
		t.Errorf("svn-diff body = %q", diff.Diff)
	}
	for _, bad := range []string{"path=../x", "path=-r", "path=a/.svn/x"} {
		if rec := svnView(t, handleSvnDiff, wcName, bad, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("svn-diff %s = %d, want 400", bad, rec.Code)
		}
	}

	// ---- with the credential stored, the log and a revision open.
	if err := svnSaveCred(base, user, pass, false); err != nil {
		t.Fatal(err)
	}
	type logResp struct {
		Revisions  []svnLogEntry `json:"revisions"`
		HasMore    bool          `json:"hasMore"`
		WCRevision string        `json:"wcRevision"`
	}
	revs := func(l logResp) []int {
		var r []int
		for _, e := range l.Revisions {
			r = append(r, e.Rev)
		}
		return r
	}
	var l logResp
	if rec := svnView(t, handleSvnLog, wcName, "limit=3", &l); rec.Code != http.StatusOK {
		t.Fatalf("svn-log = %d: %s", rec.Code, rec.Body.String())
	}
	// Newest first, including r5, which is newer than the working copy.
	if !reflect.DeepEqual(revs(l), []int{5, 4, 3}) || !l.HasMore || l.WCRevision != "4" {
		t.Fatalf("page 1 = %v hasMore=%v wc=%q", revs(l), l.HasMore, l.WCRevision)
	}
	if l.Revisions[0].Message != "remove a" || l.Revisions[0].Author != user || len(l.Revisions[0].Paths) != 1 || l.Revisions[0].Paths[0].Action != "D" {
		t.Errorf("r5 = %+v", l.Revisions[0])
	}
	if rec := svnView(t, handleSvnLog, wcName, "limit=3&from=3", &l); rec.Code != http.StatusOK {
		t.Fatalf("svn-log page 2 = %d: %s", rec.Code, rec.Body.String())
	}
	if !reflect.DeepEqual(revs(l), []int{2, 1}) || l.HasMore {
		t.Fatalf("page 2 = %v hasMore=%v", revs(l), l.HasMore)
	}
	if rec := svnView(t, handleSvnLog, wcName, "from=1", &l); rec.Code != http.StatusOK || len(l.Revisions) != 0 || l.HasMore {
		t.Fatalf("page past r1 = %d %+v", rec.Code, l)
	}
	if rec := svnView(t, handleSvnLog, wcName, "path=sub", &l); rec.Code != http.StatusOK || !reflect.DeepEqual(revs(l), []int{4}) {
		t.Fatalf("path filter = %d %v", rec.Code, revs(l))
	}
	for _, bad := range []string{"path=../x", "path=-r", "from=HEAD", "from=-5"} {
		if rec := svnView(t, handleSvnLog, wcName, bad, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("svn-log %s = %d, want 400", bad, rec.Code)
		}
	}

	var show struct {
		Rev     int          `json:"rev"`
		Short   string       `json:"short"`
		Subject string       `json:"subject"`
		Body    string       `json:"body"`
		Paths   []svnLogPath `json:"paths"`
		Diff    string       `json:"diff"`
	}
	if rec := svnView(t, handleSvnShow, wcName, "rev=3", &show); rec.Code != http.StatusOK {
		t.Fatalf("svn-show r3 = %d: %s", rec.Code, rec.Body.String())
	}
	if show.Rev != 3 || show.Short != "r3" || show.Subject != "edit a" || show.Body != "second line of the message" {
		t.Errorf("show header = %+v", show)
	}
	if len(show.Paths) != 1 || show.Paths[0].Path != "/trunk/a.txt" || show.Paths[0].Action != "M" {
		t.Errorf("show paths = %+v", show.Paths)
	}
	if !strings.Contains(show.Diff, "diff --git") || !strings.Contains(show.Diff, "+two") {
		t.Errorf("show diff = %q", show.Diff)
	}
	// A revision newer than the working copy opens too (the diff is taken at the root URL).
	if rec := svnView(t, handleSvnShow, wcName, "rev=5", &show); rec.Code != http.StatusOK || !strings.Contains(show.Diff, "-two") {
		t.Fatalf("svn-show r5 = %d: %s", rec.Code, rec.Body.String())
	}
	// A path filter keeps only that subtree's paths.
	if rec := svnView(t, handleSvnShow, wcName, "rev=4&path=sub", &show); rec.Code != http.StatusOK || len(show.Paths) != 2 || !strings.Contains(show.Diff, "diff --git a/b.txt b/b.txt") {
		t.Fatalf("svn-show r4 path=sub = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := svnView(t, handleSvnShow, wcName, "rev=999", nil); rec.Code == http.StatusOK {
		t.Errorf("svn-show of a missing revision = 200")
	}
	for _, bad := range []string{"", "rev=0", "rev=HEAD", "rev=3:4", "rev=-1", "rev=3&path=../x"} {
		if rec := svnView(t, handleSvnShow, wcName, bad, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("svn-show %q = %d, want 400", bad, rec.Code)
		}
	}
}

// TestSvnViewNotAWorkingCopy: a git folder (or a missing one) is a 404, never a svn run.
func TestSvnViewNotAWorkingCopy(t *testing.T) {
	if !svnAvailable() {
		t.Skip("svn not installed")
	}
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Join(gitx.ReposRoot(), "plain"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, h := range []http.HandlerFunc{handleSvnLog, handleSvnShow, handleSvnChanges, handleSvnDiff} {
		if rec := svnView(t, h, "plain", "rev=1", nil); rec.Code != http.StatusNotFound {
			t.Errorf("non-svn folder = %d, want 404", rec.Code)
		}
	}
}
