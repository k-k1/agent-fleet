package agents

import "sync"

// Shared store for CLI-advertised commands (docs/log/50 v2). On ACP kinds (cursor and the
// like) the CLI itself streams the skill/command list through session/update's
// available_commands_update, and that is the only complete source for such a kind: builtin
// skills plus global plus project, all in one (measured on cursor). The driver's onNotify
// publishes here on every arrival and the REST skills handler reads it. In-memory is
// enough: the list arrives again on every runtime spawn/load, and while it is missing (just
// after an agent restart, say) the FS fallback covers it. The driver has no duty to clear
// it either — slightly stale beats empty.

// AdvertisedCommand is one entry of a CLI-advertised command/skill list.
type AdvertisedCommand struct {
	Name        string // invocation name (a leading "/" is normalised away)
	Description string
}

var advCommands sync.Map // session name → []AdvertisedCommand

// PublishCommands records the latest CLI-advertised command list for a session.
// Called from driver notify loops — MUST stay cheap (single map store).
func PublishCommands(session string, cmds []AdvertisedCommand) {
	if session == "" {
		return
	}
	advCommands.Store(session, cmds)
}

// AdvertisedCommands returns the last-published list for a session (nil if none).
func AdvertisedCommands(session string) []AdvertisedCommand {
	v, ok := advCommands.Load(session)
	if !ok {
		return nil
	}
	cmds, _ := v.([]AdvertisedCommand)
	return cmds
}
