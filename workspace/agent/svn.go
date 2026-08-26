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
	"sync"
	"time"

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

// runSvnCtx is runSvn bound to a context — for a local probe whose cost scales with
// the size of the working copy (status), where an unbounded run would hold a handler.
func runSvnCtx(ctx context.Context, args ...string) (string, error) {
	full := append([]string{"--non-interactive"}, args...)
	out, err := exec.CommandContext(ctx, "svn", full...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// svnTrustFailures is the set of certificate problems --trust-server-cert-failures
// waves through when a server's cert is opted into trust. It is the full set (the
// modern superset of the deprecated --trust-server-cert, which only covered
// unknown-ca) so a deliberate "trust this dev server" toggle also survives the
// hostname mismatch that self-signed certs almost always have. Security trade-off:
// it disables cert verification for that server entirely, hence the explicit,
// per-server opt-in (docs/41).
const svnTrustFailures = "--trust-server-cert-failures=unknown-ca,cn-mismatch,expired,not-yet-valid,other"

// svnNetTimeout bounds a SYNCHRONOUS network op (update) so a hung/slow server can't
// occupy the handler indefinitely; generous because updating a large working copy
// legitimately takes long. The first checkout does NOT use it — it runs as a job on
// repoJobTimeout, because killing a long checkout at a handler-shaped deadline is what
// deleted a half-hour-old working copy (docs/78).
const svnNetTimeout = 30 * time.Minute

// svnAuthedArgs builds the full argv (after "svn") for a network op: the
// non-interactive / no-auth-cache flags, an optional cert-trust flag, optional
// --username, then the subcommand args. --password-from-stdin (added here when
// authed) means the password is fed on stdin, never in the argv. Pure, so the flag
// wiring is unit-tested without spawning svn.
func svnAuthedArgs(creds *secrets.SVNCred, args ...string) (full []string, authed bool) {
	full = []string{"--non-interactive", "--no-auth-cache"}
	if creds != nil && creds.TrustCert {
		full = append(full, svnTrustFailures)
	}
	authed = creds != nil && creds.Username != ""
	if authed {
		full = append(full, "--username", creds.Username, "--password-from-stdin")
	}
	return append(full, args...), authed
}

// runSvnAuthed runs a network svn subcommand (checkout/update), injecting basic
// auth via --username + --password-from-stdin when creds are present. --no-auth-cache
// keeps the password out of ~/.subversion/auth (no plaintext on disk). Returns
// trimmed combined output so the handler can surface svn's own message verbatim.
func runSvnAuthed(ctx context.Context, creds *secrets.SVNCred, args ...string) (string, error) {
	return runSvnAuthedSink(ctx, nil, creds, args...)
}

// runSvnAuthedSink is runSvnAuthed with an optional progress sink. With a sink the
// output is STREAMED (line-counted, tail-buffered) instead of accumulated: a first
// checkout of a large repository prints one line per file, and holding all of them in
// memory is both wasteful and invisible — the sink turns the same bytes into a live
// "N files, last: …" and keeps only the tail for the error message.
func runSvnAuthedSink(ctx context.Context, sink *repoJobSink, creds *secrets.SVNCred, args ...string) (string, error) {
	full, authed := svnAuthedArgs(creds, args...)
	cmd := exec.CommandContext(ctx, "svn", full...)
	if authed {
		cmd.Stdin = strings.NewReader(creds.Password + "\n")
	}
	if sink == nil {
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	// svn prints each added path relative to the CWD. Rooting the command at ~/repos
	// makes the progress line read "bigdocs/sub/f1.bin" instead of leaking whatever
	// directory the Agent happened to start in. Every path we pass is absolute, so the
	// CWD changes nothing else.
	if root := reposRoot(); root != "" {
		if _, err := os.Stat(root); err == nil {
			cmd.Dir = root
		}
	}
	cmd.Stdout, cmd.Stderr = sink, sink
	err := cmd.Run()
	return sink.tailString(), err
}

// svnLocked reports whether svn output signals a wedged working-copy lock (an
// interrupted/killed op leaves .svn locked — very common under OOM churn). The fix
// is a local `svn cleanup`; callers auto-heal by running it and retrying once.
//
// ★ E155037 を必ず含めること。中断された checkout/update の後始末が要る状態で svn が実際に
// 出すのは「E155037: Previous operation has not finished; run 'cleanup' if it was interrupted」で、
// **`svn cleanup` ではなく `cleanup`** と書く。E155004 系の文言だけを見ていた頃、これが素通りして
// 自動修復が一度も走らず、利用者は毎回 更新 が失敗するのを手動で ロックを解除 するしかなかった
// （実測: svn 1.14.2 / docs/78）。
func svnLocked(out string) bool {
	s := strings.ToLower(out)
	return strings.Contains(out, "E155004") ||
		strings.Contains(out, "E155037") ||
		strings.Contains(s, "run 'svn cleanup'") ||
		strings.Contains(s, "run 'cleanup'") ||
		strings.Contains(s, "previous operation has not finished") ||
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
	return runSvnAuthedHealingSink(ctx, nil, dir, creds, args...)
}

// runSvnAuthedHealingSink is runSvnAuthedHealing with a progress sink (job path).
func runSvnAuthedHealingSink(ctx context.Context, sink *repoJobSink, dir string, creds *secrets.SVNCred, args ...string) (string, error) {
	out, err := runSvnAuthedSink(ctx, sink, creds, args...)
	// A cancelled/timed-out job also fails "locked" (we killed it mid-write); healing it
	// would restart the very work the user just stopped.
	if err != nil && dir != "" && ctx.Err() == nil && svnLocked(out) {
		if _, cerr := svnCleanup(dir); cerr == nil {
			out, err = runSvnAuthedSink(ctx, sink, creds, args...)
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
// Two `--show-item` runs rather than one parsed `svn info`: the field names in the
// human-readable output are LOCALE-DEPENDENT, and parsing them would break on a
// workspace whose LC_MESSAGES is not English. Measured, the second process costs ~5ms
// on a 20k-file working copy — not a trade worth making.
func svnInfo(dir string) (revision, url string) {
	return svnInfoItem(dir, "revision"), svnInfoItem(dir, "url")
}

// svnDirtyTimeout bounds the list view's `svn status`. Unlike info (one wc.db read),
// status walks the WHOLE working copy and stats every file — on a big checkout over
// network storage that is seconds to minutes, and GET /repos would block on it for
// every row. Measured locally: 20k files ≈ 0.24s; an 11.4 GB documents checkout is an
// order of magnitude more files, on EFS, where every stat is a network round trip.
const svnDirtyTimeout = 20 * time.Second

// svnDirtyScan は「同じ作業コピーの走査は 1 本だけ」にするための相乗り。一覧は 60 秒の
// ポーリングに加えて画面の操作でも refresh されるので、素直に書くと大きな作業コピーに対して
// 全走査が重なる（走行中の checkout と wc.db を奪い合ったのと同じ形を、今度は自分で作る）。
// TTL キャッシュにしないのは、手動の 更新 を押した直後に古い判定を返さないため。
type svnDirtyScan struct {
	done  chan struct{}
	dirty bool
}

var svnDirtyState = struct {
	mu      sync.Mutex
	running map[string]*svnDirtyScan
	last    map[string]bool // 直近の「実際に測れた」答え
}{running: map[string]*svnDirtyScan{}, last: map[string]bool{}}

// svnDirty reports whether the working copy has local modifications. `svn status`
// is local; any non-empty output (modified/added/deleted/unversioned) counts, so a
// clean copy shows no dirty dot (mirrors git's Dirty incl. untracked).
//
// Bounded by svnDirtyTimeout, and concurrent callers for the same working copy share
// one scan. On timeout the LAST KNOWN answer is reused rather than reporting "clean":
// a working copy too big to scan in 20 seconds is not a clean one, and a dot that
// flickers off on a slow filesystem is worse than a slightly stale one.
func svnDirty(dir string) bool {
	svnDirtyState.mu.Lock()
	if scan, ok := svnDirtyState.running[dir]; ok {
		svnDirtyState.mu.Unlock()
		<-scan.done
		return scan.dirty
	}
	scan := &svnDirtyScan{done: make(chan struct{})}
	svnDirtyState.running[dir] = scan
	prev, seen := svnDirtyState.last[dir]
	svnDirtyState.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), svnDirtyTimeout)
	defer cancel()
	out, err := runSvnCtx(ctx, "status", dir)
	dirty := err == nil && out != ""
	measured := err == nil
	if err != nil && ctx.Err() != nil && seen {
		dirty = prev // scan outran the budget — keep the last real answer
	}

	svnDirtyState.mu.Lock()
	if measured {
		svnDirtyState.last[dir] = dirty
	}
	delete(svnDirtyState.running, dir)
	svnDirtyState.mu.Unlock()
	scan.dirty = dirty
	close(scan.done)
	return dirty
}

// svnRepoEntry builds the list-view representation of an SVN working copy for
// handleListRepos. Git-only fields (branch/ahead/behind/worktree) stay zero.
func svnRepoEntry(name, dir string) Repo {
	rev, url := svnInfo(dir)
	return Repo{
		Name:          name,
		WorkingCopyID: workingCopyID(dir),
		Path:          dir,
		Vcs:           "svn",
		Revision:      rev,
		URL:           url,
		Dirty:         svnDirty(dir),
	}
}

// pickSvnCred returns the credential whose URLPrefix is the LONGEST prefix of url
// (so a per-repo entry beats a broader per-server one), or nil. Pure — the store
// lookup wraps it.
func pickSvnCred(list []secrets.SVNCred, url string) *secrets.SVNCred {
	var best *secrets.SVNCred
	for i := range list {
		c := &list[i]
		if svnPrefixMatch(url, c.URLPrefix) && (best == nil || len(c.URLPrefix) > len(best.URLPrefix)) {
			best = c
		}
	}
	if best == nil {
		return nil
	}
	cp := *best
	return &cp
}

// svnPrefixMatch reports whether url falls under prefix at a URL segment
// boundary. A raw HasPrefix would also match a DIFFERENT host that merely starts
// with the same bytes ("https://svn.corp.com" vs
// "https://svn.corp.com.evil.example/…") — and the stored password would be sent
// there as Basic auth.
func svnPrefixMatch(url, prefix string) bool {
	if prefix == "" || !strings.HasPrefix(url, prefix) {
		return false
	}
	return len(url) == len(prefix) || strings.HasSuffix(prefix, "/") || url[len(prefix)] == '/'
}

// svnCredsFor returns the stored credential matching url (longest prefix), or nil.
func svnCredsFor(url string) *secrets.SVNCred {
	s, err := secrets.Load()
	if err != nil {
		return nil
	}
	return pickSvnCred(s.SVN, url)
}

// svnSaveCred upserts an SVN store entry keyed by URL prefix. A non-empty user sets
// the basic-auth credential (the "save credentials" opt-in of the checkout modal);
// an empty user leaves any existing username/password untouched so a trust-only
// entry can be written without a password. trust is OR-ed in (never cleared here) so
// `svn update` of a self-signed server keeps trusting its cert even when the
// password was not saved.
func svnSaveCred(prefix, user, pass string, trust bool) error {
	s, err := secrets.Load()
	if err != nil {
		return err
	}
	for i := range s.SVN {
		if s.SVN[i].URLPrefix == prefix {
			if user != "" {
				s.SVN[i].Username, s.SVN[i].Password = user, pass
			}
			s.SVN[i].TrustCert = s.SVN[i].TrustCert || trust
			return s.Save()
		}
	}
	s.SVN = append(s.SVN, secrets.SVNCred{URLPrefix: prefix, Username: user, Password: pass, TrustCert: trust})
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
		e := map[string]string{"urlPrefix": c.URLPrefix, "username": c.Username}
		if c.TrustCert {
			e["trustCert"] = "1"
		}
		list = append(list, e)
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
	URL       string `json:"url"`
	Subpath   string `json:"subpath"`
	Name      string `json:"name"`
	Username  string `json:"username"`
	Password  string `json:"password"`
	Save      bool   `json:"save"`
	TrustCert bool   `json:"trustCert"` // accept a self-signed / untrusted server cert (docs/41)
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

// handleSvnCheckout (POST /repos/svn) starts an SVN checkout into ~/repos/<name>.
// Mirrors handleCloneRepo: derive/validate a folder name, refuse an existing dir, then
// hand the network work to a background job and answer 202 with it (docs/78) — a first
// checkout routinely outlives every proxy in the path, and the synchronous shape made
// the Console call a still-running checkout "done". Credentials are used once and, when
// Save is set, upserted into the encrypted store for later updates.
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
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "name must start with a letter or number, may contain letters/numbers plus . _ @ -, and be at most 96 characters")
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
	// Cert trust is a per-server property (not the credential): overlay the request
	// flag onto whatever creds we resolved, allocating a trust-only entry when there
	// is no auth at all (a public self-signed repo).
	if req.TrustCert {
		if creds == nil {
			creds = &secrets.SVNCred{URLPrefix: base}
		}
		creds.TrustCert = true
	}
	if repoJobActive(name) {
		httpx.WriteErr(w, http.StatusConflict, "job_running", "an import is already running for: "+name)
		return
	}
	saveCred := func() {
		// Persist only after a successful checkout proves it works (base URL as the
		// prefix, so a later `svn update` of any subtree matches it). The password is
		// saved only on the explicit Save opt-in; cert trust — not a secret — persists
		// whenever it was used, so updates keep trusting the server's cert.
		if creds == nil {
			return
		}
		saveUser, savePass := "", ""
		if req.Save && creds.Username != "" {
			saveUser, savePass = creds.Username, creds.Password
		}
		if saveUser != "" || creds.TrustCert {
			_ = svnSaveCred(base, saveUser, savePass, creds.TrustCert)
		}
	}
	job := startRepoJob("svn", name, dir, full, func(ctx context.Context, sink *repoJobSink) error {
		out, err := runSvnAuthedHealingSink(ctx, sink, dir, creds, "checkout", full, dir)
		if err != nil {
			// Keep a working copy that svn can resume (cleanup + update picks up where
			// it stopped); only a checkout that never produced one is swept away. The
			// old unconditional RemoveAll is exactly how a half-hour download vanished.
			if !isSvnRepo(dir) {
				_ = os.RemoveAll(dir)
			}
			return fmt.Errorf("%v: %s", err, out)
		}
		saveCred()
		return nil
	})
	httpx.WriteJSON(w, http.StatusAccepted, map[string]any{"job": job})
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
	ctx, cancel := context.WithTimeout(context.Background(), svnNetTimeout)
	defer cancel()
	out, err := runSvnAuthedHealing(ctx, dir, creds, "update", dir)
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
