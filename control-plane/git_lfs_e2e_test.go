package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// gitMux builds a mux with the internal-git routes (LFS + smart-HTTP) exactly as
// routes.go registers them, so the test exercises real route precedence and dispatch.
func (a gitServerAPI) gitMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /git/{slug}/{repo}/info/lfs/objects/batch", a.lfsBatch)
	mux.HandleFunc("PUT /git/{slug}/{repo}/info/lfs/objects/{oid}", a.lfsUpload)
	mux.HandleFunc("GET /git/{slug}/{repo}/info/lfs/objects/{oid}", a.lfsDownload)
	mux.HandleFunc("POST /git/{slug}/{repo}/info/lfs/locks", a.lfsLockCreate)
	mux.HandleFunc("GET /git/{slug}/{repo}/info/lfs/locks", a.lfsLocksList)
	mux.HandleFunc("POST /git/{slug}/{repo}/info/lfs/locks/verify", a.lfsLocksVerify)
	mux.HandleFunc("POST /git/{slug}/{repo}/info/lfs/locks/{id}/unlock", a.lfsUnlock)
	mux.HandleFunc("/git/{slug}/{repo...}", a.gitHTTP)
	return mux
}

// TestLFSEndToEnd drives real git + git-lfs against the full mux: track a pattern,
// commit a large file (stored via LFS), push, then a fresh clone smudges the object
// back to its real bytes. Proves the batch + transfer wiring works with the actual
// client. Skips where git / git-http-backend / git-lfs are absent.
func TestLFSEndToEnd(t *testing.T) {
	if _, err := os.Stat(gitBackendPath()); err != nil {
		t.Skipf("git-http-backend absent: %v", err)
	}
	for _, bin := range []string{"git"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed", bin)
		}
	}
	if err := exec.Command("git", "lfs", "version").Run(); err != nil {
		t.Skip("git-lfs not installed")
	}

	ctx := context.Background()
	tmp := t.TempDir()
	dataRoot := filepath.Join(tmp, "data")
	st, err := store.OpenSQLite(filepath.Join(tmp, "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	dflt, _ := st.EnsureDefaultTenant(ctx)
	ident, _ := st.UpsertIdentity(ctx, "u@x", "u-x", "")
	mem, _ := st.EnsureMembership(ctx, ident.ID, dflt.ID, "member")

	master := []byte("master-key-lfs-e2e-0000000000000000")
	mgr := &manager{store: st, master32: master, dataRoot: dataRoot}
	token := mintGitToken(gitSignKey(master), mem.ID)

	// Create the bare + ledger row.
	dir := filepath.Join(dataRoot, "git", "default", "shared.git")
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "--bare", "--initial-branch=main", dir).CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v: %s", err, out)
	}
	if err := st.CreateGitRepo(ctx, store.GitRepo{ID: store.NewID(), TenantID: dflt.ID, Name: "shared", DefaultBranch: "main", CreatedAt: store.NowTS()}); err != nil {
		t.Fatal(err)
	}

	// publicBaseURL must point at the test server so batch hrefs resolve back to it.
	// gitServerAPI binds it by value at construction, so build the API AFTER the
	// listener exists: an unstarted server exposes its address without serving yet.
	srv := httptest.NewUnstartedServer(nil)
	defer srv.Close()
	g := newGitServerAPI(mgr, "http://"+srv.Listener.Addr().String())
	srv.Config.Handler = g.gitMux()
	srv.Start()

	hostPart := srv.URL[len("http://"):]
	authURL := "http://x-access-token:" + token + "@" + hostPart + "/git/default/shared.git"

	// Isolated git env: a temp HOME + global config with a catch-all credential
	// helper and the LFS filters, so nothing touches the real user config.
	home := filepath.Join(tmp, "home")
	os.MkdirAll(home, 0o755)
	gcfg := filepath.Join(home, ".gitconfig")
	helper := "!f() { echo username=x-access-token; echo password=" + token + "; }; f"
	cfg := "[user]\n\tname = t\n\temail = t@x\n[credential]\n\thelper = \"" + helper + "\"\n" +
		"[filter \"lfs\"]\n\tclean = git-lfs clean -- %f\n\tsmudge = git-lfs smudge -- %f\n\tprocess = git-lfs filter-process\n\trequired = true\n"
	if err := os.WriteFile(gcfg, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	env := []string{
		"HOME=" + home,
		"GIT_CONFIG_GLOBAL=" + gcfg,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_LFS_SKIP_SMUDGE=0",
	}
	runOut := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), env...)
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		if err := cmd.Run(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out.String())
		}
		return out.String()
	}
	run := func(dir string, args ...string) { runOut(dir, args...) }

	// Author clone: track *.bin via LFS, commit a large blob, push.
	wa := filepath.Join(tmp, "wsA")
	run(tmp, "clone", authURL, wa)
	run(wa, "lfs", "track", "*.bin")
	run(wa, "add", ".gitattributes")
	big := bytes.Repeat([]byte("LFS-PAYLOAD-0123456789\n"), 5000) // ~110 KB
	if err := os.WriteFile(filepath.Join(wa, "asset.bin"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	run(wa, "add", "asset.bin")
	run(wa, "commit", "-m", "add large asset via lfs")
	run(wa, "push", "origin", "HEAD:main")

	// The object must have landed in the CP's LFS store and ledger.
	if n, _ := st.TenantLFSBytes(ctx, dflt.ID); n != int64(len(big)) {
		t.Fatalf("ledger bytes = %d, want %d (object not stored via LFS)", n, len(big))
	}

	// Fresh clone: git-lfs smudge must pull the real bytes back, not a pointer.
	wb := filepath.Join(tmp, "wsB")
	run(tmp, "clone", authURL, wb)
	got, err := os.ReadFile(filepath.Join(wb, "asset.bin"))
	if err != nil {
		t.Fatalf("read cloned asset: %v", err)
	}
	if !bytes.Equal(got, big) {
		// A pointer file would be ~130 bytes starting with "version https://git-lfs".
		t.Fatalf("cloned asset not smudged: %d bytes, prefix=%q", len(got), string(got[:min(60, len(got))]))
	}
	if strings.HasPrefix(string(got), "version https://git-lfs") {
		t.Fatal("cloned asset is still an LFS pointer (smudge did not fetch)")
	}

	// Locking API through the real client: lock → appears in `git lfs locks` →
	// unlock → gone. Proves the create/list/unlock JSON contract matches git-lfs.
	run(wa, "lfs", "lock", "asset.bin")
	if locks := runOut(wa, "lfs", "locks"); !strings.Contains(locks, "asset.bin") {
		t.Fatalf("locked file not listed by `git lfs locks`:\n%s", locks)
	}
	run(wa, "lfs", "unlock", "asset.bin")
	if locks := runOut(wa, "lfs", "locks"); strings.Contains(locks, "asset.bin") {
		t.Fatalf("file still locked after unlock:\n%s", locks)
	}
}
