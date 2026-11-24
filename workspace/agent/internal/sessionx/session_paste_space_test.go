package sessionx

import (
	"bytes"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/spacex"
)

// pasteTo uploads one file to the session through the real handler.
func pasteTo(t *testing.T, name, fileName, content string) *httptest.ResponseRecorder {
	t.Helper()
	// The size cap is main's fsx.MaxUploadBytes, which these tests do not wire; a fixed cap is
	// all the handler needs here.
	prev := deps.MaxUploadBytes
	deps.MaxUploadBytes = func() int64 { return 1 << 20 }
	t.Cleanup(func() { deps.MaxUploadBytes = prev })
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", fileName)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write([]byte(content))
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/sessions/"+name+"/paste-image", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.SetPathValue("name", name)
	rec := httptest.NewRecorder()
	HandlePasteImage(rec, req)
	return rec
}

func TestPasteToASpaceSessionLeavesACopyInTheSpace(t *testing.T) {
	isolateAgentState(t)
	sp, err := spacex.Create("Research")
	if err != nil {
		t.Fatal(err)
	}
	session.WriteMeta(session.Meta{Name: "in-space", Kind: session.KindClaude, Dir: sp.Dir, CreatedAt: "2026-10-29T10:00:00+09:00"})
	rec := pasteTo(t, "in-space", "売上.csv", "a,b\n1,2\n")
	if rec.Code != http.StatusCreated {
		t.Fatalf("paste: %d %s", rec.Code, rec.Body)
	}
	var res struct {
		Path      string `json:"path"`
		SpaceCopy string `json:"spaceCopy"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	// The prompt's path keeps the shape the mirror recognises; the copy is the member's.
	if !strings.Contains(res.Path, "/pasted/") || !strings.Contains(filepath.Base(res.Path), "paste-") {
		t.Fatalf("path = %q", res.Path)
	}
	if want := filepath.Join(sp.Dir, "attachments", "売上.csv"); res.SpaceCopy != want {
		t.Fatalf("spaceCopy = %q, want %q", res.SpaceCopy, want)
	}
	for _, p := range []string{res.Path, res.SpaceCopy} {
		if b, err := os.ReadFile(p); err != nil || string(b) != "a,b\n1,2\n" {
			t.Fatalf("%s = %q, %v", p, b, err)
		}
	}
}

func TestPasteToARepositorySessionLeavesNoCopy(t *testing.T) {
	isolateAgentState(t)
	repo := filepath.Join(homeDir(), "repos", "app")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	session.WriteMeta(session.Meta{Name: "in-repo", Kind: session.KindClaude, Dir: repo, CreatedAt: "2026-10-29T10:00:00+09:00"})
	rec := pasteTo(t, "in-repo", "notes.txt", "x")
	if rec.Code != http.StatusCreated || strings.Contains(rec.Body.String(), "spaceCopy") {
		t.Fatalf("paste: %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(repo, "attachments")); err == nil {
		t.Fatal("an attachments folder appeared in the repository")
	}
}

// An upload that promised a copy in the space and could not make it is not reported as taken, and
// the stored file does not linger without a prompt that names it.
func TestPasteToASpaceSessionFailsWhenTheCopyFails(t *testing.T) {
	isolateAgentState(t)
	sp, err := spacex.Create("Research")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sp.Dir, "attachments"), []byte("a file, not a folder"), 0o644); err != nil {
		t.Fatal(err)
	}
	session.WriteMeta(session.Meta{Name: "in-space", Kind: session.KindClaude, Dir: sp.Dir, CreatedAt: "2026-10-29T10:00:00+09:00"})
	rec := pasteTo(t, "in-space", "a.txt", "x")
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "space_copy_failed") {
		t.Fatalf("paste: %d %s", rec.Code, rec.Body)
	}
	dir := pastedDir(session.UUID(sp.Dir, "in-space"))
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("%d stored file(s) left behind", len(ents))
	}
}

// The copy is made before the upload is published; a publish that then fails must take the copy
// back, or the member finds a file in the space for an attachment the session never received.
func TestPasteToASpaceSessionRollsTheCopyBackWhenPublishingFails(t *testing.T) {
	isolateAgentState(t)
	sp, err := spacex.Create("Research")
	if err != nil {
		t.Fatal(err)
	}
	session.WriteMeta(session.Meta{Name: "in-space", Kind: session.KindClaude, Dir: sp.Dir, CreatedAt: "2026-10-29T10:00:00+09:00"})
	prev := renameFn
	renameFn = func(string, string) error { return errors.New("injected: rename failed") }
	t.Cleanup(func() { renameFn = prev })
	rec := pasteTo(t, "in-space", "a.txt", "x")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("paste: %d %s", rec.Code, rec.Body)
	}
	for _, dir := range []string{filepath.Join(sp.Dir, "attachments"), pastedDir(session.UUID(sp.Dir, "in-space"))} {
		if ents, _ := os.ReadDir(dir); len(ents) != 0 {
			t.Fatalf("%s holds %d file(s) after a failed upload", dir, len(ents))
		}
	}
}

// The space a session ran in can be gone (Clean, or a folder removed by hand; deleting a space
// through the trash shelves its sessions, so they are not pasted to). The upload is then stored
// as for any session and answers 201 with no space copy: the prompt names the stored path, which
// is all the agent needs, and there is no folder left to copy into. Pinned so a change to refuse
// it, or to recreate the folder, is a decision and not an accident (#5085).
func TestPasteToASessionWhoseSpaceIsGoneIsAnOrdinaryUpload(t *testing.T) {
	isolateAgentState(t)
	sp, err := spacex.Create("Research")
	if err != nil {
		t.Fatal(err)
	}
	session.WriteMeta(session.Meta{Name: "in-space", Kind: session.KindClaude, Dir: sp.Dir, CreatedAt: "2026-10-29T10:00:00+09:00"})
	if err := os.RemoveAll(sp.Dir); err != nil {
		t.Fatal(err)
	}
	rec := pasteTo(t, "in-space", "a.txt", "x")
	if rec.Code != http.StatusCreated || strings.Contains(rec.Body.String(), "spaceCopy") {
		t.Fatalf("paste: %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(sp.Dir); err == nil {
		t.Fatal("the paste recreated the removed space")
	}
}
