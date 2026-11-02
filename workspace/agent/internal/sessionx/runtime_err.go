package sessionx

// Classification of managed-runtime (shared daemon) launch failures on the way back to the
// Console.
//
// Every failure used to come back as one `502 runtime_failed`, which the Console renders as
// "could not start the agent, wait a moment and retry". Once the shared daemon of docs/log/27
// grew a "do not wake it when unauthenticated" gate (codex.ErrNotLoggedIn /
// opencode.ErrNotConnected), permanent causes started arriving as "wait and retry": the user
// keeps pressing until the launch succeeds and the real reason (not signed in) appears nowhere.
//
// So the two gate-derived cases are split out: permanent = 4xx + a dedicated code, transient =
// 502 runtime_failed as before. The 4xx is not only about wording — the Console's isTransientErr
// (core/api/client.ts) treats 5xx as "retryable", so a permanent cause returned as 5xx stays
// retryable no matter how the message reads.
//
// Classification uses only the sentinels each package exports, never a substring match on the
// message: the moment upstream rewords it, the case would silently fall back to transient.
//
// Only the `runtime_failed` side is a literal, because it is emitted from this one place
// (errcodes.go's cross-cutting table lists only the codes the 15 files of main use). The
// permanent side is a new spelling paired with the Console's i18n, so it lives in errcodes.go
// like the rest and arrives through deps (see "do not redefine on the sessionx side" in deps.go).

import (
	"errors"
	"net/http"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// writeRuntimeErr turns a managed-runtime launch failure into an HTTP response. Gate-derived
// failures (not logged in / not connected) become 409 + errCodeAgentNotConnected, everything
// else stays 502 + runtime_failed. Both carry err.Error() as the message: a generic code cannot
// express the "why", so the Console shows it alongside via errDetail().
func writeRuntimeErr(w http.ResponseWriter, err error) {
	if writeStartRefusal(w, err) {
		return
	}
	if errors.Is(err, errDriverSwitching) {
		httpx.WriteErr(w, http.StatusConflict, errCodeDriverSwitching, err.Error())
		return
	}
	if errors.Is(err, opencode.ErrUnsupportedVersion) {
		httpx.WriteErr(w, http.StatusConflict, errCodeAgentUnsupportedVer, err.Error())
		return
	}
	if errors.Is(err, codex.ErrNotLoggedIn) || errors.Is(err, opencode.ErrNotConnected) {
		httpx.WriteErr(w, http.StatusConflict, errCodeAgentNotConnected, err.Error())
		return
	}
	httpx.WriteErr(w, http.StatusBadGateway, "runtime_failed", runtimeErrMessage(err))
}

// runtimeErrMessage is err's message plus, for a child that failed to start, the end of its
// stderr. This response is the one place the tail is rendered: StartError.Error() leaves it
// out so the logs that record start errors never carry it.
func runtimeErrMessage(err error) string {
	msg := err.Error()
	if tail := agents.StartErrStderr(err); tail != "" {
		msg += "\n\n--- stderr (tail) ---\n" + tail
	}
	return msg
}

// resumeManaged is d.Resume for a launch the Agent makes on a caller's behalf: a session that is
// already alive keeps the idempotent Resume, and one that would be started is refused, with the
// reason, when its CLI failed to install. Every managed input path (turn, input, answer, bridge,
// carried) goes through it, so the refusal is not only on the explicit start endpoints.
func resumeManaged(d agents.Driver, m session.Meta) (agents.ThreadHandle, error) {
	if !ManagedAlive(m) {
		if ref := startRefusal(m.Kind); ref != nil {
			return nil, &StartRefusalError{ref}
		}
	}
	return d.Resume(m)
}

// writeTmuxLaunchErr answers a failed Terminal (CLI) launch. An opencode version the Agent
// refuses is permanent and gets the same 409 code as the Managed path; every other failure stays
// 500 tmux_failed.
func writeTmuxLaunchErr(w http.ResponseWriter, err error) {
	if writeUnsupportedVersion(w, err) {
		return
	}
	httpx.WriteErr(w, http.StatusInternalServerError, "tmux_failed", err.Error())
}

// writeUnsupportedVersion answers the refused-opencode-version sentinel with the permanent 409
// code and reports whether it did; callers keep their own generic code for everything else.
func writeUnsupportedVersion(w http.ResponseWriter, err error) bool {
	if !errors.Is(err, opencode.ErrUnsupportedVersion) {
		return false
	}
	httpx.WriteErr(w, http.StatusConflict, errCodeAgentUnsupportedVer, err.Error())
	return true
}

// writeStartRefusal answers err with its typed code when it is a StartRefusalError.
func writeStartRefusal(w http.ResponseWriter, err error) bool {
	var refusal *StartRefusalError
	if !errors.As(err, &refusal) {
		return false
	}
	httpx.WriteErr(w, refusal.Ref.Status, refusal.Ref.Code, refusal.Ref.Message)
	return true
}
