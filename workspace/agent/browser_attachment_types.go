package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	browserAttachmentIDPrefix      = "ba_"
	browserAttachmentLabelHeader   = "X-AF-Browser-Attachment-Label"
	browserAttachmentMaxTargetID   = 1024
	browserAttachmentMaxTitle      = 1024
	browserAttachmentMaxURL        = 16 * 1024
	browserAttachmentMaxHandoff    = 8 * 1024
	browserAttachmentMaxLabel      = 256
	browserAttachmentDiscoveryBody = 2 << 20
)

const (
	attachmentStateAttached       = "attached"
	attachmentStateViewerOpen     = "viewer-open"
	attachmentStateTargetClosed   = "target-closed"
	attachmentStateDisconnected   = "disconnected"
	attachmentStateUnsupportedURL = "unsupported-target-url"
)

const (
	attachmentControlViewOnly = "view-only"
	attachmentControlUser     = "user-control"
	attachmentControlLocked   = "locked"
)

type browserAttachTarget struct {
	TargetID string `json:"targetId"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	URL      string `json:"url"`
}

type browserAttachTargetsResponse struct {
	Targets []browserAttachTarget `json:"targets"`
	// BrowserID identifies the Chromium instance answering on this port (the GUID
	// Chromium writes as line 2 of <user-data-dir>/DevToolsActivePort). A caller
	// that launched its own Chromium passes it back as browserId on attach so a
	// port collision cannot silently hand it a different browser.
	BrowserID string `json:"browserId,omitempty"`
}

type browserAttachmentCreateRequest struct {
	Port     int                    `json:"port"`
	TargetID string                 `json:"targetId"`
	Viewport browserViewportRequest `json:"viewport"`
	// BrowserID is optional; when set the attach fails unless the endpoint really
	// is that Chromium instance.
	BrowserID string `json:"browserId,omitempty"`
	Label     string `json:"-"`
}

type browserAttachmentHandoffRequest struct {
	Message         string `json:"message"`
	CompletionLabel string `json:"completionLabel"`
	AllowCancel     bool   `json:"allowCancel"`
	ControlMode     string `json:"controlMode"`
}

type browserAttachmentHandoffResultRequest struct {
	Result string `json:"result"`
}

type browserAttachmentControlModeRequest struct {
	ControlMode string `json:"controlMode"`
}

// browserAttachmentRetargetRequest switches an existing attachment onto a
// different CDP target on the same port, keeping its id/URL (and so its
// already-open Console pane) instead of forcing a close-and-reattach cycle.
type browserAttachmentRetargetRequest struct {
	TargetID string `json:"targetId"`
	// BrowserID is optional; when set the retarget fails unless the endpoint
	// really is that Chromium instance (same guard as Create).
	BrowserID string `json:"browserId,omitempty"`
}

// browserAttachmentSiblingTarget is a candidate for Retarget: another target
// on the SAME Chromium instance this attachment is already on. Current marks
// the one the attachment is presently attached to so the Console can show it
// distinctly, since (unlike browserAttachTarget from plain discovery) the
// caller has no other way to tell which of several open tabs — often with an
// identical title — is the one it is already looking at.
type browserAttachmentSiblingTarget struct {
	TargetID string `json:"targetId"`
	Title    string `json:"title"`
	URL      string `json:"url"`
	Current  bool   `json:"current"`
}

type browserAttachmentSiblingTargetsResponse struct {
	Targets []browserAttachmentSiblingTarget `json:"targets"`
}

type browserAttachmentHandoffResponse struct {
	Message         string `json:"message"`
	CompletionLabel string `json:"completionLabel"`
	AllowCancel     bool   `json:"allowCancel"`
	ControlMode     string `json:"controlMode"`
	Result          string `json:"result"`
}

type browserAttachmentResponse struct {
	ID          string                            `json:"id"`
	State       string                            `json:"state"`
	Title       string                            `json:"title,omitempty"`
	URL         string                            `json:"url,omitempty"`
	OpenURL     string                            `json:"openUrl"`
	ExpiresAt   *time.Time                        `json:"expiresAt,omitempty"`
	Viewer      bool                              `json:"viewer"`
	ControlMode string                            `json:"controlMode"`
	Handoff     *browserAttachmentHandoffResponse `json:"handoff,omitempty"`
}

// browserAttachmentListResponse backs the Console's "which Chromium pages am I
// still attached to?" entry — the way back in when the action link has scrolled
// out of the mirror or the pane was closed.
type browserAttachmentListResponse struct {
	Attachments []browserAttachmentResponse `json:"attachments"`
}

type browserAttachmentAPIError struct {
	Status  int
	Code    string
	Message string
	Cause   error
}

func (e *browserAttachmentAPIError) Error() string {
	if e.Cause != nil {
		return e.Code + ": " + e.Cause.Error()
	}
	return e.Code
}

func attachmentError(status int, code, message string, cause error) error {
	return &browserAttachmentAPIError{Status: status, Code: code, Message: message, Cause: cause}
}

func validateCDPPort(port int) error {
	if port < 1 || port > 65535 || reservedBrowserAgentPort(port) || reservedControlPlanePort(port) {
		return attachmentError(http.StatusBadRequest, "bad_cdp_port",
			"port must be 1..65535 and must not be the workspace agent port", nil)
	}
	return nil
}

func reservedControlPlanePort(port int) bool {
	u, err := url.Parse(os.Getenv("AF_CP_BASE_URL"))
	if err != nil || u.Hostname() == "" {
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return false
	}
	p := u.Port()
	if p == "" {
		if u.Scheme == "https" {
			p = "443"
		} else {
			p = "80"
		}
	}
	configured, err := strconv.Atoi(p)
	return err == nil && configured == port
}

func normalizeAttachmentViewport(v browserViewportRequest) (browserViewport, error) {
	if v.Width == 0 && v.Height == 0 && v.DeviceScaleFactor == 0 {
		v = browserViewportRequest{Width: 1280, Height: 900, DeviceScaleFactor: 1}
	}
	viewport, err := normalizeBrowserViewport(v)
	if err != nil {
		return browserViewport{}, attachmentError(http.StatusBadRequest, "bad_browser_attachment", "invalid viewport", err)
	}
	return viewport, nil
}

type cdpDiscovery struct {
	DebuggerURL string
	BrowserID   string
	Targets     []browserAttachTarget
}

func discoverCDPTargets(port int, timeout time.Duration) (cdpDiscovery, error) {
	if err := validateCDPPort(port); err != nil {
		return cdpDiscovery{}, err
	}
	if err := ensureUnambiguousCDPPort(port); err != nil {
		return cdpDiscovery{}, err
	}
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	base := "http://127.0.0.1:" + strconv.Itoa(port)
	versionBody, err := getCDPDiscovery(client, base+"/json/version")
	if err != nil {
		return cdpDiscovery{}, err
	}
	var version struct {
		Browser              string `json:"Browser"`
		ProtocolVersion      string `json:"Protocol-Version"`
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if json.Unmarshal(versionBody, &version) != nil || version.Browser == "" ||
		version.ProtocolVersion == "" || version.WebSocketDebuggerURL == "" {
		return cdpDiscovery{}, attachmentError(http.StatusUnprocessableEntity,
			"cdp_endpoint_invalid", "endpoint is not a Chromium CDP endpoint", nil)
	}
	if _, err := reconstructCDPWebSocketURL(port, version.WebSocketDebuggerURL); err != nil {
		return cdpDiscovery{}, attachmentError(http.StatusUnprocessableEntity,
			"cdp_endpoint_invalid", "endpoint advertised an invalid debugger socket", err)
	}
	listBody, err := getCDPDiscovery(client, base+"/json/list")
	if err != nil {
		return cdpDiscovery{}, err
	}
	var rawTargets []struct {
		ID    string `json:"id"`
		Type  string `json:"type"`
		Title string `json:"title"`
		URL   string `json:"url"`
	}
	if json.Unmarshal(listBody, &rawTargets) != nil {
		return cdpDiscovery{}, attachmentError(http.StatusUnprocessableEntity,
			"cdp_endpoint_invalid", "endpoint returned an invalid target list", nil)
	}
	targets := make([]browserAttachTarget, 0, len(rawTargets))
	for _, target := range rawTargets {
		if target.Type != "page" || target.ID == "" || len(target.ID) > browserAttachmentMaxTargetID || !utf8.ValidString(target.ID) {
			continue
		}
		targets = append(targets, browserAttachTarget{
			TargetID: target.ID,
			Type:     "page",
			Title:    truncateBrowserText(target.Title, browserAttachmentMaxTitle),
			URL:      truncateBrowserText(target.URL, browserAttachmentMaxURL),
		})
	}
	return cdpDiscovery{
		DebuggerURL: version.WebSocketDebuggerURL,
		BrowserID:   cdpBrowserID(version.WebSocketDebuggerURL),
		Targets:     targets,
	}, nil
}

// ensureUnambiguousCDPPort refuses a port that more than one process is
// listening on. Chromium does not fail that launch — the loser quietly takes the
// other loopback family — so without this check the caller would attach to
// whoever won 127.0.0.1, which in a shared container is another session's
// browser. The check is advisory: when /proc cannot answer (no procfs, sockets
// we may not attribute) it stays silent rather than blocking a valid attach.
func ensureUnambiguousCDPPort(port int) error {
	listeners, ok := lookupCDPPortListeners(port)
	if !ok || len(listeners) < 2 {
		return nil
	}
	return attachmentError(http.StatusConflict, "cdp_port_ambiguous",
		"port "+strconv.Itoa(port)+" has more than one listening process ("+describeCDPPortListeners(listeners)+
			"); launch Chromium with --remote-debugging-port=0 and use the port from <user-data-dir>/DevToolsActivePort", nil)
}

// cdpBrowserID extracts the instance GUID from ws://…/devtools/browser/<guid>.
func cdpBrowserID(debuggerURL string) string {
	u, err := url.Parse(debuggerURL)
	if err != nil {
		return ""
	}
	_, id, found := strings.Cut(u.Path, "/devtools/browser/")
	if !found {
		return ""
	}
	return truncateBrowserText(id, browserAttachmentMaxTargetID)
}

// normalizeCDPBrowserID accepts what a caller can actually copy: the bare GUID,
// the "/devtools/browser/<guid>" second line of DevToolsActivePort, or the full
// webSocketDebuggerUrl.
func normalizeCDPBrowserID(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "/devtools/browser/") {
		return cdpBrowserID(raw)
	}
	if strings.ContainsAny(raw, "/:") || len(raw) > browserAttachmentMaxTargetID || !utf8.ValidString(raw) {
		return ""
	}
	return raw
}

func getCDPDiscovery(client *http.Client, endpoint string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, attachmentError(http.StatusUnprocessableEntity, "cdp_endpoint_invalid", "invalid CDP endpoint", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, attachmentError(http.StatusBadGateway, "cdp_unreachable", "CDP endpoint is unreachable", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, attachmentError(http.StatusUnprocessableEntity, "cdp_endpoint_invalid",
			"endpoint is not a Chromium CDP endpoint", fmt.Errorf("HTTP %d", resp.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, browserAttachmentDiscoveryBody+1))
	if err != nil {
		return nil, attachmentError(http.StatusBadGateway, "cdp_unreachable", "cannot read CDP endpoint", err)
	}
	if len(body) > browserAttachmentDiscoveryBody {
		return nil, attachmentError(http.StatusUnprocessableEntity, "cdp_endpoint_invalid", "CDP response is too large", nil)
	}
	return body, nil
}

func supportedAttachmentURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil
}

func validAttachmentID(id string) bool {
	if !strings.HasPrefix(id, browserAttachmentIDPrefix) || len(id) != len(browserAttachmentIDPrefix)+32 {
		return false
	}
	for _, c := range id[len(browserAttachmentIDPrefix):] {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

func validateHandoffRequest(req browserAttachmentHandoffRequest) error {
	if req.Message == "" || len(req.Message) > browserAttachmentMaxHandoff || !utf8.ValidString(req.Message) {
		return attachmentError(http.StatusBadRequest, "bad_browser_handoff", "message must be non-empty valid UTF-8", nil)
	}
	if req.CompletionLabel == "" {
		req.CompletionLabel = "操作完了"
	}
	if len(req.CompletionLabel) > browserAttachmentMaxLabel || !utf8.ValidString(req.CompletionLabel) {
		return attachmentError(http.StatusBadRequest, "bad_browser_handoff", "completionLabel is invalid", nil)
	}
	if !validAttachmentControlMode(req.ControlMode) {
		return attachmentError(http.StatusBadRequest, "bad_control_mode", "controlMode must be view-only, user-control, or locked", nil)
	}
	return nil
}

func validAttachmentControlMode(mode string) bool {
	return mode == attachmentControlViewOnly || mode == attachmentControlUser || mode == attachmentControlLocked
}

func asAttachmentAPIError(err error) *browserAttachmentAPIError {
	var apiErr *browserAttachmentAPIError
	if errors.As(err, &apiErr) {
		return apiErr
	}
	return &browserAttachmentAPIError{Status: http.StatusBadGateway, Code: "cdp_disconnected", Message: "Chromium disconnected", Cause: err}
}
