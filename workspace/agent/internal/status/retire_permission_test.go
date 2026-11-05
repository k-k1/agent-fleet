package status

import (
	"testing"
	"time"
)

func permPrompt(t *testing.T, sid, msg string) (rev string, at time.Time) {
	t.Helper()
	Persist(sid, "permission")
	WritePendingPermission(sid, msg)
	st, _ := Read(sid)
	at, _ = PendingPermissionAt(sid)
	return st.Rev, at
}

func TestRetirePermissionIf(t *testing.T) {
	isolate(t)
	rev, at := permPrompt(t, "rp-ok", "Bash · ls")
	if !RetirePermissionIf("rp-ok", rev, at) {
		t.Fatal("unchanged prompt not retired")
	}
	if _, ok := ReadPendingPermission("rp-ok"); ok {
		t.Fatal("payload kept")
	}
	if st, _ := Read("rp-ok"); st.State != "idle" {
		t.Fatalf("state %q", st.State)
	}

	// Any hook write since the check (working, a turn end) wins.
	rev, at = permPrompt(t, "rp-rev", "Bash · ls")
	Persist("rp-rev", "working")
	if RetirePermissionIf("rp-rev", rev, at) {
		t.Fatal("retired over a newer status write")
	}
	if st, _ := Read("rp-rev"); st.State != "working" {
		t.Fatalf("state %q", st.State)
	}

	// The next prompt's hook has persisted "permission" again but not yet written its payload:
	// the state reads the same, the Rev does not.
	rev, at = permPrompt(t, "rp-rev2", "Bash · ls")
	Persist("rp-rev2", "permission")
	if RetirePermissionIf("rp-rev2", rev, at) {
		t.Fatal("retired over the next prompt's state write")
	}

	// A newer prompt's payload is not the one answered.
	rev, at = permPrompt(t, "rp-at", "Bash · ls")
	time.Sleep(10 * time.Millisecond)
	WritePendingPermission("rp-at", "Bash · rm -rf build")
	if RetirePermissionIf("rp-at", rev, at) {
		t.Fatal("retired a newer payload")
	}
	if _, ok := ReadPendingPermission("rp-at"); !ok {
		t.Fatal("newer payload removed")
	}
}

// The next prompt's hook arriving between the check and the writes waits for the lock, then
// writes its state and payload after the retirement: the new prompt survives whole (#3376 review).
func TestRetirePermissionIfNextPromptDuringRetire(t *testing.T) {
	isolate(t)
	defer func() { afterCheck = func() {} }()
	sid := "rp-race"
	rev, at := permPrompt(t, sid, "Bash · ls")
	done := make(chan struct{})
	afterCheck = func() {
		go func() {
			time.Sleep(5 * time.Millisecond)
			Persist(sid, "permission") // the hook's order: state first, then payload
			WritePendingPermission(sid, "Bash · rm -rf build")
			close(done)
		}()
		time.Sleep(20 * time.Millisecond)
	}
	if !RetirePermissionIf(sid, rev, at) {
		t.Fatal("not retired")
	}
	<-done
	if msg, ok := ReadPendingPermission(sid); !ok || msg != "Bash · rm -rf build" {
		t.Fatalf("next prompt's payload = %q,%v", msg, ok)
	}
	if st, _ := Read(sid); st.State != "permission" {
		t.Fatalf("next prompt's state overwritten: %q", st.State)
	}
}
