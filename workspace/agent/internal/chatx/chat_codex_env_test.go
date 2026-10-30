package chatx

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// The Agent exports codex.SQLiteHomeEnv for the member's sessions (#3013). The chat runs on
// its own CODEX_HOME and must not inherit that, or its threads land in the sessions' databases.
func TestChatCodexCmdPinsItsOwnSQLiteHome(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(codex.SQLiteHomeEnv, "/sessions-sqlite")
	cmd := chatCodexCmd(context.Background(), nil, "exec")
	want := filepath.Join(paths.AgentStateDir(), "chat-codex")
	var got []string
	for _, kv := range cmd.Env {
		if len(kv) > len(codex.SQLiteHomeEnv) && kv[:len(codex.SQLiteHomeEnv)+1] == codex.SQLiteHomeEnv+"=" {
			got = append(got, kv)
		}
	}
	if len(got) != 1 || got[0] != codex.SQLiteHomeEnv+"="+want {
		t.Fatalf("%s entries = %v, want exactly %s=%s", codex.SQLiteHomeEnv, got, codex.SQLiteHomeEnv, want)
	}
}
