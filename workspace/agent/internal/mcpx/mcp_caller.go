package mcpx

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// mcpCallerSIDArg is the reserved tools/call argument the AF opencode plugin
// (workspace/opencode-plugin/agent-fleet-caller.js) stamps with the calling opencode
// session id — the one per-call identity a Managed opencode session can deliver, since its
// sessions share one MCP child per directory (#989). The id is not an AF identity by itself:
// mcpCallerSession only uses it as a key into AF's own slot → opencode-session mapping, in
// line with mcpSourceSession's rule that a native id must never decide ownership.
const mcpCallerSIDArg = "_af_caller_sid"

// mcpCallerSIDMetaKey is where opencode 2 itself puts the calling session id, in the tools/call
// `params._meta` (measured: 2.0.28 sends it on every call, 1.19.0 never does).
// opencode 2 cannot use the stamp: its client drops arguments a closed inputSchema does not
// declare after the plugin hook has run, and af's schemas are closed. The model cannot write
// `_meta`, so unlike the stamped argument it needs no plugin to be believed.
const mcpCallerSIDMetaKey = "ai.opencode/sessionID"

// mcpCallerSID is mcpCallerSIDArg's value for the tools/call being handled, "" outside one.
// One variable is enough because RunStdio dispatches serially — but only the dispatch goroutine
// may read it: the tools/list watcher runs beside a call (a generate_image call holds the loop
// for minutes), so everything that builds the list resolves its owner through
// mcpListOwningSession, which never looks here.
var mcpCallerSID string

// mcpCallerFromMeta says mcpCallerSID came from params._meta rather than the stamped argument.
// Same lifetime and same single-reader rule as mcpCallerSID.
var mcpCallerFromMeta bool

// mcpOwningSession names the session this MCP process serves.
//
// AF_SESSION_NAME is the contract. Each launch route delivers it through the one per-session
// channel its host has (docs/log/117):
//
//   - TERMINAL: the tmux launch env (session_tmux.go), inherited by the CLI and forwarded by
//     codex (mcpreg's extraEnvVars).
//   - MANAGED codex: the THREAD config (mcpreg.CodexThreadServers). The daemon is shared, but
//     thread/start, thread/fork and a resume into a daemon that has not loaded the thread all
//     apply it; a resume into the daemon that already holds the thread keeps the one it started
//     with, which is the same name.
//   - MANAGED copilot / kiro / muse host / cursor: one vendor process per session, started with
//     it in its environment. cursor scrubs its MCP children's environment, so its config file
//     carries a `${env:…}` reference (mcpreg.cursorStdioEnv).
//   - MANAGED muse: the session/start wire's `env`, since muse scrubs too.
//   - lcpp: added to the builtin's definition in-process (agents/lcpp injectSessionName).
//
// MANAGED OPENCODE has no per-process channel: its MCP config is global and the child is
// spawned per project directory, so sessions sharing a worktree share one child (measured
// 1.18.32, contract_mcp_identity_test.go). Its channel is per CALL instead: the AF plugin
// stamps opencode's session id on each af tools/call (#989, mcpCallerSession).
//
// Everything else — an opencode whose plugin was removed, a route whose registry could not
// be read — lands in the cwd fallback below. The fallback matches the working folder, which
// is not unique — several sessions routinely share one worktree. Narrowing by liveness
// resolves the common shape (the caller is running; the others in that folder are stopped)
// and is only ever allowed to REMOVE an ambiguity: unless it lands on exactly one session,
// the original candidate set stands and the caller gets the ambiguity error.
func mcpOwningSession() (string, error) { return mcpResolveOwner(true) }

// mcpListOwningSession is mcpOwningSession for code that builds tools/list. A list carries no
// caller stamp and is shared by every session on this child, and the watcher derives it on its
// own goroutine while a call may be in flight; reading that call's stamp here would both race and
// hand the whole child the list of whichever session happened to be calling.
func mcpListOwningSession() (string, error) { return mcpResolveOwner(false) }

func mcpResolveOwner(useStamp bool) (string, error) {
	if session.ValidName(mcpSourceSession) {
		return mcpSourceSession, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("引き継ぎ元セッションを特定できません: 作業ディレクトリを取得できません")
	}
	if useStamp {
		if name, ok := mcpCallerSession(cwd); ok {
			return name, nil
		}
	}
	var found []string
	for _, m := range session.ListMetas() {
		if m.Archived || !mcpRunsIn(m, cwd) || !session.ValidName(m.Name) {
			continue
		}
		found = append(found, m.Name)
	}
	if len(found) > 1 {
		if alive, ok := mcpAliveSessions(found); ok && len(alive) == 1 {
			found = alive
		}
	}
	switch {
	case len(found) == 1:
		return found[0], nil
	case len(found) > 1:
		sort.Strings(found)
		return "", fmt.Errorf("引き継ぎ元セッションを特定できません: 同じ作業フォルダに複数のセッションがあります（%s）",
			strings.Join(found, ", "))
	}
	return "", fmt.Errorf("引き継ぎ元セッションを特定できません: AF_SESSION_NAME がありません")
}

// takeCallerSID removes mcpCallerSIDArg from a tools/call's arguments and returns what is
// left together with its value. Arguments without it come back byte-for-byte, so the
// common path costs one decode and changes nothing.
func takeCallerSID(args json.RawMessage) (json.RawMessage, string) {
	var m map[string]json.RawMessage
	if len(args) == 0 || json.Unmarshal(args, &m) != nil {
		return args, ""
	}
	raw, ok := m[mcpCallerSIDArg]
	if !ok {
		return args, ""
	}
	delete(m, mcpCallerSIDArg)
	rest, err := json.Marshal(m)
	if err != nil {
		return args, ""
	}
	var sid string
	_ = json.Unmarshal(raw, &sid) // a non-string is nobody's session id: dropped, not trusted
	return rest, sid
}

// metaCallerSID is the session id opencode 2 sent in the call's `_meta`, "" when absent or not
// a string (a non-string is nobody's session id, same as in takeCallerSID).
func metaCallerSID(meta map[string]json.RawMessage) string {
	var sid string
	_ = json.Unmarshal(meta[mcpCallerSIDMetaKey], &sid)
	return sid
}

// mcpCallerSession resolves the opencode session id stamped on this call to the AF session
// that owns it. The id arrives as a tool argument, so the model can write one too (the plugin
// overwrites it, but a member can remove the plugin): it is only ever a key. It must equal
// the id AF itself mapped to exactly one live opencode session in this MCP child's folder —
// the child is spawned per directory, so that is the set that can be calling. Anything else
// (no stamp, no match, a stopped match, a failed probe) reports ok=false and leaves the
// decision to the cwd fallback, which is what the call would have got without the plugin.
func mcpCallerSession(cwd string) (string, bool) {
	if mcpCallerSID == "" || !mcpCallerTrusted() {
		return "", false
	}
	var found []string
	for _, m := range session.ListMetas() {
		if m.Archived || m.Kind != session.KindOpencode || !session.ValidName(m.Name) {
			continue
		}
		if !mcpRunsIn(m, cwd) {
			continue
		}
		if opencode.SlotSessionID(m) == mcpCallerSID {
			found = append(found, m.Name)
		}
	}
	if len(found) != 1 {
		return "", false
	}
	st, err := agentSessionStatus(found[0])
	if err != nil || !st.Alive {
		return "", false
	}
	return found[0], true
}

// mcpCallerStampTrusted reports whether a stamp on a call can only have come from the plugin.
// The plugin overwrites the reserved argument on af's tools, but it recognises them by the
// rotated `af_<8 hex>` server name: under the legacy bare `af` name, or with the plugin removed
// or altered, the model's own value would arrive untouched and naming another session's id would be enough
// to act as it. The model shares this uid, so this is not a wall against a determined one — it
// stops the reserved argument from being a door that a plain tool call opens.
//
// The key checked is the one THIS child was registered under (mcpreg.AFServerKeyEnv, written
// into af's opencode entry only), not the Agent's current name: a copy of af's command
// registered as bare `af` — a project opencode.json, a stale entry — has no such key, and a
// child of a daemon adopted across an Agent restart keeps the rotated key it was started with.
//
// What it cannot stop is a config written to impersonate: whoever writes an MCP entry chooses
// its child's whole environment, AF_MCP_SERVER_KEY included — and could just as well set
// AF_SESSION_NAME, which mcpOwningSession believes before any of this. Identity delivered
// through the environment is only as good as the config that delivers it; this check closes the
// cases where nobody chose to lie.
func mcpCallerStampTrusted() bool {
	return mcpCallerKeyRotated() && opencode.CallerPluginCurrent()
}

func mcpCallerKeyRotated() bool {
	return mcpreg.IsRotatedAFServerName(os.Getenv(mcpreg.AFServerKeyEnv))
}

// mcpCallerTrusted is mcpCallerStampTrusted for the call being handled: an id from `_meta` was
// written by opencode, not by the model or a plugin, so only the rotated key is asked of it.
func mcpCallerTrusted() bool {
	if mcpCallerFromMeta {
		return mcpCallerKeyRotated()
	}
	return mcpCallerStampTrusted()
}

// mcpStampedFolderSessions is who can be calling this MCP child when the answer only arrives
// per call: the live sessions in this folder, returned only when every one of them is Managed
// opencode with a conversation AF has mapped — the calls whose stamp mcpCallerSession can match
// — and the stamp can be trusted at all. A folder that also holds another kind, or whose
// liveness could not be read, returns nil: a list-time offer there would promise a call that
// cannot tell its caller apart.
func mcpStampedFolderSessions() []string {
	if !mcpCallerStampTrusted() {
		return nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}
	var names []string
	metas := map[string]session.Meta{}
	for _, m := range session.ListMetas() {
		if m.Archived || !session.ValidName(m.Name) || !mcpRunsIn(m, cwd) {
			continue
		}
		names = append(names, m.Name)
		metas[m.Name] = m
	}
	alive, ok := mcpAliveSessions(names)
	if !ok || len(alive) == 0 {
		return nil
	}
	// Each live session needs its own mapped conversation: an unmapped one, or two mapped to the
	// same id, is a caller no stamp can single out, and every call would be refused.
	seen := map[string]bool{}
	for _, n := range alive {
		m := metas[n]
		id := opencode.SlotSessionID(m)
		if m.Kind != session.KindOpencode || id == "" || seen[id] {
			return nil
		}
		seen[id] = true
	}
	sort.Strings(alive)
	return alive
}

// mcpRunsIn reports whether session m's agent — and so the MCP child it spawns — runs in cwd:
// its working copy, or the subdir it was launched into (Meta.CWD).
func mcpRunsIn(m session.Meta, cwd string) bool { return m.Dir == cwd || m.CWD() == cwd }

// mcpAliveSessions keeps the names the Agent reports as alive. ok is false when any
// probe failed — a partial answer must not narrow anything, since the missing one
// could be the caller itself and dropping it would attribute the handoff to somebody
// else's session.
func mcpAliveSessions(names []string) (alive []string, ok bool) {
	for _, n := range names {
		st, err := agentSessionStatus(n)
		if err != nil {
			return nil, false
		}
		if st.Alive {
			alive = append(alive, n)
		}
	}
	return alive, true
}
