package main

// The re-authentication route, end to end against a server that really refuses.
//
// The bug being pinned shut is a STATE, not a line of code: a working copy checked out
// without saving credentials. Every later update fails, and before this route there was
// nowhere to put the password. So the test starts from that state — check out with a
// credential, then forget it — and walks the way out of it.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

func TestSvnReauthenticateWorkingCopy(t *testing.T) {
	if !svnAvailable() {
		t.Skip("svn not installed")
	}
	if _, err := exec.LookPath("svnadmin"); err != nil {
		t.Skip("svnadmin not installed")
	}
	const user, pass = "alice", "s3cret"
	srvRoot := t.TempDir()
	base := startSvnserve(t, srvRoot, user, pass)
	trunk := base + "/trunk"

	seed := filepath.Join(srvRoot, "seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "hello.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	creds := &secrets.SVNCred{URLPrefix: base, Username: user, Password: pass}
	if out, err := runSvnAuthed(t.Context(), creds, "import", "-m", "seed commit", seed, trunk); err != nil {
		t.Fatalf("seed import: %v: %s", err, out)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SECRET_KEY", "")
	wc := filepath.Join(gitx.ReposRoot(), "docs")
	if out, err := runSvnAuthedHealing(t.Context(), wc, creds, "checkout", trunk, wc); err != nil {
		t.Fatalf("checkout: %v: %s", err, out)
	}
	// …and nothing was saved, which is exactly what declining the checkout opt-in leaves.
	if c := svnCredsFor(trunk); c != nil {
		t.Fatalf("expected an empty store, got %+v", c)
	}

	// 1. Update says WHY it failed, in a code the Console can act on. Before this, every
	//    failure was one indistinguishable "update_failed".
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/repos/docs/svn-update", nil)
	req.SetPathValue("name", "docs")
	handleSvnUpdate(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("update status = %d, want 401: %s", rec.Code, rec.Body.String())
	}
	if code := errCodeOf(t, rec); code != errCodeSvnAuth {
		t.Fatalf("update error code = %q, want %q", code, errCodeSvnAuth)
	}

	// 2. What the dialog opens with: which server, and "no credential yet".
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/repos/docs/svn-auth", nil)
	req.SetPathValue("name", "docs")
	handleGetSvnAuth(rec, req)
	var info struct {
		URL       string `json:"url"`
		URLPrefix string `json:"urlPrefix"`
		HasCred   bool   `json:"hasCred"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatalf("decode: %v: %s", err, rec.Body.String())
	}
	if info.URL != trunk || info.HasCred {
		t.Fatalf("svn-auth info = %+v, want url=%q hasCred=false", info, trunk)
	}
	if info.URLPrefix != base {
		t.Errorf("urlPrefix = %q, want the repository root %q", info.URLPrefix, base)
	}

	// 3. A wrong password is refused AND NOT STORED — the whole point of verifying before
	//    saving is that "saved" cannot come to mean "saved a typo".
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/repos/docs/svn-auth", strings.NewReader(`{"username":"alice","password":"wrong"}`))
	req.SetPathValue("name", "docs")
	handleSvnAuth(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad password status = %d, want 401: %s", rec.Code, rec.Body.String())
	}
	if c := svnCredsFor(trunk); c != nil {
		t.Fatalf("a rejected credential was stored anyway: %+v", c)
	}

	// 4. The right one is stored, under the repository root so every subtree of it is
	//    covered by the one entry.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/repos/docs/svn-auth", strings.NewReader(`{"username":"alice","password":"s3cret"}`))
	req.SetPathValue("name", "docs")
	handleSvnAuth(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("re-auth status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	stored := svnCredsFor(trunk)
	if stored == nil || stored.Username != user || stored.Password != pass || stored.URLPrefix != base {
		t.Fatalf("stored = %+v, want %s/%s under %s", stored, user, pass, base)
	}

	// 5. And the operation that sent the user here now works.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/repos/docs/svn-update", nil)
	req.SetPathValue("name", "docs")
	handleSvnUpdate(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("update after re-auth = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

// errCodeOf reads the machine token out of an httpx error body.
func errCodeOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v: %s", err, rec.Body.String())
	}
	return body.Error.Code
}
