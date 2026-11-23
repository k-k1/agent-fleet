package gitx

import (
	"context"
	"fmt"
	"net/http"
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
// default branch. Found is false when that commit has no such file.
type AutorunFile struct {
	Path    string `json:"path"`
	Branch  string `json:"branch"`
	Commit  string `json:"commit"`
	Found   bool   `json:"found"`
	Content string `json:"content"`
}

// HandleAutorunFile reads the settings file from origin's default branch only, never from the
// working tree or a working branch: a run in this workspace can write both, and must not be
// able to change the settings it runs under. The default branch is asked of the remote, not
// read from refs/remotes/origin/HEAD, which anything in the clone can repoint. The Control
// Plane parses and validates the content; this only reads it.
func HandleAutorunFile(w http.ResponseWriter, r *http.Request) {
	dir, ok := RepoDirFromPath(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), autorunFetchWait)
	defer cancel()
	f, code, err := readAutorunFile(ctx, dir)
	if err != nil {
		status := http.StatusBadGateway
		if code == "file_too_large" || code == "file_not_regular" {
			status = http.StatusUnprocessableEntity
		}
		httpx.WriteErr(w, status, code, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, f)
}

func readAutorunFile(ctx context.Context, dir string) (AutorunFile, string, error) {
	out := AutorunFile{Path: AutorunFilePath}
	branch, err := remoteDefaultBranch(ctx, dir)
	if err != nil {
		return out, "no_default_branch", err
	}
	out.Branch = branch
	ref := "refs/remotes/origin/" + branch
	fetch := CmdContext(ctx, dir, "fetch", "--no-tags", "--no-recurse-submodules", "origin", "+refs/heads/"+branch+":"+ref)
	if b, err := fetch.CombinedOutput(); err != nil {
		return out, "fetch_failed", fmt.Errorf("fetch origin %s: %v: %s", branch, err, strings.TrimSpace(string(b)))
	}
	sha, err := Run(dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil || sha == "" {
		return out, "fetch_failed", fmt.Errorf("origin/%s has no commit after the fetch", branch)
	}
	out.Commit = sha
	// ls-tree names the entry's mode: a symbolic link would hand back its target's path as the
	// content, and a submodule or a directory has none.
	ls, err := Run(dir, "ls-tree", "--full-tree", sha, "--", AutorunFilePath)
	if err != nil {
		return out, "read_failed", fmt.Errorf("list %s: %v", AutorunFilePath, err)
	}
	if ls == "" {
		return out, "", nil
	}
	meta, _, _ := strings.Cut(ls, "\t")
	parts := strings.Fields(meta)
	if len(parts) != 3 || parts[1] != "blob" || (parts[0] != "100644" && parts[0] != "100755") {
		return out, "file_not_regular", fmt.Errorf("%s on %s is not a regular file", AutorunFilePath, branch)
	}
	oid := parts[2]
	sz, err := Run(dir, "cat-file", "-s", oid)
	if err != nil {
		return out, "read_failed", fmt.Errorf("size of %s: %v", AutorunFilePath, err)
	}
	if n, err := strconv.Atoi(sz); err != nil || n > AutorunFileMaxBytes {
		return out, "file_too_large", fmt.Errorf("%s is larger than %d bytes", AutorunFilePath, AutorunFileMaxBytes)
	}
	body, err := Cmd(dir, "cat-file", "blob", oid).Output()
	if err != nil {
		return out, "read_failed", fmt.Errorf("read %s: %v", AutorunFilePath, err)
	}
	out.Found, out.Content = true, string(body)
	return out, "", nil
}

// remoteDefaultBranch asks origin which branch its HEAD names.
func remoteDefaultBranch(ctx context.Context, dir string) (string, error) {
	b, err := CmdContext(ctx, dir, "ls-remote", "--symref", "origin", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("ask origin for its default branch: %v", err)
	}
	for _, ln := range strings.Split(string(b), "\n") {
		rest, ok := strings.CutPrefix(ln, "ref: refs/heads/")
		if !ok {
			continue
		}
		name, _, _ := strings.Cut(rest, "\t")
		if name != "" && Cmd(dir, "check-ref-format", "--branch", name).Run() == nil {
			return name, nil
		}
	}
	return "", fmt.Errorf("origin does not name a default branch")
}
