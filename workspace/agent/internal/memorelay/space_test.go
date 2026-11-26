package memorelay

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/spacex"
)

// createdRepoFrom is createdRepo for a create the session from makes.
func createdRepoFrom(t *testing.T, cp *fakeCP, from, repo string) string {
	t.Helper()
	cp.bodies = nil
	rec := serve(HandleCreate, http.MethodPost, "/memos",
		`{"from":`+quote(from)+`,"memo":{"kind":"text","body":"x","repo":`+quote(repo)+`}}`)
	if rec.Code != http.StatusOK || len(cp.bodies) != 1 {
		t.Fatalf("create(%q from %q) answered %d %s with %d CP calls", repo, from, rec.Code, rec.Body.String(), len(cp.bodies))
	}
	return decode(t, cp.bodies[0])["repo"].(string)
}

// spaceSessions writes a session in the space app and one in the repository app, which share
// the folder name, next to the real clone ~/repos/app that reposHome made.
func spaceSessions(t *testing.T) {
	t.Helper()
	root := reposHome(t)
	session.WriteMeta(session.Meta{Name: "inspace", Kind: session.KindClaude, Origin: session.OriginUser,
		Dir: filepath.Join(filepath.Dir(root), "spaces", "app"), Place: &session.Place{Kind: session.PlaceSpace, ID: "app"}})
	session.WriteMeta(session.Meta{Name: "inrepo", Kind: session.KindClaude, Origin: session.OriginUser,
		Dir: filepath.Join(root, "app"), Place: &session.Place{Kind: session.PlaceRepo, ID: "app"}})
}

// A session in the space app that names "app" files the memo under the space, even though a
// repository app exists; the same name from a session in that repository stays the repository.
func TestCreateFromASpaceFilesUnderTheSpaceNotTheSameNamedRepository(t *testing.T) {
	cp := newFakeCP(t)
	spaceSessions(t)
	for _, c := range []struct{ from, in, want string }{
		{"inspace", "app", "spaces/app"},
		{"inspace", " app ", "spaces/app"}, // the Control Plane trims, so the relay compares trimmed
		{"inspace", "spaces/app", "spaces/app"},
		{"inspace", "", ""},           // unfiled stays unfiled
		{"inspace", "app@one", "app"}, // another name is still a repository
		{"inrepo", "app", "app"},      // the repository of that name
		{"inrepo", "spaces/app", "spaces/app"},
		{"", "app", "app"}, // the operator has no place
	} {
		if got := createdRepoFrom(t, cp, c.from, c.in); got != c.want {
			t.Errorf("create repo=%q from %q relayed %q, want %q", c.in, c.from, got, c.want)
		}
	}
}

// A meta written before places were stored has no Place: the space marker under its directory
// decides, never the folder name.
func TestCreateFromALegacySpaceSessionUsesTheMarker(t *testing.T) {
	cp := newFakeCP(t)
	root := reposHome(t)
	sp, err := spacex.Create("app")
	if err != nil {
		t.Fatal(err)
	}
	session.WriteMeta(session.Meta{Name: "oldspace", Kind: session.KindClaude, Origin: session.OriginUser,
		Dir: filepath.Join(spacex.Root(), sp.Folder)})
	session.WriteMeta(session.Meta{Name: "oldrepo", Kind: session.KindClaude, Origin: session.OriginUser,
		Dir: filepath.Join(root, "app")})
	if got := createdRepoFrom(t, cp, "oldspace", sp.Folder); got != "spaces/"+sp.Folder {
		t.Errorf("legacy space session relayed %q, want spaces/%s", got, sp.Folder)
	}
	if got := createdRepoFrom(t, cp, "oldrepo", "app"); got != "app" {
		t.Errorf("legacy repository session relayed %q, want app", got)
	}
}

// update_memo sends its own session as ?from= with a move: a space session moving a memo to its
// folder name moves it to the space. Without from it is the repository; a move with an unknown
// from is refused.
func TestUpdateFromASpaceFilesUnderTheSpace(t *testing.T) {
	cp := newFakeCP(t)
	spaceSessions(t)
	for _, c := range []struct{ target, body, want string }{
		{"/memos/m1?from=inspace", `{"repo":"app"}`, `{"repo":"spaces/app"}`},
		{"/memos/m1?from=inspace", `{"repo":" app "}`, `{"repo":"spaces/app"}`},
		{"/memos/m1?from=inrepo", `{"repo":"app"}`, `{"repo":"app"}`},
		{"/memos/m1", `{"repo":"app"}`, `{"repo":"app"}`},
		// Nothing but a move to a named bucket reads the session, so a from that names no session
		// stops neither another edit nor an unfiling.
		{"/memos/m1?from=nobody", `{"body":"b"}`, `{"body":"b"}`},
		{"/memos/m1?from=nobody", `{"repo":""}`, `{"repo":""}`},
	} {
		cp.bodies = nil
		rec := serve(HandleUpdate, http.MethodPatch, c.target, c.body)
		if rec.Code != http.StatusOK || len(cp.bodies) != 1 || cp.bodies[0] != c.want {
			t.Errorf("PATCH %s %s: %d, relayed %v, want %s", c.target, c.body, rec.Code, cp.bodies, c.want)
		}
	}
	cp.bodies = nil
	if rec := serve(HandleUpdate, http.MethodPatch, "/memos/m1?from=nobody", `{"repo":"app"}`); rec.Code != http.StatusBadRequest || len(cp.bodies) != 0 {
		t.Errorf("unknown from: %d with %d CP calls, want 400 and none", rec.Code, len(cp.bodies))
	}
}
