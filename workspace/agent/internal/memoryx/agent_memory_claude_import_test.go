package memoryx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
)

// claudeImportEnv is agentMemTestEnv plus helpers for the claude side. The slug is the main
// clone's, which is where claude keeps a worktree session's memory too.
type claudeImportEnv struct {
	t     *testing.T
	home  string
	clone string
	wt    string
	slug  string
	pid   string
}

func newClaudeImportEnv(t *testing.T) *claudeImportEnv {
	home, clone, wt := agentMemTestEnv(t)
	p := agentMemProjectFor(clone)
	return &claudeImportEnv{t: t, home: home, clone: clone, wt: wt, slug: claude.ProjectKey(clone), pid: p.ID}
}

func (e *claudeImportEnv) memDir(slug string) string {
	return filepath.Join(claude.ConfigDir(), "projects", slug, "memory")
}

// file writes a claude memory file in the nested form claude itself writes.
func (e *claudeImportEnv) file(name, desc, typ, body string) string {
	e.t.Helper()
	text := fmt.Sprintf("---\nname: %s\ndescription: %s\nmetadata:\n  type: %s\n---\n\n%s\n", name, desc, typ, body)
	return e.raw(e.slug, name+".md", text)
}

func (e *claudeImportEnv) raw(slug, file, text string) string {
	e.t.Helper()
	p := filepath.Join(e.memDir(slug), file)
	memoryWrite(e.t, p, text)
	return p
}

func (e *claudeImportEnv) preview() *agentMemImportPreview {
	e.t.Helper()
	pv, err := agentMemImportPreviewFor(e.slug)
	if err != nil {
		e.t.Fatal(err)
	}
	return pv
}

func (e *claudeImportEnv) item(pv *agentMemImportPreview, name string) agentMemImportItem {
	e.t.Helper()
	it := pv.byName[name]
	if it == nil {
		e.t.Fatalf("no item %q in %+v", name, pv.Counts)
	}
	return *it
}

func (e *claudeImportEnv) apply(now time.Time, names ...string) agentMemImportApplied {
	e.t.Helper()
	pv := e.preview()
	req := agentMemImportReq{Project: e.pid, Slug: e.slug}
	for _, n := range names {
		req.Items = append(req.Items, agentMemImportReqItem{Name: n, SourceHash: pv.byName[n].SourceHash})
	}
	out, err := agentMemImportApply(req, now)
	if err != nil {
		e.t.Fatal(err)
	}
	return out
}

func claudeImportListed(slug string) *agentMemImportSource {
	for _, s := range agentMemImportList().Sources {
		if s.Slug == slug {
			return &s
		}
	}
	return nil
}

func claudeImportResult(a agentMemImportApplied, name string) agentMemImportResult {
	for _, r := range a.Results {
		if r.Name == name {
			return r
		}
	}
	return agentMemImportResult{}
}

func TestClaudeImportSourcesMapping(t *testing.T) {
	e := newClaudeImportEnv(t)
	e.file("a-note", "d", "user", "body")
	// A worktree has its own slug, but claude keeps its memory under the main clone's; a
	// directory with only the index has nothing to import.
	e.raw(claude.ProjectKey(e.wt), "MEMORY.md", "- idx\n")
	// A project with files and no working copy.
	e.raw("-gone-project", "x.md", "---\nname: x\ndescription: d\n---\nb\n")
	// Two plain directories whose slugs collide.
	memoryMkdirAll(t, filepath.Join(e.home, "repos", "amb-x"))
	memoryMkdirAll(t, filepath.Join(e.home, "repos", "amb@x"))
	e.raw(claude.ProjectKey(filepath.Join(e.home, "repos", "amb-x")), "y.md", "---\nname: y\ndescription: d\n---\nb\n")

	got := agentMemImportList()
	by := map[string]agentMemImportSource{}
	for _, s := range got.Sources {
		by[s.Slug] = s
	}
	if s := by[e.slug]; s.Count != 1 || s.Project == nil || s.Project.ID != e.pid || s.Reason != "" {
		t.Errorf("clone source = %+v", s)
	}
	if _, ok := by[claude.ProjectKey(e.wt)]; ok {
		t.Errorf("index-only worktree directory must not be listed")
	}
	if s := by["-gone-project"]; s.Reason != claudeImportNoProject || s.Project != nil {
		t.Errorf("no_project source = %+v", s)
	}
	if s := by[claude.ProjectKey(filepath.Join(e.home, "repos", "amb-x"))]; s.Reason != claudeImportAmbiguous {
		t.Errorf("ambiguous source = %+v", s)
	}
	// Neither is importable: the preview lists nothing and apply refuses.
	pv, err := agentMemImportPreviewFor("-gone-project")
	if err != nil || pv.Project != nil || pv.Reason != claudeImportNoProject || len(pv.Items) != 0 {
		t.Errorf("no_project preview = %+v, %v", pv, err)
	}
	if _, err := agentMemImportApply(agentMemImportReq{Project: e.pid, Slug: "-gone-project",
		Items: []agentMemImportReqItem{{Name: "x"}}}, time.Now()); agentMemCode(err) != errCodeMemoryConflict {
		t.Errorf("apply on no_project: %v", err)
	}
}

func TestClaudeImportNestedTypeAndLongDescription(t *testing.T) {
	e := newClaudeImportEnv(t)
	e.file("typed", "short", "feedback", "the body")
	e.file("badtype", "short", "bogus", "the body")
	long := strings.Repeat("あ", 150) // 450 bytes
	e.file("long-desc", long, "project", "tail")

	pv := e.preview()
	if it := e.item(pv, "typed"); it.Status != claudeImportNew || it.Type != "feedback" || it.Shortened {
		t.Errorf("typed = %+v", it)
	}
	if it := e.item(pv, "badtype"); it.Status != claudeImportNew || it.Type != "" {
		t.Errorf("an unknown type is dropped, not an error: %+v", it)
	}
	ld := e.item(pv, "long-desc")
	if !ld.Shortened || len(ld.Description) > agentMemMaxDescription || !strings.HasSuffix(ld.Description, "…") {
		t.Errorf("long-desc = %d bytes %q", len(ld.Description), ld.Description)
	}

	e.apply(time.Now(), "typed", "long-desc")
	got, err := agentMemRead(agentMemCallerT(t, "claude-main"), "", "long-desc")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got.Body, long+"\n\ntail") || len(got.Description) > agentMemMaxDescription {
		t.Errorf("the full description must lead the body: %.40q", got.Body)
	}
	if got.AuthorKind != agentMemUnknown || got.AuthorSession != agentMemUnknown || got.Revision != 1 ||
		!strings.HasPrefix(got.Source, "claude:projects/"+e.slug+"/memory/") || got.SourceHash == "" || got.Type != "project" {
		t.Errorf("imported entry = %+v", got)
	}
	msg, _ := memoryGitRun("log", "-1", "--format=%B", memoryBranch)
	for _, w := range []string{"AF-Op: import", "AF-Author-Kind: member", "AF-Author-Session: console"} {
		if !strings.Contains(msg, w) {
			t.Errorf("commit lacks %q:\n%s", w, msg)
		}
	}
	files, _ := memoryGitRun("log", "--name-only", "--format=", memoryBranch)
	if !strings.Contains(files, "projects/"+e.pid+"/project.json") {
		t.Errorf("project.json not committed: %s", files)
	}
	ch, err := agentMemListChanges(50)
	if err != nil || ch.Withheld != 0 || len(ch.Changes) != 2 || ch.Changes[0].Op != "import" {
		t.Errorf("changes = %+v, %v", ch, err)
	}
}

func TestClaudeImportInvalidReasons(t *testing.T) {
	e := newClaudeImportEnv(t)
	e.raw(e.slug, "Bad_Name.md", "---\ndescription: d\n---\nb\n")
	e.raw(e.slug, "no-fm.md", "just text\n")
	e.raw(e.slug, "no-desc.md", "---\nname: x\n---\nb\n")
	e.raw(e.slug, "no-body.md", "---\ndescription: d\n---\n\n")
	e.raw(e.slug, "nul.md", "---\ndescription: d\n---\nb\x00c\n")
	e.raw(e.slug, "wide.md", "---\ndescription: d\n---\n"+strings.Repeat("x", agentMemMaxLine+10)+"\n")
	e.raw(e.slug, "huge.md", "---\ndescription: d\n---\n"+strings.Repeat("line\n", agentMemMaxFile/5+10))
	target := filepath.Join(e.home, "elsewhere.md")
	memoryWrite(t, target, "---\ndescription: d\n---\nb\n")
	if err := os.Symlink(target, filepath.Join(e.memDir(e.slug), "link.md")); err != nil {
		t.Fatal(err)
	}

	pv := e.preview()
	want := map[string]string{"Bad_Name": "bad_name", "no-fm": "no_frontmatter", "no-desc": "no_description",
		"no-body": "no_body", "nul": "nul_byte", "wide": "line_too_long", "huge": "too_large", "link": "symlink"}
	for name, reason := range want {
		if it := e.item(pv, name); it.Status != claudeImportInvalid || it.Reason != reason {
			t.Errorf("%s = %s/%s, want invalid/%s", name, it.Status, it.Reason, reason)
		}
	}
	// Apply refuses all of them whatever hash is sent.
	req := agentMemImportReq{Project: e.pid, Slug: e.slug}
	for name := range want {
		req.Items = append(req.Items, agentMemImportReqItem{Name: strings.ToLower(name), SourceHash: pv.byName[name].SourceHash})
	}
	out, err := agentMemImportApply(req, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range out.Results {
		if r.Result != "skipped" {
			t.Errorf("imported an invalid item: %+v", r)
		}
	}
}

// The scan has no override: a hit is listed with a masked finding and never written. The value
// appears in no response, no commit and no log line.
func TestClaudeImportSecretIsSkippedAndNeverEchoed(t *testing.T) {
	e := newClaudeImportEnv(t)
	key := "ghp_" + strings.Repeat("q7Zx", 9)
	e.file("leaky", "has a token", "user", "token is "+key)
	e.file("clean", "fine", "user", "nothing here")
	// A file whose name is the secret is withheld, never named.
	nameKey := agentMemFakeSlackName()
	e.raw(e.slug, nameKey+".md", "---\ndescription: d\n---\nb\n")

	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)

	pv := e.preview()
	it := e.item(pv, "leaky")
	if it.Status != claudeImportSecret || len(it.Findings) == 0 || it.Findings[0].Path != "body" {
		t.Fatalf("leaky = %+v", it)
	}
	out := e.apply(time.Now(), "leaky", "clean")
	if r := claudeImportResult(out, "leaky"); r.Result != "skipped" || r.Reason != "status_secret" {
		t.Errorf("leaky result = %+v", r)
	}
	if r := claudeImportResult(out, "clean"); r.Result != "imported" {
		t.Errorf("clean result = %+v", r)
	}

	wire, _ := json.Marshal(pv)
	src, _ := json.Marshal(agentMemImportList())
	all, _ := memoryGitRun("log", "-p", memoryBranch)
	for label, text := range map[string]string{"preview": string(wire), "sources": string(src), "history": all, "log": logs.String()} {
		if strings.Contains(strings.ToLower(text), strings.ToLower(key)) || strings.Contains(strings.ToLower(text), nameKey) {
			t.Errorf("a secret leaked into the %s", label)
		}
	}
	if pv.Withheld != 1 {
		t.Errorf("withheld = %d, want the secret-named file counted", pv.Withheld)
	}
	if _, err := os.Stat(filepath.Join(claude.ConfigDir(), "af-agent-memory", "projects", e.pid, "leaky.md")); err == nil {
		t.Errorf("the secret-bearing memory was written")
	}
}

// A slug that is itself a token is counted, not named.
func TestClaudeImportSecretSlugWithheld(t *testing.T) {
	e := newClaudeImportEnv(t)
	slug := "AKIA" + "ZXCVBNMLKJHGFDSA"
	e.raw(slug, "x.md", "---\ndescription: d\n---\nb\n")
	got := agentMemImportList()
	b, _ := json.Marshal(got)
	if got.Withheld != 1 || strings.Contains(string(b), slug) {
		t.Errorf("sources = %s", b)
	}
}

func TestClaudeImportStatusesAndIdempotence(t *testing.T) {
	e := newClaudeImportEnv(t)
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	p := e.file("one", "first", "user", "v1")
	e.file("two", "second", "user", "v1")
	if err := os.Chtimes(p, t0, t0); err != nil {
		t.Fatal(err)
	}
	out := e.apply(t0.Add(time.Hour), "one", "two")
	if claudeImportResult(out, "one").Result != "imported" || claudeImportResult(out, "two").Result != "imported" {
		t.Fatalf("first import = %+v", out)
	}
	// Idempotent: nothing is left to do.
	pv := e.preview()
	if pv.Counts[claudeImportUnchanged] != 2 || len(pv.Counts) != 1 {
		t.Fatalf("after import counts = %v", pv.Counts)
	}
	head, _ := memoryGitRun("rev-parse", memoryBranch)
	if out := e.apply(t0.Add(2*time.Hour), "one"); claudeImportResult(out, "one").Reason != "status_unchanged" {
		t.Errorf("second apply = %+v", out)
	}
	if h, _ := memoryGitRun("rev-parse", memoryBranch); h != head {
		t.Errorf("a no-op apply committed")
	}

	// Newer claude file with different content: update. Older (or same age) one: unchanged.
	e.file("one", "first", "user", "v2")
	later := t0.Add(3 * time.Hour)
	_ = os.Chtimes(filepath.Join(e.memDir(e.slug), "one.md"), later, later)
	e.file("two", "second", "user", "v2")
	_ = os.Chtimes(filepath.Join(e.memDir(e.slug), "two.md"), t0, t0) // older than the AF copy
	pv = e.preview()
	one := e.item(pv, "one")
	if one.Status != claudeImportUpdate || one.AFUpdated == "" || one.SourceModified == "" {
		t.Errorf("one = %+v", one)
	}
	if two := e.item(pv, "two"); two.Status != claudeImportUnchanged {
		t.Errorf("two (AF copy newer) = %+v", two)
	}
	out = e.apply(later.Add(time.Hour), "one")
	if claudeImportResult(out, "one").Result != "updated" {
		t.Fatalf("update = %+v", out)
	}
	got, _ := agentMemRead(agentMemCallerT(t, "claude-main"), "", "one")
	if got.Revision != 2 || got.Body != "v2" {
		t.Errorf("updated entry = %+v", got)
	}
}

// A memory the member forgot, or whose import they reverted, is never brought back.
func TestClaudeImportForgottenIsNotResurrected(t *testing.T) {
	e := newClaudeImportEnv(t)
	now := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	e.file("gone", "d", "user", "b")
	e.file("undone", "d", "user", "b")
	e.file("fresh", "d", "user", "b")
	out := e.apply(now, "gone", "undone")

	c := agentMemCallerT(t, "claude-main")
	if _, err := agentMemForget(c, agentMemForgetReq{Name: "gone", Revision: 1}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := agentMemRevert(agentMemRevertReq{Commit: claudeImportResult(out, "undone").Commit}, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	pv := e.preview()
	for _, n := range []string{"gone", "undone"} {
		if it := e.item(pv, n); it.Status != claudeImportForgotten {
			t.Errorf("%s = %+v, want forgotten", n, it)
		}
	}
	if it := e.item(pv, "fresh"); it.Status != claudeImportNew {
		t.Errorf("fresh = %+v", it)
	}
	// Even with the tombstones gone, the history still says the names existed.
	for _, n := range []string{"gone", "undone"} {
		_ = os.Remove(filepath.Join(claude.ConfigDir(), "af-agent-memory", "projects", e.pid, agentMemTombDir, n))
	}
	if it := e.item(e.preview(), "gone"); it.Status != claudeImportForgotten {
		t.Errorf("without a tombstone, gone = %+v", it)
	}
	// Apply refuses them even with the right hash.
	out = e.apply(now.Add(time.Hour), "gone", "undone", "fresh")
	for _, n := range []string{"gone", "undone"} {
		if r := claudeImportResult(out, n); r.Result != "skipped" || r.Reason != "status_forgotten" {
			t.Errorf("%s = %+v", n, r)
		}
	}
	if r := claudeImportResult(out, "fresh"); r.Result != "imported" {
		t.Errorf("fresh = %+v", r)
	}
}

func TestClaudeImportChangedSinceAndCaps(t *testing.T) {
	e := newClaudeImportEnv(t)
	e.file("moving", "d", "user", "v1")
	pv := e.preview()
	stale := pv.byName["moving"].SourceHash
	e.file("moving", "d", "user", "v2")
	out, err := agentMemImportApply(agentMemImportReq{Project: e.pid, Slug: e.slug,
		Items: []agentMemImportReqItem{{Name: "moving", SourceHash: stale}, {Name: "moving", SourceHash: stale}, {Name: "absent"}, {Name: "Bad Name"}}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	reasons := []string{}
	for _, r := range out.Results {
		reasons = append(reasons, r.Result+"/"+r.Reason)
	}
	if got := strings.Join(reasons, " "); got != "skipped/changed_since_preview skipped/duplicate skipped/not_found skipped/bad_name" {
		t.Errorf("results = %s", got)
	}

	var many []agentMemImportReqItem
	for i := 0; i <= agentMemImportMaxItems; i++ {
		many = append(many, agentMemImportReqItem{Name: fmt.Sprintf("n%d", i)})
	}
	if _, err := agentMemImportApply(agentMemImportReq{Project: e.pid, Slug: e.slug, Items: many}, time.Now()); agentMemCode(err) != errCodeMemoryBadRequest {
		t.Errorf("51 items: %v", err)
	}
	if _, err := agentMemImportApply(agentMemImportReq{Project: e.pid, Slug: e.slug}, time.Now()); agentMemCode(err) != errCodeMemoryBadRequest {
		t.Errorf("0 items: %v", err)
	}
	if _, err := agentMemImportApply(agentMemImportReq{Project: "other-0123456789ab", Slug: e.slug,
		Items: []agentMemImportReqItem{{Name: "moving"}}}, time.Now()); agentMemCode(err) != errCodeMemoryConflict {
		t.Errorf("wrong project: %v", err)
	}
}

func TestClaudeImportSymlinkedDirectoriesRefused(t *testing.T) {
	e := newClaudeImportEnv(t)
	outside := filepath.Join(e.home, "outside")
	memoryWrite(t, filepath.Join(outside, "memory", "x.md"), "---\ndescription: d\n---\nb\n")
	projects := filepath.Join(claude.ConfigDir(), "projects")
	memoryMkdirAll(t, projects)
	if err := os.Symlink(outside, filepath.Join(projects, e.slug)); err != nil {
		t.Fatal(err)
	}
	if got := claudeImportListed(e.slug); got != nil {
		t.Errorf("a symlinked project directory was listed: %+v", got)
	}
	if _, err := agentMemImportPreviewFor(e.slug); agentMemCode(err) != errCodeMemoryNotFound {
		t.Errorf("preview through a symlink: %v", err)
	}
	// The memory directory itself a symlink.
	if err := os.Remove(filepath.Join(projects, e.slug)); err != nil {
		t.Fatal(err)
	}
	memoryMkdirAll(t, filepath.Join(projects, e.slug))
	if err := os.Symlink(filepath.Join(outside, "memory"), filepath.Join(projects, e.slug, "memory")); err != nil {
		t.Fatal(err)
	}
	if got := claudeImportListed(e.slug); got != nil {
		t.Errorf("a symlinked memory directory was listed: %+v", got)
	}
}

func TestClaudeImportHandlers(t *testing.T) {
	e := newClaudeImportEnv(t)
	e.file("via-http", "d", "user", "b")
	do := func(method, target, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		w := httptest.NewRecorder()
		switch {
		case method == http.MethodPost:
			HandleAgentMemoryClaudeApply(w, r)
		case strings.Contains(target, "preview"):
			HandleAgentMemoryClaudePreview(w, r)
		default:
			HandleAgentMemoryClaudeSources(w, r)
		}
		return w
	}
	hash := e.preview().byName["via-http"].SourceHash
	post := fmt.Sprintf(`{"project":%q,"slug":%q,"items":[{"name":"via-http","sourceHash":%q}]}`, e.pid, e.slug, hash)

	AgentMemoryEnabled = func() bool { return false }
	// Reading works with the switch off; writing does not.
	if w := do("GET", "/agents/memory/claude-import", ""); w.Code != 200 {
		t.Errorf("sources off: %d", w.Code)
	}
	if w := do("GET", "/agents/memory/claude-import/preview?slug="+e.slug, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"new"`) {
		t.Errorf("preview off: %d %s", w.Code, w.Body)
	}
	if w := do("POST", "/agents/memory/claude-import", post); w.Code != 403 || !strings.Contains(w.Body.String(), errCodeMemoryDisabled) {
		t.Errorf("apply off: %d %s", w.Code, w.Body)
	}
	if memoryHasCommits() {
		t.Errorf("a refused apply committed")
	}
	AgentMemoryEnabled = func() bool { return true }
	w := do("POST", "/agents/memory/claude-import", post)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"imported"`) {
		t.Errorf("apply on: %d %s", w.Code, w.Body)
	}
}

func TestAgentMemParseMetadataType(t *testing.T) {
	cases := map[string]string{
		"---\ndescription: d\nmetadata:\n  type: feedback\n---\nb\n":                        "feedback",
		"---\ndescription: d\ntype: user\nmetadata:\n  type: feedback\n---\nb\n":            "user",
		"---\ndescription: d\nmetadata:\n  other: 1\n  type: \"project\"\n---\nb\n":         "project",
		"---\ndescription: d\nother:\n  type: feedback\n---\nb\n":                           "",
		"---\ndescription: d\nmetadata:\n  type: feedback\nname: x\n  type: user\n---\nb\n": "feedback",
	}
	for in, want := range cases {
		e, ok := agentMemParse([]byte(in))
		if !ok || e.Type != want {
			t.Errorf("%q: type = %q, want %q", in, e.Type, want)
		}
	}
}

// claude may hold a memory under a worktree's own slug too; it maps to the same project.
func TestClaudeImportWorktreeSlugMapsToMainProject(t *testing.T) {
	e := newClaudeImportEnv(t)
	wtSlug := claude.ProjectKey(e.wt)
	e.raw(wtSlug, "w.md", "---\ndescription: d\n---\nb\n")
	s := claudeImportListed(wtSlug)
	if s == nil || s.Project == nil || s.Project.ID != e.pid {
		t.Errorf("worktree source = %+v", s)
	}
}
