package writeevidence

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestTraceAttributesWritesAcrossForkAndExec(t *testing.T) {
	trace := traceExec(10, "/bin/sh") +
		traceWrite(11, "/repo/child.txt") +
		"10 clone(flags=SIGCHLD) = 11\n" +
		traceWrite(10, "/repo/before-git.txt") +
		traceExec(10, "/usr/bin/git") +
		traceExec(12, "/bin/sh") +
		traceWrite(12, "/repo/git-child.txt") +
		"10 clone(flags=SIGCHLD) = 12\n" +
		traceWrite(10, "/repo/git.txt") +
		traceWrite(11, "/repo/child.txt") +
		traceWrite(11, "/repo/.git/index") +
		traceWrite(11, "/repo-other/outside.txt")
	got, err := parseTrace("/repo", []byte(trace))
	if err != nil || !slices.Equal(got, []string{"before-git.txt", "child.txt"}) {
		t.Fatalf("paths = %q, %v", got, err)
	}
}

func TestTraceRejectsIncompleteAttribution(t *testing.T) {
	start := traceExec(10, "/bin/sh") + traceWrite(10, "/repo/written.txt")
	for _, tc := range []struct {
		name, trace string
	}{
		{"empty", ""},
		{"truncated", strings.TrimSuffix(start, "\n")},
		{"orphan", start + traceWrite(11, "/repo/unknown.txt")},
		{"reused pid", start + "10 clone(flags=SIGCHLD) = 11\n10 clone(flags=SIGCHLD) = 11\n"},
		{"namespace", start + "10 clone(flags=CLONE_NEWPID|SIGCHLD) = 11\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			paths, err := parseTrace("/repo", []byte(tc.trace))
			if !errors.Is(err, errTrace) || len(paths) != 0 {
				t.Fatalf("paths = %q, %v; want no evidence", paths, err)
			}
		})
	}
}

func TestTracePathLimit(t *testing.T) {
	var trace strings.Builder
	trace.WriteString(traceExec(10, "/bin/sh"))
	for i := range maxPaths + 1 {
		trace.WriteString(traceWrite(10, fmt.Sprintf("/repo/path-%d", i)))
	}
	if paths, err := parseTrace("/repo", []byte(trace.String())); !errors.Is(err, ErrLimit) || len(paths) != 0 {
		t.Fatalf("paths = %q, %v; want no paths and ErrLimit", paths, err)
	}
}

func traceExec(pid int, path string) string {
	return fmt.Sprintf("%d execve(\"%s\", [...], 0x1) = 0\n", pid, traceHex(path))
}

func traceWrite(pid int, path string) string {
	return fmt.Sprintf("%d write(3<%s>, \"\"..., 1) = 1\n", pid, traceHex(path))
}

func traceHex(path string) string {
	var escaped strings.Builder
	for _, b := range []byte(path) {
		fmt.Fprintf(&escaped, `\x%02x`, b)
	}
	return escaped.String()
}
