// browser_support.go — whether a runtime offers Agent Fleet's browser features (the
// browser pane, Chromium attachments, headless verification), decided by the adapter.
//
// The adapter is the one place that decides, because only it knows what its workspace
// container is allowed to do. The decision reaches the two consumers from here:
//
//   - the Console, through BrowserUnavailable in the /api/workspace payload, which is
//     answerable while the workspace is stopped;
//   - the Workspace Agent, through BrowserUnavailableEnv on the container, which makes its
//     browser routes and the af MCP browser tools refuse with browser_unavailable instead
//     of launching a Chromium that cannot start.
//
// Two runtimes decline. kubernetes (ADR 0106, addendum 2026-10-04): a Pod Security
// `restricted` pod runs with NoNewPrivs (the setuid chrome-sandbox cannot elevate) under
// RuntimeDefault seccomp (no user namespaces), so a sandboxed Chromium exits during
// startup. ecs (Fargate): no SYS_ADMIN can be added, so the same default seccomp profile
// refuses namespaces and the setuid helper has no capability to use. ecs-ec2 is not
// among them: it adds SYS_ADMIN, as the docker runtime does. Relaxing the sandbox was
// rejected, so the runtime says so instead.
package runtime

// BrowserUnavailableEnv carries BrowserUnavailable into the workspace container. Its value
// is the runtime id; unset or empty means browser features are available. The Agent reads
// the same name (workspace/agent/internal/browserx/availability.go).
const BrowserUnavailableEnv = "AF_BROWSER_UNAVAILABLE"

// browserless is the optional half of Runtime implemented by an adapter whose workspaces
// cannot run a sandboxed Chromium.
type browserless interface {
	// BrowserUnavailable returns the runtime id to name in the explanation.
	BrowserUnavailable() string
}

// BrowserUnavailable names the runtime that withholds browser features from rt's
// workspace, or "" when rt offers them — every runtime that does not say otherwise.
func BrowserUnavailable(rt Runtime) string {
	if b, ok := rt.(browserless); ok {
		return b.BrowserUnavailable()
	}
	return ""
}
