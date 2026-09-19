package harness

// cwd.go is the ADR 0093 decision 5 cwd confinement every builtin filesystem tool
// (tools_fs.go) goes through: a tool must not read or write outside the session's
// own working directory. This is the same judgement workspace/agent/fs.go already
// makes for the Console's browse root (safeBrowsePath + fsResolvedOKUnder) — ADR
// 0081 decision 3 says not to write it twice, and fs.go is package main so it
// cannot be imported here. The shared primitive lives in internal/pathguard; this
// file only adapts it to a single cwd root (fs.go instead juggles several roots
// plus a denylist, which this package has no equivalent of — a tool's cwd is the
// user's own working directory, not home, so there is nothing in it to deny).

import (
	"fmt"
	"path/filepath"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/pathguard"
)

// resolvePath maps a tool-supplied path argument (relative to cwd, or an absolute
// path the caller expects to already be inside cwd) onto an absolute path,
// rejecting anything that lexically or (following symlinks) actually escapes cwd.
func resolvePath(cwd, p string) (string, error) {
	if cwd == "" {
		return "", fmt.Errorf("no working directory is configured for this session")
	}
	rel := p
	if filepath.IsAbs(p) {
		r, err := filepath.Rel(cwd, filepath.Clean(p))
		if err != nil {
			return "", fmt.Errorf("path %q is outside the working directory", p)
		}
		rel = r
	}
	full, _, ok := pathguard.Resolve(cwd, rel)
	if !ok {
		return "", fmt.Errorf("path %q escapes the working directory", p)
	}
	if _, ok := pathguard.ResolveUnder(full, cwd); !ok {
		return "", fmt.Errorf("path %q escapes the working directory", p)
	}
	return full, nil
}
