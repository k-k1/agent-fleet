package memoryx

// Lint step 2 (ADR 0130 decision 5): the findings become `reasons`, and a deterministic rename is
// applied to the body. Both are published changes authored `lint`, one commit per memory, each
// under the store lock with a compare-and-swap on the revision the findings were computed on: a
// memory that changed in between is left for the next run, never marked from the old text.
//
// The lock is taken once per memory and released between writes, so an agent saving meanwhile
// waits for one commit, not for the run. A run writes nothing when nothing changed (the reasons
// are compared first) and at most agentMemLintApplyMax memories, so the first run over a large
// store is a bounded number of commits and the next run continues.

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memorylint"
)

// agentMemLintApplyMax bounds the memories one run writes (a var so a test can lower it).
var agentMemLintApplyMax = 100

const (
	agentMemLintAuthor     = "lint"
	agentMemOpLint         = "lint"
	agentMemOpLintRename   = "lint-rename"
	agentMemLintRenameHops = 5
)

// agentMemLintRenameCommits bounds how far back renames are read (a var so a test can lower it).
var agentMemLintRenameCommits = 2000

// agentMemLintCaller is the author of every lint write.
func agentMemLintCaller(c agentMemCaller) agentMemCaller {
	return agentMemCaller{Session: agentMemLintAuthor, Kind: agentMemLintAuthor, Project: c.Project}
}

// agentMemLintRuleChecks are the checks whose findings are reasons. An oversize body is advice,
// not a doubt about whether the memory is true.
var agentMemLintReasonChecks = map[string]bool{
	memorylint.CheckBrokenLink:  true,
	memorylint.CheckMissingPath: true,
	memorylint.CheckMissingSym:  true,
	memorylint.CheckMissingADR:  true,
	memorylint.CheckRetiredTerm: true,
}

// agentMemLintRepoHealth says which repository rules actually ran: the path and ADR rules need the
// tree to have been read, the symbol rule also its own search. A degradation note elsewhere (an
// unreadable .gitignore, a proposal source) only makes the report report more, never less, so it
// does not stop a reason from being cleared.
func agentMemLintRepoHealth(repo memorylint.Repo, skipped []string) (pathsOK, symsOK bool) {
	if repo == nil {
		return false, false
	}
	pathsOK, symsOK = true, true
	for _, s := range skipped {
		switch {
		case strings.HasPrefix(s, "repository checks"):
			pathsOK, symsOK = false, false
		case strings.HasPrefix(s, "symbol checks"):
			symsOK = false
		}
	}
	return pathsOK, symsOK && pathsOK
}

// agentMemLintNote is a finding's subject as a one-line reason note.
func agentMemLintNote(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	return agentMemCutRunes(strings.TrimSpace(s), agentMemMaxReasonNote)
}

// agentMemLintReasonsFor is the lint reasons a memory should carry after a run: the findings that
// fire now, plus the earlier ones that stay. An earlier lint reason is dropped only when the
// memory has moved past the revision it was found on and its rule no longer fires; a rule that
// did not run (ran[rule] false) clears nothing. Other authors' reasons are kept untouched.
func agentMemLintReasonsFor(e agentMemEntry, fs []memorylint.Finding, ran func(rule string) bool) []agentMemReason {
	type key struct{ rule, note string }
	fire := map[key]bool{}
	var fresh []key
	for _, f := range fs {
		if !agentMemLintReasonChecks[f.Check] {
			continue
		}
		k := key{f.Check, agentMemLintNote(f.Subject)}
		if !fire[k] {
			fire[k] = true
			fresh = append(fresh, k)
		}
	}
	var out []agentMemReason
	have := map[key]bool{}
	for _, r := range e.Reasons {
		if r.By != agentMemReasonByLint {
			out = append(out, r)
			continue
		}
		k := key{r.Rule, r.Note}
		if !fire[k] && ran(r.Rule) && r.Revision < e.Revision {
			continue
		}
		have[k] = true
		out = append(out, r)
	}
	for _, k := range fresh {
		if have[k] || len(out) >= agentMemMaxReasons {
			continue
		}
		// The revision the reason first rides on: the write that adds it increments the revision
		// and leaves the checked text alone, so "newer than this" below means a change by
		// someone else, never lint's own bump.
		out = append(out, agentMemReason{By: agentMemReasonByLint, Rule: k.rule, Note: k.note, Revision: e.Revision + 1})
	}
	return out
}

func agentMemReasonsEqual(a, b []agentMemReason) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// agentMemLintApplyOut is what one run did.
type agentMemLintApplyOut struct {
	Scope string `json:"scope"`
	// Marked counts memories whose reasons changed; Renamed those whose body had a path replaced.
	Marked  int `json:"marked"`
	Renamed int `json:"renamed"`
	// Changed counts memories left for the next run because they moved after the check.
	Changed int `json:"changed"`
	// Deferred counts memories past the per-run bound.
	Deferred int      `json:"deferred"`
	Notes    []string `json:"notes,omitempty"`
}

// agentMemLintApply runs lint over one scope and publishes what it found: first the renames, then
// the reasons (on a fresh check, because a rename moves the revision).
func agentMemLintApply(scope, project string, now time.Time) (agentMemLintApplyOut, error) {
	budget := agentMemLintApplyMax
	return agentMemLintApplyBudget(scope, project, now, &budget)
}

// agentMemLintApplyBudget is agentMemLintApply drawing on a write budget the caller owns, so a run
// over several scopes shares one per-run cap instead of getting one per scope.
func agentMemLintApplyBudget(scope, project string, now time.Time, budgetp *int) (agentMemLintApplyOut, error) {
	out := agentMemLintApplyOut{Scope: scope}
	budget := *budgetp
	defer func() { *budgetp = budget }()

	wire, es, c, repo, err := agentMemLintCollect(scope, project)
	if err != nil {
		return out, err
	}
	if rr, ok := repo.(agentMemLintRepo); ok {
		n, err := agentMemLintApplyRenames(rr, wire, es, c, now, &out, &budget)
		if err != nil {
			return out, err
		}
		if n > 0 {
			if wire, es, c, repo, err = agentMemLintCollectMemo(scope, project, rr.memo); err != nil {
				return out, err
			}
		}
	}
	pathsOK, symsOK := agentMemLintRepoHealth(repo, wire.Report.Skipped)
	ran := func(rule string) bool {
		switch rule {
		case memorylint.CheckMissingPath, memorylint.CheckMissingADR:
			return pathsOK
		case memorylint.CheckMissingSym:
			return symsOK
		}
		return true
	}
	if repo == nil {
		out.Notes = append(out.Notes, "repository checks did not run: no path, symbol or ADR reason was added or cleared")
	}
	byMem := map[string][]memorylint.Finding{}
	for _, f := range wire.Report.Findings {
		byMem[f.Memory] = append(byMem[f.Memory], f)
	}
	sort.Slice(es, func(i, j int) bool { return es[i].Name < es[j].Name })
	lc := agentMemLintCaller(c)
	for _, e := range es {
		if agentMemExcluded(e) {
			continue
		}
		want := agentMemLintReasonsFor(e, byMem[e.Name], ran)
		if agentMemReasonsEqual(want, e.Reasons) {
			continue
		}
		if budget <= 0 {
			out.Deferred++
			continue
		}
		wrote, err := agentMemLintWrite(lc, scope, e, now, func(cur *agentMemEntry) (bool, []string) {
			cur.Reasons = want
			return true, nil
		}, agentMemOpLint)
		switch {
		case err == nil && wrote:
			out.Marked++
			budget--
		case err == nil:
		case err == errAgentMemLintMoved:
			out.Changed++
		default:
			return out, err
		}
	}
	return out, nil
}

var errAgentMemLintMoved = fmt.Errorf("the memory moved since it was checked")

// agentMemLintWrite publishes one lint change to memory `checked`, if it is still at the
// revision the check read (the compare-and-swap), as one commit with the revision incremented.
// mut edits the stored entry and says whether anything changed, with the commit's extra trailers.
// wrote is false when there was nothing to publish or the text was refused by the secret scan.
func agentMemLintWrite(lc agentMemCaller, scope string, checked agentMemEntry, now time.Time, mut func(*agentMemEntry) (bool, []string), op string) (wrote bool, err error) {
	rel, err := agentMemScopeDir(scope, lc)
	if err != nil {
		return false, err
	}
	agentMemMu.Lock()
	defer agentMemMu.Unlock()
	memorySnapshotMu.Lock()
	defer memorySnapshotMu.Unlock()

	cur, exists, err := agentMemLoadOne(lc, scope, checked.Name)
	if err != nil {
		return false, err
	}
	// A feedback reason is written without a revision bump, so the revision alone cannot see it:
	// a changed reason set is a change since the check as well.
	if !exists || cur.Withheld || cur.Entry.Revision != checked.Revision || !agentMemReasonsEqual(cur.Entry.Reasons, checked.Reasons) {
		return false, errAgentMemLintMoved
	}
	e := cur.Entry
	changed, extra := mut(&e)
	if !changed {
		return false, nil
	}
	e.Revision++
	e.AuthorKind, e.AuthorSession = lc.Kind, lc.Session
	data := agentMemRender(e)
	if f := agentMemScanText("memory", string(data)); len(f) > 0 {
		return false, nil // never publish text the scan refuses; the member sees it in the report
	}
	repoRel := rel + "/" + checked.Name + ".md"
	if _, err := agentMemApplyLocked([]agentMemChange{{Rel: repoRel, Data: data}}, op, repoRel, lc, now, extra...); err != nil {
		return false, err
	}
	agentMemPrimeScan(data, scope, checked.Name)
	return true, nil
}

// ---- lint-rename ---------------------------------------------------------------------------

// agentMemLintRename is where a path went: the new path and the commit that renamed it.
type agentMemLintRename struct{ To, Commit string }

// agentMemLintDirRename is where a directory went, inferred from the file renames of one commit
// (git records no directory renames): Files is how many files that commit renamed out of it, which
// is checked against what the directory held before the commit. To is empty when the commit's
// renames out of the directory disagree about where it went: the newest word on a directory
// stays, so an older rename never fills in for an ambiguous newer one.
type agentMemLintDirRename struct {
	agentMemLintRename
	Files int
}

// agentMemLintRenames is what the history says moved, newest first (the newest rename of a path
// wins).
type agentMemLintRenames struct {
	File map[string]agentMemLintRename
	Dir  map[string]agentMemLintDirRename
	// Unread counts renames left out because a path is quoted by git (tab, newline, quote).
	Unread int
}

// agentMemLintRenameMemo keeps the history read for the length of one run, so the report's
// former-directory lookup and the apply step read it once between them.
type agentMemLintRenameMemo struct {
	ref  string
	once sync.Once
	ren  agentMemLintRenames
	err  error

	mu     sync.Mutex
	before map[[2]string]int
}

// Renames reads the renames of the pinned revision's history. Merge commits are not read: a
// rename that only a merge shows is simply not applied.
func (r agentMemLintRepo) Renames() (agentMemLintRenames, error) {
	if r.memo == nil {
		return r.readRenames()
	}
	r.memo.once.Do(func() { r.memo.ren, r.memo.err = r.readRenames() })
	return r.memo.ren, r.memo.err
}

// FormerTopDirs lists the top-level directories that a commit renamed whole, for the path check:
// such a directory is no longer in the tree, and without it a body that still names it would never
// be recognised as a path. A directory only partly moved, or moved to several places, is not one:
// its name may be an ordinary word ("left/right") that is not worth a reason.
func (r agentMemLintRepo) FormerTopDirs() ([]string, error) {
	ren, err := r.Renames()
	if err != nil {
		return nil, err
	}
	var out []string
	for d, dr := range ren.Dir {
		if strings.Contains(d, "/") || dr.To == "" {
			continue
		}
		if n, err := r.DirFilesBefore(dr.Commit, d); err == nil && n == dr.Files {
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (r agentMemLintRepo) readRenames() (agentMemLintRenames, error) {
	ctx, cancel := context.WithTimeout(context.Background(), agentMemLintGitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "--git-dir="+r.gitDir, "-c", "core.quotePath=false", "log",
		"-n", fmt.Sprint(agentMemLintRenameCommits), "--no-merges", "-M", "--diff-filter=R", "--name-status",
		"--no-color", "--no-ext-diff", "--format=%x01%H", r.ref)
	cmd.Env = append(cmd.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "GIT_NO_REPLACE_OBJECTS=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return agentMemLintRenames{}, fmt.Errorf("git log failed")
	}
	if err := cmd.Start(); err != nil {
		return agentMemLintRenames{}, fmt.Errorf("git log failed")
	}
	out := agentMemLintRenames{File: map[string]agentMemLintRename{}, Dir: map[string]agentMemLintDirRename{}}
	commit := ""
	var pairs [][2]string
	flush := func() {
		agentMemLintAddDirRenames(out.Dir, commit, pairs)
		pairs = pairs[:0]
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		line := sc.Text()
		if rest, ok := strings.CutPrefix(line, "\x01"); ok {
			flush()
			commit = rest
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 3 && strings.HasPrefix(line, "R") {
			out.Unread++ // a tab or newline in a name splits or quotes the line
			continue
		}
		if len(f) != 3 || !strings.HasPrefix(f[0], "R") {
			continue
		}
		if strings.HasPrefix(f[1], `"`) || strings.HasPrefix(f[2], `"`) {
			out.Unread++
			continue
		}
		pairs = append(pairs, [2]string{f[1], f[2]})
		if _, seen := out.File[f[1]]; !seen {
			out.File[f[1]] = agentMemLintRename{To: f[2], Commit: commit}
		}
	}
	flush()
	for sc.Scan() {
	}
	if err := cmd.Wait(); err != nil || sc.Err() != nil {
		return agentMemLintRenames{}, fmt.Errorf("git log failed")
	}
	return out, nil
}

// agentMemLintAddDirRenames infers, from the file renames of one commit, the directories that
// moved: a file a/b/c/x -> n/m/c/x says a/b -> n/m and also a/b/c -> n/m/c (the moved part is
// where the two paths first differ; everything below it kept its name). A directory whose files
// went to different places in the commit is ambiguous and recorded as such. dst keeps the first
// (newest) word on each directory.
func agentMemLintAddDirRenames(dst map[string]agentMemLintDirRename, commit string, pairs [][2]string) {
	type cand struct {
		to    string
		files int
	}
	cands := map[string]*cand{}
	for _, p := range pairs {
		old, nw := p[0], p[1]
		for i := strings.LastIndexByte(old, '/'); i > 0; i = strings.LastIndexByte(old[:i], '/') {
			d, rest := old[:i], old[i+1:]
			if !strings.HasSuffix(nw, "/"+rest) {
				break // the longer tails cannot match either
			}
			to := nw[:len(nw)-len(rest)-1]
			if to == "" {
				break
			}
			c := cands[d]
			if c == nil {
				c = &cand{to: to}
				cands[d] = c
			} else if c.to != to {
				c.to = "" // ambiguous
			}
			c.files++
		}
	}
	for d, c := range cands {
		if old, seen := dst[d]; seen {
			// An older commit that sent the same directory somewhere else makes the move
			// ambiguous: part of it may have gone to either place.
			if old.To != "" && c.to != old.To {
				old.To = ""
				dst[d] = old
			}
			continue
		}
		dr := agentMemLintDirRename{Files: c.files}
		if c.to != "" {
			dr.agentMemLintRename = agentMemLintRename{To: c.to, Commit: commit}
		}
		dst[d] = dr
	}
}

// DirFilesBefore counts the files under dir in the parent of commit. A directory rename counts
// only when every one of them was renamed by that commit: a directory that was split, or that
// kept files behind, is not "renamed" and its anchors are left to the reason.
func (r agentMemLintRepo) DirFilesBefore(commit, dir string) (int, error) {
	if m := r.memo; m != nil {
		k := [2]string{commit, dir}
		m.mu.Lock()
		n, ok := m.before[k]
		m.mu.Unlock()
		if ok {
			return n, nil
		}
		n, err := r.dirFilesBefore(commit, dir)
		if err == nil {
			m.mu.Lock()
			if m.before == nil {
				m.before = map[[2]string]int{}
			}
			m.before[k] = n
			m.mu.Unlock()
		}
		return n, err
	}
	return r.dirFilesBefore(commit, dir)
}

func (r agentMemLintRepo) dirFilesBefore(commit, dir string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), agentMemLintGitTimeout)
	defer cancel()
	out, err := agentMemLintGit(ctx, r.gitDir, nil, "ls-tree", "-r", "--name-only", "-z", commit+"^", "--", dir+"/")
	if err != nil {
		return 0, fmt.Errorf("git ls-tree: %w", err)
	}
	n := 0
	for _, p := range strings.Split(string(out), "\x00") {
		if strings.HasPrefix(p, dir+"/") {
			n++
		}
	}
	return n, nil
}

// agentMemLintHop is one step of a path's move: where it was, where it went and the commit.
type agentMemLintHop struct{ From, To, Commit string }

// agentMemLintResolveRename follows old through the renames to a path that exists in the
// revision and returns every hop, so the evidence names each commit. A file rename wins over a
// directory one; otherwise the longest renamed directory prefix of the path (a whole segment:
// "dir2/x" is not under "dir") moves it, provided dirMoved confirms the commit moved the
// directory whole.
func agentMemLintResolveRename(old string, ren agentMemLintRenames, exists func(string) bool, dirMoved func(commit, dir string, files int) bool) ([]agentMemLintHop, bool) {
	cur := old
	var hops []agentMemLintHop
	for i := 0; i < agentMemLintRenameHops; i++ {
		next, ok := agentMemLintRenameStep(cur, ren, dirMoved)
		if !ok {
			return nil, false
		}
		hops = append(hops, agentMemLintHop{cur, next.To, next.Commit})
		cur = next.To
		if exists(cur) {
			return hops, true
		}
	}
	return nil, false
}

func agentMemLintRenameStep(p string, ren agentMemLintRenames, dirMoved func(commit, dir string, files int) bool) (agentMemLintRename, bool) {
	if r, ok := ren.File[p]; ok {
		return r, true
	}
	for i := len(p); i > 0; i = strings.LastIndexByte(p[:i], '/') {
		d := p[:i]
		dr, ok := ren.Dir[d]
		if !ok {
			continue
		}
		if dr.To == "" || dirMoved == nil || !dirMoved(dr.Commit, d, dr.Files) {
			return agentMemLintRename{}, false // the nearest directory word is the one that counts
		}
		return agentMemLintRename{To: dr.To + p[i:], Commit: dr.Commit}, true
	}
	return agentMemLintRename{}, false
}

func agentMemLintPathByte(b byte, trailing bool) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9', b == '_', b == '-':
		return true
	}
	return !trailing && (b == '.' || b == '/')
}

// agentMemLintReplacePath replaces old with to wherever old stands as a whole path: not the tail
// of a longer path, not a directory prefix, not the stem of a longer file name. It returns the
// new text and how many places changed.
func agentMemLintReplacePath(s, old, to string) (string, int) {
	var b strings.Builder
	n, at := 0, 0
	for {
		i := strings.Index(s[at:], old)
		if i < 0 {
			break
		}
		i += at
		end := i + len(old)
		okPrev := i == 0 || !agentMemLintPathByte(s[i-1], false) ||
			// "./old": a leading "./" is spelling, as the extraction treats it.
			(s[i-1] == '/' && i >= 2 && s[i-2] == '.' && (i == 2 || !agentMemLintPathByte(s[i-3], false)))
		// "dir/" ends the path (a directory written with its slash); "dir/x" continues it.
		okNext := end >= len(s) || !agentMemLintPathByte(s[end], true) &&
			!(s[end] == '.' && end+1 < len(s) && agentMemLintPathByte(s[end+1], true)) &&
			(s[end] != '/' || end+1 >= len(s) || !agentMemLintPathByte(s[end+1], false))
		b.WriteString(s[at:i])
		if okPrev && okNext {
			b.WriteString(to)
			n++
		} else {
			b.WriteString(old)
		}
		at = end
	}
	b.WriteString(s[at:])
	return b.String(), n
}

// agentMemLintApplyRenames replaces, in each memory that names a path the default branch no
// longer has, the one path that a git rename moved. Everything else in the body stays, and the
// commit that renamed it rides in the change's trailers as evidence. Revertible like any change.
func agentMemLintApplyRenames(repo agentMemLintRepo, wire agentMemLintWire, es []agentMemEntry, c agentMemCaller, now time.Time, out *agentMemLintApplyOut, budget *int) (int, error) {
	byMem := map[string][]string{}
	for _, f := range wire.Report.Findings {
		if f.Check == memorylint.CheckMissingPath {
			byMem[f.Memory] = append(byMem[f.Memory], f.Subject)
		}
	}
	if pathsOK, _ := agentMemLintRepoHealth(repo, wire.Report.Skipped); len(byMem) == 0 || !pathsOK {
		return 0, nil
	}
	ren, err := repo.Renames()
	if err != nil {
		out.Notes = append(out.Notes, "renames were not read: "+err.Error())
		return 0, nil
	}
	if ren.Unread > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("%d renames of paths with quotes, tabs or newlines were not read", ren.Unread))
	}
	paths, err := repo.Paths()
	if err != nil {
		return 0, nil
	}
	have := make(map[string]bool, len(paths))
	for _, p := range paths {
		// The files and every directory above them: a renamed directory resolves to a directory.
		for d := p; d != ""; d = d[:max(strings.LastIndexByte(d, '/'), 0)] {
			have[d] = true
		}
	}
	dirMoved := func(commit, dir string, files int) bool {
		n, err := repo.DirFilesBefore(commit, dir)
		return err == nil && n == files
	}
	sort.Slice(es, func(i, j int) bool { return es[i].Name < es[j].Name })
	lc := agentMemLintCaller(c)
	done := 0
	for _, e := range es {
		subs := byMem[e.Name]
		if len(subs) == 0 || agentMemExcluded(e) {
			continue
		}
		type hit struct {
			old  string
			hops []agentMemLintHop
		}
		var hits []hit
		for _, old := range subs {
			if hops, ok := agentMemLintResolveRename(old, ren, func(p string) bool { return have[p] }, dirMoved); ok {
				hits = append(hits, hit{old, hops})
			}
		}
		if len(hits) == 0 {
			continue
		}
		if *budget <= 0 {
			out.Deferred++
			continue
		}
		wrote, err := agentMemLintWrite(lc, wire.Scope, e, now, func(cur *agentMemEntry) (bool, []string) {
			changed := false
			var trailers []string
			for _, h := range hits {
				to := h.hops[len(h.hops)-1].To
				body, n := agentMemLintReplacePath(cur.Body, h.old, to)
				cur.Body = body
				// A stored anchor takes the same replacement as the body: it may carry a ./, a
				// trailing / or a :line, which the extraction ignores too.
				for i, a := range cur.Anchors {
					r, k := agentMemLintReplacePath(a, h.old, to)
					cur.Anchors[i], n = r, n+k
				}
				if n == 0 {
					continue
				}
				changed = true
				// One trailer per hop, so a chain a -> b -> c names the commit of each move.
				for _, hop := range h.hops {
					trailers = append(trailers, "AF-Lint-Rename: "+hop.From+" -> "+hop.To+" @ "+hop.Commit)
				}
			}
			if changed {
				cur.Updated = now.UTC().Format(time.RFC3339)
				// A body change clears a feedback reason, as in save and patch (decision 3).
				agentMemDropFeedbackReasons(&cur.agentMemLife)
			}
			return changed, trailers
		}, agentMemOpLintRename)
		switch {
		case err == nil && wrote:
			done++
			out.Renamed++
			*budget--
		case err == nil:
		case err == errAgentMemLintMoved:
			out.Changed++
		default:
			return done, err
		}
	}
	return done, nil
}
