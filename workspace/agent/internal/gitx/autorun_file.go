package gitx

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// AutorunFilePath is where a repository keeps its memo queue settings (#4881).
const AutorunFilePath = ".agent-fleet/autorun.yaml"

// AutorunFileMaxBytes bounds the file. The Control Plane, which parses it, holds the same bound.
const AutorunFileMaxBytes = 64 << 10

// autorunFetchWait bounds the two network calls; an unreachable remote must not hold the
// Console's request open.
const autorunFetchWait = 60 * time.Second

// AutorunFile is GET /repos/{name}/autorun-file: the settings file as committed on origin's
// default branch. Found is false when that commit has no such file. Remote is origin's URL with
// any credentials removed, so the person approving the settings sees where they came from.
type AutorunFile struct {
	Path    string `json:"path"`
	Remote  string `json:"remote"`
	Branch  string `json:"branch"`
	Commit  string `json:"commit"`
	Found   bool   `json:"found"`
	Content string `json:"content"`
}

// autorunNoReplace keeps refs/replace out of every read: a run can write replace refs into the
// clone, and with them make the right commit id show other content.
var autorunNoReplace = []string{"GIT_NO_REPLACE_OBJECTS=1"}

// HandleAutorunFile reads the settings file from origin's default branch only, never from the
// working tree or a working branch: a run in this workspace can write both, and must not be
// able to change the settings it runs under. Branch and commit are what origin answers, not
// refs/remotes/origin/*, which anything in the clone can repoint; the fetch only brings the
// objects. This is a guard rail, not the boundary: a run holds the same git credentials and the
// same clone configuration, and the member's approval of each difference is what applies
// anything. The Control Plane parses and validates the content; this only reads it.
//
// Error messages are fixed: git's own output may name hosts or carry a URL, and goes to the log.
func HandleAutorunFile(w http.ResponseWriter, r *http.Request) {
	dir, ok := RepoDirFromPath(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), autorunFetchWait)
	defer cancel()
	f, code, err := readAutorunFile(ctx, dir)
	if err != nil {
		log.Printf("autorun file: %s: %s: %v", r.PathValue("name"), code, err)
		status := http.StatusBadGateway
		if code == "file_too_large" || code == "file_not_regular" {
			status = http.StatusUnprocessableEntity
		}
		httpx.WriteErr(w, status, code, autorunErrText[code])
		return
	}
	httpx.WriteJSON(w, http.StatusOK, f)
}

var autorunErrText = map[string]string{
	"no_default_branch": "origin did not name its default branch",
	"fetch_failed":      "fetching origin's default branch failed",
	"origin_moved":      "origin's default branch moved while it was read; try again",
	"read_failed":       "the settings file could not be read from the fetched commit",
	"file_not_regular":  AutorunFilePath + " on the default branch is not a regular file",
	"file_too_large":    AutorunFilePath + " is larger than " + strconv.Itoa(AutorunFileMaxBytes) + " bytes",
}

func readAutorunFile(ctx context.Context, dir string) (AutorunFile, string, error) {
	out := AutorunFile{Path: AutorunFilePath}
	if u, err := Run(dir, "remote", "get-url", "origin"); err == nil {
		out.Remote = redactRemote(u)
	}
	branch, sha, err := remoteDefaultHead(ctx, dir)
	if err != nil {
		return out, "no_default_branch", err
	}
	out.Branch, out.Commit = branch, sha
	fetch := CmdContext(ctx, dir, "fetch", "--no-tags", "--no-recurse-submodules", "origin",
		"+refs/heads/"+branch+":refs/remotes/origin/"+branch)
	if b, err := fetch.CombinedOutput(); err != nil {
		return out, "fetch_failed", fmt.Errorf("%v: %s", err, strings.TrimSpace(string(b)))
	}
	// The commit origin named, not whatever the tracking ref says now. Missing means the branch
	// moved between the two calls past that commit (a force push).
	if _, err := runEnv(dir, autorunNoReplace, "cat-file", "-e", sha+"^{commit}"); err != nil {
		return out, "origin_moved", err
	}
	// ls-tree names the entry's mode: a symbolic link would hand back its target's path as the
	// content, and a submodule or a directory has none.
	ls, err := runEnv(dir, autorunNoReplace, "ls-tree", "--full-tree", sha, "--", AutorunFilePath)
	if err != nil {
		return out, "read_failed", err
	}
	if ls == "" {
		return out, "", nil
	}
	meta, _, _ := strings.Cut(ls, "\t")
	parts := strings.Fields(meta)
	if len(parts) != 3 || parts[1] != "blob" || (parts[0] != "100644" && parts[0] != "100755") {
		return out, "file_not_regular", fmt.Errorf("mode %q", meta)
	}
	oid := parts[2]
	sz, err := runEnv(dir, autorunNoReplace, "cat-file", "-s", oid)
	if err != nil {
		return out, "read_failed", err
	}
	if n, err := strconv.Atoi(sz); err != nil || n > AutorunFileMaxBytes {
		return out, "file_too_large", fmt.Errorf("size %s", sz)
	}
	cmd := Cmd(dir, "cat-file", "blob", oid)
	cmd.Env = append(cmd.Env, autorunNoReplace...)
	body, err := cmd.Output()
	if err != nil {
		return out, "read_failed", err
	}
	if len(body) > AutorunFileMaxBytes {
		return out, "file_too_large", fmt.Errorf("read %d bytes", len(body))
	}
	out.Found, out.Content = true, string(body)
	return out, "", nil
}

// remoteDefaultHead asks origin which branch its HEAD names and the commit it points at.
func remoteDefaultHead(ctx context.Context, dir string) (branch, sha string, err error) {
	b, err := CmdContext(ctx, dir, "ls-remote", "--symref", "origin", "HEAD").Output()
	if err != nil {
		return "", "", fmt.Errorf("ls-remote: %v", err)
	}
	for _, ln := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(ln, "ref: refs/heads/"); ok {
			name, _, _ := strings.Cut(rest, "\t")
			if name != "" && Cmd(dir, "check-ref-format", "--branch", name).Run() == nil {
				branch = name
			}
		} else if id, ref, ok := strings.Cut(ln, "\t"); ok && ref == "HEAD" && autorunSHARe.MatchString(id) {
			sha = id
		}
	}
	if branch == "" || sha == "" {
		return "", "", fmt.Errorf("origin named no default branch and commit")
	}
	return branch, sha, nil
}

var autorunSHARe = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// redactRemote drops the user and password of a URL remote. An scp-like remote (user@host:path)
// carries no secret and is kept.
func redactRemote(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return raw
	}
	u.User = nil
	return u.String()
}
