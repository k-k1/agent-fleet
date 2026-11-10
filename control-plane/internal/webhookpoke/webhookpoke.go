// Package webhookpoke verifies and reads provider webhook deliveries for the watch engine
// (ADR 0133 §4). A delivery is only ever turned into subject references: nothing of its payload
// leaves this package, so what the Control Plane forwards to a workspace is a poke, never data.
package webhookpoke

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// MaxBody bounds a delivery the endpoint will read. The pull request events the parsers use are
// far smaller; GitHub's 25 MB ceiling is for pushes the engine has no use for.
const MaxBody = 1 << 20

// MaxSubjects bounds the subjects one delivery may name, so one delivery cannot fan out into
// unbounded work.
const MaxSubjects = 10

// ErrBadSignature is returned for a missing, malformed or wrong signature. It carries no detail
// on purpose: the caller is anonymous.
var ErrBadSignature = errors.New("bad signature")

// Parsed is what a verified delivery says.
type Parsed struct {
	// Subjects are normalised references ("github:owner/repo#12", repository lower-cased).
	Subjects []string
	// Private reports that the repository is private. A source that cannot tell says true.
	Private bool
	// Repo is the repository the payload names, lower-cased, "" when it names none valid.
	Repo string
}

// VerifyHMAC256 checks an "sha256=<hex>" signature header (GitHub X-Hub-Signature-256,
// Bitbucket X-Hub-Signature) over body with secret. The comparison is constant time, and an
// empty secret never verifies.
func VerifyHMAC256(header string, body, secret []byte) error {
	if len(secret) == 0 {
		return ErrBadSignature
	}
	hexSig, ok := strings.CutPrefix(strings.TrimSpace(header), "sha256=")
	if !ok {
		return ErrBadSignature
	}
	got, err := hex.DecodeString(hexSig)
	if err != nil || len(got) != sha256.Size {
		return ErrBadSignature
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	if !hmac.Equal(got, mac.Sum(nil)) {
		return ErrBadSignature
	}
	return nil
}

// repoRE is an owner/name pair as both providers write them.
var repoRE = regexp.MustCompile(`^[A-Za-z0-9._~-]{1,100}/[A-Za-z0-9._~-]{1,100}$`)

// SubjectRE is the shape of a subject the ingress accepts or produces.
var SubjectRE = regexp.MustCompile(`^(github|bitbucket):[a-z0-9._~-]{1,100}/[a-z0-9._~-]{1,100}#[1-9][0-9]{0,8}$`)

// Actions subjects are poke keys, not the engine's own subjects: a workflow watch's subject
// carries a floor (the newest run seen when the watch was made) that a delivery cannot know, so
// the Agent registers, and a delivery names, the workflow without it.
//
//	run       github:o/r/actions/runs/<id>[/attempts/<n>]
//	workflow  github:o/r/actions/workflows/<file>[/branch/<b>]
//
// The repository is lower-cased as in a pull request subject; the file and branch keep their case.
var (
	actRepoPat      = `[a-z0-9_.~-]{1,100}/[a-z0-9_.~-]{1,100}`
	actRunSubjectRE = regexp.MustCompile(`^github:(` + actRepoPat + `)/actions/runs/[1-9][0-9]{0,17}(?:/attempts/[1-9][0-9]{0,3})?$`)
	actWfSubjectRE  = regexp.MustCompile(`^github:(` + actRepoPat + `)/actions/workflows/([A-Za-z0-9_.-]+\.ya?ml)(?:/branch/([A-Za-z0-9._/-]{1,100}))?$`)
)

// validBranch is the Agent's rule for a branch inside a subject.
func validBranch(b string) bool {
	return b != "" && !strings.Contains(b, "..") && !strings.HasPrefix(b, "/") && !strings.HasSuffix(b, "/") &&
		!strings.Contains(b, "//") && !strings.HasPrefix(b, "-")
}

// ValidSubject reports whether s has the shape of a subject the ingress produces: it is the one
// check the Agent's registration and a delivery both pass through.
func ValidSubject(s string) bool {
	if m := actRunSubjectRE.FindStringSubmatch(s); m != nil {
		return !dotName(m[1])
	}
	if m := actWfSubjectRE.FindStringSubmatch(s); m != nil {
		return !dotName(m[1]) && (m[3] == "" || validBranch(m[3]))
	}
	if !SubjectRE.MatchString(s) {
		return false
	}
	repo := s[strings.IndexByte(s, ':')+1 : strings.IndexByte(s, '#')]
	return !dotName(repo)
}

// dotName is true when either half of owner/name is "." or "..".
func dotName(repo string) bool {
	for _, part := range strings.Split(repo, "/") {
		if part == "." || part == ".." {
			return true
		}
	}
	return false
}

func subject(source, repo string, n int) (string, bool) {
	if n <= 0 || n > 999999999 || !repoRE.MatchString(repo) || dotName(repo) {
		return "", false
	}
	s := fmt.Sprintf("%s:%s#%d", source, strings.ToLower(repo), n)
	return s, SubjectRE.MatchString(s)
}

type ghPR struct {
	Number int `json:"number"`
}

type ghPayload struct {
	Repository struct {
		FullName string `json:"full_name"`
		Private  *bool  `json:"private"`
	} `json:"repository"`
	Number      int   `json:"number"`
	PullRequest *ghPR `json:"pull_request"`
	Issue       *struct {
		Number      int             `json:"number"`
		PullRequest json.RawMessage `json:"pull_request"`
	} `json:"issue"`
	CheckRun *struct {
		PullRequests []ghPR `json:"pull_requests"`
	} `json:"check_run"`
	CheckSuite *struct {
		PullRequests []ghPR `json:"pull_requests"`
	} `json:"check_suite"`
	Action      string `json:"action"`
	WorkflowRun *struct {
		ID           int64  `json:"id"`
		RunAttempt   int    `json:"run_attempt"`
		Path         string `json:"path"`
		HeadBranch   string `json:"head_branch"`
		PullRequests []ghPR `json:"pull_requests"`
	} `json:"workflow_run"`
}

// actionsSubjects are the poke keys a completed workflow run names: the run (with and without its
// attempt, as a run watch may hold either) and its workflow, with and without the run's branch
// (a workflow watch may follow one branch or all). A poke only brings a read forward, so a key
// that no watch holds costs nothing, and one that is wrong costs a single early read.
func actionsSubjects(repo string, wr *ghPayload) []string {
	r := wr.WorkflowRun
	if r == nil || wr.Action != "completed" || r.ID <= 0 || r.ID > 999999999999999999 || !repoRE.MatchString(repo) || dotName(repo) {
		return nil
	}
	base := "github:" + strings.ToLower(repo) + "/actions/"
	out := []string{base + "runs/" + strconv.FormatInt(r.ID, 10)}
	if r.RunAttempt > 0 && r.RunAttempt <= 9999 {
		out = append(out, out[0]+"/attempts/"+strconv.Itoa(r.RunAttempt))
	}
	// The path is ".github/workflows/<file>", with an "@<ref>" suffix on some triggers.
	file, _, _ := strings.Cut(r.Path, "@")
	if dir, f, ok := cutLast(file, "/"); ok && dir == ".github/workflows" {
		wf := base + "workflows/" + f
		out = append(out, wf)
		if validBranch(r.HeadBranch) {
			out = append(out, wf+"/branch/"+r.HeadBranch)
		}
	}
	// One check for the whole set: a field that does not fit the wire shape drops its key.
	valid := out[:0]
	for _, s := range out {
		if ValidSubject(s) {
			valid = append(valid, s)
		}
	}
	return valid
}

func cutLast(s, sep string) (before, after string, ok bool) {
	i := strings.LastIndex(s, sep)
	if i < 0 {
		return "", s, false
	}
	return s[:i], s[i+len(sep):], true
}

// ParseGitHub reads a verified GitHub delivery. event is the X-GitHub-Event header. Events that
// do not name a pull request (ping, push, status) give no subjects: they are not an error.
func ParseGitHub(event string, body []byte) (Parsed, error) {
	var p ghPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return Parsed{}, errors.New("unreadable payload")
	}
	// Private unless the payload says otherwise: when in doubt, answer true.
	out := Parsed{Private: p.Repository.Private == nil || *p.Repository.Private, Repo: strings.ToLower(p.Repository.FullName)}
	var numbers []int
	switch event {
	case "pull_request", "pull_request_review", "pull_request_review_comment", "pull_request_review_thread":
		if p.PullRequest != nil {
			numbers = append(numbers, p.PullRequest.Number)
		} else {
			numbers = append(numbers, p.Number)
		}
	case "issue_comment":
		if p.Issue != nil && len(p.Issue.PullRequest) > 0 && string(p.Issue.PullRequest) != "null" {
			numbers = append(numbers, p.Issue.Number)
		}
	case "check_run":
		if p.CheckRun != nil {
			for _, pr := range p.CheckRun.PullRequests {
				numbers = append(numbers, pr.Number)
			}
		}
	case "check_suite":
		if p.CheckSuite != nil {
			for _, pr := range p.CheckSuite.PullRequests {
				numbers = append(numbers, pr.Number)
			}
		}
	case "workflow_run":
		if p.WorkflowRun != nil {
			for _, pr := range p.WorkflowRun.PullRequests {
				numbers = append(numbers, pr.Number)
			}
		}
	}
	if event == "workflow_run" {
		out.Subjects = actionsSubjects(p.Repository.FullName, &p)
	}
	out.Subjects = append(out.Subjects, collect("github", p.Repository.FullName, numbers)...)
	if len(out.Subjects) > MaxSubjects {
		out.Subjects = out.Subjects[:MaxSubjects]
	}
	return out, nil
}

type bbPayload struct {
	Repository struct {
		FullName  string `json:"full_name"`
		IsPrivate *bool  `json:"is_private"`
	} `json:"repository"`
	PullRequest *struct {
		ID int `json:"id"`
	} `json:"pullrequest"`
}

// ParseBitbucket reads a verified Bitbucket Cloud delivery. eventKey is X-Event-Key; only the
// pullrequest:* family names a pull request.
func ParseBitbucket(eventKey string, body []byte) (Parsed, error) {
	var p bbPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return Parsed{}, errors.New("unreadable payload")
	}
	out := Parsed{Private: p.Repository.IsPrivate == nil || *p.Repository.IsPrivate, Repo: strings.ToLower(p.Repository.FullName)}
	if strings.HasPrefix(eventKey, "pullrequest:") && p.PullRequest != nil {
		out.Subjects = collect("bitbucket", p.Repository.FullName, []int{p.PullRequest.ID})
	}
	return out, nil
}

// collect builds the distinct valid subjects of one repository, at most MaxSubjects.
func collect(source, repo string, numbers []int) []string {
	var out []string
	seen := map[string]bool{}
	for _, n := range numbers {
		s, ok := subject(source, repo, n)
		if !ok || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
		if len(out) == MaxSubjects {
			break
		}
	}
	return out
}
