package watch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The Bitbucket Pipelines source (ADR 0133 §9, "CI on its own", second provider): a pipeline run
// that is not tied to a pull request. It is its own source, bitbucket_pipelines, beside the
// bitbucket pull request source, for the reason github_actions is beside github: the engine checks
// event types per source, and a pull request reference stays what it was. Its events are the
// shared ci.run.* types, so one subscription works on either provider.
//
// Two subjects, shaped like the Actions ones:
//
//	run       bitbucket:w/r/pipelines/runs/<build number>
//	pipeline  bitbucket:w/r/pipelines/after/<floor build>-<floor unix>[/name/<custom>][/branch/<b>]
//
// A pipeline watch is for the NEXT run. As for Actions, the floor comes from Bitbucket's own
// answer (the highest build number and newest creation time it listed, or its Date header when
// the repository has no run yet), never from this host's clock. Build numbers only grow within a
// repository, so build > floor is the rule and the creation time a second guard.
//
// Bitbucket Cloud allows 1,000 requests an hour per account and the pull request list already
// spends most of them, so reads are slow (see the intervals) and the number of live watches is
// bounded. A read costs one request, plus one for the steps of a failed run, once.

// PipelinesKind is the Pipelines source's name.
const PipelinesKind = "bitbucket_pipelines"

const (
	plBusyAfter    = time.Minute      // a run is in progress
	plQueuedAfter  = 2 * time.Minute  // pending, paused or waiting for a runner
	plIdleAfter    = 5 * time.Minute  // a pipeline watch with no run in flight
	plSettledAfter = 30 * time.Minute // a finished run; a re-run can still follow
	// plListSize is how many runs one page of the list holds (Bitbucket allows up to 100).
	plListSize = 50
	// plMaxPages bounds how far the list is followed (250 runs). The branch and the pipeline name
	// are filtered here, so a branch that is quiet among busy ones is found only that deep.
	plMaxPages = 5
	// plMaxSubscriptions bounds the live Pipelines watches of a workspace: a watch of a run in
	// progress is about 60 requests an hour of the account's 1,000.
	plMaxSubscriptions = 5
)

// BitbucketPipelines watches Pipelines runs.
type BitbucketPipelines struct {
	// Auth returns the whole Authorization value ("Bearer …" or "Basic …"), "" when Bitbucket is
	// not connected. It is never logged or put in an error.
	Auth func() (string, error)
	// Renew returns a fresh Authorization value after Bitbucket rejected the given one; the
	// rejected value comes back when it cannot be renewed (a pasted API token).
	Renew func(rejected string) (string, error)
	// API is the REST base; tests point it at a local server.
	API    string
	Client *http.Client
	Now    func() time.Time
}

// NewBitbucketPipelines returns the Pipelines source.
func NewBitbucketPipelines(auth func() (string, error), renew func(string) (string, error)) *BitbucketPipelines {
	return &BitbucketPipelines{Auth: auth, Renew: renew, API: "https://api.bitbucket.org", Client: &http.Client{Timeout: 20 * time.Second}}
}

func (p *BitbucketPipelines) Kind() string    { return PipelinesKind }
func (p *BitbucketPipelines) Types() []string { return []string{EventRunCompleted, EventRunFailed} }

// MaxSubscriptions implements SourceLimiter.
func (p *BitbucketPipelines) MaxSubscriptions() int { return plMaxSubscriptions }

func (p *BitbucketPipelines) clock() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

const (
	plRepo = `[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+`
	plName = `[A-Za-z0-9._-]{1,100}`
)

var (
	plRunURLRE = regexp.MustCompile(`^https://bitbucket\.org/(` + plRepo + `)/pipelines/results/([1-9][0-9]{0,9})(?:[/?#].*)?$`)
	plPipURLRE = regexp.MustCompile(`^https://bitbucket\.org/(` + plRepo + `)/pipelines/?(?:[?#].*)?$`)
	plRunRE    = regexp.MustCompile(`^(?:(` + plRepo + `):)?run/([1-9][0-9]{0,9})$`)
	plPipRE    = regexp.MustCompile(`^(?:(` + plRepo + `):)?pipelines(?:/(` + plName + `))?(?:@([A-Za-z0-9._/-]{1,100}))?$`)

	plRunSubjectRE = regexp.MustCompile(`^bitbucket:(` + plRepo + `)/pipelines/runs/([1-9][0-9]{0,9})$`)
	plPipSubjectRE = regexp.MustCompile(`^bitbucket:(` + plRepo + `)/pipelines/after/([0-9]{1,10})-([0-9]{1,12})(?:/name/(` + plName + `))?(?:/branch/([A-Za-z0-9._/-]{1,100}))?$`)
)

// pipelinesRef is a parsed reference: a run (build set) or a pipeline watch.
type pipelinesRef struct {
	repo   string
	build  int64
	name   string
	branch string
}

// LooksLikePipelinesRef reports whether ref is in a Pipelines form that is unambiguous on its own:
// a bitbucket.org pipelines URL. The bare forms (o/r:run/<id>) are the same shape as the Actions
// ones, so the watch handler reads them as Pipelines only when the caller named source bitbucket.
func LooksLikePipelinesRef(ref string) bool {
	ref = strings.TrimSpace(ref)
	return plRunURLRE.MatchString(ref) || plPipURLRE.MatchString(ref)
}

// LooksLikePipelinesShortRef reports whether ref is in a bare Pipelines form (to be used with
// source bitbucket). A pull request reference never matches.
func LooksLikePipelinesShortRef(ref string) bool {
	_, ok := matchPipelinesRef(strings.TrimSpace(ref), "x/x")
	return ok
}

func matchPipelinesRef(ref, defaultRepo string) (pipelinesRef, bool) {
	if m := plRunURLRE.FindStringSubmatch(ref); m != nil {
		return pipelinesRef{repo: m[1], build: atoi64(m[2])}, true
	}
	if m := plPipURLRE.FindStringSubmatch(ref); m != nil {
		return pipelinesRef{repo: m[1]}, true
	}
	if m := plRunRE.FindStringSubmatch(ref); m != nil {
		return pipelinesRef{repo: orDefault(m[1], defaultRepo), build: atoi64(m[2])}, true
	}
	if m := plPipRE.FindStringSubmatch(ref); m != nil {
		return pipelinesRef{repo: orDefault(m[1], defaultRepo), name: m[2], branch: m[3]}, true
	}
	return pipelinesRef{}, false
}

const pipelinesRefHelp = "give a pipeline run as its https://bitbucket.org/w/r/pipelines/results/<n> URL (or w/r:run/<n>), " +
	"or the next run of a repository's pipelines as w/r:pipelines, optionally for one custom pipeline and a branch: " +
	"w/r:pipelines/<name>@<branch>"

func parsePipelinesRef(ref, defaultRepo string) (pipelinesRef, error) {
	r, ok := matchPipelinesRef(strings.TrimSpace(ref), defaultRepo)
	if !ok {
		return pipelinesRef{}, &ErrRefused{pipelinesRefHelp}
	}
	if r.repo == "" {
		return pipelinesRef{}, &ErrRefused{"this form needs a Bitbucket repository in this session's working copy; give w/r:run/<n> or w/r:pipelines"}
	}
	if !validBBRepo(r.repo) {
		return pipelinesRef{}, &ErrRefused{pipelinesRefHelp}
	}
	r.repo = strings.ToLower(r.repo)
	if r.branch != "" && !validBranch(r.branch) {
		return pipelinesRef{}, &ErrRefused{"that branch name is not accepted; use letters, digits and . _ - / only"}
	}
	return r, nil
}

func plRunSubject(repo string, build int64) SubjectID {
	return SubjectID(fmt.Sprintf("bitbucket:%s/pipelines/runs/%d", repo, build))
}

func plPipSubject(repo, name, branch string, floorBuild, floorUnix int64) SubjectID {
	s := fmt.Sprintf("bitbucket:%s/pipelines/after/%d-%d", repo, floorBuild, floorUnix)
	if name != "" {
		s += "/name/" + name
	}
	if branch != "" {
		s += "/branch/" + branch
	}
	return SubjectID(s)
}

// plRun is the part of a pipeline run this source uses.
type plRun struct {
	UUID        string `json:"uuid"`
	BuildNumber int64  `json:"build_number"`
	CreatedOn   string `json:"created_on"`
	CompletedOn string `json:"completed_on"`
	State       struct {
		Name   string `json:"name"`
		Result struct {
			Name string `json:"name"`
		} `json:"result"`
	} `json:"state"`
	Target struct {
		RefName  string `json:"ref_name"`
		Selector struct {
			Pattern string `json:"pattern"`
		} `json:"selector"`
	} `json:"target"`
}

type plPage struct {
	Values []plRun `json:"values"`
	Next   string  `json:"next"`
}

type plStep struct {
	Name  string `json:"name"`
	State struct {
		Result struct {
			Name string `json:"name"`
		} `json:"result"`
	} `json:"state"`
}

type plStepPage struct {
	Values []plStep `json:"values"`
}

// plCursor is what one Poll keeps for the next. Steps are read once per finished run: Key is the
// run version they belong to.
type plCursor struct {
	StepsKey string   `json:"steps_key,omitempty"`
	Failed   []string `json:"failed_steps,omitempty"`
}

func (p *BitbucketPipelines) base(repo string) string {
	ws, slug, _ := strings.Cut(repo, "/")
	return "/2.0/repositories/" + url.PathEscape(ws) + "/" + url.PathEscape(slug) + "/pipelines"
}

func (p *BitbucketPipelines) runPath(repo string, build int64) string {
	return p.base(repo) + "/" + strconv.FormatInt(build, 10)
}

func (p *BitbucketPipelines) listPath(repo string, size int) string {
	return fmt.Sprintf("%s/?sort=-created_on&pagelen=%d", p.base(repo), size)
}

var errBBRejected = &ErrRefused{"Bitbucket rejected the connection's token; reconnect Bitbucket in Settings > Connections"}

// errPlScope is a 403: the connection lacks the pipelines read scope, which only a reconnect fixes.
var errPlScope = &ErrRefused{"Bitbucket refused the read; the connection needs the Pipelines read scope (reconnect Bitbucket in Settings > Connections)"}

// get performs one GET, renewing the token once on a 401, and decodes a 200 into v. The
// returned status is 200 on success; 404 is returned to the caller, which knows what is missing.
// Every other status is an error: 401 and 403 are refusals only the member can fix, 429 holds the
// source off.
func (p *BitbucketPipelines) get(ctx context.Context, rawURL string, v any) (int, http.Header, error) {
	auth, err := p.Auth()
	if err != nil {
		return 0, nil, err
	}
	if auth == "" {
		return 0, nil, errBBNotConnected
	}
	resp, err := p.do(ctx, rawURL, auth)
	if err == nil && resp.StatusCode == http.StatusUnauthorized && p.Renew != nil {
		resp.Body.Close()
		fresh, rerr := p.Renew(auth)
		switch {
		case rerr != nil:
			return 0, nil, errors.New("Bitbucket rejected the connection's token and renewing it failed")
		case fresh == "" || fresh == auth:
			return 0, nil, errBBRejected
		}
		resp, err = p.do(ctx, rawURL, fresh)
	}
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(v); err != nil {
			return 0, nil, errors.New("Bitbucket's answer for a pipelines read could not be read")
		}
		return resp.StatusCode, resp.Header, nil
	case http.StatusNotFound:
		return resp.StatusCode, resp.Header, nil
	case http.StatusUnauthorized:
		return 0, nil, errBBRejected
	case http.StatusForbidden:
		return 0, nil, errPlScope
	case http.StatusTooManyRequests:
		wait := 5 * time.Minute
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s >= 0 {
			wait = time.Duration(s) * time.Second
		}
		return 0, nil, &ErrRateLimited{Until: p.clock().Add(wait)}
	}
	return 0, nil, fmt.Errorf("Bitbucket answered %d for a pipelines read", resp.StatusCode)
}

func (p *BitbucketPipelines) do(ctx context.Context, rawURL, auth string) (*http.Response, error) {
	if strings.HasPrefix(rawURL, "/") {
		rawURL = p.API + rawURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "agent-fleet")
	return p.Client.Do(req)
}

func (p *BitbucketPipelines) Resolve(ctx context.Context, ref, defaultRepo string) (Resolved, error) {
	r, err := parsePipelinesRef(ref, defaultRepo)
	if err != nil {
		return Resolved{}, err
	}
	if r.build != 0 {
		var run plRun
		status, _, err := p.get(ctx, p.runPath(r.repo, r.build), &run)
		switch {
		case err != nil:
			return Resolved{}, err
		case status == http.StatusNotFound:
			return Resolved{}, &ErrRefused{fmt.Sprintf("%s pipeline run %d is not a run your Bitbucket connection can read", r.repo, r.build)}
		}
		return Resolved{Subject: plRunSubject(r.repo, r.build), URL: plRunURL(r.repo, r.build)}, nil
	}
	var list plPage
	status, hdr, err := p.get(ctx, p.listPath(r.repo, 5), &list)
	switch {
	case err != nil:
		return Resolved{}, err
	case status == http.StatusNotFound:
		return Resolved{}, &ErrRefused{fmt.Sprintf("%s is not a repository your Bitbucket connection can read, or it has Pipelines turned off", r.repo)}
	}
	// The floor spans every listed run, not just those of the watched branch: build numbers are
	// repository-wide and only grow, so a higher floor can only be safer.
	floorBuild, floorUnix := plFloorOf(list.Values)
	if floorBuild == 0 && floorUnix == 0 {
		if d, err := http.ParseTime(hdr.Get("Date")); err == nil {
			floorUnix = d.Unix() - d.Unix()%60 // to the minute, so quiet watches share a subject
		}
	}
	subj := plPipSubject(r.repo, r.name, r.branch, floorBuild, floorUnix)
	if !TokenOK(string(subj)) {
		return Resolved{}, &ErrRefused{"that reference is too long"}
	}
	return Resolved{Subject: subj, URL: SafeURL("https://bitbucket.org/" + r.repo + "/pipelines")}, nil
}

func plRunURL(repo string, build int64) string {
	return SafeURL(fmt.Sprintf("https://bitbucket.org/%s/pipelines/results/%d", repo, build))
}

func plFloorOf(runs []plRun) (build, unix int64) {
	for _, r := range runs {
		build = max(build, r.BuildNumber)
		if t, err := time.Parse(time.RFC3339, r.CreatedOn); err == nil {
			unix = max(unix, t.Unix())
		}
	}
	return build, unix
}

func (p *BitbucketPipelines) Poll(ctx context.Context, subject SubjectID, raw json.RawMessage, _ []string) ([]Event, json.RawMessage, Hint, error) {
	var cur plCursor
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &cur)
	}
	if m := plRunSubjectRE.FindStringSubmatch(string(subject)); m != nil {
		return p.pollRun(ctx, subject, m[1], atoi64(m[2]), &cur, raw)
	}
	if m := plPipSubjectRE.FindStringSubmatch(string(subject)); m != nil {
		return p.pollPipeline(ctx, subject, m[1], m[4], m[5], atoi64(m[2]), atoi64(m[3]), &cur, raw)
	}
	return nil, raw, Hint{}, fmt.Errorf("not a bitbucket_pipelines subject: %s", subject)
}

const (
	gonePipelines = "the repository can no longer be read with your Bitbucket connection, or its Pipelines were turned off"
	gonePlRun     = "the pipeline run was deleted or can no longer be read with your Bitbucket connection"
)

func (p *BitbucketPipelines) pollRun(ctx context.Context, subject SubjectID, repo string, build int64, cur *plCursor, raw json.RawMessage) ([]Event, json.RawMessage, Hint, error) {
	var run plRun
	status, _, err := p.get(ctx, p.runPath(repo, build), &run)
	switch {
	case err != nil:
		return nil, raw, Hint{}, err
	case status == http.StatusNotFound:
		return nil, raw, Hint{}, &ErrGone{gonePlRun}
	}
	label := fmt.Sprintf("Pipeline run #%d (%s)", build, repo)
	evs, err := p.runEvents(ctx, subject, repo, label, run, cur)
	if err != nil {
		return nil, raw, Hint{}, err
	}
	out, _ := json.Marshal(cur)
	if plDone(run) {
		return evs, out, Hint{After: plSettledAfter, Settled: true}, nil
	}
	return evs, out, Hint{After: plWait(run)}, nil
}

func (p *BitbucketPipelines) pollPipeline(ctx context.Context, subject SubjectID, repo, name, branch string, floorBuild, floorUnix int64, cur *plCursor, raw json.RawMessage) ([]Event, json.RawMessage, Hint, error) {
	runs, capped, err := p.newRuns(ctx, repo, floorBuild)
	if err != nil {
		return nil, raw, Hint{}, err
	}
	var best *plRun
	wait := time.Duration(0)
	for i := range runs {
		r := &runs[i]
		if (branch != "" && r.Target.RefName != branch) || (name != "" && r.Target.Selector.Pattern != name) {
			continue
		}
		if !plStartedAfter(*r, floorBuild, floorUnix) {
			continue
		}
		if !plDone(*r) {
			if w := plWait(*r); wait == 0 || w < wait {
				wait = w
			}
			continue
		}
		if best == nil || r.BuildNumber > best.BuildNumber {
			best = r
		}
	}
	var evs []Event
	if best != nil {
		label := fmt.Sprintf("Pipeline run #%d (%s)", best.BuildNumber, repo)
		if evs, err = p.runEvents(ctx, subject, repo, label, *best, cur); err != nil {
			return nil, raw, Hint{}, err
		}
	}
	out, _ := json.Marshal(cur)
	if wait == 0 || capped {
		wait = max(wait, plIdleAfter)
	}
	return evs, out, Hint{After: wait}, nil
}

// newRuns lists the runs newer than floorBuild, newest first, following Bitbucket's next links
// for at most plMaxPages pages and stopping at the first page that reaches the floor. A later
// page that fails fails the whole read, so a run on a page that could not be read is never judged
// missing. capped reports that the bound stopped the walk with the floor not yet reached.
func (p *BitbucketPipelines) newRuns(ctx context.Context, repo string, floorBuild int64) (runs []plRun, capped bool, err error) {
	next := p.listPath(repo, plListSize)
	seen := map[int64]bool{}
	for page := 1; next != ""; page++ {
		if page > plMaxPages {
			return runs, true, nil
		}
		var pg plPage
		status, _, err := p.get(ctx, next, &pg)
		switch {
		case err != nil:
			return nil, false, err
		case status == http.StatusNotFound:
			return nil, false, &ErrGone{gonePipelines}
		}
		reached := len(pg.Values) == 0
		for _, r := range pg.Values {
			if r.BuildNumber <= floorBuild {
				reached = true
				continue
			}
			// A run that starts between two page reads pushes one run to the next page; it must
			// not be counted twice.
			if !seen[r.BuildNumber] {
				seen[r.BuildNumber] = true
				runs = append(runs, r)
			}
		}
		if reached {
			return runs, false, nil
		}
		// Only a link on this API's own host is followed: the token goes along with it.
		next = ""
		if strings.HasPrefix(pg.Next, p.API+"/") {
			next = pg.Next
		}
	}
	return runs, false, nil
}

// plStartedAfter is the next-run rule: the build number is above the floor and the run was
// created at or after the floor's creation time.
func plStartedAfter(r plRun, floorBuild, floorUnix int64) bool {
	if r.BuildNumber <= floorBuild {
		return false
	}
	t, err := time.Parse(time.RFC3339, r.CreatedOn)
	if err != nil {
		return false // cannot show it is new: never fire for a run that may be old
	}
	return t.Unix() >= floorUnix
}

func plDone(r plRun) bool { return r.State.Name == "COMPLETED" }

func plWait(r plRun) time.Duration {
	if r.State.Name == "IN_PROGRESS" {
		return plBusyAfter
	}
	return plQueuedAfter
}

// plResultWord keeps the summary to words this source knows.
func plResultWord(c string) string {
	switch c {
	case "SUCCESSFUL", "FAILED", "ERROR", "STOPPED", "EXPIRED":
		return strings.ToLower(c)
	}
	return "unknown"
}

var plFailedResults = map[string]bool{"FAILED": true, "ERROR": true, "STOPPED": true, "EXPIRED": true}

// runEvents is the events a finished run holds: completed always, failed besides when the result
// is not a success (cancelled counts, as it does for Actions). The version is the build number
// and completion time, so the same run seen twice is not news and a run finished again is. The
// summary uses numbers and words this source controls; the branch and the failed steps, which a
// repository writer chose, go into Data.
func (p *BitbucketPipelines) runEvents(ctx context.Context, subject SubjectID, repo, label string, run plRun, cur *plCursor) ([]Event, error) {
	if !plDone(run) {
		return nil, nil
	}
	version := fmt.Sprintf("%d:%s", run.BuildNumber, run.CompletedOn)
	if t, err := time.Parse(time.RFC3339, run.CompletedOn); err == nil {
		version = fmt.Sprintf("%d:%d", run.BuildNumber, t.Unix())
	}
	summary := fmt.Sprintf("%s completed: %s.", label, plResultWord(run.State.Result.Name))
	data := []DataItem{{Text: "Pipeline run on " + run.Target.RefName, URL: plRunURL(repo, run.BuildNumber)}}
	failed := plFailedResults[run.State.Result.Name]
	if failed {
		if cur.StepsKey != version {
			steps, err := p.failedSteps(ctx, repo, run)
			if err != nil {
				return nil, err
			}
			cur.StepsKey, cur.Failed = version, steps
		}
		for _, s := range cur.Failed {
			data = append(data, DataItem{Text: "Failed step: " + s, URL: plRunURL(repo, run.BuildNumber)})
		}
	}
	at := p.clock().UTC()
	mk := func(typ string) Event {
		return Event{Source: PipelinesKind, Subject: subject, Type: typ, Version: version, Summary: summary, Data: data, At: at}
	}
	evs := []Event{mk(EventRunCompleted)}
	if failed {
		evs = append(evs, mk(EventRunFailed))
	}
	return evs, nil
}

// failedSteps names the steps of a run that did not succeed (one request, first page only: a
// pipeline has at most 100 steps).
func (p *BitbucketPipelines) failedSteps(ctx context.Context, repo string, run plRun) ([]string, error) {
	var pg plStepPage
	path := p.base(repo) + "/" + url.PathEscape(run.UUID) + "/steps/?pagelen=100"
	status, _, err := p.get(ctx, path, &pg)
	switch {
	case err != nil:
		return nil, err
	case status == http.StatusNotFound:
		return nil, &ErrGone{gonePlRun}
	}
	var out []string
	for _, s := range pg.Values {
		if plFailedResults[s.State.Result.Name] {
			out = append(out, s.Name)
		}
	}
	return out, nil
}
