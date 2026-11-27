// Package writeevidence prototypes process-scoped write attribution for Codex shell tools.
// It has no runtime callers. A caller must own the command launch and supply the session and
// turn identities at that boundary.
package writeevidence

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

const (
	maxDuration  = 15 * time.Second
	maxTrace     = 2 << 20
	maxProcesses = 512
	maxPaths     = 512
)

// ErrLimit reports that the observation exceeded its time, trace, process, or path budget.
var ErrLimit = errors.New("shell write observation exceeded its budget")

// Scope is the identity of the turn that launched the observed command.
type Scope struct {
	SessionID string
	TurnID    string
}

// Evidence contains sorted, unique workspace-relative paths observed in one command tree.
// Paths are syscall evidence. They can include intermediates that no longer exist.
type Evidence struct {
	Scope Scope
	Paths []string
}

// Command describes a new command with an absolute executable path and working directory.
// Env and the stream fields have the same defaults as exec.Cmd.
type Command struct {
	Path                  string
	Args                  []string
	Dir                   string
	Env                   []string
	Stdin, Stdout, Stderr *os.File
}

// Run observes successful data writes, descriptor-based truncation, and exclusive creation
// in root. The scope is supplied by the launcher and belongs to this invocation. The command must
// exit successfully and tracing must finish before Run returns evidence. On interruption or
// overflow, the tracer and its tracees are stopped and the invocation yields no evidence.
func Run(ctx context.Context, scope Scope, root string, command Command) (Evidence, error) {
	if scope.SessionID == "" || scope.TurnID == "" {
		return Evidence{}, errors.New("shell write observation needs a session and turn")
	}
	paths, err := observe(ctx, root, command, maxDuration, maxTrace)
	if err != nil {
		return Evidence{}, err
	}
	return Evidence{Scope: scope, Paths: paths}, nil
}

func observe(ctx context.Context, root string, command Command, duration time.Duration, traceLimit int64) ([]string, error) {
	if !filepath.IsAbs(root) || !filepath.IsAbs(command.Path) || !filepath.IsAbs(command.Dir) {
		return nil, errors.New("shell write observation needs absolute workspace, executable, and working directory paths")
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve observation workspace: %w", err)
	}
	workspace, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("stat observation workspace: %w", err)
	}
	if !workspace.IsDir() || root == string(filepath.Separator) {
		return nil, errors.New("shell write observation needs a workspace directory below filesystem root")
	}
	ctx, cancel := context.WithTimeoutCause(ctx, duration, ErrLimit)
	defer cancel()

	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	defer writer.Close()

	// Hex formatting makes filenames unambiguous, and a zero string limit elides write
	// buffers and argv contents. The trampoline closes the trace pipe before the tool starts.
	args := []string{
		"-f", "-z", "-qq", "-xx", "-y", "-s", "0", "--kill-on-exit", "-e", "signal=none",
		"-e", "trace=execve,execveat,clone,clone3,?fork,?vfork,unshare,setns,mount,umount2,move_mount,pivot_root,chroot," +
			"?open,openat,?creat,ftruncate,write,writev,pwrite64,pwritev,pwritev2,sendfile,copy_file_range,splice",
		"-o", "/proc/self/fd/3", "--", "/bin/sh", "-c", `exec 3>&-; exec "$@"`, "sh", command.Path,
	}
	cmd := exec.CommandContext(ctx, "strace", append(args, command.Args...)...)
	cmd.Dir = command.Dir
	cmd.Env = command.Env
	cmd.ExtraFiles = []*os.File{writer}
	// Linux ties Pdeathsig to the launching thread. Keep it alive through Wait so a
	// runner crash kills the tracer without tying ordinary Go thread churn to the tool.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	// Assign only non-nil files to avoid a typed nil in the io.Reader/Writer interfaces.
	if command.Stdin != nil {
		cmd.Stdin = command.Stdin
	}
	if command.Stdout != nil {
		cmd.Stdout = command.Stdout
	}
	if command.Stderr != nil {
		cmd.Stderr = command.Stderr
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start shell write tracer: %w", err)
	}
	_ = writer.Close()
	// The pipe belongs to the tracer after the trampoline execs. Killing the tracer also
	// kills tracees, even ones that left the original process group.
	trace, readErr := io.ReadAll(io.LimitReader(reader, traceLimit+1))
	overflow := int64(len(trace)) > traceLimit
	if readErr != nil || overflow {
		cancel()
	}
	waitErr := cmd.Wait()
	if overflow {
		return nil, ErrLimit
	}
	if err := context.Cause(ctx); err != nil && readErr == nil {
		return nil, err
	}
	if readErr != nil {
		return nil, fmt.Errorf("read shell write trace: %w", readErr)
	}
	if waitErr != nil {
		return nil, fmt.Errorf("shell write tracer or command exited: %w", waitErr)
	}
	current, err := os.Stat(root)
	if err != nil || !os.SameFile(workspace, current) {
		return nil, errors.New("workspace moved during shell write observation")
	}
	return parseTrace(root, trace)
}
