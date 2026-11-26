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
	"fmt"
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
	if errors.Is(err, errSessionRetired) {
		// Permanent until a person starts the session: not a runtime fault to retry.
		httpx.WriteErr(w, http.StatusConflict, "session_retired", err.Error())
		return
	}
	if errors.Is(err, errActNotRecorded) {
		httpx.WriteErr(w, http.StatusInternalServerError, "act_not_recorded", err.Error())
		return
	}
	if errors.Is(err, errRetireClearFailed) {
		httpx.WriteErr(w, http.StatusInternalServerError, "retire_clear_failed", err.Error())
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
//
// A retired session is not started here: this is the form for callers that are automatic or that
// deliver nothing a person typed. resumeManagedDeliberate is the form for input a person (or one
// of their sessions or connections) sent.
func resumeManaged(d agents.Driver, m session.Meta) (agents.ThreadHandle, error) {
	return resumeManagedAs(d, m, true, false)
}

// resumeManagedFor is resumeManaged; input says whether the handle is wanted to deliver input
// (a message of any origin, an answer, a queued prompt). Input to a live runtime is judged by the
// credential policy on the session's evidence at every delivery (ADR 0138 D12), so a message is
// refused with the reason, never delivered to a runtime the sweep is about to halt. A control that
// delivers nothing (an interrupt, a queue edit) is not: stopping work is never refused.
func resumeManagedFor(d agents.Driver, m session.Meta, input bool) (agents.ThreadHandle, error) {
	return resumeManagedAs(d, m, input, false)
}

// resumeManagedDeliberate is resumeManaged for input of a deliberate origin: the member's own
// message, a peer's or an operator's (deliberateInput, deliberateOrigin). Such input starts a
// retired, stopped session again, in /start's order.
func resumeManagedDeliberate(d agents.Driver, m session.Meta) (agents.ThreadHandle, error) {
	return resumeManagedAs(d, m, true, true)
}

// errActNotRecorded and errRetireClearFailed are the two ways a deliberate start of a retired
// session can fail after it was decided on; writeRuntimeErr answers them with /start's codes.
var (
	errActNotRecorded    = errors.New("act_not_recorded: the start was not made, because it could not be recorded as a person's")
	errRetireClearFailed = errors.New("retire_clear_failed: the session started, but its retired mark could not be cleared")
)

// resumeManagedAs is the one place a Managed session is started for input. deliberate says the
// input is of a deliberate origin; only then may it start a retired, stopped session.
//
// A session that is alive, or not retired, is resumed as it always was. A retired, stopped one is
// refused (errSessionRetired) unless deliberate and input: then it follows /start's order under
// the read side of the retire gate, held from the check to the end of the launch: record the act,
// launch, and only after its own launch take the mark off. A launch that fails leaves the mark as
// it was. A meta that cannot be read is in doubt and is not launched. The read side is taken here
// and must not be held by the caller (a second read on one path can deadlock behind a waiting
// retire); the caller holds the lifecycle gate, which comes first in the lock order.
func resumeManagedAs(d agents.Driver, m session.Meta, input, deliberate bool) (agents.ThreadHandle, error) {
	g := retireGate(m.Name)
	g.RLock()
	defer g.RUnlock()
	retired := false
	if !managedAlive(m) {
		cur, ok := readMetaStable(m.Name)
		switch {
		case ok && isRetired(cur):
			retired = true
		case !ok && metaFileExists(m.Name):
			return nil, errSessionRetired // unreadable is in doubt: not launched
		}
		if retired && !(deliberate && input) {
			return nil, errSessionRetired
		}
		if ref := startRefusalOf(m); ref != nil {
			return nil, &StartRefusalError{ref}
		}
	} else if input {
		if ref := storedLaunchPolicyRefusal(m); ref != nil {
			return nil, &StartRefusalError{&SpawnRefusal{Status: ref.Status, Code: ref.Code, Message: ref.Message}}
		}
	}
	if retired {
		// Before the launch, and refused when it cannot be written: a start that goes unrecorded
		// is one a retire decided on an older list would stop.
		if err := recordPersonAct(m.Name); err != nil {
			return nil, fmt.Errorf("%w: %v", errActNotRecorded, err)
		}
	}
	// Reserved even for a runtime that looked alive above: it can die before Resume, which then
	// starts it again (reserveLaunch).
	src := &m
	if !metaFileExists(m.Name) {
		src = launchUnrecorded // starts as on main; a delete shelves a Managed session, never removes its meta
	}
	release, ref := reserveLaunch(m, src)
	if ref != nil {
		return nil, &StartRefusalError{ref}
	}
	defer release()
	h, err := d.Resume(m)
	if err != nil {
		return nil, err
	}
	if retired {
		if err := clearRetired(m.Name); err != nil {
			return nil, fmt.Errorf("%w: %v", errRetireClearFailed, err)
		}
	}
	return h, nil
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
		// A launch refused by its route (a settings conflict found where the working copy is
		// known) keeps the route's own code instead of a generic launch failure.
		var rr *agents.RouteRefusal
		if !errors.As(err, &rr) {
			return false
		}
		httpx.WriteErr(w, rr.Status, rr.Code, rr.Message)
	}
	return true
}

// writeStartRefusal answers err with its typed code when it is a StartRefusalError.
func writeStartRefusal(w http.ResponseWriter, err error) bool {
	var refusal *StartRefusalError
	if !errors.As(err, &refusal) {
		var rr *agents.RouteRefusal
		if !errors.As(err, &rr) {
			return false
		}
		httpx.WriteErr(w, rr.Status, rr.Code, rr.Message)
		return true
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
