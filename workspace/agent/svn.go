package main

// Subversion (SVN) checkout support (docs/41). SVN working copies live under
// ~/repos/<name> alongside git working copies — the folder name IS the id, same
// flat model as git (git.go §22). There is no provider abstraction: an SVN
// checkout is just a URL + optional basic auth. SVN addresses subtrees by URL, so
// "check out a specific path" is simply part of the URL, and "check out different
// paths several times" is several folders — the same isolation git gets from
// separate clones. SVN has no worktree analog, so sessions launch in place.
//
// Credentials: SVN does not use git's credential-helper protocol, so we cannot
// reuse `workspace-agent cred`. Instead the REST checkout/update paths look creds
// up in the encrypted store (secrets.SVNCred, longest URL-prefix wins) and pass
// them to `svn` as --username / --password-from-stdin (svn ≥1.10; the image ships
// 1.14) so the password never lands in the process list. We pass --no-auth-cache
// so no plaintext is written under ~/.subversion/auth — the trade-off is that an
// `svn` command an agent runs itself in a terminal is NOT transparently
// authenticated (documented limitation).

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// isSvnRepo reports whether dir is an SVN working copy (has a .svn dir). The
// counterpart to isGitRepo; the repo list uses both to classify a folder.
func isSvnRepo(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, ".svn"))
	return err == nil && fi.IsDir()
}

// svnAvailable reports whether the `svn` binary is on PATH. It is baked into the
// Workspace image, but the native (WSL) runtime relies on host tools, so a clear
// error beats a cryptic exec failure there.
func svnAvailable() bool {
	_, err := exec.LookPath("svn")
	return err == nil
}

// runSvn runs a LOCAL svn subcommand (info/status/cleanup) that needs no network
// or auth, returning trimmed combined output. --non-interactive keeps it from ever
// blocking on a prompt.
func runSvn(dir string, args ...string) (string, error) {
	full := append([]string{"--non-interactive"}, args...)
	out, err := exec.Command("svn", full...).CombinedOutput()
	_ = dir // args carry an explicit path; cwd is irrelevant
	return strings.TrimSpace(string(out)), err
}

// runSvnAuthed runs a network svn subcommand (checkout/update), injecting basic
// auth via --username + --password-from-stdin when creds are present. --no-auth-cache
// keeps the password out of ~/.subversion/auth (no plaintext on disk). Returns
// trimmed combined output so the handler can surface svn's own message verbatim.
func runSvnAuthed(ctx context.Context, creds *secrets.SVNCred, args ...string) (string, error) {
	full := []string{"--non-interactive", "--no-auth-cache"}
	authed := creds != nil && creds.Username != ""
	if authed {
		full = append(full, "--username", creds.Username, "--password-from-stdin")
	}
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, "svn", full...)
	if authed {
		cmd.Stdin = strings.NewReader(creds.Password + "\n")
	}
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// svnLocked reports whether svn output signals a wedged working-copy lock (an
// interrupted/killed op leaves .svn locked — very common under OOM churn). The fix
// is a local `svn cleanup`; callers auto-heal by running it and retrying once.
func svnLocked(out string) bool {
	s := strings.ToLower(out)
	return strings.Contains(out, "E155004") ||
		strings.Contains(s, "run 'svn cleanup'") ||
		strings.Contains(s, "working copy locked") ||
		strings.Contains(s, "is already locked")
}

// svnCleanup runs `svn cleanup <dir>` — purely local, no auth needed, so it always
// works even when no credential is stored.
func svnCleanup(dir string) (string, error) {
	return runSvn(dir, "cleanup", dir)
}

// runSvnAuthedHealing runs a network op and, if it fails on a working-copy lock,
// runs `svn cleanup <dir>` once and retries — so a killed checkout/update self-heals
// without the user (or the agent) having to notice.
func runSvnAuthedHealing(ctx context.Context, dir string, creds *secrets.SVNCred, args ...string) (string, error) {
	out, err := runSvnAuthed(ctx, creds, args...)
	if err != nil && dir != "" && svnLocked(out) {
		if _, cerr := svnCleanup(dir); cerr == nil {
			out, err = runSvnAuthed(ctx, creds, args...)
		}
	}
	return out, err
}

// svnInfoItem returns a single `svn info --show-item <item>` value for a working
// copy (local, no network/auth). "" on failure.
func svnInfoItem(dir, item string) string {
	out, err := runSvn(dir, "info", "--show-item", item, dir)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// svnInfo returns a working copy's current revision and repository URL (both local).
func svnInfo(dir string) (revision, url string) {
	return svnInfoItem(dir, "revision"), svnInfoItem(dir, "url")
}

// svnDirty reports whether the working copy has local modifications. `svn status`
// is local; any non-empty output (modified/added/deleted/unversioned) counts, so a
// clean copy shows no dirty dot (mirrors git's Dirty incl. untracked).
func svnDirty(dir string) bool {
	out, err := runSvn(dir, "status", dir)
	return err == nil && out != ""
}

// svnRepoEntry builds the list-view representation of an SVN working copy for
// handleListRepos. Git-only fields (branch/ahead/behind/worktree) stay zero.
func svnRepoEntry(name, dir string) Repo {
	rev, url := svnInfo(dir)
	return Repo{
		Name:     name,
		Path:     dir,
		Vcs:      "svn",
		Revision: rev,
		URL:      url,
		Dirty:    svnDirty(dir),
	}
}

// pickSvnCred returns the credential whose URLPrefix is the LONGEST prefix of url
// (so a per-repo entry beats a broader per-server one), or nil. Pure — the store
// lookup wraps it.
func pickSvnCred(list []secrets.SVNCred, url string) *secrets.SVNCred {
	var best *secrets.SVNCred
	for i := range list {
		c := &list[i]
		if strings.HasPrefix(url, c.URLPrefix) && (best == nil || len(c.URLPrefix) > len(best.URLPrefix)) {
			best = c
		}
	}
	if best == nil {
		return nil
	}
	cp := *best
	return &cp
}

// svnCredsFor returns the stored credential matching url (longest prefix), or nil.
func svnCredsFor(url string) *secrets.SVNCred {
	s, err := secrets.Load()
	if err != nil {
		return nil
	}
	return pickSvnCred(s.SVN, url)
}

// svnSaveCred upserts a basic-auth credential keyed by URL prefix into the
// encrypted store (the "save credentials" opt-in of the checkout modal).
func svnSaveCred(prefix, user, pass string) error {
	s, err := secrets.Load()
	if err != nil {
		return err
	}
	for i := range s.SVN {
		if s.SVN[i].URLPrefix == prefix {
			s.SVN[i].Username, s.SVN[i].Password = user, pass
			return s.Save()
		}
	}
	s.SVN = append(s.SVN, secrets.SVNCred{URLPrefix: prefix, Username: user, Password: pass})
	return s.Save()
}

// svnForgetCred removes a stored SVN credential by exact URL prefix.
func svnForgetCred(prefix string) error {
	s, err := secrets.Load()
	if err != nil {
		return err
	}
	out := s.SVN[:0]
	for _, c := range s.SVN {
		if c.URLPrefix != prefix {
			out = append(out, c)
		}
	}
	s.SVN = out
	return s.Save()
}

// svnConnStatus reports the saved SVN servers (URL prefix + username — never the
// password) so the Console can list/forget them. Folded into GET /connections.
func svnConnStatus(s *secrets.Data) []map[string]string {
	list := []map[string]string{}
	for _, c := range s.SVN {
		list = append(list, map[string]string{"urlPrefix": c.URLPrefix, "username": c.Username})
	}
	return list
}

// deriveSvnName turns an SVN URL into a folder name: the last path segment, but a
// bare "trunk" (the common layout tip) falls back to its parent so the folder is
// named after the repository, not "trunk". The Console still de-dupes / lets the
// user override.
func deriveSvnName(url string) string {
	segs := []string{}
	for _, s := range strings.Split(strings.Trim(url, "/"), "/") {
		if s = strings.TrimSpace(s); s != "" {
			segs = append(segs, s)
		}
	}
	if len(segs) == 0 {
		return ""
	}
	last := segs[len(segs)-1]
	if last == "trunk" && len(segs) >= 2 {
		return segs[len(segs)-2]
	}
	return last
}

type svnCheckoutReq struct {
	URL      string `json:"url"`
	Subpath  string `json:"subpath"`
	Name     string `json:"name"`
	Username string `json:"username"`
	Password string `json:"password"`
	Save     bool   `json:"save"`
}

// svnBuildURL joins a base repo URL with an optional subpath (SVN addresses
// subtrees by URL). Both are trimmed of surrounding slashes/space.
func svnBuildURL(base, subpath string) string {
	u := strings.TrimRight(strings.TrimSpace(base), "/")
	if sp := strings.Trim(strings.TrimSpace(subpath), "/"); sp != "" {
		u += "/" + sp
	}
	return u
}

// handleSvnCheckout (POST /repos/svn) checks out an SVN URL into ~/repos/<name>.
// Mirrors handleCloneRepo: derive/validate a folder name, refuse an existing dir,
// run the checkout, remove a half-written dir on failure. Credentials are used
// once and, when Save is set, upserted into the encrypted store for later updates.
func handleSvnCheckout(w http.ResponseWriter, r *http.Request) {
	if !svnAvailable() {
		httpx.WriteErr(w, http.StatusNotImplemented, "svn_missing", "the 'svn' command is not available in this workspace")
		return
	}
	var req svnCheckoutReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	base := strings.TrimSpace(req.URL)
	if base == "" || strings.HasPrefix(base, "-") || !strings.Contains(base, "://") {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_url", "url is required and must be an absolute http(s):// URL")
		return
	}
	full := svnBuildURL(base, req.Subpath)
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = deriveSvnName(full)
	}
	dir, ok := resolveRepoDir(name)
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "name must match [A-Za-z0-9][A-Za-z0-9._-]{0,95}")
		return
	}
	if _, err := os.Stat(dir); err == nil {
		httpx.WriteErr(w, http.StatusConflict, "exists", "repo already exists: "+name)
		return
	}
	if err := os.MkdirAll(reposRoot(), 0o755); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "mkdir_failed", err.Error())
		return
	}
	// Credential precedence: explicit request creds, else a stored one matching the URL.
	var creds *secrets.SVNCred
	if strings.TrimSpace(req.Username) != "" || strings.TrimSpace(req.Password) != "" {
		creds = &secrets.SVNCred{URLPrefix: base, Username: strings.TrimSpace(req.Username), Password: req.Password}
	} else {
		creds = svnCredsFor(full)
	}
	out, err := runSvnAuthedHealing(context.Background(), dir, creds, "checkout", full, dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		httpx.WriteErr(w, http.StatusBadGateway, "checkout_failed", fmt.Sprintf("%v: %s", err, out))
		return
	}
	// Save only after a successful checkout proves the creds work (base URL as the
	// prefix, so a later `svn update` of any subtree matches it).
	if req.Save && creds != nil && creds.Username != "" {
		_ = svnSaveCred(base, creds.Username, creds.Password)
	}
	httpx.WriteJSON(w, http.StatusCreated, svnRepoEntry(name, dir))
}

// svnDirFromPath validates {name} and ensures it is an SVN working copy.
func svnDirFromPath(w http.ResponseWriter, r *http.Request) (string, bool) {
	dir, ok := resolveRepoDir(r.PathValue("name"))
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid repo name")
		return "", false
	}
	if !isSvnRepo(dir) {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such svn working copy: "+r.PathValue("name"))
		return "", false
	}
	return dir, true
}

// handleSvnUpdate (POST /repos/{name}/svn-update) runs `svn update`, self-healing a
// wedged lock. Creds come from the store (matched to the working copy's URL).
func handleSvnUpdate(w http.ResponseWriter, r *http.Request) {
	dir, ok := svnDirFromPath(w, r)
	if !ok {
		return
	}
	if running := liveSessionsInDir(dir); len(running) > 0 {
		httpx.WriteErr(w, http.StatusConflict, errCodeSessionsRunning,
			fmt.Sprintf("%d session(s) are running in this working copy (%s); finish or stop them before updating.",
				len(running), strings.Join(running, ", ")))
		return
	}
	_, url := svnInfo(dir)
	creds := svnCredsFor(url)
	out, err := runSvnAuthedHealing(context.Background(), dir, creds, "update", dir)
	if err != nil {
		httpx.WriteErr(w, http.StatusBadGateway, "update_failed", fmt.Sprintf("%v: %s", err, out))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, svnRepoEntry(r.PathValue("name"), dir))
}

// handleSvnCleanup (POST /repos/{name}/svn-cleanup) runs `svn cleanup` to clear a
// wedged working-copy lock. Local only — no auth, always available.
func handleSvnCleanup(w http.ResponseWriter, r *http.Request) {
	dir, ok := svnDirFromPath(w, r)
	if !ok {
		return
	}
	if out, err := svnCleanup(dir); err != nil {
		httpx.WriteErr(w, http.StatusBadGateway, "cleanup_failed", fmt.Sprintf("%v: %s", err, out))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, svnRepoEntry(r.PathValue("name"), dir))
}

// handleDeleteSvnConn (DELETE /connections/svn?prefix=...) forgets a saved SVN
// credential.
func handleDeleteSvnConn(w http.ResponseWriter, r *http.Request) {
	prefix := strings.TrimSpace(r.URL.Query().Get("prefix"))
	if prefix == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_prefix", "prefix is required")
		return
	}
	if err := svnForgetCred(prefix); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"forgot": prefix})
}
