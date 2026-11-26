package mcpsrv

import (
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// A space or home session records no repo; the mirror's display falls back to its directory's
// own name before the opaque slug.
func TestSessionRowDisplayFallsBackToTheDirectoryName(t *testing.T) {
	cases := []struct {
		row  store.SessionRow
		want string
	}{
		{store.SessionRow{Name: "s1", Label: "[AF:s1] app @0831-1922", Repo: "app"}, "app @0831-1922"},
		{store.SessionRow{Name: "s1", Repo: "app", Dir: "/home/dev/repos/app"}, "app"},
		{store.SessionRow{Name: "s1", Dir: "/home/dev/spaces/notes/"}, "notes"},
		{store.SessionRow{Name: "s1"}, "s1"},
		{store.SessionRow{Name: "s1", Dir: "/"}, "s1"},
	}
	for _, c := range cases {
		if got := sessionRowDisplay(c.row); got != c.want {
			t.Errorf("sessionRowDisplay(%+v) = %q, want %q", c.row, got, c.want)
		}
	}
}
