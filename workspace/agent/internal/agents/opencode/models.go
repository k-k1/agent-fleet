package opencode

import (
	"context"
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

func Models() []string {
	modelsMu.Lock()
	defer modelsMu.Unlock()
	if modelsList != nil && time.Since(modelsAt) < time.Minute {
		return modelsList
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "opencode", "models").Output()
	if err != nil {
		return modelsList // stale-if-error: an expired cache still beats an empty picker
	}
	modelsList = parseModels(string(out))
	modelsAt = time.Now()
	return modelsList
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
