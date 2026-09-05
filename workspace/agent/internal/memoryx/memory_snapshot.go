package memoryx

// Agent memory version management (docs/log/39 / ADR 0022) - the snapshot itself.
//
//	live ──① allowlist copy──▶ staging ──② git commit──▶ af-memory.git (bare)
//
// Nothing is committed when nothing changed, so empty commits never pollute the history. The
// commit message's trailers carry the trigger (AF-Trigger) and the changed slugs, so the list
// API can be assembled from git log alone.

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// memorySnapshotMu serializes snapshot, and later restore and import. They share the staging
// dir and the bare repo's index, so concurrent runs are not allowed (the trigger loop and the
// manual API would otherwise race).
var memorySnapshotMu sync.Mutex

// Snapshot triggers (the value of the AF-Trigger commit trailer). restore / import are used
// by P2/P3.
const (
	memoryTriggerAuto       = "auto"
	memoryTriggerManual     = "manual"
	memoryTriggerPreRestore = "pre-restore"
	memoryTriggerRestore    = "restore"
	memoryTriggerImport     = "import"
)

// memorySnapshotResult is the outcome of one snapshot run. Committed=false means nothing was
// stacked because nothing changed - a normal result, not an error.
type memorySnapshotResult struct {
	Committed bool               `json:"committed"`
	Rev       string             `json:"rev,omitempty"`
	Trigger   string             `json:"trigger"`
	Files     int                `json:"files"`    // total number of covered files carried in the repo
	Changed   []string           `json:"changed"`  // changed paths inside the repo
	Projects  []memoryProjectRef `json:"projects"` // claude projects that changed
	Kinds     []string           `json:"kinds"`    // kinds of the roots that were covered
}

// memorySnapshot makes one live -> staging -> commit round trip. now comes from the caller:
// time.Now() is never called inside, so tests can verify deterministically (the same style as
// the existing cleanup_archive).
func memorySnapshot(trigger string, now time.Time) (memorySnapshotResult, error) {
	memorySnapshotMu.Lock()
	defer memorySnapshotMu.Unlock()
	return memorySnapshotLocked(trigger, now)
}

// memorySnapshotLocked makes one round trip while holding memorySnapshotMu. trailers are extra
// trailer lines appended after AF-Trigger (restore records the rev it came from and the scope).
func memorySnapshotLocked(trigger string, now time.Time, trailers ...string) (memorySnapshotResult, error) {
	res := memorySnapshotResult{Trigger: trigger, Changed: []string{}, Projects: []memoryProjectRef{}, Kinds: []string{}}
	roots := memoryRoots()
	if len(roots) == 0 {
		return res, nil
	}
	if err := memoryEnsureRepo(); err != nil {
		return res, err
	}
	staging := memoryStagingDir()
	for _, r := range roots {
		n, err := memorySyncToStaging(r, staging)
		if err != nil {
			return res, err
		}
		res.Files += n
		res.Kinds = append(res.Kinds, r.Kind)
	}
	if _, err := memoryGitRun("add", "-A"); err != nil {
		return res, fmt.Errorf("stage memory: %w", err)
	}
	// Stack nothing when the diff is empty (this also holds down repo growth, ★8).
	changed, err := memoryGitRun("diff", "--cached", "--name-only")
	if err != nil {
		return res, fmt.Errorf("inspect staged memory: %w", err)
	}
	if strings.TrimSpace(changed) == "" {
		return res, nil
	}
	for _, p := range strings.Split(changed, "\n") {
		if p = strings.TrimSpace(p); p != "" {
			res.Changed = append(res.Changed, p)
		}
	}
	sort.Strings(res.Changed)
	_, res.Projects, _ = memorySummarizePaths(res.Changed)

	msg := memoryCommitMessage(trigger, now, res.Changed, res.Projects, trailers)
	if _, err := memoryGitRun("commit", "--quiet", "--no-verify", "-m", msg); err != nil {
		return res, fmt.Errorf("commit memory snapshot: %w", err)
	}
	rev, err := memoryGitRun("rev-parse", memoryBranch)
	if err != nil {
		return res, err
	}
	res.Committed, res.Rev = true, rev
	// ★8 repo growth: leave the judgement to git (--auto acts only past its threshold). A
	// failure is swallowed because the snapshot already succeeded - returning it here would
	// read as "stacked, yet failed".
	_, _ = memoryGitRun("gc", "--auto", "--quiet")
	return res, nil
}

// memoryCommitMessage assembles the first-line summary and the trailers (AF-Trigger /
// AF-Changed). The trailers sit together in the final paragraph, the shape
// `git log --pretty=%(trailers:...)` can pick up.
func memoryCommitMessage(trigger string, now time.Time, changed []string, projects []memoryProjectRef, trailers []string) string {
	// The verb on the first line makes the reason it was stacked readable from the head of the
	// list; the details live in the trailers.
	verb := "snapshot"
	switch trigger {
	case memoryTriggerRestore:
		verb = "restore"
	case memoryTriggerImport:
		verb = "import"
	}
	var subject string
	switch {
	case len(projects) == 1:
		subject = fmt.Sprintf("%s: %s (%s)", verb, now.Format(time.RFC3339), projects[0].Display)
	case len(projects) > 1:
		subject = fmt.Sprintf("%s: %s (%d projects changed)", verb, now.Format(time.RFC3339), len(projects))
	default:
		subject = fmt.Sprintf("%s: %s (%d files changed)", verb, now.Format(time.RFC3339), len(changed))
	}
	var b strings.Builder
	b.WriteString(subject)
	b.WriteString("\n\n")
	b.WriteString("AF-Trigger: " + trigger + "\n")
	for _, t := range trailers {
		if t = strings.TrimSpace(t); t != "" {
			b.WriteString(t + "\n")
		}
	}
	for _, p := range projects {
		b.WriteString("AF-Changed: " + p.Slug + "\n")
	}
	return b.String()
}
