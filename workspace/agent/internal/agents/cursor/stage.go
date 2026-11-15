package cursor

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

// What a cursor sign-in writes that matters (measured on a real sign-in under a scratch HOME):
// the tokens in ~/.config/cursor/auth.json, which alone make `status` report authenticated, and
// two account caches in ~/.cursor/cli-config.json that status does not rewrite. Everything else
// in cli-config.json is the member's settings and stays.

// accountKeys are the cli-config.json keys that describe the signed-in account.
var accountKeys = []string{"authInfo", "privacyCache"}

// signedInAt reports a finished sign-in under the staging home. A var for tests.
var signedInAt = func(home string) bool { return statusAuthenticated(home) }

// installLogin installs the staged login into home: the tokens, then the account caches.
func installLogin(stage, home string) error {
	if err := agents.InstallFile(filepath.Join(stage, ".config", "cursor", "auth.json"),
		filepath.Join(home, ".config", "cursor", "auth.json"), 0o600); err != nil {
		return err
	}
	// The tokens are what sign cursor in; a stale account cache only mislabels it, so a failure
	// here is logged rather than reported as a failed sign-in.
	if err := replaceAccountKeys(filepath.Join(stage, ".cursor", "cli-config.json"),
		filepath.Join(home, ".cursor", "cli-config.json")); err != nil {
		log.Printf("cursor: sign-in installed, but its account cache was not: %v", err)
	}
	return nil
}

// replaceAccountKeys sets accountKeys in dst to their values in src (dropping a key src lacks),
// leaving every other key as it is.
func replaceAccountKeys(src, dst string) error {
	staged, err := readJSONObject(src)
	if err != nil {
		return err
	}
	live, err := readJSONObject(dst)
	if errors.Is(err, fs.ErrNotExist) {
		live = map[string]json.RawMessage{}
	} else if err != nil {
		return err
	}
	for _, k := range accountKeys {
		if v, ok := staged[k]; ok {
			live[k] = v
		} else {
			delete(live, k)
		}
	}
	b, err := json.MarshalIndent(live, "", "  ")
	if err != nil {
		return err
	}
	perm := os.FileMode(0o644) // what cursor itself creates
	if fi, err := os.Stat(dst); err == nil {
		perm = fi.Mode().Perm()
	}
	return agents.WriteFileAtomic(dst, append(b, '\n'), perm)
}

func readJSONObject(p string) (map[string]json.RawMessage, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	m := map[string]json.RawMessage{}
	return m, json.Unmarshal(b, &m)
}
