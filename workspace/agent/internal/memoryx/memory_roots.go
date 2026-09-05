package memoryx

// Version control for agent memory (docs/log/39 / ADR 0022) — root declarations and the
// live→staging copy.
//
// Only two local trees are subject to "agent memory" versioning (the docs/log/39 inventory),
// and this file holds them as a declaration table. Once opencode or another agent implements
// memory upstream, one extra line in memoryRoots() gives it snapshot, rollback and export.
//
// The core of ★1 (collateral capture): scope is decided by the allowlist globs alone. Never
// invert this into a denylist. This file's responsibility is that no code path exists by
// which the live tree (883MB of transcripts, .credentials.json, settings.json, codex's
// derived sqlite state and its own .git) can enter the repo, and memory_snapshot_test.go
// verifies that against a real data layout.

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
)

// memoryRoot declares one version-controlled memory root.
type memoryRoot struct {
	Kind string // "claude" | "codex" — must match the session kind (the busy check reads it)
	// Label is the Console display name. i18n lives on the front end, so this is the plain
	// kind name.
	Label string
	// Dir is the absolute live path, resolved on every call: tests swap HOME /
	// CLAUDE_CONFIG_DIR, so it must not be cached.
	Dir string
	// RepoPrefix is the namespace inside the bare repo, kept separate per kind
	// (docs/log/39 item 1).
	RepoPrefix string
	// Include is the allowlist of globs matched against the path relative to Dir. `**`
	// matches zero or more segments. A file that does not match is never read.
	Include []string
	// Exclude narrows Include. Codex's own .git (the diff baseline of its integration
	// pipeline) must never be touched, and never enters the repo.
	Exclude []string
	// Scopes says whether directory-grained partial rollback is possible (claude=true,
	// codex=false). For codex the project split is an entry INSIDE a file, so there is no
	// directory granularity to roll back to.
	Scopes bool
	// RequireDir makes a root active only while its live dir exists. Codex's memories
	// feature is off by default, so on an environment where it was never enabled the root
	// itself does not appear.
	RequireDir bool
}

// memoryRootDecls is the declaration table itself, before existence detection. Kept separate
// from memoryRoots() so tests can see every entry.
func memoryRootDecls() []memoryRoot {
	return []memoryRoot{
		{
			Kind:       "claude",
			Label:      "Claude Code",
			Dir:        filepath.Join(claude.ConfigDir(), "projects"),
			RepoPrefix: "claude/projects",
			// projects/<slug>/memory/** only. The <sid>.jsonl transcripts sitting at the
			// same level, and .credentials.json / settings.json in the parent, are
			// structurally out of scope.
			Include: []string{"*/memory/**"},
			Scopes:  true,
		},
		{
			Kind:  "codex",
			Label: "Codex",
			// Use the same definition as the enabling path (codex.setMemories). If the two
			// drift apart it breaks as "enabled, yet the root never appears", which is hard
			// to trace.
			Dir:        codex.MemoriesDir(),
			RepoPrefix: "codex",
			Include:    []string{"**"},
			// Codex's integration pipeline uses .git as its diff baseline (upstream PR
			// #18982), and phase2_workspace_diff.md is an intermediate product of that
			// integration. memories_1.sqlite lives outside Dir (in ~/.codex/) and is
			// therefore already out of scope, but is listed in case it ever moves.
			Exclude:    []string{".git/**", "phase2_workspace_diff.md", "*.sqlite", "*.sqlite-*"},
			Scopes:     false,
			RequireDir: true,
		},
	}
}

// memoryRoots returns the roots active in this environment (a RequireDir one only while its
// live dir exists).
func memoryRoots() []memoryRoot {
	var out []memoryRoot
	for _, r := range memoryRootDecls() {
		if r.RequireDir {
			if st, err := os.Stat(r.Dir); err != nil || !st.IsDir() {
				continue
			}
		}
		out = append(out, r)
	}
	return out
}

// memoryInactiveRoot describes a root that is declared but not active in this environment
// (docs/log/39 P4). Dropping a RequireDir root silently would leave the Console unable to say
// why codex memory is missing, and the user with no route to the switch that enables it.
type memoryInactiveRoot struct {
	Kind   string `json:"kind"`
	Label  string `json:"label"`
	Reason string `json:"reason"` // "codex_memories_disabled" | "codex_memories_pending" | "absent"
	// Toggleable says whether the Console can enable/disable it. When true, Enabled carries
	// the current value.
	Toggleable bool `json:"toggleable"`
	Enabled    bool `json:"enabled"`
}

// memoryInactiveRoots returns the roots memoryRoots() dropped, each with its reason.
func memoryInactiveRoots() []memoryInactiveRoot {
	active := map[string]bool{}
	for _, r := range memoryRoots() {
		active[r.Kind] = true
	}
	out := []memoryInactiveRoot{}
	for _, r := range memoryRootDecls() {
		if active[r.Kind] {
			continue
		}
		v := memoryInactiveRoot{Kind: r.Kind, Label: r.Label, Reason: "absent"}
		if r.Kind == "codex" {
			v.Toggleable = true
			v.Enabled = codex.MemoriesEnabled()
			// Even once enabled, ~/.codex/memories does not appear until codex next runs.
			// "the setting did not take" and "not created yet" are different states, so
			// they are reported apart.
			if v.Enabled {
				v.Reason = "codex_memories_pending"
			} else {
				v.Reason = "codex_memories_disabled"
			}
		}
		out = append(out, v)
	}
	return out
}

// memoryRootByKind looks a root up by kind (for scope resolution in the REST layer).
func memoryRootByKind(kind string) (memoryRoot, bool) {
	for _, r := range memoryRoots() {
		if r.Kind == kind {
			return r, true
		}
	}
	return memoryRoot{}, false
}

// memoryAllowed reports whether rel — the slash-separated path relative to Dir — is under
// version control. The allowlist is the only decision; Exclude only narrows within Include.
func memoryAllowed(r memoryRoot, rel string) bool {
	rel = filepath.ToSlash(rel)
	if rel == "" || rel == "." || strings.HasPrefix(rel, "../") || rel == ".." {
		return false
	}
	matched := false
	for _, p := range r.Include {
		if memoryGlobMatch(p, rel) {
			matched = true
			break
		}
	}
	if !matched {
		return false
	}
	for _, p := range r.Exclude {
		if memoryGlobMatch(p, rel) {
			return false
		}
	}
	return true
}

// memoryGlobMatch is a glob match that understands `**` (zero or more segments), delegating
// wildcards within a segment to path.Match. This thin wrapper exists because path/filepath's
// Match cannot handle `**` and its `*` does not cross a separator.
func memoryGlobMatch(pattern, name string) bool {
	return memoryGlobSegs(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func memoryGlobSegs(pat, seg []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			// `**` swallows zero or more segments; at the end it matches all that is left.
			if len(pat) == 1 {
				return true
			}
			for i := 0; i <= len(seg); i++ {
				if memoryGlobSegs(pat[1:], seg[i:]) {
					return true
				}
			}
			return false
		}
		if len(seg) == 0 {
			return false
		}
		ok, err := path.Match(pat[0], seg[0])
		if err != nil || !ok {
			return false
		}
		pat, seg = pat[1:], seg[1:]
	}
	return len(seg) == 0
}

// memoryFile is the coordinate of one collected file, used by both the listing API and the
// copy.
type memoryFile struct {
	Rel   string // path relative to root.Dir (slash-separated)
	Abs   string
	Size  int64
	MTime int64 // Unix seconds
}

// memoryCollect enumerates the allowlist-matching files under root. Symlinks are not
// followed: to close every route out of the allowlist (transcripts, credentials), a link is
// ignored whether it points at a file or a directory (★1).
func memoryCollect(r memoryRoot) []memoryFile {
	var out []memoryFile
	root := r.Dir
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// Skip an unreadable branch silently (other processes keep rewriting live).
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if p == root {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		// Reject symlinks of any kind (a target outside the allowlist would be reachable).
		if d.Type()&fs.ModeSymlink != 0 {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			// Prune a directory excluded together with everything under it (codex's .git)
			// before descending, by matching a "<dir>/**" Exclude against the dir itself.
			for _, ex := range r.Exclude {
				if base, ok := strings.CutSuffix(ex, "/**"); ok && memoryGlobMatch(base, rel) {
					return fs.SkipDir
				}
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if !memoryAllowed(r, rel) {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		out = append(out, memoryFile{Rel: rel, Abs: p, Size: info.Size(), MTime: info.ModTime().Unix()})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Rel < out[j].Rel })
	return out
}

// memorySyncToStaging copies root's allowlist-matching files under staging/<RepoPrefix>/. So
// that a file deleted on the live side does not linger in the snapshot, the whole prefix is
// removed first and then rewritten (a few hundred KB, so effectively free; docs/log/39).
func memorySyncToStaging(r memoryRoot, stagingRoot string) (int, error) {
	dst := filepath.Join(stagingRoot, filepath.FromSlash(r.RepoPrefix))
	if err := os.RemoveAll(dst); err != nil {
		return 0, fmt.Errorf("staging reset %s: %w", r.RepoPrefix, err)
	}
	files := memoryCollect(r)
	if len(files) == 0 {
		return 0, nil
	}
	for _, f := range files {
		out := filepath.Join(dst, filepath.FromSlash(f.Rel))
		if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
			return 0, err
		}
		if err := memoryCopyFile(f.Abs, out); err != nil {
			// Live keeps moving, so skip a file that vanished or became unreadable.
			continue
		}
	}
	return len(files), nil
}

func memoryCopyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// memoryScopeSlug extracts the project slug from a claude in-repo path
// (claude/projects/<slug>/memory/...). For a path of a root without scopes, ok=false.
func memoryScopeSlug(repoPath string) (string, bool) {
	const prefix = "claude/projects/"
	if !strings.HasPrefix(repoPath, prefix) {
		return "", false
	}
	rest := strings.TrimPrefix(repoPath, prefix)
	slug, _, found := strings.Cut(rest, "/")
	if !found || slug == "" {
		return "", false
	}
	return slug, true
}

// memorySlugDisplay turns a claude slug (an absolute path with "/" flattened to "-") into a
// human-readable name (★6). Reverse-mapping it to a real directory under ~/repos is the
// reliable route, so that is tried first; failing that, the part after "-repos-" is used.
func memorySlugDisplay(slug string) string {
	root := gitx.ReposRoot()
	if ents, err := os.ReadDir(root); err == nil {
		for _, e := range ents {
			if !e.IsDir() {
				continue
			}
			if strings.ReplaceAll(filepath.Join(root, e.Name()), "/", "-") == slug {
				return e.Name()
			}
		}
	}
	if i := strings.LastIndex(slug, "-repos-"); i >= 0 {
		if name := slug[i+len("-repos-"):]; name != "" {
			return name
		}
	}
	return strings.TrimPrefix(slug, "-")
}
