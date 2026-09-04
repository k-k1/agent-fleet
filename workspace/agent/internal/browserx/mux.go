package browserx

import "net/http"

// Route is one (pattern, handler) pair browserx owns.
type Route struct {
	Pattern string
	Handler http.HandlerFunc
}

// Routes is the ONE table of the routes browserx owns.
//
// Being in a single place is the point: the browser section of routes.go and the table in
// mux_test.go used to spell the same 15 routes out separately, and a copy rots silently —
// adding a route to one side leaves every test on the other side green. ADR 0067
// decision 2 folded them into this one table.
//
// Whether the registration itself is still there on the routes.go side is watched by
// testdata/routes.golden and TestBrowserRoutesMatchAgentRouteTable.
func Routes() []Route {
	return []Route{
		// Browser pane — ephemeral BrowserContext + Page ownership and a restricted
		// screencast/input WebSocket. The CP proxies these internal routes verbatim.
		{"POST /browser/pages", HandleBrowserPagesCreate},
		{"GET /browser/pages/{id}", HandleBrowserPageGet},
		{"DELETE /browser/pages/{id}", HandleBrowserPageDelete},
		{"GET /ws/browser", HandleBrowserWebSocket},

		// External-owner Chromium attachments use a separate namespace and manager:
		// detach releases only AF's CDP session and never closes the target/process.
		{"GET /browser/attach-targets", HandleBrowserAttachTargets},
		{"POST /browser/attachments", HandleBrowserAttachmentCreate},
		{"GET /browser/attachments", HandleBrowserAttachmentList},
		{"GET /browser/attachments/{id}", HandleBrowserAttachmentGet},
		{"DELETE /browser/attachments/{id}", HandleBrowserAttachmentDelete},
		{"POST /browser/attachments/{id}/control-mode", HandleBrowserAttachmentControlMode},
		{"GET /browser/attachments/{id}/targets", HandleBrowserAttachmentSiblingTargets},
		{"POST /browser/attachments/{id}/retarget", HandleBrowserAttachmentRetarget},
		{"POST /browser/attachments/{id}/handoff", HandleBrowserAttachmentHandoff},
		{"POST /browser/attachments/{id}/handoff-result", HandleBrowserAttachmentHandoffResult},
		{"GET /ws/browser-attachments", HandleBrowserAttachmentWebSocket},
	}
}

// RegisterRoutes registers browserx's routes on the Agent's mux.
func RegisterRoutes(mux *http.ServeMux) {
	for _, r := range Routes() {
		mux.HandleFunc(r.Pattern, r.Handler)
	}
}
