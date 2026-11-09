package mcpc

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
)

// The watch engine's connections start a member's stdio server with a per-definition
// environment; the session-side dialer keeps the Agent's (ADR 0133 §8).
func TestDialStdioNarrowEnvironment(t *testing.T) {
	t.Setenv("AF_SECRET_KEY", "store-key")
	for _, narrow := range []bool{false, true} {
		out := t.TempDir() + "/env"
		def := mcpreg.ServerDef{Name: "w", Origin: mcpreg.OriginUser, Transport: mcpreg.TransportStdio, Command: "sh",
			Args: []string{"-c", "env > $OUT"}, Env: map[string]string{"OUT": out}}
		c, err := dialStdio(def, narrow)
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-c.doneCh:
		case <-time.After(5 * time.Second):
			t.Fatal("child did not exit")
		}
		_ = c.close()
		b, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		if has := strings.Contains(string(b), "AF_SECRET_KEY=store-key"); has == narrow {
			t.Errorf("narrow=%v: Agent secret visible=%v", narrow, has)
		}
		if !strings.Contains(string(b), "OUT="+out) {
			t.Errorf("narrow=%v: the definition's own variable is missing", narrow)
		}
	}
}

func TestManagerCallServerNamesTheServer(t *testing.T) {
	m := NewManager(t.Context())
	if _, _, err := m.CallServer(t.Context(), "nope", "watch_poll", nil); err == nil {
		t.Error("a call to an unconnected server succeeded")
	}
}
