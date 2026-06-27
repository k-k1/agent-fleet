package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
)

// Toolchain selection (node via nvm, java via pre-baked Temurin), stored per-
// workspace and applied by the entrypoint on container start. The Console reads
// the available java versions (detected from the image) and the current choice,
// and writes the selection. Changing a version takes effect on Stop → Start.

func toolchainsPath() string {
	return filepath.Join(homeDir(), ".config", "agent-fleet", "toolchains.json")
}

type toolchains struct {
	Node string `json:"node"`
	Java string `json:"java"`
}

func readToolchains() toolchains {
	t := toolchains{}
	if b, err := os.ReadFile(toolchainsPath()); err == nil {
		_ = json.Unmarshal(b, &t)
	}
	return t
}

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

func handleToolchainsGet(w http.ResponseWriter, r *http.Request) {
	t := readToolchains()
	writeJSON(w, http.StatusOK, map[string]any{
		"node":           t.Node,
		"java":           t.Java,
		"java_available": availableJava(),
		"node_options":   nodeOptions,
	})
}

func handleToolchainsPut(w http.ResponseWriter, r *http.Request) {
	var req toolchains
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
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
