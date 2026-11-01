package opencode

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Only opencode 2 is supported (ADR 0127). The major is read from the installed package, never by
// running the binary: the launch path is hot, and `opencode --version` costs a process start.

// buildPinsPath is the image's pin list (workspace/Dockerfile writes it from its ARGs). It is
// only the fallback for a binary whose package.json cannot be found; a var so tests can swap it.
var buildPinsPath = "/usr/local/share/agent-fleet/versions.json"

// opencodeBinary resolves the opencode on PATH; a var so tests can point it at a fake tree.
var opencodeBinary = func() string {
	p, err := exec.LookPath("opencode")
	if err != nil {
		return ""
	}
	return p
}

// installedMajor is the major of the opencode on PATH: 2 for @opencode/cli, 1 for opencode-ai, 0
// when unknown. Both packages put the executable at <pkg>/bin/opencode[.exe] and the manifest
// beside bin/, so the version comes from the resolved binary's package.json. A binary whose
// manifest cannot be found (a copied or wrapped executable) falls back to the image pin.
func installedMajor() int {
	if bin := opencodeBinary(); bin != "" {
		if real, err := filepath.EvalSymlinks(bin); err == nil {
			if m := majorOf(readPackageVersion(filepath.Join(filepath.Dir(real), "..", "package.json"))); m != 0 {
				return m
			}
		}
	}
	if b, err := os.ReadFile(buildPinsPath); err == nil {
		var pins struct {
			Opencode string `json:"opencode"`
		}
		if json.Unmarshal(b, &pins) == nil {
			return majorOf(pins.Opencode)
		}
	}
	return 0
}

// checkSupported refuses an opencode whose major is known to be older than 2. An unknown major
// is let through: the launch then fails or works on its own, and a wrapped binary of the
// supported major must not be refused for hiding its manifest.
func checkSupported() error {
	if m := installedMajor(); m != 0 && m < 2 {
		v := installedVersion()
		if v == "" {
			v = strconv.Itoa(m) + ".x"
		}
		return fmt.Errorf("opencode %s には対応していません。Agent Fleet が動かせるのは opencode 2（@opencode/cli）だけです", v)
	}
	return nil
}

// installedVersion is the full version of the opencode on PATH ("" when unknown).
func installedVersion() string {
	if bin := opencodeBinary(); bin != "" {
		if real, err := filepath.EvalSymlinks(bin); err == nil {
			return readPackageVersion(filepath.Join(filepath.Dir(real), "..", "package.json"))
		}
	}
	return ""
}

func readPackageVersion(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var pkg struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if json.Unmarshal(b, &pkg) != nil || (pkg.Name != "opencode-ai" && pkg.Name != "@opencode/cli") {
		return ""
	}
	return pkg.Version
}

// majorOf parses the leading integer of a semver string; 0 when there is none.
func majorOf(v string) int {
	head, _, _ := strings.Cut(strings.TrimSpace(v), ".")
	n, err := strconv.Atoi(head)
	if err != nil || n < 1 {
		return 0
	}
	return n
}

// SetBinaryForTest makes the major detection look at the opencode binary path (and ignore the
// image pin list) until the returned function is called. Tests of other packages use it to
// choose the major without installing anything.
func SetBinaryForTest(path string) (restore func()) {
	oldBin, oldPins := opencodeBinary, buildPinsPath
	opencodeBinary = func() string { return path }
	buildPinsPath = ""
	return func() { opencodeBinary, buildPinsPath = oldBin, oldPins }
}
