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
	"time"
	"unicode"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memorylint"
)

// agentMemLintApplyMax bounds the memories one run writes (a var so a test can lower it).
var agentMemLintApplyMax = 100

const (
	agentMemLintAuthor   = "lint"
	agentMemOpLint       = "lint"
	agentMemOpLintRename = "lint-rename"
	// agentMemLintRenameCommits bounds how far back renames are read.
	agentMemLintRenameCommits = 2000
	agentMemLintRenameHops    = 5
)

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

// agentMemLintRepoRules are the rules that only a complete repository read can clear.
var agentMemLintRepoRules = map[string]bool{
	memorylint.CheckMissingPath: true,
	memorylint.CheckMissingSym:  true,
	memorylint.CheckMissingADR:  true,
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
	out := agentMemLintApplyOut{Scope: scope}
	budget := agentMemLintApplyMax

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
			if wire, es, c, repo, err = agentMemLintCollect(scope, project); err != nil {
				return out, err
			}
		}
	}
	ranRepo := repo != nil && len(wire.Report.Skipped) == 0
	ran := func(rule string) bool { return !agentMemLintRepoRules[rule] || ranRepo }
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
	if !exists || cur.Withheld || cur.Entry.Revision != checked.Revision {
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

// Renames reads the file renames of the pinned revision's history, newest first (the newest
// rename of a path wins), and returns old path -> where it went. Merge commits are not read: a
// rename that only a merge shows is simply not applied.
func (r agentMemLintRepo) Renames() (map[string]agentMemLintRename, error) {
	ctx, cancel := context.WithTimeout(context.Background(), agentMemLintGitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "--git-dir="+r.gitDir, "-c", "core.quotePath=false", "log",
		"-n", fmt.Sprint(agentMemLintRenameCommits), "--no-merges", "-M", "--diff-filter=R", "--name-status",
		"--no-color", "--no-ext-diff", "--format=%x01%H", r.ref)
	cmd.Env = append(cmd.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "GIT_NO_REPLACE_OBJECTS=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("git log failed")
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("git log failed")
	}
	out := map[string]agentMemLintRename{}
	commit := ""
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		line := sc.Text()
		if rest, ok := strings.CutPrefix(line, "\x01"); ok {
			commit = rest
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 3 || !strings.HasPrefix(f[0], "R") || strings.HasPrefix(f[1], `"`) || strings.HasPrefix(f[2], `"`) {
			continue
		}
		if _, seen := out[f[1]]; !seen {
			out[f[1]] = agentMemLintRename{To: f[2], Commit: commit}
		}
	}
	for sc.Scan() {
	}
	if err := cmd.Wait(); err != nil || sc.Err() != nil {
		return nil, fmt.Errorf("git log failed")
	}
	return out, nil
}

// agentMemLintResolveRename follows old through the renames to a path that exists in the
// revision; the evidence is the commit of the last hop.
func agentMemLintResolveRename(old string, ren map[string]agentMemLintRename, exists func(string) bool) (agentMemLintRename, bool) {
	cur, hop := old, agentMemLintRename{}
	for i := 0; i < agentMemLintRenameHops; i++ {
		r, ok := ren[cur]
		if !ok {
			return agentMemLintRename{}, false
		}
		cur, hop = r.To, r
		if exists(cur) {
			return hop, true
		}
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
		okPrev := i == 0 || !agentMemLintPathByte(s[i-1], false)
		okNext := end >= len(s) || !agentMemLintPathByte(s[end], true) &&
			!(s[end] == '.' && end+1 < len(s) && agentMemLintPathByte(s[end+1], true)) && s[end] != '/'
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
	if len(byMem) == 0 || len(wire.Report.Skipped) > 0 {
		return 0, nil
	}
	ren, err := repo.Renames()
	if err != nil {
		out.Notes = append(out.Notes, "renames were not read: "+err.Error())
		return 0, nil
	}
	paths, err := repo.Paths()
	if err != nil {
		return 0, nil
	}
	have := make(map[string]bool, len(paths))
	for _, p := range paths {
		have[p] = true
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
			old string
			r   agentMemLintRename
		}
		var hits []hit
		for _, old := range subs {
			if r, ok := agentMemLintResolveRename(old, ren, func(p string) bool { return have[p] }); ok {
				hits = append(hits, hit{old, r})
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
				body, n := agentMemLintReplacePath(cur.Body, h.old, h.r.To)
				if n == 0 {
					continue
				}
				cur.Body, changed = body, true
				for i, a := range cur.Anchors {
					if a == h.old {
						cur.Anchors[i] = h.r.To
					}
				}
				trailers = append(trailers, "AF-Lint-Rename: "+h.old+" -> "+h.r.To+" @ "+h.r.Commit)
			}
			if changed {
				cur.Updated = now.UTC().Format(time.RFC3339)
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
