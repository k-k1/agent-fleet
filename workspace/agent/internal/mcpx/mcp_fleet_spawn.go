package mcpx

import (
	"fmt"
	"strconv"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// stoppedChildExpiryClause is the part of create_session's limits sentence that promises a
// stopped child frees its slot by itself. Only true while auto-archive is on: with the user's
// setting at "off" the promise would have a caller wait for a slot that never comes back.
func stoppedChildExpiryClause() string {
	if _, ok := session.StoppedTTL(); ok {
		return ", or when one you left stopped expires"
	}
	return ""
}

// mcpStdioFleetSpawnTools — the ten session-steering tools, advertised only under
// `--self-report --fleet-spawn` (ADR 0073). Written out here rather than reused from the
// operator's list for the reasons in mcpStdioFleetObserveTools: the operator's text is Japanese
// and points at tools a session does not get. The handlers are shared.
//
// The descriptions carry the refusals (children only, three at a time, no grandchildren, no
// shells) because a limit a model learns by hitting it costs a whole turn, and because these
// are the sentences that make the difference between "start a session for every thought" and
// "start one when the work genuinely splits".
func mcpStdioFleetSpawnTools() []map[string]any {
	return []map[string]any{
		{
			"name": "create_session",
			"description": "Agent Fleet: start a new session in this workspace and give it a task. It runs in " +
				"parallel with you, with its own agent, model and context. " +
				"Use it when work genuinely splits: a long independent subtask, a second repository, something " +
				"that would fill your context and starve the rest. Do not use it for work you could just do - " +
				"a session holds a whole agent's memory on a host shared with every other session. " +
				"Tell the user you are starting one, and what for. " +
				"It starts in a NEW worktree by default, so it never shares your working copy; pass " +
				"worktree=false only for a directory nobody is working in. " +
				"To run a child in YOUR OWN working copy on purpose (e.g. a reviewer on what you are implementing), " +
				"pass worktree=false and allow_shared_working_copy=true: it shares your checkout, index and branch, so tell it " +
				"(and keep to) no checkout/switch/stash/branch changes and stage and commit explicitly by path. " +
				"It works only while you are running in a canonical path (no symlink in the one you were started with; " +
				"naming the target dir through an alias is fine) and only for your own copy: another session's working " +
				"copy cannot be shared this way, and if the refusal repeats with the flag, do not retry - use a worktree. " +
				"Limits: at most " + strconv.Itoa(session.SpawnChildLimit()) + " children at a time (a slot frees when the user deletes or " +
				"archives that child" + stoppedChildExpiryClause() + " - list_child_sessions shows what " +
				"you have), a session you started cannot start its own, and shell sessions cannot be started " +
				"from here. " +
				"You are NOT told when it finishes: poll get_session_status, or leave report_back on and it " +
				"sends you one message when it is done. " +
				"Children outlive you, so before your last turn list them and tell your user which ones you " +
				"left and what state they are in - only they can delete one. " +
				"Use list_repos for dir and list_models for model and effort - do not guess either. " +
				"The child's permission mode is not yours to choose: it follows the user's per-kind default in Settings > Agents.",
			"inputSchema": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"dir":                       map[string]any{"type": "string", "description": "Working directory (a path from list_repos, or your own). Default: home"},
					"title":                     map[string]any{"type": "string", "description": "Short display name saying what the task is (optional)"},
					"kind":                      map[string]any{"type": "string", "description": "Agent kind: claude (default) | codex | opencode | agy | copilot | cursor | kiro | lcpp | muse. shell/ssm are refused"},
					"model":                     map[string]any{"type": "string", "description": "Model id from list_models for that kind (optional)"},
					"effort":                    map[string]any{"type": "string", "description": "Reasoning effort (optional; default: the model's defaultEffort). Needs model: use one of that model's efforts from list_models; anything else, or an effort without model, is refused. opencode and kiro list none: their value is not checked here, and a wrong one fails the child's first turn. agy and cursor fold effort into the model id instead"},
					"initial_prompt":            map[string]any{"type": "string", "description": "The task, delivered as the child's first instruction. Write it for someone with none of your context: what to do, where, what done looks like. If it has to wait for a message (from you or another session), tell it to end its turn while it waits: a message to a busy session can be held until its turn ends, and waiting inside a tool (a sleep loop, a blocking wait) keeps that turn from ending"},
					"worktree":                  map[string]any{"type": "boolean", "description": "Start in a new worktree off dir. Default TRUE from a session - two agents in one working copy corrupt each other's work"},
					"allow_shared_working_copy": map[string]any{"type": "boolean", "description": "Opt in to starting the child in YOUR OWN working copy while you are running in it (default false). Needs worktree=false, dir = your working copy (an alias spelling of it is fine) and that you were started in a canonical path (no symlink); any other directory in use is still refused, and a repeated refusal means it cannot work - do not retry. You and the child then share one checkout, index and branch: no checkout/switch/stash/branch changes, stage and commit explicitly by path"},
					"branch":                    map[string]any{"type": "string", "description": "Base branch for the worktree (optional; default: current HEAD)"},
					"new_branch":                map[string]any{"type": "string", "description": "Name of the branch to create in the worktree (optional; default: generated)"},
					"subdir":                    map[string]any{"type": "string", "description": "Relative path inside the working copy to start in, e.g. console (optional)"},
					"report_back":               map[string]any{"type": "boolean", "description": "Ask the child to send you one message when it finishes. Default true. Turn it off when you will read the result in the Console instead"},
					"spend_cap_usd":             map[string]any{"type": "number", "description": "The child's own spend budget in USD, an estimate at list price (optional; default: the user's default budget; 0 = none). Past it the child stops after its turn. It is not charged to yours"},
				},
			},
		},
		{
			"name": "list_child_sessions",
			"description": "Agent Fleet: list the sessions YOU started, each one's state and when its last turn " +
				"ended, and how many child slots you have left. " +
				"Call it whenever you need a child's name: create_session hands it out once and a compaction takes " +
				"it away, leaving stop_session / resume_session / get_session_output nothing to name. Call it " +
				"before your last turn too, to tell your user which children you left and how they stand. " +
				"lastTurnEndAt separates a child that FINISHED from one that never started - idle means both. It " +
				"is absent while a child is mid-turn and after a restart. " +
				"Archived sessions are not listed and hold no slot.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			"name": "list_repos",
			"description": "Agent Fleet: list the working copies in this workspace (path, repo, branch). " +
				"Call it before create_session to pick dir - a path you remember may not exist here.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			"name": "list_models",
			"description": "Agent Fleet: list the model ids currently selectable for one agent kind. " +
				"Call it before passing model to create_session and use an id from the answer: the list already " +
				"has the user's excluded models removed, and a create with an excluded or invented id is refused. " +
				"For opencode the same model can appear on two billing routes - prefer the one listed first.",
			"inputSchema": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"kind": map[string]any{"type": "string", "description": "claude | codex | opencode | agy | copilot | cursor | kiro | lcpp | muse"},
				},
				"required": []string{"kind"},
			},
		},
		{
			"name": "get_agent_usage",
			"description": "Agent Fleet: report each agent CLI's subscription usage and rate limits (claude / codex / agy / muse). " +
				"pct is how much of a window is used, resetsAt when it lifts, authed=false means that CLI is not " +
				"signed in. " +
				"Call it before handing a long task to a kind you do not normally use - starting a session on a " +
				"CLI that is out of quota or signed out wastes the launch. Not the same as get_session_usage, " +
				"which is about context, not the plan.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			"name": "get_session_output",
			"description": "Agent Fleet: read the recent terminal output of a session YOU started. " +
				"Only your own children; for another session use peek_session_output where it is offered. " +
				"Call it when get_session_status says a child is idle or stopped and you need to know what came " +
				"of the task. Long output is clipped to the tail; omit since to continue from where you last read.",
			"inputSchema": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"name":  map[string]any{"type": "string", "minLength": 1, "description": "Child session name (from create_session's result)"},
					"since": map[string]any{"type": "integer", "description": "Read from this output offset instead of continuing (optional; 0 = from the start)"},
				},
				"required": []string{"name"},
			},
		},
		{
			"name": "stop_session",
			"description": "Agent Fleet: stop a session you started, now. Only your own children. " +
				"The stop is resumable - conversation and working copy stay, and resume_session brings it back - " +
				"but whatever it was doing is cut off mid-turn. " +
				"Use it for a child that is looping or working on something you no longer want. If it is simply " +
				"busy with work you still want, use stop_session_after_turn instead. " +
				"Stopping does not free one of your child slots straight away - the user deleting or archiving " +
				"the child does, and a child left stopped expires on its own after a while.",
			"inputSchema": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"name": map[string]any{"type": "string", "minLength": 1, "description": "Child session name"},
				},
				"required": []string{"name"},
			},
		},
		{
			"name": "stop_session_after_turn",
			"description": "Agent Fleet: book a child to stop once it finishes what it is doing. Only your own children. " +
				"Unlike stop_session nothing is cut off: the turn ends, its report goes out, then the session folds " +
				"away. This is the one to use when you fanned work out and want each child to release its memory as " +
				"it finishes. " +
				"It does not stop a session that is waiting on a question or an approval, and sending it new work " +
				"releases the booking. on=false cancels it.",
			"inputSchema": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"name": map[string]any{"type": "string", "minLength": 1, "description": "Child session name"},
					"on":   map[string]any{"type": "boolean", "description": "true = book the stop (default), false = cancel it"},
				},
				"required": []string{"name"},
			},
		},
		{
			"name": "resume_session",
			"description": "Agent Fleet: restart a stopped child, keeping its conversation. Only your own children. " +
				"Call it when you need more from a child you stopped. It only brings the session back - to give it " +
				"work, send it a peer message afterwards. A running session is left alone.",
			"inputSchema": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"name": map[string]any{"type": "string", "minLength": 1, "description": "Child session name"},
				},
				"required": []string{"name"},
			},
		},
		{
			"name": "rename_child_session",
			"description": "Agent Fleet: retitle a child you started, so its name still says what it is doing. " +
				"Only your own children, and it is refused once the user has renamed that child themselves.",
			"inputSchema": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"name":  map[string]any{"type": "string", "minLength": 1, "description": "Child session name"},
					"title": map[string]any{"type": "string", "minLength": 1, "description": "The new display title"},
				},
				"required": []string{"name", "title"},
			},
		},
	}
}

// sessionDriveAllowed authorizes a tool that ACTS ON a named session: reading its output,
// stopping it, resuming it. The operator may drive anything; a session may drive only what it
// started itself (ADR 0073 decision 4).
//
// The predicate is BOTH origin=session AND a matching parent, deliberately narrower than the
// one guarding recursion. A fork of a child keeps the lineage but is origin=handoff — a person
// made it in the Console, and nothing about being the fork source makes it the parent's to
// stop. Everything else — peers, the user's own sessions, another session's children — is
// simply not the caller's business.
//
// The advertised tool set is still the first boundary; this is the second, because unlike the
// stage 1 read tools these name a target.
func sessionDriveAllowed(name string) error {
	// The operator surface is unrestricted — including read-only (--write absent), which still
	// advertises get_session_output. The test is which SURFACE this server is, not which
	// capability it holds: keying off writeEnabled() would put a read-only assistant through
	// the child check and then refuse it for having no session of its own.
	if !selfReportOnly() {
		return nil
	}
	self, err := mcpOwningSession()
	if err != nil {
		return err
	}
	m, ok := session.ReadMeta(name)
	if !ok {
		return fmt.Errorf("セッション %q が見つかりません", name)
	}
	if session.OriginOf(m) != session.OriginSession || m.OriginSession != self {
		return fmt.Errorf("セッション %q はこのセッションが起こした子ではないので操作できません。"+
			"自分が create_session で起こしたセッションだけを止める・再開する・出力を読むことができます", name)
	}
	// An archived child is the user's decision to fold it away, and it has no row in the
	// active list. Resuming one would put a live agent on the host that nobody can see, which
	// is also how archiving would become a way around "no deleting, no archiving" (ADR 0073
	// decision 13): archive from the Console, revive from here.
	if m.Archived {
		return fmt.Errorf("セッション %q は利用者がアーカイブ済みなので操作できません。"+
			"戻すかどうかは利用者が Console で決めます", name)
	}
	return nil
}
