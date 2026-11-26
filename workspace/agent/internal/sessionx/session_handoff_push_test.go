package sessionx

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memberorigin"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memberorigin/memberorigintest"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// pushFixture is a working copy cloned from a bare origin with main pushed and tracked. HOME
// is a temp dir, so the developer's global git config (hooksPath, credential helpers) plays
// no part.
func pushFixture(t *testing.T) (work, origin string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	origin = filepath.Join(root, "origin.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", "-b", "main", origin).CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v: %s", err, out)
	}
	work = filepath.Join(root, "work")
	gitInit(t, work)
	gitRun(t, work, "remote", "add", "origin", origin)
	gitRun(t, work, "push", "-q", "-u", "origin", "main")
	return work, origin
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// remoteRef is the sha of ref in the bare remote, "" when it does not exist there.
func remoteRef(dir, ref string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--verify", "--quiet", ref).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// confirmed is what the modal showed and the owner confirmed.
func confirmed(t *testing.T, dir string) handoffPushRequest {
	t.Helper()
	c := buildHandoffContext(dir)
	if c.PushRemote == "" {
		t.Fatalf("no push offered: %+v", c)
	}
	return handoffPushRequest{Branch: c.Branch, HeadSha: c.HeadSha, PushRemote: c.PushRemote}
}

func commitFile(t *testing.T, dir, name, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(msg), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", name)
	gitRun(t, dir, "commit", "-qm", msg)
}

// TestHandoffPushNoUpstreamSetsUpstream: the most common handoff state, a branch never
// pushed. The push publishes that branch alone and records its upstream, and the rebuilt
// verdict lets the offer through. Another unpushed branch and a tag stay local.
func TestHandoffPushNoUpstreamSetsUpstream(t *testing.T) {
	work, origin := pushFixture(t)
	gitRun(t, work, "checkout", "-q", "-b", "temp/work")
	commitFile(t, work, "a", "one")
	gitRun(t, work, "tag", "v-local")
	gitRun(t, work, "checkout", "-q", "feature")
	commitFile(t, work, "b", "other branch")
	gitRun(t, work, "checkout", "-q", "temp/work")
	// Uncommitted work is not pushed, nor committed.
	if err := os.WriteFile(filepath.Join(work, "dirty"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	c := buildHandoffContext(work)
	if c.Blocked != handoffBlockNoUpstream || c.PushRemote != origin || c.PushCommits != 1 {
		t.Fatalf("before: blocked=%q pushRemote=%q pushCommits=%d, want no_upstream origin 1", c.Blocked, c.PushRemote, c.PushCommits)
	}
	head := gitOut(t, work, "rev-parse", "HEAD")

	res, perr := pushForHandoff(context.Background(), work, confirmed(t, work))
	if perr != nil {
		t.Fatalf("push refused: %+v", perr)
	}
	if !res.Pushed || res.Remote != origin || res.Branch != "temp/work" {
		t.Fatalf("result = %+v", res)
	}
	if got := remoteRef(origin, "refs/heads/temp/work"); got != head {
		t.Fatalf("origin temp/work = %q, want %s", got, head)
	}
	if got := gitOut(t, work, "rev-parse", "--abbrev-ref", "temp/work@{u}"); got != "origin/temp/work" {
		t.Fatalf("upstream = %q, want origin/temp/work", got)
	}
	if res.Context.Blocked != "" || res.Context.HeadSha != head || res.Context.Warning != handoffWarnDirty {
		t.Fatalf("after: %+v, want the gate open on %s with the dirty warning kept", res.Context, head)
	}
	if got := remoteRef(origin, "refs/heads/feature"); got != "" {
		t.Fatalf("the other branch reached origin (%s); only the current branch may be pushed", got)
	}
	if got := remoteRef(origin, "refs/tags/v-local"); got != "" {
		t.Fatalf("a tag reached origin (%s)", got)
	}
	if st := gitOut(t, work, "status", "--porcelain"); !strings.Contains(st, "?? dirty") {
		t.Fatalf("the uncommitted file was touched: %q", st)
	}
}

func TestHandoffPushUnpushedCommits(t *testing.T) {
	work, origin := pushFixture(t)
	commitFile(t, work, "a", "one")
	commitFile(t, work, "b", "two")
	c := buildHandoffContext(work)
	if c.Blocked != handoffBlockUnpushed || c.PushRemote != origin || c.PushCommits != 2 {
		t.Fatalf("before: blocked=%q pushRemote=%q pushCommits=%d", c.Blocked, c.PushRemote, c.PushCommits)
	}
	res, perr := pushForHandoff(context.Background(), work, confirmed(t, work))
	if perr != nil {
		t.Fatalf("push refused: %+v", perr)
	}
	if head := gitOut(t, work, "rev-parse", "HEAD"); remoteRef(origin, "refs/heads/main") != head || res.Context.Blocked != "" {
		t.Fatalf("origin main = %q, want %s; after = %+v", remoteRef(origin, "refs/heads/main"), head, res.Context)
	}
}

// TestHandoffPushDivergedIsRefused: someone else pushed first. Never force: git's rejection
// comes back as it reported it and the remote keeps their commit.
func TestHandoffPushDivergedIsRefused(t *testing.T) {
	work, origin := pushFixture(t)
	other := filepath.Join(t.TempDir(), "other")
	if out, err := exec.Command("git", "clone", "-q", origin, other).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v: %s", err, out)
	}
	commitFile(t, other, "theirs", "theirs")
	gitRun(t, other, "push", "-q", "origin", "main")
	theirs := gitOut(t, other, "rev-parse", "HEAD")

	commitFile(t, work, "mine", "mine")
	_, perr := pushForHandoff(context.Background(), work, confirmed(t, work))
	if perr == nil || perr.code != "push_rejected" || !strings.Contains(perr.message, "rejected") {
		t.Fatalf("diverged push = %+v, want push_rejected carrying git's message", perr)
	}
	if got := remoteRef(origin, "refs/heads/main"); got != theirs {
		t.Fatalf("origin main = %s, want their commit %s kept", got, theirs)
	}
}

func TestHandoffPushWithoutOriginIsRefused(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	gitInit(t, dir)
	c := buildHandoffContext(dir)
	if c.Blocked != handoffBlockNoUpstream || c.PushRemote != "" {
		t.Fatalf("blocked=%q pushRemote=%q, want no_upstream and nothing to push to", c.Blocked, c.PushRemote)
	}
	if _, perr := pushForHandoff(context.Background(), dir, handoffPushRequest{Branch: "main", HeadSha: "x", PushRemote: "x"}); perr == nil || perr.code != "no_push_remote" {
		t.Fatalf("push without origin = %+v, want no_push_remote", perr)
	}
}

func TestHandoffPushDetachedIsNotOffered(t *testing.T) {
	work, origin := pushFixture(t)
	commitFile(t, work, "a", "one")
	gitRun(t, work, "checkout", "-q", "--detach")
	c := buildHandoffContext(work)
	if c.Blocked != handoffBlockDetached || c.PushRemote != "" || c.PushCommits != 0 {
		t.Fatalf("detached: %+v, want blocked detached_head with no push offered", c)
	}
	before := remoteRef(origin, "refs/heads/main")
	if _, perr := pushForHandoff(context.Background(), work, handoffPushRequest{Branch: "main", HeadSha: "x", PushRemote: "x"}); perr == nil || perr.code != handoffBlockDetached {
		t.Fatalf("detached push = %+v, want detached_head", perr)
	}
	if got := remoteRef(origin, "refs/heads/main"); got != before {
		t.Fatalf("origin main moved to %s", got)
	}
}

// TestHandoffPushRunsPrePushHook: the repository's own hooks run (a repo may guard its
// protected branches or scan for secrets there). A refusal reaches the user verbatim, minus
// any credential in a URL it printed.
func TestHandoffPushRunsPrePushHook(t *testing.T) {
	work, origin := pushFixture(t)
	hooks := filepath.Join(t.TempDir(), "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	hook := "#!/bin/sh\necho 'pre-push: blocked by policy for https://bot:s3cr3tvalue@example.com/x.git' >&2\necho 'http.extraheader=Authorization: Basic dXNlcjpwYXNz' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(hooks, "pre-push"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, work, "config", "core.hooksPath", hooks)
	commitFile(t, work, "a", "one")
	before := remoteRef(origin, "refs/heads/main")

	_, perr := pushForHandoff(context.Background(), work, confirmed(t, work))
	if perr == nil || perr.code != "push_rejected" || !strings.Contains(perr.message, "pre-push: blocked by policy") {
		t.Fatalf("hooked push = %+v, want push_rejected with the hook's output", perr)
	}
	if strings.Contains(perr.message, "s3cr3tvalue") || strings.Contains(perr.message, "dXNlcjpwYXNz") || !strings.Contains(perr.message, "https://example.com/x.git") {
		t.Fatalf("hook output not scrubbed of the URL credential: %q", perr.message)
	}
	if got := remoteRef(origin, "refs/heads/main"); got != before {
		t.Fatalf("origin main moved to %s past a refusing hook", got)
	}
}

// A gate that is already open pushes nothing, and still answers only for the confirmed commit.
func TestHandoffPushNothingToDo(t *testing.T) {
	work, _ := pushFixture(t)
	c := buildHandoffContext(work)
	res, perr := pushForHandoff(context.Background(), work, handoffPushRequest{Branch: c.Branch, HeadSha: c.HeadSha, PushRemote: c.Remote})
	if perr != nil || res.Pushed || res.Context.Blocked != "" {
		t.Fatalf("clean pushed branch: res=%+v err=%+v, want pushed=false and no error", res, perr)
	}
	if _, perr := pushForHandoff(context.Background(), work, handoffPushRequest{Branch: "other", HeadSha: c.HeadSha, PushRemote: c.Remote}); perr == nil || perr.code != handoffPushChanged {
		t.Fatalf("open gate, another branch confirmed: %+v, want changed", perr)
	}
}

// The handler admits only a request the CP marked, decodes the confirmed snapshot and pushes.
func TestHandleSessionHandoffPush(t *testing.T) {
	work, origin := pushFixture(t)
	gitRun(t, work, "checkout", "-q", "-b", "temp/http")
	commitFile(t, work, "a", "one")
	// Stored as a space: the context after the push names the repo by the stored place, as
	// GET handoff-context does, not by what the directory looks like now.
	session.WriteMeta(session.Meta{Name: "push1", Dir: work, Kind: session.KindClaude, Place: &session.Place{Kind: session.PlaceSpace, ID: "work"}})
	session.WriteMeta(session.Meta{Name: "push2", Kind: session.KindClaude})
	signer := memberorigintest.Install(t)
	h := memberorigin.Require(HandoffPushMaxRequest, http.HandlerFunc(HandleSessionHandoffPush))
	want := confirmed(t, work)
	body, _ := json.Marshal(want)
	call := func(name string, sign bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/sessions/"+name+"/handoff-push", strings.NewReader(string(body)))
		r.Header.Set("Content-Type", "application/json")
		r.SetPathValue("name", name)
		if sign {
			signer.Sign(r, body)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if got := call("push1", false); got.Code != http.StatusForbidden || remoteRef(origin, "refs/heads/temp/http") != "" {
		t.Fatalf("unmarked: %d %s - must be refused before any push", got.Code, got.Body.String())
	}
	// The handler's own check, for a route registered without Require.
	r := httptest.NewRequest(http.MethodPost, "/sessions/push1/handoff-push", strings.NewReader(string(body)))
	r.SetPathValue("name", "push1")
	w := httptest.NewRecorder()
	HandleSessionHandoffPush(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("handler without Require: %d", w.Code)
	}
	if got := call("push2", true); got.Code != http.StatusConflict || !strings.Contains(got.Body.String(), "no_working_copy") {
		t.Fatalf("no working copy: %d %s", got.Code, got.Body.String())
	}
	if got := call("nope", true); got.Code != http.StatusNotFound {
		t.Fatalf("unknown session: %d %s", got.Code, got.Body.String())
	}
	got := call("push1", true)
	var res struct {
		Pushed  bool           `json:"pushed"`
		Context map[string]any `json:"context"`
	}
	if got.Code != http.StatusOK || json.Unmarshal(got.Body.Bytes(), &res) != nil || !res.Pushed {
		t.Fatalf("push: %d %s", got.Code, got.Body.String())
	}
	if res.Context["headSha"] != remoteRef(origin, "refs/heads/temp/http") || res.Context["blocked"] != nil {
		t.Fatalf("context after push = %v", res.Context)
	}
	if res.Context["repo"] != "" {
		t.Errorf("context after push names repo %v for a session stored as a space", res.Context["repo"])
	}
}

// TestHandoffPushRefusesAPushURLItDoesNotShow: origin fetches from A but pushurl or
// pushInsteadOf sends to B. The modal would show A, so no push is offered and none is made.
func TestHandoffPushRefusesAPushURLItDoesNotShow(t *testing.T) {
	for _, cfg := range []func(t *testing.T, work, other string){
		func(t *testing.T, work, other string) { gitRun(t, work, "config", "remote.origin.pushurl", other) },
		func(t *testing.T, work, other string) {
			url := gitOut(t, work, "remote", "get-url", "origin")
			gitRun(t, work, "config", "url."+other+".pushInsteadOf", url)
		},
	} {
		work, origin := pushFixture(t)
		other := filepath.Join(t.TempDir(), "other.git")
		if out, err := exec.Command("git", "init", "-q", "--bare", "-b", "main", other).CombinedOutput(); err != nil {
			t.Fatalf("init bare: %v: %s", err, out)
		}
		cfg(t, work, other)
		commitFile(t, work, "a", "one")
		c := buildHandoffContext(work)
		if c.Blocked != handoffBlockUnpushed || c.PushRemote != "" {
			t.Fatalf("blocked=%q pushRemote=%q, want no push offered", c.Blocked, c.PushRemote)
		}
		want := handoffPushRequest{Branch: "main", HeadSha: c.HeadSha, PushRemote: c.Remote}
		if _, perr := pushForHandoff(context.Background(), work, want); perr == nil || perr.code != "no_push_remote" {
			t.Fatalf("push = %+v, want no_push_remote", perr)
		}
		if remoteRef(other, "refs/heads/main") != "" || remoteRef(origin, "refs/heads/main") == c.HeadSha {
			t.Fatal("something was pushed")
		}
	}
}

// TestHandoffPushRefusesAnUpstreamTheOfferDoesNotName: the offer names origin's URL and the
// branch's own name. An upstream on another remote, or under another branch name, would
// publish the commit where the recipient never looks.
func TestHandoffPushRefusesAnUpstreamTheOfferDoesNotName(t *testing.T) {
	for _, cfg := range [][2]string{{"review-upstream", "refs/heads/main"}, {"origin", "refs/heads/published"}} {
		work, origin := pushFixture(t)
		other := filepath.Join(t.TempDir(), "other.git")
		if out, err := exec.Command("git", "init", "-q", "--bare", "-b", "main", other).CombinedOutput(); err != nil {
			t.Fatalf("init bare: %v: %s", err, out)
		}
		gitRun(t, work, "remote", "add", "review-upstream", other)
		gitRun(t, work, "push", "-q", "review-upstream", "main", "main:published")
		gitRun(t, work, "push", "-q", "origin", "main:published")
		gitRun(t, work, "fetch", "-q", "--all")
		gitRun(t, work, "config", "branch.main.remote", cfg[0])
		gitRun(t, work, "config", "branch.main.merge", cfg[1])
		commitFile(t, work, "a", "one")
		c := buildHandoffContext(work)
		if c.Blocked != handoffBlockUnpushed || c.PushRemote != "" {
			t.Fatalf("%v: blocked=%q pushRemote=%q, want no push offered", cfg, c.Blocked, c.PushRemote)
		}
		want := handoffPushRequest{Branch: "main", HeadSha: c.HeadSha, PushRemote: c.Remote}
		if _, perr := pushForHandoff(context.Background(), work, want); perr == nil || perr.code != "no_push_remote" {
			t.Fatalf("%v: push = %+v, want no_push_remote", cfg, perr)
		}
		for _, r := range []string{other + " refs/heads/main", other + " refs/heads/published", origin + " refs/heads/published", origin + " refs/heads/main"} {
			f := strings.Fields(r)
			if remoteRef(f[0], f[1]) == c.HeadSha {
				t.Fatalf("%v: %s reached %s", cfg, c.HeadSha, r)
			}
		}
	}
}

// TestHandoffPushRefusesAChangedSnapshot: a commit or a checkout after the modal was shown
// stops the push; what is published is never other than what was confirmed.
func TestHandoffPushRefusesAChangedSnapshot(t *testing.T) {
	work, origin := pushFixture(t)
	commitFile(t, work, "a", "one")
	want := confirmed(t, work)
	before := remoteRef(origin, "refs/heads/main")
	commitFile(t, work, "b", "two")
	if _, perr := pushForHandoff(context.Background(), work, want); perr == nil || perr.code != handoffPushChanged {
		t.Fatalf("push after a new commit = %+v, want changed", perr)
	}
	gitRun(t, work, "checkout", "-q", "-b", "elsewhere", want.HeadSha)
	if _, perr := pushForHandoff(context.Background(), work, handoffPushRequest{Branch: "main", HeadSha: want.HeadSha, PushRemote: want.PushRemote}); perr == nil || perr.code != handoffPushChanged {
		t.Fatalf("push after a checkout = %+v, want changed", perr)
	}
	if got := remoteRef(origin, "refs/heads/main"); got != before {
		t.Fatalf("origin main moved to %s", got)
	}
}

func installHook(t *testing.T, work, name, script string) {
	t.Helper()
	hooks := filepath.Join(t.TempDir(), "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooks, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, work, "config", "core.hooksPath", hooks)
}

// TestHandoffPushRefusesWhenAHookMovesHEAD: a pre-push hook that checks out another, already
// pushed branch leaves a state whose verdict is "clear" but describes another commit. The
// push went through, but nothing may be offered on it.
func TestHandoffPushRefusesWhenAHookMovesHEAD(t *testing.T) {
	work, _ := pushFixture(t)
	gitRun(t, work, "push", "-q", "-u", "origin", "feature")
	commitFile(t, work, "a", "one")
	want := confirmed(t, work)
	installHook(t, work, "pre-push", "#!/bin/sh\ngit checkout -q feature\n")
	if _, perr := pushForHandoff(context.Background(), work, want); perr == nil || perr.code != handoffPushChanged {
		t.Fatalf("push with a HEAD-moving hook = %+v, want changed", perr)
	}
}

// TestHandoffPushCancelKillsTheHook: the timeout stops the hook and its children, not git
// alone, and the call returns promptly.
func TestHandoffPushCancelKillsTheHook(t *testing.T) {
	work, _ := pushFixture(t)
	commitFile(t, work, "a", "one")
	want := confirmed(t, work)
	marker := filepath.Join(t.TempDir(), "started")
	installHook(t, work, "pre-push", "#!/bin/sh\nsleep 30 &\necho $! > "+marker+".child\necho $$ > "+marker+"\nwait\n")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for i := 0; i < 200; i++ {
			if _, err := os.Stat(marker); err == nil {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
		cancel()
	}()
	start := time.Now()
	_, perr := pushForHandoff(ctx, work, want)
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("returned after %s; the hook kept it waiting", took)
	}
	if perr == nil || perr.code != "push_timeout" {
		t.Fatalf("cancelled push = %+v, want push_timeout", perr)
	}
	raw, err := os.ReadFile(marker + ".child")
	if err != nil {
		t.Fatalf("the hook never started: %v", err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	deadline := time.Now().Add(2 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("the hook's child %d is still running", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestScrubPushOutput(t *testing.T) {
	for _, in := range []string{
		"http.extraheader=Authorization: Basic dXNlcjpwYXNz",
		"http.https://example.com/.extraheader=AUTHORIZATION: bearer abc",
		"Proxy-Authorization: Basic Zm9vOmJhcg==",
		"remote.origin.password=hunter2",
	} {
		got := scrubPushOutput(in)
		for _, secret := range []string{"dXNlcjpwYXNz", "abc", "Zm9vOmJhcg", "hunter2"} {
			if strings.Contains(got, secret) {
				t.Errorf("scrubPushOutput(%q) = %q still carries %q", in, got, secret)
			}
		}
	}
	in := "To https://x-access-token:ghp_abcdefghijklmnopqrstuvwxyz0123@github.com/o/r.git\n ! [rejected] main -> main (fetch first)"
	got := scrubPushOutput(in)
	if strings.Contains(got, "ghp_") || strings.Contains(got, "x-access-token") {
		t.Fatalf("credential survived: %q", got)
	}
	if !strings.Contains(got, "https://github.com/o/r.git") || !strings.Contains(got, "[rejected] main -> main (fetch first)") {
		t.Fatalf("git's message was mangled: %q", got)
	}
}
