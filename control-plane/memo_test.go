package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// buildFlushMessage groups by category with a heading each, numbers restart per
// category, file memos surface the ref path (plus comment), text memos the body.
func TestBuildFlushMessage(t *testing.T) {
	memos := []store.Memo{
		{Category: "frontend", Kind: "file", RefPath: "~/repos/a/Button.tsx", Body: "余白を詰めて"},
		{Category: "frontend", Kind: "text", Body: "色も直して"},
		{Category: "api", Kind: "text", Body: "エラーハンドリング追加"},
		{Category: "", Kind: "file", RefPath: "~/repos/a/x.go"},
	}
	got := buildFlushMessage(memos)

	for _, want := range []string{
		"## frontend",
		"1. 対象ファイル: ~/repos/a/Button.tsx",
		"   余白を詰めて",
		"2. 色も直して",
		"## api",
		"1. エラーハンドリング追加",
		"## 未分類",
		"1. 対象ファイル: ~/repos/a/x.go",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("message missing %q\n---\n%s", want, got)
		}
	}
	if strings.Contains(got, "以下のメモを") {
		t.Fatalf("flush message retains the removed instruction template\n%s", got)
	}
	// The api heading restarts numbering at 1 (not continuing frontend's count).
	if strings.Contains(got, "3. エラーハンドリング追加") {
		t.Fatalf("numbering did not restart per category\n%s", got)
	}
}

// A memo with image attachments surfaces the names inline (human-readable) and appends
// the machine-facing "open with Read tool" line with the absolute paths once at the end,
// so the flush target agent opens them. An image-only memo (empty body) still numbers.
func TestBuildFlushMessageAttachments(t *testing.T) {
	atts := func(a ...memoAttachment) string {
		b, _ := json.Marshal(a)
		return string(b)
	}
	memos := []store.Memo{
		{Category: "ui", Kind: "text", Body: "この画面", Attachments: atts(memoAttachment{Path: "/home/dev/.cache/agent-fleet/memo-images/paste-1.png", Name: "paste-1.png"})},
		{Category: "ui", Kind: "text", Body: "", Attachments: atts(memoAttachment{Path: "/home/dev/.cache/agent-fleet/memo-images/paste-2.jpg", Name: "paste-2.jpg"})},
	}
	got := buildFlushMessage(memos)
	for _, want := range []string{
		"1. この画面",
		"   添付画像: paste-1.png",
		"2. （画像）",
		"   添付画像: paste-2.jpg",
		"Open the following file(s) with the Read tool: /home/dev/.cache/agent-fleet/memo-images/paste-1.png /home/dev/.cache/agent-fleet/memo-images/paste-2.jpg",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("flush missing %q\n---\n%s", want, got)
		}
	}
}

// validateMemo accepts an image-only text memo (no body, has attachments) and derives a
// missing attachment name from its path; a text memo with neither body nor attachments
// is rejected.
func TestValidateMemoAttachments(t *testing.T) {
	mv := store.MembershipView{MembershipID: "mem-1"}

	m, aerr := validateMemo(mv, memoDTO{Kind: "text", Attachments: []memoAttachment{{Path: "/x/paste-9.png"}}})
	if aerr != nil {
		t.Fatalf("image-only memo rejected: %#v", aerr)
	}
	var back []memoAttachment
	if err := json.Unmarshal([]byte(m.Attachments), &back); err != nil || len(back) != 1 || back[0].Name != "paste-9.png" {
		t.Fatalf("attachments not normalized: %q (%v)", m.Attachments, err)
	}

	if _, aerr := validateMemo(mv, memoDTO{Kind: "text"}); aerr == nil || aerr.code != "bad_body" {
		t.Fatalf("empty text memo should be rejected, got %#v", aerr)
	}
	if _, aerr := validateMemo(mv, memoDTO{Kind: "text", Attachments: []memoAttachment{{Path: "  "}}}); aerr == nil || aerr.code != "bad_attachment" {
		t.Fatalf("blank attachment path should be rejected, got %#v", aerr)
	}
}

func TestSendMemoPromptUsesSemanticTurn(t *testing.T) {
	const prompt = "まとめたメモ"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sessions/codex_one/turn" {
			t.Errorf("path = %q, want semantic /turn", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization = %q", got)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["op"] != "start" || body["prompt"] != prompt {
			t.Errorf("body = %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sent":"codex_one","op":"start"}`))
	}))
	defer srv.Close()

	rt := stubRuntime{endpoint: srv.URL, token: "tok"}
	if aerr := sendMemoPrompt(context.Background(), rt, "codex_one", prompt); aerr != nil {
		t.Fatalf("sendMemoPrompt: %v", aerr)
	}
}

func TestSendMemoPromptFallsBackForOldAgent(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/sessions/s1/turn":
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		case "/sessions/s1/input":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["prompt"] != "memo" || body["op"] != "" {
				t.Errorf("legacy body = %#v", body)
			}
			_, _ = w.Write([]byte(`{"sent":"s1"}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	if aerr := sendMemoPrompt(context.Background(), stubRuntime{endpoint: srv.URL}, "s1", "memo"); aerr != nil {
		t.Fatalf("sendMemoPrompt: %v", aerr)
	}
	if got := strings.Join(paths, ","); got != "/sessions/s1/turn,/sessions/s1/input" {
		t.Fatalf("paths = %q", got)
	}
}

func TestSendMemoPromptPreservesStructuredAgentError(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"code":"question_pending","message":"answer first"}}`))
	}))
	defer srv.Close()

	aerr := sendMemoPrompt(context.Background(), stubRuntime{endpoint: srv.URL}, "s1", "memo")
	if aerr == nil || aerr.status != http.StatusConflict || aerr.code != "question_pending" {
		t.Fatalf("error = %#v", aerr)
	}
	if calls != 1 {
		t.Fatalf("structured error unexpectedly fell back: calls = %d", calls)
	}
}

// The memo token round-trips to its membership, rejects tampering, a wrong sign key,
// and a foreign membership's tag (the internal memo-bridge auth, memo_bridge.go).
func TestMemoTokenRoundTrip(t *testing.T) {
	key := memoSignKey([]byte("master-key-under-test"))
	tok := mintMemoToken(key, "mem-123")

	if mid, ok := verifyMemoToken(key, tok); !ok || mid != "mem-123" {
		t.Fatalf("round-trip: got (%q,%v), want (mem-123,true)", mid, ok)
	}
	// Bearer-prefixed value must NOT verify (verify takes the raw token; the handler
	// strips "Bearer " first) — guards against accidentally passing the header verbatim.
	if _, ok := verifyMemoToken(key, "Bearer "+tok); ok {
		t.Fatal("verify accepted a Bearer-prefixed token")
	}
	// A different sign key (wrong master) is rejected.
	if _, ok := verifyMemoToken(memoSignKey([]byte("other-master")), tok); ok {
		t.Fatal("verify accepted a token signed by a different key")
	}
	// A tampered membership id with the original tag is rejected (tag binds the id).
	origTag := tok[strings.LastIndexByte(tok, '.')+1:]
	otherBody := strings.TrimPrefix(mintMemoToken(key, "mem-999"), "afm_")
	forged := "afm_" + otherBody[:strings.LastIndexByte(otherBody, '.')] + "." + origTag
	if _, ok := verifyMemoToken(key, forged); ok {
		t.Fatal("verify accepted a forged membership id")
	}
	// The git token and the memo token do not cross-verify (separate credentials).
	if _, ok := verifyMemoToken(key, mintGitToken(gitSignKey([]byte("master-key-under-test")), "mem-123")); ok {
		t.Fatal("verify accepted a git token as a memo token")
	}
}
