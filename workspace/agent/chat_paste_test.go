package main

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// The assistant chat's image attach rides the same save→reference flow as a terminal
// session (docs/log/19): upload to a conversation-scoped dir, then reference the returned
// absolute path so the chat's headless agent opens it (claude: Read tool / codex:
// view_image — both live-verified). Drive the two endpoints end-to-end over a mux (so
// PathValue routing is real) to prove the wiring, the agent gate (claude/codex allowed,
// opencode rejected — `opencode run` declines images on non-vision models), and the
// save→serve roundtrip.
func TestChatPasteImageRoundtrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	mux := http.NewServeMux()
	mux.HandleFunc("POST /chat/conversations/{id}/paste-image", handleChatPasteImage)
	mux.HandleFunc("GET /chat/conversations/{id}/pasted/{file}", handleChatPastedImage)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	upload := func(t *testing.T, convID string) *http.Response {
		t.Helper()
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		fw, err := mw.CreateFormFile("file", "shot.png") // .png → accepted via filename fallback
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fw.Write([]byte("not-a-real-png-but-the-ext-decides"))
		_ = mw.Close()
		req, _ := http.NewRequest("POST", srv.URL+"/chat/conversations/"+convID+"/paste-image", &body)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	// A claude conversation accepts the upload and serves it back.
	claudeID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	if err := chatx.SaveConv(&chatx.ChatConversation{ID: claudeID, Agent: session.KindClaude}); err != nil {
		t.Fatal(err)
	}
	res := upload(t, claudeID)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("upload status = %d, want 201", res.StatusCode)
	}
	var saved struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(res.Body).Decode(&saved); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	_ = res.Body.Close()
	if saved.Name == "" || !strings.HasSuffix(saved.Name, ".png") {
		t.Fatalf("unexpected saved name %q", saved.Name)
	}

	// The preview endpoint returns the stored bytes.
	get, err := http.Get(srv.URL + "/chat/conversations/" + claudeID + "/pasted/" + saved.Name)
	if err != nil {
		t.Fatal(err)
	}
	if get.StatusCode != http.StatusOK {
		t.Fatalf("preview status = %d, want 200", get.StatusCode)
	}
	if ct := get.Header.Get("Content-Type"); ct != "image/png" {
		t.Fatalf("preview content-type = %q, want image/png", ct)
	}

	// A codex conversation accepts the upload too (codex exec opens the path via
	// view_image — live-verified when the gate was widened).
	codexID := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	if err := chatx.SaveConv(&chatx.ChatConversation{ID: codexID, Agent: session.KindCodex}); err != nil {
		t.Fatal(err)
	}
	if res := upload(t, codexID); res.StatusCode != http.StatusCreated {
		t.Fatalf("codex upload status = %d, want 201", res.StatusCode)
	}

	// An opencode conversation is rejected: `opencode run` declines image input on
	// non-vision models, and the chat can't know the model sees images.
	ocID := "dddddddd-dddd-dddd-dddd-dddddddddddd"
	if err := chatx.SaveConv(&chatx.ChatConversation{ID: ocID, Agent: session.KindOpencode}); err != nil {
		t.Fatal(err)
	}
	if res := upload(t, ocID); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("opencode upload status = %d, want 400", res.StatusCode)
	}

	// An unknown conversation is a 404.
	if res := upload(t, "cccccccc-cccc-cccc-cccc-cccccccccccc"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown-conv upload status = %d, want 404", res.StatusCode)
	}
}
