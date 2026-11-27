package sessionx

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func TestRecreateReportsLaunchAndRestoreFailures(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a read-only directory does not stop root")
	}
	p := newCreateProbe(t)
	const name = "recrfail"
	t.Cleanup(WaitListBackground)

	if err := session.WriteMetaChecked(session.Meta{Name: name, Dir: p.repo, Kind: session.KindClaude}); err != nil {
		t.Fatal(err)
	}
	metaDir := session.MetaDir()
	setMode := func(mode os.FileMode) {
		t.Helper()
		if err := os.Chmod(metaDir, mode); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { setMode(0o700) })
	launchErr := &agents.RouteRefusal{
		Status: http.StatusConflict, Code: "test_route_refusal", Message: "launch refused at the driver",
	}
	launchTmuxFn = func(session.Meta, bool) error {
		if old, ok := session.ReadMeta(name); !ok || !old.Archived {
			t.Fatalf("old session should be on the shelf at launch: %+v", old)
		}
		setMode(0o500)
		return launchErr
	}

	w := callHandler(HandleRecreateSession, http.MethodPost, name, "")
	var out struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if w.Code != launchErr.Status || out.Error.Code != launchErr.Code ||
		!strings.HasPrefix(out.Error.Message, launchErr.Message+"\n") ||
		!strings.Contains(out.Error.Message, "Could not automatically restore the old session "+name) ||
		!strings.Contains(out.Error.Message, "permission denied") ||
		!strings.Contains(out.Error.Message, "Open Archived sessions (the shelf) and choose Restore") {
		t.Fatalf("recreate = %d %s, want original refusal and shelf recovery instructions", w.Code, w.Body)
	}
	if old, ok := session.ReadMeta(name); !ok || !old.Archived {
		t.Fatalf("failed rollback should leave the old session on the shelf: %+v", old)
	}

	setMode(0o700)
	if w := callHandler(HandleRestoreSession, http.MethodPost, name, ""); w.Code != http.StatusOK {
		t.Fatalf("shelf restore = %d %s", w.Code, w.Body)
	}
	if old, ok := session.ReadMeta(name); !ok || old.Archived {
		t.Fatalf("shelf restore should return the old session to the active list: %+v", old)
	}
}
