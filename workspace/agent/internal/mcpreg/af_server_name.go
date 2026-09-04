package mcpreg

// af's own MCP server name is minted afresh on every boot (docs/log/48 §8.4).
//
// Why: af writes into each CLI's user/global scope, but when a repository's project scope
// defines the same name, project wins for every CLI except claude (measured; the table in
// §8.4). A server merely named `af` in a `.mcp.json` or a `.codex/config.toml` is therefore
// enough to silently kill self-reporting, handoff proposals and Chromium attach for every
// session in that repository. `reservedNames` only takes effect inside the AF registry and
// cannot stop a file in someone else's repository.
//
// A random suffix on the name makes an accidental collision practically impossible. The
// rotation is per boot so that a collision, if one ever happened, clears itself on a restart.
//
// What changes is only the key written into each CLI's config file (the part that becomes the
// `mcp__<name>__<tool>` prefix on the client side); the tool names do not change (`af_report`
// and the rest). The instructions AF injects are written in terms of tool names, and neither
// the code nor the docs spell the prefix anywhere. The registry identifier (ID) also stays
// `af` — every branch looks at the ID.

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// afNameRE matches a name this file generates. It is what lets a stale entry be
// recognised as af's own even when the ownership ledger has lost it — see
// StaleAFServerName.
var afNameRE = regexp.MustCompile(`^af_[0-9a-f]{8}$`)

func afNamePath() string { return filepath.Join(paths.AgentConfigDir(), "mcp-af-name") }

var afNameOnce struct {
	sync.Mutex
	value string
}

// RotateAFServerName mints a new name for this boot. The Agent calls it once at
// startup, BEFORE the first materialize, so every config written this boot agrees.
// A failure is not fatal: the previous name (or the legacy "af") keeps working, and
// the only thing lost is the rotation.
func RotateAFServerName() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return AFServerName() // keep whatever is already in effect
	}
	name := "af_" + hex.EncodeToString(b[:])
	afNameOnce.Lock()
	defer afNameOnce.Unlock()
	if err := os.MkdirAll(filepath.Dir(afNamePath()), 0o700); err != nil {
		return afServerNameLocked()
	}
	if err := writeFileAtomic(afNamePath(), []byte(name+"\n"), 0o600); err != nil {
		return afServerNameLocked()
	}
	afNameOnce.value = name
	return name
}

// AFServerName is the name af's own server is written under right now. Processes other
// than the rotating one (and the rotating one after it has rotated) read the same file,
// so a session launched from any path agrees with the config on disk.
func AFServerName() string {
	afNameOnce.Lock()
	defer afNameOnce.Unlock()
	return afServerNameLocked()
}

func afServerNameLocked() string {
	if afNameOnce.value != "" {
		return afNameOnce.value
	}
	b, err := os.ReadFile(afNamePath())
	if name := strings.TrimSpace(string(b)); err == nil && afNameRE.MatchString(name) {
		afNameOnce.value = name
		return name
	}
	// No rotation has happened yet (fresh home, or the file was lost): fall back to the
	// historical name rather than inventing one here. Minting from a read path would let
	// two processes disagree, which is worse than not rotating.
	return BuiltinAF
}

// StaleAFServerName reports whether name is one of af's OWN rotated names left over
// from an earlier boot, so a materializer may remove it even though the ownership
// ledger (mcp-managed.json) no longer lists it.
//
// This is what makes per-boot rotation safe. The ledger is normally the only thing
// that authorizes deleting a row from a user's config (docs/log/48 §8.2) — deliberately
// conservative, so af can never eat a server it did not write. But with a name that
// changes every boot, a lost or stale ledger would stop af recognising its own previous
// entries, and each boot would leave another live `af_xxxxxxxx` behind: N boots, N MCP
// children all running the same server. The generated shape is narrow enough
// (`af_` + 8 hex) that recognising it costs none of the ledger's caution.
func StaleAFServerName(name string, keep map[string]bool) bool {
	return !keep[name] && afNameRE.MatchString(name)
}
