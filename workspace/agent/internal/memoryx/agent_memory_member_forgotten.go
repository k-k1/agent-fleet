package memoryx

// The member's list of forgotten memories (#2003). The changes list is a window of recent
// changes, so a memory forgotten before that window could not be found to restore. The store
// keeps a tombstone per forgotten name under <scope dir>/.forgotten/, and this lists those
// directly. Like the rest of the member routes it is read-only, counts no use and is not gated by
// the switch; a restore still goes through the revert route with its own checks.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// agentMemForgottenMax bounds the rows of one page (a variable so a test can use a small one).
var agentMemForgottenMax = 300

// agentMemTombJudgeMax bounds how many memories one request judges (a few git calls each).
// Candidates are ordered newest first, and those without a published change are dropped before the
// budget is spent, so only the oldest published ones can fall outside it.
var agentMemTombJudgeMax = 2000

// agentMemSeqTrailer carries the write time in nanoseconds; see agentMemTombCand.seq.
const agentMemSeqTrailer = "AF-Seq"

const (
	// agentMemTombMaxSize: a tombstone holds a revision number. Anything bigger is not one.
	agentMemTombMaxSize = 64
)

// agentMemForgottenWire is the forgotten list, newest forgotten first. Rows are the same view the
// changes list gives its newest row, so Revertible says whether the commit can be restored.
// Withheld counts names left out because the name failed the scan or the newest change of the
// memory did; they are never listed. Truncated says listable memories were left out because more than one
// answer holds: narrow the scope.
// Projects names every project that has a listable forgotten memory, cut or not, so the Console can
// keep offering a scope whose rows did not fit.
type agentMemForgottenWire struct {
	Forgotten []agentMemChangeView `json:"forgotten"`
	Projects  []*agentMemProject   `json:"projects"`
	Withheld  int                  `json:"withheld,omitempty"`
	Truncated bool                 `json:"truncated,omitempty"`
	// Next is the cursor to pass as `before` for the following page, set whenever candidates remain
	// that this page did not list: past the page size, or past the judging budget. Withheld, Projects
	// and Incomplete describe the first page's scan; the Console takes them from there and only adds
	// the projects of later pages.
	Next string `json:"next,omitempty"`
	// Incomplete says the request ran out of judging budget: the oldest published tombstones were
	// not judged, so rows may be missing even after narrowing the scope, and Withheld is a lower
	// bound.
	Incomplete bool `json:"incomplete,omitempty"`
}

// agentMemTombNames lists the names with a tombstone under one scope directory. The directory is
// untrusted: a symlink anywhere on the way, a non-regular or oversized entry, or a name that is
// not a memory name is not a tombstone. bad counts entries refused for their name.
func agentMemTombNames(scopeRel string) (names []string, bad int) {
	dir, err := agentMemCheckDir(scopeRel+"/"+agentMemTombDir, false)
	if err != nil {
		return nil, 0
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0
	}
	for _, d := range ents {
		info, err := d.Info()
		if err != nil || !d.Type().IsRegular() || info.Size() > agentMemTombMaxSize {
			continue
		}
		// The raw name is judged before it is used for anything: it may be a token.
		if agentMemValidName(d.Name()) && agentMemCleanText(d.Name()) {
			names = append(names, d.Name())
		} else {
			bad++
		}
	}
	sort.Strings(names)
	return names, bad
}

// agentMemTombCand is one tombstone with the newest change of its memory, found in a single pass
// over the history rather than one search per memory.
type agentMemTombCand struct {
	scope, project, name, rel string
	commit                    string
	at                        time.Time
	// seq is the AF-Seq trailer of the commit (nanoseconds at write time, 0 when absent). Commit
	// times have one-second resolution, so seq is what orders two changes of one second. It lives
	// in the message, so a purge that rewrites history keeps it; a position counted in commits
	// would shift when the purge drops some.
	seq int64
}

// agentMemTombBefore is the list order: newest change first. Two changes in the same second go
// by the AF-Seq trailer (a forget made later lists first), and only then does the path digest break a
// tie. The cursor carries all three, so a position is a point in this order and does not depend
// on the cursor's own row still being there.
func agentMemTombBefore(a, b agentMemTombCand) bool {
	if !a.at.Equal(b.at) {
		return a.at.After(b.at)
	}
	if a.seq != b.seq {
		return a.seq > b.seq
	}
	return agentMemTombDigest(a.rel) < agentMemTombDigest(b.rel)
}

// agentMemTombCursor names a position in that order: the time of the newest change, its write
// sequence and a digest of the path. The path itself never goes in: the cursor may name a candidate that was withheld (a
// token-shaped project id, or text that fails the scan), and the answer must not carry either.
func agentMemTombCursor(c agentMemTombCand) string {
	return c.at.UTC().Format(time.RFC3339Nano) + "|" + strconv.FormatInt(c.seq, 10) + "|" + agentMemTombDigest(c.rel)
}

func agentMemTombDigest(rel string) string {
	sum := sha256.Sum256([]byte(rel))
	return hex.EncodeToString(sum[:16])
}

type agentMemTombPos struct {
	at     time.Time
	seq    int64
	digest string
}

func agentMemParseTombCursor(s string) (agentMemTombPos, bool) {
	ts, rest, ok := strings.Cut(s, "|")
	if !ok {
		return agentMemTombPos{}, false
	}
	ordS, dig, ok := strings.Cut(rest, "|")
	seq, ordErr := strconv.ParseInt(ordS, 10, 64)
	if _, err := hex.DecodeString(dig); !ok || len(dig) != 32 || err != nil || ordErr != nil || seq < 0 {
		return agentMemTombPos{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return agentMemTombPos{}, false
	}
	return agentMemTombPos{at: at, seq: seq, digest: dig}, true
}

// agentMemTombResume is the index of the first candidate ordered after the cursor, whether or not
// the candidate the cursor named still exists.
func agentMemTombResume(found []agentMemTombCand, p agentMemTombPos) int {
	return sort.Search(len(found), func(i int) bool {
		c := found[i]
		if !c.at.Equal(p.at) {
			return c.at.Before(p.at)
		}
		if c.seq != p.seq {
			return c.seq < p.seq
		}
		return agentMemTombDigest(c.rel) > p.digest
	})
}

// agentMemNewestCommits returns, for each wanted "af/..." path, the newest agent-memory commit
// that names it with its author time, deciding "newest" by the trailer parsing of
// agentMemLatestChange. A commit whose time does not parse sorts as the zero time.
func agentMemNewestCommits(want map[string]bool) (map[string]agentMemTombCand, error) {
	out := map[string]agentMemTombCand{}
	if len(want) == 0 {
		return out, nil
	}
	log, err := memoryGitRun("log", memoryBranch, "-F", "--grep=AF-Trigger: "+memoryTriggerAgentMemory,
		"--format=%H"+memoryFldSep+"%aI"+memoryFldSep+"%B"+memoryRecSep)
	if err != nil {
		return nil, err
	}
	for _, rec := range strings.Split(log, memoryRecSep) {
		f := strings.SplitN(strings.TrimSpace(rec), memoryFldSep, 3)
		if len(f) < 3 {
			continue
		}
		tr := agentMemTrailers(f[2])
		path := tr["AF-Memory"]
		if tr["AF-Trigger"] != memoryTriggerAgentMemory || !want[path] {
			continue
		}
		if _, seen := out[path]; seen {
			continue
		}
		at, _ := time.Parse(time.RFC3339, f[1])
		seq, _ := strconv.ParseInt(tr[agentMemSeqTrailer], 10, 64)
		seq = max(seq, 0)
		out[path] = agentMemTombCand{commit: f[0], at: at, seq: seq}
	}
	return out, nil
}

// agentMemMemberForgotten lists the forgotten memories of the user scope and every project, or of
// the one scope named, newest forgotten first. Every tombstone is judged before the newest rows
// are chosen, so the cut never drops a newer memory for an older one. A tombstone counts only
// while the memory is absent and has a published change; the newest change is judged by
// agentMemMemberLatest's scan, raw and decoded, so a name or text that would be withheld there is
// counted here, never listed. agentMemMu is taken per memory, not across the request.
//
// before is the cursor of an earlier answer's Next: the candidates ordered after it are listed. The
// order is (time of the newest change, write sequence, path), a total order that a write between two pages cannot
// shift, so a page neither repeats a row nor skips one. A memory forgotten again after page one
// moves to the front instead.
func agentMemMemberForgotten(scope, project, before string) (agentMemForgottenWire, error) {
	out := agentMemForgottenWire{Forgotten: []agentMemChangeView{}, Projects: []*agentMemProject{}}
	var cursor *agentMemTombPos
	if before != "" {
		c, ok := agentMemParseTombCursor(before)
		if !ok {
			return out, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "before is not a cursor of this list")
		}
		cursor = &c
	}
	type src struct{ scope, project, rel string }
	var srcs []src
	switch scope {
	case "":
		srcs = append(srcs, src{agentMemScopeUser, "", "user"})
		if dir, err := agentMemCheckDir("projects", false); err == nil {
			ents, _ := os.ReadDir(dir)
			for _, d := range ents {
				if d.IsDir() && agentMemValidProjectID(d.Name()) {
					srcs = append(srcs, src{agentMemScopeProject, d.Name(), "projects/" + d.Name()})
				}
			}
		}
	default:
		if _, err := agentMemMemberCaller(scope, project); err != nil {
			return out, err
		}
		rel := "user"
		if scope == agentMemScopeProject {
			rel = "projects/" + project
		}
		srcs = append(srcs, src{scope, project, rel})
	}

	var cands []agentMemTombCand
	want := map[string]bool{}
	for _, s := range srcs {
		names, bad := agentMemTombNames(s.rel)
		out.Withheld += bad
		for _, name := range names {
			cands = append(cands, agentMemTombCand{scope: s.scope, project: s.project, name: name, rel: s.rel + "/" + name + ".md"})
			want[agentMemRepoPrefix+"/"+s.rel+"/"+name+".md"] = true
		}
	}
	agentMemMu.RLock()
	var newest map[string]agentMemTombCand
	var err error
	if memoryHasCommits() {
		newest, err = agentMemNewestCommits(want)
	}
	agentMemMu.RUnlock()
	if err != nil {
		return out, err
	}
	found := cands[:0]
	for _, c := range cands {
		if n, ok := newest[agentMemRepoPrefix+"/"+c.rel]; ok {
			c.commit, c.at, c.seq = n.commit, n.at, n.seq
			found = append(found, c)
		}
	}
	sort.SliceStable(found, func(i, j int) bool { return agentMemTombBefore(found[i], found[j]) })
	if cursor != nil {
		found = found[agentMemTombResume(found, *cursor):]
	}

	projects := map[string]bool{}
	// A project id is shown to the Console, and its form check does not scan it: a token-shaped
	// id is withheld whether or not the memory was judged.
	addProject := func(id string) bool {
		if id == "" || projects[id] {
			return true
		}
		if !agentMemCleanText(id) {
			return false
		}
		projects[id] = true
		out.Projects = append(out.Projects, agentMemProjectInfo(id))
		return true
	}
	var last agentMemTombCand // the newest row of this page that is listed
	for i, c := range found {
		if i >= agentMemTombJudgeMax {
			if out.Next == "" && i > 0 {
				out.Next = agentMemTombCursor(found[i-1])
			}
			// Out of judging budget, oldest first. The project is still offered (unless the
			// memory is plainly live), and the answer says it is incomplete.
			out.Incomplete = true
			if _, err := os.Lstat(filepath.Join(agentMemDir(), filepath.FromSlash(c.rel))); err != nil && !addProject(c.project) {
				out.Withheld++
			}
			continue
		}
		agentMemMu.RLock()
		v, err := agentMemMemberChangeAt(c.scope, c.name, c.rel, c.commit)
		agentMemMu.RUnlock()
		var ue *memoryUserErr
		switch {
		case err == nil:
		case errors.As(err, &ue) && ue.Code == errCodeMemorySecretDetected:
			out.Withheld++
			continue
		default:
			return out, err
		}
		if v.Live {
			continue
		}
		addProject(c.project) // already scanned by agentMemChangeFrom
		if len(out.Forgotten) >= agentMemForgottenMax {
			out.Truncated = true
			out.Next = agentMemTombCursor(last)
			continue
		}
		out.Forgotten = append(out.Forgotten, v)
		last = c
	}
	return out, nil
}

// HandleAgentMemoryMemberForgotten lists the forgotten memories to the Console. Read-only.
func HandleAgentMemoryMemberForgotten(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	out, err := agentMemMemberForgotten(q.Get("scope"), q.Get("project"), q.Get("before"))
	if err != nil {
		agentMemWriteErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
