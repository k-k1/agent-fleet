package writeevidence

import (
	"errors"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

var errTrace = errors.New("incomplete or unrecognized shell write trace")

type tracedProcess struct {
	parent        *tracedProcess
	forkAt, gitAt int
	paths         map[string]struct{}
	checking      bool
	checked       bool
	inheritedGit  bool
}

func parseTrace(root string, trace []byte) ([]string, error) {
	if len(trace) == 0 || trace[len(trace)-1] != '\n' {
		return nil, errTrace
	}
	processes := make(map[int]*tracedProcess)
	process := func(pid int) *tracedProcess {
		if processes[pid] == nil {
			processes[pid] = &tracedProcess{}
		}
		return processes[pid]
	}
	var first *tracedProcess
	at := 0
	for line := range strings.SplitSeq(string(trace[:len(trace)-1]), "\n") {
		at++
		pidText, call, ok := strings.Cut(line, " ")
		pid, err := strconv.Atoi(pidText)
		name, rest, hasArgs := strings.Cut(strings.TrimSpace(call), "(")
		if !ok || err != nil || pid <= 0 || !hasArgs {
			return nil, errTrace
		}
		end := strings.LastIndex(rest, ") ")
		if end < 0 {
			return nil, errTrace
		}
		result, ok := strings.CutPrefix(strings.TrimSpace(rest[end+1:]), "= ")
		if !ok {
			return nil, errTrace
		}
		value, _, _ := strings.Cut(result, "<")
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil || n < 0 {
			return nil, errTrace
		}
		p := process(pid)
		if first == nil {
			if name != "execve" {
				return nil, errTrace
			}
			first = p
		}
		args := strings.Split(rest[:end], ", ")
		switch name {
		case "clone", "clone3", "fork", "vfork":
			if changesNamespace(rest[:end]) {
				return nil, errTrace
			}
			child := process(int(n))
			// A reused PID would combine unrelated process lifetimes in this trace.
			if n == 0 || child == first || child.parent != nil || child == p {
				return nil, errTrace
			}
			child.parent, child.forkAt = p, at
		case "setns", "mount", "umount2", "move_mount", "pivot_root", "chroot":
			// Process IDs and reported paths must still refer to the runner's namespaces.
			return nil, errTrace
		case "unshare":
			if changesNamespace(rest[:end]) {
				return nil, errTrace
			}
		case "execve", "execveat":
			path := execPath(name, args)
			if path == "" {
				return nil, errTrace
			}
			if p.gitAt == 0 && filepath.Base(path) == "git" {
				p.gitAt = at
			}
		default:
			if p.gitAt == 0 {
				path := workspacePath(root, writePath(name, args, result, n))
				if path != "" {
					if p.paths == nil {
						p.paths = make(map[string]struct{})
					}
					p.paths[path] = struct{}{}
				}
			}
		}
		if len(processes) > maxProcesses || len(p.paths) > maxPaths {
			return nil, ErrLimit
		}
	}
	unique := make(map[string]struct{})
	for _, p := range processes {
		if err := resolveGitAncestry(p, first); err != nil {
			return nil, err
		}
		if !p.inheritedGit {
			for path := range p.paths {
				unique[path] = struct{}{}
				if len(unique) > maxPaths {
					return nil, ErrLimit
				}
			}
		}
	}
	paths := make([]string, 0, len(unique))
	for path := range unique {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	return paths, nil
}

func changesNamespace(args string) bool {
	return strings.Contains(args, "CLONE_NEWPID") || strings.Contains(args, "CLONE_NEWNS")
}

// With successful-only tracing, a child's writes can appear before the parent receives the
// return value of clone. Resolve ancestry after the stream ends and compare Git exec to fork
// in the parent. A process may have written files before it execed Git.
func resolveGitAncestry(p, first *tracedProcess) error {
	if p == first || p.checked {
		return nil
	}
	if p.parent == nil || p.checking {
		return errTrace
	}
	p.checking = true
	if err := resolveGitAncestry(p.parent, first); err != nil {
		return err
	}
	p.inheritedGit = p.parent.inheritedGit || p.parent.gitAt > 0 && p.parent.gitAt < p.forkAt
	p.checked = true
	return nil
}

func execPath(name string, args []string) string {
	if name == "execveat" {
		if len(args) < 2 {
			return ""
		}
		if args[1] == `""` {
			return descriptorPath(args[0])
		}
		return hexPath(strings.Trim(args[1], `"`))
	}
	return hexPath(strings.Trim(args[0], `"`))
}

func writePath(name string, args []string, result string, n int64) string {
	switch name {
	case "creat":
		return descriptorPath(result)
	case "open", "openat":
		flagIndex := 1
		if name == "openat" {
			flagIndex = 2
		}
		if len(args) > flagIndex {
			flags := "|" + args[flagIndex] + "|"
			if strings.Contains(flags, "|O_TRUNC|") ||
				strings.Contains(flags, "|O_CREAT|") && strings.Contains(flags, "|O_EXCL|") {
				return descriptorPath(result)
			}
		}
	case "ftruncate":
		return descriptorPath(args[0])
	case "write", "writev", "pwrite64", "pwritev", "pwritev2", "sendfile":
		if n > 0 {
			return descriptorPath(args[0])
		}
	case "splice", "copy_file_range":
		if n > 0 && len(args) > 2 {
			return descriptorPath(args[2])
		}
	}
	return ""
}

func descriptorPath(fd string) string {
	_, escaped, ok := strings.Cut(fd, "<")
	if !ok || !strings.HasPrefix(escaped, `\x2f`) || !strings.HasSuffix(escaped, ">") {
		return ""
	}
	return hexPath(strings.TrimSuffix(escaped, ">"))
}

func hexPath(escaped string) string {
	if !strings.HasPrefix(escaped, `\x`) {
		return ""
	}
	path, err := strconv.Unquote(`"` + escaped + `"`)
	if err != nil {
		return ""
	}
	return path
}

func workspacePath(root, path string) string {
	if path == "" || strings.HasSuffix(path, " (deleted)") {
		return ""
	}
	path, ok := strings.CutPrefix(path, root+string(filepath.Separator))
	if !ok || !filepath.IsLocal(path) || filepath.Clean(path) != path {
		return ""
	}
	for part := range strings.SplitSeq(path, string(filepath.Separator)) {
		if part == ".git" {
			return ""
		}
	}
	return path
}
