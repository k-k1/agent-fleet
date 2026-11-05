//go:build linux

package routegw

import (
	"errors"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// soPeerPIDFD is SO_PEERPIDFD (Linux 6.5): a pidfd for the process that connected, which keeps
// naming that process even after its pid is reused. golang.org/x/sys does not define it yet; 77
// is the asm-generic value, which amd64 and arm64 (the Agent's only targets) use.
const soPeerPIDFD = 77

// peerPIDFD asks the kernel for the connector's pidfd. A var so a test can make it fail the
// ways a kernel does.
var peerPIDFD = func(sock int) (int, error) { return unix.GetsockoptInt(sock, unix.SOL_SOCKET, soPeerPIDFD) }

// peerStart reads the start time of the connected process pid. With SO_PEERPIDFD the read is
// proven to be of that process: its pidfd still names pid and the process is still alive after
// the read, so the pid was not reused in between. Only a kernel that does not know the option
// (ENOPROTOOPT, before 6.5) falls back to reading the pid directly, where the window left is the
// connector exiting and its pid being reused between connect and this read. Any other failure
// refuses: a kernel that has the option answers EINVAL when the connector has already exited
// (pidfd_prepare finds no task), and that is exactly when the pid may name someone else.
func peerStart(sock, pid int) (uint64, bool) {
	pidfd, err := peerPIDFD(sock)
	if errors.Is(err, unix.ENOPROTOOPT) {
		info, ok := readProc("/proc", pid)
		return info.StartTicks, ok
	}
	if err != nil {
		return 0, false
	}
	defer unix.Close(pidfd)
	if pidfdPID(pidfd) != pid {
		return 0, false // exited before the accept, or not the process SO_PEERCRED named
	}
	info, ok := readProc("/proc", pid)
	if !ok || unix.PidfdSendSignal(pidfd, 0, nil, 0) != nil {
		return 0, false
	}
	return info.StartTicks, true
}

// pidfdPID is the pid a pidfd names in this pid namespace, -1 once the process has exited.
func pidfdPID(pidfd int) int {
	b, err := os.ReadFile("/proc/self/fdinfo/" + strconv.Itoa(pidfd))
	if err != nil {
		return -1
	}
	for _, ln := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(ln, "Pid:"); ok {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				return -1
			}
			return n
		}
	}
	return -1
}
