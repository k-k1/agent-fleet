package sessionx

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

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

// A space and a repository that share a folder name never share a place: neither the kind nor
// the meta's stored value lets a client file one under the other.
func TestPlaceSpaceAndRepositoryOfOneNameStayApart(t *testing.T) {
	p := newCreateProbe(t)
	launchTmuxFn = func(session.Meta, bool) error { return nil }
	spaceDir := placeSpace(t, "app") // newCreateProbe's repository is ~/repos/app
	for _, body := range []map[string]any{
		{"kind": "shell", "dir": p.repo},
		{"kind": "shell", "space": map[string]any{"folder": "app"}},
	} {
		if rec := p.post(body, ""); rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
			t.Fatalf("create %v: %d %s", body, rec.Code, rec.Body)
		}
	}
	places := map[string]*session.Place{}
	for _, m := range session.ListMetas() {
		if m.Dir == p.repo || m.Dir == spaceDir {
			places[m.Dir] = m.Place
		}
	}
	repo, space := places[p.repo], places[spaceDir]
	if repo == nil || *repo != (session.Place{Kind: session.PlaceRepo, ID: "app"}) {
		t.Errorf("repository session place = %+v", repo)
	}
	if space == nil || *space != (session.Place{Kind: session.PlaceSpace, ID: "app"}) {
		t.Errorf("space session place = %+v", space)
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
	if got := forkMeta(session.Meta{Name: "plsrc", Dir: spaceDir, Kind: session.KindShell}, "plfork", "", "plsrc", "").Place; got == nil || *got != want {
		t.Errorf("fork place = %+v, want %+v", got, want)
	}

	session.WriteMeta(session.Meta{Name: "plrecr", Dir: spaceDir, Kind: session.KindShell})
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
}
