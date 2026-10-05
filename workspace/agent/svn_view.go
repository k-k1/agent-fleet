package main

// Read-only SVN history and local-changes views (issue #1705, docs/log/41 amendment).
//
// The git SCM endpoints (gitx/git_view.go) shell out to `git` and validate a hex sha, so an SVN
// working copy had no equivalent. These four routes give the Console the same panes:
//
//	GET /repos/{name}/svn-log      svn log --xml -v      NETWORK  (stored credential injected)
//	GET /repos/{name}/svn-show     svn diff -c REV       NETWORK
//	GET /repos/{name}/svn-changes  svn status --xml      LOCAL    (never touches the server)
//	GET /repos/{name}/svn-diff     svn diff              LOCAL
//
// The split matters for the Console's refresh: only the local pair may ride an auto-refresh
// tick. `svn log` is a round trip to the server — slow, and it can fail on auth or reachability.
//
// All of it goes through svnCmd (straight to the real binary, never the PATH shim) and parses
// svn's `--xml` output with encoding/xml: the human-readable form is locale-dependent.

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

const (
	// svnViewTimeout bounds one view request. svnNetTimeout (30 min) is sized for an update of
	// a huge working copy; a log page or one revision's diff that takes longer than this is a
	// hung server, and the pane is waiting on it.
	svnViewTimeout = 2 * time.Minute
	// svnViewMaxBytes caps a diff the way the git view does (gitx maxViewBytes).
	svnViewMaxBytes = 2 << 20
	// svnLogDefaultLimit / svnLogMaxLimit bound one log page.
	svnLogDefaultLimit = 50
	svnLogMaxLimit     = 500
)

// ---- svn log --xml -v ------------------------------------------------------------------

// svnLogPath is one changed path of a revision.
type svnLogPath struct {
	Action       string `json:"action"` // A / M / D / R
	Path         string `json:"path"`   // repository-root relative, e.g. /trunk/a.txt
	Kind         string `json:"kind,omitempty"`
	CopyFromPath string `json:"copyFromPath,omitempty"`
	CopyFromRev  string `json:"copyFromRev,omitempty"`
}

// svnLogEntry is one revision of `svn log`.
type svnLogEntry struct {
	Rev     int          `json:"rev"`
	Author  string       `json:"author"`
	Date    string       `json:"date"`
	Message string       `json:"message"`
	Paths   []svnLogPath `json:"paths"`
}

// svnLogResp is the body of GET /repos/{name}/svn-log. The Console's SvnLogPage mirrors it.
type svnLogResp struct {
	Revisions  []svnLogEntry `json:"revisions"`
	HasMore    bool          `json:"hasMore"`
	WCRevision string        `json:"wcRevision"` // the working copy's own revision, "" when unknown
}

// svnShowResp is the body of GET /repos/{name}/svn-show: the CommitData shape the Console's
// commit pane already renders for git (subject/body/author/date/short/diff/truncated), plus the
// changed paths git's diff does not need to list.
type svnShowResp struct {
	Rev       int          `json:"rev"`
	Short     string       `json:"short"`
	Author    string       `json:"author"`
	Date      string       `json:"date"`
	Subject   string       `json:"subject"`
	Body      string       `json:"body"`
	Paths     []svnLogPath `json:"paths"`
	Diff      string       `json:"diff"`
	Truncated bool         `json:"truncated"`
}

// svnChangesResp is the body of GET /repos/{name}/svn-changes.
type svnChangesResp struct {
	Changes []svnChange `json:"changes"`
}

// svnDiffResp is the body of GET /repos/{name}/svn-diff (the git /diff shape).
type svnDiffResp struct {
	Diff      string `json:"diff"`
	Truncated bool   `json:"truncated"`
}

type svnLogXML struct {
	Entries []struct {
		Revision int    `xml:"revision,attr"`
		Author   string `xml:"author"`
		Date     string `xml:"date"`
		Msg      string `xml:"msg"`
		Paths    []struct {
			Action       string `xml:"action,attr"`
			Kind         string `xml:"kind,attr"`
			CopyFromPath string `xml:"copyfrom-path,attr"`
			CopyFromRev  string `xml:"copyfrom-rev,attr"`
			Path         string `xml:",chardata"`
		} `xml:"paths>path"`
	} `xml:"logentry"`
}

// parseSvnLogXML decodes `svn log --xml [-v]`. Author is absent on an anonymous commit and
// msg on an empty one; both simply come back empty.
func parseSvnLogXML(data []byte) ([]svnLogEntry, error) {
	var x svnLogXML
	if err := xml.Unmarshal(data, &x); err != nil {
		return nil, fmt.Errorf("parse svn log: %w", err)
	}
	out := make([]svnLogEntry, 0, len(x.Entries))
	for _, e := range x.Entries {
		le := svnLogEntry{Rev: e.Revision, Author: e.Author, Date: e.Date, Message: strings.TrimRight(e.Msg, "\n"), Paths: []svnLogPath{}}
		for _, p := range e.Paths {
			le.Paths = append(le.Paths, svnLogPath{
				Action: p.Action, Path: p.Path, Kind: p.Kind,
				CopyFromPath: p.CopyFromPath, CopyFromRev: p.CopyFromRev,
			})
		}
		out = append(out, le)
	}
	return out, nil
}

// ---- svn status --xml ------------------------------------------------------------------

// svnChange is one `svn status` entry, in the spirit of gitx.Change.
type svnChange struct {
	Path      string `json:"path"`
	Status    string `json:"status"` // svn's one-letter code: M A D R C ? ! ~
	Props     string `json:"props,omitempty"`
	Untracked bool   `json:"untracked"`
	Conflict  bool   `json:"conflict"`
}

type svnStatusEntry struct {
	Path     string `xml:"path,attr"`
	WCStatus struct {
		Item  string `xml:"item,attr"`
		Props string `xml:"props,attr"`
	} `xml:"wc-status"`
}

type svnStatusXML struct {
	Targets []struct {
		Entries []svnStatusEntry `xml:"entry"`
	} `xml:"target"`
	// Entries that belong to a changelist are listed here, not under their target.
	Changelists []struct {
		Entries []svnStatusEntry `xml:"entry"`
	} `xml:"changelist"`
}

// svnStatusLetter maps wc-status/@item to the code `svn status` prints. "" means "not a
// local change" (normal, ignored, external): the list leaves it out.
func svnStatusLetter(item, props string) string {
	switch item {
	case "modified":
		return "M"
	case "added":
		return "A"
	case "deleted":
		return "D"
	case "replaced":
		return "R"
	case "conflicted":
		return "C"
	case "unversioned":
		return "?"
	case "missing", "incomplete":
		return "!"
	case "obstructed":
		return "~"
	case "normal":
		// A property-only change leaves the item "normal" and flips props.
		switch props {
		case "modified":
			return "M"
		case "conflicted":
			return "C"
		}
	}
	return ""
}

// parseSvnStatusXML decodes `svn status --xml`, run from the working copy root with target
// ".", so paths are working-copy relative. The root entry itself (".") is dropped.
func parseSvnStatusXML(data []byte) ([]svnChange, error) {
	var x svnStatusXML
	if err := xml.Unmarshal(data, &x); err != nil {
		return nil, fmt.Errorf("parse svn status: %w", err)
	}
	out := []svnChange{}
	add := func(es []svnStatusEntry) {
		for _, e := range es {
			letter := svnStatusLetter(e.WCStatus.Item, e.WCStatus.Props)
			p := filepath.ToSlash(filepath.Clean(e.Path))
			if letter == "" || p == "." {
				continue
			}
			c := svnChange{Path: p, Status: letter, Untracked: letter == "?"}
			if e.WCStatus.Props != "none" && e.WCStatus.Props != "" && e.WCStatus.Item != "normal" {
				c.Props = e.WCStatus.Props
			}
			c.Conflict = letter == "C" || e.WCStatus.Props == "conflicted"
			out = append(out, c)
		}
	}
	for _, t := range x.Targets {
		add(t.Entries)
	}
	for _, cl := range x.Changelists {
		add(cl.Entries)
	}
	return out, nil
}

// ---- argument validation ---------------------------------------------------------------

// svnRelPath validates a user-supplied working-copy-relative path: no traversal out of the
// working copy, nothing that svn could read as an option, nothing aimed at the admin area.
// "" is valid and means the working copy root. Returns the slash-separated clean path.
func svnRelPath(p string) (string, bool) {
	p = strings.TrimSpace(p)
	p = strings.TrimLeft(p, "/")
	if p == "" || p == "." {
		return "", true
	}
	if strings.ContainsRune(p, 0) || strings.HasPrefix(p, "-") {
		return "", false
	}
	clean := path.Clean(filepath.ToSlash(p))
	if clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "-") {
		return "", false
	}
	for _, seg := range strings.Split(clean, "/") {
		if seg == ".svn" {
			return "", false
		}
	}
	return clean, true
}

// svnLogTarget renders a validated relative path as an `svn log` target. A trailing "@" stops
// svn reading an "@" inside the name as a peg revision. Only log wants it: a plain working-copy
// `svn diff` takes its targets literally and answers "node not found" for "x@y@" (measured,
// svn 1.14), so diff passes the bare path.
func svnLogTarget(rel string) string {
	if rel == "" {
		return "."
	}
	return rel + "@"
}

// parseSvnRev accepts a positive decimal integer and nothing else ("HEAD", "r5", "5:9",
// "-3" and "{date}" are all revision SYNTAX that must not reach svn).
func parseSvnRev(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 9 {
		return 0, false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	return n, err == nil && n > 0
}

// svnURLJoin appends a slash-separated repository path to a repository URL, escaping each
// segment. "@" is escaped too, so it can never start a peg revision.
func svnURLJoin(base, rel string) string {
	base = strings.TrimRight(base, "/")
	if rel == "" {
		return base
	}
	segs := strings.Split(rel, "/")
	for i, s := range segs {
		segs[i] = strings.ReplaceAll(url.PathEscape(s), "@", "%40") // PathEscape leaves "@"
	}
	return base + "/" + strings.Join(segs, "/")
}

// ---- diff shape ------------------------------------------------------------------------

// svnDiffToGit rewrites svn's classic diff into the shape the Console's diff renderer splits
// on. svn opens each file with "Index: X" and a row of "=", which the renderer would take for
// context lines of the PREVIOUS file; `svn diff --git` was tried instead and prints repository
// paths ("a/trunk/x") that do not match the working-copy paths the changes list shows
// (measured, svn 1.14). So: "Index: X" becomes "diff --git a/X b/X", the "=" row is dropped,
// and the first ---/+++ pair gets the a/ b/ prefixes the renderer strips. A bare "Index:" at
// column 0 cannot be file content — every content line starts with a space, "+" or "-".
func svnDiffToGit(diff string) string {
	if !strings.Contains(diff, "Index: ") {
		return diff
	}
	lines := strings.SplitAfter(diff, "\n")
	var b strings.Builder
	b.Grow(len(diff) + 64)
	oldDone, newDone := true, true // true until an Index: line opens a file
	for i, l := range lines {
		raw := strings.TrimRight(l, "\r\n")
		nl := l[len(raw):]
		switch {
		case strings.HasPrefix(raw, "Index: ") && i+1 < len(lines) && strings.HasPrefix(lines[i+1], "====="):
			name := strings.TrimPrefix(raw, "Index: ")
			b.WriteString("diff --git a/" + name + " b/" + name + nl)
			oldDone, newDone = false, false
		case strings.HasPrefix(raw, "=====") && strings.Trim(raw, "=") == "" && i > 0 && strings.HasPrefix(lines[i-1], "Index: "):
			// the separator row under an Index: line
		case !oldDone && strings.HasPrefix(raw, "--- "):
			b.WriteString("--- a/" + raw[4:] + nl)
			oldDone = true
		case !newDone && strings.HasPrefix(raw, "+++ "):
			b.WriteString("+++ b/" + raw[4:] + nl)
			newDone = true
		default:
			b.WriteString(l)
		}
	}
	return b.String()
}

// ---- running svn -----------------------------------------------------------------------

// svnViewResult is what one view run produced.
type svnViewResult struct {
	stdout    string
	stderr    string
	truncated bool // stdout hit max and svn was stopped
}

// runSvnView runs svn with stdout and stderr kept APART — runSvn/runSvnAuthed return the
// combined stream, and a warning on stderr in front of `--xml` output would make it unparsable.
// With creds != nil or network, the auth flags and stdin password are applied (runSvnAuthed's
// contract); otherwise it is a local run. Output beyond max bytes is cut and svn is stopped,
// so one huge revision cannot be buffered whole. dir is the working directory: local targets
// are relative to it, which also makes svn print working-copy-relative paths.
func runSvnView(ctx context.Context, dir string, network bool, creds *secrets.SVNCred, max int, args ...string) (svnViewResult, error) {
	var res svnViewResult
	var full []string
	authed := false
	if network {
		full, authed = svnAuthedArgs(creds, args...)
	} else {
		full = append([]string{"--non-interactive"}, args...)
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := svnCmd(runCtx, full...)
	cmd.Dir = dir
	if authed {
		cmd.Stdin = strings.NewReader(creds.Password + "\n")
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return res, err
	}
	if err := cmd.Start(); err != nil {
		return res, err
	}
	buf, rerr := io.ReadAll(io.LimitReader(pipe, int64(max)+1))
	if len(buf) > max {
		buf, res.truncated = buf[:max], true
		cancel() // stop svn rather than let it stream the rest into a dead pipe
	}
	werr := cmd.Wait()
	res.stdout, res.stderr = string(buf), strings.TrimSpace(stderr.String())
	if res.truncated {
		return res, nil // the kill is ours; what we kept is the answer
	}
	if rerr != nil {
		return res, rerr
	}
	return res, werr
}

// svnViewFail answers a failed view run: an auth failure becomes the 401 the Console turns
// into the re-authentication dialog, anything else (E170013 included — it also means
// "unreachable") a plain 502 carrying svn's own words.
func svnViewFail(w http.ResponseWriter, res svnViewResult, err error) {
	msg := strings.TrimSpace(res.stderr)
	if msg == "" {
		msg = err.Error()
	}
	if svnAuthFailure(msg) {
		httpx.WriteErr(w, http.StatusUnauthorized, errCodeSvnAuth, msg)
		return
	}
	var ee *exec.ExitError
	code := "svn_failed"
	if !errors.As(err, &ee) {
		code = "svn_exec_failed"
	}
	httpx.WriteErr(w, http.StatusBadGateway, code, msg)
}

// svnViewDir is the common preamble: svn present, name valid, folder is an SVN working copy.
func svnViewDir(w http.ResponseWriter, r *http.Request) (string, bool) {
	if !svnAvailable() {
		httpx.WriteErr(w, http.StatusNotImplemented, "svn_missing", "the 'svn' command is not available in this workspace")
		return "", false
	}
	return svnDirFromPath(w, r)
}

// ---- handlers --------------------------------------------------------------------------

// handleSvnLog (GET /repos/{name}/svn-log?limit=&path=&from=) returns one page of history,
// newest first. `from` is the OLDEST revision of the previous page: the next page is
// `-r from-1:1`, and a page of "limit" entries is fetched with one extra so hasMore is exact.
// wcRevision is the working copy's own revision, so the Console can mark what is newer.
func handleSvnLog(w http.ResponseWriter, r *http.Request) {
	dir, ok := svnViewDir(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	limit := svnLogDefaultLimit
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 && n <= svnLogMaxLimit {
		limit = n
	}
	rel, ok := svnRelPath(q.Get("path"))
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_path", "invalid path")
		return
	}
	revRange := "HEAD:1"
	if f := strings.TrimSpace(q.Get("from")); f != "" {
		from, ok := parseSvnRev(f)
		if !ok {
			httpx.WriteErr(w, http.StatusBadRequest, "bad_rev", "from must be a positive revision number")
			return
		}
		if from <= 1 { // nothing older than r1
			httpx.WriteJSON(w, http.StatusOK, svnLogResp{Revisions: []svnLogEntry{}, WCRevision: svnInfoItem(dir, "revision")})
			return
		}
		revRange = fmt.Sprintf("%d:1", from-1)
	}
	wcRev, wcURL := svnInfo(dir)
	ctx, cancel := context.WithTimeout(r.Context(), svnViewTimeout)
	defer cancel()
	res, err := runSvnView(ctx, dir, true, svnCredsFor(wcURL), svnViewMaxBytes*4,
		"log", "--xml", "-v", "-r", revRange, "-l", strconv.Itoa(limit+1), svnLogTarget(rel))
	if err != nil {
		svnViewFail(w, res, err)
		return
	}
	entries, err := parseSvnLogXML([]byte(res.stdout))
	if err != nil {
		httpx.WriteErr(w, http.StatusBadGateway, "svn_parse_failed", err.Error())
		return
	}
	hasMore := len(entries) > limit
	if hasMore {
		entries = entries[:limit]
	}
	httpx.WriteJSON(w, http.StatusOK, svnLogResp{Revisions: entries, HasMore: hasMore, WCRevision: wcRev})
}

// svnCommitMessageParts splits a log message into its subject (first line) and the rest.
func svnCommitMessageParts(msg string) (subject, body string) {
	subject, body, _ = strings.Cut(msg, "\n")
	return strings.TrimSpace(subject), strings.TrimSpace(body)
}

// handleSvnShow (GET /repos/{name}/svn-show?rev=&path=) returns one revision: header, the
// changed paths, and the colored patch. The diff is taken against the REPOSITORY ROOT URL
// (or root + the working copy's own path joined with `path`), not the working copy: a
// revision newer than the working copy — the very thing the log marks — may not exist at
// the working copy's peg, and the log lists the whole commit's paths, so the diff matches it.
func handleSvnShow(w http.ResponseWriter, r *http.Request) {
	dir, ok := svnViewDir(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	rev, ok := parseSvnRev(q.Get("rev"))
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_rev", "rev must be a positive revision number")
		return
	}
	rel, ok := svnRelPath(q.Get("path"))
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_path", "invalid path")
		return
	}
	wcURL := svnInfoItem(dir, "url")
	root := svnInfoItem(dir, "repos-root-url")
	if wcURL == "" || root == "" {
		httpx.WriteErr(w, http.StatusBadGateway, "no_url", "cannot read this working copy's repository URL")
		return
	}
	creds := svnCredsFor(wcURL)
	ctx, cancel := context.WithTimeout(r.Context(), svnViewTimeout)
	defer cancel()

	revArg := strconv.Itoa(rev)
	logRes, err := runSvnView(ctx, dir, true, creds, svnViewMaxBytes*4, "log", "--xml", "-v", "-r", revArg, root)
	if err != nil {
		svnViewFail(w, logRes, err)
		return
	}
	entries, err := parseSvnLogXML([]byte(logRes.stdout))
	if err != nil || len(entries) != 1 {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such revision")
		return
	}
	e := entries[0]

	// Narrow to the sub-path: the working copy's own location inside the repository, then rel.
	diffTarget := root
	paths := e.Paths
	if rel != "" {
		prefix := strings.TrimPrefix(wcURL, root)
		if prefix == "" {
			prefix = "/"
		}
		repoPath := path.Join(prefix, rel)
		diffTarget = svnURLJoin(root, strings.TrimLeft(repoPath, "/"))
		kept := []svnLogPath{}
		for _, p := range paths {
			if p.Path == repoPath || strings.HasPrefix(p.Path, repoPath+"/") {
				kept = append(kept, p)
			}
		}
		paths = kept
	}
	diffRes, err := runSvnView(ctx, dir, true, creds, svnViewMaxBytes, "diff", "-c", revArg, diffTarget)
	if err != nil {
		svnViewFail(w, diffRes, err)
		return
	}
	subject, body := svnCommitMessageParts(e.Message)
	httpx.WriteJSON(w, http.StatusOK, svnShowResp{
		Rev: e.Rev, Short: "r" + strconv.Itoa(e.Rev), Author: e.Author, Date: e.Date,
		Subject: subject, Body: body, Paths: paths,
		Diff: svnDiffToGit(diffRes.stdout), Truncated: diffRes.truncated,
	})
}

// handleSvnChanges (GET /repos/{name}/svn-changes) lists local modifications. LOCAL ONLY: no
// credential, no network, safe on the Console's auto-refresh tick.
func handleSvnChanges(w http.ResponseWriter, r *http.Request) {
	dir, ok := svnViewDir(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), svnViewTimeout)
	defer cancel()
	res, err := runSvnView(ctx, dir, false, nil, svnViewMaxBytes*4, "status", "--xml", ".")
	if err != nil {
		svnViewFail(w, res, err)
		return
	}
	cs, err := parseSvnStatusXML([]byte(res.stdout))
	if err != nil {
		httpx.WriteErr(w, http.StatusBadGateway, "svn_parse_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, svnChangesResp{Changes: cs})
}

// handleSvnDiff (GET /repos/{name}/svn-diff?path=) is one file's (or, with no path, the whole
// working copy's) uncommitted diff, in git's unified format. LOCAL ONLY.
func handleSvnDiff(w http.ResponseWriter, r *http.Request) {
	dir, ok := svnViewDir(w, r)
	if !ok {
		return
	}
	rel, ok := svnRelPath(r.URL.Query().Get("path"))
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_path", "invalid path")
		return
	}
	diffTarget := rel
	if diffTarget == "" {
		diffTarget = "."
	}
	ctx, cancel := context.WithTimeout(r.Context(), svnViewTimeout)
	defer cancel()
	res, err := runSvnView(ctx, dir, false, nil, svnViewMaxBytes, "diff", diffTarget)
	if err != nil {
		svnViewFail(w, res, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, svnDiffResp{Diff: svnDiffToGit(res.stdout), Truncated: res.truncated})
}
