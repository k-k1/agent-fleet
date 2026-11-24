package memoryx

// The proposed change for one promotion candidate (ADR 0130 decision 11): which document of the
// repository, which section, the text and the memory it came from, as a patch over the default
// branch the candidate was judged on. The text goes through AF's body secret scan and the
// repository's own pre-commit hook, because one member's memory may name hosts or accounts a
// shared repository must not.
//
// AF never commits, pushes or opens a pull request: the patch is built and the hook is run in a
// throwaway clone that shares the repository's objects and is deleted before the answer. The
// member's working copy, its index and its refs are not touched, nothing is fetched, and the
// memory store is not written. The member opens the pull request, or asks a session to.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memorypromote"
)

const (
	// agentMemProposeHookTimeout bounds the repository's pre-commit hook. This repository's hook
	// builds and runs a Go scanner and gitleaks; a hook that needs longer is reported as timed out.
	agentMemProposeHookTimeout = 3 * time.Minute
	// agentMemProposeCloneTimeout bounds the throwaway clone and its checkout (measured: 7 s for
	// this repository's 137 MB tree).
	agentMemProposeCloneTimeout = 2 * time.Minute
	// agentMemProposeOutputMax bounds the hook output carried back.
	agentMemProposeOutputMax = 16 << 10
)

// agentMemProposeDocs are the documents tried, in order, when no anchor of the memory is a
// Markdown file: the ones every agent of the repository reads.
var agentMemProposeDocs = []string{"AGENTS.md", "CLAUDE.md", "README.md"}

// agentMemProposeBusy lets one proposal run at a time: each checks out the whole tree and runs a
// hook, on a host whose memory every session shares.
var agentMemProposeBusy sync.Mutex

// agentMemProposeReq names the candidate and, optionally, where its text should go.
type agentMemProposeReq struct {
	Project string `json:"project"`
	Name    string `json:"name"`
	// File is a normalised path of a Markdown file in the repository; empty picks one.
	File string `json:"file,omitempty"`
	// Section is the heading the text goes under; empty derives one from the description.
	Section string `json:"section,omitempty"`
	// Days is the quiet period the candidate is judged with (default as for the list).
	Days int `json:"days,omitempty"`
}

// agentMemProposeSource is one memory the proposed text came from, at the revision quoted.
type agentMemProposeSource struct {
	Name        string `json:"name"`
	Scope       string `json:"scope"`
	Revision    int    `json:"revision"`
	Description string `json:"description,omitempty"`
}

// agentMemPreCommit is the outcome of the repository's own pre-commit hook on the patch.
type agentMemPreCommit struct {
	// Ran is false when there is no hook to run (Note says why) or it could not be started.
	Ran bool `json:"ran"`
	// Hook is where the hook was found: core.hooksPath as configured, or hooks/ of the repository.
	Hook   string `json:"hook,omitempty"`
	Passed bool   `json:"passed"`
	// Absent: the repository has no hook git would run on a commit, so there is nothing to pass.
	Absent bool   `json:"absent,omitempty"`
	Output string `json:"output,omitempty"`
	Note   string `json:"note,omitempty"`
}

// agentMemProposeScans is what each scan found.
type agentMemProposeScans struct {
	Secret    []memorySecretFinding `json:"secret"`
	PreCommit agentMemPreCommit     `json:"preCommit"`
}

// agentMemProposeTarget is the document target the member's supersede will name after the merge.
type agentMemProposeTarget struct {
	Repo    string `json:"repo"`
	Path    string `json:"path"`
	Heading string `json:"heading"`
}

// agentMemProposeWire is what `af-memory promote-proposal` reads.
type agentMemProposeWire struct {
	Project *agentMemProject `json:"project,omitempty"`
	// Ref and Commit name the default branch's revision the patch applies to.
	Ref           string                  `json:"ref"`
	Commit        string                  `json:"commit"`
	Target        agentMemProposeTarget   `json:"target"`
	FileExists    bool                    `json:"fileExists"`
	SectionExists bool                    `json:"sectionExists"`
	Sources       []agentMemProposeSource `json:"sources"`
	// Text and Patch are left out when the secret scan found something: the member fixes the
	// memory first.
	Text     string               `json:"text,omitempty"`
	Patch    string               `json:"patch,omitempty"`
	Warnings []string             `json:"warnings"`
	Scans    agentMemProposeScans `json:"scans"`
	// Ready is true when both scans passed (a repository without a pre-commit hook passes that
	// one). Warnings do not change it.
	Ready bool `json:"ready"`
}

// agentMemDocPathOK says whether p is a normalised relative path, the form a document target
// stores (agentMemSupersededProblems applies the same rule to a stored one).
func agentMemDocPathOK(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") || strings.HasSuffix(p, "/") ||
		agentMemDocBadChars.MatchString(p) || utf8.RuneCountInString(p) > agentMemMaxDocField {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

func agentMemIsMarkdown(p string) bool {
	e := strings.ToLower(filepath.Ext(p))
	return e == ".md" || e == ".markdown"
}

// agentMemSectionOK checks a section heading given by the member.
func agentMemSectionOK(s string) bool {
	s = strings.TrimSpace(s)
	return s != "" && !agentMemDocBadChars.MatchString(s) && utf8.RuneCountInString(s) <= memorypromote.MaxSection &&
		!strings.HasPrefix(s, "#")
}

// agentMemTreeBlob says whether path is a regular file (not a symlink, submodule or directory) in
// commit, and whether it exists at all.
func agentMemTreeBlob(ctx context.Context, gitDir, commit, path string) (blob, exists bool, err error) {
	out, err := agentMemLintGit(ctx, gitDir, nil, "ls-tree", "-z", commit, "--", path)
	if err != nil {
		return false, false, err
	}
	rec := strings.TrimSuffix(string(out), "\x00")
	if rec == "" {
		return false, false, nil
	}
	meta, name, _ := strings.Cut(rec, "\t")
	f := strings.Fields(meta)
	if name != path || len(f) != 3 {
		// A directory's ls-tree without -r names the directory itself; anything else is not a file.
		return false, true, nil
	}
	return f[1] == "blob" && (f[0] == "100644" || f[0] == "100755"), true, nil
}

// agentMemProposeFile picks the document: the first anchor that is a Markdown file on the default
// branch, else the first of agentMemProposeDocs present, else AGENTS.md as a new file.
func agentMemProposeFile(ctx context.Context, gitDir, commit string, anchors []string) (string, error) {
	try := []string{}
	for _, a := range anchors {
		if a = strings.TrimSpace(a); agentMemDocPathOK(a) && agentMemIsMarkdown(a) {
			try = append(try, a)
		}
	}
	try = append(try, agentMemProposeDocs...)
	for _, p := range try {
		ok, _, err := agentMemTreeBlob(ctx, gitDir, commit, p)
		if err != nil {
			return "", err
		}
		if ok {
			return p, nil
		}
	}
	return agentMemProposeDocs[0], nil
}

// agentMemProposeEnv is the environment of every step in the throwaway clone: nothing that points
// git at another repository, no prompts, no lazy fetch.
func agentMemProposeEnv() []string {
	var env []string
	for _, e := range os.Environ() {
		k, _, _ := strings.Cut(e, "=")
		switch k {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES",
			"GIT_COMMON_DIR", "GIT_NAMESPACE", "GIT_PREFIX":
			continue
		}
		env = append(env, e)
	}
	return append(env, "GIT_TERMINAL_PROMPT=0", "GIT_NO_LAZY_FETCH=1", "GIT_OPTIONAL_LOCKS=0")
}

// agentMemProposeRun runs one command in its own process group under ctx; on the deadline the
// whole group is killed, so nothing a hook started outlives the request.
func agentMemProposeRun(ctx context.Context, dir string, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = agentMemProposeEnv()
	var out agentMemCappedBuf
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if errors.Is(err, exec.ErrWaitDelay) {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return out.Bytes(), err
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
		return out.Bytes(), ctx.Err()
	}
}

// agentMemCappedBuf keeps the first agentMemProposeOutputMax bytes written to it and drops the
// rest, so a chatty hook cannot fill the Agent's memory.
type agentMemCappedBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
	cut bool
}

func (b *agentMemCappedBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := agentMemProposeOutputMax - b.buf.Len(); room < len(p) {
		if room > 0 {
			b.buf.Write(p[:room])
		}
		b.cut = true
		return len(p), nil
	}
	b.buf.Write(p)
	return len(p), nil
}

func (b *agentMemCappedBuf) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := append([]byte(nil), b.buf.Bytes()...)
	if b.cut {
		out = append(out, "\n… (output cut)\n"...)
	}
	return out
}

// agentMemProposeHooks resolves the directory the repository's hooks are taken from:
// core.hooksPath as the working copy configures it (a relative one is relative to the top of the
// tree, so it is resolved in the clone, from the default branch, or in the working copy when the
// tree has no hook there), else hooks/ of the repository.
func agentMemProposeHooks(root, gitDir, clone string) (dir, shown string) {
	v, err := gitx.Run(root, "config", "--type=path", "--get", "core.hooksPath")
	if err != nil || v == "" {
		return filepath.Join(gitDir, "hooks"), "hooks/ of the repository"
	}
	if filepath.IsAbs(v) {
		return v, "core.hooksPath " + v
	}
	// A hook directory the tree does not carry (one a tool generates, left untracked) is the
	// working copy's: the one git would run when the member commits.
	if _, err := os.Stat(filepath.Join(clone, v, "pre-commit")); err != nil {
		return filepath.Join(root, v), "core.hooksPath " + v + " (from the working copy)"
	}
	return filepath.Join(clone, v), "core.hooksPath " + v
}

// agentMemProposePatch writes data to file in a throwaway clone at commit, stages it, and answers
// the staged patch and the pre-commit hook's outcome. The clone shares the repository's objects
// (--shared) and is removed before returning.
func agentMemProposePatch(root, gitDir, commit, file string, data []byte) (string, agentMemPreCommit, error) {
	var pc agentMemPreCommit
	tmp, err := os.MkdirTemp("", "af-memory-promote-")
	if err != nil {
		return "", pc, err
	}
	defer os.RemoveAll(tmp)
	clone := filepath.Join(tmp, "repo")
	empty := filepath.Join(tmp, "no-hooks")
	if err := os.Mkdir(empty, 0o700); err != nil {
		return "", pc, err
	}
	// Until the repository's own hook runs, no hook runs at all: a global core.hooksPath would
	// otherwise fire post-checkout in the clone.
	quiet := []string{"-c", "core.hooksPath=" + empty, "-c", "advice.detachedHead=false"}
	ctx, cancel := context.WithTimeout(context.Background(), agentMemProposeCloneTimeout)
	defer cancel()
	step := func(args ...string) error {
		out, err := agentMemProposeRun(ctx, tmp, "git", append(quiet, args...)...)
		if err != nil {
			return fmt.Errorf("git %s: %v: %s", args[0], err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	if err := step("clone", "-q", "--shared", "--no-checkout", "--", gitDir, clone); err != nil {
		return "", pc, err
	}
	if err := step("-C", clone, "checkout", "-q", "--detach", commit); err != nil {
		return "", pc, err
	}
	dst := filepath.Join(clone, filepath.FromSlash(file))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", pc, err
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return "", pc, err
	}
	if err := step("-C", clone, "add", "--", file); err != nil {
		return "", pc, err
	}
	patch, err := agentMemProposeRun(ctx, tmp, "git", append(quiet, "-C", clone, "diff", "--cached", "--no-color",
		"--no-ext-diff", "--no-textconv", "--no-renames")...)
	if err != nil {
		return "", pc, fmt.Errorf("git diff: %v", err)
	}

	hooks, shown := agentMemProposeHooks(root, gitDir, clone)
	pc.Hook = shown
	st, err := os.Stat(filepath.Join(hooks, "pre-commit"))
	switch {
	case err != nil || !st.Mode().IsRegular():
		pc.Absent, pc.Note = true, "the repository has no pre-commit hook ("+shown+")"
		return string(patch), pc, nil
	case st.Mode().Perm()&0o111 == 0:
		pc.Absent, pc.Note = true, "the pre-commit hook is not executable ("+shown+"), so git would not run it either"
		return string(patch), pc, nil
	}
	hctx, hcancel := context.WithTimeout(context.Background(), agentMemProposeHookTimeout)
	defer hcancel()
	out, err := agentMemProposeRun(hctx, clone, "git", "-c", "core.hooksPath="+hooks, "hook", "run", "pre-commit")
	pc.Ran = true
	pc.Output = strings.ReplaceAll(string(out), clone+"/", "")
	var ee *exec.ExitError
	switch {
	case err == nil:
		pc.Passed = true
	case errors.Is(err, context.DeadlineExceeded):
		pc.Note = fmt.Sprintf("the hook did not finish within %s", agentMemProposeHookTimeout)
	case errors.As(err, &ee):
		pc.Note = fmt.Sprintf("the hook failed (exit %d)", ee.ExitCode())
	default:
		pc.Ran = false
		pc.Note = "the hook could not be run: " + err.Error()
	}
	return string(patch), pc, nil
}

// agentMemPropose builds the proposed change for one current candidate. It writes nothing.
func agentMemPropose(req agentMemProposeReq, now time.Time) (agentMemProposeWire, error) {
	var out agentMemProposeWire
	bad := func(format string, a ...any) error {
		return memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, format, a...)
	}
	conflict := func(format string, a ...any) error {
		return memoryErrf(http.StatusConflict, errCodeMemoryConflict, format, a...)
	}
	req.Name, req.File, req.Section = strings.TrimSpace(req.Name), strings.TrimSpace(req.File), strings.TrimSpace(req.Section)
	switch {
	case req.Project == "":
		return out, bad("project is required")
	case agentMemCheckName(req.Name) != nil:
		return out, bad("name is not a memory name")
	case req.File != "" && (!agentMemDocPathOK(req.File) || !agentMemIsMarkdown(req.File)):
		return out, bad("file must be a normalised relative path of a Markdown file (.md)")
	case req.Section != "" && !agentMemSectionOK(req.Section):
		return out, bad("section must be one line of at most %d characters, without the leading #", memorypromote.MaxSection)
	case req.Days < 0 || req.Days > agentMemPromoteMaxDays:
		return out, bad("days is a whole number from 1 to %d", agentMemPromoteMaxDays)
	}
	days := req.Days
	if days == 0 {
		days = int(memorypromote.DefaultQuiet / (24 * time.Hour))
	}
	if !agentMemProposeBusy.TryLock() {
		return out, conflict("another promotion proposal is being checked; try again when it is done")
	}
	defer agentMemProposeBusy.Unlock()

	cands, repo, err := agentMemPromoteCandidatesRepo(req.Project, days, now)
	if err != nil {
		return out, err
	}
	out.Project, out.Ref = cands.Project, cands.Ref
	var cand *memorypromote.Candidate
	for i := range cands.Candidates {
		if cands.Candidates[i].Name == req.Name {
			cand = &cands.Candidates[i]
		}
	}
	if cand == nil {
		why := "it is not a memory of a promotable kind in this project"
		if len(cands.Skipped) > 0 {
			why = "nothing could be checked: " + strings.Join(cands.Skipped, "; ")
		}
		for _, h := range cands.Held {
			if h.Name == req.Name {
				var rules []string
				for _, m := range h.Missing {
					rules = append(rules, m.Rule)
				}
				why = "it is held: " + strings.Join(rules, ", ")
			}
		}
		return out, conflict("%s is not a promotion candidate: %s", req.Name, why)
	}
	lr, ok := repo.(agentMemLintRepo)
	if !ok || out.Project == nil {
		return out, conflict("the repository could not be read")
	}
	if out.Project.Remote == "" {
		return out, conflict("the project has no origin URL to name its repository by")
	}
	out.Commit = lr.ref

	c, err := agentMemMemberCaller(agentMemScopeProject, req.Project)
	if err != nil {
		return out, err
	}
	agentMemMu.RLock()
	cur, exists, err := agentMemLoadOne(c, agentMemScopeProject, req.Name)
	agentMemMu.RUnlock()
	switch {
	case err != nil:
		return out, err
	case !exists || cur.Withheld || agentMemExcluded(cur.Entry):
		return out, conflict("%s is no longer readable", req.Name)
	case cur.Entry.Revision != cand.Revision:
		// The candidate was judged on another revision; its confirmations do not vouch for this one.
		return out, conflict("%s moved to revision %d while it was being checked; ask again", req.Name, cur.Entry.Revision)
	}
	e := cur.Entry
	out.Sources = []agentMemProposeSource{{Name: e.Name, Scope: agentMemScopeProject, Revision: e.Revision, Description: e.Description}}

	ctx, cancel := context.WithTimeout(context.Background(), agentMemLintGitTimeout)
	defer cancel()
	file := req.File
	if file == "" {
		if file, err = agentMemProposeFile(ctx, lr.gitDir, lr.ref, e.Anchors); err != nil {
			return out, fmt.Errorf("reading the default branch: %w", err)
		}
	}
	blob, exists, err := agentMemTreeBlob(ctx, lr.gitDir, lr.ref, file)
	switch {
	case err != nil:
		return out, fmt.Errorf("reading the default branch: %w", err)
	case exists && !blob:
		return out, bad("%s is not a regular file on %s", file, out.Ref)
	}
	var doc []byte
	if exists {
		if doc, err = lr.ReadFile(file); err != nil {
			return out, fmt.Errorf("reading %s: %w", file, err)
		}
	}
	section := req.Section
	if section == "" {
		section = memorypromote.DefaultSection(e.Description, e.Name)
	}
	text := strings.TrimSpace(e.Body)
	out.Target = agentMemProposeTarget{Repo: out.Project.Remote, Path: file, Heading: section}
	out.FileExists = exists
	newDoc, existed := memorypromote.Insert(doc, section, text)
	out.SectionExists = existed
	out.Warnings = []string{}
	if !exists {
		out.Warnings = append(out.Warnings, memorypromote.WarnNewFile)
	}
	if memorypromote.LanguageDiffers(text, doc) {
		out.Warnings = append(out.Warnings, memorypromote.WarnLanguage)
	}
	if memorypromote.HasMemoryLinks(text) {
		out.Warnings = append(out.Warnings, memorypromote.WarnMemoryLinks)
	}

	out.Scans.Secret = agentMemScanText(file, text+"\n"+section)
	if out.Scans.Secret == nil {
		out.Scans.Secret = []memorySecretFinding{}
	}
	if len(out.Scans.Secret) > 0 {
		// The heading may be what the scan flagged: the answer does not repeat it.
		out.Target.Heading = ""
		out.Scans.PreCommit.Note = "not run: the secret scan found something in the text"
		return out, nil
	}
	patch, pc, err := agentMemProposePatch(out.Project.Root, lr.gitDir, lr.ref, file, newDoc)
	if err != nil {
		return out, fmt.Errorf("building the patch: %w", err)
	}
	out.Text, out.Patch, out.Scans.PreCommit = text, patch, pc
	out.Ready = pc.Passed || pc.Absent
	return out, nil
}

// HandleAgentMemoryPromoteProposal answers the proposed change for one promotion candidate. It
// writes nothing to the store or the working copy; it runs the repository's pre-commit hook in a
// throwaway clone, so it is a POST.
func HandleAgentMemoryPromoteProposal(w http.ResponseWriter, r *http.Request) {
	var req agentMemProposeReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	out, err := agentMemPropose(req, time.Now())
	if err != nil {
		agentMemWriteErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
