package chatx

// MCP wiring for the assistant chat (docs/log/48 P2).
//
// An assistant's Integrations widened from "built-in integration id" to "server id in the
// effective registry" (the three built-in ids are still the same strings, so existing
// assistants need no migration). This file resolves such an id against the registry
// definition and hands it to each provider's configuration shape. Serialising a definition
// into a CLI's config form lives in internal/mcpreg/attach.go; this file only decides which
// server goes where, and how.
//
// The contract here is that secrets (env / header values) never reach argv: claude,
// opencode and agy read them from a 0600 config file, codex gets them via the environment.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// mcpServersFor resolves this conversation's attached registry servers for the
// backend kind actually driving the turn. Unknown, disabled, not-ready and
// out-of-scope ids drop out silently — the pre-registry contract for a builtin whose
// connection was missing, now applied to every origin.
func (c *ChatConversation) mcpServersFor(kind string) []mcpreg.ServerDef {
	if len(c.Integrations) == 0 {
		return nil
	}
	avail, err := mcpreg.ForAssistant(kind)
	if err != nil {
		return nil
	}
	var out []mcpreg.ServerDef
	for _, id := range c.Integrations {
		if d, ok := avail[id]; ok {
			out = append(out, d)
		}
	}
	// Name order, not selection order: several providers write these into a config
	// file every turn, and a stable file avoids pointless rewrites (and pointless
	// diffs when a user looks at one).
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// afServerArgs returns the argv tail that launches the local Agent Fleet stdio MCP
// server for this conversation's tool grant, or false when the grant is "none".
// --conv hands the server its owning conversation id so create_session /
// send_to_session auto-attach report_to (docs/log/30) — the report link is tool-side
// plumbing, never something the model has to carry.
func (c *ChatConversation) afServerArgs() ([]string, bool) {
	if !c.afToolsEnabled() {
		return nil, false
	}
	if c.AFWriteEnabled() {
		return []string{"mcp-stdio", "--write", "--conv", c.ID}, true
	}
	return []string{"mcp-stdio"}, true
}

// --- claude ----------------------------------------------------------------------

// chatMCPConfigDir holds the per-conversation --mcp-config files. Outside repos, and
// wiped with the container's home only — same lifecycle as the rest of the chat state.
func chatMCPConfigDir() string {
	return filepath.Join(homeDir(), ".config", "agent-fleet", "chat-mcp")
}

func chatMCPConfigPath(convID string) string {
	return filepath.Join(chatMCPConfigDir(), convID+".json")
}

// removeChatMCPConfig drops a deleted conversation's config file. Best-effort: a
// missing file is the normal case (only claude chats write one).
func removeChatMCPConfig(convID string) {
	if paths.ValidIDSegment(convID) {
		_ = os.Remove(chatMCPConfigPath(convID))
	}
}

// mcpConfigArgs builds the chat's --mcp-config, scoped strictly to this claude
// (--strict-mcp-config: no global/project MCP leaks in, and none of these leak out to
// the interactive sessions). docs/log/19 Q1, docs/log/25 Phase 1, docs/log/48 §7.
//   - the local Agent Fleet stdio server ("af"), when the assistant grants af tools;
//     with af_write it also advertises the write tools (the advertised set is the gate).
//   - one server per attached registry entry: the builtins launch via
//     `workspace-agent mcp-run <id>` (which injects the user's stored key at spawn, so
//     no credential is written anywhere), user/tenant registrations carry their own
//     command or URL.
//
// The config goes into a 0600 FILE rather than inline in argv, because a registered
// server's env values and headers are secrets and argv is readable for the whole uid
// (docs/log/48 §5.1 puts materialized secrets in 0600 files). claude accepts either form
// for --mcp-config. If the file cannot be written we fall back to inline JSON with
// the secret-bearing servers dropped — the af tools keep working, and no credential
// is smuggled into argv by a failure path.
func (c *ChatConversation) mcpConfigArgs() []string {
	exe := paths.ExePath()
	servers := mcpreg.ClaudeServers(c.mcpServersFor(session.KindClaude))
	if sargs, ok := c.afServerArgs(); ok {
		servers["af"] = map[string]any{"type": "stdio", "command": exe, "args": anyArgs(sargs)}
	}
	if len(servers) == 0 {
		return nil
	}
	cfg, err := json.Marshal(map[string]any{"mcpServers": servers})
	if err != nil {
		return nil
	}
	if path, err := writeChatMCPConfig(c.ID, cfg); err == nil {
		return []string{"--mcp-config", path, "--strict-mcp-config"}
	}
	safe := mcpreg.ClaudeServers(secretFree(c.mcpServersFor(session.KindClaude)))
	if sargs, ok := c.afServerArgs(); ok {
		safe["af"] = map[string]any{"type": "stdio", "command": exe, "args": anyArgs(sargs)}
	}
	if len(safe) == 0 {
		return nil
	}
	cfg, err = json.Marshal(map[string]any{"mcpServers": safe})
	if err != nil {
		return nil
	}
	return []string{"--mcp-config", string(cfg), "--strict-mcp-config"}
}

func writeChatMCPConfig(convID string, cfg []byte) (string, error) {
	// The id becomes a filename, so re-check it here rather than trusting that every
	// caller reached us through a handler that validated it.
	if !paths.ValidIDSegment(convID) {
		return "", errors.New("invalid conversation id")
	}
	if err := os.MkdirAll(chatMCPConfigDir(), 0o700); err != nil {
		return "", err
	}
	path := chatMCPConfigPath(convID)
	tmp := path + ".af-tmp"
	if err := os.WriteFile(tmp, append(cfg, '\n'), 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return path, nil
}

// secretFree keeps only the definitions that carry no credential, for the argv
// fallback above.
func secretFree(defs []mcpreg.ServerDef) []mcpreg.ServerDef {
	var out []mcpreg.ServerDef
	for _, d := range defs {
		if !mcpreg.HasSecrets(d) {
			out = append(out, d)
		}
	}
	return out
}

func anyArgs(v []string) []any {
	out := make([]any, len(v))
	for i, s := range v {
		out[i] = s
	}
	return out
}
