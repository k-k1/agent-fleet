package sessionx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/spacex"
)

// queueSpaceBody is a space bucket's queue create as the Control Plane sends it
// (memoDeliveryCreateBody): no dir, no worktree, the space by folder, and the item's work item.
func queueSpaceBody(p *createProbe, folder, key string) map[string]any {
	body := queueBody(p, []map[string]any{{"id": "m-1", "kind": "text", "body": "", "provenance": "member",
		"reference": map[string]any{"provider": "github", "key": "acme/app#12", "url": "https://github.com/acme/app/issues/12"}}})
	body["dir"], body["worktree"] = "", false
	body["space"] = map[string]any{"folder": folder}
	body["work_item"] = map[string]any{"provider": "github", "key": "acme/app#12"}
	body["idempotency_key"] = key
	body["queue_delivery"].(map[string]any)["label"] = "spaces/" + folder + "/impl"
	return body
}

// createdName is the session name a create answered with.
func createdName(body []byte) string {
	var s struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(body, &s)
	return s.Name
}

// A queue create that names a space starts the session in that space, with the work item and
// the queue's delivery on the meta and a space place, and a retry under the same durable key
// replays that session instead of making a second one.
func TestQueueCreateInASpace(t *testing.T) {
	p := newCreateProbe(t)
	sp, err := spacex.Create("app")
	if err != nil {
		t.Fatal(err)
	}
	body := queueSpaceBody(p, sp.Folder, "q_d-space")
	rec, prompt := queueCreate(t, p, body)
	if rec.Code >= 300 || rec.Result().Header.Get(createOutcomeHeader) != "created" {
		t.Fatalf("create = %d %v %s", rec.Code, rec.Result().Header, rec.Body)
	}
	if prompt == "" {
		t.Error("the queue prompt was not delivered")
	}
	m := p.createdMeta()
	if m.Dir != sp.Dir || m.Place == nil || m.Place.Kind != session.PlaceSpace || m.Place.ID != sp.Folder {
		t.Fatalf("meta dir=%q place=%+v, want %s as space %s", m.Dir, m.Place, sp.Dir, sp.Folder)
	}
	if m.WorkItem == nil || m.WorkItem.Key != "acme/app#12" || m.Origin != session.OriginQueue || m.QueueDelivery != "d-123" {
		t.Errorf("meta workItem=%+v origin=%q queueDelivery=%q", m.WorkItem, m.Origin, m.QueueDelivery)
	}
	first := createdName(rec.Body.Bytes())
	again := p.post(body, "")
	if again.Code >= 300 || createdName(again.Body.Bytes()) != first {
		t.Errorf("retry under the same key = %d %s, want the session %s again", again.Code, again.Body, first)
	}
	if n := len(session.ListMetas()); n != 3 { // parent1, kid and the one session
		t.Errorf("%d metas after the retry, want 3", n)
	}
}

// A space that is gone, or being deleted, is refused after the durable key was claimed, so the
// answer carries the key's refused outcome and the Control Plane closes the run as not sent.
func TestQueueCreateInAMissingOrDeletedSpaceIsRefusedUnderTheKey(t *testing.T) {
	p := newCreateProbe(t)
	launchTmuxFn = func(session.Meta, bool) error { return nil }
	reserveDurable(p, "q_d-gone")
	rec := p.post(queueSpaceBody(p, "gone", "q_d-gone"), "")
	if rec.Code != http.StatusNotFound || spaceErrCode(t, rec.Body.Bytes()) != "space_not_found" ||
		rec.Result().Header.Get(createOutcomeHeader) != "refused" {
		t.Fatalf("missing space = %d %v %s, want 404 space_not_found, refused", rec.Code, rec.Result().Header, rec.Body)
	}

	sp, err := spacex.Create("going")
	if err != nil {
		t.Fatal(err)
	}
	end, ok, why := gitx.BeginDelete(sp.Dir, "")
	if !ok {
		t.Fatal(why)
	}
	defer end()
	reserveDurable(p, "q_d-going")
	rec = p.post(queueSpaceBody(p, sp.Folder, "q_d-going"), "")
	if rec.Code != http.StatusConflict || spaceErrCode(t, rec.Body.Bytes()) != gitx.ErrCodeDeleteInProgress() ||
		rec.Result().Header.Get(createOutcomeHeader) != "refused" {
		t.Fatalf("space being deleted = %d %v %s, want 409 delete in progress, refused", rec.Code, rec.Result().Header, rec.Body)
	}
	if n := len(session.ListMetas()); n != 2 {
		t.Errorf("a refused create wrote a meta: %d metas", n)
	}
}

// A queue names a repository by its folder under ~/repos: a dir there that is a symlink (here to
// a space of the same name) or nested deeper is refused under the key, with or without a
// worktree, while the real repository and a create that is not a queue's keep working.
func TestQueueCreateRefusesADirThatIsNotARepository(t *testing.T) {
	p := newCreateProbe(t)
	launchTmuxFn = func(session.Meta, bool) error { return nil }
	sp, err := spacex.Create("notes")
	if err != nil {
		t.Fatal(err)
	}
	// The link carries the space's own folder name: the same-name case of #5222.
	alias := filepath.Join(p.home, "repos", sp.Folder)
	if err := os.Symlink(sp.Dir, alias); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(p.repo, "sub")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	for i, c := range []struct {
		dir      string
		worktree bool
		code     string
	}{
		{"repos/" + sp.Folder, false, "queue_dir_not_repository"},
		{alias, true, "queue_dir_not_repository"},
		{nested, false, "queue_dir_not_repository"},
		// A dir that cannot be read when it is judged is refused, never let through to a later
		// read that a link put in place meanwhile would lead into a space.
		{"repos/absent", false, "bad_dir"},
		{"repos/absent", true, "bad_dir"},
	} {
		key := "q_d-alias-" + string(rune('a'+i))
		reserveDurable(p, key)
		body := queueBody(p, []map[string]any{{"id": "m-1", "kind": "text", "body": "x", "provenance": "member"}})
		body["dir"], body["worktree"], body["idempotency_key"] = c.dir, c.worktree, key
		rec := p.post(body, "")
		if rec.Code != http.StatusBadRequest || spaceErrCode(t, rec.Body.Bytes()) != c.code ||
			rec.Result().Header.Get(createOutcomeHeader) != "refused" {
			t.Errorf("queue create in %s (worktree %v) = %d %v %s, want 400 %s, refused",
				c.dir, c.worktree, rec.Code, rec.Result().Header, rec.Body, c.code)
		}
	}
	if n := len(session.ListMetas()); n != 2 {
		t.Fatalf("a refused create wrote a meta: %d metas", n)
	}

	// The same symlink is fine for a create that is not a queue's (the general dir path), and
	// the real repository is fine for a queue.
	if rec := p.post(map[string]any{"kind": "shell", "dir": alias}, ""); rec.Code >= 300 {
		t.Errorf("a plain create in %s = %d %s", alias, rec.Code, rec.Body)
	}
	rec, _ := queueCreate(t, p, queueBody(p, []map[string]any{{"id": "m-1", "kind": "text", "body": "x", "provenance": "member"}}))
	if rec.Code >= 300 {
		t.Errorf("a queue create in the repository = %d %s", rec.Code, rec.Body)
	}
}

// The list carries the queueSpace marker: without it the Control Plane never sends a space's
// delivery, which an older Agent would start in home.
func TestListEnvelopeCarriesQueueSpace(t *testing.T) {
	newRetireEnv(t)
	rec := httptest.NewRecorder()
	HandleListSessions(rec, httptest.NewRequest(http.MethodGet, "/sessions", nil))
	var env map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || string(env["queueSpace"]) != "1" {
		t.Fatalf("list envelope queueSpace = %s (err %v), want 1", env["queueSpace"], err)
	}
}

// armMeta reads the place it stores again: a repository that passed the checks while the working
// copy was resolved, and was replaced by a link to the space of the same name before the meta is
// made, is refused there instead of launched into. The stages are called directly, so the
// replacement needs no timing.
func TestQueueArmMetaRefusesARepositoryReplacedByALink(t *testing.T) {
	p := newCreateProbe(t)
	sp, err := spacex.Create("app")
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(p.home, "repos", sp.Folder)
	gitInit(t, repo)
	rec := httptest.NewRecorder()
	run := &createRun{w: rec, r: httptest.NewRequest(http.MethodPost, "/sessions", nil), name: "qarm0001",
		origin: session.OriginQueue, req: &CreateReq{Dir: "repos/" + sp.Folder, QueueDelivery: &QueueDelivery{ID: "d1", Queue: "cat-1"}}}
	if !run.resolveWorkingCopy() {
		t.Fatalf("the real repository was refused: %d %s", rec.Code, rec.Body)
	}
	if err := os.Rename(repo, repo+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sp.Dir, repo); err != nil {
		t.Fatal(err)
	}
	if _, ok := run.armMeta(); ok || rec.Code != http.StatusBadRequest || spaceErrCode(t, rec.Body.Bytes()) != "queue_dir_not_repository" {
		t.Fatalf("armMeta after the swap: ok=%v %d %s, want 400 queue_dir_not_repository", ok, rec.Code, rec.Body)
	}
}
