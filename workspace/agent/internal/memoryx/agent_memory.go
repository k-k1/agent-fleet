package memoryx

// AF-owned agent memory (ADR 0108) — the store every agent kind reads and writes through the
// af MCP tools.
//
//	<claude config>/af-agent-memory/user/<name>.md               user-wide scope
//	<claude config>/af-agent-memory/projects/<id>/<name>.md      one project
//	<claude config>/af-agent-memory/projects/<id>/project.json   what <id> stands for
//
// Every change is published at once (ADR 0108 decision 8): the revision check, the secret scan
// and one commit under af/ in the 0022 history happen under memorySnapshotMu, so a change that
// is visible is also a commit with its author. The store is deliberately not a memoryRoot: the
// generic restore and import write a root back without a secret scan, which decision 9 forbids
// for this store.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/projcfg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

const (
	agentMemScopeUser    = "user"
	agentMemScopeProject = "project"

	// agentMemRepoPrefix is where the store lives inside af-memory.git, beside claude/ and codex/.
	agentMemRepoPrefix = "af"

	agentMemMaxBody        = 64 << 10
	agentMemMaxDescription = 300
	agentMemMaxKinds       = 16
	agentMemIndexCap       = 500
	agentMemSearchDefault  = 20
	agentMemSearchMax      = 50
	agentMemSnippetLines   = 3
	agentMemSnippetChars   = 200
	// agentMemUnknown is what an author AF cannot establish is recorded as; never a guess.
	agentMemUnknown = "unknown"
)

var (
	agentMemNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	agentMemKindRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	// agentMemTypes are the memory types claude's auto-memory uses, so the import keeps them.
	agentMemTypes = map[string]bool{"": true, "user": true, "feedback": true, "project": true, "reference": true}
)

func agentMemDir() string { return filepath.Join(claude.ConfigDir(), "af-agent-memory") }

// agentMemRoot is the store seen as a memoryRoot, so the copy into staging reuses the
// allowlist and symlink rules of memorySyncToStaging. It is never added to memoryRootDecls.
func agentMemRoot() memoryRoot {
	return memoryRoot{
		Kind: "af", Label: "Agent Fleet", Dir: agentMemDir(), RepoPrefix: agentMemRepoPrefix,
		Include: []string{"user/*.md", "projects/*/*.md", "projects/*/project.json"},
	}
}

// agentMemProject is the project a session works in (ADR 0108 decision 2).
type agentMemProject struct {
	ID      string `json:"id"`
	Root    string `json:"root"`
	VCS     string `json:"vcs"`
	Display string `json:"display"`
}

// agentMemCaller is who is asking: the author of a write and the scope of a read.
type agentMemCaller struct {
	Session string
	Kind    string
	Project *agentMemProject // nil: the session has no working copy, so user scope only
}

// agentMemResolveCaller looks the calling session up. An empty name is an author AF cannot
// establish; a name that is not a session is refused rather than recorded.
func agentMemResolveCaller(name string) (agentMemCaller, error) {
	c := agentMemCaller{Session: agentMemUnknown, Kind: agentMemUnknown}
	name = strings.TrimSpace(name)
	if name == "" {
		return c, nil
	}
	m, ok := session.ReadMeta(name)
	if !ok {
		return c, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "no session named %q", name)
	}
	c.Session = name
	if m.Kind != "" {
		c.Kind = m.Kind
	}
	c.Project = agentMemProjectFor(m.Dir)
	return c, nil
}

// agentMemProjectFor maps a session's working copy to its project. A worktree shares its main
// clone's memory; anything outside ~/repos (a shell in the home directory) has no project.
func agentMemProjectFor(dir string) *agentMemProject {
	if dir == "" {
		return nil
	}
	repos := gitx.ReposRoot()
	rel, err := filepath.Rel(repos, dir)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return nil
	}
	root, vcs := filepath.Clean(dir), projcfg.DetectVCS(dir)
	if vcs == projcfg.VCSGit {
		if parent := gitx.WorktreeParent(dir); parent != "" {
			root = filepath.Clean(parent)
		}
	}
	display := filepath.Base(root)
	if r, err := filepath.Rel(repos, root); err == nil && !strings.HasPrefix(r, "..") {
		display = filepath.ToSlash(r)
	}
	return &agentMemProject{ID: agentMemProjectID(root), Root: root, VCS: vcs, Display: display}
}

// agentMemProjectID keys a project by its root path: readable for a person browsing the store,
// and injective through the hash where two roots share a base name.
func agentMemProjectID(root string) string {
	sum := sha256.Sum256([]byte(root))
	base := strings.ToLower(filepath.Base(root))
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	name := strings.Trim(b.String(), "-.")
	if len(name) > 40 {
		name = name[:40]
	}
	if name == "" {
		name = "project"
	}
	return name + "-" + hex.EncodeToString(sum[:])[:12]
}

// agentMemScopeDir is the directory of one scope, relative to the store.
func agentMemScopeDir(scope string, c agentMemCaller) (string, error) {
	switch scope {
	case agentMemScopeUser:
		return "user", nil
	case agentMemScopeProject:
		if c.Project == nil {
			return "", memoryErrf(http.StatusBadRequest, errCodeMemoryNoProject,
				"this session has no working copy under ~/repos, so it has no project scope; use scope \"user\"")
		}
		return "projects/" + c.Project.ID, nil
	}
	return "", memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "scope must be %q or %q", agentMemScopeUser, agentMemScopeProject)
}

// agentMemScopes is the read order: the project first, because it is the more specific.
func agentMemScopes(c agentMemCaller) []string {
	if c.Project != nil {
		return []string{agentMemScopeProject, agentMemScopeUser}
	}
	return []string{agentMemScopeUser}
}

// agentMemEntry is one memory. The frontmatter keys are snake_case (ADR 0108 decision 3, the
// shape claude's memory files use); the wire is camelCase like the rest of the memory API.
type agentMemEntry struct {
	Name          string   `json:"name"`
	Scope         string   `json:"scope"`
	Description   string   `json:"description"`
	Type          string   `json:"type,omitempty"`
	Kinds         []string `json:"kinds,omitempty"`
	Revision      int      `json:"revision"`
	AuthorKind    string   `json:"authorKind"`
	AuthorSession string   `json:"authorSession"`
	Created       string   `json:"created"`
	Updated       string   `json:"updated"`
	Source        string   `json:"source,omitempty"`
	SourceHash    string   `json:"sourceHash,omitempty"`
	Body          string   `json:"body,omitempty"`
}

// agentMemRender writes an entry as frontmatter plus body. String values are written as
// JSON strings, which are valid YAML double-quoted scalars, so a colon or a quote in a
// description cannot break the file.
func agentMemRender(e agentMemEntry) []byte {
	var b bytes.Buffer
	q := func(s string) string { v, _ := json.Marshal(s); return string(v) }
	b.WriteString("---\n")
	b.WriteString("name: " + q(e.Name) + "\n")
	b.WriteString("description: " + q(e.Description) + "\n")
	if e.Type != "" {
		b.WriteString("type: " + q(e.Type) + "\n")
	}
	if len(e.Kinds) > 0 {
		ks := make([]string, len(e.Kinds))
		for i, k := range e.Kinds {
			ks[i] = q(k)
		}
		b.WriteString("kinds: [" + strings.Join(ks, ", ") + "]\n")
	}
	b.WriteString("revision: " + strconv.Itoa(e.Revision) + "\n")
	b.WriteString("author_kind: " + q(e.AuthorKind) + "\n")
	b.WriteString("author_session: " + q(e.AuthorSession) + "\n")
	b.WriteString("created: " + q(e.Created) + "\n")
	b.WriteString("updated: " + q(e.Updated) + "\n")
	if e.Source != "" {
		b.WriteString("source: " + q(e.Source) + "\n")
	}
	if e.SourceHash != "" {
		b.WriteString("source_hash: " + q(e.SourceHash) + "\n")
	}
	b.WriteString("---\n")
	b.WriteString(strings.TrimRight(e.Body, "\n"))
	b.WriteString("\n")
	return b.Bytes()
}

// agentMemParse reads a file agentMemRender wrote. It also accepts plain YAML scalars and
// skips keys it does not know (indented lines included), so a file a person edited by hand
// still loads.
func agentMemParse(b []byte) (agentMemEntry, bool) {
	var e agentMemEntry
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	rest, ok := strings.CutPrefix(s, "---\n")
	if !ok {
		return e, false
	}
	head, body, ok := strings.Cut(rest, "\n---\n")
	if !ok {
		head, ok = strings.CutSuffix(rest, "\n---")
		if !ok {
			return e, false
		}
	}
	unq := func(v string) string {
		v = strings.TrimSpace(v)
		if strings.HasPrefix(v, `"`) {
			var out string
			if json.Unmarshal([]byte(v), &out) == nil {
				return out
			}
		}
		if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
			return strings.ReplaceAll(v[1:len(v)-1], "''", "'")
		}
		return v
	}
	for _, line := range strings.Split(head, "\n") {
		if line == "" || line[0] == ' ' || line[0] == '\t' || line[0] == '#' {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "name":
			e.Name = unq(v)
		case "description":
			e.Description = unq(v)
		case "type":
			e.Type = unq(v)
		case "kinds":
			v = strings.TrimSpace(v)
			v = strings.TrimSuffix(strings.TrimPrefix(v, "["), "]")
			for _, k := range strings.Split(v, ",") {
				if k = unq(k); k != "" {
					e.Kinds = append(e.Kinds, k)
				}
			}
		case "revision":
			e.Revision, _ = strconv.Atoi(strings.TrimSpace(v))
		case "author_kind":
			e.AuthorKind = unq(v)
		case "author_session":
			e.AuthorSession = unq(v)
		case "created":
			e.Created = unq(v)
		case "updated":
			e.Updated = unq(v)
		case "source":
			e.Source = unq(v)
		case "source_hash":
			e.SourceHash = unq(v)
		}
	}
	e.Body = strings.TrimRight(body, "\n")
	return e, true
}

// agentMemLoadScope reads every memory of one scope. A file that does not parse is skipped:
// one hand-broken file must not take the index down for every kind.
func agentMemLoadScope(scope string, c agentMemCaller) ([]agentMemEntry, error) {
	rel, err := agentMemScopeDir(scope, c)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(agentMemDir(), filepath.FromSlash(rel))
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []agentMemEntry
	for _, d := range ents {
		name, ok := strings.CutSuffix(d.Name(), ".md")
		if !ok || !d.Type().IsRegular() || !agentMemNameRe.MatchString(name) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, d.Name()))
		if err != nil {
			continue
		}
		e, ok := agentMemParse(b)
		if !ok {
			continue
		}
		e.Name, e.Scope = name, scope // the file name is the identity, not the frontmatter
		out = append(out, e)
	}
	return out, nil
}

// agentMemAppliesTo says whether a memory is meant for the caller's kind. An unknown caller
// sees everything rather than nothing.
func agentMemAppliesTo(e agentMemEntry, kind string) bool {
	if len(e.Kinds) == 0 || kind == agentMemUnknown {
		return true
	}
	for _, k := range e.Kinds {
		if k == kind {
			return true
		}
	}
	return false
}

// agentMemIndex is the answer to memory_index: no bodies, newest first.
type agentMemIndex struct {
	Project   *agentMemProject `json:"project"`
	Entries   []agentMemEntry  `json:"entries"`
	Truncated bool             `json:"truncated,omitempty"`
}

func agentMemListIndex(c agentMemCaller) (agentMemIndex, error) {
	out := agentMemIndex{Project: c.Project, Entries: []agentMemEntry{}}
	for _, scope := range agentMemScopes(c) {
		es, err := agentMemLoadScope(scope, c)
		if err != nil {
			return out, err
		}
		for _, e := range es {
			if agentMemAppliesTo(e, c.Kind) {
				e.Body = ""
				out.Entries = append(out.Entries, e)
			}
		}
	}
	sort.SliceStable(out.Entries, func(i, j int) bool { return out.Entries[i].Updated > out.Entries[j].Updated })
	if len(out.Entries) > agentMemIndexCap {
		out.Entries, out.Truncated = out.Entries[:agentMemIndexCap], true
	}
	return out, nil
}

// agentMemHit is one search result: the entry without its body, and the lines that matched.
type agentMemHit struct {
	agentMemEntry
	Snippets []string `json:"snippets"`
}

// agentMemSearch matches every whitespace-separated term, case-insensitively, against name,
// description and body. Plain substring matching is enough at the size measured for one
// project (469 files, ADR 0108); ranking is #1558's.
func agentMemSearch(c agentMemCaller, query string, limit int) ([]agentMemHit, error) {
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) == 0 {
		return nil, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "query is required")
	}
	if limit <= 0 {
		limit = agentMemSearchDefault
	}
	limit = min(limit, agentMemSearchMax)
	hits := []agentMemHit{}
	for _, scope := range agentMemScopes(c) {
		es, err := agentMemLoadScope(scope, c)
		if err != nil {
			return nil, err
		}
		for _, e := range es {
			if !agentMemAppliesTo(e, c.Kind) {
				continue
			}
			hay := strings.ToLower(e.Name + "\n" + e.Description + "\n" + e.Body)
			all := true
			for _, t := range terms {
				if !strings.Contains(hay, t) {
					all = false
					break
				}
			}
			if !all {
				continue
			}
			h := agentMemHit{agentMemEntry: e, Snippets: []string{}}
			for _, line := range strings.Split(e.Body, "\n") {
				low := strings.ToLower(line)
				for _, t := range terms {
					if strings.Contains(low, t) {
						line = strings.TrimSpace(line)
						if len(line) > agentMemSnippetChars {
							line = agentMemTruncate(line, agentMemSnippetChars)
						}
						h.Snippets = append(h.Snippets, line)
						break
					}
				}
				if len(h.Snippets) >= agentMemSnippetLines {
					break
				}
			}
			h.Body = ""
			hits = append(hits, h)
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Updated > hits[j].Updated })
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// agentMemTruncate cuts at a rune boundary so a snippet of Japanese text stays valid UTF-8.
func agentMemTruncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && (s[n]&0xC0) == 0x80 {
		n--
	}
	return s[:n] + "…"
}

// agentMemRead returns one memory with its body. With no scope it looks in the project first.
func agentMemRead(c agentMemCaller, scope, name string) (agentMemEntry, error) {
	if !agentMemNameRe.MatchString(name) {
		return agentMemEntry{}, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "invalid memory name %q", name)
	}
	scopes := agentMemScopes(c)
	if scope != "" {
		scopes = []string{scope}
	}
	for _, s := range scopes {
		e, ok, err := agentMemLoadOne(c, s, name)
		if err != nil {
			return e, err
		}
		if ok {
			return e, nil
		}
	}
	return agentMemEntry{}, memoryErrf(http.StatusNotFound, errCodeMemoryNotFound, "no memory named %q", name)
}

func agentMemLoadOne(c agentMemCaller, scope, name string) (agentMemEntry, bool, error) {
	rel, err := agentMemScopeDir(scope, c)
	if err != nil {
		return agentMemEntry{}, false, err
	}
	b, err := os.ReadFile(filepath.Join(agentMemDir(), filepath.FromSlash(rel), name+".md"))
	if err != nil {
		if os.IsNotExist(err) {
			return agentMemEntry{}, false, nil
		}
		return agentMemEntry{}, false, err
	}
	e, ok := agentMemParse(b)
	if !ok {
		return agentMemEntry{}, false, fmt.Errorf("memory %s/%s is not in the expected format", rel, name)
	}
	e.Name, e.Scope = name, scope
	return e, true, nil
}

// agentMemSaveReq is a create or update. Revision is the one the caller read: 0 creates, and
// anything else must match the current file.
type agentMemSaveReq struct {
	Session     string   `json:"session"`
	Scope       string   `json:"scope"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Type        string   `json:"type"`
	Kinds       []string `json:"kinds"`
	Body        string   `json:"body"`
	Revision    int      `json:"revision"`
}

// agentMemForgetReq removes a memory from what is published; history keeps it.
type agentMemForgetReq struct {
	Session  string `json:"session"`
	Scope    string `json:"scope"`
	Name     string `json:"name"`
	Revision int    `json:"revision"`
}

// agentMemWriteResult is what a save or forget answers.
type agentMemWriteResult struct {
	Name     string `json:"name"`
	Scope    string `json:"scope"`
	Revision int    `json:"revision"`
	Commit   string `json:"commit,omitempty"`
	Created  bool   `json:"created,omitempty"`
	Deleted  bool   `json:"deleted,omitempty"`
}

// agentMemSecretErr carries the scan's findings to the REST layer. The agent gets the rule and
// the line so it can rewrite the memory; the value is never returned (ADR 0108 decision 9).
type agentMemSecretErr struct{ Findings []memorySecretFinding }

func (e *agentMemSecretErr) Error() string { return "the memory contains possible secrets" }

func agentMemValidate(req *agentMemSaveReq) error {
	bad := func(format string, args ...any) error {
		return memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, format, args...)
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Description = strings.TrimSpace(req.Description)
	req.Type = strings.TrimSpace(req.Type)
	req.Body = strings.TrimSpace(req.Body)
	switch {
	case !agentMemNameRe.MatchString(req.Name):
		return bad("name must be lower-case letters, digits and hyphens (at most 64), got %q", req.Name)
	case req.Description == "":
		return bad("description is required")
	case strings.ContainsAny(req.Description, "\r\n"):
		return bad("description must be one line")
	case len(req.Description) > agentMemMaxDescription:
		return bad("description is longer than %d bytes", agentMemMaxDescription)
	case !agentMemTypes[req.Type]:
		return bad("type must be one of user, feedback, project, reference")
	case req.Body == "":
		return bad("body is required")
	case len(req.Body) > agentMemMaxBody:
		return memoryErrf(http.StatusRequestEntityTooLarge, errCodeMemoryTooLarge, "body is larger than %d bytes", agentMemMaxBody)
	case len(req.Kinds) > agentMemMaxKinds:
		return bad("at most %d kinds", agentMemMaxKinds)
	case req.Revision < 0:
		return bad("revision must not be negative")
	}
	seen := map[string]bool{}
	kinds := []string{}
	for _, k := range req.Kinds {
		k = strings.TrimSpace(k)
		if !agentMemKindRe.MatchString(k) {
			return bad("invalid kind %q", k)
		}
		if !seen[k] {
			seen[k] = true
			kinds = append(kinds, k)
		}
	}
	req.Kinds = kinds
	return nil
}

// agentMemDefaultScope is the project when there is one: what an agent learns is usually
// about the code in front of it.
func agentMemDefaultScope(scope string, c agentMemCaller) string {
	if scope != "" {
		return scope
	}
	if c.Project != nil {
		return agentMemScopeProject
	}
	return agentMemScopeUser
}

// agentMemSave publishes a create or update (ADR 0108 decisions 4, 8 and 9).
func agentMemSave(c agentMemCaller, req agentMemSaveReq, now time.Time) (agentMemWriteResult, error) {
	if err := agentMemValidate(&req); err != nil {
		return agentMemWriteResult{}, err
	}
	req.Scope = agentMemDefaultScope(req.Scope, c)
	rel, err := agentMemScopeDir(req.Scope, c)
	if err != nil {
		return agentMemWriteResult{}, err
	}

	memorySnapshotMu.Lock()
	defer memorySnapshotMu.Unlock()

	cur, exists, err := agentMemLoadOne(c, req.Scope, req.Name)
	if err != nil {
		return agentMemWriteResult{}, err
	}
	switch {
	case exists && req.Revision == 0 && cur.Revision != 0:
		return agentMemWriteResult{}, memoryErrf(http.StatusConflict, errCodeMemoryConflict,
			"memory %q already exists at revision %d; read it and pass that revision to update it", req.Name, cur.Revision)
	case exists && req.Revision != cur.Revision:
		return agentMemWriteResult{}, memoryErrf(http.StatusConflict, errCodeMemoryConflict,
			"memory %q is at revision %d, not %d; read it again and rewrite from the current text", req.Name, cur.Revision, req.Revision)
	case !exists && req.Revision != 0:
		return agentMemWriteResult{}, memoryErrf(http.StatusNotFound, errCodeMemoryNotFound,
			"memory %q does not exist (it may have been forgotten); save it with revision 0 to create it", req.Name)
	}

	stamp := now.UTC().Format(time.RFC3339)
	e := agentMemEntry{
		Name: req.Name, Scope: req.Scope, Description: req.Description, Type: req.Type,
		Kinds: req.Kinds, Revision: cur.Revision + 1, AuthorKind: c.Kind, AuthorSession: c.Session,
		Created: stamp, Updated: stamp, Body: req.Body,
	}
	if exists {
		e.Created, e.Source, e.SourceHash = cur.Created, cur.Source, cur.SourceHash
	}
	data := agentMemRender(e)
	repoPath := agentMemRepoPrefix + "/" + rel + "/" + req.Name + ".md"
	if f := memoryScanContent(repoPath, data); len(f) > 0 {
		return agentMemWriteResult{}, &agentMemSecretErr{Findings: f}
	}

	abs := filepath.Join(agentMemDir(), filepath.FromSlash(rel), req.Name+".md")
	prev, _ := os.ReadFile(abs)
	if c.Project != nil && req.Scope == agentMemScopeProject {
		if err := agentMemWriteProjectInfo(*c.Project); err != nil {
			return agentMemWriteResult{}, err
		}
	}
	if err := agentMemWriteFile(abs, data); err != nil {
		return agentMemWriteResult{}, err
	}
	op := "update"
	if !exists {
		op = "create"
	}
	rev, err := agentMemCommitLocked(op, repoPath, c, now)
	if err != nil {
		agentMemUndo(abs, prev, exists)
		return agentMemWriteResult{}, err
	}
	return agentMemWriteResult{Name: e.Name, Scope: e.Scope, Revision: e.Revision, Commit: rev, Created: !exists}, nil
}

// agentMemForget removes a memory from what is published. Its text stays in history; purging
// a secret from history is a separate, member-only operation (ADR 0108 open question 3).
func agentMemForget(c agentMemCaller, req agentMemForgetReq, now time.Time) (agentMemWriteResult, error) {
	req.Name = strings.TrimSpace(req.Name)
	if !agentMemNameRe.MatchString(req.Name) {
		return agentMemWriteResult{}, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "invalid memory name %q", req.Name)
	}
	if req.Revision < 0 {
		return agentMemWriteResult{}, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "revision must not be negative")
	}
	req.Scope = agentMemDefaultScope(req.Scope, c)
	rel, err := agentMemScopeDir(req.Scope, c)
	if err != nil {
		return agentMemWriteResult{}, err
	}

	memorySnapshotMu.Lock()
	defer memorySnapshotMu.Unlock()

	cur, exists, err := agentMemLoadOne(c, req.Scope, req.Name)
	if err != nil {
		return agentMemWriteResult{}, err
	}
	if !exists {
		return agentMemWriteResult{}, memoryErrf(http.StatusNotFound, errCodeMemoryNotFound, "no memory named %q in scope %s", req.Name, req.Scope)
	}
	if cur.Revision != req.Revision {
		return agentMemWriteResult{}, memoryErrf(http.StatusConflict, errCodeMemoryConflict,
			"memory %q is at revision %d, not %d; read it again before forgetting it", req.Name, cur.Revision, req.Revision)
	}
	abs := filepath.Join(agentMemDir(), filepath.FromSlash(rel), req.Name+".md")
	prev, err := os.ReadFile(abs)
	if err != nil {
		return agentMemWriteResult{}, err
	}
	if err := os.Remove(abs); err != nil {
		return agentMemWriteResult{}, err
	}
	repoPath := agentMemRepoPrefix + "/" + rel + "/" + req.Name + ".md"
	rev, err := agentMemCommitLocked("forget", repoPath, c, now)
	if err != nil {
		agentMemUndo(abs, prev, true)
		return agentMemWriteResult{}, err
	}
	return agentMemWriteResult{Name: req.Name, Scope: req.Scope, Revision: cur.Revision, Commit: rev, Deleted: true}, nil
}

// agentMemWriteFile replaces a file by rename, so a reader in another kind never sees half a
// memory.
func agentMemWriteFile(abs string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(abs), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), abs)
}

// agentMemUndo puts the live file back when the commit failed: a change that is visible but
// has no commit would have no author and no way back.
func agentMemUndo(abs string, prev []byte, existed bool) {
	if existed {
		_ = agentMemWriteFile(abs, prev)
		return
	}
	_ = os.Remove(abs)
}

// agentMemWriteProjectInfo records what a project id stands for, for a person reading the
// store and for the Console. Written once; the id is derived from Root, so it cannot drift.
func agentMemWriteProjectInfo(p agentMemProject) error {
	abs := filepath.Join(agentMemDir(), "projects", p.ID, "project.json")
	if _, err := os.Stat(abs); err == nil {
		return nil
	}
	b, _ := json.MarshalIndent(p, "", "  ")
	return agentMemWriteFile(abs, append(b, '\n'))
}

// agentMemCommitLocked records one change as one commit under af/, with memorySnapshotMu held.
// Only af/ is staged and committed, so claude's and codex's live changes wait for their own
// snapshot instead of being attributed to this author.
func agentMemCommitLocked(op, repoPath string, c agentMemCaller, now time.Time) (string, error) {
	if err := memoryEnsureRepo(); err != nil {
		return "", err
	}
	if _, err := memorySyncToStaging(agentMemRoot(), memoryStagingDir()); err != nil {
		return "", err
	}
	if _, err := memoryGitRun("add", "-A", "--", agentMemRepoPrefix); err != nil {
		return "", fmt.Errorf("stage agent memory: %w", err)
	}
	msg := fmt.Sprintf("agent-memory: %s %s (%s)\n\nAF-Trigger: %s\nAF-Op: %s\nAF-Memory: %s\nAF-Author-Kind: %s\nAF-Author-Session: %s\n",
		op, strings.TrimPrefix(repoPath, agentMemRepoPrefix+"/"), now.Format(time.RFC3339),
		memoryTriggerAgentMemory, op, repoPath, c.Kind, c.Session)
	// --only with a pathspec commits af/ alone even if something else is staged.
	if _, err := memoryGitRun("commit", "--quiet", "--no-verify", "-m", msg, "--", agentMemRepoPrefix); err != nil {
		return "", fmt.Errorf("commit agent memory: %w", err)
	}
	rev, err := memoryGitRun("rev-parse", memoryBranch)
	if err != nil {
		return "", err
	}
	_, _ = memoryGitRun("gc", "--auto", "--quiet")
	return rev, nil
}
