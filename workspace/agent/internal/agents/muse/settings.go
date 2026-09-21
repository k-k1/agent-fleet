package muse

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// settings.go owns `~/.config/muse/settings.json` — the clamps of ADR 0095 decision 6 that
// have no environment route, written before a host is ever spawned.
//
// Three properties make this more than a config write.
//
// **It is fail-close.** These keys are the safety mechanism: without them a session runs with
// eight subagents, workflows and the bundled foreign-reader skills all enabled. A failed write
// therefore refuses the start, unlike the MCP materialiser, which logs and carries on by
// design ("a session must still launch when its MCP config could not be updated"). The gate is
// inside the driver's own Resume rather than in StartManagedSession, because Resume is not the
// only way a child is spawned — the turn, answer, carried-session and bridge paths and the
// boot-time ReconcileManaged all reach it directly, and a restart must not start an unclamped
// host.
//
// **It preserves what it does not own.** A member's own `tui`, model defaults, telemetry keys
// and `mcp_servers` block survive an AF write. AF writes no `mcp_servers` of its own: decision
// 11 puts MCP servers on the wire in `session/start.config.mcpServers`, which is also why this
// writer needs no coordination with the MCP materialiser — with the server block gone, AF has
// exactly one writer for this file.
//
// **The file has another writer, and it reads before it locks.** Muse writes settings.json
// itself (`muse skills disable`, `/settings`), taking `flock(LOCK_EX)` on a `.settings.json.lock`
// sidecar, writing a temp file and renaming over the target. AF takes the same protocol.
// Measured, muse's own read happens ELEVEN SYSCALLS BEFORE its flock, so its update is a
// read-merge-write with an unprotected read and the lock alone cannot make the merge safe —
// hence the verify pass after the write, outside the lock. Also measured: muse blocks
// indefinitely on a held lock, so nothing slow happens while AF holds it.

// clamp is one owned setting: the path to it and the value AF requires.
type clamp struct {
	path []string
	// value is what AF writes. It is also what the verify pass compares against, so a clamp
	// whose value a member changed afterwards is detected rather than assumed.
	value any
}

// subagentCap is the fan-out ceiling. Left alone one session may run eight agents on a
// memory-constrained host shared with every other session in this container. The value is a
// cap, not a switch: `run.subagent_delegation_mode` below turns delegation off outright, and
// the cap is what remains meaningful if a deployment ever turns it back on.
const subagentCap = 2

// foreignReaderSkills are the bundled skills whose stated job is to read ANOTHER agent's
// transcripts, memory notes and MCP configuration — the directories workspace policy puts
// off-limits. Measured on 1.3.0-R3401.1 with `muse skills list`; the ADR's list also named
// `daemon`, `host-manager` and `slack-connector`, which do not exist on this version, and
// `read-session`, which does but reads MUSE's own store and says in its own description never
// to probe ~/.claude or ~/.codex — so it is not a foreign reader and is left alone.
//
// The activation key is a pack-qualified PATH, not a skill id (measured: `muse skills disable
// bundled:resume-claude` writes `bundled://muse-core/skills/resume-claude/SKILL.md`), so a
// rename or a move in a later release silently re-enables the skill. An entry for a skill that
// does not exist is accepted just as silently. Neither is detectable from the file, which is
// why the live test asks `muse skills list` whether each one really reports `off`.
var foreignReaderSkills = []string{"resume-claude", "resume-codex", "import", "migrate"}

// ownedClamps is everything AF writes into this file. Every path here was validated against
// the vendor's own offline checker (`muse config validate --plane defaults`), which names the
// exact failing member — a misspelling inside a strictly parsed section makes `muse serve`
// exit rc=3 before `initialize`, and a misspelling anywhere else is completely silent.
func ownedClamps() []clamp {
	cs := []clamp{
		// The file requires this or every muse command fails at startup, so it is written
		// even when AF is merging into a file that already exists without it.
		{path: []string{"schema_version"}, value: float64(1)},
		{path: []string{"agents", "execution_capacity"}, value: float64(subagentCap)},
		// A session could otherwise spawn up to 1,000 children over its lifetime.
		{path: []string{"run", "workflow_trigger_mode"}, value: "off"},
		// Measured: all six muse.subagent_* tools leave the model's tool list. It is also
		// what makes the token ledger exact, since subagent model calls never reach the wire.
		{path: []string{"run", "subagent_delegation_mode"}, value: "off"},
		// TWO keys, not one: either alone leaves the other half on. Belt to the braces of
		// MUSE_EXPERIMENTAL_FOREIGN_PERSONAL_CONTEXT_KILL in the child environment — without
		// both, a real turn assembles the member's own ~/.claude/CLAUDE.md and sends it to
		// Meta.
		{path: []string{"context", "foreign_personal_rules"}, value: false},
		{path: []string{"context", "foreign_personal_skills"}, value: false},
	}
	for _, s := range foreignReaderSkills {
		cs = append(cs, clamp{
			path:  []string{"skills", "activation", "bundled", bundledSkillKey(s)},
			value: "off",
		})
	}
	return cs
}

func bundledSkillKey(id string) string {
	return "bundled://muse-core/skills/" + id + "/SKILL.md"
}

func settingsPath() string { return filepath.Join(ConfigHome(), "settings.json") }
func settingsLock() string { return filepath.Join(ConfigHome(), ".settings.json.lock") }

// settingsMu serialises AF's own writers. The lock file coordinates with muse; this
// coordinates with AF, where several sessions resume at once on boot and would otherwise each
// read-merge-write the same file.
var settingsMu sync.Mutex

// EnsureClamps writes the owned settings and verifies they survived. It is idempotent and
// cheap enough to run on every Resume, which is the point — a member who edits the file, or a
// `muse` command that rewrites it, must not leave the next session unclamped.
func EnsureClamps() error {
	settingsMu.Lock()
	defer settingsMu.Unlock()

	// One retry, and only one. A single lost update is ordinary — muse reads before it locks,
	// so a concurrent `muse skills disable` can drop AF's merge — but a second failure means
	// something is rewriting the file continuously, and starting an unclamped host because we
	// could not win a race is exactly what fail-close exists to prevent.
	for attempt := 0; attempt < 2; attempt++ {
		if err := writeClampsLocked(); err != nil {
			return err
		}
		missing, err := verifyClamps()
		if err != nil {
			return err
		}
		if len(missing) == 0 {
			return nil
		}
		if attempt == 1 {
			return fmt.Errorf("Muse Code の設定（%s）に安全策を書き込めませんでした: %v",
				settingsPath(), missing)
		}
	}
	return nil
}

// writeClampsLocked performs the read-merge-rename under muse's own lock protocol.
func writeClampsLocked() error {
	if err := os.MkdirAll(ConfigHome(), 0o700); err != nil {
		return fmt.Errorf("%s を作成できません: %w", ConfigHome(), err)
	}
	lock, err := os.OpenFile(settingsLock(), os.O_RDWR|os.O_CREATE, 0o666)
	if err != nil {
		return fmt.Errorf("設定ロックを開けません: %w", err)
	}
	defer lock.Close()
	// Blocking, with no LOCK_NB — the same call muse makes. Failing fast here would mean
	// refusing to launch a session because the member happened to be running a muse command.
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("設定ロックを取得できません: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	cur, err := readSettings()
	if err != nil {
		return err
	}
	for _, c := range ownedClamps() {
		setPath(cur, c.path, c.value)
	}
	return writeSettingsAtomic(cur)
}

// readSettings loads the file as a generic map so every key AF does not own round-trips
// untouched. A missing file is an empty document, not an error; a CORRUPT one is an error,
// because overwriting a file we could not parse would silently discard a member's own
// configuration.
func readSettings() (map[string]any, error) {
	b, err := os.ReadFile(settingsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, fmt.Errorf("%s を読めません: %w", settingsPath(), err)
	}
	if len(b) == 0 {
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s が壊れています（AF は上書きしません）: %w", settingsPath(), err)
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

// writeSettingsAtomic is muse's own sequence: a temp file in the same directory, fsync,
// rename over the target, fsync of the directory. The directory fsync is what makes the
// rename survive a power loss; without it the file can be there with no name.
func writeSettingsAtomic(m map[string]any) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')

	tmp, err := os.CreateTemp(ConfigHome(), ".settings.json.af-*")
	if err != nil {
		return fmt.Errorf("設定の一時ファイルを作れません: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op once the rename succeeded

	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, settingsPath()); err != nil {
		return fmt.Errorf("設定を書き込めません: %w", err)
	}
	dir, err := os.Open(ConfigHome())
	if err != nil {
		return nil // the rename landed; an unsyncable directory is not worth refusing a launch
	}
	defer dir.Close()
	_ = dir.Sync()
	return nil
}

// verifyClamps re-reads the file and reports the owned settings that are not what AF wrote.
// It runs OUTSIDE the lock, because that is the window it exists to detect: muse reads the
// file before taking the lock, so a concurrent muse write can silently drop AF's merge.
func verifyClamps() ([]string, error) {
	cur, err := readSettings()
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, c := range ownedClamps() {
		got, ok := getPath(cur, c.path)
		if !ok || !sameJSON(got, c.value) {
			missing = append(missing, pathString(c.path))
		}
	}
	return missing, nil
}

// setPath writes value at path, creating the intermediate objects. A non-object sitting where
// a section belongs is REPLACED: muse would refuse to load the file otherwise, and a member
// with a string where `agents` goes has a broken file either way.
func setPath(m map[string]any, path []string, value any) {
	for i, key := range path {
		if i == len(path)-1 {
			m[key] = value
			return
		}
		next, ok := m[key].(map[string]any)
		if !ok {
			next = map[string]any{}
			m[key] = next
		}
		m = next
	}
}

func getPath(m map[string]any, path []string) (any, bool) {
	var cur any = m
	for _, key := range path {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = obj[key]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// sameJSON compares two decoded JSON values. Numbers round-trip as float64, so a clamp's value
// is declared that way and this stays a plain comparison rather than a reflect walk.
func sameJSON(a, b any) bool { return a == b }

func pathString(path []string) string {
	out := ""
	for i, p := range path {
		if i > 0 {
			out += "."
		}
		out += p
	}
	return out
}
