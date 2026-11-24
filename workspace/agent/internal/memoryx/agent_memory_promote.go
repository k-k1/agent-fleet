package memoryx

// The read-only list of promotion candidates (ADR 0130 decision 11): which memories of a project
// have proven themselves enough to be proposed to the repository's own documents. The selection is
// memorypromote's; this file feeds it the project's memories, their feedback records, the lint
// result over the default branch and a positive check of every stored anchor on it.
//
// Nothing here writes: no memory, no sidecar, no use is counted, no working copy is touched and
// nothing is fetched (lint reads the repository by ref with lazy fetching off). Proposing a text
// and superseding a memory are separate steps.
//
// When in doubt a memory is held, never listed: a candidate is what a member may copy into a
// shared repository, so every signal has to be read, not assumed.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memorylint"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memorypromote"
)

const (
	agentMemPromoteMaxDays = 90
	// agentMemPromoteHistoryMax bounds how many published changes of one memory are followed back
	// to where its revision number began; past it the memory is held.
	agentMemPromoteHistoryMax = 100
)

// agentMemPromoteWire is what `af-memory promote-candidates` reads.
type agentMemPromoteWire struct {
	Project *agentMemProject `json:"project,omitempty"`
	// Ref and Commit say which revision of the default branch the anchors were checked on; empty
	// when the repository could not be read, and then nothing is a candidate.
	Ref         string `json:"ref,omitempty"`
	Commit      string `json:"commit,omitempty"`
	MinSessions int    `json:"minSessions"`
	QuietDays   int    `json:"quietDays"`
	// Withheld counts files left out because they failed the scan or cannot be parsed.
	Withheld int `json:"withheld,omitempty"`
	// Skipped says why the anchors could not be checked, so an empty list is never taken for
	// "nothing has proven itself".
	Skipped []string `json:"skipped,omitempty"`
	memorypromote.Result
}

// agentMemLintLoadDocStrict reads the retired-term list once and says whether it could be. A
// missing file is an empty list. An unreadable file, or one that is not an object with a "terms"
// array (what the save route always writes), is an error: the lint report shows such a list as
// empty, but promotion must not, or a retired term would pass unseen.
func agentMemLintLoadDocStrict() (agentMemLintTermsDoc, error) {
	d := agentMemLintTermsDoc{Terms: []string{}, Rejected: []string{}}
	b, ok, err := agentMemReadFile(agentMemLintTermsPath())
	if err != nil || !ok {
		return d, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil || raw == nil {
		return d, errors.New("not a JSON object")
	}
	var f agentMemLintTermsDoc
	if t := bytes.TrimSpace(raw["terms"]); len(t) == 0 || t[0] != '[' {
		return d, errors.New("no terms array")
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return d, err
	}
	d.Terms = f.Terms
	if f.Rejected != nil {
		d.Rejected = f.Rejected
	}
	return d, nil
}

// agentMemPromoteDefaultKnown says whether the ref lint read is the one origin/HEAD names. Lint
// falls back to origin/main or origin/master when origin/HEAD is not set, which is right for a
// report but is a guess, and an anchor present only on a branch that is not the default must not
// promote a memory.
func agentMemPromoteDefaultKnown(repo memorylint.Repo, ref string) bool {
	r, ok := repo.(agentMemLintRepo)
	if !ok || ref == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), agentMemLintGitTimeout)
	defer cancel()
	out, err := agentMemLintGit(ctx, r.gitDir, nil, "symbolic-ref", "-q", "refs/remotes/origin/HEAD")
	return err == nil && strings.TrimSpace(string(out)) == "refs/remotes/"+ref
}

// agentMemPromoteGit reads the store's history straight from its bare repository. memoryGit is
// not used: it creates the staging directory it runs in, and this list writes nothing.
func agentMemPromoteGit(args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"--git-dir=" + memoryRepoDir()}, args...)...)
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GIT_DIR=") && !strings.HasPrefix(e, "GIT_WORK_TREE=") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_OPTIONAL_LOCKS=0",
		"GIT_TERMINAL_PROMPT=0", "GIT_NO_LAZY_FETCH=1", "GIT_NO_REPLACE_OBJECTS=1")
	return cmd.Output()
}

// agentMemPromoteHead is the commit of the store's history, read under agentMemMu so that it
// matches the files read under the same lock; "" when there is no history.
func agentMemPromoteHead() string {
	if _, err := os.Stat(memoryRepoDir()); err != nil {
		return ""
	}
	out, err := agentMemPromoteGit("rev-parse", "--verify", "--quiet", memoryBranch+"^{commit}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// agentMemPromoteSnap is what the history check of one memory needs, taken under the lock.
type agentMemPromoteSnap struct {
	idx      int // into the memories handed to Select
	fileRel  string
	file     []byte
	revision int
	body     string
}

// agentMemPromoteHistory checks one memory against the store's history at head, outside the lock:
// when its file last changed (the frontmatter's updated stamp does not move on a lint reason write,
// which still makes a new revision), whether the file is the one that change published (an edit
// outside AF is not), and whether the change before it agrees with the revision (a revision line
// broken by hand reads as revision 1; republished by a pin, it would let revision 1's
// confirmations vouch for a later text).
func agentMemPromoteHistory(head string, s agentMemPromoteSnap) (at time.Time, miss []memorypromote.Miss) {
	unpublished := func(detail string) []memorypromote.Miss {
		return []memorypromote.Miss{{Rule: memorypromote.RuleUnpublished, Detail: detail}}
	}
	if head == "" {
		return time.Time{}, unpublished("no published change")
	}
	path := agentMemRepoPrefix + "/" + s.fileRel
	out, err := agentMemPromoteGit("log", "-n", strconv.Itoa(agentMemPromoteHistoryMax+1), "--no-renames", "--diff-filter=AM",
		"--format=%H %cI", head, "--", path)
	if err != nil {
		return time.Time{}, unpublished("the history could not be read")
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	commit, stamp, _ := strings.Cut(lines[0], " ")
	if commit == "" {
		return time.Time{}, unpublished("no published change")
	}
	if at, err = time.Parse(time.RFC3339, stamp); err != nil {
		return time.Time{}, unpublished("the history could not be read")
	}
	blob, err := agentMemPromoteGit("cat-file", "blob", commit+":"+path)
	if err != nil || !bytes.Equal(blob, s.file) {
		return at, unpublished("the file differs from its last published change")
	}
	// Back through every published change that carries the current revision number (a pin or a
	// feedback mark republishes the file without a new revision): each must hold the same text,
	// and the change before them a lower revision. Comparing only the last two would lose a broken
	// revision line after two pins.
	for _, ln := range lines[1:] {
		prev, _, _ := strings.Cut(ln, " ")
		pb, err := agentMemPromoteGit("cat-file", "blob", prev+":"+path)
		if err != nil {
			return at, unpublished("the history could not be read")
		}
		pe, ok := agentMemParse(pb)
		switch {
		case !ok || pe.Revision > s.revision || pe.Revision == s.revision && pe.Body != s.body:
			return at, unpublished("the revision does not follow the change before it")
		case pe.Revision < s.revision:
			return at, nil
		}
	}
	if len(lines) > agentMemPromoteHistoryMax {
		return at, unpublished("the revision's history is too long to follow")
	}
	return at, nil
}

// agentMemPromoteCandidates lists the candidates of one project. Read-only.
func agentMemPromoteCandidates(project string, quietDays int, now time.Time) (agentMemPromoteWire, error) {
	out := agentMemPromoteWire{MinSessions: memorypromote.DefaultMinSessions, QuietDays: quietDays}
	// One read of the term list serves the lint run and the check that it was read.
	terms, termsErr := agentMemLintLoadDocStrict()
	lint, es, c, repo, err := agentMemLintCollectDoc(agentMemScopeProject, project, nil, &terms)
	if err != nil {
		return out, err
	}
	out.Project, out.Ref, out.Commit, out.Withheld = lint.Project, lint.Ref, lint.Commit, lint.Withheld

	// Every source of "unchecked" ends in one reason; the first one found is enough to hold all.
	for _, s := range lint.Report.Skipped {
		if strings.HasPrefix(s, "repository checks") || strings.HasPrefix(s, "symbol checks") {
			out.Skipped = append(out.Skipped, s)
		}
	}
	var missing map[string][]string
	switch {
	case len(out.Skipped) > 0:
	case repo == nil:
		out.Skipped = append(out.Skipped, "repository checks: the default branch could not be read")
	case !agentMemPromoteDefaultKnown(repo, lint.Ref):
		out.Skipped = append(out.Skipped, "anchor checks: origin/HEAD does not name the default branch, so "+lint.Ref+
			" is only a guess (`git remote set-head origin --auto` sets it)")
	}
	if termsErr != nil {
		out.Skipped = append(out.Skipped, "retired-term checks: the list could not be read")
	}
	if len(out.Skipped) == 0 {
		mems := make([]memorylint.Memory, 0, len(es))
		for _, e := range es {
			mems = append(mems, memorylint.Memory{Name: e.Name, Anchors: e.Anchors})
		}
		if missing, err = memorylint.VerifyAnchors(mems, memorylint.Options{}, repo); err != nil {
			out.Skipped = append(out.Skipped, "anchor checks: "+err.Error())
		}
	}
	unverified := strings.Join(out.Skipped, "; ")

	findings := map[string][]string{}
	for _, f := range lint.Report.Findings {
		findings[f.Memory] = append(findings[f.Memory], f.Check)
	}
	checked := make(map[string]agentMemEntry, len(es))
	for _, e := range es {
		checked[e.Name] = e
	}
	rel, err := agentMemScopeDir(agentMemScopeProject, c)
	if err != nil {
		return out, err
	}

	// The entries, their records, the link targets and the head of the store's history are read
	// again under one read lock, so a reason, a confirmation and a revision are judged together; a
	// memory that moved since the lint read it is held, never judged on a mix of the two. The
	// history itself is read after the lock, at that head.
	agentMemMu.RLock()
	cur, _, err := agentMemLoadDir(agentMemScopeProject, rel)
	if err != nil {
		agentMemMu.RUnlock()
		return out, err
	}
	names := make(map[string]bool, len(cur))
	for _, e := range cur {
		names[e.Name] = true
	}
	mems := make([]memorypromote.Memory, 0, len(cur))
	var snaps []agentMemPromoteSnap
	for _, e := range cur {
		if !memorypromote.Promotable(e.MemKind) || agentMemExcluded(e) {
			continue
		}
		m := memorypromote.Memory{
			Name: e.Name, Kind: e.MemKind, Type: e.Type, Revision: e.Revision,
			Anchors: e.Anchors, Reasons: len(e.Reasons), Findings: findings[e.Name], Missing: missing[e.Name],
		}
		if t, err := time.Parse(time.RFC3339, e.Updated); err == nil {
			m.Updated = t
		}
		for _, r := range agentMemFeedbackRecords(rel, e.Name) {
			m.Reports = append(m.Reports, memorypromote.Confirmation{Session: r.Session, Revision: r.Revision, Verdict: r.Verdict})
		}
		moved := false
		if was, ok := checked[e.Name]; !ok || was.Revision != e.Revision || !agentMemReasonsEqual(was.Reasons, e.Reasons) ||
			!slicesEqual(was.Anchors, e.Anchors) || was.Body != e.Body {
			moved = true
		}
		// Lint judged the links (anchors and body) against the names it loaded; a target that was
		// there then and is gone now makes that judgement stale. One missing both times is a lint
		// finding or an anchor not found, not a move.
		targets := memorylint.Links(e.Body)
		for _, a := range e.Anchors {
			if t, ok := agentMemPromoteLink(a); ok {
				targets = append(targets, t)
			}
		}
		for _, t := range targets {
			if _, was := checked[t]; was && !names[t] {
				moved = true
			}
		}
		if moved {
			m.Also = append(m.Also, memorypromote.Miss{Rule: memorypromote.RuleMoved})
		}
		// The history is read only for a memory every other signal already admits.
		if unverified == "" && !moved && m.Reasons == 0 && len(m.Anchors) > 0 && len(m.Missing) == 0 &&
			len(m.Findings) == 0 && m.Type != "user" && memorypromote.Sessions(m.Reports, m.Revision) >= out.MinSessions {
			// The bytes compared with the history must be the ones judged: a process that edits the
			// store directly is not held off by the lock, so they are parsed again and must agree.
			fileRel := rel + "/" + e.Name + ".md"
			b, ok, err := agentMemReadFile(filepath.Join(agentMemDir(), filepath.FromSlash(fileRel)))
			pe, parsed := agentMemParse(b)
			if err != nil || !ok || !parsed || !agentMemPromoteSameEntry(pe, e) {
				m.Also = append(m.Also, memorypromote.Miss{Rule: memorypromote.RuleMoved})
			} else {
				snaps = append(snaps, agentMemPromoteSnap{idx: len(mems), fileRel: fileRel, file: b, revision: e.Revision, body: e.Body})
			}
		}
		mems = append(mems, m)
	}
	head := ""
	if len(snaps) > 0 {
		head = agentMemPromoteHead()
	}
	agentMemMu.RUnlock()

	for _, s := range snaps {
		at, miss := agentMemPromoteHistory(head, s)
		m := &mems[s.idx]
		m.Also = append(m.Also, miss...)
		if at.After(m.Updated) {
			m.Updated = at
		}
	}
	out.Result = memorypromote.Select(mems, memorypromote.Options{
		Now: now, Quiet: time.Duration(quietDays) * 24 * time.Hour, Unverified: unverified,
	})
	return out, nil
}

// agentMemPromoteSameEntry says whether two reads of a memory agree on everything selection uses.
func agentMemPromoteSameEntry(a, b agentMemEntry) bool {
	return a.Revision == b.Revision && a.Type == b.Type && a.Body == b.Body && a.MemKind == b.MemKind &&
		slicesEqual(a.Anchors, b.Anchors) && agentMemReasonsEqual(a.Reasons, b.Reasons) &&
		agentMemExcluded(a) == agentMemExcluded(b)
}

// agentMemPromoteLink is the target of an anchor that is a whole [[link]].
func agentMemPromoteLink(a string) (string, bool) {
	a = strings.TrimSpace(a)
	if !strings.HasPrefix(a, "[[") || !strings.HasSuffix(a, "]]") {
		return "", false
	}
	return strings.TrimSpace(a[2 : len(a)-2]), true
}

func slicesEqual(a, b []string) bool {
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

func agentMemPromoteBad(format string, args ...any) error {
	return memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, format, args...)
}

// HandleAgentMemoryPromoteCandidates serves the list for one project. Not gated by the switch,
// writes nothing, counts no use: like the lint report it is the member's view of the store.
func HandleAgentMemoryPromoteCandidates(w http.ResponseWriter, r *http.Request) {
	// A query that does not parse is refused rather than read with the bad pair dropped.
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		agentMemWriteErr(w, agentMemPromoteBad("the query does not parse"))
		return
	}
	for k, vs := range q {
		if len(vs) > 1 {
			agentMemWriteErr(w, agentMemPromoteBad("%s is given more than once", k))
			return
		}
	}
	if s := q.Get("scope"); q.Has("scope") && s != agentMemScopeProject {
		agentMemWriteErr(w, agentMemPromoteBad("promotion candidates are per project: scope must be %q or left out", agentMemScopeProject))
		return
	}
	if q.Get("project") == "" {
		agentMemWriteErr(w, agentMemPromoteBad("project is required"))
		return
	}
	days := int(memorypromote.DefaultQuiet / (24 * time.Hour))
	if q.Has("days") {
		v := q.Get("days")
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > agentMemPromoteMaxDays || strings.TrimLeft(v, "0123456789") != "" {
			agentMemWriteErr(w, agentMemPromoteBad("days is a whole number from 1 to %d", agentMemPromoteMaxDays))
			return
		}
		days = n
	}
	out, err := agentMemPromoteCandidates(q.Get("project"), days, time.Now())
	if err != nil {
		agentMemWriteErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
