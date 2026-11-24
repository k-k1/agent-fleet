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
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memorylint"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memorypromote"
)

const agentMemPromoteMaxDays = 90

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

// agentMemLintTermsErr says whether the retired-term list can be read. A missing file is an
// empty list; an unreadable or malformed one is an error, which the lint report shows as an empty
// list but promotion must not: a retired term would then pass unseen.
func agentMemLintTermsErr() error {
	b, ok, err := agentMemReadFile(agentMemLintTermsPath())
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	var f agentMemLintTermsDoc
	return json.Unmarshal(b, &f)
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

// agentMemPromotePublished answers when the memory's file last changed in the store's history and
// whether the file is byte-identical to that change. The frontmatter's updated stamp is not
// enough: a lint reason write moves the revision but not the stamp, and a file edited outside AF
// carries whatever it says. Called with agentMemMu held for reading.
func agentMemPromotePublished(fileRel string) (at time.Time, same bool) {
	if !memoryHasCommits() {
		return time.Time{}, false
	}
	commit, err := agentMemLatestChange(fileRel)
	if err != nil || commit == "" {
		return time.Time{}, false
	}
	stamp, err := memoryGitRun("log", "-1", "--format=%cI", commit)
	if err != nil {
		return time.Time{}, false
	}
	if at, err = time.Parse(time.RFC3339, strings.TrimSpace(stamp)); err != nil {
		return time.Time{}, false
	}
	blob, err := memoryGit("cat-file", "blob", commit+":"+agentMemRepoPrefix+"/"+fileRel).Output()
	if err != nil {
		return at, false
	}
	cur, ok, err := agentMemReadFile(filepath.Join(agentMemDir(), filepath.FromSlash(fileRel)))
	return at, err == nil && ok && bytes.Equal(blob, cur)
}

// agentMemPromoteCandidates lists the candidates of one project. Read-only.
func agentMemPromoteCandidates(project string, quietDays int, now time.Time) (agentMemPromoteWire, error) {
	out := agentMemPromoteWire{MinSessions: memorypromote.DefaultMinSessions, QuietDays: quietDays}
	lint, es, c, repo, err := agentMemLintCollect(agentMemScopeProject, project)
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
	if err := agentMemLintTermsErr(); err != nil {
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

	// The entries, their records and their published history are read again under one read lock,
	// so a reason, a confirmation and a revision are judged together; a memory that moved since
	// the lint read it is held, never judged on a mix of the two.
	agentMemMu.RLock()
	defer agentMemMu.RUnlock()
	cur, _, err := agentMemLoadDir(agentMemScopeProject, rel)
	if err != nil {
		return out, err
	}
	mems := make([]memorypromote.Memory, 0, len(cur))
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
		if was, ok := checked[e.Name]; !ok || was.Revision != e.Revision || !agentMemReasonsEqual(was.Reasons, e.Reasons) ||
			!slicesEqual(was.Anchors, e.Anchors) || was.Body != e.Body {
			m.Also = append(m.Also, memorypromote.Miss{Rule: memorypromote.RuleMoved})
		}
		// The history is read only for a memory every cheaper signal already admits.
		if unverified == "" && len(m.Also) == 0 && m.Reasons == 0 && len(m.Anchors) > 0 && len(m.Missing) == 0 &&
			len(m.Findings) == 0 && m.Type != "user" && memorypromote.Sessions(m.Reports, m.Revision) >= out.MinSessions {
			at, same := agentMemPromotePublished(rel + "/" + e.Name + ".md")
			if !same {
				m.Also = append(m.Also, memorypromote.Miss{Rule: memorypromote.RuleUnpublished})
			}
			if at.After(m.Updated) {
				m.Updated = at
			}
		}
		mems = append(mems, m)
	}
	out.Result = memorypromote.Select(mems, memorypromote.Options{
		Now: now, Quiet: time.Duration(quietDays) * 24 * time.Hour, Unverified: unverified,
	})
	return out, nil
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
