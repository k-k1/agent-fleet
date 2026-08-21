package main

import (
	"path/filepath"
	"runtime"
	"sort"
	"testing"
)

// otherArch is the architecture token this container is NOT, so the tests read the
// same on an amd64 CI runner and an arm64 one.
func otherArch() string {
	if runtime.GOARCH == "amd64" {
		return "arm64"
	}
	return "amd64"
}

func TestForeignArchJDK(t *testing.T) {
	if foreignArchJDK("temurin-21-jdk-" + runtime.GOARCH) {
		t.Fatalf("our own arch must not be foreign")
	}
	if !foreignArchJDK("temurin-21-jdk-" + otherArch()) {
		t.Fatalf("%s must be foreign on %s", otherArch(), runtime.GOARCH)
	}
	// No suffix at all: not foreign — a hand-placed tree stays usable.
	if foreignArchJDK("temurin-21-jdk") {
		t.Fatalf("an unsuffixed dir must not be treated as foreign")
	}
}

// The regression this whole change exists for: "amd64" sorts before "arm64", so the
// old glob+sort+[0] returned the x86 tree on an arm64 workspace whose home had been
// filled on x86 (docs/70 §70.5.1).
func TestPickArchJDKPrefersOurArchitecture(t *testing.T) {
	matches := []string{
		filepath.Join("/jvm", "temurin-21-jdk-amd64"),
		filepath.Join("/jvm", "temurin-21-jdk-arm64"),
	}
	sort.Strings(matches)
	want := filepath.Join("/jvm", "temurin-21-jdk-"+runtime.GOARCH)
	if got := pickArchJDK(matches); got != want {
		t.Fatalf("pickArchJDK = %q, want %q", got, want)
	}
}

func TestPickArchJDKRejectsForeignOnly(t *testing.T) {
	matches := []string{filepath.Join("/jvm", "temurin-21-jdk-"+otherArch())}
	if got := pickArchJDK(matches); got != "" {
		t.Fatalf("pickArchJDK = %q, want \"\" (a foreign tree is not a JAVA_HOME)", got)
	}
}

func TestPickArchJDKFallsBackToUnsuffixed(t *testing.T) {
	neutral := filepath.Join("/jvm", "temurin-21-jdk")
	matches := []string{filepath.Join("/jvm", "temurin-21-jdk-"+otherArch()), neutral}
	sort.Strings(matches)
	if got := pickArchJDK(matches); got != neutral {
		t.Fatalf("pickArchJDK = %q, want %q", got, neutral)
	}
}
