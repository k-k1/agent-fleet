package mcpx

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/browserx"
)

func chromiumAttachmentIDInputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"attachment_id": map[string]any{"type": "string", "minLength": 1},
		},
		"required": []string{"attachment_id"},
	}
}

func chromiumControlModeSchema() map[string]any {
	return map[string]any{"type": "string", "enum": []string{"view-only", "user-control", "locked"}}
}

func chromiumTargetsOutputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"targets": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"target_id": map[string]any{"type": "string"},
						"title":     map[string]any{"type": "string"},
						"url":       map[string]any{"type": "string"},
					},
					"required": []string{"target_id", "title", "url"},
				},
			},
			"browser_id": map[string]any{"type": "string"},
		},
		"required": []string{"targets"},
	}
}

func chromiumAttachOutputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"attachment_id": map[string]any{"type": "string"},
			"open_url":      map[string]any{"type": "string"},
			"expires_at":    map[string]any{"type": "string"},
		},
		"required": []string{"attachment_id", "open_url", "expires_at"},
	}
}

func chromiumAttachmentOutputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"attachment_id":    map[string]any{"type": "string"},
			"state":            map[string]any{"type": "string"},
			"viewer_connected": map[string]any{"type": "boolean"},
			"control_mode":     chromiumControlModeSchema(),
			"action_result":    map[string]any{"type": "string", "enum": []string{"pending", "completed", "cancelled"}},
			"expires_at":       map[string]any{"type": "string"},
		},
		"required": []string{"attachment_id", "state"},
	}
}

func chromiumActionRequestOutputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"attachment_id": map[string]any{"type": "string"},
			"result":        map[string]any{"type": "string", "enum": []string{"pending", "completed", "cancelled"}},
			"control_mode":  chromiumControlModeSchema(),
		},
		"required": []string{"attachment_id", "result"},
	}
}

func isChromiumWriteTool(name string) bool {
	switch name {
	case "attach_chromium", "detach_chromium", "request_browser_action",
		"get_browser_action_result", "set_chromium_control_mode":
		return true
	default:
		return false
	}
}

func isChromiumReadTool(name string) bool {
	switch name {
	case "list_chromium_targets", "get_chromium_attachment":
		return true
	default:
		return false
	}
}

func mcpChromiumWriteEnabled() bool {
	return writeEnabled() || (selfReportOnly() && sessionChromiumEnabled())
}

// mcpChromiumToolInScope reports whether name is a Chromium tool this server's flags would
// advertise when the workspace has browser features: the read pair on the assistant surface or
// under --chromium-attach, the mutating five wherever mcpChromiumWriteEnabled allows them.
func mcpChromiumToolInScope(name string) bool {
	if isChromiumReadTool(name) {
		return !selfReportOnly() || sessionChromiumEnabled()
	}
	return isChromiumWriteTool(name) && mcpChromiumWriteEnabled()
}

func mcpListChromiumTargets(id json.RawMessage, port int) []byte {
	if port < 1 || port > 65535 {
		return mcpToolErr(id, "portには1〜65535のChromium remote-debugging portが必要です")
	}
	body, err := agentGET("/browser/attach-targets?port=" + strconv.Itoa(port))
	if err != nil {
		return mcpChromiumToolErr(id, "Chromium target一覧の取得", err)
	}
	var response browserx.BrowserAttachTargetsResponse
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		return mcpToolErr(id, "Agentが不正なChromium target一覧を返しました")
	}
	targets := make([]any, 0, len(response.Targets))
	for _, target := range response.Targets {
		if target.TargetID == "" {
			continue
		}
		targets = append(targets, map[string]any{
			"target_id": target.TargetID,
			"title":     target.Title,
			"url":       target.URL,
		})
	}
	result := map[string]any{"targets": targets}
	// browser_id lets the caller check that the Chromium on this port really is the instance
	// it started itself. It is neither a CDP endpoint nor a credential.
	if response.BrowserID != "" {
		result["browser_id"] = response.BrowserID
	}
	return mcpStructuredResult(id, result)
}

func mcpGetChromiumAttachment(id json.RawMessage, attachmentID string) []byte {
	if attachmentID == "" {
		return mcpToolErr(id, "attachment_idが必要です")
	}
	body, err := agentGET("/browser/attachments/" + url.PathEscape(attachmentID))
	if err != nil {
		return mcpChromiumToolErr(id, "Chromium attachment状態の取得", err)
	}
	status, err := chromiumAttachmentStatus(body, attachmentID)
	if err != nil {
		return mcpToolErr(id, "Agentが不正なChromium attachment状態を返しました")
	}
	return mcpStructuredResult(id, status)
}

func mcpAttachChromium(id json.RawMessage, port int, targetID, expectedBrowserID, label string) []byte {
	if port < 1 || port > 65535 {
		return mcpToolErr(id, "portには1〜65535のChromium remote-debugging portが必要です")
	}
	if targetID == "" {
		return mcpToolErr(id, "target_idが必要です。先にlist_chromium_targetsで確認してください")
	}
	if len(label) > browserx.BrowserAttachmentMaxLabel || !utf8.ValidString(label) {
		return mcpToolErr(id, "labelは256 byte以内のUTF-8文字列にしてください")
	}
	req := map[string]any{"port": port, "targetId": targetID}
	if expectedBrowserID != "" {
		browserID := browserx.NormalizeCDPBrowserID(expectedBrowserID)
		if browserID == "" {
			return mcpToolErr(id, "expected_browser_idはDevToolsActivePortの2行目（/devtools/browser/<GUID>）かそのGUIDを渡してください")
		}
		req["browserId"] = browserID
	}
	reqBody, _ := json.Marshal(req)
	// The P1 REST request body is fixed to port/targetId/viewport. Carry the
	// MCP-only display label over the authenticated loopback hop separately.
	headers := map[string]string{}
	if label != "" {
		headers[browserx.BrowserAttachmentLabelHeader] = base64.RawURLEncoding.EncodeToString([]byte(label))
	}
	body, err := agentPOSTHeaders("/browser/attachments", reqBody, headers)
	if err != nil {
		return mcpChromiumToolErr(id, "Chromium attachmentの作成", err)
	}
	var response browserx.BrowserAttachmentResponse
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		return mcpToolErr(id, "Agentが不正なChromium attachment結果を返しました")
	}
	if response.ID == "" || response.OpenURL == "" || response.ExpiresAt == nil {
		return mcpToolErr(id, "AgentのChromium attachment結果にattachment ID、open URL、またはexpiryがありません")
	}
	if !validChromiumOpenURL(response.OpenURL, response.ID) {
		return mcpToolErr(id, "Agentが不正なChromium attachment open URLを返しました")
	}
	// Even if the Agent response grows title / url / port / target / CDP fields, none of
	// them are passed on to MCP.
	return mcpStructuredResult(id, map[string]any{
		"attachment_id": response.ID,
		"open_url":      response.OpenURL,
		"expires_at":    response.ExpiresAt.Format(time.RFC3339Nano),
	})
}

func mcpDetachChromium(id json.RawMessage, attachmentID string) []byte {
	if attachmentID == "" {
		return mcpToolErr(id, "attachment_idが必要です")
	}
	if _, err := agentDo(http.MethodDelete, "/browser/attachments/"+url.PathEscape(attachmentID), nil); err != nil {
		return mcpChromiumToolErr(id, "Chromium attachmentの切断", err)
	}
	return mcpStructuredResult(id, map[string]any{
		"attachment_id": attachmentID,
		"detached":      true,
	})
}

func mcpRequestBrowserAction(id json.RawMessage, attachmentID, message, completionLabel string, allowCancel *bool, controlMode string) []byte {
	if attachmentID == "" || strings.TrimSpace(message) == "" {
		return mcpToolErr(id, "attachment_idとmessageが必要です")
	}
	if controlMode != "" && !validChromiumControlMode(controlMode) {
		return mcpToolErr(id, "control_modeはview-only、user-control、lockedのいずれかです")
	}
	req := map[string]any{"message": message, "allowCancel": false}
	if completionLabel != "" {
		req["completionLabel"] = completionLabel
	}
	if allowCancel != nil {
		req["allowCancel"] = *allowCancel
	}
	if controlMode != "" {
		req["controlMode"] = controlMode
	}
	// Best-effort: without a valid owning session there is simply nobody to notify
	// once a human responds — the handoff still works exactly as before, the tool
	// call must not fail over this. See browser_handoff_ledger.go.
	if self, err := mcpOwningSession(); err == nil {
		req["sessionName"] = self
	}
	reqBody, _ := json.Marshal(req)
	body, err := AgentPOST("/browser/attachments/"+url.PathEscape(attachmentID)+"/handoff", reqBody)
	if err != nil {
		return mcpChromiumToolErr(id, "ブラウザ操作依頼の作成", err)
	}
	var response browserx.BrowserAttachmentResponse
	if json.Unmarshal([]byte(body), &response) != nil || response.ID != attachmentID ||
		response.Handoff == nil || response.Handoff.Result != "pending" {
		return mcpToolErr(id, "Agentが不正なブラウザ操作依頼結果を返しました")
	}
	result := map[string]any{"attachment_id": attachmentID, "result": "pending"}
	if controlMode != "" {
		result["control_mode"] = controlMode
	}
	return mcpStructuredResult(id, result)
}

func mcpGetBrowserActionResult(id json.RawMessage, attachmentID string) []byte {
	if attachmentID == "" {
		return mcpToolErr(id, "attachment_idが必要です")
	}
	body, err := agentGET("/browser/attachments/" + url.PathEscape(attachmentID))
	if err != nil {
		return mcpChromiumToolErr(id, "ブラウザ操作結果の取得", err)
	}
	var response browserx.BrowserAttachmentResponse
	if json.Unmarshal([]byte(body), &response) != nil || response.ID != attachmentID {
		return mcpToolErr(id, "Agentが不正なブラウザ操作結果を返しました")
	}
	result := ""
	if response.Handoff != nil {
		result = response.Handoff.Result
	}
	if result == "" {
		result = "pending"
	} else if !validChromiumActionResult(result) {
		return mcpToolErr(id, "Agentが不正なブラウザ操作結果を返しました")
	}
	return mcpStructuredResult(id, map[string]any{
		"attachment_id": attachmentID,
		"result":        result,
	})
}

func mcpSetChromiumControlMode(id json.RawMessage, attachmentID, controlMode string) []byte {
	if attachmentID == "" {
		return mcpToolErr(id, "attachment_idが必要です")
	}
	if !validChromiumControlMode(controlMode) {
		return mcpToolErr(id, "control_modeはview-only、user-control、lockedのいずれかです")
	}
	reqBody, _ := json.Marshal(map[string]string{"controlMode": controlMode})
	body, err := AgentPOST("/browser/attachments/"+url.PathEscape(attachmentID)+"/control-mode", reqBody)
	if err != nil {
		return mcpChromiumToolErr(id, "Chromium control modeの変更", err)
	}
	var response browserx.BrowserAttachmentResponse
	if json.Unmarshal([]byte(body), &response) != nil || response.ID != attachmentID || response.ControlMode != controlMode {
		return mcpToolErr(id, "Agentが不正なChromium control mode結果を返しました")
	}
	return mcpStructuredResult(id, map[string]any{
		"attachment_id": attachmentID,
		"control_mode":  controlMode,
	})
}

func validChromiumControlMode(mode string) bool {
	return mode == "view-only" || mode == "user-control" || mode == "locked"
}

func validChromiumActionResult(result string) bool {
	return result == "pending" || result == "completed" || result == "cancelled"
}

func validChromiumOpenURL(openURL, attachmentID string) bool {
	parsed, err := url.Parse(openURL)
	if err != nil || parsed.Scheme != "" || parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	return parsed.Path == "/open/browser-attachment/"+url.PathEscape(attachmentID)
}

func chromiumAttachmentStatus(body, fallbackID string) (map[string]any, error) {
	var response browserx.BrowserAttachmentResponse
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		return nil, err
	}
	if response.ID == "" || response.ID != fallbackID || response.State == "" || !validChromiumControlMode(response.ControlMode) {
		return nil, errors.New("missing state")
	}
	result := map[string]any{
		"attachment_id":    response.ID,
		"state":            response.State,
		"viewer_connected": response.Viewer,
		"control_mode":     response.ControlMode,
	}
	if response.Handoff != nil && validChromiumActionResult(response.Handoff.Result) {
		result["action_result"] = response.Handoff.Result
	}
	if response.ExpiresAt != nil {
		result["expires_at"] = response.ExpiresAt.Format(time.RFC3339Nano)
	}
	return result, nil
}

// chromiumToolErrHints adds a next step for the codes whose remedy is unambiguous. A port
// collision means the caller was about to attach to another session's Chromium, so the fix
// is on the launch side, not a retry.
//
// mcpChromiumToolErr below keeps raw Agent/CDP details out of the model-visible result:
// stable Agent error codes remain useful, endpoint URLs, ports and target IDs do not.
var chromiumToolErrHints = map[string]string{
	"cdp_port_ambiguous": "そのportは複数プロセスがlistenしています。別セッションのChromiumへ繋がる恐れがあるため中断しました。" +
		"自分のChromiumを--remote-debugging-port=0で起動し直し、<user-data-dir>/DevToolsActivePortの1行目のportを使ってください。",
	"cdp_browser_mismatch": "そのportに居るChromiumはexpected_browser_idの個体ではありません。portが他プロセスに取られています。" +
		"--remote-debugging-port=0で起動し直し、DevToolsActivePortのport/GUIDを使ってください。",
}

func mcpChromiumToolErr(id json.RawMessage, action string, err error) []byte {
	var httpErr *agentHTTPError
	if errors.As(err, &httpErr) {
		if httpErr.hasCode(browserx.UnavailableCode) {
			return mcpToolErr(id, chromiumUnavailableText(httpErr.Body))
		}
		if code := httpErr.code(); code != "" {
			if hint := chromiumToolErrHints[code]; hint != "" {
				return mcpToolErr(id, fmt.Sprintf("%sに失敗しました（Agent API %d, code=%s）。%s", action, httpErr.StatusCode, code, hint))
			}
			return mcpToolErr(id, fmt.Sprintf("%sに失敗しました（Agent API %d, code=%s）", action, httpErr.StatusCode, code))
		}
		return mcpToolErr(id, fmt.Sprintf("%sに失敗しました（Agent API %d）", action, httpErr.StatusCode))
	}
	return mcpToolErr(id, action+"に失敗しました（Workspace Agentへ接続できません）")
}

// chromiumUnavailableText is the result of a browser tool on a workspace runtime without
// browser features: the Agent's own explanation plus what to do instead, so the model stops
// rather than starting Chromium itself (which fails there too) or reaching for
// --no-sandbox (which was decided against).
func chromiumUnavailableText(body string) string {
	var b struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	msg := "Browser features are not available on this workspace runtime."
	if json.Unmarshal([]byte(body), &b) == nil && b.Error.Message != "" {
		msg = b.Error.Message
	}
	return chromiumUnavailableTextFor(msg)
}

// chromiumUnavailableTextFor wraps the explanation msg in the browser_unavailable result.
func chromiumUnavailableTextFor(msg string) string {
	return "code=" + browserx.UnavailableCode + ": " + msg + " Do not start Chromium here, with or without " +
		"--no-sandbox, and do not retry. Verify with tests, curl or the lightweight preview instead, " +
		"and tell the user the browser pane is unavailable on this workspace."
}
