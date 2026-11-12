// Package datalayout names everything that lives directly under the Control Plane's
// data root (WS_DATA) other than a home.
//
// The data root is one flat namespace shared by three kinds of entry: a default-tenant
// member's home (<root>/<user_key>), another tenant's directory (<root>/<slug>/<user_key>),
// and the names below. Nothing on disk keeps them apart, so a tenant slug or a
// default-tenant user key that equals one of these names silently mixes one party's files
// into another's (a tenant "git" puts its members' homes inside the internal git tree).
// The store refuses such a slug or key against Reserved.
//
// A new CP path under the data root must be a constant here and be joined through it —
// datalayout_test.go in the module root fails on a string literal joined onto dataRoot,
// and on a $WS_DATA/<name> in deploy/ that is not in Reserved.
package datalayout

import (
	"path/filepath"
	"strings"
)

const (
	// GitDir holds the internal git bare repositories, <root>/git/<slug>/<repo>.git.
	GitDir = "git"
	// DBFile is the default SQLite metadata store (AF_DB overrides the path).
	DBFile = "control-plane.db"
	// GitTokenMasterFile is the dev-mode git token signing master.
	GitTokenMasterFile = "git-token-master.key"
	// DrawioStencilsDir is the draw.io stencil cache (also drawio-preseed's default).
	DrawioStencilsDir = "drawio-stencils"
)

// sqliteSidecars are the files SQLite writes beside DBFile (WAL mode and rollback journal).
var sqliteSidecars = []string{"-wal", "-shm", "-journal"}

// deployNames are placed under the data root by the deployment, not by CP code:
// shared/ (WS_JVM_DIR in compose and the local scripts, the native rootfs), caddy/
// (compose), guide-staged/ (AF_DOCS_DIR in the local scripts).
var deployNames = []string{"shared", "caddy", "guide-staged"}

// Reserved returns every name a tenant slug or a default-tenant user key must not take.
func Reserved() []string {
	out := []string{GitDir, DBFile, GitTokenMasterFile, DrawioStencilsDir}
	for _, s := range sqliteSidecars {
		out = append(out, DBFile+s)
	}
	return append(out, deployNames...)
}

// IsReserved reports whether name collides with a reserved entry. The comparison ignores
// case because the native runtime can run on a case-insensitive file system, where
// "Git" and "git" are the same directory.
func IsReserved(name string) bool {
	for _, r := range Reserved() {
		if strings.EqualFold(name, r) {
			return true
		}
	}
	return false
}

// GitRoot is the internal git tree under the data root. Creation, serving, GC and the
// startup config backfill all resolve it here: a pass that joined its own copy of the
// path could silently scan a different tree than the one repositories live in.
func GitRoot(root string) string { return filepath.Join(root, GitDir) }

// GitTenantDir is one tenant's repository directory, <root>/git/<slug>.
func GitTenantDir(root, slug string) string { return filepath.Join(GitRoot(root), slug) }

// GitRepoDir is one bare repository, <root>/git/<slug>/<name>.git.
func GitRepoDir(root, slug, name string) string {
	return filepath.Join(GitTenantDir(root, slug), name+".git")
}
