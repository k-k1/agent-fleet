package opencode

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// windowFn answers the context window opencode is using for one provider/model pair, in
// tokens, or 0 when nothing is declared for it (which leaves the Console's model-name guess
// in place, exactly as before).
type windowFn func(providerID, modelID string) int

// modelWindows reads the per-model context window back out of the config file af itself
// writes the engine provider block into (WriteEngineProviders / PushEngineProviders, which
// opencode persists to the same file). Keyed "<providerID>/<modelID>" — the pair a message
// row carries.
//
// WHY the file rather than the daemon: this runs on the transcript poll, and the poll must
// not touch `opencode serve`. A tmux session has no daemon at all, and asking for one there
// would start a process the session never wanted; a managed session has one, and an extra
// request per poll against it is the shape that made the catalogue push restart the daemon
// mid-turn (docs/log/54). The file is what opencode loaded, so it is the same number with
// none of that.
//
// WHY it matters that this is the SAME number llama-server was started with: for a
// self-hosted engine the window travels forward as one value — store.EngineModel.ContextTokens
// → the active set's `c` → the sidecar's preset → llama-server's --ctx-size — and, since ADR
// 0093 phase 0, one value travels BACK: `GET /engine/{key}/props` reads the window llama-server
// actually started with (`default_generation_settings.n_ctx`) and engines.go's
// syncEngineProviders substitutes it for the catalogue's declared context_tokens whenever the
// box is warm enough to answer, before either number reaches engineProviderEntry's
// `limit.context` here. Recording it on the turn is what stops the mirror from falling back to
// usagex.WindowGuess, which reads a self-hosted id as an unknown non-Claude model and answers
// 200,000 — a 32k engine then showed a 25k conversation as 13% full while opencode was
// compacting it on every turn.
//
// A provider af did not write (anthropic, bedrock, …) is simply absent: opencode knows those
// windows from its own catalogue, not from this file, and 0 means "nobody said" rather than
// "no window".
func modelWindows() map[string]int {
	path := engineConfigPath()
	windowMu.Lock()
	defer windowMu.Unlock()
	// The path is part of the key, not just the age: it moves when the config switches
	// between .jsonc and .json, and a cache that ignored that would answer for a file
	// opencode is no longer reading (and would carry one test's HOME into the next).
	if windowBy != nil && windowPath == path && time.Since(windowAt) < windowTTL {
		return windowBy
	}
	windowBy, windowPath, windowAt = readModelWindows(path), path, time.Now()
	return windowBy
}

// windowTTL is the backstop for a config edited by hand. af's own writes say so through
// InvalidateModels, so in the normal case this expiry only costs one small read a minute in
// a workspace that has a chat engine at all.
const windowTTL = time.Minute

var (
	windowMu   sync.Mutex
	windowAt   time.Time
	windowPath string
	windowBy   map[string]int
)

// invalidateWindows drops the cached windows. Called from InvalidateModels, so the one entry
// point every config write already uses covers this cache too.
func invalidateWindows() {
	windowMu.Lock()
	windowAt = time.Time{}
	windowMu.Unlock()
}

// readModelWindows parses `provider.<p>.models.<m>.limit.context` out of one config file.
// Never an error: a missing, unreadable or hand-commented (jsonc) file just means no windows
// are known, which is the pre-existing behaviour rather than a failure worth reporting on a
// poll.
func readModelWindows(path string) map[string]int {
	b, err := os.ReadFile(path)
	if err != nil {
		return map[string]int{}
	}
	var root struct {
		Provider map[string]struct {
			Models map[string]struct {
				Limit struct {
					Context int `json:"context"`
				} `json:"limit"`
			} `json:"models"`
		} `json:"provider"`
	}
	if json.Unmarshal(b, &root) != nil {
		return map[string]int{}
	}
	out := make(map[string]int, len(root.Provider))
	for p, prov := range root.Provider {
		for m, mod := range prov.Models {
			if mod.Limit.Context > 0 {
				out[p+"/"+m] = mod.Limit.Context
			}
		}
	}
	return out
}

// modelWindowLookup is the windowFn the transcript reader passes down: the map is taken once
// per poll, so a conversation of 200 messages costs one cache read rather than 200.
func modelWindowLookup() windowFn {
	by := modelWindows()
	return func(providerID, modelID string) int {
		if providerID == "" || modelID == "" {
			return 0
		}
		return by[providerID+"/"+modelID]
	}
}
