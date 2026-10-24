package main

// toolchain_uninstall.go — listing and removing JDK / Node.js majors for Settings →
// Toolchains, so a member can give back the home-volume space of versions they no longer
// use (a JDK is ~350 MB, a Node major ~200 MB per patch).
//
// Only home-volume trees are ever removed. /usr/lib/jvm is provided by the deployment
// (baked or bind-mounted read-only) and the image's own node is not under ~/.nvm, so
// neither is offered.
//
// What is refused, and why:
//   - The selected major when nothing else would provide it: new sessions would get no
//     JAVA_HOME / node, and the next container start would download it again. A JDK
//     major that /usr/lib/jvm also has may go: resolution falls back to that copy.
//   - A major whose install job is running: the job writes into the same directory.
//
// A removal checks and stages in one step, then deletes. Staging renames the trees into
// <root>/.agent-fleet-trash, which neither discovery path looks at, while holding
//   - toolchainsMu, which the selection PUT also takes, so a major cannot become
//     selected between the check and the rename;
//   - the installer's mutex, for the HTTP install job's state;
//   - the root's install flock (installLock), which install-jdk / install-node hold for
//     the whole install even when run from a terminal, i.e. from another process.
// The delete then runs outside all of them (seconds for a JDK; the status poll must not
// wait). Only the trash directory is ever swept, never a name pattern elsewhere, and only
// when it is a real directory.

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// trashDir is where staged trees wait for deletion, under each root.
const trashDir = ".agent-fleet-trash"

// toolchainsMu orders the selection PUT against uninstall's check-and-stage.
var toolchainsMu sync.Mutex

// toolchainInstall is one installed tree (Java) or major (Node) in the listing.
type toolchainInstall struct {
	Major   string `json:"major"`
	Version string `json:"version"`
	// Source is "home" (the home volume, removable) or "system" (/usr/lib/jvm).
	Source string `json:"source"`
	Bytes  int64  `json:"bytes"`
	// InUse marks the tree JAVA_HOME / node resolves to for the selected major.
	InUse     bool `json:"in_use"`
	Removable bool `json:"removable"`
	// Reason says why a home tree cannot be removed: "selected" or "installing".
	Reason string `json:"reason,omitempty"`
}

type toolchainInstallsReply struct {
	Java []toolchainInstall `json:"java"`
	Node []toolchainInstall `json:"node"`
}

// sizeBudget caps the entries walked per tree; a Node major with npm is ~6000.
const sizeBudget = 200000

func treeBytes(dir string) int64 {
	budget := sizeBudget
	b, _ := walkSize(dir, &budget)
	return b
}

// homeJDKFor is this architecture's home-volume tree of a major, or "".
func homeJDKFor(major string) string {
	m, _ := filepath.Glob(filepath.Join(jvmHomeRoot(), "temurin-"+major+"-jdk*"))
	sort.Strings(m)
	return pickArchJDK(m)
}

func systemJDKFor(major string) string {
	m, _ := filepath.Glob(filepath.Join(jvmSystemRoot, "temurin-"+major+"-jdk*"))
	sort.Strings(m)
	return pickArchJDK(m)
}

// homeNodeTrees lists the ~/.nvm/versions/node directories of a major.
func homeNodeTrees(major string) []string {
	m, _ := filepath.Glob(filepath.Join(nvmNodeRoot(), "v"+major+".*"))
	var out []string
	for _, p := range m {
		if v := parseDotted(strings.TrimPrefix(filepath.Base(p), "v")); len(v) > 0 {
			out = append(out, p)
		}
	}
	return out
}

func listToolchainInstalls() toolchainInstallsReply {
	sweepTrash(jvmHomeRoot())
	sweepTrash(nvmNodeRoot())
	sel := readToolchains()
	selJava, selNode := selectedMajor(sel.Java), selectedMajor(sel.Node)
	jdkBusy := rootBusy(jvmHomeRoot(), &jdkInstaller)
	nodeBusy := rootBusy(nvmNodeRoot(), &nodeInstaller)
	reply := toolchainInstallsReply{Java: []toolchainInstall{}, Node: []toolchainInstall{}}

	for _, major := range installedJavaMajors() {
		resolved := javaHomeFor(major)
		sys := systemJDKFor(major)
		if home := homeJDKFor(major); home != "" {
			e := toolchainInstall{Major: major, Version: jdkVersion(home), Source: "home",
				Bytes: treeBytes(home), InUse: selJava == major && resolved == home}
			switch {
			case selJava == major && !independentJDK(sys, home):
				e.Reason = "selected"
			case jdkBusy:
				e.Reason = "installing"
			default:
				e.Removable = true
			}
			reply.Java = append(reply.Java, e)
		}
		if sys != "" {
			reply.Java = append(reply.Java, toolchainInstall{Major: major, Version: jdkVersion(sys),
				Source: "system", InUse: selJava == major && resolved == sys})
		}
	}

	for _, major := range installedNodeMajors() {
		trees := homeNodeTrees(major)
		if len(trees) == 0 {
			continue
		}
		var bytes int64
		for _, t := range trees {
			bytes += treeBytes(t)
		}
		e := toolchainInstall{Major: major, Source: "home", Bytes: bytes, InUse: selNode == major}
		if bin := nodeBinFor(major); bin != "" {
			e.Version = filepath.Base(filepath.Dir(bin))
		}
		switch {
		case selNode == major:
			e.Reason = "selected"
		case nodeBusy:
			e.Reason = "installing"
		default:
			e.Removable = true
		}
		reply.Node = append(reply.Node, e)
	}
	sort.SliceStable(reply.Node, func(i, j int) bool { return majorLess(reply.Node[i].Major, reply.Node[j].Major) })
	return reply
}

func majorLess(a, b string) bool {
	x, y := parseDotted(a), parseDotted(b)
	return compareDotted(x, y) < 0
}

// selectedMajor is the major a saved selection asks for: "22" for "22", "22.23" or
// "22.23.4"; "" for none or "system".
func selectedMajor(sel string) string {
	if v := parseDotted(sel); len(v) > 0 {
		return strconv.Itoa(v[0])
	}
	return ""
}

// independentJDK reports whether sys is a usable JDK that removing home leaves intact.
// Final targets are not enough (a link can pass through home and come back), so sys
// must be a real directory with no symlink on the path above it, outside home, and
// self-contained by the same rule the Node
// prune uses: only relative file links that never climb above the tree, no directory
// links. Temurin 21/25 trees pass (in-tree file links only); a tree that does not is no
// fallback, which only means its home copy cannot go while that major is selected.
func independentJDK(sys, home string) bool {
	if sys == "" {
		return false
	}
	if fi, err := os.Lstat(sys); err != nil || !fi.IsDir() {
		return false
	}
	// No symlink above it either: javaHomeFor keeps looking it up through this path, so
	// a link in /usr/lib/jvm or higher that passes through home would break the lookup
	// once home is gone even though the tree itself survives. Deployments bake or bind-
	// mount /usr/lib/jvm as a real directory.
	parent := filepath.Dir(filepath.Clean(sys))
	if real, err := filepath.EvalSymlinks(parent); err != nil || real != parent {
		return false
	}
	realHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		realHome = filepath.Clean(home)
	}
	realSys, err := filepath.EvalSymlinks(sys)
	if err != nil || realSys == realHome || strings.HasPrefix(realSys, realHome+string(filepath.Separator)) {
		return false
	}
	if !selfContained(sys) {
		return false
	}
	fi, err := os.Stat(filepath.Join(sys, "bin", "java"))
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}

// rootBusy reports whether an install into root is running: the root's flock is held
// (any process, the CLI included) or the Agent's own job is past its start. The listing
// and the DELETE handlers share it, so a row never offers what the DELETE would refuse.
func rootBusy(root string, inst interface {
	snapshot() (string, string, string)
}) bool {
	if state, _, _ := inst.snapshot(); state == "installing" {
		return true
	}
	unlock, free, err := installLock(root, false)
	if err != nil || !free {
		return true
	}
	unlock()
	return false
}

// installLock takes the root's install flock, shared by install-jdk / install-node in
// any process. With wait false it reports ok=false instead of waiting.
func installLock(root string, wait bool) (unlock func(), ok bool, err error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, false, err
	}
	f, err := os.OpenFile(filepath.Join(root, ".agent-fleet-install.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, false, err
	}
	how := syscall.LOCK_EX
	if !wait {
		how |= syscall.LOCK_NB
	}
	if err := syscall.Flock(int(f.Fd()), how); err != nil {
		_ = f.Close()
		if !wait && err == syscall.EWOULDBLOCK {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("flock: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, true, nil
}

// trashPath returns root's trash directory, creating it when asked, but only as a real
// directory: a symlink there (or anything else) is refused, so neither a sweep nor a
// staged rename can reach outside root.
func trashPath(root string, create bool) (string, bool) {
	dir := filepath.Join(root, trashDir)
	fi, err := os.Lstat(dir)
	if os.IsNotExist(err) && create {
		if err := os.Mkdir(dir, 0o755); err != nil && !os.IsExist(err) {
			return "", false
		}
		fi, err = os.Lstat(dir)
	}
	if err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return "", false
	}
	return dir, true
}

// sweepTrash deletes what an interrupted removal left in root's trash directory. The
// directory itself stays: removing it could pull it from under a concurrent staging.
func sweepTrash(root string) {
	dir, ok := trashPath(root, false)
	if !ok {
		return
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		_ = removeTree(filepath.Join(dir, e.Name()))
	}
}

// stageRemoval moves dir into its root's trash directory under a unique name, so
// discovery stops seeing it at once, and returns the new path.
func stageRemoval(dir string) (string, error) {
	trash, ok := trashPath(filepath.Dir(dir), true)
	if !ok {
		return "", fmt.Errorf("%s is not a plain directory", filepath.Join(filepath.Dir(dir), trashDir))
	}
	var rnd [6]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", err
	}
	staged := filepath.Join(trash, filepath.Base(dir)+"-"+hex.EncodeToString(rnd[:]))
	if err := os.Rename(dir, staged); err != nil {
		return "", err
	}
	return staged, nil
}

func handleToolchainInstalls(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, listToolchainInstalls())
}

// handleJDKUninstall is DELETE /env/jdk/{major}: removes this architecture's home-volume
// tree of that major.
func handleJDKUninstall(w http.ResponseWriter, r *http.Request) {
	major := r.PathValue("major")
	if !majorOnlyRe.MatchString(major) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_major", "invalid JDK major version")
		return
	}
	toolchainsMu.Lock()
	jdkInstaller.mu.Lock()
	home := homeJDKFor(major)
	var staged string
	var code, msg string
	unlock, free, lerr := installLock(jvmHomeRoot(), false)
	switch {
	case lerr != nil:
		code, msg = "remove_failed", lerr.Error()
	case home == "":
		code, msg = "not_installed", "Temurin "+major+" is not installed in the home volume"
	case selectedMajor(readToolchains().Java) == major && !independentJDK(systemJDKFor(major), home):
		code, msg = "selected", "Temurin "+major+" is the selected JDK; select another one first"
	case !free || jdkInstaller.state == "installing":
		code, msg = "installing", "a JDK is being installed; try again when it has finished"
	default:
		var err error
		if staged, err = stageRemoval(home); err != nil {
			code, msg = "remove_failed", err.Error()
		}
	}
	if unlock != nil {
		unlock()
	}
	jdkInstaller.mu.Unlock()
	toolchainsMu.Unlock()
	if code != "" {
		httpx.WriteErr(w, uninstallStatus(code), code, msg)
		return
	}
	if err := removeTree(staged); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "remove_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, listToolchainInstalls())
}

// handleNodeUninstall is DELETE /env/node/{major}: removes every home-volume tree of
// that major, the ones nvm installed included.
func handleNodeUninstall(w http.ResponseWriter, r *http.Request) {
	major := r.PathValue("major")
	if !majorOnlyRe.MatchString(major) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_major", "invalid node major version")
		return
	}
	toolchainsMu.Lock()
	nodeInstaller.mu.Lock()
	trees := homeNodeTrees(major)
	var staged []string
	var code, msg string
	unlock, free, lerr := installLock(nvmNodeRoot(), false)
	switch {
	case lerr != nil:
		code, msg = "remove_failed", lerr.Error()
	case len(trees) == 0:
		code, msg = "not_installed", "node "+major+" is not installed"
	case selectedMajor(readToolchains().Node) == major:
		code, msg = "selected", "node "+major+" is the selected version; select another one first"
	case !free || nodeInstaller.state == "installing":
		code, msg = "installing", "a Node.js version is being installed; try again when it has finished"
	default:
		for _, t := range trees {
			s, err := stageRemoval(t)
			if err != nil {
				code, msg = "remove_failed", err.Error()
				break
			}
			staged = append(staged, s)
		}
	}
	if unlock != nil {
		unlock()
	}
	nodeInstaller.mu.Unlock()
	toolchainsMu.Unlock()
	// Delete whatever was staged even when a later rename failed: those trees are
	// already invisible.
	for _, s := range staged {
		if err := removeTree(s); err != nil && code == "" {
			code, msg = "remove_failed", err.Error()
		}
	}
	if code != "" {
		httpx.WriteErr(w, uninstallStatus(code), code, msg)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, listToolchainInstalls())
}

func uninstallStatus(code string) int {
	switch code {
	case "not_installed":
		return http.StatusNotFound
	case "selected", "installing":
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}
