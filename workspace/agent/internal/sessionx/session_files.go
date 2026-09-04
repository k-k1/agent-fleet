package sessionx

// Aggregation of "the files this session changed" (docs/log/68 / decisions/0049).
//
// The population is the edit tool calls in the transcript; the git working-tree state is
// joined in by the Console against GET /fs/changes, keyed by `(repo, rel)`. Only the former
// is produced here.
//
// It rides along where the todos do (CollectTasks → resp["tasks"]): the whole transcript, not
// a window, is folded and included in the same /messages response. No dedicated endpoint and
// no second poll (decision 3).

import (
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// fileAggEntry is one session's folded state. Transcripts are append-only, so the answer
// for a prefix never changes and only the new tail has to be folded on each poll — which
// is what makes running a line differ over the whole history affordable at poll rate.
type fileAggEntry struct {
	src   string // transcript path; a different file means a different conversation
	head  string // fingerprint of the first record — catches a rewrite in place
	done  int    // how many lines/turns are already folded into acc
	acc   map[string]*transcript.FileTouch
	order []string // insertion order of acc's keys (stable tiebreak)
	used  time.Time
}

var (
	fileAggMu sync.Mutex
	fileAggs  = map[string]*fileAggEntry{}
)

// maxTrackedFiles bounds one session's list. A session that genuinely touches more than
// this is a mass rewrite, where a file list is not the affordance anyone wants; the cap
// keeps the response and the cache bounded rather than trying to serve that case.
const maxTrackedFiles = 500

// sessionFileTouches folds fresh transcript records into the session's accumulator and
// returns the current list, newest touch first.
//
// `stable` is how many of the records are IMMUTABLE. claude's jsonl lines never change
// once written, so it passes len(lines). The store-backed agents can still append parts
// to their last message (opencode does — see genericMutableTail), so they hold that one
// back: everything below `stable` is folded into the cache, and the mutable tail is
// folded into a copy on every call.
func sessionFileTouches(name, src, head string, n, stable int, edits func(from, to int) []transcript.FileEdit) []transcript.FileTouch {
	if stable < 0 {
		stable = 0
	}
	if stable > n {
		stable = n
	}
	fileAggMu.Lock()
	defer fileAggMu.Unlock()

	e := fileAggs[name]
	// A different transcript file, a rewritten head, or a transcript that SHRANK (reset,
	// replaced conversation, compaction into a new file) invalidates the fold — start over.
	if e == nil || e.src != src || e.head != head || e.done > stable {
		e = &fileAggEntry{src: src, head: head, acc: map[string]*transcript.FileTouch{}}
		fileAggs[name] = e
	}
	if e.done < stable {
		foldFileEdits(e, edits(e.done, stable))
		e.done = stable
	}
	e.used = time.Now()
	evictFileAggs()

	out := e
	if stable < n {
		out = cloneFileAgg(e)
		foldFileEdits(out, edits(stable, n))
	}
	list := make([]transcript.FileTouch, 0, len(out.order))
	for _, k := range out.order {
		if t := out.acc[k]; t != nil {
			list = append(list, *t)
		}
	}
	// Newest touch first: that is the question the list actually answers ("the one I just
	// changed"). Ties fall back to the transcript index, then insertion order (sort.Stable).
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].LastTS != list[j].LastTS {
			return list[i].LastTS > list[j].LastTS
		}
		return list[i].LastIdx > list[j].LastIdx
	})
	return list
}

// foldFileEdits merges raw edit calls into per-file rows.
func foldFileEdits(e *fileAggEntry, edits []transcript.FileEdit) {
	for _, ed := range edits {
		abs := absEditPath(ed.Path, ed.Cwd)
		if abs == "" {
			continue
		}
		t := e.acc[abs]
		if t == nil {
			if len(e.acc) >= maxTrackedFiles {
				continue
			}
			repo, rel := repoRelOf(abs)
			t = &transcript.FileTouch{
				Path: toBrowseRel(abs, "", browseRoot()), Repo: repo, Rel: rel,
				Sidechain: ed.Sidechain,
			}
			e.acc[abs] = t
			e.order = append(e.order, abs)
		}
		t.Count++
		t.Added += ed.Added
		t.Removed += ed.Removed
		t.Verb = ed.Verb // the last call is what the file IS now
		// Sidechain marks a file only SUBAGENTS touched; one main-thread edit clears it.
		t.Sidechain = t.Sidechain && ed.Sidechain
		if ed.TS > t.LastTS || (ed.TS == t.LastTS && ed.Idx >= t.LastIdx) {
			t.LastTS, t.LastIdx = ed.TS, ed.Idx
		}
	}
}

func cloneFileAgg(e *fileAggEntry) *fileAggEntry {
	c := &fileAggEntry{src: e.src, head: e.head, done: e.done,
		acc: make(map[string]*transcript.FileTouch, len(e.acc)), order: append([]string(nil), e.order...)}
	for k, v := range e.acc {
		cp := *v
		c.acc[k] = &cp
	}
	return c
}

// absEditPath anchors the target the agent wrote. A relative path needs the turn's cwd;
// without one there is no way to place it, and a guess would open the wrong file in
// another working copy — so it is dropped instead.
func absEditPath(p, cwd string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if !filepath.IsAbs(p) {
		if cwd == "" {
			return ""
		}
		p = filepath.Join(cwd, p)
	}
	return filepath.Clean(p)
}

// repoRelOf splits an absolute path into the working-copy folder and the path inside it.
// Both are empty for anything outside ~/repos (a file in the home dir, an agent config):
// the row is still listed, it just has no git side to be joined with.
func repoRelOf(abs string) (repo, rel string) {
	r, err := filepath.Rel(gitx.ReposRoot(), abs)
	if err != nil || r == "." || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", ""
	}
	parts := strings.SplitN(filepath.ToSlash(r), "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", ""
	}
	return parts[0], parts[1]
}

// evictFileAggs keeps the cache bounded. Entries are small (a few hundred rows), so this
// only has to stop unbounded growth across a long-lived Agent that has seen many sessions.
func evictFileAggs() {
	const keep = 64
	if len(fileAggs) <= keep {
		return
	}
	cut := time.Now().Add(-30 * time.Minute)
	for k, v := range fileAggs {
		if v.used.Before(cut) {
			delete(fileAggs, k)
		}
	}
	for len(fileAggs) > keep {
		oldest, at := "", time.Time{}
		for k, v := range fileAggs {
			if oldest == "" || v.used.Before(at) {
				oldest, at = k, v.used
			}
		}
		delete(fileAggs, oldest)
	}
}

// forgetSessionFiles drops a session's folded state (stop / delete / recreate). Losing it
// is never wrong — the next poll rebuilds from the transcript.
func forgetSessionFiles(name string) {
	fileAggMu.Lock()
	delete(fileAggs, name)
	fileAggMu.Unlock()
}

// fileAggHead fingerprints the first record so a transcript rewritten IN PLACE (same
// path, same or greater length) is not mistaken for an append.
func fileAggHead(b []byte) string {
	if len(b) > 256 {
		b = b[:256]
	}
	return string(b)
}

// ── Deciding what is committed (docs/log/68 P2) ──────────────────────────────────
//
// A row in the strip means "the agent edited this", yet some rows have no diff left in the
// working tree. P0 showed all of those as the single word "no changes", because committed and
// reverted could not be told apart.
//
// What is added here is only "committed"; "reverted" is never claimed. There are other reasons
// for having no diff and not appearing in a commit (it was already in a commit from before the
// session started, it happened in another working copy, the file was moved). The only fact
// that can be asserted is "it appeared in a commit"; calling the rest "reverted" would write an
// unfounded claim into the UI. Those rows stay "no changes".

// maxCommitScan bounds the log walk. A session that produced more commits than this is
// well past the point where a per-file badge is the interesting information.
const maxCommitScan = 200

// HandleSessionCommittedFiles reports the repo-relative paths that appeared in a commit
// in this session's working copy SINCE the session was created.
//
// The evidence is a timestamp, so commits made in parallel by another session in the same
// working copy are included too. That does little harm because the result is only joined
// against the files THIS session edited, and "a file you touched was committed afterwards" is
// true regardless of who committed it.
func HandleSessionCommittedFiles(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	meta, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"repo":      meta.Repo,
		"committed": committedSince(meta.Dir, meta.CreatedAt),
	})
}

// committedSince lists the paths touched by commits in dir since `since` (RFC3339).
// Any failure — not a git working copy, no such directory, an unparsable timestamp —
// returns an empty list: the badge simply stays "no changes", which is the honest degradation.
func committedSince(dir, since string) []string {
	if dir == "" || since == "" {
		return []string{}
	}
	if _, err := time.Parse(time.RFC3339, since); err != nil {
		return []string{}
	}
	out, err := gitx.Run(dir, "-c", "core.quotePath=false", "log",
		"--since="+since, "--max-count="+strconv.Itoa(maxCommitScan),
		"--name-only", "--pretty=format:", "--no-renames")
	if err != nil {
		return []string{}
	}
	seen := map[string]bool{}
	paths := []string{}
	for _, ln := range strings.Split(out, "\n") {
		p := strings.TrimSpace(ln)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		paths = append(paths, p)
	}
	return paths
}
