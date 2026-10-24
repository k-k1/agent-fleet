package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCompareJDKVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"21.0.13+9", "21.0.11+10", 1},
		{"21.0.11+10", "21.0.11+9", 1},
		{"21.0.11+10", "21.0.11+10", 0},
		{"21.0.13+9.0.LTS", "21.0.13+9", 0}, // Adoptium API semver vs a release file
		{"17.0.4.1+1", "17.0.4+8", 1},       // a fourth field outranks any build number
		{"8.0.492+9", "8.0.482+8", 1},
		{"21.0.2+13", "21.0.10+7", -1}, // numeric, not lexical
		{"", "21.0.11+10", -1},
	}
	for _, c := range cases {
		if got := compareJDKVersions(c.a, c.b); got != c.want {
			t.Errorf("compareJDKVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// fakeJDK creates <root>/<name> holding a release file with the given
// SEMANTIC_VERSION ("" = no release file at all).
func fakeJDK(t *testing.T, root, name, semver string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if semver != "" {
		body := "IMPLEMENTOR=\"Eclipse Adoptium\"\nJAVA_VERSION=\"x\"\nSEMANTIC_VERSION=\"" + semver + "\"\n"
		if err := os.WriteFile(filepath.Join(dir, "release"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// jdkRoots points both search dirs at temp trees and returns (system, home).
func jdkRoots(t *testing.T) (string, string) {
	t.Helper()
	sys := t.TempDir()
	prev := jvmSystemRoot
	jvmSystemRoot = sys
	t.Cleanup(func() { jvmSystemRoot = prev })
	t.Setenv("HOME", t.TempDir())
	home := jvmHomeRoot()
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	return sys, home
}

func TestJDKVersionReadsReleaseFile(t *testing.T) {
	root := t.TempDir()
	if got := jdkVersion(fakeJDK(t, root, "a", "21.0.11+10")); got != "21.0.11+10" {
		t.Fatalf("jdkVersion = %q", got)
	}
	if got := jdkVersion(fakeJDK(t, root, "b", "")); got != "" {
		t.Fatalf("jdkVersion without release = %q, want \"\"", got)
	}
}

// The bug this exists for: install-jdk put a newer 21 in the home volume and
// JAVA_HOME stayed on the older /usr/lib/jvm copy.
func TestJavaHomeForPrefersNewerHomeCopy(t *testing.T) {
	sys, home := jdkRoots(t)
	name := "temurin-21-jdk-" + runtime.GOARCH
	fakeJDK(t, sys, name, "21.0.11+10")
	want := fakeJDK(t, home, name, "21.0.13+9")
	if got := javaHomeFor("21"); got != want {
		t.Fatalf("javaHomeFor = %q, want the newer home copy %q", got, want)
	}
}

// An image update must not be shadowed by a stale home copy.
func TestJavaHomeForKeepsNewerSystemCopy(t *testing.T) {
	sys, home := jdkRoots(t)
	name := "temurin-21-jdk-" + runtime.GOARCH
	want := fakeJDK(t, sys, name, "21.0.15+3")
	fakeJDK(t, home, name, "21.0.13+9")
	if got := javaHomeFor("21"); got != want {
		t.Fatalf("javaHomeFor = %q, want %q", got, want)
	}
}

func TestJavaHomeForTieAndUnknownKeepPriorityOrder(t *testing.T) {
	for _, c := range []struct{ label, sysVer, homeVer string }{
		{"equal", "21.0.11+10", "21.0.11+10"},
		{"system unreadable", "", "21.0.13+9"},
		{"home unreadable", "21.0.11+10", ""},
	} {
		t.Run(c.label, func(t *testing.T) {
			sys, home := jdkRoots(t)
			name := "temurin-21-jdk-" + runtime.GOARCH
			want := fakeJDK(t, sys, name, c.sysVer)
			fakeJDK(t, home, name, c.homeVer)
			if got := javaHomeFor("21"); got != want {
				t.Fatalf("javaHomeFor = %q, want the /usr/lib/jvm copy %q", got, want)
			}
		})
	}
}

// A newer tree of another architecture is still never a JAVA_HOME.
func TestJavaHomeForIgnoresNewerForeignArch(t *testing.T) {
	sys, home := jdkRoots(t)
	want := fakeJDK(t, sys, "temurin-21-jdk-"+runtime.GOARCH, "21.0.11+10")
	fakeJDK(t, home, "temurin-21-jdk-"+otherArch(), "21.0.13+9")
	if got := javaHomeFor("21"); got != want {
		t.Fatalf("javaHomeFor = %q, want %q", got, want)
	}
}

func TestJavaHomeForHomeOnly(t *testing.T) {
	_, home := jdkRoots(t)
	want := fakeJDK(t, home, "temurin-17-jdk-"+runtime.GOARCH, "17.0.17+10")
	if got := javaHomeFor("17"); got != want {
		t.Fatalf("javaHomeFor = %q, want %q", got, want)
	}
	if got := javaHomeFor("11"); got != "" {
		t.Fatalf("javaHomeFor(absent) = %q, want \"\"", got)
	}
}
