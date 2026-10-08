package cloudexec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// StopGrace is how long Supervise waits after SIGTERM before it kills the command.
const StopGrace = 30 * time.Second

// Stop is what a Side asks of the supervisor: end the command and exit with Code, after
// printing Msg (when not empty).
type Stop struct {
	Code int
	Msg  string
}

// Supervision runs a command as a child of this process instead of exec'ing into it, so
// something can run beside it for as long as it lives (af-gcloud-exec's token refresh).
type Supervision struct {
	Prog string
	Argv []string
	Env  []string
	// Side runs in its own goroutine while the command lives. ctx ends when the command has
	// exited; Side returns promptly then, and the supervisor waits for it. stop asks for the
	// command to be ended: SIGTERM, then SIGKILL after Grace. Only the first call counts.
	Side func(ctx context.Context, stop func(Stop))
	// Grace is StopGrace when zero.
	Grace  time.Duration
	Stderr io.Writer
}

// Outcome is how a supervised command ended. Signal is set when the command died of one
// (Code is then 128+Signal, the shell's reading).
type Outcome struct {
	Code   int
	Signal syscall.Signal
}

// Supervise starts the command and returns when it has ended, with what the caller should
// exit with. It keeps the exec'd command's behaviour as far as a parent can:
//   - the command shares this process's group and terminal, so Ctrl-C and job control reach
//     it as before; SIGINT/SIGQUIT are forwarded only when, at the moment they arrive, this
//     process is not in the terminal's foreground group (a second SIGINT makes Terraform
//     abort, and fg/bg moves the group);
//   - SIGTERM, SIGHUP, SIGUSR1 and SIGUSR2 are forwarded to the command;
//   - a command that stops itself (SIGSTOP) stops this process too, so the caller's shell
//     sees the job stopped, and SIGCONT here continues the command;
//   - the command is sent SIGTERM if this process dies of anything, SIGKILL included
//     (PR_SET_PDEATHSIG). That is the direct child only, and a child may ignore SIGTERM:
//     its own children are not reached, and a killed wrapper does not remove the token
//     file (a later run sweeps it). Nothing of this process outlives the command: the Side
//     is a goroutine of this process.
func Supervise(s Supervision) (Outcome, error) {
	// Pdeathsig fires when the thread that started the command exits, so that thread is
	// pinned for the rest of the process's life.
	runtime.LockOSThread()

	stderr := s.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	grace := s.Grace
	if grace == 0 {
		grace = StopGrace
	}

	cmd := exec.Command(s.Prog, s.Argv[1:]...)
	cmd.Args = s.Argv
	cmd.Env = s.Env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}

	// Handlers go in before the command starts, so a signal in between is not lost.
	// SIGINT/SIGQUIT are caught either way, so this process outlives the Ctrl-C it shares
	// with the command and reports the command's end.
	sigs := make(chan os.Signal, 8)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGUSR1, syscall.SIGUSR2,
		syscall.SIGINT, syscall.SIGQUIT, syscall.SIGCONT)
	defer signal.Stop(sigs)

	if err := cmd.Start(); err != nil {
		return Outcome{}, err
	}
	pid := cmd.Process.Pid

	ctx, ended := context.WithCancel(context.Background())
	defer ended()
	var (
		stopCh   = make(chan Stop, 1)
		sideDone = make(chan struct{})
	)
	if s.Side != nil {
		go func() {
			defer close(sideDone)
			s.Side(ctx, func(st Stop) {
				select {
				case stopCh <- st:
				default:
				}
			})
		}()
	} else {
		close(sideDone)
	}

	// The command is reaped here, not by cmd.Wait, which would not report that it stopped.
	events := make(chan unix.WaitStatus, 4)
	go func() {
		for {
			var ws unix.WaitStatus
			_, err := unix.Wait4(pid, &ws, unix.WUNTRACED|unix.WCONTINUED, nil)
			if err == unix.EINTR {
				continue
			}
			if err != nil {
				// Not ours any more (reaped elsewhere): report it as gone.
				ws = 0
			}
			events <- ws
			if err != nil || ws.Exited() || ws.Signaled() {
				return
			}
		}
	}()

	var stopped *Stop
	var killTimer <-chan time.Time
	var ws unix.WaitStatus
loop:
	for {
		select {
		case ws = <-events:
			switch {
			case ws.Stopped():
				// A stop the terminal delivered to the whole group has stopped this process
				// already, and the command is running again when this is read.
				if childStopped(pid) {
					_ = syscall.Kill(syscall.Getpid(), syscall.SIGSTOP)
					_ = cmd.Process.Signal(syscall.SIGCONT)
				}
			case ws.Continued():
			default:
				break loop
			}
		case sig := <-sigs:
			switch sig {
			case syscall.SIGINT, syscall.SIGQUIT:
				if foregroundOfTerminal() {
					continue
				}
			case syscall.SIGCONT:
				// Continued after the command stopped itself and stopped this process.
				if childStopped(pid) {
					_ = cmd.Process.Signal(syscall.SIGCONT)
				}
				continue
			}
			_ = cmd.Process.Signal(sig)
		case st := <-stopCh:
			if stopped == nil {
				stopped = &st
				_ = cmd.Process.Signal(syscall.SIGTERM)
				killTimer = time.After(grace)
			}
		case <-killTimer:
			_ = cmd.Process.Kill()
			killTimer = nil
		}
	}
	ended()
	<-sideDone
	if stopped == nil {
		select {
		case st := <-stopCh:
			// The command exited by itself first; its own status stands, but the Side's
			// message is still the reason a person needs.
			if st.Msg != "" {
				fmt.Fprintln(stderr, st.Msg)
			}
		default:
		}
	}
	if stopped != nil {
		if stopped.Msg != "" {
			fmt.Fprintln(stderr, stopped.Msg)
		}
		return Outcome{Code: stopped.Code}, nil
	}
	switch {
	case ws.Signaled():
		return Outcome{Signal: syscall.Signal(ws.Signal()), Code: 128 + int(ws.Signal())}, nil
	case ws.Exited():
		return Outcome{Code: ws.ExitStatus()}, nil
	}
	return Outcome{}, errors.New("the command's status could not be read")
}

// childStopped reports whether the process is in the stopped state now.
func childStopped(pid int) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	// The state is the field after the last ")" (the command name may hold one).
	i := strings.LastIndexByte(string(b), ')')
	return i >= 0 && i+2 < len(b) && b[i+2] == 'T'
}

// foregroundOfTerminal reports whether this process's group is the foreground group of a
// terminal it has open, i.e. the terminal's Ctrl-C reaches the command by itself.
func foregroundOfTerminal() bool {
	for _, f := range []*os.File{os.Stdin, os.Stderr, os.Stdout} {
		pg, err := unix.IoctlGetInt(int(f.Fd()), unix.TIOCGPGRP)
		if err == nil {
			return pg == syscall.Getpgrp()
		}
	}
	return false
}

// kernelSigaction is the kernel's struct sigaction (Linux, amd64 and arm64).
type kernelSigaction struct {
	handler  uintptr
	flags    uint64
	restorer uintptr
	mask     uint64
}

// defaultDisposition sets sig to the kernel's SIG_DFL. signal.Reset would only give back
// Go's own default, which for SIGQUIT and SIGABRT is a goroutine dump and exit 2, and for
// SIGPIPE or SIGUSR1 an ordinary exit.
func defaultDisposition(sig syscall.Signal) {
	var sa kernelSigaction
	_, _, _ = syscall.RawSyscall6(syscall.SYS_RT_SIGACTION, uintptr(sig), uintptr(unsafe.Pointer(&sa)), 0, 8, 0, 0)
}

// Exit ends this process with the outcome of a supervised command: when the command died
// of a signal, this process dies of the same one with the kernel's default action, so the
// caller's wait status matches; otherwise it exits with the command's code.
func (o Outcome) Exit() {
	if o.Signal != 0 {
		signal.Reset(o.Signal)
		defaultDisposition(o.Signal)
		_ = unix.PthreadSigmask(unix.SIG_UNBLOCK, &unix.Sigset_t{Val: [16]uint64{1 << (uint(o.Signal) - 1)}}, nil)
		_ = syscall.Kill(syscall.Getpid(), o.Signal)
		time.Sleep(time.Second)
	}
	os.Exit(o.Code)
}

// Supervise is Exec for a command that needs something beside it: it runs the command as
// a child, exits with its status, and returns only by exiting. It exits ExitRefused when
// the command cannot be started.
func (w Wrapper) Supervise(s Supervision) {
	out, err := Supervise(s)
	if err != nil {
		w.Fail(ExitRefused, "run "+s.Prog+": "+err.Error())
	}
	out.Exit()
}
