package sessionx

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/spacex"
)

// placeSpace makes ~/spaces/<folder> with its marker, as spacex.Create would but under a folder
// name the test chooses, so a space can be given a repository's name.
func placeSpace(t *testing.T, folder string) string {
	t.Helper()
	dir := filepath.Join(spacex.Root(), folder)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, spacex.MarkerFile), []byte(`{"name":"`+folder+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// editConfig replaces the one occurrence of old in a git config file.
func editConfig(t *testing.T, path, old, new string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(b), old) != 1 {
		t.Fatalf("%s: %q occurs %d times in %s", path, old, strings.Count(string(b), old), b)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(b), old, new, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func placeWorktree(t *testing.T, main, dir, branch string) {
	t.Helper()
	if out, err := exec.Command("git", "-C", main, "worktree", "add", "-b", branch, dir).CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v: %s", err, out)
	}
}

func TestPlaceOf(t *testing.T) {
	isolateAgentState(t)
	home := homeDir()
	repos := filepath.Join(home, "repos")
	gitInit(t, filepath.Join(repos, "foo"))
	placeWorktree(t, filepath.Join(repos, "foo"), filepath.Join(repos, "foo@wt"), "wt")
	// A worktree whose main copy lies outside ~/repos is still a worktree, with no parent.
	gitInit(t, filepath.Join(home, "elsewhere", "main"))
	placeWorktree(t, filepath.Join(home, "elsewhere", "main"), filepath.Join(repos, "stray"), "stray")
	if err := os.MkdirAll(filepath.Join(repos, "foo", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repos, "plain"), 0o755); err != nil {
		t.Fatal(err)
	}
	placeSpace(t, "foo")
	if err := os.MkdirAll(filepath.Join(spacex.Root(), "handmade"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "home-link")
	if err := os.Symlink(home, link); err != nil {
		t.Fatal(err)
	}
	// GET /repos lists any folder name, spaces included, so the place does too.
	gitInit(t, filepath.Join(repos, "team app"))
	// A symlink entry under ~/repos is skipped by GET /repos and is no repository here either, and
	// a link elsewhere is filed by its own spelling, never by where it points now (it can be
	// pointed elsewhere while the session is stopped).
	if err := os.Symlink(filepath.Join(repos, "foo"), filepath.Join(repos, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "links"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(repos, "foo@wt"), filepath.Join(home, "links", "foo@wt")); err != nil {
		t.Fatal(err)
	}
	// ~/links/sub/.. runs in ~/repos/foo today, and wherever sub points after it is re-pointed.
	if err := os.Symlink(filepath.Join(repos, "foo", "sub"), filepath.Join(home, "links", "sub")); err != nil {
		t.Fatal(err)
	}
	// ~/repos/hop/../foo runs in ~/elsewhere/foo, wherever Clean would fold it lexically.
	gitInit(t, filepath.Join(home, "elsewhere", "inner"))
	gitInit(t, filepath.Join(home, "elsewhere", "foo"))
	if err := os.Symlink(filepath.Join(home, "elsewhere", "inner"), filepath.Join(repos, "hop")); err != nil {
		t.Fatal(err)
	}
	// ~/elsewhere/back/../team app runs in ~/repos/team app: back points into ~/repos.
	if err := os.Symlink(filepath.Join(repos, "foo"), filepath.Join(home, "elsewhere", "back")); err != nil {
		t.Fatal(err)
	}
	// A linked worktree of a --separate-git-dir copy, and one of a bare repository: the folder
	// above their common git directory is no working copy, so neither gets a parent.
	if err := os.MkdirAll(filepath.Join(repos, "storage"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repos, "init", "-q", "-b", "main", "--separate-git-dir", filepath.Join(repos, "storage", "admin.git"), filepath.Join(repos, "sep"))
	gitRun(t, filepath.Join(repos, "sep"), "commit", "-q", "--allow-empty", "-m", "init")
	placeWorktree(t, filepath.Join(repos, "sep"), filepath.Join(repos, "sep@wt"), "sepwt")
	gitRun(t, repos, "clone", "-q", "--bare", filepath.Join(repos, "foo"), filepath.Join(repos, "storage", "vendor.git"))
	placeWorktree(t, filepath.Join(repos, "storage", "vendor.git"), filepath.Join(repos, "vendor@wt"), "vendorwt")
	// A bare repository kept in a directory called .git: the folder above it holds no working
	// copy, whatever the common dir's name says.
	gitRun(t, repos, "clone", "-q", "--bare", filepath.Join(repos, "foo"), filepath.Join(repos, "hollow", ".git"))
	placeWorktree(t, filepath.Join(repos, "hollow", ".git"), filepath.Join(repos, "hollow@wt"), "hollowwt")
	// Config syntax git accepts must not fool the parent check: a comment after a section
	// header leaves hollow bare, a comment after a value leaves foo a working copy, and a
	// [core "x"] subsection's bare is another key.
	editConfig(t, filepath.Join(repos, "hollow", ".git", "config"), "[core]", "[core] # storage")
	editConfig(t, filepath.Join(repos, "foo", ".git", "config"), "bare = false", "bare = false # working copy\n[core \"unrelated\"]\n\tbare = true\n[core]")
	// A git dir named .git inside vault whose core.worktree is app3: app3 is the main copy,
	// and vault holds none.
	if err := os.MkdirAll(filepath.Join(repos, "vault"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repos, "init", "-q", "-b", "main", "--separate-git-dir", filepath.Join(repos, "vault", ".git"), filepath.Join(repos, "app3"))
	gitRun(t, filepath.Join(repos, "app3"), "config", "core.worktree", filepath.Join(repos, "app3"))
	gitRun(t, filepath.Join(repos, "app3"), "commit", "-q", "--allow-empty", "-m", "init")
	placeWorktree(t, filepath.Join(repos, "app3"), filepath.Join(repos, "app3@wt"), "app3wt")
	// A gitfile git refuses (no "gitdir: " prefix, or no space after it) makes no worktree,
	// even when the path it names is a real one.
	gitInit(t, filepath.Join(repos, "bad"))
	for _, c := range []struct{ folder, body string }{{"bad@noprefix", "%s\n"}, {"bad@nospace", "gitdir:%s\n"}} {
		dir := filepath.Join(repos, c.folder)
		placeWorktree(t, filepath.Join(repos, "bad"), dir, c.folder[4:])
		admin := filepath.Join(repos, "bad", ".git", "worktrees", c.folder)
		if err := os.WriteFile(filepath.Join(dir, ".git"), []byte(fmt.Sprintf(c.body, admin)), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	for _, c := range []struct {
		dir  string
		want session.Place
	}{
		{home, session.Place{Kind: session.PlaceHome}},
		{home + "/", session.Place{Kind: session.PlaceHome}},
		{link, session.Place{Kind: session.PlaceHome}},
		{filepath.Join(repos, "foo"), session.Place{Kind: session.PlaceRepo, ID: "foo"}},
		{filepath.Join(link, "repos", "foo"), session.Place{Kind: session.PlaceRepo, ID: "foo"}},
		{filepath.Join(repos, "plain"), session.Place{Kind: session.PlaceRepo, ID: "plain"}},
		{filepath.Join(repos, "foo") + "/.", session.Place{Kind: session.PlaceRepo, ID: "foo"}},
		{filepath.Join(repos, "foo", "sub") + "/..", session.Place{Kind: session.PlaceRepo, ID: "foo"}},
		{filepath.Join(repos, "team app"), session.Place{Kind: session.PlaceRepo, ID: "team app"}},
		{filepath.Join(repos, "alias"), session.Place{Kind: session.PlaceOther}},
		{filepath.Join(repos, "alias") + "/", session.Place{Kind: session.PlaceOther}},
		{filepath.Join(home, "links", "foo@wt"), session.Place{Kind: session.PlaceOther}},
		{filepath.Join(repos, "hop") + "/../foo", session.Place{Kind: session.PlaceOther}},
		{filepath.Join(repos, "hop") + "/../foo/.", session.Place{Kind: session.PlaceOther}},
		// A ".." over a link is no place: the link can be pointed elsewhere later (#5262 review).
		{filepath.Join(home, "elsewhere", "back") + "/../team app", session.Place{Kind: session.PlaceOther}},
		{filepath.Join(home, "links", "sub") + "/..", session.Place{Kind: session.PlaceOther}},
		{filepath.Join(repos, "sep"), session.Place{Kind: session.PlaceRepo, ID: "sep"}},
		{filepath.Join(repos, "sep@wt"), session.Place{Kind: session.PlaceWorktree, ID: "sep@wt"}},
		{filepath.Join(repos, "vendor@wt"), session.Place{Kind: session.PlaceWorktree, ID: "vendor@wt"}},
		{filepath.Join(repos, "hollow@wt"), session.Place{Kind: session.PlaceWorktree, ID: "hollow@wt"}},
		{filepath.Join(repos, "app3@wt"), session.Place{Kind: session.PlaceWorktree, ID: "app3@wt"}},
		{filepath.Join(repos, "bad@noprefix"), session.Place{Kind: session.PlaceRepo, ID: "bad@noprefix"}},
		{filepath.Join(repos, "bad@nospace"), session.Place{Kind: session.PlaceRepo, ID: "bad@nospace"}},
		{filepath.Join(repos, "foo@wt"), session.Place{Kind: session.PlaceWorktree, ID: "foo@wt", Parent: "foo"}},
		{filepath.Join(repos, "stray"), session.Place{Kind: session.PlaceWorktree, ID: "stray"}},
		{filepath.Join(spacex.Root(), "foo"), session.Place{Kind: session.PlaceSpace, ID: "foo"}},
		// A folder made by hand under ~/spaces carries no marker and is no space.
		{filepath.Join(spacex.Root(), "handmade"), session.Place{Kind: session.PlaceOther}},
		{repos, session.Place{Kind: session.PlaceOther}},
		{filepath.Join(repos, "foo", "sub"), session.Place{Kind: session.PlaceOther}},
		{filepath.Join(home, "elsewhere", "main"), session.Place{Kind: session.PlaceOther}},
		{"/", session.Place{Kind: session.PlaceOther}},
		{"repos/foo", session.Place{Kind: session.PlaceOther}},
		{"", session.Place{Kind: session.PlaceOther}},
	} {
		got := PlaceOf(c.dir)
		if got == nil || *got != c.want {
			t.Errorf("PlaceOf(%q) = %+v, want %+v", c.dir, got, c.want)
		}
	}
}

// With ~/repos a symlink, a relative gitfile path leaves from the link's target, as git reads
// it: ~/repos/wt's "../../meta/app/.git/worktrees/wt" names <ws>/meta/..., never ~/meta/....
func TestPlaceOfReadsRelativeGitPathsPhysically(t *testing.T) {
	isolateAgentState(t)
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "repos"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(ws, "repos"), filepath.Join(homeDir(), "repos")); err != nil {
		t.Fatal(err)
	}
	gitInit(t, filepath.Join(ws, "meta", "app"))
	placeWorktree(t, filepath.Join(ws, "meta", "app"), filepath.Join(ws, "repos", "wt"), "wt")
	if err := os.WriteFile(filepath.Join(ws, "repos", "wt", ".git"), []byte("gitdir: ../../meta/app/.git/worktrees/wt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(homeDir(), "repos", "wt")
	if got := PlaceOf(dir); *got != (session.Place{Kind: session.PlaceWorktree, ID: "wt"}) {
		t.Errorf("PlaceOf(%q) = %+v, want a worktree with no parent under ~/repos", dir, got)
	}
}

// A FIFO where a gitfile or commondir should be is never read: the create that classifies the
// directory would otherwise wait for a writer forever.
func TestPlaceOfNeverBlocksOnAFIFO(t *testing.T) {
	isolateAgentState(t)
	repos := filepath.Join(homeDir(), "repos")
	if err := os.MkdirAll(filepath.Join(repos, "pipe"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(repos, "pipe", ".git"), 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	gitInit(t, filepath.Join(repos, "main"))
	placeWorktree(t, filepath.Join(repos, "main"), filepath.Join(repos, "main@wt"), "wt")
	commondir := filepath.Join(repos, "main", ".git", "worktrees", "main@wt", "commondir")
	if err := os.Remove(commondir); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(commondir, 0o644); err != nil {
		t.Fatal(err)
	}
	// The main copy's own config.worktree is read only by the parent check, never by the
	// worktree's session: a FIFO there costs the create no wait, only the parent.
	gitInit(t, filepath.Join(repos, "cfg"))
	gitRun(t, filepath.Join(repos, "cfg"), "config", "extensions.worktreeConfig", "true")
	placeWorktree(t, filepath.Join(repos, "cfg"), filepath.Join(repos, "cfg@wt"), "cfgwt")
	if err := syscall.Mkfifo(filepath.Join(repos, "cfg", ".git", "config.worktree"), 0o644); err != nil {
		t.Fatal(err)
	}
	for dir, want := range map[string]session.Place{
		filepath.Join(repos, "pipe"):    {Kind: session.PlaceRepo, ID: "pipe"},
		filepath.Join(repos, "main@wt"): {Kind: session.PlaceRepo, ID: "main@wt"},
		filepath.Join(repos, "cfg@wt"):  {Kind: session.PlaceWorktree, ID: "cfg@wt"},
	} {
		done := make(chan *session.Place, 1)
		go func() { done <- PlaceOf(dir) }()
		select {
		case got := <-done:
			if *got != want {
				t.Errorf("PlaceOf(%q) = %+v, want %+v", dir, got, want)
			}
		// Below the parent check's 3 s timeout, so waiting it out fails too.
		case <-time.After(2 * time.Second):
			t.Fatalf("PlaceOf(%q) waited on a FIFO", dir)
		}
	}
}

// A space and a repository that share a folder name never share a place: neither the kind nor
// the meta's stored value lets a client file one under the other.
func TestPlaceSpaceAndRepositoryOfOneNameStayApart(t *testing.T) {
	p := newCreateProbe(t)
	launchTmuxFn = func(session.Meta, bool) error { return nil }
	spaceDir := placeSpace(t, "app") // newCreateProbe's repository is ~/repos/app
	for _, body := range []map[string]any{
		{"kind": "shell", "dir": p.repo},
		{"kind": "shell", "space": map[string]any{"folder": "app"}},
		{"kind": "shell", "dir": homeDir()},
	} {
		if rec := p.post(body, ""); rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
			t.Fatalf("create %v: %d %s", body, rec.Code, rec.Body)
		}
	}
	places := map[string]*session.Place{}
	repos := map[string]string{}
	for _, m := range session.ListMetas() {
		if m.Dir == p.repo || m.Dir == spaceDir || m.Dir == homeDir() {
			places[m.Dir] = m.Place
			repos[m.Dir] = m.Repo
		}
	}
	// Repo names a working copy only: a space and home record none, so neither passes for the
	// repository named like its folder.
	if repos[p.repo] != "app" || repos[spaceDir] != "" || repos[homeDir()] != "" {
		t.Errorf("Repo by dir = %q", repos)
	}
	if h := places[homeDir()]; h == nil || h.Kind != session.PlaceHome {
		t.Errorf("home session place = %+v", h)
	}
	repo, space := places[p.repo], places[spaceDir]
	if repo == nil || *repo != (session.Place{Kind: session.PlaceRepo, ID: "app"}) {
		t.Errorf("repository session place = %+v", repo)
	}
	if space == nil || *space != (session.Place{Kind: session.PlaceSpace, ID: "app"}) {
		t.Errorf("space session place = %+v", space)
	}

	// The same two rows as GET /sessions answers them.
	rec := httptest.NewRecorder()
	HandleListSessions(rec, httptest.NewRequest(http.MethodGet, "/sessions", nil))
	var body struct {
		Sessions []struct {
			Dir   string         `json:"dir"`
			Place *session.Place `json:"place"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("GET /sessions: %d %v: %s", rec.Code, err, rec.Body)
	}
	listed := map[string]session.Place{}
	for _, r := range body.Sessions {
		if r.Place != nil {
			listed[r.Dir] = *r.Place
		}
	}
	if got := listed[p.repo]; got != (session.Place{Kind: session.PlaceRepo, ID: "app"}) {
		t.Errorf("GET /sessions repository row place = %+v", got)
	}
	if got := listed[spaceDir]; got != (session.Place{Kind: session.PlaceSpace, ID: "app"}) {
		t.Errorf("GET /sessions space row place = %+v", got)
	}
}

// The wire carries the stored place as it is, on the live list and the archived shelf alike,
// and a meta written before the field existed reports none rather than a guess.
func TestWireCarriesTheStoredPlace(t *testing.T) {
	isolateAgentState(t)
	stored := &session.Place{Kind: session.PlaceSpace, ID: "notes"}
	m := session.Meta{Name: "plw1", Kind: session.KindShell, Dir: filepath.Join(homeDir(), "repos", "notes"), Repo: "notes", Place: stored}
	for name, s := range map[string]session.Session{"live": wireSession(m, false), "archived": wireArchivedRow(m)} {
		if s.Place == nil || *s.Place != *stored {
			t.Errorf("%s: place = %+v, want %+v", name, s.Place, stored)
		}
		if s.Place == stored {
			t.Errorf("%s: the wire row aliases the meta's place", name)
		}
	}
	legacy := m
	legacy.Place = nil
	for name, s := range map[string]session.Session{"live": wireSession(legacy, false), "archived": wireArchivedRow(legacy)} {
		if s.Place != nil {
			t.Errorf("%s: a meta with no place reports %+v", name, s.Place)
		}
		b, _ := json.Marshal(s)
		var raw map[string]any
		_ = json.Unmarshal(b, &raw)
		if _, ok := raw["place"]; ok {
			t.Errorf("%s: a meta with no place writes a place key: %s", name, b)
		}
	}
}

// A fork and a recreate are creates too: each stores the place of its own directory instead of
// leaving it empty.
func TestForkAndRecreateStoreThePlace(t *testing.T) {
	isolateAgentState(t)
	spaceDir := placeSpace(t, "kept")
	want := session.Place{Kind: session.PlaceSpace, ID: "kept"}
	fork := forkMeta(session.Meta{Name: "plsrc", Dir: spaceDir, Kind: session.KindShell, Repo: "kept"}, "plfork", "", "plsrc", "")
	if got := fork.Place; got == nil || *got != want {
		t.Errorf("fork place = %+v, want %+v", got, want)
	}
	if fork.Repo != "" {
		t.Errorf("fork of a space session records Repo %q", fork.Repo)
	}

	// Repo as an Agent from before the place was stored wrote it: the space's folder name.
	session.WriteMeta(session.Meta{Name: "plrecr", Dir: spaceDir, Kind: session.KindShell, Repo: "kept"})
	if rec := callHandler(HandleRecreateSession, http.MethodPost, "plrecr", ""); rec.Code != http.StatusOK {
		t.Fatalf("recreate: %d %s", rec.Code, rec.Body)
	}
	var fresh *session.Meta
	for _, m := range session.ListMetas() {
		if m.Dir == spaceDir && m.Name != "plrecr" {
			m := m
			fresh = &m
		}
	}
	if fresh == nil {
		t.Fatal("recreate left no new session")
	}
	if fresh.Place == nil || *fresh.Place != want {
		t.Errorf("recreate place = %+v, want %+v", fresh.Place, want)
	}
	if fresh.Repo != "" {
		t.Errorf("recreate copied Repo %q instead of deriving it from the place", fresh.Repo)
	}
}

// The handoff context names the working copy by the same rule as the meta: none for a space.
func TestHandoffContextNamesNoRepositoryForASpace(t *testing.T) {
	isolateAgentState(t)
	spaceDir := placeSpace(t, "app")
	if got := handoffRepo(session.Meta{Dir: spaceDir}); got != "" {
		t.Errorf("space handoff context repo = %q", got)
	}
	if got := handoffRepo(session.Meta{Dir: homeDir()}); got != "" {
		t.Errorf("home handoff context repo = %q", got)
	}
	other := filepath.Join(homeDir(), "projects", "app")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := handoffRepo(session.Meta{Dir: other}); got != "app" {
		t.Errorf("other directory handoff context repo = %q, want its base name", got)
	}

	// GET handoff-context goes by the place the session stored, not by what the directory
	// looks like now: with the space's marker gone the session is still the space's.
	session.WriteMeta(session.Meta{Name: "plhoff", Kind: session.KindShell, Dir: spaceDir, Place: &session.Place{Kind: session.PlaceSpace, ID: "app"}})
	if err := os.Remove(filepath.Join(spaceDir, spacex.MarkerFile)); err != nil {
		t.Fatal(err)
	}
	rec := callHandler(HandleSessionHandoffContext, http.MethodGet, "plhoff", "")
	var c handoffContext
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("GET handoff-context: %d %v: %s", rec.Code, err, rec.Body)
	}
	if c.Repo != "" {
		t.Errorf("handoff context of a space session whose marker is gone names repo %q", c.Repo)
	}
}

// A shared space or home session keeps the folder name the recipient's heading and an inbox
// attachment show, although its meta records no Repo.
func TestCatalogNamesASpaceSessionByItsFolder(t *testing.T) {
	isolateAgentState(t)
	spaceDir := placeSpace(t, "notes")
	if got := catalogEntry(session.Meta{Name: "plcat", Kind: session.KindShell, Dir: spaceDir}, false).Repo; got != "notes" {
		t.Errorf("space catalog repo = %q", got)
	}
	if got := catalogEntry(session.Meta{Name: "plcat", Kind: session.KindShell, Dir: spaceDir, Repo: "app"}, false).Repo; got != "app" {
		t.Errorf("catalog repo with Repo set = %q", got)
	}
}

// The cleanup dialog groups a stopped space session under its folder although its meta records
// no Repo, so same-titled rows of two spaces stay apart.
func TestCleanupGroupsASpaceSessionByItsFolder(t *testing.T) {
	isolateAgentState(t)
	for _, f := range []string{"one", "two"} {
		session.WriteMeta(session.Meta{Name: "plcl" + f, Kind: session.KindShell, Dir: placeSpace(t, f), Title: "same", Place: &session.Place{Kind: session.PlaceSpace, ID: f}})
	}
	rec := httptest.NewRecorder()
	HandleSessionsCleanup(rec, httptest.NewRequest(http.MethodGet, "/sessions/cleanup", nil))
	var body struct {
		Candidates []cleanupCandidate `json:"candidates"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("GET /sessions/cleanup: %d %v: %s", rec.Code, err, rec.Body)
	}
	got := map[string]string{}
	for _, c := range body.Candidates {
		got[c.ID] = c.Repo
	}
	if got["plclone"] != "one" || got["plcltwo"] != "two" {
		t.Errorf("cleanup repo by session = %v", got)
	}
}

// With a subdir the process enters the lexically cleaned dir (Meta.CWD Joins), so the place is
// judged there too: ~/repos/hop/../app with a subdir runs in ~/repos/app, not where hop leads.
func TestSessionPlaceFollowsTheLaunchRule(t *testing.T) {
	isolateAgentState(t)
	home := homeDir()
	repos := filepath.Join(home, "repos")
	gitInit(t, filepath.Join(repos, "app"))
	gitInit(t, filepath.Join(home, "elsewhere", "app"))
	if err := os.MkdirAll(filepath.Join(home, "elsewhere", "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "elsewhere", "child"), filepath.Join(repos, "hop")); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(repos, "hop") + "/../app"
	src := session.Meta{Name: "plsd", Dir: dir, Subdir: "src", Kind: session.KindShell}
	// The subdir is gone (a branch switch removed it): CWD falls back to the raw dir, and so
	// does the place.
	if cwd, got := src.CWD(), sessionPlaceOf(dir, "src"); cwd != dir || got.Kind != session.PlaceOther {
		t.Errorf("subdir missing: CWD %q, place %+v", cwd, got)
	}
	if err := os.MkdirAll(filepath.Join(repos, "app", "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if cwd, got := src.CWD(), sessionPlaceOf(dir, "src"); cwd != filepath.Join(repos, "app", "src") || *got != (session.Place{Kind: session.PlaceRepo, ID: "app"}) {
		t.Errorf("subdir present: CWD %q, place %+v", cwd, got)
	}
	if got := sessionPlaceOf(dir, ""); got.Kind != session.PlaceOther {
		t.Errorf("without a subdir: %+v", got)
	}
	if got := forkMeta(src, "plsdf", "", "plsd", "").Place; got == nil || got.Kind != session.PlaceRepo {
		t.Errorf("fork with a subdir: %+v", got)
	}
}

// #5268: a link inside a copy, A/current -> A/src, with dir A/current/.. leads back into A at
// create (the physical and lexical readings agree), and into B once the link is pointed at B's
// folder. A ".." that cancels a link is no place, before and after the retarget, while a ".."
// over a real directory keeps its place.
func TestPlaceOfDotDotOverALinkInsideACopy(t *testing.T) {
	isolateAgentState(t)
	repos := filepath.Join(homeDir(), "repos")
	a, b := filepath.Join(repos, "A"), filepath.Join(repos, "B")
	for _, d := range []string{a, b} {
		gitInit(t, d)
		if err := os.MkdirAll(filepath.Join(d, "src"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(a, "current")
	if err := os.Symlink(filepath.Join(a, "src"), link); err != nil {
		t.Fatal(err)
	}
	dir := link + "/.."
	if got := PlaceOf(dir); got.Kind != session.PlaceOther {
		t.Errorf("before the retarget: %+v", got)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(b, "src"), link); err != nil {
		t.Fatal(err)
	}
	if got := PlaceOf(dir); got.Kind != session.PlaceOther {
		t.Errorf("after the retarget: %+v", got)
	}
	if got := PlaceOf(filepath.Join(a, "src") + "/.."); *got != (session.Place{Kind: session.PlaceRepo, ID: "A"}) {
		t.Errorf("a .. over a real directory: %+v", got)
	}
	if got := PlaceOf(filepath.Join(a, "src", "x") + "/../.."); got.Kind != session.PlaceOther {
		t.Errorf("a .. over a missing component: %+v", got)
	}
}
