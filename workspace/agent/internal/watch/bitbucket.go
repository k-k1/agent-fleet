package watch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/branchpr"
)

// Bitbucket reads pull requests through the session list's Bitbucket PR cache. Bitbucket
// Cloud allows 1,000 requests an hour per account and the list already spends most of them, so
// this source asks no faster than the list would (an open PR is re-read after the cache's own
// interval) and shares its answers with the list's rows (ADR 0133 rule 6.2).
type Bitbucket struct {
	// Read answers one PR: from the cache when fresh, otherwise by asking Bitbucket and waiting.
	// A nil PR with no error means the PR does not exist or the connection cannot see it.
	Read func(ctx context.Context, repo string, number int) (*branchpr.PR, error)
}

// bitbucketTypes are the types this source can produce. Bitbucket's pull requests carry no
// resolved state for comment threads, and reading every comment page of every watched PR does
// not fit the hourly budget, so pr.review.threads_unresolved is refused (ADR 0133 §3).
var bitbucketTypes = []string{
	"pr.checks.completed", "pr.checks.failed",
	"pr.conflict", "pr.mergeable", "pr.ready",
	"pr.merged", "pr.closed",
	"pr.review.changes_requested",
}

// Poll intervals: a cheap cache read each minute; the cache decides when Bitbucket is asked.
const (
	bbOpenAfter    = time.Minute
	bbSettledAfter = 30 * time.Minute
)

func (b *Bitbucket) Kind() string    { return "bitbucket" }
func (b *Bitbucket) Types() []string { return append([]string(nil), bitbucketTypes...) }

var (
	bbRepoRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	bbRefRE  = regexp.MustCompile(`^(?:([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)#|#)?([1-9][0-9]{0,9})$`)
	bbURLRE  = regexp.MustCompile(`^https://bitbucket\.org/([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)/pull-requests/([1-9][0-9]{0,9})(?:[/?#].*)?$`)
)

// ParseBitbucketRef turns "workspace/repo#12", "#12" / "12" (with defaultRepo) or a pull request
// URL into the repository and number.
func ParseBitbucketRef(ref, defaultRepo string) (repo string, number int, ok bool) {
	ref = strings.TrimSpace(ref)
	if m := bbURLRE.FindStringSubmatch(ref); m != nil {
		repo, number = m[1], atoi(m[2])
	} else if m := bbRefRE.FindStringSubmatch(ref); m != nil {
		repo, number = m[1], atoi(m[2])
		if repo == "" {
			repo = defaultRepo
		}
	} else {
		return "", 0, false
	}
	if !validBBRepo(repo) || number <= 0 {
		return "", 0, false
	}
	// Lower-cased so one PR is one subject; branchpr lower-cases the keys it shares with the list.
	return strings.ToLower(repo), number, true
}

func validBBRepo(repo string) bool {
	if !bbRepoRE.MatchString(repo) {
		return false
	}
	ws, slug, _ := strings.Cut(repo, "/")
	return ws != "." && ws != ".." && slug != "." && slug != ".."
}

func bbSubject(repo string, n int) SubjectID {
	return SubjectID(fmt.Sprintf("bitbucket:%s#%d", repo, n))
}

func parseBBSubject(id SubjectID) (string, int, bool) {
	rest, ok := strings.CutPrefix(string(id), "bitbucket:")
	if !ok {
		return "", 0, false
	}
	repo, num, ok := strings.Cut(rest, "#")
	if !ok || !validBBRepo(repo) {
		return "", 0, false
	}
	n := atoi(num)
	return repo, n, n > 0
}

// errBBNotConnected is a refusal: only the member can connect Bitbucket.
var errBBNotConnected = &ErrRefused{"Bitbucket is not connected (Settings > Connections)"}

func (b *Bitbucket) read(ctx context.Context, repo string, n int) (*branchpr.PR, error) {
	pr, err := b.Read(ctx, repo, n)
	if branchpr.ErrNotConnected(err) {
		return nil, errBBNotConnected
	}
	return pr, err
}

func (b *Bitbucket) Resolve(ctx context.Context, ref, defaultRepo string) (Resolved, error) {
	repo, n, ok := ParseBitbucketRef(ref, defaultRepo)
	if !ok {
		if defaultRepo == "" && bbRefRE.MatchString(strings.TrimSpace(ref)) {
			return Resolved{}, &ErrRefused{"a bare #N needs a Bitbucket repository in this session's working copy; give workspace/repo#N"}
		}
		return Resolved{}, &ErrRefused{"give a pull request as workspace/repo#N, #N in this session's repository, or its https://bitbucket.org/… URL"}
	}
	pr, err := b.read(ctx, repo, n)
	if err != nil {
		return Resolved{}, err
	}
	if pr == nil {
		return Resolved{}, &ErrRefused{fmt.Sprintf("%s#%d is not a pull request your Bitbucket connection can read", repo, n)}
	}
	return Resolved{Subject: bbSubject(repo, n), URL: SafeURL(pr.URL)}, nil
}

// bbCursor keeps what a version needs across reads: Bitbucket gives no id for a change request
// or a conflict, so each counts the times its condition came back.
type bbCursor struct {
	CREpisode       int  `json:"cr_episode,omitempty"`
	CRWas           bool `json:"cr_was,omitempty"`
	ConflictEpisode int  `json:"conflict_episode,omitempty"`
	ConflictWas     bool `json:"conflict_was,omitempty"`
	CloseEpisode    int  `json:"close_episode,omitempty"`
	ClosedWas       bool `json:"closed_was,omitempty"`
}

// MaxSubscriptions bounds the live Bitbucket watches of a workspace: a watched open PR the list
// does not show costs about 50 requests an hour of the account's 1,000, which the list already
// spends most of.
func (Bitbucket) MaxSubscriptions() int { return bbMaxSubscriptions }

const bbMaxSubscriptions = 10

func (b *Bitbucket) Poll(ctx context.Context, subject SubjectID, raw json.RawMessage, _ []string) ([]Event, json.RawMessage, Hint, error) {
	repo, n, ok := parseBBSubject(subject)
	if !ok {
		return nil, raw, Hint{}, fmt.Errorf("not a bitbucket subject: %s", subject)
	}
	var cur bbCursor
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &cur)
	}
	pr, err := b.read(ctx, repo, n)
	if err != nil {
		return nil, raw, Hint{}, err
	}
	if pr == nil {
		return nil, raw, Hint{}, errors.New("Bitbucket did not return the pull request: it is gone or no longer readable with your connection")
	}
	at := time.Now().UTC()
	ev := func(typ, version, summary string, data []DataItem) Event {
		return Event{Source: "bitbucket", Subject: subject, Type: typ, Version: version, Summary: summary, Data: data, At: at}
	}
	ref := fmt.Sprintf("PR %s#%d", repo, n)
	closed := pr.State == "closed"
	if closed && !cur.ClosedWas {
		cur.CloseEpisode++ // declined, reopened and declined again is a second close
	}
	cur.ClosedWas = closed
	switch pr.State {
	case "merged":
		out, _ := json.Marshal(cur)
		return []Event{ev("pr.merged", "merged", ref+" was merged.", nil)}, out, Hint{After: bbSettledAfter, Settled: true}, nil
	case "closed":
		out, _ := json.Marshal(cur)
		return []Event{ev("pr.closed", fmt.Sprintf("closed:%d", cur.CloseEpisode), ref+" was declined or superseded.", nil)}, out,
			Hint{After: bbSettledAfter, Settled: true}, nil
	}

	var events []Event
	head := pr.Head
	checksDone := !pr.ChecksPending && (pr.Checks == "success" || pr.Checks == "failure")
	if checksDone && head != "" {
		version := head + ":" + pr.ChecksDigest
		var data []DataItem
		for _, c := range pr.FailingChecks {
			data = append(data, DataItem{Text: c.Name, URL: c.URL})
		}
		result := "passed"
		if pr.Checks == "failure" {
			result = fmt.Sprintf("failed (%d failing)", len(pr.FailingChecks))
		}
		sum := fmt.Sprintf("Checks on %s finished on %s: %s.", ref, short(head), result)
		completed := ev("pr.checks.completed", version, sum, data)
		completed.Failed = pr.Checks == "failure"
		events = append(events, completed)
		if pr.Checks == "failure" {
			events = append(events, ev("pr.checks.failed", version, sum, data))
		}
	}

	conflict := pr.Mergeable == "CONFLICTING"
	if pr.Mergeable == "UNKNOWN" {
		// A failed conflict read is no observation: read as "resolved", the next read that
		// answers would start a new episode and repeat the notice.
		conflict = cur.ConflictWas
	}
	if conflict && !cur.ConflictWas {
		cur.ConflictEpisode++
	}
	cur.ConflictWas = conflict
	// Bitbucket does not say where the destination stood, so mergeable and ready carry the
	// conflict episode: mergeable again after a conflict on the same head is news again.
	pair := fmt.Sprintf("%s:%d", head, cur.ConflictEpisode)
	if head != "" {
		switch pr.Mergeable {
		case "CONFLICTING":
			events = append(events, ev("pr.conflict", pair, ref+" has a merge conflict with its destination.", nil))
		case "MERGEABLE":
			events = append(events, ev("pr.mergeable", pair, ref+" can be merged into its destination.", nil))
		}
		if pr.Ready() {
			events = append(events, ev("pr.ready", pair, ref+" is ready to merge: checks passed, no conflict, approved, no changes requested.", nil))
		}
	}

	if pr.ChangesRequested && !cur.CRWas {
		cur.CREpisode++
	}
	cur.CRWas = pr.ChangesRequested
	if pr.ChangesRequested {
		events = append(events, ev("pr.review.changes_requested", fmt.Sprintf("%d", cur.CREpisode), fmt.Sprintf("Changes were requested on %s.", ref), nil))
	}
	out, _ := json.Marshal(cur)
	return events, out, Hint{After: bbOpenAfter}, nil
}
