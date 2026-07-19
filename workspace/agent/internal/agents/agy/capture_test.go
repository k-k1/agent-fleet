package agy

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// captureConversation は「起動前スナップショットから変わった UUID だけを採用」
// が肝: 同じ dir で前のセッションが残した stale なマップエントリを新スロットが
// 拾ってはいけない（docs/32 Track D-3）。
func TestCaptureConversationAdoptsOnlyFreshUUID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := "/home/dev/repos/proj"
	m := session.Meta{Dir: dir, Name: "slot01", Kind: session.KindAgy}
	slotSid := session.UUID(dir, "slot01")

	// Fresh launch in a dir with a stale entry: snapshot it (BuildLaunch 相当).
	writeLastConversations(t, map[string]string{dir: "stale-uuid"})
	prelaunch.Write(slotSid, lastConversationFor(dir))

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
	prelaunch.Write(slotSid, lastConversationFor(dir))
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
	agentImpl{}.ClearResume(sid)
	if sids.Read(sid) != "" || prelaunch.Read(sid) != "" {
		t.Fatal("ClearResume left store entries behind")
	}
}
