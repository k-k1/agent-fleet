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

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// writeRuntimeErr turns a managed-runtime launch failure into an HTTP response. Gate-derived
// failures (not logged in / not connected) become 409 + errCodeAgentNotConnected, everything
// else stays 502 + runtime_failed. Both carry err.Error() as the message: a generic code cannot
// express the "why", so the Console shows it alongside via errDetail().
func writeRuntimeErr(w http.ResponseWriter, err error) {
	if errors.Is(err, codex.ErrNotLoggedIn) || errors.Is(err, opencode.ErrNotConnected) {
		httpx.WriteErr(w, http.StatusConflict, errCodeAgentNotConnected, err.Error())
		return
	}
	httpx.WriteErr(w, http.StatusBadGateway, "runtime_failed", err.Error())
}
