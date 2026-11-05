package sessionx

import (
	"encoding/json"
	"net/http"
	"strconv"
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
	return claude.ParsePermissionMenu(tmuxx.CapturePane(session.TmuxName(name)))
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
// detail is the payload's "Tool · target" line. A target that cannot be compared (the payload
// names none) is let through, since the token still binds the answer; a target the dialog does
// not show, or a dialog whose body cannot be read, is refused as "target_mismatch".
func permissionKeys(name, choice, detail, cwd string) (keys []string, code string) {
	m, ok := readPermissionMenu(name)
	if !ok {
		return nil, "menu_unreadable"
	}
	// The token binds an answer to the payload, but claude can draw the next dialog before its
	// hook rewrites the payload (#3359). Only a dialog that shows the payload's target is the one
	// the card or button was drawn for.
	if m.CheckTarget(detail, cwd) == claude.TargetDiffers {
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
	detail, hasPerm := status.ReadPendingPermission(sid)
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
	keys, code := permissionKeys(name, req.Choice, detail, meta.Dir)
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
