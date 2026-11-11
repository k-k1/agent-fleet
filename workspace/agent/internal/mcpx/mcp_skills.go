package mcpx

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// mcpToolSkillRestore is the session-side face of GET /agents/skills/archive and POST
// /agents/skills/archive/restore (#3864): how a member gets back a skill the curator archived.
// One tool with two modes rather than two, because every advertised description is a fixed cost
// on every session's first turn. It is offered to every session (the member asks whichever agent
// they are talking to), and the Agent only ever acts on the member's own skills directories.
const mcpToolSkillRestore = "skill_restore"

type skillRestoreArgs struct {
	Archived string `json:"archived"`
	Root     string `json:"root"`
}

func skillRestoreSchema() map[string]any {
	return map[string]any{
		"name": mcpToolSkillRestore,
		"description": "Agent Fleet: bring back an agent-written skill (afa-*) that the skill curator archived as unused. " +
			"With no arguments it lists the archived skills (archived, name, archived_at, root). " +
			"To restore one, pass archived and root exactly as listed. It refuses when a live skill of that name exists, " +
			"and a restored skill counts as just used, so the curator does not archive it again at once. " +
			"Restore only what your user asked for.",
		"inputSchema": map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"archived": map[string]any{"type": "string", "description": "The listed archived entry to restore (omit to list)"},
				"root":     map[string]any{"type": "string", "description": "The listed root of that entry"},
			},
		},
	}
}

func mcpSkillRestore(id json.RawMessage, raw json.RawMessage) []byte {
	// A write on the member's skills: the session surface only, never the assistant surface.
	if !selfReportOnly() {
		return mcpToolErr(id, "skill_restore is only available from a session's Agent Fleet server")
	}
	var a skillRestoreArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return mcpToolErr(id, "cannot read skill_restore arguments: "+err.Error())
		}
	}
	a.Archived, a.Root = strings.TrimSpace(a.Archived), strings.TrimSpace(a.Root)
	if a.Archived == "" {
		out, err := agentGET("/agents/skills/archive")
		if err != nil {
			return mcpToolErr(id, "listing archived skills failed: "+err.Error())
		}
		return mcpTextResult(id, out)
	}
	if a.Root == "" {
		return mcpToolErr(id, "pass root together with archived, both as listed")
	}
	body, _ := json.Marshal(a)
	out, err := agentDo(http.MethodPost, "/agents/skills/archive/restore", body)
	if err != nil {
		var he *agentHTTPError
		if errors.As(err, &he) {
			switch he.code() {
			case "skill_exists":
				return mcpToolErr(id, "a live skill with that name already exists; nothing was restored. Ask your user whether to rename or remove it first")
			case "not_archived", "bad_root":
				return mcpToolErr(id, "that entry is not in the archive; list again and pass archived and root as listed")
			}
		}
		return mcpToolErr(id, "restoring the skill failed: "+err.Error())
	}
	return mcpTextResult(id, out)
}

func skillTools() []toolSpec {
	return []toolSpec{{
		name: mcpToolSkillRestore, session: skillRestoreSchema, ownArgs: true,
		handle: handler(func(req mcpReq, p toolCallParams, a *noArgs) []byte {
			return mcpSkillRestore(req.ID, p.Args)
		}),
	}}
}

// mcpStdioSkillTools is the skill family's session list.
func mcpStdioSkillTools() []map[string]any {
	return sessionTools(skillTools())
}
