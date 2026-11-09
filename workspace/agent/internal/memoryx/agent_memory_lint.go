package memoryx

// The read-only lint report over AF-owned agent memory (ADR 0130 decision 5, step 1). The checks
// are the pure memorylint package's; this file only feeds it: the bodies of one scope, the
// names, and a reader of the project's default branch.
//
// Nothing here writes a memory. A body is loaded the way the member's read loads it (no use is
// counted, no sidecar is touched), and the repository is read by ref from its git-common-dir
// with `git --git-dir`, so no working copy is checked out, refreshed or locked. The one thing
// the member edits is the retired-term list, kept beside the store and not in it: it is a
// setting of the lint, not a memory, and it must never ride in a memory's history.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memberorigin"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memorylint"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/projcfg"
)

const (
	agentMemLintTermsFile = "af-memory-lint-terms.json"
	agentMemLintMaxTerms  = 500
	agentMemLintMaxTermLn = 100
	// agentMemLintGitTimeout bounds one git process of the repository reader; past it the
	// check is reported as skipped, never as clean.
	agentMemLintGitTimeout = 60 * time.Second
	// agentMemLintSymChunk is how many symbols one `git grep` is given.
	agentMemLintSymChunk = 100
)

// agentMemLintTermsPath is outside the store (af-agent-memory/), so the list is never swept into
// a memory commit and a purge or restore of the store leaves it alone.
func agentMemLintTermsPath() string {
	return filepath.Join(claude.ConfigDir(), agentMemLintTermsFile)
}

// agentMemLintTermsDoc is the list file and the wire body of the terms routes. Rejected holds
// retired-term proposals the member turned down; on a save a nil Rejected leaves the stored set
// as it is, so a client that only edits terms cannot clear it.
type agentMemLintTermsDoc struct {
	Terms    []string `json:"terms"`
	Rejected []string `json:"rejected,omitempty"`
}

// agentMemLintLoadTerms reads the member's list; a missing or unreadable file is an empty list.
func agentMemLintLoadTerms() []string { return agentMemLintLoadDoc().Terms }

// agentMemLintLoadDoc reads the list file whole; both slices are non-nil.
func agentMemLintLoadDoc() agentMemLintTermsDoc {
	d := agentMemLintTermsDoc{Terms: []string{}, Rejected: []string{}}
	b, ok, err := agentMemReadFile(agentMemLintTermsPath())
	if err != nil || !ok {
		return d
	}
	var f agentMemLintTermsDoc
	if json.Unmarshal(b, &f) != nil {
		return d
	}
	if f.Terms != nil {
		d.Terms = f.Terms
	}
	if f.Rejected != nil {
		d.Rejected = f.Rejected
	}
	return d
}

// agentMemLintCleanTerms trims, drops blanks and duplicates (case-insensitively) and refuses a
// list or term outside the limits.
func agentMemLintCleanTerms(in []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, t := range in {
		t = strings.TrimSpace(t)
		if t == "" || seen[strings.ToLower(t)] {
			continue
		}
		if utf8.RuneCountInString(t) > agentMemLintMaxTermLn || strings.IndexFunc(t, unicode.IsControl) >= 0 {
			return nil, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest,
				"a term is at most %d characters on one line", agentMemLintMaxTermLn)
		}
		seen[strings.ToLower(t)] = true
		out = append(out, t)
	}
	if len(out) > agentMemLintMaxTerms {
		return nil, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "at most %d terms", agentMemLintMaxTerms)
	}
	return out, nil
}

// agentMemLintSaveTerms replaces the list. Written to a temporary file and renamed, so a reader
// never sees half a list.
func agentMemLintSaveTerms(doc agentMemLintTermsDoc) error {
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	p := agentMemLintTermsPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), agentMemLintTermsFile+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// agentMemLintGit runs git against one git directory by ref, never in a working copy.
func agentMemLintGit(ctx context.Context, gitDir string, stdin []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"--git-dir=" + gitDir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "GIT_NO_REPLACE_OBJECTS=1")
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	return cmd.Output()
}

// agentMemLintRepo reads one ref of a repository for memorylint.
type agentMemLintRepo struct {
	gitDir, ref string
	// memo, when set, shares one read of the rename history between the report and the apply step.
	memo *agentMemLintRenameMemo
}

func (r agentMemLintRepo) Paths() ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), agentMemLintGitTimeout)
	defer cancel()
	out, err := agentMemLintGit(ctx, r.gitDir, nil, "ls-tree", "-r", "--name-only", "-z", r.ref)
	if err != nil {
		return nil, fmt.Errorf("git ls-tree: %w", err)
	}
	var paths []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// ReadFile returns one blob of the pinned revision (`git cat-file blob <oid>:<path>`), never a working
// copy's file, so the .gitignore rules the path check applies are the revision's own.
func (r agentMemLintRepo) ReadFile(path string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), agentMemLintGitTimeout)
	defer cancel()
	out, err := agentMemLintGit(ctx, r.gitDir, nil, "cat-file", "blob", r.ref+":"+path)
	if err != nil {
		return nil, fmt.Errorf("git cat-file blob: %w", err)
	}
	return out, nil
}

// agentMemLintKeyRe is the shape of a configuration key the history scan looks for: the
// workspace's environment variables (at least three characters after AF_, which leaves out
// short names such as AF_ID that occur everywhere).
var agentMemLintKeyRe = regexp.MustCompile(`\bAF_[A-Z][A-Z0-9_]{2,}\b`)

const (
	// agentMemLintKeyCommits bounds how far back the scan reads; older removals are not proposed.
	agentMemLintKeyCommits = 300
	agentMemLintKeyMax     = 2000
)

// RemovedKeys lists AF_* keys that appear on a removed line of the last commits of the pinned
// revision, newest removal first, each with its commit. The caller checks they are gone from the
// current files. The journals are left out of the scan: they quote old keys while describing them.
func (r agentMemLintRepo) RemovedKeys() ([]memorylint.RemovedKey, error) {
	ctx, cancel := context.WithTimeout(context.Background(), agentMemLintGitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "--git-dir="+r.gitDir, "log", "-n", strconv.Itoa(agentMemLintKeyCommits),
		"--no-merges", "-p", "-U0", "--no-color", "--no-ext-diff", "--format=%x01%h%x02%s", r.ref, "--", ":(exclude,top)docs/log")
	// A work tree in the environment must not narrow the pathspec to a subtree.
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GIT_WORK_TREE=") && !strings.HasPrefix(e, "GIT_DIR=") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "GIT_NO_REPLACE_OBJECTS=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, errors.New("git log failed")
	}
	if err := cmd.Start(); err != nil {
		return nil, errors.New("git log failed")
	}
	var out []memorylint.RemovedKey
	seen := map[string]bool{}
	commit, subject := "", ""
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		line := sc.Text()
		if rest, ok := strings.CutPrefix(line, "\x01"); ok {
			commit, subject, _ = strings.Cut(rest, "\x02")
			continue
		}
		if !strings.HasPrefix(line, "-") || strings.HasPrefix(line, "---") || len(out) >= agentMemLintKeyMax {
			continue
		}
		for _, k := range agentMemLintKeyRe.FindAllString(line, -1) {
			if !seen[k] {
				seen[k] = true
				out = append(out, memorylint.RemovedKey{Key: k, Commit: commit, Subject: subject})
			}
		}
	}
	scanErr := sc.Err()
	_, _ = io.Copy(io.Discard, stdout)
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return nil, errors.New("git log timed out")
		}
		return nil, fmt.Errorf("git log: %w", err)
	}
	if scanErr != nil {
		// A line past the buffer ends the scan early: say so rather than propose from half a history.
		return nil, fmt.Errorf("git log output could not be read: %w", scanErr)
	}
	return out, nil
}

// FindSymbols streams `git grep -o -w -F` over the ref in chunks and keeps only which symbols
// occurred, so a symbol found in thousands of places costs a set entry, not memory.
func (r agentMemLintRepo) FindSymbols(syms []string) (map[string]bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), agentMemLintGitTimeout)
	defer cancel()
	found := make(map[string]bool, len(syms))
	for i := 0; i < len(syms); i += agentMemLintSymChunk {
		chunk := syms[i:min(i+agentMemLintSymChunk, len(syms))]
		var pat bytes.Buffer
		for _, s := range chunk {
			pat.WriteString(s + "\n")
		}
		cmd := exec.CommandContext(ctx, "git", "--git-dir="+r.gitDir, "grep", "-h", "-o", "-w", "-F", "-I", "-f", "-", r.ref)
		cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "GIT_NO_REPLACE_OBJECTS=1")
		cmd.Stdin = &pat
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return nil, errors.New("git grep failed")
		}
		if err := cmd.Start(); err != nil {
			return nil, errors.New("git grep failed")
		}
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			found[sc.Text()] = true
		}
		_, _ = io.Copy(io.Discard, stdout) // drain after a scanner error so Wait cannot block
		werr := cmd.Wait()
		// git grep exits 1 when nothing matched.
		var ee *exec.ExitError
		if werr != nil && !(errors.As(werr, &ee) && ee.ExitCode() == 1) {
			if ctx.Err() != nil {
				return nil, errors.New("git grep timed out")
			}
			return nil, errors.New("git grep failed")
		}
	}
	return found, nil
}

// agentMemLintDefaultRef names the project's default branch as the remote has it. No fetch
// happens: the report says which ref and commit it read, so a stale clone is visible.
func agentMemLintDefaultRef(ctx context.Context, gitDir string) (ref string, ok bool) {
	if out, err := agentMemLintGit(ctx, gitDir, nil, "symbolic-ref", "-q", "refs/remotes/origin/HEAD"); err == nil {
		if r := strings.TrimSpace(string(out)); strings.HasPrefix(r, "refs/remotes/origin/") {
			if _, err := agentMemLintGit(ctx, gitDir, nil, "rev-parse", "--verify", "-q", r+"^{commit}"); err == nil {
				return r, true
			}
		}
	}
	for _, b := range []string{"main", "master"} {
		r := "refs/remotes/origin/" + b
		if _, err := agentMemLintGit(ctx, gitDir, nil, "rev-parse", "--verify", "-q", r+"^{commit}"); err == nil {
			return r, true
		}
	}
	return "", false
}

// agentMemLintRepoFor opens the repository reader for a project, or says why the repository
// checks do not run. A project without git, an orphaned one (its working copy is gone, so the
// repository cannot be found) and one without an origin default branch get link, term and size
// checks only.
func agentMemLintRepoFor(p *agentMemProject) (repo memorylint.Repo, ref string, why string) {
	if p == nil {
		return nil, "", "user scope: no repository"
	}
	if p.VCS != projcfg.VCSGit {
		return nil, "", "project has no git repository"
	}
	if p.Root == "" || !filepath.IsAbs(p.Root) {
		return nil, "", "project root is unknown"
	}
	// The record is a file agents' shells can write: only a root under ~/repos is entered.
	rel, err := filepath.Rel(gitx.ReposRoot(), filepath.Clean(p.Root))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return nil, "", "project root is outside the workspace's repositories"
	}
	if agentMemRootGone(p.Root) {
		return nil, "", "orphaned project: its working copy is gone"
	}
	common, err := gitx.Run(p.Root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || common == "" {
		return nil, "", "the repository could not be found"
	}
	ctx, cancel := context.WithTimeout(context.Background(), agentMemLintGitTimeout)
	defer cancel()
	r, ok := agentMemLintDefaultRef(ctx, common)
	if !ok {
		return nil, "", "no origin/<default> branch in the repository"
	}
	// Pin the revision: a fetch by another session while the lint runs must not give the path
	// check one tree and the symbol check another. Only the displayed ref keeps its name.
	out, err := agentMemLintGit(ctx, common, nil, "rev-parse", "--verify", "-q", r+"^{commit}")
	oid := strings.TrimSpace(string(out))
	if err != nil || oid == "" {
		return nil, "", "no origin/<default> branch in the repository"
	}
	return agentMemLintRepo{gitDir: common, ref: oid, memo: &agentMemLintRenameMemo{ref: oid}}, strings.TrimPrefix(r, "refs/remotes/"), ""
}

// agentMemLintWire is what the Console and `af-memory lint` read.
type agentMemLintWire struct {
	Scope   string           `json:"scope"`
	Project *agentMemProject `json:"project,omitempty"`
	// Ref and Commit say which revision the repository checks read; empty when they did not run.
	Ref    string `json:"ref,omitempty"`
	Commit string `json:"commit,omitempty"`
	// Withheld counts files left out because they failed the scan or cannot be parsed.
	Withheld int `json:"withheld,omitempty"`
	// Terms is how many retired terms were applied.
	Terms  int               `json:"terms"`
	Report memorylint.Report `json:"report"`
}

// agentMemMemberLint lints one scope. Read-only.
func agentMemMemberLint(scope, project string) (agentMemLintWire, error) {
	out, _, _, _, err := agentMemLintCollect(scope, project)
	return out, err
}

// agentMemLintCollect runs the checks and also hands back what the apply step needs: the entries
// as loaded (their revisions are the ones the findings were computed on), the caller of the scope
// and the repository reader (nil when the repository checks did not run).
func agentMemLintCollect(scope, project string) (agentMemLintWire, []agentMemEntry, agentMemCaller, memorylint.Repo, error) {
	return agentMemLintCollectMemo(scope, project, nil)
}

// agentMemLintCollectMemo is agentMemLintCollect that reuses the rename history another collect of
// the same run already read, when it is the history of the same pinned revision.
func agentMemLintCollectMemo(scope, project string, memo *agentMemLintRenameMemo) (agentMemLintWire, []agentMemEntry, agentMemCaller, memorylint.Repo, error) {
	out := agentMemLintWire{Scope: scope}
	c, err := agentMemMemberCaller(scope, project)
	if err != nil {
		return out, nil, c, nil, err
	}
	agentMemMu.RLock()
	es, withheld, err := agentMemLoadScope(scope, c)
	agentMemMu.RUnlock()
	if err != nil {
		return out, nil, c, nil, err
	}
	mems := make([]memorylint.Memory, 0, len(es))
	for _, e := range es {
		mems = append(mems, memorylint.Memory{Name: e.Name, Body: e.Body, Anchors: e.Anchors})
	}
	out.Project, out.Withheld = c.Project, withheld
	doc := agentMemLintLoadDoc()
	terms := doc.Terms
	out.Terms = len(terms)

	var repo memorylint.Repo
	var why string
	if scope == agentMemScopeProject {
		repo, out.Ref, why = agentMemLintRepoFor(c.Project)
		if r, ok := repo.(agentMemLintRepo); ok && memo != nil && memo.ref == r.ref {
			r.memo = memo
			repo = r
		}
	} else {
		why = "user scope: no repository"
	}
	if repo != nil {
		// The commit shown is the one the checks read, because the reader is pinned to it.
		if oid := repo.(agentMemLintRepo).ref; len(oid) >= 12 {
			out.Commit = oid[:12]
		}
	}
	out.Report = memorylint.Lint(mems, memorylint.Options{RetiredTerms: terms, RejectedTerms: doc.Rejected}, repo)
	if repo == nil {
		out.Report.Skipped = append(out.Report.Skipped, "repository checks (paths, symbols, ADRs): "+why)
	}
	sort.Strings(out.Report.Skipped)
	return out, es, c, repo, nil
}

// HandleAgentMemoryMemberLint serves the report to the Console and the CLI. Not gated by the
// switch, writes nothing, counts no use.
func HandleAgentMemoryMemberLint(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	out, err := agentMemMemberLint(q.Get("scope"), q.Get("project"))
	if err != nil {
		agentMemWriteErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// HandleAgentMemoryLintTerms returns the member's retired-term list.
func HandleAgentMemoryLintTerms(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, agentMemLintLoadDoc())
}

// HandleAgentMemoryLintTermsSave replaces the list. Member-origin only: the list decides what
// the report calls retired, so a session with AGENT_TOKEN alone must not shape it.
func HandleAgentMemoryLintTermsSave(w http.ResponseWriter, r *http.Request) {
	if !memberorigin.RefuseUnverified(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MemberWriteMaxRequest)
	var req agentMemLintTermsDoc
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	// A body without "terms" is a mistake, not a request to empty the list.
	if req.Terms == nil {
		agentMemWriteErr(w, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "terms is required (an empty array clears the list)"))
		return
	}
	terms, err := agentMemLintCleanTerms(req.Terms)
	if err != nil {
		agentMemWriteErr(w, err)
		return
	}
	doc := agentMemLintTermsDoc{Terms: terms, Rejected: agentMemLintLoadDoc().Rejected}
	if req.Rejected != nil {
		if doc.Rejected, err = agentMemLintCleanTerms(req.Rejected); err != nil {
			agentMemWriteErr(w, err)
			return
		}
	}
	if err := agentMemLintSaveTerms(doc); err != nil {
		agentMemWriteErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, doc)
}

// agentMemLintApplyReq names the scope one run covers.
type agentMemLintApplyReq struct {
	Scope   string `json:"scope"`
	Project string `json:"project,omitempty"`
}

// HandleAgentMemoryLintApply publishes lint's findings as reasons and applies its renames for one
// scope (decision 5, step 2). It answers to the memory switch and needs no member: what it writes
// is what the read-only report already says, as changes authored `lint` that a member can revert.
func HandleAgentMemoryLintApply(w http.ResponseWriter, r *http.Request) {
	if !agentMemRequireEnabled(w) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, agentMemSaveMaxRequest)
	var req agentMemLintApplyReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	out, err := agentMemLintApply(req.Scope, req.Project, time.Now())
	if err != nil {
		agentMemWriteErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
