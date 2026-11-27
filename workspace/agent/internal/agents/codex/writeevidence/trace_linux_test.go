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
		traceExec(11, "/tools/git-formatter") +
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

func TestTraceWriteSyscalls(t *testing.T) {
	const root = "/repo"
	file := `"` + traceHex(root+"/argument.txt") + `"`
	destination := "3<" + traceHex(root+"/written.txt") + ">"
	source := "4<" + traceHex(root+"/source.txt") + ">"
	for _, tc := range []struct {
		name, call string
		want       bool
	}{
		{"truncating open", "open(" + file + ", O_RDWR|O_TRUNC) = " + destination, true},
		{"exclusive openat", "openat(AT_FDCWD, " + file + ", O_WRONLY|O_CREAT|O_EXCL, 0600) = " + destination, true},
		{"plain create openat", "openat(AT_FDCWD, " + file + ", O_CREAT|O_WRONLY, 0600) = " + destination, false},
		{"read-only open", "open(" + file + ", O_RDONLY) = " + destination, false},
		{"creat", "creat(" + file + ", 0600) = " + destination, true},
		{"ftruncate", "ftruncate(" + destination + ", 0) = 0", true},
		{"write", "write(" + destination + `, ""..., 1) = 1`, true},
		{"empty write", "write(" + destination + `, "", 0) = 0`, false},
		{"pipe write", "write(3<" + traceHex("pipe:[17]") + `>, ""..., 1) = 1`, false},
		{"writev", "writev(" + destination + ", [...], 1) = 1", true},
		{"pwrite64", "pwrite64(" + destination + `, ""..., 1, 0) = 1`, true},
		{"pwritev", "pwritev(" + destination + ", [...], 1, 0) = 1", true},
		{"pwritev2", "pwritev2(" + destination + ", [...], 1, 0, 0) = 1", true},
		{"sendfile", "sendfile(" + destination + ", " + source + ", NULL, 1) = 1", true},
		{"splice", "splice(" + source + ", NULL, " + destination + ", NULL, 1, 0) = 1", true},
		{"copy_file_range", "copy_file_range(" + source + ", NULL, " + destination + ", NULL, 1, 0) = 1", true},
		{"empty copy_file_range", "copy_file_range(" + source + ", NULL, " + destination + ", NULL, 1, 0) = 0", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			trace := traceExec(10, "/bin/sh") + "10 " + tc.call + "\n"
			var want []string
			if tc.want {
				want = []string{"written.txt"}
			}
			got, err := parseTrace(root, []byte(trace))
			if err != nil || !slices.Equal(got, want) {
				t.Fatalf("paths = %q, %v; want %q", got, err, want)
			}
		})
	}
}

func TestTraceGitExecveat(t *testing.T) {
	for _, args := range []string{
		`AT_FDCWD, "` + traceHex("/usr/bin/git") + `", [...], 0x1, 0`,
		`3<` + traceHex("/usr/bin/git") + `>, "", [...], 0x1, AT_EMPTY_PATH`,
	} {
		trace := traceExec(10, "/bin/sh") +
			"10 execveat(" + args + ") = 0\n" +
			traceWrite(10, "/repo/git.txt")
		if paths, err := parseTrace("/repo", []byte(trace)); err != nil || len(paths) != 0 {
			t.Fatalf("execveat(%s) yielded paths %q, %v", args, paths, err)
		}
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

func TestTraceProcessLimit(t *testing.T) {
	var trace strings.Builder
	trace.WriteString(traceExec(10, "/bin/sh"))
	trace.WriteString(traceWrite(10, "/repo/written.txt"))
	for child := range maxProcesses - 1 {
		fmt.Fprintf(&trace, "10 clone(flags=SIGCHLD) = %d\n", child+11)
	}
	if paths, err := parseTrace("/repo", []byte(trace.String())); err != nil || !slices.Equal(paths, []string{"written.txt"}) {
		t.Fatalf("at limit: paths = %q, %v; want written.txt", paths, err)
	}
	fmt.Fprintf(&trace, "10 clone(flags=SIGCHLD) = %d\n", maxProcesses+10)
	if paths, err := parseTrace("/repo", []byte(trace.String())); !errors.Is(err, ErrLimit) || len(paths) != 0 {
		t.Fatalf("over limit: paths = %q, %v; want no paths and ErrLimit", paths, err)
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
