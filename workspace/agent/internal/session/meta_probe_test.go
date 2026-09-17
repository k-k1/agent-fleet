package session

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// The probe behind ADR 0087's source A. It is skipped unless AF_PROBE=1 because its only
// output is a syscall count, which needs strace around it to be worth anything:
//
//	go test -c -o /tmp/probe ./internal/session/
//	AF_PROBE=1 strace -f -c -e trace=getdents64,openat,newfstatat,read,close \
//	  /tmp/probe -test.run '^TestProbeListMetas$'
//
// ⚠️ -e trace=file is NOT enough: read and close take no file name, so they fall outside
// that set and 624 of the 836 syscalls per call go uncounted.
//
// ⚠️ A running Agent cannot be straced here (ptrace_scope=1 refuses PTRACE_SEIZE on a
// process it does not own), which is why the real code is frozen into a test binary and
// raised as strace's own child instead.
//
// What it shows is that the COUNT does not change — ListMetas still reads one file per
// session — and that the count is now spent on the home volume. Before the move each of
// those syscalls was an NFS round trip on the keep mount, once every four seconds per open
// Console tab; point AF_SESSIONS_DIR at a directory under ~/.config/agent-fleet to
// reproduce the old side of the measurement with the same binary.
func TestProbeListMetas(t *testing.T) {
	if os.Getenv("AF_PROBE") == "" {
		t.Skip("set AF_PROBE=1 and run under strace; see the comment above")
	}
	const metas, calls = 207, 100 // the production workspace measured on 2026-09-16

	// HOME, not AF_SESSIONS_DIR: the point of the measurement is WHICH ROOT the reads land
	// under, so the probe has to go through the same resolver production does.
	home := t.TempDir()
	t.Setenv("HOME", home)
	if want := filepath.Join(home, ".local", "state", "agent-fleet", "sessions"); MetaDir() != want {
		t.Fatalf("MetaDir() = %s, want %s", MetaDir(), want)
	}
	for i := 0; i < metas; i++ {
		WriteMeta(Meta{Name: fmt.Sprintf("slot%03d", i), Dir: "/home/dev/repos/agent-fleet", Kind: "claude"})
	}
	t.Logf("probing %d ListMetas() calls over %d metas in %s", calls, metas, MetaDir())
	got := 0
	for i := 0; i < calls; i++ {
		got = len(ListMetas())
	}
	if got != metas {
		t.Fatalf("ListMetas returned %d metas, want %d — the probe measured the wrong thing", got, metas)
	}
}
