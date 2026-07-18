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
