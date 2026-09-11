package sessionx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// setTitle drives the rename endpoint the way both of its writers do: the Console sends no
// title_set_by at all, a spawning parent sends "parent".
func setTitle(t *testing.T, name, title, setBy string) *httptest.ResponseRecorder {
	t.Helper()
	payload := map[string]string{"title": title}
	if setBy != "" {
		payload["title_set_by"] = setBy
	}
	body, _ := json.Marshal(payload)
	r := httptest.NewRequest(http.MethodPost, "/sessions/"+name+"/title/set", strings.NewReader(string(body)))
	r.SetPathValue("name", name)
	w := httptest.NewRecorder()
	HandleSetTitle(w, r)
	return w
}

// The whole point of TitleSetBy: two writers on one field, ordered rather than arbitrated. The
// user's rename always lands and closes the title; the parent's only lands while nobody has
// (ADR 0073 decision 4, amendment 2026-09-11).
func TestParentRenameYieldsToTheUsersRename(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	const name = "child1"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude,
		Origin: session.OriginSession, OriginSession: "parent1"})

	// Nobody has named it yet, so the parent may.
	if got := setTitle(t, name, "分割した作業", session.TitleSetByParent); got.Code != http.StatusOK {
		t.Fatalf("the first parent rename was refused: status=%d body=%s", got.Code, got.Body.String())
	}
	m, _ := session.ReadMeta(name)
	if m.Title != "分割した作業" || m.TitleSetBy != session.TitleSetByParent {
		t.Fatalf("after a parent rename: title=%q setBy=%q", m.Title, m.TitleSetBy)
	}

	// A parent may rewrite its OWN title — the reuse this tool exists for is a child whose task
	// changed twice.
	if got := setTitle(t, name, "二つ目の作業", session.TitleSetByParent); got.Code != http.StatusOK {
		t.Fatalf("a parent could not rewrite its own title: status=%d body=%s", got.Code, got.Body.String())
	}

	// The user's rename overrides the parent's, always.
	if got := setTitle(t, name, "利用者が付けた名前", ""); got.Code != http.StatusOK {
		t.Fatalf("the user's rename was refused: status=%d body=%s", got.Code, got.Body.String())
	}
	m, _ = session.ReadMeta(name)
	if m.Title != "利用者が付けた名前" || m.TitleSetBy != session.TitleSetByUser {
		t.Fatalf("after the user's rename: title=%q setBy=%q", m.Title, m.TitleSetBy)
	}

	// And from there the parent is out. The refusal has to name the reason, not just fail: the
	// caller is a model that will otherwise retry.
	got := setTitle(t, name, "親が奪い返す", session.TitleSetByParent)
	if got.Code != http.StatusConflict {
		t.Fatalf("the parent overwrote a title the user chose: status=%d body=%s", got.Code, got.Body.String())
	}
	if body := got.Body.String(); !strings.Contains(body, "title_set_by_user") || !strings.Contains(body, "利用者") {
		t.Errorf("the refusal does not say why: %s", body)
	}
	if m, _ := session.ReadMeta(name); m.Title != "利用者が付けた名前" {
		t.Fatalf("the refused rename still wrote: %q", m.Title)
	}
}

// Clearing the title is the user reverting to the auto label, so it puts the session back in the
// state a fresh one is in — parent included. Without this the only way out of "the parent may
// never rename this again" would be a field no UI can reach.
func TestClearingTheTitleReopensItToTheParent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	const name = "child2"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude,
		Origin: session.OriginSession, OriginSession: "parent1"})

	setTitle(t, name, "利用者が付けた名前", "")
	if got := setTitle(t, name, "", ""); got.Code != http.StatusOK {
		t.Fatalf("clearing the title was refused: status=%d body=%s", got.Code, got.Body.String())
	}
	if m, _ := session.ReadMeta(name); m.TitleSetBy != "" {
		t.Fatalf("a cleared title still claims an author: %q", m.TitleSetBy)
	}
	if got := setTitle(t, name, "親が付け直す", session.TitleSetByParent); got.Code != http.StatusOK {
		t.Fatalf("the parent was still locked out after the title was cleared: status=%d body=%s", got.Code, got.Body.String())
	}

	// The other direction: a PARENT may not clear it. Reverting to the auto label is the user's
	// affordance, and a parent sending an empty title has lost the name rather than chosen one.
	if got := setTitle(t, name, "", session.TitleSetByParent); got.Code != http.StatusBadRequest {
		t.Fatalf("a parent cleared the title: status=%d body=%s", got.Code, got.Body.String())
	}
	if m, _ := session.ReadMeta(name); m.Title != "親が付け直す" {
		t.Fatalf("the refused clear still wrote: %q", m.Title)
	}
}

// Accepting the suggestion banner is the user choosing that name, so it has to close the title to
// the parent exactly as the rename dialog does. Otherwise the one title a parent CAN overwrite is
// the one the user just pressed accept on.
func TestAcceptingASuggestedTitleCountsAsTheUsers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	const name = "child3"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude,
		Origin: session.OriginSession, OriginSession: "parent1", SuggestedTitle: "LLM が考えた名前"})

	r := httptest.NewRequest(http.MethodPost, "/sessions/"+name+"/title/accept", nil)
	r.SetPathValue("name", name)
	w := httptest.NewRecorder()
	HandleAcceptSuggestedTitle(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("accept status=%d body=%s", w.Code, w.Body.String())
	}
	if m, _ := session.ReadMeta(name); m.TitleSetBy != session.TitleSetByUser {
		t.Fatalf("an accepted suggestion claims author %q, want the user", m.TitleSetBy)
	}
	if got := setTitle(t, name, "親が奪う", session.TitleSetByParent); got.Code != http.StatusConflict {
		t.Fatalf("the parent overwrote an accepted suggestion: status=%d body=%s", got.Code, got.Body.String())
	}
}

// The wire carries it (ADR 0073 decision 4): a field the Agent stores but does not send is one
// the CP relay and the Console can never see, and this whole family of keys has been dropped in
// that relay before.
func TestTitleSetByRidesTheSessionWire(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	m := session.Meta{Name: "child4", Dir: t.TempDir(), Kind: session.KindClaude,
		Title: "利用者が付けた名前", TitleSetBy: session.TitleSetByUser}
	if got := wireSession(m, false).TitleSetBy; got != session.TitleSetByUser {
		t.Fatalf("wireSession dropped titleSetBy: %q", got)
	}
	raw, _ := json.Marshal(wireSession(m, false))
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	if out["titleSetBy"] != session.TitleSetByUser {
		t.Fatalf("the json key titleSetBy is not on the wire: %s", raw)
	}
	// Absent rather than empty for a session nobody has renamed, so the Console's optional
	// declaration and the CP's omitempty agree with what is actually sent.
	raw, _ = json.Marshal(wireSession(session.Meta{Name: "child5", Kind: session.KindClaude}, false))
	out = nil // unmarshalling into a live map MERGES, so the previous row's key would survive
	_ = json.Unmarshal(raw, &out)
	if _, present := out["titleSetBy"]; present {
		t.Fatalf("an unrenamed session claims an author: %s", raw)
	}
}
