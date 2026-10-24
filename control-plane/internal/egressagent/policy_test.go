package egressagent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRenderAllowIsMinimalSortedAndSafe(t *testing.T) {
	got := string(renderAllow([]string{
		"api.anthropic.com", ".anthropic.com", "*.Example.org", "example.org.", "a.b.example.org",
		"github.com", "github.com", "# comment", "", "bad host", "x.com/path", "..dots",
	}))
	want := generatedHeader + ".anthropic.com\n.example.org\ngithub.com\n"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if string(renderAllow([]string{"b.com", "a.com"})) != string(renderAllow([]string{"a.com", "b.com"})) {
		t.Fatal("the same policy must render the same bytes in any order")
	}
}

// fakeCP serves a policy that the test can change and break.
type fakeCP struct {
	mu        sync.Mutex
	allowlist []string
	enforce   bool
	status    int
}

func (f *fakeCP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer tok" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"allowlist": f.allowlist, "enforce": f.enforce})
}

func (f *fakeCP) set(fn func(*fakeCP)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

// newPolicyFixture returns a policySync whose reload command appends a line to a file, so
// the test counts reloads.
func newPolicyFixture(t *testing.T, cp *fakeCP) (*policySync, func() int) {
	t.Helper()
	dir := t.TempDir()
	srv := httptest.NewServer(cp)
	t.Cleanup(srv.Close)
	reloads := filepath.Join(dir, "reloads")
	cfg := &Config{
		PolicyURL: srv.URL, Token: "tok", Client: srv.Client(),
		AllowFile: filepath.Join(dir, "allow.txt"), ModeFile: filepath.Join(dir, "mode.conf"),
		ReloadCmd: []string{"sh", "-c", "echo r >> " + reloads},
	}
	count := func() int {
		b, _ := os.ReadFile(reloads)
		return strings.Count(string(b), "r")
	}
	return newPolicySync(cfg), count
}

func TestPolicySyncWritesAndReloadsOnlyOnChange(t *testing.T) {
	cp := &fakeCP{allowlist: []string{"github.com"}}
	s, reloads := newPolicyFixture(t, cp)
	ctx := context.Background()

	if err := s.sync(ctx); err != nil {
		t.Fatal(err)
	}
	if reloads() != 1 {
		t.Fatalf("first sync: %d reloads, want 1", reloads())
	}
	if b, _ := os.ReadFile(s.cfg.ModeFile); string(b) != ModeLogOnly {
		t.Fatalf("mode file = %q", b)
	}
	if !s.cur.Load().match.Allows("github.com") || s.cur.Load().match.Allows("x.example") {
		t.Fatal("snapshot does not match the policy")
	}

	if err := s.sync(ctx); err != nil {
		t.Fatal(err)
	}
	if reloads() != 1 {
		t.Fatalf("unchanged policy reloaded Squid (%d reloads)", reloads())
	}

	cp.set(func(f *fakeCP) { f.enforce = true })
	if err := s.sync(ctx); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(s.cfg.ModeFile); string(b) != ModeEnforce || reloads() != 2 {
		t.Fatalf("mode change: file %q, %d reloads", b, reloads())
	}

	cp.set(func(f *fakeCP) { f.allowlist = append(f.allowlist, ".example.org") })
	if err := s.sync(ctx); err != nil {
		t.Fatal(err)
	}
	if reloads() != 3 || !s.cur.Load().match.Allows("a.example.org") {
		t.Fatalf("allowlist change: %d reloads", reloads())
	}

	// A restart reads the same files back and reloads once, because it cannot know whether
	// the previous run's reload happened; after that, no reload for nothing.
	s2 := newPolicySync(s.cfg)
	s2.loadFromDisk()
	if snap := s2.cur.Load(); snap == nil || !snap.enforce || !snap.match.Allows("a.example.org") {
		t.Fatalf("loadFromDisk snapshot = %+v", snap)
	}
	for i := 0; i < 2; i++ {
		if err := s2.sync(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if reloads() != 4 {
		t.Fatalf("restart: %d reloads, want 4", reloads())
	}
}

func TestPolicyRestartAfterFailedReloadReloads(t *testing.T) {
	// The files were written and the reload failed, then the agent restarted: the files
	// already match the CP, but Squid still runs the old policy.
	cp := &fakeCP{allowlist: []string{"github.com"}, enforce: true}
	s, reloads := newPolicyFixture(t, cp)
	good := s.cfg.ReloadCmd
	s.cfg.ReloadCmd = []string{"false"}
	if err := s.sync(context.Background()); err == nil {
		t.Fatal("a failed reload must report an error")
	}
	s.cfg.ReloadCmd = good
	restarted := newPolicySync(s.cfg)
	if err := restarted.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if reloads() != 1 {
		t.Fatalf("after the restart Squid was reloaded %d times, want 1", reloads())
	}
}

func TestPolicySyncKeepsLastWorkingPolicy(t *testing.T) {
	cp := &fakeCP{allowlist: []string{"github.com"}}
	s, reloads := newPolicyFixture(t, cp)
	ctx := context.Background()
	if err := s.sync(ctx); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(s.cfg.AllowFile)
	if reloads() != 1 {
		t.Fatalf("%d reloads after the first sync", reloads())
	}

	for _, breakIt := range []func(*fakeCP){
		func(f *fakeCP) { f.status = http.StatusServiceUnavailable },
		func(f *fakeCP) { f.status = 0; f.allowlist = nil; f.enforce = true },
	} {
		cp.set(breakIt)
		if err := s.sync(ctx); err == nil {
			t.Fatal("a failed fetch must report an error")
		}
		after, _ := os.ReadFile(s.cfg.AllowFile)
		mode, _ := os.ReadFile(s.cfg.ModeFile)
		if string(after) != string(before) || string(mode) != ModeLogOnly || reloads() != 1 {
			t.Fatalf("files or reloads changed on a failed fetch (%d reloads)", reloads())
		}
		if !s.cur.Load().match.Allows("github.com") || s.cur.Load().enforce {
			t.Fatal("snapshot changed on a failed fetch")
		}
	}

	// The CP down entirely.
	s.cfg.PolicyURL = "http://127.0.0.1:1/internal/egress/policy"
	if err := s.sync(ctx); err == nil {
		t.Fatal("an unreachable CP must report an error")
	}
	if after, _ := os.ReadFile(s.cfg.AllowFile); string(after) != string(before) {
		t.Fatal("allow file changed while the CP was unreachable")
	}
}

func TestPolicySyncRetriesFailedReload(t *testing.T) {
	cp := &fakeCP{allowlist: []string{"github.com"}}
	s, _ := newPolicyFixture(t, cp)
	ctx := context.Background()
	marker := filepath.Join(t.TempDir(), "ok")
	s.cfg.ReloadCmd = []string{"sh", "-c", "test -e " + marker}
	if err := s.sync(ctx); err == nil {
		t.Fatal("a failed reload must report an error")
	}
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// The files are already current; the owed reload is what makes this sync run it.
	s.cfg.ReloadCmd = []string{"sh", "-c", "test -e " + marker + " && touch " + marker + ".ran"}
	if err := s.sync(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker + ".ran"); err != nil {
		t.Fatal("the reload that failed was not retried")
	}
}
