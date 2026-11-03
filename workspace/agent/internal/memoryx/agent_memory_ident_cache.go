package memoryx

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
)

// Project identity is derived by up to four git processes (rev-parse, remote get-url,
// worktree list) and a working copy's answer changes rarely, yet every memory call re-derived
// it (#3255). agentMemProjectFor now remembers the answer per working copy, valid while the
// files git reads for it are unchanged.
//
// Soundness rests on two independent guards; a hit needs both:
//   - a stamp of every file the answer is read from (inode, size, mtime, ctime). git writes
//     config through a lock file and renames it over the old one, so a `remote set-url`
//     changes the inode even inside one timestamp tick.
//   - a hard TTL, for what no stamp covers (an include.path target, a retargeted ancestor
//     symlink, environment git reads that we do not list).
//
// A failed or unresolved identity is never cached: it may be transient (a lock, a missing
// binary) and the uncached path is the old behaviour.
const (
	agentMemIdentTTL = 30 * time.Second
	// agentMemIdentMax bounds the map; past it, expired entries go first, then everything.
	agentMemIdentMax = 512
)

type agentMemIdentEntry struct {
	proj  agentMemProject
	stamp string
	at    time.Time
}

var agentMemIdentCache = struct {
	sync.Mutex
	m   map[string]agentMemIdentEntry
	now func() time.Time
}{m: map[string]agentMemIdentEntry{}, now: time.Now}

// agentMemIdentEnv lists the environment variables that change what git answers about a
// working copy (which config files it reads, which repository it finds).
var agentMemIdentEnv = []string{
	"HOME", "XDG_CONFIG_HOME", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_NOSYSTEM",
	"GIT_CONFIG_COUNT", "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_CEILING_DIRECTORIES",
}

// agentMemIdentKey names the cache slot. The directory is the string the caller passed, not a
// resolved path: the same string can only name another directory through a symlink, which the
// stamp of the directory itself (an lstat) and the TTL cover. The repos root and the
// environment are part of the key because the answer depends on them.
func agentMemIdentKey(dir, repos string) string {
	var b strings.Builder
	b.WriteString(dir)
	b.WriteByte(0)
	b.WriteString(repos)
	for _, k := range agentMemIdentEnv {
		b.WriteByte(0)
		b.WriteString(os.Getenv(k))
	}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GIT_CONFIG_") {
			b.WriteByte(0)
			b.WriteString(kv)
		}
	}
	return b.String()
}

func agentMemStatLine(b *strings.Builder, path string) {
	fi, err := os.Lstat(path)
	if err != nil {
		fmt.Fprintf(b, "%s|absent\n", path)
		return
	}
	if fi.IsDir() {
		// A directory's size and times move with every file created in it (index.lock, HEAD.lock);
		// only its identity matters.
		fmt.Fprintf(b, "%s|dir|%s\n", path, statExtra(fi, false))
		return
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		to, _ := os.Readlink(path)
		fmt.Fprintf(b, "%s|link|%s\n", path, to)
		return
	}
	fmt.Fprintf(b, "%s|%d|%d|%d|%s\n", path, fi.Size(), fi.ModTime().UnixNano(), fi.Mode(), statExtra(fi, true))
}

// agentMemIdentStamp describes the files the identity of dir is read from. ok is false when
// the layout is not one it understands, in which case the caller does not cache.
func agentMemIdentStamp(dir string) (stamp string, ok bool) {
	// These make git look somewhere other than dir/.git; the files stamped below would then
	// not be the ones the answer comes from.
	for _, k := range []string{"GIT_DIR", "GIT_COMMON_DIR", "GIT_WORK_TREE", "GIT_CONFIG_PARAMETERS"} {
		if _, set := os.LookupEnv(k); set {
			return "", false
		}
	}
	var b strings.Builder
	agentMemStatLine(&b, dir)
	dotGit := filepath.Join(dir, ".git")
	agentMemStatLine(&b, dotGit)
	fi, err := os.Stat(dotGit)
	if err != nil {
		return "", false
	}
	gitDir := dotGit
	if fi.Mode().IsRegular() {
		// A linked worktree or submodule: ".git" is a file "gitdir: <path>".
		raw, err := os.ReadFile(dotGit)
		if err != nil {
			return "", false
		}
		p, found := strings.CutPrefix(strings.TrimSpace(string(raw)), "gitdir:")
		if !found {
			return "", false
		}
		gitDir = strings.TrimSpace(p)
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(dir, gitDir)
		}
		fmt.Fprintf(&b, "gitdir=%s\n", gitDir)
	} else if !fi.IsDir() {
		return "", false
	}
	agentMemStatLine(&b, gitDir)
	// HEAD decides whether git accepts the directory as a repository at all (and
	// includeIf.onbranch), so a deleted or rewritten one must miss.
	agentMemStatLine(&b, filepath.Join(gitDir, "HEAD"))
	common := gitDir
	if raw, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		agentMemStatLine(&b, filepath.Join(gitDir, "commondir"))
		common = strings.TrimSpace(string(raw))
		if !filepath.IsAbs(common) {
			common = filepath.Join(gitDir, common)
		}
		common = filepath.Clean(common)
		agentMemStatLine(&b, common)
		// The main working tree is the first `worktree list` entry, kept only if its .git exists.
		agentMemStatLine(&b, filepath.Join(filepath.Dir(common), ".git"))
	}
	agentMemStatLine(&b, filepath.Join(common, "HEAD"))
	cfgs := append([]string{filepath.Join(common, "config"), filepath.Join(common, "config.worktree"),
		filepath.Join(gitDir, "config.worktree")}, agentMemGitConfigFiles()...)
	for _, p := range cfgs {
		agentMemStatLine(&b, p)
		// Follow a symlinked config: replacing its target is not visible on the link itself.
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			fmt.Fprintf(&b, "%d|%d|%s\n", fi.Size(), fi.ModTime().UnixNano(), statExtra(fi, true))
			// An include's target and condition are outside the stamp: do not cache at all.
			if raw, err := os.ReadFile(p); err == nil && agentMemConfigHasInclude(raw) {
				return "", false
			}
		}
	}
	return b.String(), true
}

// agentMemGitConfigFiles are the user- and system-level config files git consults (they carry
// url.<base>.insteadOf, which `remote get-url` applies).
func agentMemGitConfigFiles() []string {
	var out []string
	if g, ok := os.LookupEnv("GIT_CONFIG_GLOBAL"); ok {
		out = append(out, g)
	} else {
		home := os.Getenv("HOME")
		xdg := os.Getenv("XDG_CONFIG_HOME")
		if xdg == "" && home != "" {
			xdg = filepath.Join(home, ".config")
		}
		if xdg != "" {
			out = append(out, filepath.Join(xdg, "git", "config"))
		}
		if home != "" {
			out = append(out, filepath.Join(home, ".gitconfig"))
		}
	}
	if s, ok := os.LookupEnv("GIT_CONFIG_SYSTEM"); ok {
		out = append(out, s)
	} else if os.Getenv("GIT_CONFIG_NOSYSTEM") == "" {
		out = append(out, "/etc/gitconfig")
	}
	return out
}

func agentMemIdentLookup(key, dir string) (*agentMemProject, bool) {
	c := &agentMemIdentCache
	c.Lock()
	e, hit := c.m[key]
	now := c.now()
	c.Unlock()
	if !hit || now.Sub(e.at) >= agentMemIdentTTL {
		return nil, false
	}
	if st, ok := agentMemIdentStamp(dir); !ok || st != e.stamp {
		return nil, false
	}
	p := e.proj
	return &p, true
}

// agentMemIdentStore keeps p under key. stamp was taken BEFORE the identity was derived, so a
// change racing with the derivation leaves a stale stamp (a later miss) rather than a stale
// answer under a fresh stamp.
func agentMemIdentStore(key, stamp string, p *agentMemProject) {
	c := &agentMemIdentCache
	c.Lock()
	defer c.Unlock()
	now := c.now()
	if len(c.m) >= agentMemIdentMax {
		for k, e := range c.m {
			if now.Sub(e.at) >= agentMemIdentTTL {
				delete(c.m, k)
			}
		}
		if len(c.m) >= agentMemIdentMax {
			clear(c.m)
		}
	}
	c.m[key] = agentMemIdentEntry{proj: *p, stamp: stamp, at: now}
}

// agentMemProjectCached is agentMemProjectIdent over a real directory, answered from the
// cache when the stamp still matches. Only a git working copy whose identity resolved is
// cached; every other outcome is derived afresh as before.
func agentMemProjectCached(dir string, derive func() (*agentMemProject, bool)) *agentMemProject {
	if dir == "" {
		p, _ := derive()
		return p
	}
	key := agentMemIdentKey(dir, gitx.ReposRoot())
	if p, ok := agentMemIdentLookup(key, dir); ok {
		return p
	}
	stamp, stampOK := agentMemIdentStamp(dir)
	p, identified := derive()
	if stampOK && identified && p != nil {
		agentMemIdentStore(key, stamp, p)
	}
	return p
}

// statExtra is the inode and ctime of fi: what an in-place edit or a rename-over leaves
// behind even when size and mtime come out the same.
func statExtra(fi os.FileInfo, ctime bool) string {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	if !ctime {
		return fmt.Sprint(st.Ino)
	}
	return fmt.Sprintf("%d,%d.%d", st.Ino, st.Ctim.Sec, st.Ctim.Nsec)
}

// agentMemConfigHasInclude reports whether a config file has an [include] or [includeIf]
// section. It over-matches (a comment or value containing the word) on purpose: a false
// positive only skips the cache.
func agentMemConfigHasInclude(raw []byte) bool {
	return strings.Contains(strings.ToLower(string(raw)), "include")
}
