package sessionx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/spacex"
)

// errCode reads the refusal code of a create's answer.
func spaceErrCode(t *testing.T, body []byte) string {
	t.Helper()
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	return e.Error.Code
}

func spaceFolders(t *testing.T) []string {
	t.Helper()
	sps, err := spacex.List()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, s := range sps {
		out = append(out, s.Folder)
	}
	return out
}

func TestCreateInANewSpace(t *testing.T) {
	p := newCreateProbe(t)
	launchTmuxFn = func(session.Meta, bool) error { return nil }
	rec := p.post(map[string]any{"kind": "shell", "space": map[string]any{"name": "Research notes"}}, "")
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	sps, _ := spacex.List()
	if len(sps) != 1 || sps[0].Name != "Research notes" {
		t.Fatalf("spaces = %+v", sps)
	}
	var made *session.Meta
	for _, m := range session.ListMetas() {
		if m.Dir == sps[0].Dir {
			m := m
			made = &m
		}
	}
	if made == nil {
		t.Fatalf("no session runs in %s", sps[0].Dir)
	}
	if got := wireSession(*made, false).Space; got != sps[0].Folder {
		t.Fatalf("wire space = %q, want %q", got, sps[0].Folder)
	}
}

func TestCreateInAnExistingSpace(t *testing.T) {
	p := newCreateProbe(t)
	launchTmuxFn = func(session.Meta, bool) error { return nil }
	sp, err := spacex.Create("kept")
	if err != nil {
		t.Fatal(err)
	}
	rec := p.post(map[string]any{"kind": "shell", "space": map[string]any{"folder": sp.Folder}}, "")
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	if got := spaceFolders(t); len(got) != 1 || got[0] != sp.Folder {
		t.Fatalf("a second space was made: %v", got)
	}
	found := false
	for _, m := range session.ListMetas() {
		found = found || m.Dir == sp.Dir
	}
	if !found {
		t.Fatal("no session runs in the existing space")
	}
}

// A create that made a space and then failed keeps the space: it is listed from the moment its
// marker exists, so another create may already run in it, and removing it is never safe. It
// leaves no session behind.
func TestCreateInANewSpaceThatFailsKeepsTheSpace(t *testing.T) {
	p := newCreateProbe(t)
	metas := len(session.ListMetas())
	rec := p.post(map[string]any{"kind": "shell", "space": map[string]any{"name": "kept"}}, "")
	if rec.Code < 400 {
		t.Fatalf("the probe's launch was meant to fail: %d %s", rec.Code, rec.Body)
	}
	sps, _ := spacex.List()
	if len(sps) != 1 || sps[0].Name != "kept" {
		t.Fatalf("spaces = %+v, want the one the create made", sps)
	}
	if got := len(session.ListMetas()); got != metas {
		t.Fatalf("the failed create left a meta: %d -> %d", metas, got)
	}
}

// An existing space is never removed by a failed create, even an empty one.
func TestCreateInAnExistingSpaceThatFailsKeepsIt(t *testing.T) {
	p := newCreateProbe(t)
	sp, _ := spacex.Create("kept")
	rec := p.post(map[string]any{"kind": "shell", "space": map[string]any{"folder": sp.Folder}}, "")
	if rec.Code < 400 {
		t.Fatalf("the probe's launch was meant to fail: %d %s", rec.Code, rec.Body)
	}
	if _, ok := spacex.Lookup(sp.Folder); !ok {
		t.Fatal("a failed create removed a space it did not make")
	}
}

func TestCreateSpaceRefusals(t *testing.T) {
	cases := []struct {
		name string
		body func(p *createProbe) map[string]any
		code string
	}{
		{"with_dir", func(p *createProbe) map[string]any {
			return map[string]any{"dir": p.repo, "space": map[string]any{"name": "x"}}
		}, "bad_space"},
		{"with_worktree", func(p *createProbe) map[string]any {
			return map[string]any{"worktree": true, "space": map[string]any{"name": "x"}}
		}, "bad_space"},
		{"with_remote_url", func(p *createProbe) map[string]any {
			return map[string]any{"remote_url": "https://example.invalid/x.git", "space": map[string]any{"name": "x"}}
		}, "bad_space"},
		{"ssm", func(p *createProbe) map[string]any {
			return map[string]any{"kind": "ssm", "ssm_target": "i-0", "space": map[string]any{"name": "x"}}
		}, "bad_space"},
		{"folder_and_name", func(p *createProbe) map[string]any {
			sp, _ := spacex.Create("a")
			return map[string]any{"space": map[string]any{"folder": sp.Folder, "name": "b"}}
		}, "bad_space"},
		{"bad_name", func(p *createProbe) map[string]any {
			return map[string]any{"space": map[string]any{"name": "a\u0007b"}}
		}, "bad_space"},
		{"missing_folder", func(p *createProbe) map[string]any {
			return map[string]any{"space": map[string]any{"folder": "nope-abcdef"}}
		}, "space_not_found"},
		{"folder_outside_root", func(p *createProbe) map[string]any {
			return map[string]any{"space": map[string]any{"folder": "../repos/app"}}
		}, "space_not_found"},
		{"unmarked_folder", func(p *createProbe) map[string]any {
			_ = os.MkdirAll(filepath.Join(p.home, "spaces", "by-hand"), 0o755)
			return map[string]any{"space": map[string]any{"folder": "by-hand"}}
		}, "space_not_found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newCreateProbe(t)
			body := tc.body(p)
			before := spaceFolders(t)
			metas := len(session.ListMetas())
			rec := p.post(body, "")
			if got := spaceErrCode(t, rec.Body.Bytes()); got != tc.code {
				t.Fatalf("code = %q (%d %s), want %q", got, rec.Code, rec.Body, tc.code)
			}
			if after := spaceFolders(t); len(after) != len(before) {
				t.Fatalf("a refused create changed the spaces: %v -> %v", before, after)
			}
			if got := len(session.ListMetas()); got != metas {
				t.Fatalf("a refused create wrote a meta: %d -> %d", metas, got)
			}
		})
	}
}

// A space whose delete has decided is going away: a session started in it now would run in a folder
// that is gone, so the create is refused and no meta is written. Once the delete is over the
// refusal is gone with the mark.
func TestCreateInASpaceBeingDeletedIsRefused(t *testing.T) {
	p := newCreateProbe(t)
	launchTmuxFn = func(session.Meta, bool) error { return nil }
	sp, err := spacex.Create("going")
	if err != nil {
		t.Fatal(err)
	}
	end, ok, why := gitx.BeginDelete(sp.Dir, "")
	if !ok {
		t.Fatal(why)
	}
	metas := len(session.ListMetas())
	rec := p.post(map[string]any{"kind": "shell", "space": map[string]any{"folder": sp.Folder}}, "")
	if got := spaceErrCode(t, rec.Body.Bytes()); got != gitx.ErrCodeDeleteInProgress() || rec.Code != http.StatusConflict {
		t.Fatalf("create in a space being deleted: %d %s", rec.Code, rec.Body)
	}
	if got := len(session.ListMetas()); got != metas {
		t.Fatalf("the refused create wrote a meta: %d -> %d", metas, got)
	}
	end()
	rec = p.post(map[string]any{"kind": "shell", "space": map[string]any{"folder": sp.Folder}}, "")
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("create after the delete ended: %d %s", rec.Code, rec.Body)
	}
}

// A launch into an existing space where an AI session is running is refused with 409 space_busy
// naming that session, unless the request says "here anyway" (space.allow_busy). Shells, stopped
// and archived sessions do not make a space busy, and the refusal leaves no meta behind (#5230).
func TestCreateInABusySpace(t *testing.T) {
	p := newCreateProbe(t)
	launchTmuxFn = func(session.Meta, bool) error { return nil }
	sp, err := spacex.Create("shared")
	if err != nil {
		t.Fatal(err)
	}
	other, err := spacex.Create("other")
	if err != nil {
		t.Fatal(err)
	}
	p.meta(session.Meta{Name: "term", Kind: session.KindShell, Dir: sp.Dir, Origin: session.OriginUser})
	p.meta(session.Meta{Name: "old", Kind: session.KindCodex, Dir: sp.Dir, Origin: session.OriginUser})
	p.meta(session.Meta{Name: "gone", Kind: session.KindClaude, Dir: sp.Dir, Origin: session.OriginUser, Archived: true})
	p.meta(session.Meta{Name: "away", Kind: session.KindClaude, Dir: other.Dir, Origin: session.OriginUser})
	live := map[string]bool{"term": true, "gone": true, "away": true}
	sessionAliveFn = func(m session.Meta) bool { return live[m.Name] }
	post := func(space map[string]any) *httptest.ResponseRecorder {
		return p.post(map[string]any{"kind": "claude", "space": space}, "")
	}

	// A live shell, a stopped agent, an archived one and an agent in another space: not busy.
	rec := post(map[string]any{"folder": sp.Folder})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create next to a shell and stopped sessions = %d %s, want 201", rec.Code, rec.Body)
	}
	var made session.Session
	_ = json.Unmarshal(rec.Body.Bytes(), &made)

	// Now an agent runs there: the next launch is refused and names it, title included when set.
	live[made.Name] = true
	third, err := spacex.Create("third")
	if err != nil {
		t.Fatal(err)
	}
	p.meta(session.Meta{Name: "titled", Kind: session.KindCodex, Dir: third.Dir, Title: "Notes run", Origin: session.OriginSchedule})
	live["titled"] = true
	for _, tc := range []struct {
		sp                   spacex.Space
		wantSession, wantTit string
	}{{sp, made.Name, ""}, {third, "titled", "Notes run"}} {
		metas := len(session.ListMetas())
		rec = post(map[string]any{"folder": tc.sp.Folder})
		var e struct {
			Error struct{ Code, Message, Session, Title, Display string } `json:"error"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &e)
		if rec.Code != http.StatusConflict || e.Error.Code != "space_busy" {
			t.Fatalf("create in %s = %d %s, want 409 space_busy", tc.sp.Folder, rec.Code, rec.Body)
		}
		if e.Error.Session != tc.wantSession || e.Error.Title != tc.wantTit || e.Error.Display == "" ||
			(tc.wantTit != "" && e.Error.Display != tc.wantTit) || !strings.Contains(e.Error.Message, tc.wantSession) {
			t.Fatalf("refusal names %+v, want session %q titled %q", e.Error, tc.wantSession, tc.wantTit)
		}
		if got := len(session.ListMetas()); got != metas {
			t.Fatalf("the refused create wrote a meta: %d -> %d", metas, got)
		}
	}

	// A new shell is no second agent: it opens next to the running one without the opt-in.
	if rec = p.post(map[string]any{"kind": "shell", "space": map[string]any{"folder": sp.Folder}}, ""); rec.Code != http.StatusCreated {
		t.Fatalf("a shell in a busy space = %d %s, want 201", rec.Code, rec.Body)
	}

	// The explicit opt-in starts it anyway.
	rec = post(map[string]any{"folder": sp.Folder, "allow_busy": true})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create with allow_busy = %d %s, want 201", rec.Code, rec.Body)
	}
	// A new space is nobody's: never busy.
	rec = post(map[string]any{"name": "fresh"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create in a new space = %d %s, want 201", rec.Code, rec.Body)
	}
}

// A retried create whose first attempt already started a session in the space replays that
// session from the ledger: the session it started must not turn its own retry into space_busy.
func TestCreateInABusySpaceReplaysTheKeyedCreate(t *testing.T) {
	p := newCreateProbe(t)
	launches := 0
	launchTmuxFn = func(session.Meta, bool) error { launches++; return nil }
	sp, err := spacex.Create("kept")
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"kind": "claude", "space": map[string]any{"folder": sp.Folder}, "idempotency_key": "space-retry"}
	rec := p.post(body, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("first create = %d %s, want 201", rec.Code, rec.Body)
	}
	var first session.Session
	_ = json.Unmarshal(rec.Body.Bytes(), &first)
	sessionAliveFn = func(m session.Meta) bool { return m.Name == first.Name }
	rec = p.post(body, "")
	var again session.Session
	_ = json.Unmarshal(rec.Body.Bytes(), &again)
	if rec.Code != http.StatusOK || again.Name != first.Name {
		t.Fatalf("retry = %d %s, want 200 replaying %s", rec.Code, rec.Body, first.Name)
	}
	if launches != 1 {
		t.Fatalf("launches = %d, want 1", launches)
	}
}

// The busy test compares where a session really runs. A recorded dir that goes through a symlink
// and then `..` lands above the link's target, not next to the link: it makes the space busy only
// when that real place is the space.
func TestCreateInABusySpaceFollowsTheLinkBeforeDotDot(t *testing.T) {
	p := newCreateProbe(t)
	launchTmuxFn = func(session.Meta, bool) error { return nil }
	sp, err := spacex.Create("notes")
	if err != nil {
		t.Fatal(err)
	}
	// ~/spaces/link -> ~/elsewhere/child, and ~/elsewhere has a folder named like the space.
	elsewhere := filepath.Join(p.home, "elsewhere")
	for _, d := range []string{filepath.Join(elsewhere, "child"), filepath.Join(elsewhere, sp.Folder)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(elsewhere, "child"), filepath.Join(spacex.Root(), "link")); err != nil {
		t.Fatal(err)
	}
	// Spelt like the space, runs in ~/elsewhere/<folder>: not busy. (Concatenated, not Joined:
	// Join would clean the `..` away before the code under test sees it.)
	p.meta(session.Meta{Name: "away", Kind: session.KindClaude, Dir: spacex.Root() + "/link/../" + sp.Folder, Origin: session.OriginUser})
	// ~/elsewhere/back -> the space.
	if err := os.Symlink(sp.Dir, filepath.Join(elsewhere, "back")); err != nil {
		t.Fatal(err)
	}
	live := map[string]bool{"away": true}
	sessionAliveFn = func(m session.Meta) bool { return live[m.Name] }
	rec := p.post(map[string]any{"kind": "claude", "space": map[string]any{"folder": sp.Folder}}, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create next to a session in another folder = %d %s, want 201", rec.Code, rec.Body)
	}
	var made session.Session
	_ = json.Unmarshal(rec.Body.Bytes(), &made)
	if err := os.Remove(session.MetaPath(made.Name)); err != nil {
		t.Fatal(err)
	}
	// Spelt as ~/spaces/back (which does not exist), runs in ~/elsewhere/back, the space: busy.
	p.meta(session.Meta{Name: "here", Kind: session.KindClaude, Dir: spacex.Root() + "/link/../back", Origin: session.OriginUser})
	live["here"] = true
	rec = p.post(map[string]any{"kind": "claude", "space": map[string]any{"folder": sp.Folder}}, "")
	if rec.Code != http.StatusConflict || spaceErrCode(t, rec.Body.Bytes()) != "space_busy" || !strings.Contains(rec.Body.String(), `"session":"here"`) {
		t.Fatalf("create next to a session that runs in the space = %d %s, want 409 space_busy naming here", rec.Code, rec.Body)
	}

	// With a Subdir the launch Joins first, which drops `link/..` lexically: the same spelling as
	// "away" then runs in the space itself (Meta.CWD), and the space is busy.
	live["here"] = false
	if err := os.MkdirAll(filepath.Join(sp.Dir, "work"), 0o755); err != nil {
		t.Fatal(err)
	}
	p.meta(session.Meta{Name: "sub", Kind: session.KindClaude, Dir: spacex.Root() + "/link/../" + sp.Folder, Subdir: "work", Origin: session.OriginUser})
	live["sub"] = true
	rec = p.post(map[string]any{"kind": "claude", "space": map[string]any{"folder": sp.Folder}}, "")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"session":"sub"`) {
		t.Fatalf("create next to a subdir session that runs in the space = %d %s, want 409 space_busy naming sub", rec.Code, rec.Body)
	}
}
