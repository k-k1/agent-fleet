package sessionx

import (
	"encoding/json"
	"net/http"
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

// permissionKeys resolves the keystroke that answers the dialog with choice, from the rows
// actually on screen (#3354). code is "" on success.
//
// Deny never needs the menu: Escape cancels the dialog whatever its rows are, which is why it
// is the fallback when the menu cannot be read or has no "No" row. Allow and always have no
// such key, so they are refused rather than guessed: a guessed position is exactly how
// "Always allow" came to press "No" and "Deny" came to press "Yes".
func permissionKeys(name, choice string) (keys []string, code string) {
	m, ok := readPermissionMenu(name)
	if k, found := m.Key(choice); ok && found {
		return []string{k}, ""
	}
	if choice == claude.PermDeny {
		return []string{"Escape"}, ""
	}
	if !ok {
		return nil, "menu_unreadable"
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
	_, hasPerm := status.ReadPendingPermission(sid)
	if effectiveModal(sid, st.State) != "permission" || !hasPerm {
		httpx.WriteErr(w, http.StatusConflict, "no_permission", "no pending permission prompt (already handled?)")
		return
	}
	pane, err := claudePane(name)
	if err != nil {
		httpx.WriteErr(w, http.StatusConflict, "not_running", err.Error())
		return
	}
	keys, code := permissionKeys(name, req.Choice)
	switch code {
	case "menu_unreadable":
		httpx.WriteErr(w, http.StatusConflict, code, "許可の選択肢を画面から読み取れませんでした。ターミナルで答えてください")
		return
	case "choice_unavailable":
		httpx.WriteErr(w, http.StatusConflict, code, "この確認には「常に許可」の選択肢がありません")
		return
	}
	if err := sendNamedKeys(pane, keys); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "tmux_failed", err.Error())
		return
	}
	if req.Choice != claude.PermDeny {
		markSessionWorking(name)
	}
	httpx.WriteJSON(w, http.StatusOK, permissionRespondResult{Responded: name, Choice: req.Choice})
}
