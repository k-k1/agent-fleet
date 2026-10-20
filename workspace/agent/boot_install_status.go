package main

// What the entrypoint's boot-install did for each CLI on this start, so Settings › Toolchains
// can say "install failed: home disk full" where it used to show "—".
//
// entrypoint.sh writes one tab-separated line per CLI to bootInstallStatusPath:
//
//	<tool>\t<status>\t<reason>
//
// status is ok, skipped (already present) or failed; reason is set for failed only and is one
// of no_space, network, checksum, other. The file lives under /tmp, not in home: the failure
// being reported is usually a full home, and a status file that needed home to be written
// would vanish exactly then. /tmp is per container and wiped on restart, which matches what
// it describes: one boot's outcome. A missing or unreadable file means "unknown", never "ok".

import (
	"os"
	"strings"
)

// bootInstallStatusPath is a var only so tests can point it at a temp file.
var bootInstallStatusPath = "/tmp/af-boot-install.tsv"

// bootInstall is one CLI's outcome.
type bootInstall struct {
	Status string `json:"status"`           // ok | skipped | failed
	Reason string `json:"reason,omitempty"` // no_space | network | checksum | other (failed only)
}

var bootInstallStatuses = map[string]bool{"ok": true, "skipped": true, "failed": true}
var bootInstallReasons = map[string]bool{"no_space": true, "network": true, "checksum": true, "other": true}

// readBootInstallStatus parses the file; unknown statuses and reasons are dropped rather than
// passed on, so the Console only ever sees the closed set it has strings for.
func readBootInstallStatus() map[string]bootInstall {
	b, err := os.ReadFile(bootInstallStatusPath)
	if err != nil {
		return nil
	}
	out := map[string]bootInstall{}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) < 2 || !bootInstallStatuses[f[1]] {
			continue
		}
		bi := bootInstall{Status: f[1]}
		if bi.Status == "failed" {
			bi.Reason = "other"
			if len(f) > 2 && bootInstallReasons[f[2]] {
				bi.Reason = f[2]
			}
		}
		out[f[0]] = bi // a later line for the same tool wins
	}
	return out
}
