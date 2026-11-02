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

// writeTmuxLaunchErr answers a failed Terminal (CLI) launch. Refusals that waiting does not fix
// (opencode version, not connected) or that a retry in a minute does (codex still holding the
// thread) get the same 409 codes as the Managed path; every other failure stays 500 tmux_failed.
func writeTmuxLaunchErr(w http.ResponseWriter, err error) {
	if writeLaunchRefusal(w, err) {
		return
	}
	httpx.WriteErr(w, http.StatusInternalServerError, "tmux_failed", err.Error())
}

// writeLaunchRefusal answers the sentinels a launch can be refused with, as 409 + a code the
// Console renders from its catalogue, and reports whether it did; callers keep their own
// generic code for everything else. Used by the /start-shaped handlers that do not go through
// writeRuntimeErr (which keeps its own copy of the first two branches for the 502 fallthrough).
func writeLaunchRefusal(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, opencode.ErrUnsupportedVersion):
		httpx.WriteErr(w, http.StatusConflict, errCodeAgentUnsupportedVer, err.Error())
	case errors.Is(err, codex.ErrNotLoggedIn), errors.Is(err, opencode.ErrNotConnected):
		httpx.WriteErr(w, http.StatusConflict, errCodeAgentNotConnected, err.Error())
	case errors.Is(err, codex.ErrThreadReleasing):
		httpx.WriteErr(w, http.StatusConflict, errCodeCodexReleasing, err.Error())
	default:
		return false
	}
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

// Messages of the driver-selection refusals. The Agent does not know the member's language, so
// each is a "ja / en" pair; the Console renders the code from its catalogue and errDetail()
// shows that line alone for these codes.
const (
	msgDriverUnavailable = "managed driver はこの kind ではまだ利用できません / the managed driver is not available for this kind yet"
	msgDriverUnsupported = "managed ドライバはこの kind ではまだ利用できません / the managed driver is not available for this kind yet"
	msgNoTerminalRoute   = "この kind には Terminal(CLI) 実行方式がありません（managed 専用です） / this kind has no Terminal (CLI) execution method (managed only)"
	msgBusySwitch        = "実行中のターンがあります。完了を待つか停止してから切り替えてください / " +
		"a turn is running; wait for it to finish or stop it before switching"
	msgCodexStopFirst = "codex のセッションは停止してからターミナル（CLI）に切り替えてください（停止から 1 分ほどで切り替えられます） / " +
		"stop the codex session first, then switch it to Terminal (CLI) (about a minute after the stop)"
)
