package main

import (
	"net/http"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// manager owns the set of per-user Workspace runtimes. Each Control Plane user
// gets one container (af-ws-<user>) with an isolated home (<dataRoot>/<user>/home)
// and its own host-published Agent port. This is the `local` Runtime adapter
// scaled from a single fixed container to a per-user map (docs/09 §9.3); the
// Agent contract (/sessions, /repos, /connections) is unchanged — only the CP's
// user->container resolution is new.
type manager struct {
	mu  sync.Mutex
	rts map[string]*dockerRuntime

	// template fields shared by every per-user runtime
	image      string
	dataRoot   string // host path; <dataRoot>/<user>/home is bind-mounted to ~
	agentHost  string
	memory     string
	sessionCmd string
	extraEnv   []string

	portBase int // host port for the first user's Agent (7700)
	nextPort int

	// user resolution (AuthGateway port). authMode: "dev" (fixed id) | "proxy"
	// (read the authenticated email from the gateway header).
	authMode    string
	devUser     string
	emailHeader string
}

// forUser returns the runtime descriptor for a user, allocating its name, home
// and Agent port on first use. It does not start the container — that's start().
// If a container for this user already exists, its published host port is adopted
// so a CP restart keeps talking to the running Agent on the right port.
func (m *manager) forUser(user string) *dockerRuntime {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rt, ok := m.rts[user]; ok {
		return rt
	}
	name := "af-ws-" + user
	port := dockerPublishedPort(name)
	if port == "" {
		port = strconv.Itoa(m.nextPort)
		m.nextPort++
	} else if p, err := strconv.Atoi(port); err == nil && p >= m.nextPort {
		m.nextPort = p + 1 // keep the allocator ahead of adopted ports
	}
	// Adopt a running container's token across CP restarts; else mint a fresh one.
	token := dockerEnvValue(name, "AGENT_TOKEN")
	if token == "" {
		token = randHex(24)
	}
	rt := &dockerRuntime{
		image:      m.image,
		name:       name,
		network:    "af-net-" + user,
		dataDir:    filepath.Join(m.dataRoot, user),
		agentHost:  m.agentHost,
		agentPort:  port,
		token:      token,
		memory:     m.memory,
		sessionCmd: m.sessionCmd,
		extraEnv:   m.extraEnv,
	}
	m.rts[user] = rt
	return rt
}

// resolveUser maps a request to a Control Plane user key. In dev mode this is a
// fixed id; in proxy mode it is the sanitized email injected by the AuthGateway
// (oauth2-proxy). Falls back to the dev user when the header is missing/empty.
func (m *manager) resolveUser(r *http.Request) string {
	if m.authMode == "proxy" {
		if u := sanitizeUser(r.Header.Get(m.emailHeader)); u != "" {
			return u
		}
	}
	return m.devUser
}

var userInvalid = regexp.MustCompile(`[^a-z0-9]+`)

// sanitizeUser turns an email (or any id) into a container-name-safe key:
// lowercase, non-alphanumerics collapsed to '-', trimmed, length-capped.
// e.g. "Alice.B@example.com" -> "alice-b-example-com".
func sanitizeUser(s string) string {
	s = userInvalid.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-")
	s = strings.Trim(s, "-")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	return s
}

// dockerPublishedPort returns the host port mapped to the container's 7700/tcp,
// or "" if the container does not exist / has no mapping.
func dockerPublishedPort(name string) string {
	out, err := exec.Command("docker", "inspect", "-f",
		`{{with index .NetworkSettings.Ports "7700/tcp"}}{{(index . 0).HostPort}}{{end}}`, name).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// dockerEnvValue returns the value of an env var baked into a container's config
// (e.g. AGENT_TOKEN), or "" if the container does not exist or lacks the var.
func dockerEnvValue(name, key string) string {
	out, err := exec.Command("docker", "inspect", "-f",
		`{{range .Config.Env}}{{println .}}{{end}}`, name).Output()
	if err != nil {
		return ""
	}
	prefix := key + "="
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}
