package gitx

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/url"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secretpat"
)

// AutorunFilePath is where a repository keeps its memo queue settings (#4881).
const AutorunFilePath = ".agent-fleet/autorun.yaml"

// AutorunFileMaxBytes bounds the file. The Control Plane, which parses it, holds the same bound.
const AutorunFileMaxBytes = 64 << 10

// autorunWait bounds the whole read, every git step included: an unreachable remote, or a lazy
// fetch a partial clone makes for a missing blob, must not outlive the Console's request.
var autorunWait = 60 * time.Second

// AutorunFile is GET /repos/{name}/autorun-file: the settings file as committed on origin's
// default branch. Found is false when that commit has no such file. Remote names where it was
// read from (host and path only), so the person approving the settings sees it.
type AutorunFile struct {
	Path    string `json:"path"`
	Remote  string `json:"remote"`
	Branch  string `json:"branch"`
	Commit  string `json:"commit"`
	Found   bool   `json:"found"`
	Content string `json:"content"`
}

// autorunEnv keeps refs/replace out of every read: a run can write replace refs into the clone,
// and with them make the right commit id show other content. Paths are literal: a file named
// ":x" or "*" is that file, never pathspec magic or a glob.
var autorunEnv = []string{"GIT_NO_REPLACE_OBJECTS=1", "GIT_LITERAL_PATHSPECS=1"}

// HandleAutorunFile reads the settings file from origin's default branch only, never from the
// working tree or a working branch: a run in this workspace can write both, and must not be
// able to change the settings it runs under. The network steps go through the named remote, and
// origin's URL is compared before and after them (see readAutorunFile). Branch and commit are
// what origin answers, not refs/remotes/origin/*, which anything in the clone can repoint; the
// fetch only brings the objects. This is a guard rail, not the boundary: a run holds the same git
// credentials and the same clone configuration, and the member's approval of each difference is
// what applies anything (#4895). The Control Plane parses and validates the content.
//
// Messages and log lines are fixed per code: git's output may name hosts or carry credentials.
func HandleAutorunFile(w http.ResponseWriter, r *http.Request) {
	dir, ok := RepoDirFromPath(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), autorunWait)
	defer cancel()
	f, code := readAutorunFile(ctx, dir)
	if code != "" {
		log.Printf("autorun file: %s: %s", r.PathValue("name"), code)
		status := http.StatusBadGateway
		if code == "file_too_large" || code == "file_not_regular" || code == "file_not_utf8" {
			status = http.StatusUnprocessableEntity
		}
		httpx.WriteErr(w, status, code, autorunErrText[code])
		return
	}
	httpx.WriteJSON(w, http.StatusOK, f)
}

// AutorunInstructionMaxBytes bounds a category's instruction file: the Control Plane's own bound
// on a queue's instruction (memoQueueInstructionMax), which refuses anything longer anyway.
const AutorunInstructionMaxBytes = 16 << 10

// AutorunInstruction is GET /repos/{name}/autorun-file/instruction: the file a category's
// instruction_file names, as the commit the settings file was read from holds it. Found is false
// when that commit has no such file.
type AutorunInstruction struct {
	Path    string `json:"path"`
	Commit  string `json:"commit"`
	Found   bool   `json:"found"`
	Content string `json:"content"`
}

// HandleAutorunInstruction reads ?path= from ?commit=, which must be the full id of a commit the
// clone already holds: the one HandleAutorunFile answered, so the instruction comes from the same
// commit as the settings beside it. No ref is read or fetched, so a working branch or the working
// tree can never stand in for it. A partial clone may fetch the file's blob by its id, as the
// settings read does: the id pins the content, and the read's deadline bounds the fetch.
func HandleAutorunInstruction(w http.ResponseWriter, r *http.Request) {
	dir, ok := RepoDirFromPath(w, r)
	if !ok {
		return
	}
	sha, p := r.URL.Query().Get("commit"), r.URL.Query().Get("path")
	if !autorunSHARe.MatchString(sha) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_commit", "commit must be a full commit id")
		return
	}
	if !autorunRepoPath(p) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_path", "path must be a clean relative path inside the repository")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), autorunWait)
	defer cancel()
	out := AutorunInstruction{Path: p, Commit: sha}
	code := ""
	if !autorunIsCommit(ctx, dir, sha) {
		code = "commit_missing"
	} else {
		out.Content, out.Found, code = readAutorunBlob(ctx, dir, sha, p, AutorunInstructionMaxBytes)
	}
	if code != "" && ctx.Err() != nil {
		code = "timeout"
	}
	if code != "" {
		log.Printf("autorun instruction: %s: %s", r.PathValue("name"), code)
		status := http.StatusBadGateway
		switch code {
		case "file_too_large", "file_not_regular", "file_not_utf8":
			status = http.StatusUnprocessableEntity
		case "commit_missing":
			status = http.StatusConflict
		}
		httpx.WriteErr(w, status, code, autorunInstructionErrText[code])
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// autorunIsCommit says whether sha is the full id of a commit object in the clone. rev-parse
// expands a unique prefix (a 40-digit one in a SHA-256 clone) and cat-file -t names the object's
// own type, so neither a prefix nor an annotated tag that peels to a commit passes.
func autorunIsCommit(ctx context.Context, dir, sha string) bool {
	full, err := autorunGit(ctx, dir, "rev-parse", "--verify", "--quiet", sha)
	if err != nil || strings.TrimSpace(string(full)) != sha {
		return false
	}
	typ, err := autorunGit(ctx, dir, "cat-file", "-t", sha)
	return err == nil && strings.TrimSpace(string(typ)) == "commit"
}

var autorunInstructionErrText = map[string]string{
	"commit_missing":   "the working copy holds no commit with that full id; read the settings file again",
	"read_failed":      "the instruction file could not be read from the commit",
	"timeout":          "reading the instruction file took too long",
	"file_not_regular": "the instruction file is not a regular file",
	"file_too_large":   "the instruction file is larger than " + strconv.Itoa(AutorunInstructionMaxBytes) + " bytes",
	"file_not_utf8":    "the instruction file is not UTF-8 text",
}

// autorunRepoPath accepts what the Control Plane's parser accepts for instruction_file: a clean
// relative path with / as the separator that stays inside the repository. git would read a
// leading "-" or a pathspec's magic otherwise, and "--" plus the literal checks keep both out.
func autorunRepoPath(p string) bool {
	return p != "" && p != "." && p != ".." && !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "../") &&
		!strings.Contains(p, "\\") && !strings.ContainsRune(p, 0) && path.Clean(p) == p
}

var autorunErrText = map[string]string{
	"no_origin":         "the working copy has no origin remote",
	"no_default_branch": "origin did not name its default branch",
	"fetch_failed":      "fetching origin's default branch failed",
	"origin_moved":      "origin's default branch moved while it was read; try again",
	"origin_changed":    "the working copy's origin changed while it was read; try again",
	"read_failed":       "the settings file could not be read from the fetched commit",
	"timeout":           "reading the settings file took too long",
	"file_not_regular":  AutorunFilePath + " on the default branch is not a regular file",
	"file_too_large":    AutorunFilePath + " is larger than " + strconv.Itoa(AutorunFileMaxBytes) + " bytes",
	"file_not_utf8":     AutorunFilePath + " on the default branch is not UTF-8 text",
}

// autorunGit runs one git step under the read's deadline, in its own process group: on the
// deadline the whole group is killed, so a transport helper or a lazy fetch git started does not
// outlive the request.
func autorunGit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cmd := Cmd(dir, args...)
	cmd.Env = append(cmd.Env, autorunEnv...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		// git exited but something it started still held the output: that group is still
		// alive, and is cleared like a deadline's.
		if errors.Is(err, exec.ErrWaitDelay) {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return out.Bytes(), err
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
		return nil, ctx.Err()
	}
}

// readAutorunFile answers the file, or the code of what went wrong. The network steps go through
// the named remote, so its own settings (proxy, upload-pack, URL rewriting) apply once, as for any
// fetch; origin's URL is read before and after them, and a change in between fails the read
// rather than show one repository's settings under another's name.
func readAutorunFile(ctx context.Context, dir string) (AutorunFile, string) {
	out := AutorunFile{Path: AutorunFilePath}
	fail := func(code string) (AutorunFile, string) {
		if ctx.Err() != nil {
			code = "timeout"
		}
		return out, code
	}
	raw, err := autorunGit(ctx, dir, "remote", "get-url", "origin")
	remote := strings.TrimSpace(string(raw))
	if err != nil || remote == "" {
		return fail("no_origin")
	}
	out.Remote = displayRemote(remote)
	branch, sha, ok := remoteDefaultHead(ctx, dir)
	if !ok {
		return fail("no_default_branch")
	}
	out.Branch, out.Commit = branch, sha
	// A read changes nothing in the clone but the one tracking ref: no pruning of refs or tags the
	// remote's settings may ask for, no configured ref mapping, no FETCH_HEAD.
	if _, err := autorunGit(ctx, dir, "fetch", "--no-tags", "--no-prune", "--no-prune-tags", "--refmap=",
		"--no-write-fetch-head", "--no-recurse-submodules", "origin",
		"+refs/heads/"+branch+":refs/remotes/origin/"+branch); err != nil {
		return fail("fetch_failed")
	}
	if again, err := autorunGit(ctx, dir, "remote", "get-url", "origin"); err != nil || strings.TrimSpace(string(again)) != remote {
		return fail("origin_changed")
	}
	// The commit the remote named, not whatever the tracking ref says now. Missing means the
	// branch moved between the two calls past that commit (a force push).
	if _, err := autorunGit(ctx, dir, "cat-file", "-e", sha+"^{commit}"); err != nil {
		return fail("origin_moved")
	}
	content, found, code := readAutorunBlob(ctx, dir, sha, AutorunFilePath, AutorunFileMaxBytes)
	if code != "" {
		return fail(code)
	}
	out.Found, out.Content = found, content
	return out, ""
}

// readAutorunBlob reads one file of a commit the clone holds. found is false when the commit has
// no entry at p. ls-tree names the entry's mode: a symbolic link would hand back its target's
// path as the content, and a submodule or a directory has none.
func readAutorunBlob(ctx context.Context, dir, sha, p string, max int) (content string, found bool, code string) {
	ls, err := autorunGit(ctx, dir, "ls-tree", "-z", "--full-tree", sha, "--", p)
	if err != nil {
		return "", false, "read_failed"
	}
	if len(ls) == 0 {
		return "", false, ""
	}
	meta, name, _ := strings.Cut(strings.TrimSuffix(string(ls), "\x00"), "\t")
	parts := strings.Fields(meta)
	// -z leaves the name unquoted, so it is compared as it is: ls-tree matches a directory's
	// entry when p names one, which is no file either.
	if name != p || len(parts) != 3 || parts[1] != "blob" || (parts[0] != "100644" && parts[0] != "100755") {
		return "", false, "file_not_regular"
	}
	oid := parts[2]
	sz, err := autorunGit(ctx, dir, "cat-file", "-s", oid)
	if err != nil {
		return "", false, "read_failed"
	}
	if n, err := strconv.Atoi(strings.TrimSpace(string(sz))); err != nil || n > max {
		return "", false, "file_too_large"
	}
	body, err := autorunGit(ctx, dir, "cat-file", "blob", oid)
	if err != nil {
		return "", false, "read_failed"
	}
	if len(body) > max {
		return "", false, "file_too_large"
	}
	// JSON would carry a byte that is not UTF-8 as U+FFFD, and the Control Plane would compare
	// text the commit does not hold.
	if !utf8.Valid(body) {
		return "", false, "file_not_utf8"
	}
	return string(body), true, ""
}

// remoteDefaultHead asks origin which branch its HEAD names and the commit it points at.
func remoteDefaultHead(ctx context.Context, dir string) (branch, sha string, ok bool) {
	b, err := autorunGit(ctx, dir, "ls-remote", "--symref", "origin", "HEAD")
	if err != nil {
		return "", "", false
	}
	for _, ln := range strings.Split(string(b), "\n") {
		if rest, found := strings.CutPrefix(ln, "ref: refs/heads/"); found {
			name, _, _ := strings.Cut(rest, "\t")
			if _, err := autorunGit(ctx, dir, "check-ref-format", "--branch", name); name != "" && err == nil {
				branch = name
			}
		} else if id, ref, found := strings.Cut(ln, "\t"); found && ref == "HEAD" && autorunSHARe.MatchString(id) {
			sha = id
		}
	}
	return branch, sha, branch != "" && sha != ""
}

var autorunSHARe = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

var (
	scpRemoteRe  = regexp.MustCompile(`^(?:[A-Za-z0-9._-]+@)?([A-Za-z0-9.-]+):([A-Za-z0-9._~/-]+)$`)
	safeHostRe   = regexp.MustCompile(`^[A-Za-z0-9.-]+(:[0-9]{1,5})?$`)
	safePathRe   = regexp.MustCompile(`^/[A-Za-z0-9._~/-]*$`)
	remoteHidden = "(remote not shown)"
)

// remoteSecretRules are the credential shapes the Control Plane refuses in the settings file
// (control-plane/internal/autorunfile), which follow the memory scan
// (internal/memoryx/memory_secrets.go): a remote that matches one is not shown.
var remoteSecretRules = []*regexp.Regexp{
	regexp.MustCompile(`\b(?:A3T[A-Z0-9]|AKIA|ABIA|ACCA|ASIA)[0-9A-Z]{16}\b`),
	secretpat.GitHubToken,
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{60,}\b`),
	regexp.MustCompile(`\bglpat-[A-Za-z0-9_\-]{20,}\b`),
	regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9\-]{10,}\b`),
	regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_\-]{24,}`),
	regexp.MustCompile(`\bsk-(?:proj-)?[A-Za-z0-9_\-]{32,}\b`),
	regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{35}\b`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{10,}\.eyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}`),
}

var (
	remoteTokenRe = regexp.MustCompile(`^(gh[pousr]_|github_pat_|glpat-|xox[abprs]-|sk-ant-|sk-proj-|ATBB)[A-Za-z0-9_-]{8,}`)
	remoteRunRe   = regexp.MustCompile(`^[A-Za-z0-9_]{32,}$`)
	digitRe       = regexp.MustCompile(`[0-9]`)
	letterRe      = regexp.MustCompile(`[A-Za-z]`)
)

// remoteTokenLike says whether a path segment looks like a token rather than a name: a known
// token prefix, or, on a network remote, a long run that mixes letters and digits (a name rarely
// does; a local directory, a temporary one say, often does and carries no credential).
func remoteTokenLike(seg string, network bool) bool {
	return remoteTokenRe.MatchString(seg) ||
		(network && remoteRunRe.MatchString(seg) && digitRe.MatchString(seg) && letterRe.MatchString(seg))
}

// displayRemote names a remote by host and path only: what the approval card needs, and nothing
// a URL can carry besides (user, password, query, fragment). A form it cannot take apart safely,
// percent-encoding included (it would show another path than the one fetched), or a path segment
// that looks like a token, is not shown.
func displayRemote(raw string) string {
	d := displayRemoteForm(raw)
	if d == remoteHidden {
		return d
	}
	for _, re := range remoteSecretRules {
		if re.MatchString(d) {
			return remoteHidden
		}
	}
	network := !strings.HasPrefix(d, "/") && !strings.HasPrefix(d, "file://")
	for _, seg := range strings.FieldsFunc(d, func(r rune) bool { return r == '/' || r == ':' }) {
		if remoteTokenLike(seg, network) {
			return remoteHidden
		}
	}
	return d
}

func displayRemoteForm(raw string) string {
	if strings.Contains(raw, "%") {
		return remoteHidden
	}
	if m := scpRemoteRe.FindStringSubmatch(raw); m != nil && !strings.Contains(raw, "://") {
		return m[1] + ":" + m[2]
	}
	if strings.HasPrefix(raw, "/") {
		if safePathRe.MatchString(raw) {
			return raw
		}
		return remoteHidden
	}
	u, err := url.Parse(raw)
	if err != nil {
		return remoteHidden
	}
	switch u.Scheme {
	case "https", "http", "ssh", "git":
		if !safeHostRe.MatchString(u.Host) || (u.Path != "" && !safePathRe.MatchString(u.Path)) {
			return remoteHidden
		}
		return u.Scheme + "://" + u.Host + u.Path
	case "file":
		if safePathRe.MatchString(u.Path) {
			return "file://" + u.Path
		}
	}
	return remoteHidden
}
