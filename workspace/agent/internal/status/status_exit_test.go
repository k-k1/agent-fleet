package status

import "testing"

func TestExitReasonAfterStop(t *testing.T) {
	for _, c := range []struct {
		name      string
		code, sig int
		oom, ask  bool
		want      string
	}{
		{"cli handled SIGTERM, asked", 143, 0, false, true, "stopped"},
		{"cli exit 1 during asked stop", 1, 0, false, true, "stopped"},
		{"143 nobody asked", 143, 0, false, false, "crashed"},
		{"clean exit asked", 0, 0, false, true, "exited"},
		{"SIGKILL after grace, asked", 137, 9, false, true, "killed"},
		{"OOM during asked stop", 137, 9, true, true, "oom"},
		{"SIGSEGV during asked stop", 139, 11, false, true, "crashed"},
		{"SIGTERM death asked", 143, 15, false, true, "stopped"},
	} {
		if got := ExitReasonAfterStop(c.code, c.sig, c.oom, c.ask); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}
