package agy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// The point of captureConversation is "adopt only a UUID that changed since the
// pre-launch snapshot": a new slot must not pick up a stale map entry a previous
// session left behind for the same dir (docs/log/32 Track D-3).
func TestCaptureConversationAdoptsOnlyFreshUUID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := "/home/dev/repos/proj"
	m := session.Meta{Dir: dir, Name: "slot01", Kind: session.KindAgy}
	slotSid := session.UUID(dir, "slot01")

	// Fresh launch in a dir with a stale entry: snapshot it (as BuildLaunch does).
	writeLastConversations(t, map[string]string{dir: "stale-uuid"})
	prelaunch.Write(slotSid, LastConversationFor(dir))

	// Map still shows the stale entry → must NOT adopt.
	captureConversation(m)
	if got := sids.Read(slotSid); got != "" {
		t.Fatalf("adopted stale uuid %q", got)
	}

	// agy wrote this session's conversation → adopt it.
	writeLastConversations(t, map[string]string{dir: "fresh-uuid"})
	captureConversation(m)
	if got := sids.Read(slotSid); got != "fresh-uuid" {
		t.Fatalf("got %q want fresh-uuid", got)
	}

	// Later map churn (another agy run in the dir) must not move the slot.
	writeLastConversations(t, map[string]string{dir: "other-uuid"})
	captureConversation(m)
	if got := sids.Read(slotSid); got != "fresh-uuid" {
		t.Fatalf("slot moved to %q; want pinned fresh-uuid", got)
	}
}

func TestCaptureConversationEmptyDirNoSnapshot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := "/home/dev/repos/newproj"
	m := session.Meta{Dir: dir, Name: "slot02", Kind: session.KindAgy}
	slotSid := session.UUID(dir, "slot02")

	// No map at all at launch (first ever agy run): snapshot is "".
	prelaunch.Write(slotSid, LastConversationFor(dir))
	captureConversation(m)
	if got := sids.Read(slotSid); got != "" {
		t.Fatalf("adopted %q from empty map", got)
	}
	writeLastConversations(t, map[string]string{dir: "first-uuid"})
	captureConversation(m)
	if got := sids.Read(slotSid); got != "first-uuid" {
		t.Fatalf("got %q want first-uuid", got)
	}
}

func TestClearResumeDropsBothStores(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sid := session.UUID("/d", "s")
	sids.Write(sid, "u1")
	prelaunch.Write(sid, "u0")
	brainPrelaunch.Write(sid, "conv-a")
	agentImpl{}.ClearResume(sid)
	if sids.Read(sid) != "" || prelaunch.Read(sid) != "" || brainPrelaunch.Read(sid) != "" {
		t.Fatal("ClearResume left store entries behind")
	}
}

// brain-dir diff: while polling a live session, adopt when exactly one brain/<uuid>/ the
// snapshot did not have has appeared (that is what lights the live mirror). Two or more
// are passed over to avoid picking the wrong one, and with no snapshot at all (a resume,
// or a launch from before the feature) nothing is diagnosed.
func TestCaptureConversationBrainDirDiff(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := "/home/dev/repos/proj"
	m := session.Meta{Dir: dir, Name: "slot03", Kind: session.KindAgy}
	slotSid := session.UUID(dir, "slot03")

	mkBrain := func(name string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(brainDir(), name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Launch-time snapshot: one pre-existing conversation.
	mkBrain("conv-old")
	prelaunch.Write(slotSid, "")
	brainPrelaunch.Write(slotSid, strings.Join(listBrainDirs(), "\n"))

	// Nothing new yet → no adoption.
	captureConversation(m)
	if got := sids.Read(slotSid); got != "" {
		t.Fatalf("adopted %q before any new conversation", got)
	}

	// First prompt landed → exactly one fresh dir → adopt while alive.
	mkBrain("conv-new")
	captureConversation(m)
	if got := sids.Read(slotSid); got != "conv-new" {
		t.Fatalf("got %q want conv-new", got)
	}
	if brainPrelaunch.Read(slotSid) != "" {
		t.Fatal("snapshot not cleaned up after adoption")
	}
}

func TestCaptureConversationBrainDiffAmbiguousDefers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := "/home/dev/repos/proj"
	m := session.Meta{Dir: dir, Name: "slot04", Kind: session.KindAgy}
	slotSid := session.UUID(dir, "slot04")

	prelaunch.Write(slotSid, "")
	brainPrelaunch.Write(slotSid, "") // empty snapshot (no conversations yet)
	for _, n := range []string{"conv-a", "conv-b"} {
		if err := os.MkdirAll(filepath.Join(brainDir(), n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	captureConversation(m)
	if got := sids.Read(slotSid); got != "" {
		t.Fatalf("guessed %q between two fresh conversations", got)
	}

	// The graceful-exit cwd map later disambiguates (per-cwd, authoritative).
	writeLastConversations(t, map[string]string{dir: "conv-b"})
	captureConversation(m)
	if got := sids.Read(slotSid); got != "conv-b" {
		t.Fatalf("got %q want conv-b from cwd map", got)
	}
}

func TestCaptureConversationNoSnapshotSkipsBrainDiff(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := "/home/dev/repos/proj"
	m := session.Meta{Dir: dir, Name: "slot05", Kind: session.KindAgy}
	slotSid := session.UUID(dir, "slot05")

	// A brain dir exists but this slot has no launch snapshot (resume / old
	// launch): the diff has no baseline and must stay silent.
	if err := os.MkdirAll(filepath.Join(brainDir(), "conv-x"), 0o755); err != nil {
		t.Fatal(err)
	}
	prelaunch.Write(slotSid, "")
	captureConversation(m)
	if got := sids.Read(slotSid); got != "" {
		t.Fatalf("adopted %q without a snapshot baseline", got)
	}
}
