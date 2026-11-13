// Package datadurable judges whether the Control Plane's data root survives the
// replacement of its container, and finds internal git ledger rows whose bare repository
// is gone (issue #4089). On ECS the data root is the task's ephemeral disk, so the
// internal git provider's repositories vanish on every task replacement while their ledger
// rows stay and keep being listed.
package datadurable

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/datalayout"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Mount is one line of /proc/self/mountinfo, reduced to what the check needs.
type Mount struct {
	Point  string
	FSType string
}

// ParseMountInfo reads /proc/self/mountinfo. Format (proc(5)):
//
//	id parent maj:min root mountpoint opts [optional fields...] - fstype source superopts
//
// The optional fields have no fixed count, so the " - " separator is what locates fstype.
// Mount points escape space, tab, newline and backslash as octal (\040).
func ParseMountInfo(r io.Reader) []Mount {
	var out []Mount
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 7 {
			continue
		}
		sep := -1
		for i := 6; i < len(f); i++ {
			if f[i] == "-" {
				sep = i
				break
			}
		}
		if sep < 0 || sep+1 >= len(f) {
			continue
		}
		out = append(out, Mount{Point: unescapeMountField(f[4]), FSType: f[sep+1]})
	}
	return out
}

func unescapeMountField(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			var v int
			ok := true
			for _, c := range s[i+1 : i+4] {
				if c < '0' || c > '7' {
					ok = false
					break
				}
				v = v*8 + int(c-'0')
			}
			if ok {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// pathUnder reports whether p is dir itself or inside it, on path-segment boundaries
// ("/tmpfoo" is not under "/tmp").
func pathUnder(p, dir string) bool {
	if dir == "/" {
		return true
	}
	return p == dir || strings.HasPrefix(p, dir+"/")
}

// nonDurableFS are the filesystem types whose contents do not outlive the container:
// memory-backed ones, and the overlay a container's root filesystem is made of. A volume
// or bind mount of the data directory is a different mount with its own type, which is
// why the lookup takes the longest matching mount point and not just the root.
var nonDurableFS = map[string]bool{"tmpfs": true, "ramfs": true, "overlay": true}

// Durability judges whether root survives a replacement of the container. It
// answers (true, "") when it does, or when it cannot tell: a check that guesses wrong
// toward "not durable" would switch a working feature off, and the operator's own
// AF_DATA_DURABLE_REQUIRED is only a safety net. mounts is the parsed mountinfo (nil when
// unreadable); resolve maps a path to its symlink-free form and may be the identity.
func Durability(root string, mounts []Mount, resolve func(string) string) (bool, string) {
	p := filepath.Clean(root)
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if resolve != nil {
		p = resolve(p)
	}
	// /tmp is ephemeral everywhere the CP runs, whatever it is mounted on.
	if pathUnder(p, "/tmp") {
		return false, fmt.Sprintf("data directory %s is under /tmp", p)
	}
	best := -1
	var fstype string
	for _, m := range mounts {
		// Later entries of an equal mount point stack on top of earlier ones.
		if pathUnder(p, m.Point) && len(m.Point) >= best {
			best, fstype = len(m.Point), m.FSType
		}
	}
	if best >= 0 && nonDurableFS[fstype] {
		return false, fmt.Sprintf("data directory %s is on a %s filesystem", p, fstype)
	}
	return true, ""
}

// ResolveExisting follows symlinks through the deepest part of p that exists: the data
// root itself may not have been created yet.
func ResolveExisting(p string) string {
	rest := ""
	for cur := p; ; {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// Refused answers whether a deployment that set AF_DATA_DURABLE_REQUIRED (required is the
// raw value) must refuse internal git: it returns the reason the data root is not durable,
// or "" when the feature may run, including when the variable is unset.
func Refused(required, root string, mounts []Mount, resolve func(string) string) string {
	if !EnvTruthy(required) {
		return ""
	}
	if durable, why := Durability(root, mounts, resolve); !durable {
		return why
	}
	return ""
}

// EnvTruthy reads a boolean environment value.
func EnvTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// Lister is the slice of the store the orphan scan reads.
type Lister interface {
	ListTenants(ctx context.Context) ([]store.Tenant, error)
	ListGitReposByTenant(ctx context.Context, tenantID string) ([]store.GitRepo, error)
}

// MissingGitRepos lists the ledger repositories ("<slug>/<name>") whose bare repository
// is absent from the data root, in tenant then name order. Nothing is deleted: the rows
// are the only record of what was lost. A push alone cannot restore one: create rejects the
// existing name and git-http-backend has no repository to receive into, so recovery is to
// delete the row and re-create the repository, then push from a member's clone.
func MissingGitRepos(ctx context.Context, st Lister, root string) ([]string, error) {
	tenants, err := st.ListTenants(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, t := range tenants {
		repos, err := st.ListGitReposByTenant(ctx, t.ID)
		if err != nil {
			return nil, err
		}
		for _, g := range repos {
			if _, err := os.Stat(datalayout.GitRepoDir(root, t.Slug, g.Name)); os.IsNotExist(err) {
				out = append(out, t.Slug+"/"+g.Name)
			}
		}
	}
	return out, nil
}
