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
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
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
		if v := packageVersion(filepath.Join(d, "package.json")); v != "" {
			vers = append(vers, v)
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
	v := packageVersion(filepath.Join(filepath.Dir(clean), "package.json"))
	return v, true, v != ""
}

func packageVersion(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var p struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(b, &p) != nil {
		return ""
	}
	return p.Version
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

// versionUnder returns the first path component of p below root, or "".
func versionUnder(root, p string) string {
	rel, ok := strings.CutPrefix(p, root+string(filepath.Separator))
	if !ok {
		return ""
	}
	v, _, _ := strings.Cut(rel, string(filepath.Separator))
	return v
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
	inUse, ok := s.versionsInUse()
	if !ok {
		log.Printf("cli-versions: %s is running at a version that cannot be told; keeping every version", s.name)
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
// file under. ok=false when a process is this CLI but its version cannot be told.
func (s cliVersionStore) versionsInUse() (map[string]bool, bool) {
	used := map[string]bool{}
	note := func(p string) {
		p = strings.TrimSuffix(p, " (deleted)")
		for _, r := range s.roots {
			if v := versionUnder(r, p); v != "" {
				used[v] = true
			}
		}
	}
	ents, _ := os.ReadDir(procRoot)
	for _, e := range ents {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		pd := filepath.Join(procRoot, e.Name())
		if exe, err := os.Readlink(filepath.Join(pd, "exe")); err == nil {
			note(exe)
			v, is, ok := s.exeVersion(exe)
			if is && !ok {
				return nil, false
			}
			if is {
				used[v] = true
			}
		}
		if b, err := os.ReadFile(filepath.Join(pd, "maps")); err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				if i := strings.IndexByte(line, '/'); i >= 0 {
					note(line[i:])
				}
			}
		}
		fds, _ := os.ReadDir(filepath.Join(pd, "fd"))
		for _, fd := range fds {
			if t, err := os.Readlink(filepath.Join(pd, "fd", fd.Name())); err == nil {
				note(t)
			}
		}
	}
	return used, true
}
