package chatx

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSuggestReplyPrefs(t *testing.T, body string) {
	t.Helper()
	home := withTempHome(t)
	dir := filepath.Join(home, ".config", "agent-fleet")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ui-prefs.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func suggestRepliesRequest(t *testing.T, id string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/chat/conversations/"+id+"/suggest-replies", nil)
	req.SetPathValue("id", id)
	rr := httptest.NewRecorder()
	HandleChatSuggestReplies(rr, req)
	return rr
}

// Before docs/log/103, the chat's own ✨ read the MIRROR's ui-prefs key (`replySuggestEnabled`)
// through a mismatch that itself dated to day one (docs/log/103-review §0.2: the read side
// spelled it `replySuggest`, which nothing ever wrote, so it always evaluated to the
// missing-key default). The two features are independently switched now
// (`assistantReplySuggestEnabled`): this proves the chat's OWN key gates it...
func TestHandleChatSuggestRepliesGatedByItsOwnKey(t *testing.T) {
	writeSuggestReplyPrefs(t, `{"assistantReplySuggestEnabled":false,"replySuggestEnabled":true}`)
	c := &ChatConversation{ID: RandUUID(), Agent: "claude", Messages: []ChatMessage{
		{Role: "user", Content: "そのまま進めて"},
	}}
	if err := SaveConv(c); err != nil {
		t.Fatal(err)
	}
	rr := suggestRepliesRequest(t, c.ID)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "feature_disabled") {
		t.Fatalf("code = %d body = %s (the mirror's key must not keep the chat's ✨ on)", rr.Code, rr.Body.String())
	}
}

// ...and, the other direction, that the MIRROR's key being off does not silently reach into
// the chat's own toggle — the two are independent switches, not one shared bool wearing two
// names. The conversation is left with no messages so a pass through the feature gate lands on
// the NEXT check (no_content) rather than reaching a real model call — this test's whole point
// is which 400 comes back, not what generation does.
func TestHandleChatSuggestRepliesIgnoresMirrorKey(t *testing.T) {
	writeSuggestReplyPrefs(t, `{"assistantReplySuggestEnabled":true,"replySuggestEnabled":false}`)
	c := &ChatConversation{ID: RandUUID(), Agent: "claude", Messages: []ChatMessage{}}
	if err := SaveConv(c); err != nil {
		t.Fatal(err)
	}
	rr := suggestRepliesRequest(t, c.ID)
	if strings.Contains(rr.Body.String(), "feature_disabled") {
		t.Fatalf("code = %d body = %s (the chat's ✨ must not be gated by the mirror's key)", rr.Code, rr.Body.String())
	}
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "no_content") {
		t.Fatalf("code = %d body = %s (expected to clear the feature gate and stop at no_content)", rr.Code, rr.Body.String())
	}
}

// 103-impl-review 重大5: settings.ts's migrateAiAssistPrefs carries an explicit legacy
// replySuggestEnabled:false to assistantReplySuggestEnabled — but only IN MEMORY, in the
// browser. It reaches ui-prefs.json only the next time the user saves ANY setting (a whole-
// object PUT), so in between, the server sees `assistantReplySuggestEnabled` missing and the
// old key explicitly false. The server must still refuse — otherwise the Console hides the ✨
// button while `POST .../suggest-replies` (reachable by anything holding AGENT_TOKEN) still
// runs, the same failure shape §103.3-3 fixed on the mirror side.
func TestHandleChatSuggestRepliesFallsBackToMirrorKeyBeforeConsoleMigrationLands(t *testing.T) {
	writeSuggestReplyPrefs(t, `{"replySuggestEnabled":false}`)
	c := &ChatConversation{ID: RandUUID(), Agent: "claude", Messages: []ChatMessage{
		{Role: "user", Content: "そのまま進めて"},
	}}
	if err := SaveConv(c); err != nil {
		t.Fatal(err)
	}
	rr := suggestRepliesRequest(t, c.ID)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "feature_disabled") {
		t.Fatalf("code = %d body = %s (an explicit legacy OFF must gate the chat's ✨ until the new key is written)",
			rr.Code, rr.Body.String())
	}
}
