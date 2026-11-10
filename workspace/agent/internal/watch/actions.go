package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The GitHub Actions source of ADR 0133 §9 ("CI on its own"): a workflow run that is not tied
// to a pull request. It is its own source, github_actions, so a pull request reference stays
// what it always was, and the event types of each source are checked by the engine as for any
// other source (a run cannot be asked for pr.merged). It reads through the GitHub source, so the
// token, the conditional requests and the 10% share of the hourly limit are the same ones.
//
// Two subjects:
//
//	run       github:o/r/actions/runs/<id>[/attempts/<n>]
//	workflow  github:o/r/actions/workflows/<file>/after/<floor id>-<floor unix>[/branch/<b>]
//
// A workflow watch is for the NEXT run, so its subject carries the floor that separates old
// runs from new ones. The floor is taken from GitHub's own answer when the watch is made (the
// highest run id and the newest creation time it listed, or its Date header when the workflow
// has no run yet), never from this host's clock: a skewed local clock cannot make an old run
// look new. Run ids only grow, so id > floor is the rule; the creation time is a second guard.
// A floor in the subject also means watches made while the workflow is quiet share one read.

// Event types of the Actions source (provider-prefixed, ADR 0133 §3).
const (
	EventRunCompleted = "github.workflow_run.completed"
	EventRunFailed    = "github.workflow_run.failed"
)

// ActionsKind is the Actions source's name.
const ActionsKind = "github_actions"

const (
	actionsBusyAfter   = 15 * time.Second // a run is in progress
	actionsQueuedAfter = 30 * time.Second // queued, waiting for approval or a runner
	actionsIdleAfter   = 2 * time.Minute  // a workflow with no run in flight; a 304 is free
	// actionsListSize is how many runs one read of a workflow lists: those created after the
	// floor, newest first. A workflow that starts more than this between two reads is not paged.
	actionsListSize = 20
)

// GitHubActions watches workflow runs.
type GitHubActions struct {
	GH *GitHub
}

// NewGitHubActions returns the Actions source over gh.
func NewGitHubActions(gh *GitHub) *GitHubActions { return &GitHubActions{GH: gh} }

func (a *GitHubActions) Kind() string    { return ActionsKind }
func (a *GitHubActions) Types() []string { return []string{EventRunCompleted, EventRunFailed} }

const (
	actRepo = `[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+`
	actFile = `[A-Za-z0-9_.-]+\.ya?ml`
)

var (
	actRunURLRE = regexp.MustCompile(`^https://github\.com/(` + actRepo + `)/actions/runs/([1-9][0-9]{0,17})(?:/attempts/([1-9][0-9]{0,3}))?(?:[/?#].*)?$`)
	actWfURLRE  = regexp.MustCompile(`^https://github\.com/(` + actRepo + `)/actions/workflows/(` + actFile + `)(?:[?#].*)?$`)
	actRunRE    = regexp.MustCompile(`^(?:(` + actRepo + `):)?run/([1-9][0-9]{0,17})(?:/attempts/([1-9][0-9]{0,3}))?$`)
	actWfRE     = regexp.MustCompile(`^(?:(` + actRepo + `):)?(` + actFile + `)(?:@([A-Za-z0-9._/-]{1,100}))?$`)

	actRunSubjectRE = regexp.MustCompile(`^github:(` + actRepo + `)/actions/runs/([1-9][0-9]{0,17})(?:/attempts/([1-9][0-9]{0,3}))?$`)
	actWfSubjectRE  = regexp.MustCompile(`^github:(` + actRepo + `)/actions/workflows/(` + actFile + `)/after/([0-9]{1,18})-([0-9]{1,12})(?:/branch/([A-Za-z0-9._/-]{1,100}))?$`)
)

// actionsRef is a parsed reference: a run (runID set) or a workflow (file set).
type actionsRef struct {
	repo    string
	runID   int64
	attempt int
	file    string
	branch  string
}

// LooksLikeActionsRef reports whether ref is in one of the Actions forms, so the watch handler
// can send it to this source when the caller left the source at its github default. A pull
// request reference never matches: those are owner/repo#N, #N, N and /pull/ URLs.
func LooksLikeActionsRef(ref string) bool {
	_, ok := matchActionsRef(strings.TrimSpace(ref), "x/x")
	return ok
}

func matchActionsRef(ref, defaultRepo string) (actionsRef, bool) {
	if m := actRunURLRE.FindStringSubmatch(ref); m != nil {
		return actionsRef{repo: m[1], runID: atoi64(m[2]), attempt: atoi(m[3])}, true
	}
	if m := actWfURLRE.FindStringSubmatch(ref); m != nil {
		return actionsRef{repo: m[1], file: m[2]}, true
	}
	if m := actRunRE.FindStringSubmatch(ref); m != nil {
		return actionsRef{repo: orDefault(m[1], defaultRepo), runID: atoi64(m[2]), attempt: atoi(m[3])}, true
	}
	if m := actWfRE.FindStringSubmatch(ref); m != nil {
		return actionsRef{repo: orDefault(m[1], defaultRepo), file: m[2], branch: m[3]}, true
	}
	return actionsRef{}, false
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func atoi64(s string) int64 { n, _ := strconv.ParseInt(s, 10, 64); return n }

const actionsRefHelp = "give a workflow run as its https://github.com/o/r/actions/runs/<id> URL (or o/r:run/<id>, optionally /attempts/<n>), " +
	"or the next run of a workflow as o/r:<file>.yml, optionally with a branch as o/r:<file>.yml@<branch>"

func parseActionsRef(ref, defaultRepo string) (actionsRef, error) {
	r, ok := matchActionsRef(strings.TrimSpace(ref), defaultRepo)
	if !ok {
		return actionsRef{}, &ErrRefused{actionsRefHelp}
	}
	if r.repo == "" {
		return actionsRef{}, &ErrRefused{"this form needs a GitHub repository in this session's working copy; give o/r:" + orDefault(r.file, "run/<id>")}
	}
	if !validRepo(r.repo) {
		return actionsRef{}, &ErrRefused{actionsRefHelp}
	}
	r.repo = strings.ToLower(r.repo) // one spelling, one subject
	if r.branch != "" && !validBranch(r.branch) {
		return actionsRef{}, &ErrRefused{"that branch name is not accepted; use letters, digits and . _ - / only"}
	}
	return r, nil
}

// validBranch keeps a branch to the header token set, which the subject is written in.
func validBranch(b string) bool {
	return b != "" && !strings.Contains(b, "..") && !strings.HasPrefix(b, "/") && !strings.HasSuffix(b, "/") &&
		!strings.Contains(b, "//") && !strings.HasPrefix(b, "-")
}

func runSubject(repo string, id int64, attempt int) SubjectID {
	s := fmt.Sprintf("github:%s/actions/runs/%d", repo, id)
	if attempt > 0 {
		s += fmt.Sprintf("/attempts/%d", attempt)
	}
	return SubjectID(s)
}

func workflowSubject(repo, file, branch string, floorID, floorUnix int64) SubjectID {
	s := fmt.Sprintf("github:%s/actions/workflows/%s/after/%d-%d", repo, file, floorID, floorUnix)
	if branch != "" {
		s += "/branch/" + branch
	}
	return SubjectID(s)
}

// ghRun is the part of a workflow run this source uses.
type ghRun struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	RunNumber  int    `json:"run_number"`
	RunAttempt int    `json:"run_attempt"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	HTMLURL    string `json:"html_url"`
	CreatedAt  string `json:"created_at"`
}

type ghRunList struct {
	Runs []ghRun `json:"workflow_runs"`
}

type ghJobList struct {
	Jobs []struct {
		Name       string `json:"name"`
		Conclusion string `json:"conclusion"`
		HTMLURL    string `json:"html_url"`
	} `json:"jobs"`
}

// actionsCursor is what one Poll keeps for the next: each read's ETag and last answer.
type actionsCursor struct {
	Run  ghCached[ghRun]     `json:"run"`
	List ghCached[ghRunList] `json:"list"`
	Jobs ghCached[ghJobList] `json:"jobs"`
}

func runPath(repo string, id int64, attempt int) string {
	if attempt > 0 {
		return fmt.Sprintf("/repos/%s/actions/runs/%d/attempts/%d", repo, id, attempt)
	}
	return fmt.Sprintf("/repos/%s/actions/runs/%d", repo, id)
}

// workflowRunsPath lists a workflow's runs, newest first. With a floor it asks only for runs
// created at or after it, so a quiet workflow answers with a short (or empty) page.
func workflowRunsPath(repo, file, branch string, size int, floorUnix int64) string {
	p := fmt.Sprintf("/repos/%s/actions/workflows/%s/runs?per_page=%d", repo, file, size)
	if branch != "" {
		p += "&branch=" + url.QueryEscape(branch)
	}
	if floorUnix > 0 {
		p += "&created=" + url.QueryEscape(">="+time.Unix(floorUnix, 0).UTC().Format("2006-01-02T15:04:05Z"))
	}
	return p
}

func (a *GitHubActions) Resolve(ctx context.Context, ref, defaultRepo string) (Resolved, error) {
	r, err := parseActionsRef(ref, defaultRepo)
	if err != nil {
		return Resolved{}, err
	}
	if r.runID != 0 {
		var run ghRun
		status, _, err := a.GH.get(ctx, runPath(r.repo, r.runID, r.attempt), "", &run)
		switch {
		case err != nil:
			return Resolved{}, err
		case status == http.StatusNotFound || status == http.StatusForbidden:
			return Resolved{}, &ErrRefused{fmt.Sprintf("%s run %d is not a workflow run your GitHub connection can read", r.repo, r.runID)}
		case status != http.StatusOK:
			return Resolved{}, fmt.Errorf("GitHub answered %d", status)
		}
		return Resolved{Subject: runSubject(r.repo, r.runID, r.attempt), URL: SafeURL(run.HTMLURL)}, nil
	}
	var list ghRunList
	status, _, hdr, err := a.GH.getHdr(ctx, workflowRunsPath(r.repo, r.file, r.branch, 5, 0), "", &list)
	switch {
	case err != nil:
		return Resolved{}, err
	case status == http.StatusNotFound || status == http.StatusForbidden:
		return Resolved{}, &ErrRefused{fmt.Sprintf("%s is not a workflow file in a repository your GitHub connection can read", r.repo+"/"+r.file)}
	case status != http.StatusOK:
		return Resolved{}, fmt.Errorf("GitHub answered %d", status)
	}
	floorID, floorUnix := floorOf(list.Runs)
	if floorUnix == 0 && floorID == 0 {
		// No run yet: GitHub's own clock, from the answer, is the floor.
		if d, err := http.ParseTime(hdr.Get("Date")); err == nil {
			floorUnix = d.Unix()
		}
	}
	subj := workflowSubject(r.repo, r.file, r.branch, floorID, floorUnix)
	if !TokenOK(string(subj)) {
		return Resolved{}, &ErrRefused{"that reference is too long"}
	}
	return Resolved{Subject: subj, URL: SafeURL("https://github.com/" + r.repo + "/actions/workflows/" + r.file)}, nil
}

// floorOf is the highest run id and the newest creation time (unix seconds) among runs.
func floorOf(runs []ghRun) (id, unix int64) {
	for _, r := range runs {
		id = max(id, r.ID)
		if t, err := time.Parse(time.RFC3339, r.CreatedAt); err == nil {
			unix = max(unix, t.Unix())
		}
	}
	return id, unix
}

// actionsGet reads path with the cursor's ETag and keeps the answer on a 200. A subject that is
// gone or no longer readable is ErrGone, so the watch ends with a reason.
func actionsGet[T any](g *GitHub, ctx context.Context, path string, c *ghCached[T], gone string) error {
	etag := ""
	if c.Key == path {
		etag = c.ETag
	}
	var v T
	status, tag, err := g.get(ctx, path, etag, &v)
	if err != nil {
		return err
	}
	switch status {
	case http.StatusNotModified:
		return nil
	case http.StatusOK:
		c.Key, c.ETag, c.Val = path, tag, v
		return nil
	case http.StatusNotFound, http.StatusForbidden, http.StatusGone:
		return &ErrGone{gone}
	}
	return fmt.Errorf("GitHub answered %d for %s", status, path)
}

func (a *GitHubActions) Poll(ctx context.Context, subject SubjectID, raw json.RawMessage, _ []string) ([]Event, json.RawMessage, Hint, error) {
	var cur actionsCursor
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &cur)
	}
	if m := actRunSubjectRE.FindStringSubmatch(string(subject)); m != nil {
		return a.pollRun(ctx, subject, m[1], atoi64(m[2]), atoi(m[3]), &cur, raw)
	}
	if m := actWfSubjectRE.FindStringSubmatch(string(subject)); m != nil {
		return a.pollWorkflow(ctx, subject, m[1], m[2], m[5], atoi64(m[3]), atoi64(m[4]), &cur, raw)
	}
	return nil, raw, Hint{}, fmt.Errorf("not a github_actions subject: %s", subject)
}

const goneRun = "the workflow run was deleted or can no longer be read with your GitHub connection"

func (a *GitHubActions) pollRun(ctx context.Context, subject SubjectID, repo string, id int64, attempt int, cur *actionsCursor, raw json.RawMessage) ([]Event, json.RawMessage, Hint, error) {
	if err := actionsGet(a.GH, ctx, runPath(repo, id, attempt), &cur.Run, goneRun); err != nil {
		return nil, raw, Hint{}, err
	}
	run := cur.Run.Val
	label := fmt.Sprintf("Workflow run #%d (%s run %d)", run.RunNumber, repo, run.ID)
	evs, err := a.runEvents(ctx, subject, repo, label, run, cur)
	if err != nil {
		return nil, raw, Hint{}, err
	}
	out, _ := json.Marshal(cur)
	if run.Status == "completed" {
		// A re-run of the run (a new attempt) can still follow, so keep reading, rarely.
		return evs, out, Hint{After: ghSettledAfter, Settled: true}, nil
	}
	return evs, out, Hint{After: runWait(run.Status)}, nil
}

func (a *GitHubActions) pollWorkflow(ctx context.Context, subject SubjectID, repo, file, branch string, floorID, floorUnix int64, cur *actionsCursor, raw json.RawMessage) ([]Event, json.RawMessage, Hint, error) {
	path := workflowRunsPath(repo, file, branch, actionsListSize, floorUnix)
	if err := actionsGet(a.GH, ctx, path, &cur.List, "the workflow or its repository can no longer be read with your GitHub connection"); err != nil {
		return nil, raw, Hint{}, err
	}
	var best *ghRun
	wait := time.Duration(0)
	for i := range cur.List.Val.Runs {
		r := &cur.List.Val.Runs[i]
		if !startedAfter(*r, floorID, floorUnix) {
			continue
		}
		if r.Status != "completed" {
			if w := runWait(r.Status); wait == 0 || w < wait {
				wait = w
			}
			continue
		}
		if best == nil || r.ID > best.ID {
			best = r
		}
	}
	var evs []Event
	if best != nil {
		label := fmt.Sprintf("Workflow %s run #%d (%s run %d)", file, best.RunNumber, repo, best.ID)
		var err error
		if evs, err = a.runEvents(ctx, subject, repo, label, *best, cur); err != nil {
			return nil, raw, Hint{}, err
		}
	}
	out, _ := json.Marshal(cur)
	if wait == 0 {
		wait = actionsIdleAfter
	}
	return evs, out, Hint{After: wait}, nil
}

// startedAfter is the next-run rule: a run counts only when its id is above the floor (ids only
// grow) and it was created at or after the floor's creation time.
func startedAfter(r ghRun, floorID, floorUnix int64) bool {
	if r.ID <= floorID {
		return false
	}
	t, err := time.Parse(time.RFC3339, r.CreatedAt)
	if err != nil {
		return false // cannot show it is new: never fire for a run that may be old
	}
	return t.Unix() >= floorUnix
}

func runWait(status string) time.Duration {
	if status == "in_progress" {
		return actionsBusyAfter
	}
	return actionsQueuedAfter
}

// conclusionWord keeps the summary to words this source knows; anything else GitHub might send
// is not written into it.
func conclusionWord(c string) string {
	switch c {
	case "success", "failure", "cancelled", "skipped", "timed_out", "neutral", "action_required", "startup_failure", "stale":
		return c
	}
	return "unknown"
}

// runEvents is the events a completed run holds: completed always, failed besides when the
// conclusion is a failure. The version is run id and attempt, so a re-run is news and the same
// attempt seen twice is not. The summary uses only numbers and words this source controls; the
// workflow's name and the failed jobs, which a repository writer chose, go into Data.
func (a *GitHubActions) runEvents(ctx context.Context, subject SubjectID, repo, label string, run ghRun, cur *actionsCursor) ([]Event, error) {
	if run.Status != "completed" {
		return nil, nil
	}
	attempt := max(run.RunAttempt, 1)
	version := fmt.Sprintf("%d:%d", run.ID, attempt)
	summary := fmt.Sprintf("%s attempt %d completed: %s.", label, attempt, conclusionWord(run.Conclusion))
	data := []DataItem{{Text: "Workflow: " + run.Name, URL: SafeURL(run.HTMLURL)}}
	failed := failedConclusions[run.Conclusion]
	if failed {
		jobs := fmt.Sprintf("/repos/%s/actions/runs/%d/attempts/%d/jobs?per_page=100", repo, run.ID, attempt)
		if err := actionsGet(a.GH, ctx, jobs, &cur.Jobs, goneRun); err != nil {
			return nil, err
		}
		for _, j := range cur.Jobs.Val.Jobs {
			if failedConclusions[j.Conclusion] {
				data = append(data, DataItem{Text: "Failed job: " + j.Name, URL: j.HTMLURL})
			}
		}
	}
	at := a.GH.clock().UTC()
	mk := func(typ string) Event {
		return Event{Source: ActionsKind, Subject: subject, Type: typ, Version: version, Summary: summary, Data: data, At: at}
	}
	evs := []Event{mk(EventRunCompleted)}
	if failed {
		evs = append(evs, mk(EventRunFailed))
	}
	return evs, nil
}
