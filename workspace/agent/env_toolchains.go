package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
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
	Node     string `json:"node"`
	Java     string `json:"java"`
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

// availableJava lists the major versions of the Temurin JDKs baked into the image
// (dirs like /usr/lib/jvm/temurin-21-jdk-amd64), sorted ascending.
func availableJava() []string {
	re := regexp.MustCompile(`^temurin-(\d+)-jdk`)
	seen := map[string]bool{}
	if entries, err := os.ReadDir("/usr/lib/jvm"); err == nil {
		for _, e := range entries {
			if m := re.FindStringSubmatch(e.Name()); m != nil {
				seen[m[1]] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := strconv.Atoi(out[i])
		b, _ := strconv.Atoi(out[j])
		return a < b
	})
	return out
}

// nodeOptions are the versions the Console offers for nvm. "system" keeps the
// image's base node.
var nodeOptions = []string{"system", "18", "20", "22", "24"}

// resolvedToolchains resolves the current selection to concrete values for
// injection into freshly-launched sessions/shells. node uses the highest installed
// patch of the chosen major under the home nvm dir (must already be installed —
// session launch won't run a network install); java globs the baked Temurin; tz is
// honored only if its zoneinfo exists. Empty strings mean "no override".
func resolvedToolchains() (javaHome, nodeBin, tz string) {
	t := readToolchains()
	if t.Java != "" {
		if m, _ := filepath.Glob("/usr/lib/jvm/temurin-" + t.Java + "-jdk*"); len(m) > 0 {
			javaHome = m[0]
		}
	}
	if t.Node != "" && t.Node != "system" {
		if m, _ := filepath.Glob(filepath.Join(homeDir(), ".nvm", "versions", "node", "v"+t.Node+".*", "bin")); len(m) > 0 {
			sort.Strings(m)
			nodeBin = m[len(m)-1] // highest installed patch
		}
	}
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
// / node bins are prepended, so they win over the entrypoint's stale ones.
func toolchainShellPrefix() string {
	jh, nodeBin, tz := resolvedToolchains()
	var b strings.Builder
	if jh != "" {
		b.WriteString("export JAVA_HOME=" + shellQuote(jh) + "; ")
		b.WriteString("export PATH=" + shellQuote(jh+"/bin") + ":\"$PATH\"; ")
	}
	if nodeBin != "" {
		b.WriteString("export PATH=" + shellQuote(nodeBin) + ":\"$PATH\"; ")
	}
	if tz != "" {
		b.WriteString("export TZ=" + shellQuote(tz) + "; ")
	}
	return b.String()
}

// applyToolchainEnv overlays the selected toolchain onto a process environment (the
// default shell in handlePTY, launched directly from Go). It rewrites a single PATH
// entry (avoiding duplicate keys, where getenv would keep the first/stale one).
func applyToolchainEnv(env []string) []string {
	jh, nodeBin, tz := resolvedToolchains()
	if jh == "" && nodeBin == "" && tz == "" {
		return env
	}
	path := ""
	out := make([]string, 0, len(env)+3)
	for _, e := range env {
		switch {
		case strings.HasPrefix(e, "PATH="):
			path = strings.TrimPrefix(e, "PATH=")
		case jh != "" && strings.HasPrefix(e, "JAVA_HOME="):
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
	writeJSON(w, http.StatusOK, map[string]any{
		"node":           t.Node,
		"java":           t.Java,
		"timezone":       t.Timezone,
		"agentUpdate":    t.AgentUpdate,
		"java_available": availableJava(),
		"node_options":   nodeOptions,
		"tz_options":     tzOptions,
	})
}

func handleToolchainsPut(w http.ResponseWriter, r *http.Request) {
	var req toolchains
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if req.Timezone == "" {
		req.Timezone = defaultTimezone
	}
	if !tzNameRe.MatchString(req.Timezone) {
		writeErr(w, http.StatusBadRequest, "bad_timezone", "invalid timezone name")
		return
	}
	p := toolchainsPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		writeErr(w, http.StatusInternalServerError, "write_failed", err.Error())
		return
	}
	b, _ := json.MarshalIndent(req, "", "  ")
	if err := os.WriteFile(p, append(b, '\n'), 0o600); err != nil {
		writeErr(w, http.StatusInternalServerError, "write_failed", err.Error())
		return
	}
	handleToolchainsGet(w, r)
}
