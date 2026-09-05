package sessionx

// The coordinates of a handoff (docs/log/77 §77.5).
//
// This endpoint exists so the SERVER finds them out for itself. The recipient is another
// member's Workspace, from which the owner's disk is invisible. So which remote, which
// branch and which commit is being handed over is not written by the model or by the
// Console: this asks git and answers.
//
// Why the remote URL is not a model input (ADR 0057 decision 5): the Console turns this
// value into a clone action. As a structured field the model can write, merely getting it
// to read a poisoned repository would be enough to make the recipient clone an arbitrary
// remote.
//
// The returned `blocked` carries the decision of whether to stop the handoff, not the raw
// facts. If the caller (CP) rebuilt it from ahead and the presence of an upstream, the
// condition would live in two places and would drift.

import (
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// handoffContext folds "is this session's working copy in a state that can be handed
// over" into one answer.
type handoffContext struct {
	Repo          string `json:"repo"`
	Dir           string `json:"dir"`
	WorkingCopyID string `json:"workingCopyId,omitempty"`
	// Vcs is git, svn, or "" (not a working copy). Anything but git has a different
	// notion of push, so no gate is applied there and Blocked stays empty.
	Vcs     string `json:"vcs"`
	Branch  string `json:"branch,omitempty"`
	Remote  string `json:"remote,omitempty"`
	HeadSha string `json:"headSha,omitempty"`
	Ahead   int    `json:"ahead"`
	Dirty   bool   `json:"dirty"`
	// Detached / NoUpstream are the "cannot tell whether it is pushed" states that Ahead
	// cannot express. A branch with no upstream reports Ahead 0 (the `# branch.ab` line
	// is absent entirely), so a gate that looks only at ahead>0 lets a branch that was
	// never pushed straight through.
	Detached   bool `json:"detached,omitempty"`
	NoUpstream bool `json:"noUpstream,omitempty"`
	// Blocked is the reason to stop the handoff ("" = do not stop). Dirty does not stop
	// it; it goes in Warning.
	Blocked string `json:"blocked,omitempty"`
	Warning string `json:"warning,omitempty"`
}

// handoffBlockUnpushed / handoffBlockNoUpstream / handoffBlockDetached / handoffWarnDirty
// are the machine tokens CP and Console match on; the displayed wording is the Console's
// i18n.
const (
	handoffBlockUnpushed   = "unpushed_commits"
	handoffBlockNoUpstream = "no_upstream"
	handoffBlockDetached   = "detached_head"
	handoffWarnDirty       = "uncommitted_changes"
)

// sanitizeRemoteURL strips credentials out of a remote URL, so a URL like
// `https://x-access-token:ghp_…@host/…` never rides along in an offer to another member.
// The SSH form goes through the existing sshToHTTPS first and then the same path, since
// comparison and display both need one shape.
func sanitizeRemoteURL(raw string) string {
	s := gitx.SSHToHTTPS(strings.TrimSpace(raw))
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		// A form that does not parse (a local path, say) has no host and cannot carry
		// credentials, so it is returned as is — but anything containing `@` is dropped,
		// erring on the safe side.
		if strings.Contains(s, "@") {
			return ""
		}
		return s
	}
	u.User = nil
	return u.String()
}

// gitHeadSha is HEAD's commit id, or "" when there is none (a working copy with no
// history).
func gitHeadSha(dir string) string {
	out, err := gitx.Run(dir, "rev-parse", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// gitHasUpstream reports whether the current branch has an upstream configured.
func gitHasUpstream(dir string) bool {
	_, err := gitx.Run(dir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	return err == nil
}

// buildHandoffContext folds dir's state into one answer. When dir is not a git working
// copy, Vcs is left empty (or set to svn) and no gate is evaluated.
func buildHandoffContext(dir string) handoffContext {
	c := handoffContext{Repo: filepath.Base(dir), Dir: dir, WorkingCopyID: gitx.WorkingCopyID(dir)}
	switch {
	case gitx.IsGitRepo(dir):
		c.Vcs = "git"
	case isSvnRepo(dir):
		c.Vcs = "svn"
		return c
	default:
		return c
	}
	st, err := gitx.GitStatus(dir)
	if err != nil {
		// If git cannot answer, never claim the work can be handed over: undecidable
		// falls on the blocking side.
		c.Blocked = handoffBlockNoUpstream
		return c
	}
	c.Branch, c.Dirty, c.Ahead, c.Detached = st.Branch, st.Dirty, st.Ahead, st.Detached
	c.HeadSha = gitHeadSha(dir)
	if origin, ok := gitx.GitOriginURL(dir); ok {
		c.Remote = sanitizeRemoteURL(origin)
	}
	c.NoUpstream = !c.Detached && !gitHasUpstream(dir)
	switch {
	case c.Detached:
		c.Blocked = handoffBlockDetached
	case c.NoUpstream:
		c.Blocked = handoffBlockNoUpstream
	case c.Ahead > 0:
		c.Blocked = handoffBlockUnpushed
	}
	if c.Dirty {
		c.Warning = handoffWarnDirty
	}
	return c
}

// HandleSessionHandoffContext — GET /sessions/{name}/handoff-context.
func HandleSessionHandoffContext(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
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
	httpx.WriteJSON(w, http.StatusOK, buildHandoffContext(m.Dir))
}
