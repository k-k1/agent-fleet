package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
)

// opencode web: an optional, per-workspace web UI for opencode (the browser-native
// alternative to the tmux TUI). It is NOT core opencode web — that one is root-path
// only and won't sit under our /ocweb sub-path proxy. Instead we run the
// prefix-aware pk-opencode-webui (baked at /opt/opencode-web by the Dockerfile) in
// front of a headless `opencode serve`:
//
//   browser {origin}{extPrefix}/ocweb/… → CP → agent /ocweb/… (handleOcwebProxy)
//     → bun serve-ui.ts :uiPort  (BASE_PATH={extPrefix}/ocweb/, API_URL=:servePort)
//     → opencode serve :servePort (127.0.0.1, headless API)
//
// Off by default; toggled from the Console エージェント tab. State is durable in
// ~/.config/agent-fleet/opencode-web.json so the choice survives restarts, and the
// agent (re)starts the pair on boot when enabled. See
// docs/decisions/0007-opencode-web-via-pk-webui.md.

const (
	opencodeWebDir   = "/opt/opencode-web"
	opencodeWebServe = opencodeWebDir + "/serve-ui.ts"
	opencodeWebDist  = opencodeWebDir + "/dist"
)

// opencodeWebPref is the durable toggle. BasePrefix is the deployment's external
// URL prefix (PUBLIC_BASE_URL path, e.g. "/agent-fleet"; "" when served at root) —
// passed in by the CP so BASE_PATH matches what the browser sees. The agent can't
// derive it on its own.
type opencodeWebPref struct {
	Enabled    bool   `json:"enabled"`
	BasePrefix string `json:"base_prefix"`
}

func opencodeWebPrefPath() string {
	return filepath.Join(homeDir(), ".config", "agent-fleet", "opencode-web.json")
}

func readOpencodeWebPref() opencodeWebPref {
	var p opencodeWebPref
	if b, err := os.ReadFile(opencodeWebPrefPath()); err == nil {
		_ = json.Unmarshal(b, &p)
	}
	return p
}

func writeOpencodeWebPref(p opencodeWebPref) error {
	path := opencodeWebPrefPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

// ocwebBasePath is the BASE_PATH handed to pk-webui (and the path it serves under):
// the external prefix + "/ocweb/". Always exactly one trailing slash, one leading,
// no internal doubles regardless of how the prefix is slashed.
func ocwebBasePath(prefix string) string {
	if p := trimSlashes(prefix); p != "" {
		return "/" + p + "/ocweb/"
	}
	return "/ocweb/"
}

func trimSlashes(s string) string {
	for len(s) > 0 && s[0] == '/' {
		s = s[1:]
	}
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

// opencodeWebAvailable reports whether this image can run opencode web (the baked
// UI + the bun runtime + opencode). Deployments without the vendored UI report
// unavailable and the Console hides the toggle.
func opencodeWebAvailable() bool {
	if _, err := os.Stat(opencodeWebServe); err != nil {
		return false
	}
	if _, err := exec.LookPath("bun"); err != nil {
		return false
	}
	if _, err := exec.LookPath("opencode"); err != nil {
		return false
	}
	return true
}

// ocWebManager supervises the serve+ui process pair. A single instance per agent
// (one web UI per workspace).
type ocWebManager struct {
	mu      sync.Mutex
	serve   *exec.Cmd
	ui      *exec.Cmd
	port    int // the ui (pk-webui) port; 0 when not running
	running bool
}

var ocWeb = &ocWebManager{}

func startProc(name string, args, extraEnv []string, cwd string) (*exec.Cmd, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), extraEnv...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// New process group so we can signal the whole tree (opencode serve / bun may
	// fork helpers) on stop.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

func killProc(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if pgid, err := syscall.Getpgid(cmd.Process.Pid); err == nil {
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
	} else {
		_ = cmd.Process.Kill()
	}
}

func (m *ocWebManager) start(basePrefix string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return nil
	}
	if !opencodeWebAvailable() {
		return errors.New("opencode web is not available in this image")
	}
	servePort := envOr("AF_OPENCODE_SERVE_PORT", "4096")
	uiPort := envOr("AF_OCWEB_PORT", "4097")

	// opencode serve: headless API on localhost, with the same provider keys the
	// tmux opencode sessions get (so the web UI can reach providers).
	serve, err := startProc("opencode",
		[]string{"serve", "--hostname", "127.0.0.1", "--port", servePort},
		opencodeEnv(), homeDir())
	if err != nil {
		return err
	}
	// pk-webui: prefix-aware UI proxying to the serve API.
	ui, err := startProc("bun", []string{opencodeWebServe}, []string{
		"PORT=" + uiPort,
		"BASE_PATH=" + ocwebBasePath(basePrefix),
		"API_URL=http://127.0.0.1:" + servePort,
		"DIST_DIR=" + opencodeWebDist,
	}, opencodeWebDir)
	if err != nil {
		killProc(serve)
		return err
	}
	p, _ := strconv.Atoi(uiPort)
	m.serve, m.ui, m.port, m.running = serve, ui, p, true
	go m.reap(serve, "opencode serve")
	go m.reap(ui, "opencode web ui")
	log.Printf("[opencode-web] started (serve :%s, ui :%s, base %s)", servePort, uiPort, ocwebBasePath(basePrefix))
	return nil
}

// reap waits on one process; if it exits while it is still the active pair, tear the
// pair down (a half-running serve/ui is useless) so status reflects reality.
func (m *ocWebManager) reap(cmd *exec.Cmd, label string) {
	_ = cmd.Wait()
	m.mu.Lock()
	if m.serve != cmd && m.ui != cmd {
		m.mu.Unlock() // already replaced/stopped — nothing to do
		return
	}
	other := m.ui
	if cmd == m.ui {
		other = m.serve
	}
	m.serve, m.ui, m.port, m.running = nil, nil, 0, false
	m.mu.Unlock()
	killProc(other)
	log.Printf("[opencode-web] %s exited; pair stopped", label)
}

func (m *ocWebManager) stop() {
	m.mu.Lock()
	serve, ui := m.serve, m.ui
	m.serve, m.ui, m.port, m.running = nil, nil, 0, false
	m.mu.Unlock()
	killProc(serve)
	killProc(ui)
}

func (m *ocWebManager) status() (running bool, port int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running, m.port
}

// reconcileOpencodeWeb (re)starts the pair on boot when the durable pref is on.
func reconcileOpencodeWeb() {
	p := readOpencodeWebPref()
	if p.Enabled && opencodeWebAvailable() {
		if err := ocWeb.start(p.BasePrefix); err != nil {
			log.Printf("[opencode-web] boot start failed: %v", err)
		}
	}
}

func opencodeWebBody() map[string]any {
	running, port := ocWeb.status()
	p := readOpencodeWebPref()
	return map[string]any{
		"available": opencodeWebAvailable(),
		"enabled":   p.Enabled,
		"running":   running,
		"port":      port,
	}
}

func handleOpencodeWebGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, opencodeWebBody())
}

type opencodeWebReq struct {
	Enabled    *bool   `json:"enabled"`
	BasePrefix *string `json:"base_prefix"`
}

func handleOpencodeWebPut(w http.ResponseWriter, r *http.Request) {
	var req opencodeWebReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	p := readOpencodeWebPref()
	if req.BasePrefix != nil {
		p.BasePrefix = *req.BasePrefix
	}
	if req.Enabled != nil {
		p.Enabled = *req.Enabled
	}
	if err := writeOpencodeWebPref(p); err != nil {
		writeErr(w, http.StatusInternalServerError, "write_failed", err.Error())
		return
	}
	if p.Enabled {
		if err := ocWeb.start(p.BasePrefix); err != nil {
			writeErr(w, http.StatusServiceUnavailable, "start_failed", err.Error())
			return
		}
	} else {
		ocWeb.stop()
	}
	writeJSON(w, http.StatusOK, opencodeWebBody())
}

// handleOcwebProxy reverse-proxies /ocweb/… to the local pk-webui, preserving the
// full path (pk-webui's BASE_PATH expects it) and tunneling WebSockets (terminal
// PTY). The CP forwards the browser request here with the internal bearer, which we
// strip before it reaches pk-webui.
func handleOcwebProxy(w http.ResponseWriter, r *http.Request) {
	running, port := ocWeb.status()
	if !running || port == 0 {
		writeErr(w, http.StatusServiceUnavailable, "opencode_web_off",
			"opencode web is not running (enable it in settings)")
		return
	}
	target := &url.URL{Scheme: "http", Host: "127.0.0.1:" + strconv.Itoa(port)}
	rp := httputil.NewSingleHostReverseProxy(target) // target.Path="" → req path kept as-is
	base := rp.Director
	rp.Director = func(req *http.Request) {
		base(req)
		req.Host = target.Host
		req.Header.Del("Authorization") // internal CP↔Agent token, not for pk-webui
	}
	rp.ServeHTTP(w, r)
}
