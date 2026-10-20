package main

// What the entrypoint's boot-install did for each CLI on this start, so Settings › Toolchains
// can say "install failed: home disk full" where it used to show "—".
//
// entrypoint.sh writes one tab-separated line per CLI to bootInstallStatusPath, inside a private
// directory it created (/tmp is shared, so nothing is read from a path someone else could have
// planted):
//
//	<tool>\t<status>\t<reason>
//
// status is ok, skipped (already present) or failed; reason is set for failed only and is one
// of no_space, network, checksum, other. The file lives under /tmp, not in home: the failure
// being reported is usually a full home, and a status file that needed home to be written
// would vanish exactly then. /tmp is per container and wiped on restart, which matches what
// it describes: one boot's outcome. A missing or unreadable file means "unknown", never "ok".

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// bootInstallStatusPath is a var only so tests can point it at a temp file. The directory is the
// entrypoint's BI_DIR.
var bootInstallStatusPath = fmt.Sprintf("/tmp/af-boot-%d/install.tsv", os.Getuid())

// bootInstallMaxBytes bounds the read: a handful of CLIs need a few hundred bytes. Anything
// larger is not what the entrypoint wrote.
const bootInstallMaxBytes = 8 << 10

// ownedBy reports whether fi is owned by the current user.
func ownedBy(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}

// readBootInstallFile returns the file's bytes only when the directory is a real directory of
// ours and the file is a small regular file of ours; anything else (symlink, FIFO, foreign owner,
// too large) is "unknown". It is called with the tool-versions cache lock held, so it must never
// block: O_NONBLOCK keeps a FIFO from stalling the open, and the type is checked on the open
// descriptor, not on the name.
func readBootInstallFile(path string) ([]byte, bool) {
	dir, err := os.Lstat(filepath.Dir(path))
	if err != nil || !dir.IsDir() || !ownedBy(dir) {
		return nil, false
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || !ownedBy(fi) || fi.Size() > bootInstallMaxBytes {
		return nil, false
	}
	// The file can still grow between the stat and the read: cap the read itself.
	b, err := io.ReadAll(io.LimitReader(f, bootInstallMaxBytes+1))
	if err != nil || len(b) > bootInstallMaxBytes {
		return nil, false
	}
	return b, true
}

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
	b, ok := readBootInstallFile(bootInstallStatusPath)
	if !ok {
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
