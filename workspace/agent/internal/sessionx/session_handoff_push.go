package sessionx

// "Push, then send" for a member handoff (ADR 0057, issue #2072).
//
// When the push gate blocks because the branch is unpushed or was never pushed, the owner
// can ask for the push from the offer modal instead of leaving it to push by hand. The CP
// calls this endpoint on the owner's own click, inside the offer request, and then asks
// handoff-context again: whether the offer may go stays this Agent's verdict.
//
// What is pushed is deliberately narrow: the commit the owner was shown, to the branch of the
// same name on origin, and only when that is where git would send it — the push URL is
// origin's own URL and any upstream is origin's branch of the same name, so the offer's
// remote and branch name exactly where the commit went. Never with force, tags or
// submodules. Uncommitted changes are not touched. The workspace's own git setup supplies the
// credentials (the global credential helper), and the repository's hooks run: a pre-push hook
// that refuses is reported as git printed it.
//
// The route accepts only a request the CP marked (memberorigin, ADR 0113): AGENT_TOKEN is in
// every session's shell, and a push from here must be the owner's own click.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/bridge"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memberorigin"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// handoffPushTimeout bounds the push. GIT_TERMINAL_PROMPT=0 stops git asking for a password
// but not a slow host or a slow pre-push hook, and the CP's request to this Agent waits on
// it (agentHTTPClient gives up after two minutes).
const handoffPushTimeout = 60 * time.Second

// handoffPushWaitDelay bounds the wait for git's output pipes after the process group is
// killed: a hook's grandchild that escaped the group would otherwise hold them open.
const handoffPushWaitDelay = 2 * time.Second

// handoffPushOutputMax caps the git output relayed to the Console; a chatty pre-push hook
// keeps its last lines, which is where the reason for a refusal is.
const handoffPushOutputMax = 4 << 10

// HandoffPushMaxRequest bounds the body memberorigin.Require buffers: three short strings.
const HandoffPushMaxRequest = 4 << 10

// handoffPushClears reports whether a push can clear this gate reason. A detached HEAD and
// an undecidable state cannot be fixed by pushing and stay blocked.
func handoffPushClears(blocked string) bool {
	return blocked == handoffBlockUnpushed || blocked == handoffBlockNoUpstream
}

// handoffPushTarget is where the current branch would be pushed: always origin, to the branch
// of the same name.
type handoffPushTarget struct {
	branch string
	// url is origin's URL as git resolves it for both fetch and push (insteadOf applied), the
	// one the offer names as its remote.
	url string
	// setUpstream: the branch has no upstream configured, so the push records one.
	setUpstream bool
}

// handoffPushTargetOf resolves the push target of branch, or says why there is none the offer
// could describe truthfully.
//
//   - The upstream, when configured, must be origin's branch of the same name. Another remote
//     or another branch name would publish the commit somewhere the offer (origin's URL, this
//     branch name) does not point, and the recipient would start from a stale branch.
//   - origin must push where it fetches: exactly one push URL, equal to the fetch URL, after
//     pushurl, insteadOf and pushInsteadOf. Otherwise the owner would confirm one address and
//     git would publish to another.
//
// The upstream is read from config rather than @{u}, because @{u} fails once the
// remote-tracking ref is gone.
func handoffPushTargetOf(dir, branch string) (handoffPushTarget, error) {
	if branch == "" || branch == "(detached)" {
		return handoffPushTarget{}, errors.New("HEAD is detached")
	}
	t := handoffPushTarget{branch: branch}
	remote, _ := gitx.Run(dir, "config", "--get", "branch."+branch+".remote")
	merge, _ := gitx.Run(dir, "config", "--get", "branch."+branch+".merge")
	switch {
	case remote == "" && merge == "":
		t.setUpstream = true
	case remote != "origin" || merge != "refs/heads/"+branch:
		return handoffPushTarget{}, errors.New("the branch's upstream is not origin's " + branch +
			"; push it yourself, or point the upstream there")
	}
	fetch, err := gitx.Run(dir, "remote", "get-url", "origin")
	if err != nil || fetch == "" {
		return handoffPushTarget{}, errHandoffNoRemote
	}
	push, err := gitx.Run(dir, "remote", "get-url", "--push", "--all", "origin")
	if err != nil || push != fetch {
		return handoffPushTarget{}, errors.New("origin pushes somewhere other than where it fetches from " +
			"(pushurl or pushInsteadOf); push it yourself")
	}
	t.url = fetch
	return t, nil
}

var errHandoffNoRemote = errors.New("the working copy has no origin remote")

// commits is how many commits the push would publish. With an upstream that is git's own
// ahead count; without one, the commits no origin ref has.
func (t handoffPushTarget) commits(dir string, ahead int) int {
	if !t.setUpstream && ahead > 0 {
		return ahead
	}
	out, err := gitx.Run(dir, "rev-list", "--count", "refs/heads/"+t.branch, "--not", "--remotes=origin")
	if err != nil {
		return ahead
	}
	n, _ := strconv.Atoi(out)
	return n
}

// args is the push command for sha. The source is the commit id, so a branch that moves
// between the check and the push cannot change what is published; the refspec carries no
// '+', so nothing is forced whatever remote.*.push, push.default or push.followTags say; and
// "--" ends the options, so no value can be read as one.
func (t handoffPushTarget) args(sha string) []string {
	return []string{"push", "--no-follow-tags", "--recurse-submodules=no", "--", "origin",
		sha + ":refs/heads/" + t.branch}
}

// urlUserinfoRe matches the userinfo of a URL in free text, so "https://user:token@host/…"
// in git's or a hook's output loses the credential before the Console shows it.
var urlUserinfoRe = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.\-]*://)[^/\s@]+@`)

// authHeaderRe matches an HTTP authorization value however short or plain it is (a hook that
// prints `git config -l` shows http.extraheader verbatim): the scheme is kept, the
// credential goes. The entropy scan in bridge.ScrubSecrets misses a short Basic value.
var authHeaderRe = regexp.MustCompile(`(?i)((?:proxy-)?authorization\s*[:=]\s*)(?:(basic|bearer|token|digest|negotiate)\s+)?\S+`)

// credAssignRe matches key=value / key: value pairs whose key names a credential, in any case
// (bridge.ScrubSecrets only matches UPPERCASE names).
var credAssignRe = regexp.MustCompile(`(?i)\b([a-z0-9_.\-]*(?:password|passwd|token|secret|credential)[a-z0-9_.\-]*\s*[=:]\s*)\S+`)

// scrubPushOutput makes git's output safe to show: URL credentials, authorization values and
// credential assignments go first, then the token shapes the chat bridges already scrub. The
// tail is kept when it is long.
func scrubPushOutput(s string) string {
	s = urlUserinfoRe.ReplaceAllString(strings.TrimSpace(s), "$1")
	s = authHeaderRe.ReplaceAllString(s, "${1}${2} "+bridge.RedactedMark)
	s = credAssignRe.ReplaceAllString(s, "${1}"+bridge.RedactedMark)
	s = bridge.ScrubSecrets(s)
	if len(s) > handoffPushOutputMax {
		s = "…" + s[len(s)-handoffPushOutputMax:]
	}
	return s
}

// handoffPushRequest is what the owner confirmed (the CP's handoffPushExpect): the push
// happens only while the working copy still shows exactly this.
type handoffPushRequest struct {
	Branch     string `json:"branch"`
	HeadSha    string `json:"headSha"`
	PushRemote string `json:"pushRemote"`
}

// handoffPushResult is the answer of a push. Context is handoff-context rebuilt after the
// push, so the caller judges on the state the push left behind.
type handoffPushResult struct {
	Pushed  bool           `json:"pushed"`
	Remote  string         `json:"remote,omitempty"`
	Branch  string         `json:"branch,omitempty"`
	HeadSha string         `json:"headSha,omitempty"`
	Output  string         `json:"output,omitempty"`
	Context handoffContext `json:"context"`
}

// handoffPushError is a refusal: code is the machine token, message the reason a person
// reads (for a failed push, git's own output).
type handoffPushError struct {
	status        int
	code, message string
}

const handoffPushChanged = "changed"

// pushForHandoff pushes the commit want names when the gate says a push can clear it and the
// working copy still matches want. A gate that is already open pushes nothing.
func pushForHandoff(ctx context.Context, dir string, want handoffPushRequest) (handoffPushResult, *handoffPushError) {
	before := buildHandoffContext(dir)
	switch {
	case before.Vcs != "git":
		return handoffPushResult{}, &handoffPushError{http.StatusConflict, "not_git", "the working copy is not a git repository"}
	case before.Blocked == "" && (before.Branch != want.Branch || before.HeadSha != want.HeadSha):
		return handoffPushResult{}, &handoffPushError{http.StatusConflict, handoffPushChanged,
			"the branch or commit changed after it was shown; check it again"}
	case before.Blocked == "":
		return handoffPushResult{Context: before}, nil
	case before.Blocked == handoffBlockDetached:
		return handoffPushResult{}, &handoffPushError{http.StatusConflict, handoffBlockDetached, "HEAD is detached; there is no branch to push"}
	case !handoffPushClears(before.Blocked):
		return handoffPushResult{}, &handoffPushError{http.StatusConflict, before.Blocked, "the working copy is in a state a push cannot fix"}
	}
	t, err := handoffPushTargetOf(dir, before.Branch)
	if err != nil {
		return handoffPushResult{}, &handoffPushError{http.StatusConflict, "no_push_remote", err.Error()}
	}
	if before.Branch != want.Branch || before.HeadSha != want.HeadSha || sanitizeRemoteURL(t.url) != want.PushRemote {
		return handoffPushResult{}, &handoffPushError{http.StatusConflict, handoffPushChanged,
			"the branch, commit or remote changed after it was shown; check it again"}
	}
	pctx, cancel := context.WithTimeout(ctx, handoffPushTimeout)
	defer cancel()
	raw, err := runPush(pctx, dir, t.args(want.HeadSha))
	out := scrubPushOutput(raw)
	if err != nil {
		if pctx.Err() != nil {
			return handoffPushResult{}, &handoffPushError{http.StatusGatewayTimeout, "push_timeout",
				"the push did not finish within " + handoffPushTimeout.String()}
		}
		if out == "" {
			out = err.Error()
		}
		return handoffPushResult{}, &handoffPushError{http.StatusConflict, "push_rejected", out}
	}
	if t.setUpstream {
		// Pushing a commit id records no upstream (--set-upstream needs a branch as the
		// source), so it is written the way `push -u` would have.
		_, _ = gitx.Run(dir, "config", "branch."+t.branch+".remote", "origin")
		_, _ = gitx.Run(dir, "config", "branch."+t.branch+".merge", "refs/heads/"+t.branch)
	}
	after := buildHandoffContext(dir)
	if after.Branch != want.Branch || after.HeadSha != want.HeadSha {
		// A hook checked out or committed meanwhile: what was published is not what the
		// state now shows, so nothing may be offered on it.
		return handoffPushResult{}, &handoffPushError{http.StatusConflict, handoffPushChanged,
			"the push went through, but the working copy changed while it ran (a hook moved HEAD), " +
				"so no handoff was offered; check it again"}
	}
	return handoffPushResult{Pushed: true, Remote: sanitizeRemoteURL(t.url), Branch: t.branch, HeadSha: want.HeadSha,
		Output: out, Context: after}, nil
}

// runPush runs git in its own process group and kills the whole group when ctx ends: killing
// git alone leaves a pre-push hook running and holding the output pipe open past the timeout.
func runPush(ctx context.Context, dir string, args []string) (string, error) {
	cmd := gitx.Cmd(dir, args...)
	var buf strings.Builder
	cmd.Stdout, cmd.Stderr = &buf, &buf
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = handoffPushWaitDelay
	if err := cmd.Start(); err != nil {
		return "", err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return buf.String(), err
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done // bounded by WaitDelay once git is gone
		return "", ctx.Err()
	}
}

// HandleSessionHandoffPush — POST /sessions/{name}/handoff-push. Called by the CP only, with
// a member-origin mark, from the owner's offer request; no Console route leads here.
func HandleSessionHandoffPush(w http.ResponseWriter, r *http.Request) {
	if !memberorigin.RefuseUnverified(w, r) {
		return
	}
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	var want handoffPushRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, HandoffPushMaxRequest)).Decode(&want); err != nil ||
		want.Branch == "" || want.HeadSha == "" || want.PushRemote == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "branch, headSha and pushRemote are required")
		return
	}
	m, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	if strings.TrimSpace(m.Dir) == "" {
		httpx.WriteErr(w, http.StatusConflict, "no_working_copy", "session has no working copy")
		return
	}
	res, perr := pushForHandoff(r.Context(), m.Dir, want)
	if perr != nil {
		httpx.WriteErr(w, perr.status, perr.code, perr.message)
		return
	}
	res.Context.Repo = handoffRepo(m)
	httpx.WriteJSON(w, http.StatusOK, res)
}
