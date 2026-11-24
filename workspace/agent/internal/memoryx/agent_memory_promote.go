package memoryx

// The read-only list of promotion candidates (ADR 0130 decision 11): which memories of a project
// have proven themselves enough to be proposed to the repository's own documents. The selection is
// memorypromote's; this file feeds it the project's memories, their feedback records and the lint
// result over the default branch, which is what says the anchors exist.
//
// Nothing here writes: no memory, no sidecar, no use is counted, no working copy is touched (lint
// reads the repository by ref). Proposing a text and superseding a memory are separate steps.

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
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

// agentMemPromoteCandidates lists the candidates of one project. Read-only.
func agentMemPromoteCandidates(project string, quietDays int, now time.Time) (agentMemPromoteWire, error) {
	out := agentMemPromoteWire{MinSessions: memorypromote.DefaultMinSessions, QuietDays: quietDays}
	lint, es, c, repo, err := agentMemLintCollect(agentMemScopeProject, project)
	if err != nil {
		return out, err
	}
	out.Project, out.Ref, out.Commit, out.Withheld = lint.Project, lint.Ref, lint.Commit, lint.Withheld
	// The anchors count as present on the default branch only when lint read it and every check
	// over it ran; a timed-out symbol search must not read as "every symbol exists".
	checked := repo != nil
	for _, s := range lint.Report.Skipped {
		if strings.HasPrefix(s, "repository checks") || strings.HasPrefix(s, "symbol checks") {
			checked = false
			out.Skipped = append(out.Skipped, s)
		}
	}
	findings := map[string][]string{}
	for _, f := range lint.Report.Findings {
		findings[f.Memory] = append(findings[f.Memory], f.Check)
	}
	rel, err := agentMemScopeDir(agentMemScopeProject, c)
	if err != nil {
		return out, err
	}
	mems := make([]memorypromote.Memory, 0, len(es))
	// Records are appended under the store lock; reading them under it gives each memory one
	// consistent list. A revision that moved since the lint load only makes its old records
	// count for the revision shown, which is the one a later supersede has to match.
	agentMemMu.RLock()
	for _, e := range es {
		if !memorypromote.Promotable(e.MemKind) || agentMemExcluded(e) {
			continue
		}
		m := memorypromote.Memory{
			Name: e.Name, Kind: e.MemKind, Type: e.Type, Revision: e.Revision,
			Anchors: e.Anchors, Reasons: len(e.Reasons), Findings: findings[e.Name],
		}
		if t, err := time.Parse(time.RFC3339, e.Updated); err == nil {
			m.Updated = t
		}
		for _, r := range agentMemFeedbackRecords(rel, e.Name) {
			m.Reports = append(m.Reports, memorypromote.Confirmation{Session: r.Session, Revision: r.Revision, Verdict: r.Verdict})
		}
		mems = append(mems, m)
	}
	agentMemMu.RUnlock()
	out.Result = memorypromote.Select(mems, memorypromote.Options{
		Now: now, Quiet: time.Duration(quietDays) * 24 * time.Hour, RepoChecked: checked,
	})
	return out, nil
}

// HandleAgentMemoryPromoteCandidates serves the list for one project. Not gated by the switch,
// writes nothing, counts no use: like the lint report it is the member's view of the store.
func HandleAgentMemoryPromoteCandidates(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	days := int(memorypromote.DefaultQuiet / (24 * time.Hour))
	if v := q.Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > agentMemPromoteMaxDays {
			agentMemWriteErr(w, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "days is a whole number from 1 to %d", agentMemPromoteMaxDays))
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
