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
// A removal first renames the trees to hidden ".removing-*" names under the installer's
// lock, then deletes them outside it. The rename is what makes the check and the
// removal one step against a concurrent install; the delete takes seconds and must not
// hold the lock the status poll uses. Neither discovery path matches a hidden name, and
// leftovers from an interrupted delete are swept by the next listing.

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

const removingPrefix = ".removing-"

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

func installing(inst interface {
	snapshot() (string, string, string)
}, major string) bool {
	state, m, _ := inst.snapshot()
	return state == "installing" && m == major
}

func listToolchainInstalls() toolchainInstallsReply {
	sweepRemoving(jvmHomeRoot())
	sweepRemoving(nvmNodeRoot())
	sel := readToolchains()
	reply := toolchainInstallsReply{Java: []toolchainInstall{}, Node: []toolchainInstall{}}

	for _, major := range installedJavaMajors() {
		resolved := javaHomeFor(major)
		sys := systemJDKFor(major)
		if home := homeJDKFor(major); home != "" {
			e := toolchainInstall{Major: major, Version: jdkVersion(home), Source: "home",
				Bytes: treeBytes(home), InUse: sel.Java == major && resolved == home}
			switch {
			case sel.Java == major && sys == "":
				e.Reason = "selected"
			case installing(&jdkInstaller, major):
				e.Reason = "installing"
			default:
				e.Removable = true
			}
			reply.Java = append(reply.Java, e)
		}
		if sys != "" {
			reply.Java = append(reply.Java, toolchainInstall{Major: major, Version: jdkVersion(sys),
				Source: "system", InUse: sel.Java == major && resolved == sys})
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
		e := toolchainInstall{Major: major, Source: "home", Bytes: bytes, InUse: sel.Node == major}
		if bin := nodeBinFor(major); bin != "" {
			e.Version = filepath.Base(filepath.Dir(bin))
		}
		switch {
		case sel.Node == major:
			e.Reason = "selected"
		case installing(&nodeInstaller, major):
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

// sweepRemoving deletes leftovers of an interrupted removal.
func sweepRemoving(root string) {
	ents, _ := os.ReadDir(root)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), removingPrefix) {
			_ = removeTree(filepath.Join(root, e.Name()))
		}
	}
}

// stageRemoval renames dir to a hidden name in the same root, so discovery stops seeing
// it at once, and returns the new path.
func stageRemoval(dir string) (string, error) {
	staged, err := os.MkdirTemp(filepath.Dir(dir), removingPrefix+filepath.Base(dir)+"-")
	if err != nil {
		return "", err
	}
	// MkdirTemp only reserved the name; the rename needs it gone.
	if err := os.Remove(staged); err != nil {
		return "", err
	}
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
	jdkInstaller.mu.Lock()
	home := homeJDKFor(major)
	var staged string
	var code, msg string
	switch {
	case home == "":
		code, msg = "not_installed", "Temurin "+major+" is not installed in the home volume"
	case readToolchains().Java == major && systemJDKFor(major) == "":
		code, msg = "selected", "Temurin "+major+" is the selected JDK; select another one first"
	case jdkInstaller.state == "installing" && jdkInstaller.major == major:
		code, msg = "installing", "Temurin "+major+" is being installed"
	default:
		var err error
		if staged, err = stageRemoval(home); err != nil {
			code, msg = "remove_failed", err.Error()
		}
	}
	jdkInstaller.mu.Unlock()
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
	nodeInstaller.mu.Lock()
	trees := homeNodeTrees(major)
	var staged []string
	var code, msg string
	switch {
	case len(trees) == 0:
		code, msg = "not_installed", "node "+major+" is not installed"
	case readToolchains().Node == major:
		code, msg = "selected", "node "+major+" is the selected version; select another one first"
	case nodeInstaller.state == "installing" && nodeInstaller.major == major:
		code, msg = "installing", "node "+major+" is being installed"
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
	nodeInstaller.mu.Unlock()
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
