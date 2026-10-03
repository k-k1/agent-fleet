package browserx

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// Whether this workspace offers browser features is not the Agent's call: the Control
// Plane's runtime adapter decides (control-plane/internal/runtime/browser_support.go) and
// sets UnavailableEnv on the container. The Agent only obeys it, so the Console, the CP and
// the Agent cannot disagree.
//
// The one runtime that declines is kubernetes (ADR 0106, addendum 2026-10-04). Measured in a
// Pod Security `restricted` pod on GKE: NoNewPrivs=1, CapEff=0, Seccomp=2 and `unshare -U`
// is refused, so neither the setuid chrome-sandbox nor the namespace sandbox can start and
// Chromium exits during Target.setDiscoverTargets. Launching without the sandbox was
// rejected, so nothing here may fall back to --no-sandbox.

// UnavailableEnv is the variable the CP sets to the runtime id when browser features are
// withheld. Unset or empty means available.
const UnavailableEnv = "AF_BROWSER_UNAVAILABLE"

// UnavailableCode is the stable error code of every refusal. The Console and the af MCP
// tools key on it.
const UnavailableCode = "browser_unavailable"

// errBrowserUnavailable is what the launcher returns instead of starting Chromium.
var errBrowserUnavailable = errors.New(UnavailableCode)

// unavailableRuntime reads UnavailableEnv once: the environment of a running Agent does
// not change. A variable so tests can substitute it.
var unavailableRuntime = sync.OnceValue(func() string {
	return strings.TrimSpace(os.Getenv(UnavailableEnv))
})

// Unavailable returns the runtime id that withholds browser features from this
// workspace, or "" when they are available.
func Unavailable() string { return unavailableRuntime() }

// UnavailableMessage is the English explanation shown to agents and in the Agent's error
// body. The CP's refusal (control-plane/browser.go) uses the same wording.
func UnavailableMessage(runtimeID string) string {
	return "Browser features are not available on this workspace runtime (" + runtimeID + "): " +
		"Chromium's sandbox needs user namespaces or a setuid helper, which the runtime's " +
		"restricted pod forbids. See ref/browser-pane.md in the user guide."
}

// writeUnavailable answers a browser route on a workspace without browser features.
// A 4xx on purpose: the Console treats every 5xx as transient and retries it, and no retry
// can succeed here.
func writeUnavailable(w http.ResponseWriter, runtimeID string) {
	httpx.WriteErr(w, http.StatusConflict, UnavailableCode, UnavailableMessage(runtimeID))
}

// availableOnly refuses the request before h runs when browser features are withheld.
// Applied to every route in Routes, so no handler can reach the launcher, the Chromium
// install or an external CDP port on such a workspace.
func availableOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if id := Unavailable(); id != "" {
			writeUnavailable(w, id)
			return
		}
		h(w, r)
	}
}
