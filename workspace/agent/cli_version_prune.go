package main

// Old agent CLI versions left in home, removed once per Agent boot.
//
// Two CLIs unpack a full copy of themselves for every version they have ever run and never
// remove the old ones, and home outlives the image, so every pin bump or opt-in update
// leaves one behind (measured: copilot 15 versions / 2.7G, cursor 12 versions / 4.6G in
// one home):
//
//   - copilot: the platform binary extracts itself into ~/.cache/copilot/pkg/<platform>/<ver>/.
//   - cursor: the installer (entrypoint boot-install and upstream install.sh alike) unpacks
//     into ~/.local/share/cursor-agent/versions/<ver>/ and repoints the launcher symlink.
//
// Unlike the tool caches (tool_caches.go) these are not caches anyone reads again, so they
// go without asking. What stays for each CLI:
//
//   - the version the launcher currently resolves to,
//   - the versions.json pin (the self-update opt-out relinks to the pinned directory
//     instead of downloading it again, entrypoint.sh), and
//   - any version a live process has its executable, a mapping or an open file under.
//
// When in doubt nothing goes: an unresolvable launcher, a running copilot whose version
// cannot be told, or a directory name that is not a version all leave that CLI untouched.
// Versions change only in the entrypoint, which runs before the Agent, so one pass at boot
// is enough; a process started after the scan can only be on the current version.

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// npmGlobalRoots are the node_modules trees a copilot install can live in (the lean
// boot-install and the self-update shadow use ~/.local, a baked image /usr/local). A var
// so tests can swap it.
var npmGlobalRoots = func(home string) []string {
	return []string{filepath.Join(home, ".local/lib/node_modules"), "/usr/local/lib/node_modules"}
}

// lookPathFn resolves a command on PATH; a var because tests must not find the real CLI
// installed on the machine running them.
var lookPathFn = exec.LookPath

// freshVersionAge is how recently a version directory may have changed and still be left
// alone; pruneNow is the clock it is measured against, a var so tests can move it.
const freshVersionAge = time.Hour

var pruneNow = time.Now

var (
	copilotVersionName = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$`)
	cursorVersionName  = regexp.MustCompile(`^[0-9]{4}\.[0-9]{2}\.[0-9]{2}-[0-9a-f]+$`)
)

// cliVersionStore is one CLI's pile of version directories.
type cliVersionStore struct {
	name string
	// roots are the directories whose children are version directories.
	roots []string
	// versionName says which children are versions; anything else is left alone.
	versionName *regexp.Regexp
	// current returns the versions the launchers resolve to; ok=false means it could not
	// be told, and nothing is removed.
	current func() (vers []string, ok bool)
	// exeVersion names the version of a running executable that is this CLI but lives
	// outside the roots; ok=false means it is this CLI and its version cannot be told.
	// is=false means the executable is not this CLI.
	exeVersion func(exe string) (ver string, is, ok bool)
}

type prunedVersion struct {
	CLI     string
	Version string
	Bytes   int64
}

// pruneOldCLIVersionsAtBoot is the boot entry point. Outside a Workspace image (no
// versions.json, e.g. the native runtime on someone's own machine) home is not ours to
// tidy, so it does nothing.
func pruneOldCLIVersionsAtBoot() {
	b, err := os.ReadFile(buildPinsPath)
	if err != nil {
		return
	}
	pins := map[string]string{}
	if json.Unmarshal(b, &pins) != nil {
		return
	}
	var total int64
	for _, p := range pruneOldCLIVersions(homeDir(), pins) {
		log.Printf("cli-versions: removed %s %s (%d MiB)", p.CLI, p.Version, p.Bytes>>20)
		total += p.Bytes
	}
	if total > 0 {
		log.Printf("cli-versions: freed %d MiB of old CLI versions", total>>20)
	}
}

func pruneOldCLIVersions(home string, pins map[string]string) []prunedVersion {
	var out []prunedVersion
	for _, s := range cliVersionStores(home) {
		out = append(out, s.prune(pins[s.name])...)
	}
	return out
}

func cliVersionStores(home string) []cliVersionStore {
	cursorRoot := filepath.Join(home, ".local/share/cursor-agent/versions")
	copilotPkg := filepath.Join(home, ".cache/copilot/pkg")
	var copilotRoots []string
	if ents, err := os.ReadDir(copilotPkg); err == nil {
		for _, e := range ents {
			if e.IsDir() {
				copilotRoots = append(copilotRoots, filepath.Join(copilotPkg, e.Name()))
			}
		}
	}
	return []cliVersionStore{
		{
			name:        "cursor",
			roots:       []string{cursorRoot},
			versionName: cursorVersionName,
			current:     func() ([]string, bool) { return cursorCurrent(home, cursorRoot) },
			// The bundle's own node is the executable, and it lives under the root.
			exeVersion: func(string) (string, bool, bool) { return "", false, true },
		},
		{
			name:        "copilot",
			roots:       copilotRoots,
			versionName: copilotVersionName,
			current:     func() ([]string, bool) { return copilotCurrent(home) },
			exeVersion:  copilotExeVersion,
		},
	}
}

// cursorCurrent reads the version out of the launcher symlinks (`agent` is the alias
// upstream install.sh adds). A launcher that exists but points elsewhere makes the answer
// unknown; with neither present there is nothing current in home.
func cursorCurrent(home, root string) ([]string, bool) {
	var vers []string
	for _, n := range []string{"cursor-agent", "agent"} {
		link := filepath.Join(home, ".local/bin", n)
		if _, err := os.Lstat(link); os.IsNotExist(err) {
			continue
		}
		v := versionUnder(root, readLinkAbs(link))
		if v == "" {
			if n == "agent" {
				continue // a bare `agent` may be some other tool
			}
			return nil, false
		}
		vers = append(vers, v)
	}
	if len(vers) == 0 {
		return nil, false
	}
	return vers, true
}

// copilotCurrent is the version of every @github/copilot install that could be the one on
// PATH.
func copilotCurrent(home string) ([]string, bool) {
	var dirs []string
	for _, r := range npmGlobalRoots(home) {
		dirs = append(dirs, filepath.Join(r, "@github/copilot"))
	}
	if p, err := lookPathFn("copilot"); err == nil {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			dirs = append(dirs, filepath.Dir(real))
		}
	}
	var vers []string
	for _, d := range dirs {
		v, err := packageVersion(filepath.Join(d, "package.json"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			// Installed but unreadable (or mid-install): which version it is cannot be told.
			return nil, false
		}
		vers = append(vers, v)
		// The platform package is what extracts into pkg/<ver>, and a copilot started after
		// the /proc scan runs that version. It moves in lockstep with the wrapper, but
		// keeping it too means a mismatch can never delete what a new session needs.
		plats, _ := filepath.Glob(filepath.Join(d, "node_modules/@github/copilot-*/package.json"))
		for _, pj := range plats {
			pv, err := packageVersion(pj)
			if err != nil {
				return nil, false
			}
			vers = append(vers, pv)
		}
	}
	return vers, len(vers) > 0
}

// copilotExeVersion recognises the platform binary (…/@github/copilot-<platform>/copilot)
// and reads its version from the package beside it. A replaced binary shows as
// "(deleted)" and its version is gone with it.
func copilotExeVersion(exe string) (string, bool, bool) {
	clean := strings.TrimSuffix(exe, " (deleted)")
	if filepath.Base(clean) != "copilot" || !strings.HasPrefix(filepath.Base(filepath.Dir(clean)), "copilot-") {
		return "", false, true
	}
	if clean != exe {
		return "", true, false
	}
	v, err := packageVersion(filepath.Join(filepath.Dir(clean), "package.json"))
	return v, true, err == nil
}

// packageVersion reads the version out of a package.json; a file without one is an error.
func packageVersion(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var p struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return "", err
	}
	if p.Version == "" {
		return "", fmt.Errorf("%s: no version", path)
	}
	return p.Version, nil
}

func readLinkAbs(link string) string {
	t, err := os.Readlink(link)
	if err != nil {
		return ""
	}
	if !filepath.IsAbs(t) {
		t = filepath.Join(filepath.Dir(link), t)
	}
	return filepath.Clean(t)
}

// versionUnder returns the first path component of p below root, or "". root is compared
// both as written and resolved: /proc reports resolved paths, and ~/.cache or ~/.local/share
// can be a symlink (onto $AF_WS_SCRATCH storage, say), so a running version would otherwise
// go unrecognised.
func versionUnder(root, p string) string {
	roots := []string{root}
	if real, err := filepath.EvalSymlinks(root); err == nil && real != root {
		roots = append(roots, real)
	}
	for _, r := range roots {
		if rel, ok := strings.CutPrefix(p, r+string(filepath.Separator)); ok {
			v, _, _ := strings.Cut(rel, string(filepath.Separator))
			return v
		}
	}
	return ""
}

func (s cliVersionStore) prune(pin string) []prunedVersion {
	if len(s.roots) == 0 {
		return nil
	}
	cur, ok := s.current()
	if !ok {
		return nil
	}
	keep := map[string]bool{pin: true}
	for _, v := range cur {
		keep[v] = true
	}
	inUse, err := s.versionsInUse()
	if err != nil {
		log.Printf("cli-versions: %s: %v; keeping every version", s.name, err)
		return nil
	}
	for v := range inUse {
		keep[v] = true
	}
	var out []prunedVersion
	for _, root := range s.roots {
		ents, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range ents {
			v := e.Name()
			// DirEntry.IsDir is false for a symlink, which is not ours to follow.
			if !e.IsDir() || keep[v] || !s.versionName.MatchString(v) {
				continue
			}
			// A version that appeared moments ago may belong to an install racing this pass
			// (npm run by hand, a first run extracting itself) that the scan above could not
			// see yet; the next boot takes it if it is really old.
			if info, err := e.Info(); err != nil || pruneNow().Sub(info.ModTime()) < freshVersionAge {
				continue
			}
			dir := filepath.Join(root, v)
			budget := toolCacheMaxEntries
			bytes, _ := walkSize(dir, &budget)
			if err := removeTree(dir); err != nil {
				log.Printf("cli-versions: remove %s: %v", dir, err)
				continue
			}
			out = append(out, prunedVersion{CLI: s.name, Version: v, Bytes: bytes})
		}
	}
	return out
}

// versionsInUse collects the versions any process has its executable, a mapping or an open
// file under. It fails closed: an error when /proc cannot be listed, when a process that
// could be this CLI cannot be read (a process that has since exited is fine), or when a
// process is this CLI but its version cannot be told.
//
// Some processes deny exe, maps and fd: another user's (home is often world-readable, so
// they could still be running a copy from it), or our own that made itself non-dumpable
// (ssh-agent does). Failing closed on every such process would stop the prune for good, so
// their still-readable cmdline decides whether they could be this CLI.
func (s cliVersionStore) versionsInUse() (map[string]bool, error) {
	used := map[string]bool{}
	note := func(p string) {
		p = strings.TrimSuffix(p, " (deleted)")
		for _, r := range s.roots {
			if v := versionUnder(r, p); v != "" {
				used[v] = true
			}
		}
	}
	ents, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, err
	}
	for _, e := range ents {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		pd := filepath.Join(procRoot, e.Name())
		unreadable := func(err error) error {
			if os.IsPermission(err) && !s.cmdlineMentions(pd) {
				return nil
			}
			return fmt.Errorf("pid %s: %w", e.Name(), err)
		}
		exe, err := os.Readlink(filepath.Join(pd, "exe"))
		switch {
		case err == nil:
			note(exe)
			v, is, ok := s.exeVersion(exe)
			if is && !ok {
				return nil, fmt.Errorf("pid %s runs it at a version that cannot be told", e.Name())
			}
			if is {
				used[v] = true
			}
		case !os.IsNotExist(err): // ENOENT: exited, a zombie or a kernel thread
			if err := unreadable(err); err != nil {
				return nil, err
			}
			continue
		}
		b, err := os.ReadFile(filepath.Join(pd, "maps"))
		if err != nil && !os.IsNotExist(err) {
			if err := unreadable(err); err != nil {
				return nil, err
			}
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			if i := strings.IndexByte(line, '/'); i >= 0 {
				note(line[i:])
			}
		}
		fds, err := os.ReadDir(filepath.Join(pd, "fd"))
		if err != nil && !os.IsNotExist(err) {
			if err := unreadable(err); err != nil {
				return nil, err
			}
			continue
		}
		for _, fd := range fds {
			t, err := os.Readlink(filepath.Join(pd, "fd", fd.Name()))
			if err != nil && !os.IsNotExist(err) {
				if err := unreadable(err); err != nil {
					return nil, err
				}
				continue
			}
			note(t)
		}
	}
	return used, nil
}

// cmdlineMentions says whether a process's argv names this CLI anywhere; unreadable counts
// as yes.
func (s cliVersionStore) cmdlineMentions(pd string) bool {
	b, err := os.ReadFile(filepath.Join(pd, "cmdline"))
	if err != nil {
		return !os.IsNotExist(err)
	}
	return strings.Contains(string(b), s.name)
}
