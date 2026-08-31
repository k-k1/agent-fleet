package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Toolchain selection (node via nvm, java via pre-baked Temurin), stored per-
// workspace and applied by the entrypoint on container start. The Console reads
// the available java versions (detected from the image) and the current choice,
// and writes the selection. The entrypoint applies it to the agent process on
// container start; in addition, every session/shell launched afterward re-reads
// the selection and injects it (resolvedToolchains / toolchainShellPrefix /
// applyToolchainEnv) so a Console change takes effect on the NEXT launch without a
// Stop → Start.

func toolchainsPath() string {
	return filepath.Join(homeDir(), ".config", "agent-fleet", "toolchains.json")
}

type toolchains struct {
	Node string `json:"node"`
	Java string `json:"java"`
	// Go: ""/"system" keeps the baked /usr/local/go (or none, on a lean rootfs);
	// a version string selects an on-demand toolchain the entrypoint installs
	// into the home via `workspace-agent install-go` (docs/log/35 §35.7.2-5).
	Go       string `json:"go,omitempty"`
	Timezone string `json:"timezone"`
	// AgentUpdate: member opt-in to update the baked CLIs (claude/opencode/codex) to
	// latest at container start. Only honored when the operator allows it (the
	// entrypoint gates on AF_AGENT_SELF_UPDATE_ALLOWED); a Stop→Start reverts to the
	// image versions when turned off. Persists in the home volume like the rest.
	AgentUpdate bool `json:"agentUpdate,omitempty"`
}

// defaultTimezone is the initial per-user timezone (entrypoint exports TZ from it,
// so session timestamps, the shell, and claude all use it). JST per request.
const defaultTimezone = "Asia/Tokyo"

// tzOptions are common zones offered in the Console; any IANA name is accepted.
var tzOptions = []string{
	"Asia/Tokyo", "UTC", "Asia/Shanghai", "Asia/Kolkata", "Asia/Singapore",
	"Europe/London", "Europe/Berlin", "America/New_York", "America/Los_Angeles",
}

func readToolchains() toolchains {
	t := toolchains{}
	if b, err := os.ReadFile(toolchainsPath()); err == nil {
		_ = json.Unmarshal(b, &t)
	}
	if t.Timezone == "" {
		t.Timezone = defaultTimezone
	}
	return t
}

// tzNameRe guards the timezone against shell/path injection (entrypoint exports it
// and resolves /usr/share/zoneinfo/<tz>). IANA names: letters, digits, _ + - / .
var tzNameRe = regexp.MustCompile(`^[A-Za-z0-9_+./-]{1,64}$`)

// toolchainVerRe guards node/java/go the same way: ""/"system" or a dotted numeric
// version. The values feed shell exports and path/glob lookups (ShellQuote defends
// today, but one unquoted use would turn a free-form string into injection).
var toolchainVerRe = regexp.MustCompile(`^(system|[0-9]{1,3}(\.[0-9]{1,4}){0,2})?$`)

// Temurin JDK discovery (installed majors, installable set, JAVA_HOME resolution)
// lives in jdk.go, which searches both /usr/lib/jvm and the per-user home volume.

// nodeOptions are the versions the Console offers for nvm. "system" keeps the
// image's base node.
var nodeOptions = []string{"system", "18", "20", "22", "24"}

// goOptions merges "system" (baked /usr/local/go, or none), the build pin and the
// on-demand versions already installed — the list the Console offers.
func goOptions() []string {
	opts := []string{"system"}
	seen := map[string]bool{}
	if pin := readBuildPins()["go"]; pin != "" {
		opts = append(opts, pin)
		seen[pin] = true
	}
	for _, v := range installedGoVersions() {
		if !seen[v] {
			opts = append(opts, v)
			seen[v] = true
		}
	}
	return opts
}

// resolvedToolchains resolves the current selection to concrete values for
// injection into freshly-launched sessions/shells. node uses the highest installed
// patch of the chosen major under the home nvm dir (must already be installed —
// session launch won't run a network install); java resolves the selected Temurin
// across the search dirs (/usr/lib/jvm then the home volume); go resolves the
// on-demand GOROOT (or the baked one when its pin matches); tz is honored only if
// its zoneinfo exists. Empty strings mean "no override".
func resolvedToolchains() (javaHome, nodeBin, goRoot, tz string) {
	t := readToolchains()
	if t.Java != "" {
		javaHome = javaHomeFor(t.Java)
	}
	if t.Node != "" && t.Node != "system" {
		if m, _ := filepath.Glob(filepath.Join(homeDir(), ".nvm", "versions", "node", "v"+t.Node+".*", "bin")); len(m) > 0 {
			sort.Strings(m)
			nodeBin = m[len(m)-1] // highest installed patch
		}
	}
	goRoot = goRootFor(t.Go)
	if t.Timezone != "" {
		if _, err := os.Stat("/usr/share/zoneinfo/" + t.Timezone); err == nil {
			tz = t.Timezone
		}
	}
	return
}

// toolchainShellPrefix is an `sh -c` prefix that exports the selected toolchain
// for a tmux-launched program (tmux runs the pane command via /bin/sh -c, so the
// existing PATH expands via "$PATH"). Empty when nothing is selected. The new java
// / node / go bins are prepended, so they win over the entrypoint's stale ones.
func toolchainShellPrefix() string {
	jh, nodeBin, goRoot, tz := resolvedToolchains()
	var b strings.Builder
	if jh != "" {
		b.WriteString("export JAVA_HOME=" + session.ShellQuote(jh) + "; ")
		b.WriteString("export PATH=" + session.ShellQuote(jh+"/bin") + ":\"$PATH\"; ")
	}
	if nodeBin != "" {
		b.WriteString("export PATH=" + session.ShellQuote(nodeBin) + ":\"$PATH\"; ")
	}
	if goRoot != "" {
		b.WriteString("export GOROOT=" + session.ShellQuote(goRoot) + "; ")
		b.WriteString("export PATH=" + session.ShellQuote(goRoot+"/bin") + ":\"$PATH\"; ")
	}
	if tz != "" {
		b.WriteString("export TZ=" + session.ShellQuote(tz) + "; ")
	}
	return b.String()
}

// applyToolchainEnv overlays the selected toolchain onto a process environment (the
// default shell in handlePTY, launched directly from Go). It rewrites a single PATH
// entry (avoiding duplicate keys, where getenv would keep the first/stale one).
func applyToolchainEnv(env []string) []string {
	jh, nodeBin, goRoot, tz := resolvedToolchains()
	if jh == "" && nodeBin == "" && goRoot == "" && tz == "" {
		return env
	}
	path := ""
	out := make([]string, 0, len(env)+4)
	for _, e := range env {
		switch {
		case strings.HasPrefix(e, "PATH="):
			path = strings.TrimPrefix(e, "PATH=")
		case jh != "" && strings.HasPrefix(e, "JAVA_HOME="):
			// dropped; re-added below
		case goRoot != "" && strings.HasPrefix(e, "GOROOT="):
			// dropped; re-added below
		case tz != "" && strings.HasPrefix(e, "TZ="):
			// dropped; re-added below
		default:
			out = append(out, e)
		}
	}
	if nodeBin != "" {
		path = nodeBin + ":" + path
	}
	if goRoot != "" {
		out = append(out, "GOROOT="+goRoot)
		path = goRoot + "/bin:" + path
	}
	if jh != "" {
		out = append(out, "JAVA_HOME="+jh)
		path = jh + "/bin:" + path
	}
	if tz != "" {
		out = append(out, "TZ="+tz)
	}
	out = append(out, "PATH="+path)
	return out
}

func handleToolchainsGet(w http.ResponseWriter, r *http.Request) {
	t := readToolchains()
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"node":           t.Node,
		"java":           t.Java,
		"go":             t.Go,
		"timezone":       t.Timezone,
		"agentUpdate":    t.AgentUpdate,
		"java_available": javaOptions(),         // offered for selection (installed ∪ installable)
		"java_installed": installedJavaMajors(), // present on disk now (ready without a download)
		"node_options":   nodeOptions,
		"go_options":     goOptions(), // "system" ∪ build pin ∪ installed on-demand versions
		"tz_options":     tzOptions,
	})
}

func handleToolchainsPut(w http.ResponseWriter, r *http.Request) {
	var req toolchains
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if req.Timezone == "" {
		req.Timezone = defaultTimezone
	}
	if !tzNameRe.MatchString(req.Timezone) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_timezone", "invalid timezone name")
		return
	}
	if !toolchainVerRe.MatchString(req.Node) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_node", "invalid node version")
		return
	}
	if !toolchainVerRe.MatchString(req.Java) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_java", "invalid java version")
		return
	}
	if !toolchainVerRe.MatchString(req.Go) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_go", "invalid go version")
		return
	}
	p := toolchainsPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "write_failed", err.Error())
		return
	}
	b, _ := json.MarshalIndent(req, "", "  ")
	if err := os.WriteFile(p, append(b, '\n'), 0o600); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "write_failed", err.Error())
		return
	}
	handleToolchainsGet(w, r)
}
