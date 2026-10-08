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
	"syscall"
	"time"

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
//     it as before; SIGINT/SIGQUIT are forwarded only when this process is not in the
//     terminal's foreground group (a second SIGINT makes Terraform abort);
//   - SIGTERM, SIGHUP, SIGUSR1 and SIGUSR2 are forwarded to the command;
//   - the command is sent SIGTERM if this process dies of anything, SIGKILL included
//     (PR_SET_PDEATHSIG), so it never runs on without the Side beside it. Nothing of this
//     process outlives the command: the Side is a goroutine of this process.
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
	sigs := make(chan os.Signal, 8)
	forward := []os.Signal{syscall.SIGTERM, syscall.SIGHUP, syscall.SIGUSR1, syscall.SIGUSR2}
	// SIGINT/SIGQUIT are caught either way, so this process outlives the Ctrl-C it shares
	// with the command and reports the command's end.
	forwardInterrupts := !foregroundOfTerminal()
	signal.Notify(sigs, append(forward, syscall.SIGINT, syscall.SIGQUIT)...)
	defer signal.Stop(sigs)

	if err := cmd.Start(); err != nil {
		return Outcome{}, err
	}

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

	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()

	var stopped *Stop
	var killTimer <-chan time.Time
	var werr error
loop:
	for {
		select {
		case werr = <-waited:
			break loop
		case sig := <-sigs:
			if sig == syscall.SIGINT || sig == syscall.SIGQUIT {
				if !forwardInterrupts {
					continue
				}
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
	// A Stop that arrived with the command's own exit still counts: the Side saw the
	// refresh fail, and the command was ended because of it.
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

	var out Outcome
	var ee *exec.ExitError
	switch {
	case werr == nil:
	case errors.As(werr, &ee):
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			out.Signal = ws.Signal()
			out.Code = 128 + int(ws.Signal())
		} else {
			out.Code = ee.ExitCode()
		}
	default:
		return Outcome{}, werr
	}
	if stopped != nil {
		if stopped.Msg != "" {
			fmt.Fprintln(stderr, stopped.Msg)
		}
		return Outcome{Code: stopped.Code}, nil
	}
	return out, nil
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

// Exit ends this process with the outcome of a supervised command: the command's signal
// is re-raised on this process (default action restored first) so the caller sees the same
// death, otherwise its exit code.
func (o Outcome) Exit() {
	if o.Signal != 0 {
		signal.Reset(o.Signal)
		_ = syscall.Kill(syscall.Getpid(), o.Signal)
		time.Sleep(100 * time.Millisecond)
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
