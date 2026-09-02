package browserx

import "net/http"

// Route is one (pattern, handler) pair browserx owns.
type Route struct {
	Pattern string
	Handler http.HandlerFunc
}

// Routes は browserx が持つルートの**唯一の表**。
//
// ⚠️ ここが 1 箇所であることが要点。移送の直後は routes.go の browser 節と
// mux_test.go の表が同じ 15 本を別々に書いており（所有権の都合でウェーブ中は
// そのままにした）、**写しは黙って腐る** —— 片方に 1 本足しても、もう片方の
// テストは全部緑のまま通ってしまう。回収（ADR 0067 決定 2）で 1 本にまとめた。
//
// routes.go 側で「登録されたこと」自体が落ちていないかは、Phase 0 が撮った
// testdata/routes.golden と TestBrowserRoutesMatchAgentRouteTable が見る。
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

// RegisterRoutes は Agent の mux に browserx のルートを登録する。
func RegisterRoutes(mux *http.ServeMux) {
	for _, r := range Routes() {
		mux.HandleFunc(r.Pattern, r.Handler)
	}
}
