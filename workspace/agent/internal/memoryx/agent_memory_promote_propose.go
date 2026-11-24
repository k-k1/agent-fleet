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
//
// What the clone and the hook are given is narrowed on purpose: no GIT_* variable of the Agent
// (one could point git at another work tree or add configuration), no global or system git
// configuration for the steps that build the patch (a smudge filter would run, and could reach
// the network), no Agent credential in the hook's environment, and a TMPDIR inside the throwaway
// directory so what a hook leaves there goes with it. The hook itself is the repository's code,
// run as the Agent's user, as `git commit` in the working copy would run it.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
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
	// agentMemProposeOutputMax bounds the hook output carried back.
	agentMemProposeOutputMax = 16 << 10
	// agentMemProposePatchMax bounds the patch. A memory body is at most agentMemMaxBody; the rest
	// is the diff's context. JSON may escape a byte as six (\u003c), and the CLI reads at most
	// 16 MiB of an answer, so 2 MiB of patch plus the text still fits.
	agentMemProposePatchMax = 2 << 20
	// agentMemProposeMaxRequest bounds the request body before it is decoded.
	agentMemProposeMaxRequest = 64 << 10
)

// agentMemProposeCloneTimeout bounds the throwaway clone, its checkout and the diff, and again the
// diff after the hook (measured: 7 s for this repository's 137 MB tree). A variable so a test can
// shorten it.
var agentMemProposeCloneTimeout = 2 * time.Minute

// agentMemProposeHookTimeout bounds the repository's pre-commit hook (this repository's hook
// builds and runs a Go scanner and gitleaks). A variable so a test can shorten it.
var agentMemProposeHookTimeout = 3 * time.Minute

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
// BodySHA256 names the exact text: a supersede must quote it back, because an edit of the store
// outside AF can change a body without a new revision.
type agentMemProposeSource struct {
	Name        string `json:"name"`
	Scope       string `json:"scope"`
	Revision    int    `json:"revision"`
	BodySHA256  string `json:"bodySha256"`
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
	Absent bool `json:"absent,omitempty"`
	// Changed: the hook left a different staged change than the one it was given (a formatter);
	// the patch answered is still the one given to it, and the proposal is not ready.
	Changed bool `json:"changed,omitempty"`
	// Output is the hook's output when it did not pass, with the throwaway paths removed; it is
	// withheld when the secret scan flags it.
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
	// one) and the hook left the change as it was. Warnings do not change it.
	Ready bool `json:"ready"`
}

// agentMemDocPathOK says whether p is a normalised relative path, the form a document target
// stores (agentMemSupersededProblems applies the same rule to a stored one), outside .git.
func agentMemDocPathOK(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") || strings.HasSuffix(p, "/") ||
		agentMemDocBadChars.MatchString(p) || utf8.RuneCountInString(p) > agentMemMaxDocField {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.EqualFold(seg, ".git") {
			return false
		}
	}
	return true
}

func agentMemIsMarkdown(p string) bool {
	e := strings.ToLower(filepath.Ext(p))
	return e == ".md" || e == ".markdown"
}

// agentMemSectionOK checks a section heading: one that reads back as itself once written.
func agentMemSectionOK(s string) bool { return memorypromote.SectionOK(s) }

// agentMemTreeEntry answers the mode and type of path in commit ("" when it does not exist).
func agentMemTreeEntry(ctx context.Context, gitDir, commit, path string) (mode, typ string, err error) {
	out, err := agentMemLintGit(ctx, gitDir, nil, "ls-tree", "-z", commit, "--", path)
	if err != nil {
		return "", "", err
	}
	rec := strings.TrimSuffix(string(out), "\x00")
	if rec == "" {
		return "", "", nil
	}
	meta, name, _ := strings.Cut(rec, "\t")
	f := strings.Fields(meta)
	if name != path || len(f) != 3 {
		return "", "", fmt.Errorf("unexpected ls-tree record for %s", path)
	}
	return f[0], f[1], nil
}

// agentMemTreeBlob says whether path is a regular file (not a symlink, submodule or directory) in
// commit, and whether it exists at all.
func agentMemTreeBlob(ctx context.Context, gitDir, commit, path string) (blob, exists bool, err error) {
	mode, typ, err := agentMemTreeEntry(ctx, gitDir, commit, path)
	if err != nil || typ == "" {
		return false, false, err
	}
	return typ == "blob" && (mode == "100644" || mode == "100755"), true, nil
}

// agentMemTreeParentsOK says whether every directory above path in commit is a real directory
// or absent: a symlink there would carry a new file outside the tree.
func agentMemTreeParentsOK(ctx context.Context, gitDir, commit, path string) (bool, error) {
	segs := strings.Split(path, "/")
	for i := 1; i < len(segs); i++ {
		_, typ, err := agentMemTreeEntry(ctx, gitDir, commit, strings.Join(segs[:i], "/"))
		if err != nil {
			return false, err
		}
		switch typ {
		case "":
			return true, nil
		case "tree":
		default:
			return false, nil
		}
	}
	return true, nil
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

// agentMemProposeEnv is the environment of a step in the throwaway directory: the Agent's, less
// every GIT_* variable (and, for the hook, the Agent's credentials), with TMPDIR inside tmp.
// Building the patch also drops the global and system git configuration.
func agentMemProposeEnv(tmp string, hook bool) []string {
	var env []string
	for _, e := range os.Environ() {
		k, _, _ := strings.Cut(e, "=")
		switch {
		case strings.HasPrefix(k, "GIT_"), k == "TMPDIR":
			continue
		case hook && (k == "AGENT_TOKEN" || strings.HasPrefix(k, "AF_")):
			continue
		}
		env = append(env, e)
	}
	env = append(env, "TMPDIR="+filepath.Join(tmp, "tmp"), "GIT_TERMINAL_PROMPT=0", "GIT_NO_LAZY_FETCH=1",
		"GIT_OPTIONAL_LOCKS=0", "GIT_NO_REPLACE_OBJECTS=1")
	if !hook {
		env = append(env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	}
	return env
}

// agentMemProposeRun runs one command in its own process group under ctx and answers its stdout
// and stderr apart. Whatever is left of the group when the command exits, or when ctx ends, is
// killed, so nothing a hook put in the background outlives the request.
func agentMemProposeRun(ctx context.Context, dir string, env []string, stdoutMax int, name string, args ...string) (stdout, stderr []byte, err error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	cmd := exec.Command(name, args...)
	cmd.Dir, cmd.Env = dir, env
	o := &agentMemCappedBuf{max: stdoutMax}
	e := &agentMemCappedBuf{max: agentMemProposeOutputMax}
	cmd.Stdout, cmd.Stderr = o, e
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
		err = ctx.Err()
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if err == nil && o.cut && stdoutMax == agentMemProposePatchMax {
		// Only a cut patch is an error; a hook's chatter past the cap is just not all shown.
		err = errors.New("the output is larger than allowed")
	}
	return o.Bytes(), e.Bytes(), err
}

// agentMemCappedBuf keeps the first max bytes written to it and drops the rest, so a chatty
// command cannot fill the Agent's memory; cut says that something was dropped.
type agentMemCappedBuf struct {
	mu  sync.Mutex
	max int
	buf bytes.Buffer
	cut bool
}

func (b *agentMemCappedBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := b.max - b.buf.Len(); room < len(p) {
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
	return append([]byte(nil), b.buf.Bytes()...)
}

// agentMemProposeHooks asks the working copy's git where its pre-commit hook is, which is where
// `git commit` there would look (core.hooksPath with git's own reading of it, else hooks/ of the
// repository), in the environment the hook gets. shown names the setting without a path when the
// path is absolute. ok=false when git could not answer.
func agentMemProposeHooks(ctx context.Context, root, tmp string) (hook, shown string, ok bool) {
	env := agentMemProposeEnv(tmp, true)
	// Not --path-format=absolute: it resolves the path on disk and fails for /dev/null/pre-commit,
	// which is how a hooksPath of /dev/null turns hooks off. A relative answer is relative to root.
	out, _, err := agentMemProposeRun(ctx, root, env, agentMemProposeOutputMax, "git", "rev-parse", "--git-path", "hooks/pre-commit")
	hook = strings.TrimSuffix(string(out), "\n")
	if err != nil || hook == "" {
		return "", "", false
	}
	// Concatenated, not joined: filepath.Join would fold "link/.." before the kernel follows the
	// link, and git's path is the kernel's.
	if !filepath.IsAbs(hook) {
		hook = strings.TrimSuffix(root, "/") + "/" + hook
	}
	v, _, err := agentMemProposeRun(ctx, root, env, agentMemProposeOutputMax, "git", "config", "--get", "core.hooksPath")
	var ee *exec.ExitError
	switch val := strings.TrimSuffix(string(v), "\n"); {
	case err == nil && (filepath.IsAbs(val) || strings.HasPrefix(val, "~")):
		shown = "core.hooksPath (an absolute path)"
	case err == nil:
		shown = "core.hooksPath " + val
	case errors.As(err, &ee) && ee.ExitCode() == 1:
		// Exit 1 is git config's "not set".
		shown = "hooks/ of the repository"
	default:
		return "", "", false
	}
	return hook, shown, true
}

// agentMemProposeScrub takes the local paths out of what the answer carries: the throwaway
// directory, the working copy, the hook's directory and the home directory.
func agentMemProposeScrub(s string, paths map[string]string) string {
	for _, k := range []string{"clone", "tmp", "hooks", "root", "home"} {
		p := paths[k]
		if p == "" || p == "/" {
			continue
		}
		// A hook that prints realpath "$0" shows the path with its links resolved.
		if real, err := filepath.EvalSymlinks(p); err == nil && real != p && real != "/" {
			s = strings.ReplaceAll(s, real, "<"+k+">")
		}
		s = strings.ReplaceAll(s, p, "<"+k+">")
	}
	return s
}

// agentMemProposeShowOutput is what the answer carries of a failed hook's output: its stdout and
// stderr with the local paths taken out, or nothing when the secret scan flags it.
func agentMemProposeShowOutput(paths map[string]string, stdout, stderr []byte) string {
	s := strings.TrimRight(string(stdout), "\n")
	if t := strings.TrimRight(string(stderr), "\n"); t != "" {
		if s != "" {
			s += "\n"
		}
		s += t
	}
	s = agentMemProposeScrub(s, paths)
	if len(agentMemScanText("hook output", s)) > 0 {
		return "(withheld: the secret scan flags something in the hook's output)"
	}
	return s
}

// agentMemProposeRemoveAll removes the throwaway directory even when a hook left a directory in it
// without write permission; what cannot be removed is logged.
func agentMemProposeRemoveAll(tmp string) {
	if os.RemoveAll(tmp) == nil {
		return
	}
	_ = filepath.WalkDir(tmp, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(p, 0o700)
		}
		return nil
	})
	if err := os.RemoveAll(tmp); err != nil {
		log.Printf("memory promotion: the throwaway directory could not be removed: %v", err)
	}
}

// agentMemProposePatch writes data to file in a throwaway clone at commit, stages it, and answers
// the staged patch and the pre-commit hook's outcome. The clone shares the repository's objects
// (--shared) and is removed before returning.
func agentMemProposePatch(ctx context.Context, root, gitDir, commit, file string, data []byte) (string, agentMemPreCommit, error) {
	var pc agentMemPreCommit
	tmp, err := os.MkdirTemp("", "af-memory-promote-")
	if err != nil {
		return "", pc, err
	}
	defer agentMemProposeRemoveAll(tmp)
	clone := filepath.Join(tmp, "repo")
	for _, d := range []string{"tmp", "no-hooks"} {
		if err := os.Mkdir(filepath.Join(tmp, d), 0o700); err != nil {
			return "", pc, err
		}
	}
	env := agentMemProposeEnv(tmp, false)
	// No hook of any kind runs while the patch is built; the git and work-tree directories are
	// named on every step.
	quiet := []string{"-c", "core.hooksPath=" + filepath.Join(tmp, "no-hooks"), "-c", "advice.detachedHead=false"}
	in := append(append([]string{}, quiet...), "--git-dir="+filepath.Join(clone, ".git"), "--work-tree="+clone)
	cctx, cancel := context.WithTimeout(ctx, agentMemProposeCloneTimeout)
	defer cancel()
	step := func(args ...string) ([]byte, error) {
		out, errOut, err := agentMemProposeRun(cctx, tmp, env, agentMemProposePatchMax, "git", args...)
		if err != nil {
			sub := ""
			for _, a := range args[len(quiet):] {
				if !strings.HasPrefix(a, "-") {
					sub = a
					break
				}
			}
			return nil, fmt.Errorf("git %s: %v: %s", sub, err, strings.TrimSpace(string(errOut)))
		}
		return out, nil
	}
	if _, err := step(append(append([]string{}, quiet...), "clone", "-q", "--shared", "--no-checkout", "--", gitDir, clone)...); err != nil {
		return "", pc, err
	}
	if _, err := step(append(in, "checkout", "-q", "--detach", commit)...); err != nil {
		return "", pc, err
	}
	// Writes stay inside the clone: os.Root refuses a path that leaves it through a symlink.
	r, err := os.OpenRoot(clone)
	if err != nil {
		return "", pc, err
	}
	defer r.Close()
	if dir := filepath.Dir(filepath.FromSlash(file)); dir != "." {
		if err := r.MkdirAll(dir, 0o755); err != nil {
			return "", pc, err
		}
	}
	if err := r.WriteFile(filepath.FromSlash(file), data, 0o644); err != nil {
		return "", pc, err
	}
	if _, err := step(append(in, "add", "--", file)...); err != nil {
		return "", pc, err
	}
	diff := func() (string, error) {
		out, err := step(append(in, "diff", "--cached", "--no-color", "--no-ext-diff", "--no-textconv", "--no-renames",
			"--binary", "--full-index")...)
		return string(out), err
	}
	patch, err := diff()
	switch {
	case err != nil:
		return "", pc, err
	case patch == "":
		return "", pc, errors.New("the change is empty")
	}

	hook, shown, ok := agentMemProposeHooks(ctx, root, tmp)
	if !ok {
		pc.Note = "git could not say where the working copy's pre-commit hook is, so it is not known what a commit would run"
		return patch, pc, nil
	}
	pc.Hook = shown
	home, _ := os.UserHomeDir()
	hooksDir := strings.TrimSuffix(hook, "/pre-commit")
	paths := map[string]string{"clone": clone, "tmp": tmp, "hooks": hooksDir, "root": root, "home": home}
	st, err := os.Stat(hook)
	switch {
	case err != nil && (errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)), err == nil && !st.Mode().IsRegular():
		// Not there, or core.hooksPath disables hooks (/dev/null): git runs nothing.
		pc.Absent, pc.Note = true, "the repository has no pre-commit hook ("+shown+")"
		return patch, pc, nil
	case err != nil:
		pc.Note = "the pre-commit hook could not be read: " + agentMemProposeScrub(err.Error(), paths)
		return patch, pc, nil
	case st.Mode().Perm()&0o111 == 0:
		pc.Absent, pc.Note = true, "the pre-commit hook is not executable ("+shown+"), so git would not run it either"
		return patch, pc, nil
	}
	hctx, hcancel := context.WithTimeout(ctx, agentMemProposeHookTimeout)
	defer hcancel()
	hargs := append(append([]string{}, quiet...), "--git-dir="+filepath.Join(clone, ".git"), "--work-tree="+clone)
	hargs[1] = "core.hooksPath=" + hooksDir
	stdout, stderr, err := agentMemProposeRun(hctx, clone, agentMemProposeEnv(tmp, true), agentMemProposeOutputMax, "git",
		append(hargs, "hook", "run", "pre-commit")...)
	pc.Ran = true
	var ee *exec.ExitError
	switch {
	case err == nil:
		pc.Passed = true
	case hctx.Err() != nil:
		pc.Note = fmt.Sprintf("the hook did not finish within %s or the request ended", agentMemProposeHookTimeout)
	case errors.As(err, &ee):
		pc.Note = fmt.Sprintf("the hook failed (exit %d)", ee.ExitCode())
	default:
		pc.Ran = false
		pc.Note = "the hook could not be run: " + agentMemProposeScrub(err.Error(), paths)
	}
	if !pc.Passed {
		pc.Output = agentMemProposeShowOutput(paths, stdout, stderr)
	}
	// What the hook checked must be what is answered. A hook that rewrites the staged change (a
	// formatter) leaves another change, which no scan has seen: the answer keeps the patch that was
	// scanned and says that the hook changed it.
	actx, acancel := context.WithTimeout(ctx, agentMemProposeCloneTimeout)
	defer acancel()
	after, _, err := agentMemProposeRun(actx, tmp, env, agentMemProposePatchMax, "git", append(in, "diff", "--cached", "--no-color",
		"--no-ext-diff", "--no-textconv", "--no-renames", "--binary", "--full-index")...)
	if err != nil {
		return "", pc, fmt.Errorf("git diff after the hook: %v", err)
	}
	if string(after) != patch {
		pc.Changed = true
		pc.Note = strings.TrimSpace(pc.Note + " the hook changed the staged change; the patch shown is the one given to it")
	}
	return patch, pc, nil
}

// agentMemSHA256 is the hex digest a proposal quotes for a body.
func agentMemSHA256(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// agentMemRemoteCheck reads the working copy's origin now and says whether it is still the
// repository the project is named by: a clone whose origin was switched to a fork would otherwise
// be checked as one repository and recorded as another.
func agentMemRemoteCheck(ctx context.Context, p *agentMemProject) error {
	conflict := func(format string, a ...any) error {
		return memoryErrf(http.StatusConflict, errCodeMemoryConflict, format, a...)
	}
	if p == nil || p.Remote == "" {
		return conflict("the project has no origin URL to name its repository by")
	}
	rctx, cancel := context.WithTimeout(ctx, agentMemLintGitTimeout)
	defer cancel()
	cmd := gitx.CmdContext(rctx, p.Root, "remote", "get-url", "origin")
	raw, err := cmd.Output()
	now := agentMemNormalizeRemote(string(raw))
	switch {
	case err != nil || now == "":
		return conflict("the working copy's origin could not be read")
	case now != p.Remote:
		return conflict("the working copy's origin is %q now, not the project's %q", now, p.Remote)
	}
	return nil
}

// agentMemPropose builds the proposed change for one current candidate. It writes nothing.
func agentMemPropose(ctx context.Context, req agentMemProposeReq, now time.Time) (agentMemProposeWire, error) {
	var out agentMemProposeWire
	bad := func(format string, a ...any) error {
		return memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, format, a...)
	}
	conflict := func(format string, a ...any) error {
		return memoryErrf(http.StatusConflict, errCodeMemoryConflict, format, a...)
	}
	req.Name, req.File = strings.TrimSpace(req.Name), strings.TrimSpace(req.File)
	switch {
	case req.Project == "":
		return out, bad("project is required")
	case agentMemCheckName(req.Name) != nil:
		return out, bad("name is not a memory name")
	case req.File != "" && (!agentMemDocPathOK(req.File) || !agentMemIsMarkdown(req.File)):
		return out, bad("file must be a normalised relative path of a Markdown file (.md), outside .git")
	case req.Section != "" && !agentMemSectionOK(req.Section):
		return out, bad("section must be one line of at most %d characters that reads back as the same heading (no closing #)", memorypromote.MaxSection)
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

	cands, repo, judged, err := agentMemPromoteCandidatesRepo(req.Project, days, now)
	if err != nil {
		return out, err
	}
	if err := ctx.Err(); err != nil {
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
	if err := agentMemRemoteCheck(ctx, out.Project); err != nil {
		return out, err
	}
	out.Commit = lr.ref

	c, err := agentMemMemberCaller(agentMemScopeProject, req.Project)
	if err != nil {
		return out, err
	}
	rel, err := agentMemScopeDir(agentMemScopeProject, c)
	if err != nil {
		return out, err
	}
	// The text is read again, with what makes it a candidate: a doubt or a dispute that arrived
	// after the selection, without a new revision, stops the proposal.
	agentMemMu.RLock()
	cur, exists, err := agentMemLoadOne(c, agentMemScopeProject, req.Name)
	var reports []memorypromote.Confirmation
	for _, r := range agentMemFeedbackRecords(rel, req.Name) {
		reports = append(reports, memorypromote.Confirmation{Session: r.Session, Revision: r.Revision, Verdict: r.Verdict})
	}
	agentMemMu.RUnlock()
	switch {
	case err != nil:
		return out, err
	case !exists || cur.Withheld || agentMemExcluded(cur.Entry):
		return out, conflict("%s is no longer readable", req.Name)
	case cur.Entry.Revision != cand.Revision:
		// The candidate was judged on another revision; its confirmations do not vouch for this one.
		return out, conflict("%s moved to revision %d while it was being checked; ask again", req.Name, cur.Entry.Revision)
	case len(cur.Entry.Reasons) > 0 || memorypromote.Disputed(reports, cur.Entry.Revision) > 0:
		return out, conflict("%s was doubted while it was being checked; ask again", req.Name)
	case judged[req.Name].Revision != cur.Entry.Revision || !agentMemPromoteSameText(judged[req.Name], cur.Entry):
		// The text proposed, and the anchors that pick its document, must be the ones that were
		// judged: an edit of the store outside AF changes them without a new revision.
		return out, conflict("%s changed while it was being checked; ask again", req.Name)
	}
	e := cur.Entry
	out.Sources = []agentMemProposeSource{{Name: e.Name, Scope: agentMemScopeProject, Revision: e.Revision,
		BodySHA256: agentMemSHA256(e.Body), Description: e.Description}}

	gctx, cancel := context.WithTimeout(ctx, agentMemLintGitTimeout)
	defer cancel()
	file := req.File
	if file == "" {
		if file, err = agentMemProposeFile(gctx, lr.gitDir, lr.ref, e.Anchors); err != nil {
			return out, fmt.Errorf("reading the default branch: %w", err)
		}
	}
	blob, exists, err := agentMemTreeBlob(gctx, lr.gitDir, lr.ref, file)
	if err != nil {
		return out, fmt.Errorf("reading the default branch: %w", err)
	}
	parentsOK, err := agentMemTreeParentsOK(gctx, lr.gitDir, lr.ref, file)
	switch {
	case err != nil:
		return out, fmt.Errorf("reading the default branch: %w", err)
	case exists && !blob:
		return out, bad("%s is not a regular file on %s", file, out.Ref)
	case !parentsOK:
		return out, bad("a directory above %s is not a directory on %s (a symlink or a file)", file, out.Ref)
	}
	var doc []byte
	if exists {
		if doc, err = agentMemLintGit(gctx, lr.gitDir, nil, "cat-file", "blob", lr.ref+":"+file); err != nil {
			return out, fmt.Errorf("reading %s: %w", file, err)
		}
	}
	section := req.Section
	if section == "" {
		section = memorypromote.DefaultSection(e.Description, e.Name)
	}
	text := memorypromote.TrimBlankLines(e.Body)
	out.Target = agentMemProposeTarget{Repo: out.Project.Remote, Path: file, Heading: section}
	out.FileExists = exists
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

	// Everything the proposal adds to the repository: the text, the heading and the file name.
	// The findings name a fixed label, not the file: the file name may be what they flag.
	out.Scans.Secret = agentMemScanText("proposal", strings.Join([]string{text, section, file}, "\n"))
	if out.Scans.Secret == nil {
		out.Scans.Secret = []memorySecretFinding{}
	}
	if len(out.Scans.Secret) > 0 {
		// The heading or the path may be what the scan flagged: the answer does not repeat them.
		out.Target.Heading, out.Target.Path = "", ""
		out.Scans.PreCommit.Note = "not run: the secret scan found something in the proposed change"
		return out, nil
	}
	newDoc, existed, err := memorypromote.Insert(doc, section, text)
	if err != nil {
		return out, conflict("%s: %v; name another file or section, or edit the memory", file, err)
	}
	out.SectionExists = existed
	patch, pc, err := agentMemProposePatch(ctx, out.Project.Root, lr.gitDir, lr.ref, file, newDoc)
	if err != nil {
		return out, fmt.Errorf("building the patch: %w", err)
	}
	out.Text, out.Patch, out.Scans.PreCommit = text, patch, pc
	out.Ready = (pc.Passed || pc.Absent) && !pc.Changed
	return out, nil
}

// HandleAgentMemoryPromoteProposal answers the proposed change for one promotion candidate. It
// writes nothing to the store or the working copy; it runs the repository's pre-commit hook in a
// throwaway clone, so it is a POST, and it stops with the request.
func HandleAgentMemoryPromoteProposal(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, agentMemProposeMaxRequest)
	var req agentMemProposeReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	out, err := agentMemPropose(r.Context(), req, time.Now())
	if err != nil {
		agentMemWriteErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
