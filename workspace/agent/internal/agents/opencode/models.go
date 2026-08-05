package opencode

import (
	"context"
	"crypto/sha256"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Models enumerates the models opencode can use RIGHT NOW (`opencode models`).
// The list reflects the user's connected providers — free-tier only shows the
// bundled opencode/* models; connecting a provider grows it — so it must be read
// live rather than hardcoded (unlike codex, whose catalog is fixed per CLI release
// and kept Console-side). Returns nil when the CLI is absent or errors; the launch
// picker then just offers 既定.
//
// Cached briefly: the Console fetches on every launch-modal open, and the CLI call
// costs ~1s. Failures are not cached, so a transient error heals on the next open.
var modelsMu sync.Mutex
var modelsAt time.Time
var modelsList []string
var modelsEnvKey [sha256.Size]byte

func Models() []string {
	modelsMu.Lock()
	defer modelsMu.Unlock()
	// The Agent itself does not inherit provider credentials: they live in the
	// encrypted connection store and are normally injected only into launched
	// sessions. Give the catalog command the same environment, otherwise an
	// OpenCode Go user sees only the zero-auth FREE models in the launch picker.
	providerEnv := env()
	envKey := sha256.Sum256([]byte(strings.Join(providerEnv, "\x00")))
	if modelsList != nil && modelsEnvKey == envKey && time.Since(modelsAt) < time.Minute {
		return modelsList
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "opencode", "models")
	cmd.Env = mergeCommandEnv(os.Environ(), providerEnv)
	out, err := cmd.Output()
	if err != nil {
		return modelsList // stale-if-error: an expired cache still beats an empty picker
	}
	modelsList = parseModels(string(out))
	modelsAt = time.Now()
	modelsEnvKey = envKey
	return modelsList
}

// InvalidateModels drops the cached catalog so the next read re-runs
// `opencode models`. The cache key is the injected provider env (auth.go), which a
// Console OAuth login does NOT change — the credential lands in opencode's own auth
// store — so an authentication change has to say so explicitly or the launch picker
// keeps the pre-login catalog for up to a minute（docs/54 の反映タイミング）.
func InvalidateModels() {
	modelsMu.Lock()
	modelsAt = time.Time{}
	modelsMu.Unlock()
}

// mergeCommandEnv applies stored connection values over the Agent's inherited
// environment without leaving duplicate names. The stored value must win if an
// image/operator environment happens to define the same provider variable.
func mergeCommandEnv(base, overrides []string) []string {
	out := append([]string(nil), base...)
	indexes := make(map[string]int, len(out))
	for i, entry := range out {
		if name, _, ok := strings.Cut(entry, "="); ok {
			indexes[name] = i
		}
	}
	for _, entry := range overrides {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if i, exists := indexes[name]; exists {
			out[i] = entry
			continue
		}
		indexes[name] = len(out)
		out = append(out, entry)
	}
	return out
}

// parseModels keeps provider/model lines and drops anything else (blank lines,
// warnings, future banners): a model id is a single token containing "/".
func parseModels(out string) []string {
	list := []string{}
	for _, ln := range strings.Split(out, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.ContainsAny(ln, " \t") || !strings.Contains(ln, "/") {
			continue
		}
		list = append(list, ln)
	}
	return list
}
