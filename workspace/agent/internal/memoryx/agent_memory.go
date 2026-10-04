package memoryx

// AF-owned agent memory (ADR 0108) — the store every agent kind reads and writes through the
// af MCP tools.
//
//	<claude config>/af-agent-memory/user/<name>.md                 user-wide scope
//	<claude config>/af-agent-memory/projects/<id>/<name>.md        one project
//	<claude config>/af-agent-memory/projects/<id>/project.json     what <id> stands for
//	<claude config>/af-agent-memory/<scope dir>/.forgotten/<name>  last revision of a forgotten one
//
// Every change is published at once (ADR 0108 decision 8): the revision check, the secret scan
// and one commit under af/ in the 0022 history happen with agentMemMu and memorySnapshotMu
// held, and readers take agentMemMu too, so nothing is visible that has no commit. The store is
// deliberately not a memoryRoot: the generic restore and import write a root back without a
// secret scan, which decision 9 forbids for this store.
//
// The store sits in a directory the agents' own shells can write, so a file is trusted for
// nothing: every file is scanned before it is shown, symlinks are refused on both read and
// write, and a write stages only the file it changed.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
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
	agentMemTombDir    = ".forgotten"

	agentMemMaxBody        = 64 << 10
	agentMemMaxFile        = 4 * agentMemMaxBody
	agentMemMaxLine        = 4 << 10
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

	// agentMemMu orders readers against a write: a write holds it from the file change to the
	// commit, so a reader never sees a change that is about to be rolled back.
	agentMemMu sync.RWMutex
)

func agentMemDir() string { return filepath.Join(claude.ConfigDir(), "af-agent-memory") }

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
// establish; a name that is not a session is refused rather than recorded. The name is never
// echoed back: whatever a caller put there must not come back out in an error.
func agentMemResolveCaller(name string) (agentMemCaller, error) {
	c := agentMemCaller{Session: agentMemUnknown, Kind: agentMemUnknown}
	name = strings.TrimSpace(name)
	if name == "" {
		return c, nil
	}
	// ValidName first: ReadMeta joins the name into a path.
	if !session.ValidName(name) {
		return c, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "session is not a valid session name")
	}
	m, ok := session.ReadMeta(name)
	if !ok || m.Name != name {
		return c, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "session does not name a session in this workspace")
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
	root, key, vcs := filepath.Clean(dir), filepath.Clean(dir), projcfg.DetectVCS(dir)
	if vcs == projcfg.VCSGit {
		if r, k := agentMemGitIdentity(dir); k != "" {
			root, key = r, k
		}
	}
	display := filepath.Base(root)
	if r, err := filepath.Rel(repos, root); err == nil && !strings.HasPrefix(r, "..") {
		display = filepath.ToSlash(r)
	}
	return &agentMemProject{ID: agentMemProjectID(key), Root: root, VCS: vcs, Display: display}
}

// agentMemGitIdentity returns the repository's main working tree (for display) and its key:
// the absolute git-common-dir, which every worktree of one repository shares and no two
// repositories do. The parent of git-common-dir is not the key: with --separate-git-dir or in a
// submodule it is a metadata directory that several repositories can share.
func agentMemGitIdentity(dir string) (root, key string) {
	common, err := gitx.Run(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || common == "" {
		return "", ""
	}
	top, err := gitx.Run(dir, "rev-parse", "--show-toplevel")
	if err != nil || top == "" {
		return "", ""
	}
	root = filepath.Clean(top)
	gitDir, err := gitx.Run(dir, "rev-parse", "--absolute-git-dir")
	if err == nil && filepath.Clean(gitDir) != filepath.Clean(common) {
		// A linked worktree: the main one is git's first entry, when it is a real checkout.
		if out, err := gitx.Run(dir, "worktree", "list", "--porcelain"); err == nil {
			first, _, _ := strings.Cut(out, "\n")
			if p, ok := strings.CutPrefix(first, "worktree "); ok {
				if _, err := os.Lstat(filepath.Join(p, ".git")); err == nil {
					root = filepath.Clean(p)
				}
			}
		}
	}
	return root, filepath.Clean(common)
}

// agentMemProjectID derives the whole id from key alone, so every checkout of one repository
// (a bare repository's worktrees included) gets the same id. The readable prefix is the
// repository's own name: for ".../app/.git" it is "app", for ".../app.git" it is "app".
func agentMemProjectID(key string) string {
	sum := sha256.Sum256([]byte(key))
	base := filepath.Base(key)
	if base == ".git" {
		base = filepath.Base(filepath.Dir(key))
	}
	base = strings.ToLower(strings.TrimSuffix(base, ".git"))
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

// agentMemErrSymlink is a store path that is not a plain file in a plain directory.
var agentMemErrSymlink = errors.New("agent memory store path is a symlink or not a regular entry")

// agentMemCheckDir refuses a symlink anywhere from the store root down to rel, so neither a
// read nor a write can leave the store. With create, missing directories are made.
func agentMemCheckDir(rel string, create bool) (string, error) {
	cur := agentMemDir()
	segs := []string{""}
	if rel != "" {
		segs = append(segs, strings.Split(rel, "/")...)
	}
	for i, seg := range segs {
		if i > 0 {
			cur = filepath.Join(cur, seg)
		}
		st, err := os.Lstat(cur)
		switch {
		case err == nil && st.IsDir():
			continue
		case err == nil:
			return "", agentMemErrSymlink
		case os.IsNotExist(err) && create:
			if i == 0 {
				// The store's parent is outside the store and may not exist yet on a fresh mount.
				if err := os.MkdirAll(filepath.Dir(cur), 0o700); err != nil {
					return "", err
				}
			}
			if err := os.Mkdir(cur, 0o700); err != nil && !os.IsExist(err) {
				return "", err
			}
			if st, err := os.Lstat(cur); err != nil || !st.IsDir() {
				return "", agentMemErrSymlink
			}
		default:
			return "", err
		}
	}
	return cur, nil
}

// agentMemReadFile reads a regular file without following a symlink at the leaf.
// ok=false: it does not exist.
func agentMemReadFile(abs string) ([]byte, bool, error) {
	// O_NONBLOCK: opening a FIFO for reading would otherwise wait for a writer, with agentMemMu
	// held. It has no effect on a regular file.
	f, err := os.OpenFile(abs, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		if errors.Is(err, syscall.ELOOP) {
			return nil, false, agentMemErrSymlink
		}
		return nil, false, err
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
		return nil, false, agentMemErrSymlink
	}
	b, err := io.ReadAll(io.LimitReader(f, agentMemMaxFile+1))
	if err != nil {
		return nil, true, err
	}
	if len(b) > agentMemMaxFile {
		// Never return a truncated file: it would be shown as the memory, or written back as
		// the original by a rollback.
		return nil, true, agentMemErrTooLarge
	}
	return b, true, nil
}

// agentMemErrTooLarge is a stored file larger than any memory a save can produce.
var agentMemErrTooLarge = errors.New("agent memory file is larger than the store accepts")

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
// skips keys it does not know (indented lines included, except `type` under `metadata:`), so a file a person edited by hand
// still loads. A missing or invalid revision reads as 1, never 0: 0 means "create", and a
// hand-made file must not be overwritable by a blind create.
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
	// claude writes its memory type as `metadata:` with an indented `type:`; a top-level type wins.
	inMeta, metaType, topType := false, "", false
	for _, line := range strings.Split(head, "\n") {
		if line == "" || line[0] == '#' {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			if k, v, ok := strings.Cut(strings.TrimSpace(line), ":"); ok && inMeta && strings.TrimSpace(k) == "type" {
				metaType = unq(v)
			}
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		inMeta = strings.TrimSpace(k) == "metadata" && strings.TrimSpace(v) == ""
		switch strings.TrimSpace(k) {
		case "name":
			e.Name = unq(v)
		case "description":
			e.Description = unq(v)
		case "type":
			e.Type, topType = unq(v), true
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
	if !topType {
		e.Type = metaType
	}
	if e.Revision < 1 {
		e.Revision = 1
	}
	e.Body = strings.TrimRight(body, "\n")
	return e, true
}

// agentMemScanText scans text the way a write is judged: NUL and over-long lines are refused
// outright, because the scanner cannot vouch for them (it skips binary, and a reader is not
// helped by a 4 KiB line in a memory).
func agentMemScanText(path, text string) []memorySecretFinding {
	if strings.IndexByte(text, 0) >= 0 {
		return []memorySecretFinding{{Path: path, Rule: "nul-byte", Hint: "…"}}
	}
	for i, line := range strings.Split(text, "\n") {
		if len(line) > agentMemMaxLine {
			return []memorySecretFinding{{Path: path, Line: i + 1, Rule: "line-too-long", Hint: "…"}}
		}
	}
	return memoryScanContent(path, []byte(text))
}

// agentMemScanCache remembers the verdict on a stored file by its content and the name it is
// shown under, so the index does not rescan every file on every call.
var agentMemScanCache sync.Map // sha256(raw) + "\x00" + name -> bool (true = clean)

// agentMemPublished is every value of an entry a reader is shown, decoded. It is what gets
// scanned: the raw file can hide a value behind a JSON escape (\u0041) or an escaped quote.
func agentMemPublished(e agentMemEntry) string {
	return strings.Join(append([]string{e.Name, e.Scope, e.Description, e.Type,
		e.AuthorKind, e.AuthorSession, e.Created, e.Updated, e.Source, e.SourceHash, e.Body}, e.Kinds...), "\n")
}

// agentMemClean says whether a stored file may be shown (ADR 0108 decision 9: what already
// exists is scanned before it is first exposed): both the raw file and the decoded values it
// would be shown as. A hand-edited file that fails stays on disk, withheld, for the member to fix.
func agentMemClean(raw []byte, e agentMemEntry) bool {
	sum := sha256.Sum256(raw)
	key := hex.EncodeToString(sum[:]) + "\x00" + e.Scope + "\x00" + e.Name
	if v, ok := agentMemScanCache.Load(key); ok {
		return v.(bool)
	}
	clean := len(agentMemScanText("", string(raw))) == 0 && len(agentMemScanText("", agentMemPublished(e))) == 0
	agentMemScanCache.Store(key, clean)
	return clean
}

// agentMemLoaded is one stored file as read: its entry, or why it is withheld.
type agentMemLoaded struct {
	Entry    agentMemEntry
	Withheld bool
}

// agentMemLoadScope reads every memory of one scope. A file that does not parse, is not a
// regular file or fails the scan is not shown: one bad file must not take the index down for
// every kind, nor leak through it.
func agentMemLoadScope(scope string, c agentMemCaller) ([]agentMemEntry, int, error) {
	rel, err := agentMemScopeDir(scope, c)
	if err != nil {
		return nil, 0, err
	}
	dir, err := agentMemCheckDir(rel, false)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0, err
	}
	var out []agentMemEntry
	withheld := 0
	for _, d := range ents {
		name, ok := strings.CutSuffix(d.Name(), ".md")
		if !ok || !d.Type().IsRegular() || !agentMemNameRe.MatchString(name) {
			continue
		}
		l, ok, err := agentMemLoadFile(filepath.Join(dir, d.Name()), scope, name)
		if err != nil {
			// There, but unreadable, oversized or malformed: counted, never named.
			withheld++
			continue
		}
		if !ok {
			continue // gone since the listing
		}
		if l.Withheld {
			withheld++
			continue
		}
		out = append(out, l.Entry)
	}
	return out, withheld, nil
}

func agentMemLoadFile(abs, scope, name string) (agentMemLoaded, bool, error) {
	raw, ok, err := agentMemReadFile(abs)
	if err != nil || !ok {
		return agentMemLoaded{}, ok, err
	}
	e, ok := agentMemParse(raw)
	if !ok {
		return agentMemLoaded{}, false, fmt.Errorf("memory file is not in the expected format")
	}
	e.Name, e.Scope = name, scope // the file name is the identity, not the frontmatter
	if !agentMemClean(raw, e) {
		return agentMemLoaded{Withheld: true}, true, nil
	}
	return agentMemLoaded{Entry: e}, true, nil
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
	// Withheld counts files left out because they failed the secret scan or are malformed
	// beyond reading; their names are not shown either.
	Withheld int `json:"withheld,omitempty"`
}

func agentMemListIndex(c agentMemCaller) (agentMemIndex, error) {
	agentMemMu.RLock()
	defer agentMemMu.RUnlock()
	out := agentMemIndex{Project: c.Project, Entries: []agentMemEntry{}}
	for _, scope := range agentMemScopes(c) {
		es, withheld, err := agentMemLoadScope(scope, c)
		if err != nil {
			return out, err
		}
		out.Withheld += withheld
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
	agentMemMu.RLock()
	defer agentMemMu.RUnlock()
	hits := []agentMemHit{}
	for _, scope := range agentMemScopes(c) {
		es, _, err := agentMemLoadScope(scope, c)
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
						h.Snippets = append(h.Snippets, agentMemTruncate(strings.TrimSpace(line), agentMemSnippetChars))
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
	if err := agentMemCheckName(name); err != nil {
		return agentMemEntry{}, err
	}
	scopes := agentMemScopes(c)
	if scope != "" {
		scopes = []string{scope}
	}
	agentMemMu.RLock()
	defer agentMemMu.RUnlock()
	for _, s := range scopes {
		l, ok, err := agentMemLoadOne(c, s, name)
		if err != nil {
			return agentMemEntry{}, err
		}
		if !ok {
			continue
		}
		if l.Withheld {
			return agentMemEntry{}, memoryErrf(http.StatusUnprocessableEntity, errCodeMemorySecretDetected,
				"this memory is withheld: its file looks like it contains a secret or cannot be checked; your user has to fix the file")
		}
		return l.Entry, nil
	}
	return agentMemEntry{}, memoryErrf(http.StatusNotFound, errCodeMemoryNotFound, "no memory by that name")
}

func agentMemLoadOne(c agentMemCaller, scope, name string) (agentMemLoaded, bool, error) {
	rel, err := agentMemScopeDir(scope, c)
	if err != nil {
		return agentMemLoaded{}, false, err
	}
	dir, err := agentMemCheckDir(rel, false)
	if err != nil {
		if os.IsNotExist(err) {
			return agentMemLoaded{}, false, nil
		}
		return agentMemLoaded{}, false, err
	}
	return agentMemLoadFile(filepath.Join(dir, name+".md"), scope, name)
}

// agentMemCheckName is the gate every name passes before it reaches a path: the slug rule, then
// the secret scan, so a token-shaped name never gets as far as an error that quotes a path.
func agentMemCheckName(name string) error {
	if !agentMemNameRe.MatchString(name) {
		return agentMemBadName()
	}
	if len(agentMemScanText("name", name)) > 0 {
		return memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "name looks like a secret; choose a descriptive name")
	}
	return nil
}

func agentMemBadName() error {
	return memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "name must be lower-case letters, digits and hyphens, at most 64")
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

// agentMemValidate checks a save before anything else. Its messages name the field and the
// rule, never the value: a value refused here has not been scanned yet.
func agentMemValidate(req *agentMemSaveReq) error {
	bad := func(msg string) error { return memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "%s", msg) }
	req.Name = strings.TrimSpace(req.Name)
	req.Description = strings.TrimSpace(req.Description)
	req.Type = strings.TrimSpace(req.Type)
	req.Body = strings.TrimSpace(req.Body)
	switch {
	case !agentMemNameRe.MatchString(req.Name):
		return agentMemBadName()
	case req.Description == "":
		return bad("description is required")
	case strings.ContainsAny(req.Description, "\r\n"):
		return bad("description must be one line")
	case len(req.Description) > agentMemMaxDescription:
		return bad(fmt.Sprintf("description is longer than %d bytes", agentMemMaxDescription))
	case !agentMemTypes[req.Type]:
		return bad("type must be one of user, feedback, project, reference")
	case req.Body == "":
		return bad("body is required")
	case len(req.Body) > agentMemMaxBody:
		return memoryErrf(http.StatusRequestEntityTooLarge, errCodeMemoryTooLarge, "body is larger than %d bytes", agentMemMaxBody)
	case len(req.Kinds) > agentMemMaxKinds:
		return bad(fmt.Sprintf("at most %d kinds", agentMemMaxKinds))
	case req.Revision < 0:
		return bad("revision must not be negative")
	}
	seen := map[string]bool{}
	kinds := []string{}
	for _, k := range req.Kinds {
		k = strings.TrimSpace(k)
		if !agentMemKindRe.MatchString(k) {
			return bad("each kind must be a lower-case agent kind such as claude or codex")
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

// agentMemTombRevision is the last revision a forgotten memory had, so a re-created one never
// reuses a revision a stale reader may still hold. An unreadable tombstone is an error, never
// revision 0: guessing low is exactly the reuse it exists to prevent.
func agentMemTombRevision(rel, name string) (int, error) {
	dir, err := agentMemCheckDir(rel+"/"+agentMemTombDir, false)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	b, ok, err := agentMemReadFile(filepath.Join(dir, name))
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || n < 0 {
		return 0, fmt.Errorf("tombstone of a forgotten memory is not a revision")
	}
	return n, nil
}

// agentMemWriteTomb records the revision a memory is forgotten at. It is written before the
// forget is committed, so a forget that succeeded always has one.
func agentMemWriteTomb(rel, name string, rev int) error {
	dir, err := agentMemCheckDir(rel+"/"+agentMemTombDir, true)
	if err != nil {
		return err
	}
	return agentMemWriteFile(filepath.Join(dir, name), []byte(strconv.Itoa(rev)+"\n"))
}

func agentMemRemoveTomb(rel, name string) {
	if dir, err := agentMemCheckDir(rel+"/"+agentMemTombDir, false); err == nil {
		_ = os.Remove(filepath.Join(dir, name))
	}
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
	// The raw fields are scanned, not only the rendered file: rendering escapes quotes, which
	// would hide `password: "…"` in a description from the rule that looks for it.
	// Finding paths are fixed field names: a path built from the name would hand back a secret
	// the name itself carries.
	var findings []memorySecretFinding
	findings = append(findings, agentMemScanText("name", req.Name)...)
	findings = append(findings, agentMemScanText("description", req.Description)...)
	findings = append(findings, agentMemScanText("body", req.Body)...)
	if len(findings) > 0 {
		return agentMemWriteResult{}, &agentMemSecretErr{Findings: findings}
	}

	agentMemMu.Lock()
	defer agentMemMu.Unlock()
	memorySnapshotMu.Lock()
	defer memorySnapshotMu.Unlock()

	dir, err := agentMemCheckDir(rel, true)
	if err != nil {
		return agentMemWriteResult{}, err
	}
	cur, exists, err := agentMemLoadFile(filepath.Join(dir, req.Name+".md"), req.Scope, req.Name)
	if err != nil {
		return agentMemWriteResult{}, err
	}
	if cur.Withheld {
		return agentMemWriteResult{}, memoryErrf(http.StatusConflict, errCodeMemoryConflict,
			"a withheld file already has this name; choose another name until your user fixes it")
	}
	base := cur.Entry.Revision
	switch {
	case exists && req.Revision == 0:
		return agentMemWriteResult{}, memoryErrf(http.StatusConflict, errCodeMemoryConflict,
			"the memory already exists at revision %d; read it and pass that revision to update it", base)
	case exists && req.Revision != base:
		return agentMemWriteResult{}, memoryErrf(http.StatusConflict, errCodeMemoryConflict,
			"the memory is at revision %d, not %d; read it again and rewrite from the current text", base, req.Revision)
	case !exists && req.Revision != 0:
		return agentMemWriteResult{}, memoryErrf(http.StatusNotFound, errCodeMemoryNotFound,
			"the memory does not exist (it may have been forgotten); save it with revision 0 to create it")
	case !exists:
		if base, err = agentMemTombRevision(rel, req.Name); err != nil {
			return agentMemWriteResult{}, err
		}
	}

	stamp := now.UTC().Format(time.RFC3339)
	e := agentMemEntry{
		Name: req.Name, Scope: req.Scope, Description: req.Description, Type: req.Type,
		Kinds: req.Kinds, Revision: base + 1, AuthorKind: c.Kind, AuthorSession: c.Session,
		Created: stamp, Updated: stamp, Body: req.Body,
	}
	if exists {
		e.Created, e.Source, e.SourceHash = cur.Entry.Created, cur.Entry.Source, cur.Entry.SourceHash
	}
	data := agentMemRender(e)
	repoRel := rel + "/" + req.Name + ".md"
	if f := agentMemScanText("memory", string(data)); len(f) > 0 {
		return agentMemWriteResult{}, &agentMemSecretErr{Findings: f}
	}

	changes := []agentMemChange{{Rel: repoRel, Data: data}}
	if c.Project != nil && req.Scope == agentMemScopeProject {
		extra, err := agentMemProjectInfoChange(*c.Project)
		if err != nil {
			return agentMemWriteResult{}, err
		}
		changes = append(changes, extra...)
	}
	op := "update"
	if !exists {
		op = "create"
	}
	rev, err := agentMemApplyLocked(changes, op, repoRel, c, now)
	if err != nil {
		return agentMemWriteResult{}, err
	}
	if !exists {
		agentMemRemoveTomb(rel, req.Name)
	}
	return agentMemWriteResult{Name: e.Name, Scope: e.Scope, Revision: e.Revision, Commit: rev, Created: !exists}, nil
}

// agentMemForget removes a memory from what is published. Its text stays in history; purging
// a secret from history is a separate, member-only operation (ADR 0108 open question 3).
func agentMemForget(c agentMemCaller, req agentMemForgetReq, now time.Time) (agentMemWriteResult, error) {
	req.Name = strings.TrimSpace(req.Name)
	if err := agentMemCheckName(req.Name); err != nil {
		return agentMemWriteResult{}, err
	}
	if req.Revision < 1 {
		return agentMemWriteResult{}, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest,
			"revision is required: pass the revision you read, so a memory changed since is not removed unseen")
	}
	req.Scope = agentMemDefaultScope(req.Scope, c)
	rel, err := agentMemScopeDir(req.Scope, c)
	if err != nil {
		return agentMemWriteResult{}, err
	}

	agentMemMu.Lock()
	defer agentMemMu.Unlock()
	memorySnapshotMu.Lock()
	defer memorySnapshotMu.Unlock()

	dir, err := agentMemCheckDir(rel, false)
	if err != nil && !os.IsNotExist(err) {
		return agentMemWriteResult{}, err
	}
	var cur agentMemLoaded
	exists := false
	if err == nil {
		cur, exists, err = agentMemLoadFile(filepath.Join(dir, req.Name+".md"), req.Scope, req.Name)
		if err != nil {
			return agentMemWriteResult{}, err
		}
	}
	if !exists {
		return agentMemWriteResult{}, memoryErrf(http.StatusNotFound, errCodeMemoryNotFound, "no memory by that name in scope %s", req.Scope)
	}
	if cur.Withheld {
		return agentMemWriteResult{}, memoryErrf(http.StatusConflict, errCodeMemoryConflict, "this memory is withheld; your user has to fix or remove the file")
	}
	if cur.Entry.Revision != req.Revision {
		return agentMemWriteResult{}, memoryErrf(http.StatusConflict, errCodeMemoryConflict,
			"the memory is at revision %d, not %d; read it again before forgetting it", cur.Entry.Revision, req.Revision)
	}
	repoRel := rel + "/" + req.Name + ".md"
	if err := agentMemWriteTomb(rel, req.Name, cur.Entry.Revision); err != nil {
		return agentMemWriteResult{}, err
	}
	rev, err := agentMemApplyLocked([]agentMemChange{{Rel: repoRel, Delete: true}}, "forget", repoRel, c, now)
	if err != nil {
		// The memory is back; a tombstone beside it is harmless (it is read only on create).
		return agentMemWriteResult{}, err
	}
	return agentMemWriteResult{Name: req.Name, Scope: req.Scope, Revision: cur.Entry.Revision, Commit: rev, Deleted: true}, nil
}

// agentMemProjectInfoChange is the project.json write a project's first memory brings along, or
// nothing when it is there already.
func agentMemProjectInfoChange(p agentMemProject) ([]agentMemChange, error) {
	info := agentMemProjectInfoRel(p)
	if _, ok, _ := agentMemReadFile(filepath.Join(agentMemDir(), filepath.FromSlash(info))); ok {
		return nil, nil
	}
	b, _ := json.MarshalIndent(p, "", "  ")
	// The root path comes from a folder name anyone can choose; it is committed too.
	// Raw values as well as the JSON: JSON escapes the quotes the generic rule needs.
	if f := agentMemScanText("project", p.Root+"\n"+p.Display+"\n"+string(b)); len(f) > 0 {
		return nil, &agentMemSecretErr{Findings: f}
	}
	return []agentMemChange{{Rel: info, Data: append(b, '\n')}}, nil
}

func agentMemProjectInfoRel(p agentMemProject) string { return "projects/" + p.ID + "/project.json" }

// agentMemChange is one file of a write, relative to the store (= relative to af/ in the repo).
type agentMemChange struct {
	Rel    string
	Data   []byte
	Delete bool
}

// agentMemApplyLocked writes the changes to the store and to staging and commits exactly those
// paths, with agentMemMu and memorySnapshotMu held. Only these paths are staged: a file someone
// put in the store by hand is never swept into this author's commit unscanned. On failure the
// store, staging and the index are put back, so a later snapshot cannot commit the change
// either.
func agentMemApplyLocked(changes []agentMemChange, op, memRel string, c agentMemCaller, now time.Time, extra ...string) (string, error) {
	if err := memoryEnsureRepo(); err != nil {
		return "", err
	}
	type saved struct {
		existed bool
		data    []byte
	}
	prev := make([]saved, len(changes))
	paths := make([]string, len(changes))
	for i, ch := range changes {
		b, ok, err := agentMemReadFile(filepath.Join(agentMemDir(), filepath.FromSlash(ch.Rel)))
		if err != nil {
			return "", err
		}
		prev[i] = saved{existed: ok, data: b}
		paths[i] = agentMemRepoPrefix + "/" + ch.Rel
	}

	undo := func() {
		for i, ch := range changes {
			abs := filepath.Join(agentMemDir(), filepath.FromSlash(ch.Rel))
			var err error
			if prev[i].existed {
				err = agentMemWriteFile(abs, prev[i].data)
			} else if rmErr := os.Remove(abs); rmErr != nil && !os.IsNotExist(rmErr) {
				err = rmErr
			}
			if err != nil {
				log.Printf("agent memory: undo: %s", agentMemErrKind(err))
			}
		}
		if err := agentMemResetStaging(paths); err != nil {
			log.Printf("agent memory: reset staging: %s", agentMemErrKind(err))
		}
	}

	for _, ch := range changes {
		abs := filepath.Join(agentMemDir(), filepath.FromSlash(ch.Rel))
		stg := filepath.Join(memoryStagingDir(), agentMemRepoPrefix, filepath.FromSlash(ch.Rel))
		var err error
		if ch.Delete {
			if err = os.Remove(abs); err == nil || os.IsNotExist(err) {
				err = os.Remove(stg)
				if os.IsNotExist(err) {
					err = nil
				}
			}
		} else {
			if _, err = agentMemCheckDir(filepath.ToSlash(filepath.Dir(ch.Rel)), true); err == nil {
				if err = agentMemWriteFile(abs, ch.Data); err == nil {
					if err = os.MkdirAll(filepath.Dir(stg), 0o700); err == nil {
						err = os.WriteFile(stg, ch.Data, 0o600)
					}
				}
			}
		}
		if err != nil {
			undo()
			return "", err
		}
	}

	// A path git has never seen and that is gone now (forgetting a file put in the store by
	// hand) has nothing to stage; pathspecs that match nothing would fail the whole commit.
	var staged []string
	for i, p := range paths {
		tracked, err := memoryGitRun("ls-files", "--", p)
		if err != nil {
			// Not knowing is not "untracked": an empty commit would record a forget whose
			// tree still holds the file.
			undo()
			return "", fmt.Errorf("inspect agent memory index: %w", err)
		}
		if tracked != "" || !changes[i].Delete {
			staged = append(staged, p)
		}
	}
	if len(staged) > 0 {
		if _, err := memoryGitRun(append([]string{"add", "-A", "--"}, staged...)...); err != nil {
			undo()
			return "", fmt.Errorf("stage agent memory: %w", err)
		}
	}
	msg := fmt.Sprintf("agent-memory: %s %s (%s)\n\nAF-Trigger: %s\nAF-Op: %s\nAF-Memory: %s\nAF-Author-Kind: %s\nAF-Author-Session: %s\n",
		op, memRel, now.Format(time.RFC3339),
		memoryTriggerAgentMemory, op, agentMemRepoPrefix+"/"+memRel, c.Kind, c.Session)
	for _, t := range extra {
		msg += t + "\n"
	}
	var rev string
	if len(staged) > 0 {
		// --only with pathspecs commits these paths alone even if something else is staged.
		if _, err := memoryGitRun(append([]string{"commit", "--quiet", "--no-verify", "-m", msg, "--"}, staged...)...); err != nil {
			undo()
			return "", fmt.Errorf("commit agent memory: %w", err)
		}
		var err error
		if rev, err = memoryGitRun("rev-parse", memoryBranch); err != nil {
			return "", err
		}
	} else {
		// Still one commit per published change, so the forget has its author in history.
		var err error
		if rev, err = agentMemEmptyCommit(msg); err != nil {
			undo()
			return "", err
		}
	}
	_, _ = memoryGitRun("gc", "--auto", "--quiet")
	return rev, nil
}

// agentMemEmptyCommit records msg on main without touching the index, for a change git has no
// path for.
func agentMemEmptyCommit(msg string) (string, error) {
	args := []string{"commit-tree", "-m", msg}
	if memoryHasCommits() {
		args = append(args, "-p", memoryBranch, memoryBranch+"^{tree}")
	} else {
		tree, err := memoryGitRun("mktree")
		if err != nil {
			return "", err
		}
		args = append(args, tree)
	}
	rev, err := memoryGitRun(args...)
	if err != nil {
		return "", fmt.Errorf("record agent memory change: %w", err)
	}
	if _, err := memoryGitRun("update-ref", "refs/heads/"+memoryBranch, rev); err != nil {
		return "", err
	}
	return rev, nil
}

// agentMemResetStaging puts paths in the index and in staging back to HEAD (or removes them
// when HEAD does not have them).
func agentMemResetStaging(paths []string) error {
	var errs []error
	if memoryHasCommits() {
		if _, err := memoryGitRun(append([]string{"reset", "-q", "HEAD", "--"}, paths...)...); err != nil {
			errs = append(errs, err)
		}
	} else if _, err := memoryGitRun(append([]string{"rm", "-q", "--cached", "--ignore-unmatch", "--"}, paths...)...); err != nil {
		errs = append(errs, err)
	}
	for _, p := range paths {
		stg := filepath.Join(memoryStagingDir(), filepath.FromSlash(p))
		body, err := memoryGit("show", memoryBranch+":"+p).Output()
		if err != nil {
			if rmErr := os.Remove(stg); rmErr != nil && !os.IsNotExist(rmErr) {
				errs = append(errs, rmErr)
			}
			continue
		}
		if err := os.WriteFile(stg, body, 0o600); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// agentMemWriteFile replaces a file by rename, so a reader in another kind never sees half a
// memory. The rename replaces a symlink at the leaf rather than writing through it.
func agentMemWriteFile(abs string, data []byte) error {
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
