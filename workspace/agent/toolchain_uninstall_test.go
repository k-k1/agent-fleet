package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// selectToolchains writes the member's selection into the (isolated) HOME.
func selectToolchains(t *testing.T, java, node string) {
	t.Helper()
	p := toolchainsPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]string{"java": java, "node": node})
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func uninstallReq(t *testing.T, h http.HandlerFunc, kind, major string) (int, string) {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/env/"+kind+"/"+major, nil)
	r.SetPathValue("major", major)
	h(w, r)
	return w.Code, w.Body.String()
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestListToolchainInstalls(t *testing.T) {
	sys, home := jdkRoots(t)
	arch := "-" + runtime.GOARCH
	fakeJDK(t, sys, "temurin-21-jdk"+arch, "21.0.11+10")
	fakeJDK(t, home, "temurin-21-jdk"+arch, "21.0.13+9")
	fakeJDK(t, home, "temurin-17-jdk"+arch, "17.0.21+9")
	fakeNode(t, "v22.23.4", true)
	fakeNode(t, "v24.1.0", false)
	selectToolchains(t, "21", "22")

	got := listToolchainInstalls()
	byKey := map[string]toolchainInstall{}
	for _, e := range got.Java {
		byKey["java"+e.Major+e.Source] = e
	}
	for _, e := range got.Node {
		byKey["node"+e.Major] = e
	}
	// Selected 21 with a /usr/lib/jvm copy: the home copy (in use, newer) may still go.
	if e := byKey["java21home"]; !e.InUse || !e.Removable || e.Version != "21.0.13+9" {
		t.Errorf("java 21 home: %+v", e)
	}
	if e := byKey["java21system"]; e.Removable || e.InUse || e.Version != "21.0.11+10" {
		t.Errorf("java 21 system: %+v", e)
	}
	if e := byKey["java17home"]; !e.Removable || e.InUse {
		t.Errorf("java 17 home: %+v", e)
	}
	if e := byKey["node22"]; e.Removable || e.Reason != "selected" || e.Version != "v22.23.4" {
		t.Errorf("node 22: %+v", e)
	}
	if e := byKey["node24"]; !e.Removable {
		t.Errorf("node 24: %+v", e)
	}
}

func TestJDKUninstallGuards(t *testing.T) {
	sys, home := jdkRoots(t)
	arch := "-" + runtime.GOARCH
	only17 := fakeJDK(t, home, "temurin-17-jdk"+arch, "17.0.21+9")
	sysOnly := fakeJDK(t, sys, "temurin-8-jdk"+arch, "8.0.504+1")
	selectToolchains(t, "17", "")

	// Selected with no other copy: refused, tree kept.
	if code, body := uninstallReq(t, handleJDKUninstall, "jdk", "17"); code != http.StatusConflict || !strings.Contains(body, "selected") {
		t.Fatalf("selected: %d %s", code, body)
	}
	if !exists(only17) {
		t.Fatal("selected JDK was removed")
	}
	// Only in /usr/lib/jvm: nothing in home to remove, and /usr/lib/jvm is untouched.
	if code, _ := uninstallReq(t, handleJDKUninstall, "jdk", "8"); code != http.StatusNotFound {
		t.Fatalf("system-only: want 404 got %d", code)
	}
	if !exists(sysOnly) {
		t.Fatal("a /usr/lib/jvm tree was removed")
	}
	if code, _ := uninstallReq(t, handleJDKUninstall, "jdk", "..%2F"); code != http.StatusBadRequest {
		t.Fatalf("bad major: want 400 got %d", code)
	}

	// Installing that major: refused.
	selectToolchains(t, "", "")
	jdkInstaller.mu.Lock()
	jdkInstaller.state, jdkInstaller.major = "installing", "17"
	jdkInstaller.mu.Unlock()
	t.Cleanup(func() {
		jdkInstaller.mu.Lock()
		jdkInstaller.state, jdkInstaller.major, jdkInstaller.err = "", "", ""
		jdkInstaller.mu.Unlock()
	})
	if code, body := uninstallReq(t, handleJDKUninstall, "jdk", "17"); code != http.StatusConflict || !strings.Contains(body, "installing") {
		t.Fatalf("installing: %d %s", code, body)
	}
	jdkInstaller.mu.Lock()
	jdkInstaller.state = ""
	jdkInstaller.mu.Unlock()

	// Not selected: removed, and nothing hidden is left behind.
	if code, body := uninstallReq(t, handleJDKUninstall, "jdk", "17"); code != http.StatusOK {
		t.Fatalf("remove: %d %s", code, body)
	}
	if exists(only17) {
		t.Fatal("JDK 17 still there")
	}
	if ents, _ := os.ReadDir(home); len(ents) != 0 {
		t.Fatalf("left in home: %v", ents)
	}
}

// Selected but also in /usr/lib/jvm: the home copy may go, resolution falls back.
func TestJDKUninstallSelectedWithSystemFallback(t *testing.T) {
	sys, home := jdkRoots(t)
	arch := "-" + runtime.GOARCH
	sysCopy := fakeJDK(t, sys, "temurin-21-jdk"+arch, "21.0.11+10")
	homeCopy := fakeJDK(t, home, "temurin-21-jdk"+arch, "21.0.13+9")
	selectToolchains(t, "21", "")
	if code, body := uninstallReq(t, handleJDKUninstall, "jdk", "21"); code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	if exists(homeCopy) || !exists(sysCopy) {
		t.Fatal("want the home copy gone and /usr/lib/jvm kept")
	}
	if got := javaHomeFor("21"); got != sysCopy {
		t.Fatalf("javaHomeFor = %q, want the fallback %q", got, sysCopy)
	}
}

func TestNodeUninstall(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fakeNode(t, "v22.23.4", true)
	fakeNode(t, "v24.1.0", true)
	fakeNode(t, "v24.20.0", false) // nvm's: still part of the major the member removes
	selectToolchains(t, "", "22")

	if code, body := uninstallReq(t, handleNodeUninstall, "node", "22"); code != http.StatusConflict || !strings.Contains(body, "selected") {
		t.Fatalf("selected: %d %s", code, body)
	}
	if code, _ := uninstallReq(t, handleNodeUninstall, "node", "20"); code != http.StatusNotFound {
		t.Fatalf("absent: want 404 got %d", code)
	}
	if code, body := uninstallReq(t, handleNodeUninstall, "node", "24"); code != http.StatusOK {
		t.Fatalf("remove: %d %s", code, body)
	}
	if got := strings.Join(nodeDirs(t), ","); got != "v22.23.4" {
		t.Fatalf("left %s", got)
	}
}

func TestNodeUninstallRefusedWhileInstalling(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fakeNode(t, "v24.1.0", true)
	nodeInstaller.mu.Lock()
	nodeInstaller.state, nodeInstaller.major = "installing", "24"
	nodeInstaller.mu.Unlock()
	t.Cleanup(func() {
		nodeInstaller.mu.Lock()
		nodeInstaller.state, nodeInstaller.major, nodeInstaller.err = "", "", ""
		nodeInstaller.mu.Unlock()
	})
	if code, _ := uninstallReq(t, handleNodeUninstall, "node", "24"); code != http.StatusConflict {
		t.Fatalf("want 409 got %d", code)
	}
	if got := strings.Join(nodeDirs(t), ","); got != "v24.1.0" {
		t.Fatalf("left %s", got)
	}
}

// A ".removing-*" leftover from an interrupted delete is invisible to discovery and is
// swept by the next listing.
func TestRemovingLeftoversAreSwept(t *testing.T) {
	_, home := jdkRoots(t)
	left := filepath.Join(home, removingPrefix+"temurin-11-jdk-x")
	if err := os.MkdirAll(filepath.Join(left, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if majors := installedJavaMajors(); len(majors) != 0 {
		t.Fatalf("a staged tree counts as installed: %v", majors)
	}
	listToolchainInstalls()
	if exists(left) {
		t.Fatal("leftover not swept")
	}
}
