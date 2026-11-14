package sessionx

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

var permRespondMu sync.Map // session name -> *sync.Mutex

func permRespondLock(name string) (unlock func()) {
	mu, _ := permRespondMu.LoadOrStore(name, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	return mu.(*sync.Mutex).Unlock
}

// readPermissionMenu captures the session's pane and parses the permission dialog on it.
// A variable so tests can stand in a pane without tmux.
var readPermissionMenu = func(name string) (claude.PermissionMenu, bool) {
	return claude.ParsePermissionMenu(capturePermissionPane(name))
}

// capturePermissionPane is the session's pane text, "" when it could not be captured.
var capturePermissionPane = func(name string) string {
	return tmuxx.CapturePane(session.TmuxName(name))
}

// permDecided remembers, per session, the prompt (permissionToken) that has been given its one
// answer. Until the hook retires the payload the state still reads "permission", and a second
// answer would land on whatever came next: a digit on the next prompt, or an Escape that
// interrupts the turn the first answer resumed.
var permDecided sync.Map // session name -> token

// permissionToken identifies the prompt the pending payload describes: the payload file's
// mtime, rewritten by the hook for every new prompt. "" when nothing is pending. The card
// echoes it back, so an answer meant for one prompt can never be applied to the next (two
// tabs, or a card left on screen while the terminal moved on).
func permissionToken(sid string) string {
	at, ok := status.PendingPermissionAt(sid)
	if !ok {
		return ""
	}
	return strconv.FormatInt(at.UnixNano(), 36)
}

// permissionKeys resolves the keystroke that answers the dialog with choice, from the rows
// actually on screen (#3354). code is "" on success.
//
// Nothing is sent unless the dialog is on screen and readable. Allow and always are never
// guessed (a guessed position is how "Always allow" came to press "No" and "Deny" "Yes"), and
// neither is deny's Escape: with the dialog already closed it would interrupt the running turn.
// Escape is used only for a readable dialog that has no "No" row.
//
// detail is the payload's "Tool · target" line, and command the whole Bash command beside it
// ("" for another tool; a Bash payload without one is refused, #3521). A target that cannot be compared (the payload
// names none) is let through, since the token still binds the answer; a target the dialog does
// not show, or a dialog whose body cannot be read, is refused as "target_mismatch". So is a
// wrap-ambiguous dialog while a newer call has been announced by its PreToolUse hook
// (newerCallMayBeShown).
func permissionKeys(sid, name, choice, detail, command, cwd string) (keys []string, code string) {
	m, ok := readPermissionMenu(name)
	if !ok {
		return nil, "menu_unreadable"
	}
	// The token binds an answer to the payload, but claude can draw the next dialog before its
	// hook rewrites the payload (#3359). Only a dialog that shows the payload's target is the one
	// the card or button was drawn for.
	check, ambiguous := m.CheckTargetCommandWrap(detail, cwd, command)
	if check == claude.TargetDiffers || newerCallMayBeShown(sid, ambiguous) {
		return nil, "target_mismatch"
	}
	if k, found := m.Key(choice); found {
		return []string{k}, ""
	}
	if choice == claude.PermDeny {
		return []string{"Escape"}, ""
	}
	return nil, "choice_unavailable"
}

// newerCallMayBeShown reports whether the dialog on screen may belong to another call than the
// pending one. It matters only when the match is wrap-ambiguous: P wrapped by the terminal and a
// Q with a real newline at that wrap draw the same screen, so the screen alone matches Q for P
// (CheckTargetCommandWrap). An unambiguous match pins the dialog to P's exact text.
//
// Neither the payload nor the last-tool record can say which call a dialog belongs to: both come
// from one slot, which a sibling's PreToolUse can overwrite before the dialog's own Notification
// publishes it (the payload then names the sibling). The calls log can: Claude Code runs
// PreToolUse before its permission check (the hook's decision can allow, deny or ask), so every
// tracked call whose dialog can be on screen has started and not ended. The answer goes through
// only when that set is exactly the pending call, by tool_use id. Anything else (another call
// running, a background subagent's included; an unknown id; an unreadable log; nothing running)
// refuses, and the user answers in the terminal.
//
// Left open: a call whose PreToolUse hook never recorded it (the hook failed or timed out, and
// claude goes on to the dialog), and a call that started before the last turn end, which resets
// the log.
func newerCallMayBeShown(sid string, ambiguous bool) bool {
	if !ambiguous {
		return false
	}
	running, err := status.InFlightCalls(sid)
	if err != nil || len(running) != 1 {
		return true
	}
	pending, ok := status.ReadPendingPermissionToolUseID(sid)
	return !ok || running[0] != pending
}

// permissionChoices is what the poll reports beside pendingPermission, so the card offers only
// the buttons the dialog can honour. nil when the dialog is not (yet) drawn.
func permissionChoices(name string) []string {
	m, ok := readPermissionMenu(name)
	if !ok {
		return nil
	}
	return m.Choices()
}

// surfacePermissionPrompt adds what the card needs beside pendingPermission: the rows the dialog
// really offers (pendingPermissionChoices, read off the pane: "Always allow" is absent from some
// prompts) and the prompt's id (pendingPermissionId), which /permission-respond requires back.
//
// before is the token taken BEFORE the payload was read. The message, the choices and the id
// must describe one prompt: if it changed between that read and the pane capture, a card could
// show the old prompt's text with the new one's id, and its Allow would pass as a valid answer
// for a call the member never saw. Then all of it is dropped and the next poll shows the new
// prompt whole.
func surfacePermissionPrompt(pending map[string]any, sid, name, before string) {
	if _, ok := pending["pendingPermission"]; !ok {
		return
	}
	if ch := permissionChoices(name); ch != nil {
		pending["pendingPermissionChoices"] = ch
	}
	if tok := permissionToken(sid); tok != "" && tok == before {
		pending["pendingPermissionId"] = tok
		return
	}
	delete(pending, "pendingPermission")
	delete(pending, "pendingPermissionChoices")
}

// settleDeniedPermission retires the prompt a deny just answered. A refused tool interrupts
// claude's turn ("Interrupted · What should Claude do instead?") and no hook fires after it,
// so the payload and the "permission" state would otherwise outlive the dialog: the card stays
// up with live buttons, and the composer keeps refusing text. Measured on claude 2.1.318.
//
// wasRev is the status record read BEFORE the key was sent and token the prompt answered;
// any hook write since (a newer prompt, working, a turn end) wins and nothing is retired.
func settleDeniedPermission(sid, wasRev, token string) {
	at, ok := status.PendingPermissionAt(sid)
	if !ok || strconv.FormatInt(at.UnixNano(), 36) != token {
		return
	}
	status.RetirePermissionIf(sid, wasRev, at)
}

// sweepAnsweredPermission retires a permission prompt that closed without a hook (#3725).
// Claude Code can deny a call by itself (its built-in safety check draws the dialog even under
// bypass permissions, then closes it), and a denied tool fires no PostToolUse. If the turn goes
// on, nothing retires the payload until Stop, and the card keeps live buttons whose answers
// all fail with menu_unreadable.
//
// Retiring a live prompt is the dangerous direction: with the state gone, free text reaches
// the dialog and Enter answers it. So every one of these must hold:
//   - no question or plan payload. AskUserQuestion / ExitPlanMode raise their own permission
//     prompt, which no permtool hook precedes, so its payload carries a stale id; their menu
//     does not parse as a permission dialog either.
//   - the transcript holds the payload call's tool_result, written after the payload. The id is
//     the last PreToolUse's, which may be an earlier call's (no prompt, already finished) or a
//     parallel sibling's; an older result is that earlier call's.
//   - the pane was captured and shows no dialog. An empty capture is a tmux failure, not an
//     absent dialog.
//
// Left open: a sibling that finishes after the prompt was raised, while the real dialog is one
// ParsePermissionMenu cannot read. The sid lock and the record's Rev keep a newer prompt,
// written between these reads and the removal, from being retired. A turn still running is set
// back to working by the next poll's pane heal, as after sweepSettledState.
func sweepAnsweredPermission(sid, name string, lines [][]byte) {
	at, ok := status.PendingPermissionAt(sid)
	if !ok {
		return
	}
	st, _ := status.Read(sid)
	if st.State != "permission" {
		return
	}
	if _, ok := status.ReadPendingQuestion(sid); ok {
		return
	}
	if _, ok := status.ReadPendingPlan(sid); ok {
		return
	}
	id, ok := status.ReadPendingPermissionToolUseID(sid)
	if !ok {
		return
	}
	if res, ok := claude.ToolResultAt(lines, id); !ok || !res.After(at) {
		return
	}
	pane := capturePermissionPane(name)
	if strings.TrimSpace(pane) == "" {
		return
	}
	if _, up := claude.ParsePermissionMenu(pane); up {
		return
	}
	status.RetirePermissionIf(sid, st.Rev, at)
}

// permissionRespondResult is /permission-respond's success body.
type permissionRespondResult struct {
	Responded string `json:"responded"`
	Choice    string `json:"choice"`
}

// HandleSessionPermissionRespond (POST /sessions/{name}/permission-respond {choice}) answers a
// claude TUI tool-permission dialog with allow, always or deny, selecting the row by its
// label rather than by position.
func HandleSessionPermissionRespond(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	lease, ok := enterSessionGate(w, r, name)
	if !ok {
		return
	}
	defer lease.Release()
	var req struct {
		Choice string `json:"choice"`
		// Expect is the pendingPermissionId the card was drawn for. Required: the answer is
		// refused unless that prompt is still the one pending.
		Expect string `json:"expect"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_body", "invalid JSON body")
		return
	}
	switch req.Choice {
	case claude.PermAllow, claude.PermAlways, claude.PermDeny:
	default:
		httpx.WriteErr(w, http.StatusBadRequest, "bad_choice", "choice must be allow, always or deny")
		return
	}
	if req.Expect == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_expect", "expect (the pendingPermissionId) is required")
		return
	}
	meta, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	if meta.DriverKind() == session.DriverManaged {
		httpx.WriteErr(w, http.StatusNotImplemented, "permission_unsupported",
			"Managed セッションの承認はカードの許可／拒否で答えてください")
		return
	}
	// One answer per session at a time: two tabs pressing at once would otherwise both read
	// the same dialog, and the second digit would land on whatever came after it.
	unlock := permRespondLock(name)
	defer unlock()
	sid := session.UUID(meta.Dir, meta.Name)
	st, _ := status.Read(sid)
	// The pending-permission payload is what the card shows, and only claude's status hook
	// writes it, so it also keeps every other kind's pane out of reach of these digits.
	detail, command, hasPerm := status.ReadPendingPermissionCommand(sid)
	if effectiveModal(sid, st.State) != "permission" || !hasPerm {
		httpx.WriteErr(w, http.StatusConflict, "no_permission", "no pending permission prompt (already handled?)")
		return
	}
	token := permissionToken(sid)
	if token != req.Expect {
		httpx.WriteErr(w, http.StatusConflict, "permission_changed", "別の確認に変わりました。カードを確かめてからもう一度答えてください")
		return
	}
	if done, ok := permDecided.Load(name); ok && done == token {
		httpx.WriteErr(w, http.StatusConflict, "no_permission", "この確認にはもう回答済みです")
		return
	}
	pane, err := claudePane(name)
	if err != nil {
		httpx.WriteErr(w, http.StatusConflict, "not_running", err.Error())
		return
	}
	keys, code := permissionKeys(sid, name, req.Choice, detail, command, meta.Dir)
	// The hook and the terminal do not take permRespondLock, so the prompt can change while the
	// pane is captured; the menu just read may then be the next prompt's. Re-check after the
	// capture and send nothing on a change.
	if code == "" && permissionToken(sid) != token {
		httpx.WriteErr(w, http.StatusConflict, "permission_changed", "別の確認に変わりました。カードを確かめてからもう一度答えてください")
		return
	}
	switch code {
	case "menu_unreadable":
		httpx.WriteErr(w, http.StatusConflict, code, "許可の選択肢を画面から読み取れませんでした。ターミナルで答えてください")
		return
	case "target_mismatch":
		httpx.WriteErr(w, http.StatusConflict, code, "画面の確認がカードと別のものに変わっています。カードを確かめてからもう一度答えてください")
		return
	case "choice_unavailable":
		httpx.WriteErr(w, http.StatusConflict, code, "この確認には「常に許可」の選択肢がありません")
		return
	}
	if err := sendNamedKeys(pane, keys); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "tmux_failed", err.Error())
		return
	}
	permDecided.Store(name, token)
	if req.Choice == claude.PermDeny {
		settleDeniedPermission(sid, st.Rev, token)
	} else {
		markSessionWorking(name)
	}
	httpx.WriteJSON(w, http.StatusOK, permissionRespondResult{Responded: name, Choice: req.Choice})
}
