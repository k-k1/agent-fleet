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
	"time"
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

// withJava gives a fake JDK an executable bin/java, which a fallback needs.
func withJava(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bin", "java"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// visible lists dir without dot entries (the install lock, the trash directory).
func visible(t *testing.T, dir string) []string {
	t.Helper()
	ents, _ := os.ReadDir(dir)
	var out []string
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	return out
}

func TestListToolchainInstalls(t *testing.T) {
	sys, home := jdkRoots(t)
	arch := "-" + runtime.GOARCH
	withJava(t, fakeJDK(t, sys, "temurin-21-jdk"+arch, "21.0.11+10"))
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
	if left := visible(t, home); len(left) != 0 {
		t.Fatalf("left in home: %v", left)
	}
	if exists(filepath.Join(home, trashDir)) {
		t.Fatal("trash directory left behind")
	}
}

// Selected but also in /usr/lib/jvm: the home copy may go, resolution falls back.
func TestJDKUninstallSelectedWithSystemFallback(t *testing.T) {
	sys, home := jdkRoots(t)
	arch := "-" + runtime.GOARCH
	sysCopy := withJava(t, fakeJDK(t, sys, "temurin-21-jdk"+arch, "21.0.11+10"))
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

// Only the trash directory is swept: a leftover there goes, a look-alike name elsewhere
// (the member's own file) stays, and a staged tree is invisible to discovery.
func TestSweepOnlyTouchesTrash(t *testing.T) {
	_, home := jdkRoots(t)
	left := filepath.Join(home, trashDir, "temurin-11-jdk-x-abc")
	if err := os.MkdirAll(filepath.Join(left, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	mine := filepath.Join(home, ".removing-backup")
	if err := os.MkdirAll(mine, 0o755); err != nil {
		t.Fatal(err)
	}
	if majors := installedJavaMajors(); len(majors) != 0 {
		t.Fatalf("a staged tree counts as installed: %v", majors)
	}
	listToolchainInstalls()
	if exists(left) {
		t.Fatal("trash leftover not swept")
	}
	if !exists(mine) {
		t.Fatal("a file outside the trash was swept")
	}
}

// An exact or partial version selection still protects its major.
func TestUninstallTreatsExactSelectionAsItsMajor(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fakeNode(t, "v22.23.4", true)
	fakeNode(t, "v24.1.0", true)
	selectToolchains(t, "", "22.23.4")
	for _, e := range listToolchainInstalls().Node {
		if e.Major == "22" && (e.Removable || e.Reason != "selected") {
			t.Fatalf("listing: %+v", e)
		}
	}
	if code, _ := uninstallReq(t, handleNodeUninstall, "node", "22"); code != http.StatusConflict {
		t.Fatalf("want 409 got %d", code)
	}
	if !strings.Contains(strings.Join(nodeDirs(t), ","), "v22.23.4") {
		t.Fatal("the selected patch was removed")
	}
}

// A /usr/lib/jvm entry that links to the home copy is no fallback.
func TestJDKUninstallRejectsSystemLinkToHome(t *testing.T) {
	sys, home := jdkRoots(t)
	arch := "-" + runtime.GOARCH
	homeCopy := fakeJDK(t, home, "temurin-21-jdk"+arch, "21.0.13+9")
	if err := os.MkdirAll(filepath.Join(homeCopy, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(homeCopy, "bin", "java"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(homeCopy, filepath.Join(sys, "temurin-21-jdk"+arch)); err != nil {
		t.Fatal(err)
	}
	selectToolchains(t, "21", "")
	if code, _ := uninstallReq(t, handleJDKUninstall, "jdk", "21"); code != http.StatusConflict {
		t.Fatalf("want 409 got %d", code)
	}
	if !exists(homeCopy) {
		t.Fatal("the only real copy of the selected JDK was removed")
	}
}

// An independent /usr/lib/jvm copy with a working bin/java is a fallback.
func TestIndependentJDK(t *testing.T) {
	sys, home := jdkRoots(t)
	arch := "-" + runtime.GOARCH
	h := fakeJDK(t, home, "temurin-21-jdk"+arch, "21.0.13+9")
	s := fakeJDK(t, sys, "temurin-21-jdk"+arch, "21.0.11+10")
	if independentJDK(s, h) {
		t.Fatal("a system copy without bin/java counted as a fallback")
	}
	if err := os.MkdirAll(filepath.Join(s, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s, "bin", "java"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if !independentJDK(s, h) {
		t.Fatal("an independent system copy did not count")
	}
}

// The selection PUT and uninstall share toolchainsMu: a selection written while an
// uninstall waits is seen by it.
func TestUninstallSeesSelectionWrittenUnderTheSameLock(t *testing.T) {
	_, home := jdkRoots(t)
	arch := "-" + runtime.GOARCH
	jdk := fakeJDK(t, home, "temurin-17-jdk"+arch, "17.0.21+9")
	selectToolchains(t, "", "")
	toolchainsMu.Lock()
	done := make(chan int)
	go func() {
		code, _ := uninstallReq(t, handleJDKUninstall, "jdk", "17")
		done <- code
	}()
	select {
	case code := <-done:
		toolchainsMu.Unlock()
		t.Fatalf("uninstall finished (%d) while the selection lock was held", code)
	case <-time.After(100 * time.Millisecond):
	}
	b, _ := json.Marshal(map[string]string{"java": "17"})
	if err := writeToolchainsAtomic(toolchainsPath(), b); err != nil {
		t.Fatal(err)
	}
	toolchainsMu.Unlock()
	if code := <-done; code != http.StatusConflict {
		t.Fatalf("want 409 got %d", code)
	}
	if !exists(jdk) {
		t.Fatal("JDK selected meanwhile was removed")
	}
}

// An install running in another process holds the root's flock: uninstall refuses.
func TestUninstallRefusedWhileAnotherProcessInstalls(t *testing.T) {
	_, home := jdkRoots(t)
	jdk := fakeJDK(t, home, "temurin-17-jdk-"+runtime.GOARCH, "17.0.21+9")
	fakeNode(t, "v24.1.0", true)
	selectToolchains(t, "", "")
	for _, c := range []struct {
		root string
		h    http.HandlerFunc
		kind string
		maj  string
	}{{jvmHomeRoot(), handleJDKUninstall, "jdk", "17"}, {nvmNodeRoot(), handleNodeUninstall, "node", "24"}} {
		unlock, ok, err := installLock(c.root, false)
		if err != nil || !ok {
			t.Fatalf("lock: %v %v", ok, err)
		}
		code, body := uninstallReq(t, c.h, c.kind, c.maj)
		unlock()
		if code != http.StatusConflict || !strings.Contains(body, "installing") {
			t.Fatalf("%s: %d %s", c.kind, code, body)
		}
	}
	if !exists(jdk) || !strings.Contains(strings.Join(nodeDirs(t), ","), "v24.1.0") {
		t.Fatal("removed while an install held the lock")
	}
}

func TestWriteToolchainsAtomicLeavesNoTemp(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	selectToolchains(t, "21", "22")
	if err := writeToolchainsAtomic(toolchainsPath(), []byte(`{"java":"17"}`)); err != nil {
		t.Fatal(err)
	}
	if readToolchains().Java != "17" {
		t.Fatal("not replaced")
	}
	ents, _ := os.ReadDir(filepath.Dir(toolchainsPath()))
	if len(ents) != 1 {
		t.Fatalf("left %v", ents)
	}
}
