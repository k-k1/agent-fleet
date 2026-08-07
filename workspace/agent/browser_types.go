package main

import (
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	browserAgentPort      = 7700
	browserMaxWidth       = 1600
	browserMaxHeight      = 1200
	browserMaxPathBytes   = 4096
	browserMaxTextBytes   = 16 * 1024
	browserMaxConsoleText = 8 * 1024
	// Zoom-to-fit bounds the LAYOUT viewport, not the image (see
	// fitLayoutViewport). 4000 CSS px covers desktop sites without letting a
	// pathological page ask Chromium to lay out an unbounded canvas.
	browserMaxLayoutWidth  = 4000
	browserMaxLayoutHeight = 4000
	browserFitSlack        = 8
	// A selection copied out of the page. The user can already read it on
	// screen; the cap only keeps one Ctrl+C from shipping a whole document.
	browserMaxSelectionBytes = 128 * 1024
)

type browserViewportRequest struct {
	Width             float64 `json:"width"`
	Height            float64 `json:"height"`
	DeviceScaleFactor float64 `json:"deviceScaleFactor"`
}

type browserViewport struct {
	Width             int `json:"width"`
	Height            int `json:"height"`
	DeviceScaleFactor int `json:"deviceScaleFactor"`
}

type browserCreateRequest struct {
	Port     int                    `json:"port"`
	Path     string                 `json:"path"`
	Viewport browserViewportRequest `json:"viewport"`
}

type browserPageResponse struct {
	ID    string `json:"id"`
	Port  int    `json:"port"`
	URL   string `json:"url"`
	Title string `json:"title,omitempty"`
	State string `json:"state"`
}

func normalizeBrowserViewport(v browserViewportRequest) (browserViewport, error) {
	if !finitePositive(v.Width) || !finitePositive(v.Height) {
		return browserViewport{}, errors.New("viewport width and height must be positive")
	}
	w, h := int(math.Round(v.Width)), int(math.Round(v.Height))
	if w < 1 || h < 1 || w > browserMaxWidth || h > browserMaxHeight {
		return browserViewport{}, fmt.Errorf("viewport must be within %dx%d", browserMaxWidth, browserMaxHeight)
	}
	if v.DeviceScaleFactor != 1 {
		return browserViewport{}, errors.New("deviceScaleFactor must be 1")
	}
	return browserViewport{Width: w, Height: h, DeviceScaleFactor: 1}, nil
}

func finitePositive(v float64) bool { return v > 0 && !math.IsNaN(v) && !math.IsInf(v, 0) }

// browserSelectionExpression reads the page's current selection. A focused
// input/textarea keeps its own selection that document.getSelection() reports as
// empty, so it is asked first — otherwise "copy what I highlighted in this
// field" silently returns nothing. Read-only, and capped in the page so a huge
// document cannot be shipped over the wire in one message.
const browserSelectionExpression = `(() => {
  const el = document.activeElement;
  if (el && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA') &&
      typeof el.selectionStart === 'number' && el.selectionStart !== el.selectionEnd) {
    return String(el.value).slice(el.selectionStart, el.selectionEnd).slice(0, 131072);
  }
  const s = document.getSelection();
  return s ? s.toString().slice(0, 131072) : '';
})()`

// fitLayoutViewport answers "how wide a page can this pane show at once?".
//
// The 1600x1200 viewport ceiling bounds the SCREENCAST, which is what costs
// memory and bandwidth. A zoomed-out layout viewport does not: Chromium lays
// out wider and scales the image back down to the pane, so the frames stay
// pane-sized. That is why the layout bound is separate and larger.
//
// The returned viewport keeps the pane's aspect ratio exactly: the screencast
// scales the frame down to fit the pane and the canvas then fills the pane, so
// any other ratio would stretch the page. ok=false means "already fits, change
// nothing" — including the degenerate inputs.
func fitLayoutViewport(pane browserViewport, contentWidth float64) (browserViewport, bool) {
	if pane.Width < 1 || pane.Height < 1 || !finitePositive(contentWidth) {
		return browserViewport{}, false
	}
	// A hair over the pane is not worth a zoom: rounding and scrollbar widths
	// would make the view flip in and out of "fit" on every resize.
	if contentWidth <= float64(pane.Width)+browserFitSlack {
		return browserViewport{}, false
	}
	width := int(math.Ceil(contentWidth))
	if width > browserMaxLayoutWidth {
		width = browserMaxLayoutWidth
	}
	height := int(math.Round(float64(width) * float64(pane.Height) / float64(pane.Width)))
	if height > browserMaxLayoutHeight {
		height = browserMaxLayoutHeight
		width = int(math.Round(float64(height) * float64(pane.Width) / float64(pane.Height)))
	}
	if width <= pane.Width || height < 1 {
		return browserViewport{}, false
	}
	return browserViewport{Width: width, Height: height, DeviceScaleFactor: 1}, true
}

func browserTargetURL(port int, path string) (string, error) {
	if port < 1 || port > 65535 || reservedBrowserAgentPort(port) {
		return "", errors.New("port must be 1..65535 and must not be the workspace agent port")
	}
	ref, err := parseBrowserPath(path)
	if err != nil {
		return "", err
	}
	ref.Scheme = "http"
	ref.Host = net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	return ref.String(), nil
}

func parseBrowserPath(path string) (*url.URL, error) {
	if path == "" || len(path) > browserMaxPathBytes || !utf8.ValidString(path) ||
		!strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.Contains(path, `\`) {
		return nil, errors.New("path must be a valid /-starting relative path")
	}
	for _, r := range path {
		if r < 0x20 || r == 0x7f {
			return nil, errors.New("path contains control characters")
		}
	}
	u, err := url.Parse(path)
	if err != nil || u.IsAbs() || u.Host != "" || u.User != nil || u.Opaque != "" || !strings.HasPrefix(u.Path, "/") {
		return nil, errors.New("path must not contain a scheme, host, or userinfo")
	}
	return u, nil
}

func browserPathURL(baseURL, path string) (string, error) {
	ref, err := parseBrowserPath(path)
	if err != nil {
		return "", err
	}
	base, err := url.Parse(baseURL)
	if err != nil || !allowedTopLevelBrowserURL(base) {
		return "", errors.New("current browser origin is not allowed")
	}
	ref.Scheme, ref.Host = base.Scheme, base.Host
	return normalizeLoopbackURL(ref).String(), nil
}

func allowedTopLevelBrowserURL(u *url.URL) bool {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return false
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "http" {
			port = "80"
		} else {
			port = "443"
		}
	}
	p, err := strconv.Atoi(port)
	return err == nil && p >= 1 && p <= 65535 && !reservedBrowserAgentPort(p)
}

func normalizeLoopbackURL(u *url.URL) *url.URL {
	clone := *u
	host := strings.TrimSuffix(strings.ToLower(clone.Hostname()), ".")
	if host == "localhost" {
		host = "127.0.0.1"
	}
	if p := clone.Port(); p != "" {
		clone.Host = net.JoinHostPort(host, p)
	} else if strings.Contains(host, ":") {
		clone.Host = "[" + host + "]"
	} else {
		clone.Host = host
	}
	return &clone
}

func forbiddenBrowserResource(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return true
	}
	if u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "ws" && u.Scheme != "wss" {
		// data/blob are renderer-local and do not add a network reachability path.
		return u.Scheme != "data" && u.Scheme != "blob" && u.Scheme != "about"
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		p, _ := strconv.Atoi(u.Port())
		return reservedBrowserAgentPort(p)
	}
	switch host {
	case "host.docker.internal", "gateway.docker.internal", "metadata.google.internal", "instance-data.ec2.internal", "kubernetes.default.svc":
		return true
	}
	if base, err := url.Parse(os.Getenv("AF_CP_BASE_URL")); err == nil && base.Hostname() != "" {
		if sameBrowserEndpoint(u, base) {
			return true
		}
	}
	ip := net.ParseIP(host)
	if zone := strings.IndexByte(host, '%'); zone >= 0 {
		ip = net.ParseIP(host[:zone])
	}
	if ip == nil {
		return false // ordinary external DNS follows the Workspace egress policy
	}
	return ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

func reservedBrowserAgentPort(port int) bool {
	if port == browserAgentPort {
		return true
	}
	_, portText, err := net.SplitHostPort(os.Getenv("AGENT_ADDR"))
	if err != nil {
		return false
	}
	configured, err := strconv.Atoi(portText)
	return err == nil && configured == port
}

func sameBrowserEndpoint(a, b *url.URL) bool {
	if !strings.EqualFold(strings.TrimSuffix(a.Hostname(), "."), strings.TrimSuffix(b.Hostname(), ".")) {
		return false
	}
	port := func(u *url.URL) string {
		if u.Port() != "" {
			return u.Port()
		}
		if u.Scheme == "https" || u.Scheme == "wss" {
			return "443"
		}
		return "80"
	}
	return port(a) == port(b)
}

func truncateBrowserText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return strings.ToValidUTF8(s[:max], "")
}

// browserVirtualKeyCodes maps the DOM key names that have a DEFAULT ACTION in
// Blink to their Windows virtual-key codes.
//
// Without windowsVirtualKeyCode, Input.dispatchKeyEvent delivers a key event the
// page can observe but Blink performs no default action — so ArrowDown, PageUp,
// Home, space and friends never scrolled the pane, and Enter/Tab never moved a
// form on. Printable characters are unaffected (they arrive as `text`), which is
// why typing worked while every navigation key silently did nothing.
var browserVirtualKeyCodes = map[string]int{
	"Backspace": 8, "Tab": 9, "Enter": 13, "Escape": 27, " ": 32,
	"PageUp": 33, "PageDown": 34, "End": 35, "Home": 36,
	"ArrowLeft": 37, "ArrowUp": 38, "ArrowRight": 39, "ArrowDown": 40,
	"Insert": 45, "Delete": 46,
	"Shift": 16, "Control": 17, "Alt": 18, "CapsLock": 20, "Meta": 91,
	"F1": 112, "F2": 113, "F3": 114, "F4": 115, "F5": 116, "F6": 117,
	"F7": 118, "F8": 119, "F9": 120, "F10": 121, "F11": 122, "F12": 123,
}

// browserKeyEventParams builds the Input.dispatchKeyEvent payload for one key.
// Down events that carry no text use rawKeyDown, which is what Blink expects for
// a key whose effect is a command (scroll, caret move) rather than insertion.
func browserKeyEventParams(down bool, key, code string, modifiers int, repeat bool) map[string]any {
	params := map[string]any{
		"type": "keyUp", "key": key, "code": code, "modifiers": modifiers, "autoRepeat": repeat,
	}
	if text, ok := browserKeyText(key, modifiers); ok && down {
		params["type"] = "keyDown"
		params["text"] = text
		params["unmodifiedText"] = text
	} else if down {
		params["type"] = "rawKeyDown"
	}
	if vk, ok := browserVirtualKeyCode(key, code); ok {
		params["windowsVirtualKeyCode"] = vk
		params["nativeVirtualKeyCode"] = vk
	}
	return params
}

// browserKeyText returns the character a key inserts, if any. Modified keys
// (Ctrl/Alt/Meta) are commands, not text.
func browserKeyText(key string, modifiers int) (string, bool) {
	if modifiers&7 != 0 || utf8.RuneCountInString(key) != 1 {
		return "", false
	}
	r, _ := utf8.DecodeRuneInString(key)
	if r < 0x20 || r == 0x7f {
		return "", false
	}
	return key, true
}

func browserVirtualKeyCode(key, code string) (int, bool) {
	if vk, ok := browserVirtualKeyCodes[key]; ok {
		return vk, true
	}
	if utf8.RuneCountInString(key) == 1 {
		r, _ := utf8.DecodeRuneInString(key)
		switch {
		case r >= 'a' && r <= 'z':
			return int(r - 'a' + 'A'), true
		case (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			return int(r), true
		}
	}
	// Digits typed on the main row report their code even for symbol layouts.
	if len(code) == 6 && strings.HasPrefix(code, "Digit") {
		return int(code[5]), true
	}
	return 0, false
}
