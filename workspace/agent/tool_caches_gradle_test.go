package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func wrapperProps(t *testing.T, project, dist string) {
	t.Helper()
	body := "distributionBase=GRADLE_USER_HOME\ndistributionUrl=https\\://services.gradle.org/distributions/" + dist + "\n"
	p := filepath.Join(project, "gradle", "wrapper", "gradle-wrapper.properties")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// gradleHome lays out ~/.gradle with versions 8.1.1 (pinned), 7.6 and 8.14.3 (not pinned).
func gradleHome(t *testing.T, home string) string {
	t.Helper()
	g := filepath.Join(home, ".gradle")
	for _, v := range []string{"8.1.1", "7.6", "8.14.3"} {
		writeSized(t, filepath.Join(g, "caches", v, "x"), 100)
		writeSized(t, filepath.Join(g, "wrapper", "dists", "gradle-"+v+"-bin", "h", "dist"), 1000)
	}
	writeSized(t, filepath.Join(g, "caches", "modules-2", "m"), 5000)
	writeSized(t, filepath.Join(g, "caches", "jars-9", "j"), 50)
	writeSized(t, filepath.Join(g, "gradle.properties"), 10)
	writeSized(t, filepath.Join(g, "jdks", "jdk-17", "bin"), 7000)
	wrapperProps(t, filepath.Join(home, "repos", "app"), "gradle-8.1.1-bin.zip")
	return g
}

func deleteRow(t *testing.T, name string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("DELETE", "/cleanup/tool-caches/"+name, nil)
	req.SetPathValue("name", name)
	rec := httptest.NewRecorder()
	handleDeleteToolCache(rec, req)
	return rec
}

func pathExists(p string) bool { _, err := os.Lstat(p); return err == nil }

func TestGradleRowsRemoveOnlyUnpinnedVersions(t *testing.T) {
	home, _ := isolateToolCaches(t)
	g := gradleHome(t, home)

	if rec := deleteRow(t, "gradle-caches"); rec.Code != 200 {
		t.Fatalf("caches: %d %s", rec.Code, rec.Body)
	}
	if rec := deleteRow(t, "gradle-wrapper"); rec.Code != 200 {
		t.Fatalf("wrapper: %d %s", rec.Code, rec.Body)
	}
	for _, gone := range []string{"caches/7.6", "caches/8.14.3", "wrapper/dists/gradle-7.6-bin", "wrapper/dists/gradle-8.14.3-bin"} {
		if pathExists(filepath.Join(g, gone)) {
			t.Errorf("%s survived", gone)
		}
	}
	for _, kept := range []string{
		"caches/8.1.1/x", "wrapper/dists/gradle-8.1.1-bin/h/dist", "caches/modules-2/m", "caches/jars-9/j",
		"gradle.properties", "jdks/jdk-17/bin", "caches", "wrapper/dists",
	} {
		if !pathExists(filepath.Join(g, kept)) {
			t.Errorf("%s was removed", kept)
		}
	}
}

func TestGradleSurveyCountsOnlyWhatWouldGo(t *testing.T) {
	home, _ := isolateToolCaches(t)
	gradleHome(t, home)
	rec := httptest.NewRecorder()
	handleToolCacheUsage(rec, httptest.NewRequest("GET", "/cleanup/tool-caches", nil))
	var u toolCacheUsage
	if err := json.Unmarshal(rec.Body.Bytes(), &u); err != nil {
		t.Fatal(err)
	}
	got := map[string]toolCacheRow{}
	for _, r := range u.Caches {
		got[r.Name] = r
	}
	if r := got["gradle-caches"]; r.Bytes != 200 || r.Files != 2 || r.Skipped != "" {
		t.Errorf("gradle-caches = %+v, want 200 B in 2 files (7.6 + 8.14.3)", r)
	}
	if r := got["gradle-wrapper"]; r.Bytes != 2000 || r.Files != 2 {
		t.Errorf("gradle-wrapper = %+v, want 2000 B in 2 files", r)
	}
}

func TestGradleFailsClosedWhenPinsAreUnknown(t *testing.T) {
	cases := map[string]func(t *testing.T, home string){
		"no distributionUrl": func(t *testing.T, home string) {
			writeSized(t, filepath.Join(home, "repos", "odd", "gradle", "wrapper", "gradle-wrapper.properties"), 0)
		},
		"unparseable version": func(t *testing.T, home string) {
			wrapperProps(t, filepath.Join(home, "repos", "odd"), "custom-dist.zip")
		},
		"unreadable directory": func(t *testing.T, home string) {
			d := filepath.Join(home, "repos", "locked")
			writeSized(t, filepath.Join(d, "f"), 1)
			if err := os.Chmod(d, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(d, 0o755) })
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			if name == "unreadable directory" && os.Geteuid() == 0 {
				t.Skip("root reads everything")
			}
			home, _ := isolateToolCaches(t)
			g := gradleHome(t, home)
			setup(t, home)
			rec := deleteRow(t, "gradle-caches")
			if rec.Code != 409 {
				t.Fatalf("status %d, want 409 pins_unknown: %s", rec.Code, rec.Body)
			}
			if !pathExists(filepath.Join(g, "caches", "7.6")) {
				t.Fatal("a version was removed although the pins were unknown")
			}
			r := httptest.NewRecorder()
			handleToolCacheUsage(r, httptest.NewRequest("GET", "/cleanup/tool-caches", nil))
			var u toolCacheUsage
			_ = json.Unmarshal(r.Body.Bytes(), &u)
			for _, row := range u.Caches {
				if row.Name == "gradle-caches" && (row.Skipped == "" || row.Bytes != 0) {
					t.Fatalf("row = %+v, want skipped with 0 bytes", row)
				}
			}
		})
	}
}

func TestGradlePinsComeFromWorktreesToo(t *testing.T) {
	home, _ := isolateToolCaches(t)
	g := gradleHome(t, home)
	wrapperProps(t, filepath.Join(home, "repos", "app@feature-x", "android"), "gradle-8.14.3-all.zip")
	if rec := deleteRow(t, "gradle-caches"); rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	if !pathExists(filepath.Join(g, "caches", "8.14.3", "x")) {
		t.Fatal("a version pinned in a nested worktree project was removed")
	}
	if pathExists(filepath.Join(g, "caches", "7.6")) {
		t.Fatal("unpinned 7.6 survived")
	}
}

func TestGradleNeverFollowsOrRemovesLinks(t *testing.T) {
	home, _ := isolateToolCaches(t)
	g := gradleHome(t, home)
	outside := t.TempDir()
	writeSized(t, filepath.Join(outside, "precious"), 10)
	if err := os.Symlink(outside, filepath.Join(g, "caches", "6.0")); err != nil {
		t.Fatal(err)
	}
	if rec := deleteRow(t, "gradle-caches"); rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	if !pathExists(filepath.Join(outside, "precious")) {
		t.Fatal("a link target was emptied")
	}
	if !pathExists(filepath.Join(g, "caches", "6.0")) {
		t.Fatal("the link itself was removed")
	}

	// The caches directory itself being a link is refused outright.
	home2, _ := isolateToolCaches(t)
	real := t.TempDir()
	writeSized(t, filepath.Join(real, "7.6", "x"), 10)
	if err := os.MkdirAll(filepath.Join(home2, ".gradle"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(home2, ".gradle", "caches")); err != nil {
		t.Fatal(err)
	}
	wrapperProps(t, filepath.Join(home2, "repos", "app"), "gradle-8.1.1-bin.zip")
	if rec := deleteRow(t, "gradle-caches"); rec.Code != 409 {
		t.Fatalf("status %d, want 409 cache_is_symlink", rec.Code)
	}
	if !pathExists(filepath.Join(real, "7.6", "x")) {
		t.Fatal("emptied through a linked caches directory")
	}
}

func TestGradleRefusesWhileAGradleRuns(t *testing.T) {
	home, proc := isolateToolCaches(t)
	g := gradleHome(t, home)
	fakeProc(t, proc, "900", "/usr/lib/jvm/java-17/bin/java", "-Xmx512m", "org.gradle.launcher.daemon.bootstrap.GradleDaemon", "8.14.3")
	for _, n := range []string{"gradle-caches", "gradle-wrapper"} {
		if rec := deleteRow(t, n); rec.Code != 409 {
			t.Fatalf("%s: status %d, want 409 cache_in_use", n, rec.Code)
		}
	}
	if !pathExists(filepath.Join(g, "caches", "7.6", "x")) {
		t.Fatal("removed under a running daemon")
	}
}

func TestArgvRunsGradle(t *testing.T) {
	cases := []struct {
		argv []string
		want bool
	}{
		{[]string{"/opt/gradle/bin/gradle", "build"}, true},
		{[]string{"sh", "./gradlew", "test"}, true},
		{[]string{"java", "-cp", "x", "org.gradle.wrapper.GradleWrapperMain", "build"}, true},
		{[]string{"java", "-Dorg.gradle.appname=gradlew", "-classpath", "x", "org.gradle.wrapper.GradleWrapperMain"}, true},
		{[]string{"java", "-jar", "app.jar"}, false},
		{[]string{"bash", "build.sh", "gradlew-notes"}, false},
		{[]string{"tail", "-f", "org.gradle.wrapper.GradleWrapperMain"}, false},
	}
	for _, tc := range cases {
		if got := argvRunsGradle(tc.argv); got != tc.want {
			t.Errorf("argvRunsGradle(%q) = %v, want %v", tc.argv, got, tc.want)
		}
	}
}

func TestHomeRootsDoNotCallPartialGradleFoldersReclaimable(t *testing.T) {
	home, _ := isolateLeftovers(t)
	for _, c := range toolCaches {
		if c.env != "" {
			t.Setenv(c.env, "")
		}
	}
	r := homeRoots(home)
	for _, sub := range []string{"caches", "wrapper/dists"} {
		if _, ok := r[filepath.Join(home, ".gradle", sub)]; ok {
			t.Errorf("~/.gradle/%s is a reclaimable root although only unpinned versions go", sub)
		}
	}
}
