package sessionx

// #1255: which session actions keep the peer messages a Managed queue was holding (they become
// the next start's first turns) and which drop them.

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// holdPeerMessage leaves one peer message held for name, as a queue behind a running turn does.
func holdPeerMessage(t *testing.T, name string) {
	t.Helper()
	q := agents.NewTurnQueue(name, nil, agents.LedgerAtAccept)
	q.Accept(agents.TurnInput{Prompt: "running", Origin: agents.Origin{Kind: agents.OriginMember}})
	q.Take()
	q.Accept(agents.TurnInput{Prompt: "[agent-fleet:peer from=other intent=notice reply=none] hi",
		Origin: agents.Origin{Kind: agents.OriginPeer, From: "other"}})
	if n := agents.HeldCount(name); n != 1 {
		t.Fatalf("held = %d, want 1", n)
	}
}

func TestHeldPeerKeptByHalt(t *testing.T) {
	fakeTmux(t)
	t.Setenv("HOME", t.TempDir())
	const name = "held_halt"
	m := session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCodex, Driver: session.DriverManaged}
	session.WriteMeta(m)
	holdPeerMessage(t, name)
	if _, err := haltSession(m, true); err != nil {
		t.Fatal(err)
	}
	if n := agents.HeldCount(name); n != 1 {
		t.Fatalf("held after halt = %d, want 1", n)
	}
}

func TestHeldPeerDroppedByArchiveAndTrash(t *testing.T) {
	fakeTmux(t)
	t.Setenv("HOME", t.TempDir())
	const name = "held_archive"
	m := session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCodex, Driver: session.DriverManaged}
	session.WriteMeta(m)
	holdPeerMessage(t, name)
	ArchiveSession(m)
	if n := agents.HeldCount(name); n != 0 {
		t.Fatalf("held after archive = %d, want 0", n)
	}

	holdPeerMessage(t, name)
	ForgetRuntime(m)
	if n := agents.HeldCount(name); n != 0 {
		t.Fatalf("held after the trash = %d, want 0", n)
	}
}

func TestShelfAndRestoreReportMetaWriteFailures(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a read-only directory does not stop root")
	}
	fakeTmux(t)
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(WaitListBackground)
	name := "held_write_failure"
	m := session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindShell}
	session.WriteMeta(m)
	holdPeerMessage(t, name)
	t.Cleanup(func() { agents.DropHeld(name, agents.DropArchived) })
	setMode := func(mode os.FileMode) {
		t.Helper()
		if err := os.Chmod(session.MetaDir(), mode); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { setMode(0o700) })
	assertWriteFailure := func(h http.HandlerFunc) {
		t.Helper()
		rec := callHandler(h, http.MethodPost, name, "")
		if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), errCodeMetaWriteFailed) {
			t.Fatalf("answer = %d %s, want meta_write_failed", rec.Code, rec.Body)
		}
	}
	setMode(0o500)
	assertWriteFailure(HandleArchiveSession)
	if cur, ok := session.ReadMeta(name); !ok || cur.Archived || agents.HeldCount(name) != 1 {
		t.Fatalf("failed shelf changed the visible row or dropped its queued input: %+v", cur)
	}
	setMode(0o700)
	if rec := callHandler(HandleArchiveSession, http.MethodPost, name, ""); rec.Code != http.StatusOK {
		t.Fatalf("archive retry = %d %s", rec.Code, rec.Body)
	}
	setMode(0o500)
	assertWriteFailure(HandleRestoreSession)
	if cur, ok := session.ReadMeta(name); !ok || !cur.Archived {
		t.Fatal("failed restore removed the session from the shelf")
	}
	setMode(0o700)
	if rec := callHandler(HandleRestoreSession, http.MethodPost, name, ""); rec.Code != http.StatusOK {
		t.Fatalf("restore retry = %d %s", rec.Code, rec.Body)
	}
}

func TestHeldPeerDroppedBySwitchToTerminal(t *testing.T) {
	fakeTmux(t)
	t.Setenv("HOME", t.TempDir())
	const name = "held_to_tui"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCodex, Driver: session.DriverManaged})
	holdPeerMessage(t, name)
	rec := postDriver(t, name, `{"driver":"tui"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if n := agents.HeldCount(name); n != 0 {
		t.Fatalf("held after a switch to Terminal = %d, want 0", n)
	}
}
