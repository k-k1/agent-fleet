package codex

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

// stagedFlow registers a live flow whose staging home is a fresh directory, standing in for a
// `codex login --device-auth` still waiting for approval.
func stagedFlow(t *testing.T) (id, stage string) {
	t.Helper()
	stage = t.TempDir()
	f, err := agents.StartFlow(exec.Command("sleep", "60"))
	if err != nil {
		t.Fatal(err)
	}
	f.Dir = stage
	f.OnClose = func() { _ = os.RemoveAll(stage) }
	id = flows.Put(f)
	t.Cleanup(func() {
		if f := flows.Take(id); f != nil {
			f.Close()
		}
	})
	return id, stage
}

func poll(t *testing.T, id string) map[string]any {
	t.Helper()
	body, _ := json.Marshal(pollReq{FlowID: id})
	rec := httptest.NewRecorder()
	HandleDevicePoll(rec, httptest.NewRequest(http.MethodPost, "/connections/codex/device/poll", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("poll: %d %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got
}

// A re-authentication keeps the live login until the staged sign-in completes, then replaces
// it. The old login reads as signed in the whole time, which is exactly what must not close
// the flow early.
func TestDevicePollKeepsLiveLoginUntilStagedSignIn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	live := filepath.Join(codexHome(), "auth.json")
	if err := os.MkdirAll(codexHome(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(live, []byte(`{"old":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	prevG, prevS := loggedIn, loggedInAt
	loggedIn = func() bool { return true }
	loggedInAt = func(home string) bool {
		_, err := os.Stat(filepath.Join(home, "auth.json"))
		return err == nil
	}
	t.Cleanup(func() { loggedIn, loggedInAt = prevG, prevS })

	id, stage := stagedFlow(t)
	if got := poll(t, id); got["connected"] != false {
		t.Fatalf("poll before approval = %v, want connected false", got)
	}
	if b, _ := os.ReadFile(live); string(b) != `{"old":true}` {
		t.Fatalf("live auth.json changed before the sign-in completed: %s", b)
	}

	if err := os.WriteFile(filepath.Join(stage, "auth.json"), []byte(`{"new":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := poll(t, id); got["connected"] != true || got["stored"] != true {
		t.Fatalf("poll after approval = %v, want connected and stored", got)
	}
	if b, _ := os.ReadFile(live); string(b) != `{"new":true}` {
		t.Fatalf("live auth.json = %s, want the staged login", b)
	}
	if fi, err := os.Stat(live); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("live auth.json mode = %v, %v", fi, err)
	}
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Fatalf("staging home left behind: %v", err)
	}
	if got := poll(t, id); got["connected"] != true || got["stored"] != false {
		t.Fatalf("repeat poll = %v, want connected without stored", got)
	}
}

// An abandoned flow removes its staging home and leaves the live login as it was.
func TestAbandonedStagedFlowKeepsLiveLogin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	live := filepath.Join(codexHome(), "auth.json")
	_ = os.MkdirAll(codexHome(), 0o700)
	if err := os.WriteFile(live, []byte(`{"old":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	id, stage := stagedFlow(t)
	_ = os.WriteFile(filepath.Join(stage, "auth.json"), []byte(`{"half":true}`), 0o600)
	flows.Take(id).Close()
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Fatalf("staging home left behind: %v", err)
	}
	if b, _ := os.ReadFile(live); string(b) != `{"old":true}` {
		t.Fatalf("live auth.json = %s after an abandoned flow", b)
	}
}

// A poll of an unknown flow never reads the live login as this flow's success.
func TestDevicePollUnknownFlowIsNotConnected(t *testing.T) {
	prev := loggedIn
	loggedIn = func() bool { return true }
	t.Cleanup(func() { loggedIn = prev })
	if got := poll(t, "no-such-flow"); got["connected"] != false {
		t.Fatalf("poll = %v, want connected false", got)
	}
}

func TestInstallAuthWithoutStagedFile(t *testing.T) {
	if err := installAuth(t.TempDir(), t.TempDir()); err == nil {
		t.Fatal("installAuth succeeded with no staged auth.json")
	}
}

// The staging home is a real CODEX_HOME for the CLI: measured on codex 0.169.0, a login there
// writes only there. Skipped where codex is not installed.
func TestCodexLoginHonoursStagingHome(t *testing.T) {
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex not installed")
	}
	t.Setenv("HOME", t.TempDir())
	stage, err := newStagingHome()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("codex", "login", "--with-api-key")
	cmd.Env = append(os.Environ(), "CODEX_HOME="+stage)
	cmd.Stdin = bytes.NewReader([]byte("sk-test-not-a-key\n"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("codex login: %v %s", err, out)
	}
	if !loggedInAt(stage) {
		t.Fatal("staged home not signed in")
	}
	if _, err := os.Stat(filepath.Join(codexHome(), "auth.json")); !os.IsNotExist(err) {
		t.Fatalf("login under the staging home touched the live one: %v", err)
	}
}
