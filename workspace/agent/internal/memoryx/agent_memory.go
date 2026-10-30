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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

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

	// The store limits are what AF accepts, sized so nothing claude's or codex's own memory holds
	// is refused (ADR 0108 decision 3). What is advised to authors is the warning tier below.
	agentMemMaxBody = 200 << 10
	// agentMemMaxFile bounds a stored file: body, frontmatter (description up to 4 bytes per
	// character) and slack for hand edits.
	agentMemMaxFile = agentMemMaxBody + 32<<10
	// agentMemMaxDescription is counted in characters, not bytes: a Japanese description is 3
	// bytes per character.
	agentMemMaxDescription = 2000
	// agentMemSaveMaxRequest bounds the JSON of a save: the body and description at the worst
	// JSON escaping (6 bytes per control or quote-like character, 12 per astral one is covered
	// by the description term) plus the other fields. The limits on the decoded values are what
	// judge a save.
	agentMemSaveMaxRequest = 6*agentMemMaxBody + 12*agentMemMaxDescription + 64<<10
	// There is no per-line limit: the scanner runs every rule over the whole line (RE2 is
	// linear), so a long line is vouched for like any other and the body bound is the only
	// safety stop.

	// Authoring guidance, returned as warnings and never as a refusal. The thresholds are
	// claude's own: it lints a description above 300 characters (suggesting about 150) and
	// shows other sessions only the first 4,096 bytes of a memory file in recall.
	agentMemAdviseDescription = 150
	agentMemWarnDescription   = 300
	agentMemWarnBody          = 4096
	agentMemMaxKinds          = 16
	agentMemSearchDefault     = 20
	agentMemSearchMax         = 50
	agentMemSnippetLines      = 3
	agentMemSnippetChars      = 200
	// agentMemUnknown is what an author AF cannot establish is recorded as; never a guess.
	agentMemUnknown = "unknown"
)

var (
	// agentMemNameRe is a safe single path segment: it has no "/" and cannot start with "." or
	// "-", so ".forgotten" (the tombstone directory) is unreachable. Go has no look-ahead, so
	// agentMemValidName also refuses "..".
	agentMemNameRe = regexp.MustCompile(`^[a-z0-9_][a-z0-9._-]{0,63}$`)
	agentMemKindRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	// agentMemTypes are the memory types claude's auto-memory uses, so the import keeps them.
	agentMemTypes = map[string]bool{"": true, "user": true, "feedback": true, "project": true, "reference": true}

	// agentMemMu orders readers against a write: a write holds it from the file change to the
	// commit, so a reader never sees a change that is about to be rolled back.
	agentMemMu sync.RWMutex
)

// agentMemValidName says whether name may be a memory name and so a file name, a repo path and
// a commit-message field.
func agentMemValidName(name string) bool {
	return agentMemNameRe.MatchString(name) && !strings.Contains(name, "..")
}

func agentMemDir() string { return filepath.Join(claude.ConfigDir(), "af-agent-memory") }

// agentMemProject is the project a session works in (ADR 0108 decision 2).
type agentMemProject struct {
	ID      string `json:"id"`
	Root    string `json:"root"`
	VCS     string `json:"vcs"`
	Display string `json:"display"`
	// Remote is the normalised origin URL ("host/owner/repo", no credentials), "" when the
	// repository has none or it predates the field. It is not part of the project's identity: it
	// only lets the Console suggest moving an orphan's memories into a live project (#2110).
	Remote string `json:"remote,omitempty"`
}

// agentMemCaller is who is asking: the author of a write and the scope of a read.
type agentMemCaller struct {
	Session string
	Kind    string
	Project *agentMemProject // nil: the session has no working copy, so user scope only
	// trailers go on the commit of a write this caller makes (the external client's name).
	trailers []string
	// noRevival makes a create refuse a name that was forgotten or ever existed (the background
	// review never revives a memory the member or an agent forgot, ADR 0117 decision 5), judged
	// inside the store lock the save holds. keepKinds makes an update keep the kinds the memory
	// already has instead of taking the request's. Both are set by the review's publish only.
	noRevival bool
	keepKinds bool
	// startProject makes a project save write the project's record along with the memory, though
	// the caller is not a session: an outside client that names a repository cloned in the
	// workspace starts its project, and Project is then that working copy's identity.
	startProject bool
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
	return agentMemProjectWith(dir, dir, func(args ...string) (string, error) { return gitx.Run(dir, args...) })
}

// agentMemGitRunner runs git in one fixed directory.
type agentMemGitRunner func(args ...string) (string, error)

// agentMemProjectWith is agentMemProjectFor for a directory that is named by label (under
// ~/repos) but inspected through vcsDir and run: the muse import passes a pinned handle's
// /proc path, so what git answers is about the directory it opened, not about the path.
func agentMemProjectWith(label, vcsDir string, run agentMemGitRunner) *agentMemProject {
	dir := label
	if dir == "" {
		return nil
	}
	repos := gitx.ReposRoot()
	rel, err := filepath.Rel(repos, dir)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return nil
	}
	root, key, vcs := filepath.Clean(dir), filepath.Clean(dir), projcfg.DetectVCS(vcsDir)
	if vcs == projcfg.VCSGit {
		if r, k := agentMemGitIdentityWith(run); k != "" {
			root, key = r, k
		}
	}
	display := filepath.Base(root)
	if r, err := filepath.Rel(repos, root); err == nil && !strings.HasPrefix(r, "..") {
		display = filepath.ToSlash(r)
	}
	p := &agentMemProject{ID: agentMemProjectID(key), Root: root, VCS: vcs, Display: display}
	if vcs == projcfg.VCSGit {
		if raw, err := run("remote", "get-url", "origin"); err == nil {
			p.Remote = agentMemNormalizeRemote(raw)
		}
	}
	return p
}

// agentMemGitIdentity returns the repository's main working tree (for display) and its key:
// the absolute git-common-dir, which every worktree of one repository shares and no two
// repositories do. The parent of git-common-dir is not the key: with --separate-git-dir or in a
// submodule it is a metadata directory that several repositories can share.
func agentMemGitIdentity(dir string) (root, key string) {
	return agentMemGitIdentityWith(func(args ...string) (string, error) { return gitx.Run(dir, args...) })
}

func agentMemGitIdentityWith(run agentMemGitRunner) (root, key string) {
	common, err := run("rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || common == "" {
		return "", ""
	}
	top, err := run("rev-parse", "--show-toplevel")
	if err != nil || top == "" {
		return "", ""
	}
	root = filepath.Clean(top)
	gitDir, err := run("rev-parse", "--absolute-git-dir")
	if err == nil && filepath.Clean(gitDir) != filepath.Clean(common) {
		// A linked worktree: the main one is git's first entry, when it is a real checkout.
		if out, err := run("worktree", "list", "--porcelain"); err == nil {
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
	b, err := agentMemReadHandle(f)
	if err == agentMemErrSymlink {
		return nil, false, err
	}
	return b, true, err
}

// agentMemReadHandle reads an already opened regular file.
func agentMemReadHandle(f *os.File) ([]byte, error) {
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
		return nil, agentMemErrSymlink
	}
	b, err := io.ReadAll(io.LimitReader(f, agentMemMaxFile+1))
	if err != nil {
		return nil, err
	}
	if len(b) > agentMemMaxFile {
		// Never return a truncated file: it would be shown as the memory, or written back as
		// the original by a rollback.
		return nil, agentMemErrTooLarge
	}
	return b, nil
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
	// Pinned is the member's choice (agentMemPin): the entry sits first in the described part of
	// the index. The Console is the way to set it (no MCP tool does); a save by an agent carries the stored value forward.
	Pinned bool `json:"pinned,omitempty"`
	// Uses is the read and search-hit count from the usage sidecar, filled by the loaders that
	// rank or list. It is not stored in the memory file.
	Uses int    `json:"uses,omitempty"`
	Body string `json:"body,omitempty"`
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
	if e.Pinned {
		b.WriteString("pinned: true\n")
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
		case "pinned":
			e.Pinned = unq(v) == "true"
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

// agentMemScanText scans text the way a write is judged: NUL is refused outright, because the
// scanner cannot vouch for binary (it skips it). Lines of any length are scanned whole.
func agentMemScanText(path, text string) []memorySecretFinding {
	if strings.IndexByte(text, 0) >= 0 {
		return []memorySecretFinding{{Path: path, Rule: "nul-byte", Hint: "…"}}
	}
	return memoryScanContent(path, []byte(text))
}

// agentMemScanCache remembers the verdict on a stored file by its content and the name it is
// shown under, so the index does not rescan every file on every call.
var agentMemScanCache sync.Map // sha256(raw) + "\x00" + name -> bool (true = clean)

// agentMemScanRuns counts verdicts computed rather than found in agentMemScanCache. Tests read
// it to tell a rescan from a hit without timing anything.
var agentMemScanRuns atomic.Int64

// agentMemWarmScan fills agentMemScanCache for what c can read, without agentMemMu. Readers call
// it before taking the read lock so a cold scan (regex over every file: measured ~1.6 s for 500
// files, all of it in the scan) does not hold up a writer queued behind the lock.
// Race-free because the verdict is a pure function of the bytes read and the key is their
// hash: a file replaced meanwhile only leaves an unused entry, and the locked read that follows
// re-reads the file and still goes through agentMemClean, so nothing unscanned is returned.
// Errors are ignored; the locked read reports them.
func agentMemWarmScan(c agentMemCaller) {
	for _, scope := range agentMemScopes(c) {
		_, _, _ = agentMemLoadScope(scope, c)
	}
}

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
	agentMemScanRuns.Add(1)
	clean := len(agentMemScanText("", string(raw))) == 0 && len(agentMemScanText("", agentMemPublished(e))) == 0
	agentMemScanCache.Store(key, clean)
	return clean
}

// agentMemPrimeScan records the verdict on a file just written, judged exactly as a reader
// would (parsed back from the bytes on disk), so the first read after a save finds it cached.
func agentMemPrimeScan(data []byte, scope, name string) {
	if e, ok := agentMemParse(data); ok {
		e.Name, e.Scope = name, scope
		agentMemClean(data, e)
	}
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
	return agentMemLoadDir(scope, rel)
}

// agentMemLoadDir is agentMemLoadScope for a scope directory already resolved.
func agentMemLoadDir(scope, rel string) ([]agentMemEntry, int, error) {
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
		if !ok || !d.Type().IsRegular() || !agentMemValidName(name) {
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

// agentMemIndex is the answer to memory_index: no bodies, ranked and cut to a byte budget.
// Entries are the described part (descriptions shortened), More the grouped, abbreviated names
// of what did not fit, Omitted the count beyond even those.
type agentMemIndex struct {
	Project *agentMemProject `json:"project"`
	Entries []agentMemEntry  `json:"entries"`
	More    []string         `json:"more,omitempty"`
	Omitted int              `json:"omitted,omitempty"`
	// PinnedOmitted counts pinned memories that are in More or Omitted because the pins alone
	// are larger than the budget (agentMemBudgetIndex).
	PinnedOmitted int `json:"pinnedOmitted,omitempty"`
	// Truncated is true when any memory is not in Entries.
	Truncated bool `json:"truncated,omitempty"`
	// Withheld counts files left out because they failed the secret scan or are malformed
	// beyond reading; their names are not shown either.
	Withheld int `json:"withheld,omitempty"`
}

// agentMemListIndex ranks what the caller sees and applies the byte budget (0 = default).
func agentMemListIndex(c agentMemCaller, budget int) (agentMemIndex, error) {
	agentMemWarmScan(c)
	agentMemMu.RLock()
	defer agentMemMu.RUnlock()
	out := agentMemIndex{Project: c.Project, Entries: []agentMemEntry{}}
	var all []agentMemEntry
	for _, scope := range agentMemScopes(c) {
		es, withheld, err := agentMemLoadScope(scope, c)
		if err != nil {
			return out, err
		}
		out.Withheld += withheld
		if rel, err := agentMemScopeDir(scope, c); err == nil {
			agentMemSetUses(es, rel)
		}
		for _, e := range es {
			if agentMemAppliesTo(e, c.Kind) {
				e.Body = ""
				all = append(all, e)
			}
		}
	}
	agentMemRank(all)
	out.Entries, out.More, out.Omitted, out.PinnedOmitted = agentMemBudgetIndex(all, budget)
	out.Truncated = len(out.Entries) < len(all)
	return out, nil
}

// agentMemHit is one search result: the entry without its body, and the lines that matched.
type agentMemHit struct {
	agentMemEntry
	Snippets []string `json:"snippets"`
	// MatchedTerms names the query words this hit contains. It is set only on an any-term
	// fallback answer, where hits differ in how much of the query they matched.
	MatchedTerms []string `json:"matchedTerms,omitempty"`
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
		// An outside client is held to the kinds filter the index and search apply: a memory
		// limited to other kinds is not readable by name either, and is not counted as a use.
		if c.Kind == agentMemExternalKind && !agentMemAppliesTo(l.Entry, c.Kind) {
			continue
		}
		// Counted under the read lock: a forget or re-create (write lock) cannot slip between the
		// read and the count and leave this use on a newer memory.
		agentMemRecordUses(c, l.Entry)
		return l.Entry, nil
	}
	if scope != "" {
		if other := agentMemOtherScopeHolding(c, scope, name); other != "" {
			return agentMemEntry{}, memoryErrf(http.StatusNotFound, errCodeMemoryNotFound,
				"no memory by that name in scope %s; one exists in scope %s (pass scope %q or omit scope)", scope, other, other)
		}
	}
	return agentMemEntry{}, memoryErrf(http.StatusNotFound, errCodeMemoryNotFound, "no memory by that name")
}

// agentMemOtherScopeHolding names the other scope this caller may read that holds name, or "" when
// there is none. It applies the visibility of a read (the kinds filter of an outside client, and a
// withheld file is not offered) and never counts a use, so a miss reveals nothing a read of that
// scope would not. The caller holds agentMemMu.
func agentMemOtherScopeHolding(c agentMemCaller, missed, name string) string {
	for _, s := range agentMemScopes(c) {
		if s == missed {
			continue
		}
		l, ok, err := agentMemLoadOne(c, s, name)
		if err != nil || !ok || l.Withheld {
			continue
		}
		if c.Kind == agentMemExternalKind && !agentMemAppliesTo(l.Entry, c.Kind) {
			continue
		}
		return s
	}
	return ""
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
	if !agentMemValidName(name) {
		return agentMemBadName()
	}
	if len(agentMemScanText("name", name)) > 0 {
		return memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "name looks like a secret; choose a descriptive name")
	}
	return nil
}

func agentMemBadName() error {
	return memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "name must be 1-64 characters of lower-case letters, digits, '-', '_' and '.'; it must not start with '.' or '-' or contain '..'")
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
	// Warnings is authoring guidance; the save has succeeded either way.
	Warnings []string `json:"warnings,omitempty"`
}

// agentMemSaveWarnings is the guidance a save returns: the limits claude itself lints or
// truncates at. It never refuses; the store limits are agentMemMax*.
func agentMemSaveWarnings(req agentMemSaveReq) []string {
	var w []string
	if n := utf8.RuneCountInString(req.Description); n > agentMemWarnDescription {
		w = append(w, fmt.Sprintf("description is %d characters; keep it to about %d (one fact per memory, a one-line summary): claude lints a description above %d characters and recall matches on it as a summary",
			n, agentMemAdviseDescription, agentMemWarnDescription))
	}
	if n := len(req.Body); n > agentMemWarnBody {
		w = append(w, fmt.Sprintf("body is %d bytes; claude's recall shows only the first %d bytes (200 lines) of a memory, so put the essentials first or split it into one fact per memory",
			n, agentMemWarnBody))
	}
	return w
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
	case !agentMemValidName(req.Name):
		return agentMemBadName()
	case req.Description == "":
		return bad("description is required")
	case strings.ContainsAny(req.Description, "\r\n"):
		return bad("description must be one line")
	case utf8.RuneCountInString(req.Description) > agentMemMaxDescription:
		return bad(fmt.Sprintf("description is longer than %d characters", agentMemMaxDescription))
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
		if c.noRevival {
			if err := agentMemRefuseRevival(rel, req.Name); err != nil {
				return agentMemWriteResult{}, err
			}
		}
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
		e.Pinned = cur.Entry.Pinned
		if c.keepKinds {
			e.Kinds = cur.Entry.Kinds
		}
	}
	data := agentMemRender(e)
	repoRel := rel + "/" + req.Name + ".md"
	if f := agentMemScanText("memory", string(data)); len(f) > 0 {
		return agentMemWriteResult{}, &agentMemSecretErr{Findings: f}
	}

	changes := []agentMemChange{{Rel: repoRel, Data: data}}
	// The member and an external client name a project that already holds memory and have no
	// working copy to describe it, so the project's record is left as the sessions wrote it,
	// unless the external client started the project from a working copy (startProject).
	if c.Project != nil && req.Scope == agentMemScopeProject && (c.startProject || c.Kind != agentMemMember.Kind && c.Kind != agentMemExternalKind) {
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
		agentMemClearUsage(rel, req.Name)
	}
	agentMemPrimeScan(data, req.Scope, req.Name)
	return agentMemWriteResult{Name: e.Name, Scope: e.Scope, Revision: e.Revision, Commit: rev, Created: !exists,
		Warnings: agentMemSaveWarnings(req)}, nil
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
		if other := agentMemOtherScopeHolding(c, req.Scope, req.Name); other != "" {
			return agentMemWriteResult{}, memoryErrf(http.StatusNotFound, errCodeMemoryNotFound,
				"no memory by that name in scope %s; one exists in scope %s (read it there first and forget it with that revision)", req.Scope, other)
		}
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
	base, err := forgetIntent(rel, req.Name)
	if err != nil {
		return agentMemWriteResult{}, err
	}
	if err := agentMemWriteTomb(rel, req.Name, cur.Entry.Revision); err != nil {
		return agentMemWriteResult{}, err
	}
	rev, err := agentMemApplyLocked([]agentMemChange{{Rel: repoRel, Delete: true}}, "forget", repoRel, c, now)
	if err != nil {
		// The memory is back; a tombstone beside it is harmless (it is read only on create).
		return agentMemWriteResult{}, err
	}
	agentMemClearUsage(rel, req.Name)
	forgetDone(rel, req.Name, base)
	return agentMemWriteResult{Name: req.Name, Scope: req.Scope, Revision: cur.Entry.Revision, Commit: rev, Deleted: true}, nil
}

// agentMemProjectInfoChange is the project.json write a project's first memory brings along, or
// nothing when it is there already.
func agentMemProjectInfoChange(p agentMemProject) ([]agentMemChange, error) {
	info := agentMemProjectInfoRel(p)
	old, exists, _ := agentMemReadFile(filepath.Join(agentMemDir(), filepath.FromSlash(info)))
	if exists {
		return agentMemProjectInfoAddRemote(info, old, p)
	}
	b, _ := json.MarshalIndent(p, "", "  ")
	// The root path comes from a folder name anyone can choose; it is committed too.
	// Raw values as well as the JSON: JSON escapes the quotes the generic rule needs.
	if f := agentMemScanText("project", p.Root+"\n"+p.Display+"\n"+string(b)); len(f) > 0 {
		if p.Remote == "" {
			return nil, &agentMemSecretErr{Findings: f}
		}
		// The remote is a convenience: whatever in it the scan flags must not stop the first
		// memory of the project from being saved.
		p.Remote = ""
		return agentMemProjectInfoChange(p)
	}
	return []agentMemChange{{Rel: info, Data: append(b, '\n')}}, nil
}

// agentMemProjectInfoAddRemote adds the remote to a record written before the field existed
// (#2110) by splicing one member into the stored bytes: the fields, their order and anything a
// newer version wrote stay exactly as they were. A record that already has a "remote" member, is
// not the object of this project, or would fail the scan is left alone. Not worth failing a save for.
func agentMemProjectInfoAddRemote(info string, old []byte, p agentMemProject) ([]agentMemChange, error) {
	if p.Remote == "" || !agentMemRemoteOK(p.Remote) {
		return nil, nil
	}
	var members map[string]json.RawMessage
	if json.Unmarshal(old, &members) != nil {
		return nil, nil
	}
	var id string
	if _, has := members["remote"]; has || json.Unmarshal(members["id"], &id) != nil || id != p.ID {
		return nil, nil
	}
	body := bytes.TrimRight(old, " \t\r\n")
	if len(body) < 2 || body[len(body)-1] != '}' || len(members) == 0 {
		return nil, nil
	}
	rem, _ := json.Marshal(p.Remote)
	next := append(bytes.TrimRight(body[:len(body)-1], " \t\r\n"), []byte(",\n  \"remote\": "+string(rem)+"\n}\n")...)
	// The raw bytes hide what a JSON escape spells (\u0067hp_…), so the decoded keys and strings
	// of the whole record, unknown members and nesting included, are scanned too.
	decoded, ok := agentMemJSONStrings(next)
	if !ok || len(agentMemScanText("project", string(next)+"\n"+decoded)) > 0 {
		return nil, nil
	}
	return []agentMemChange{{Rel: info, Data: next}}, nil
}

// agentMemJSONStrings joins every object key and string value of a JSON document, decoded, as the
// token stream gives them: a key repeated in one object is not collapsed, so the earlier value is
// scanned too. ok=false when the document does not parse.
func agentMemJSONStrings(data []byte) (string, bool) {
	var b strings.Builder
	dec := json.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return b.String(), true
		}
		if err != nil {
			return "", false
		}
		if s, ok := tok.(string); ok {
			b.WriteString(s + "\n")
		}
	}
}

func agentMemProjectInfoRel(p agentMemProject) string { return "projects/" + p.ID + "/project.json" }

// agentMemOpProjectInfo is the op of the project.json backfill commit.
const agentMemOpProjectInfo = "project-info"

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
	trigger := memoryTriggerAgentMemory
	if op == agentMemOpProjectInfo {
		trigger = memoryTriggerAgentMemoryMeta
	}
	msg := fmt.Sprintf("agent-memory: %s %s (%s)\n\nAF-Trigger: %s\nAF-Op: %s\nAF-Memory: %s\nAF-Author-Kind: %s\nAF-Author-Session: %s\n%s: %d\n",
		op, memRel, now.Format(time.RFC3339),
		trigger, op, agentMemRepoPrefix+"/"+memRel, c.Kind, c.Session, agentMemSeqTrailer, time.Now().UnixNano())
	for _, t := range append(extra[:len(extra):len(extra)], c.trailers...) {
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
