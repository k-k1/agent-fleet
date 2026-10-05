package memoryx

// AF-owned agent memory (ADR 0108) — the REST side the af MCP tools call. The session parameter
// names the caller: its kind is the author recorded with a write, and its working copy decides
// which project scope it sees.

import (
	"errors"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// agentMemSecretWire is the refusal of a write whose text matched the secret scan.
type agentMemSecretWire struct {
	Error    agentMemSecretWireErr `json:"error"`
	Findings []memorySecretFinding `json:"findings"`
}

type agentMemSecretWireErr struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func agentMemWriteErr(w http.ResponseWriter, err error) {
	var se *agentMemSecretErr
	if errors.As(err, &se) {
		httpx.WriteJSON(w, http.StatusUnprocessableEntity, agentMemSecretWire{
			Error:    agentMemSecretWireErr{Code: errCodeMemorySecretDetected, Message: se.Error()},
			Findings: se.Findings,
		})
		return
	}
	var ue *memoryUserErr
	if errors.As(err, &ue) {
		httpx.WriteErr(w, ue.Status, ue.Code, ue.Msg)
		return
	}
	// OS and git errors carry paths, and a path carries a memory name nobody has scanned yet
	// (a hand-made file named like a token): neither the response nor the log gets more than
	// the kind of failure.
	msg := agentMemErrKind(err)
	log.Printf("agent memory: %s", msg)
	httpx.WriteErr(w, http.StatusInternalServerError, errCodeMemorySnapshotFailed, msg)
}

// agentMemErrKind names a failure without any text that came from the filesystem or git.
func agentMemErrKind(err error) string {
	switch {
	case errors.Is(err, agentMemErrSymlink):
		return agentMemErrSymlink.Error()
	case errors.Is(err, agentMemErrTooLarge):
		return agentMemErrTooLarge.Error()
	case errors.Is(err, fs.ErrPermission):
		return "agent memory file or directory is not accessible (permission denied)"
	case errors.Is(err, fs.ErrNotExist):
		return "agent memory file or directory vanished during the operation"
	}
	return "agent memory operation failed (internal error)"
}

// AgentMemoryEnabled is the user's switch for the tools' routes (ADR 0108, ui-prefs agentMemory,
// default off), wired by uiprefs. A nil hook reads as off. Hiding the tools is not enough on its
// own: these routes answer anything holding AGENT_TOKEN, which every session's own MCP server
// does. The Console's routes (changes, diff, revert) are not gated: the switch governs what
// sessions may do, and the member can still review and undo what was written while it was on.
var AgentMemoryEnabled func() bool

// agentMemRequireEnabled answers 403 and returns false while the switch is off.
func agentMemRequireEnabled(w http.ResponseWriter) bool {
	if AgentMemoryEnabled == nil || !AgentMemoryEnabled() {
		httpx.WriteErr(w, http.StatusForbidden, errCodeMemoryDisabled,
			"Agent Fleet memory is turned off for sessions in Settings > Agent memory")
		return false
	}
	return true
}

func agentMemCallerFrom(w http.ResponseWriter, name string) (agentMemCaller, bool) {
	if !agentMemRequireEnabled(w) {
		return agentMemCaller{}, false
	}
	c, err := agentMemResolveCaller(name)
	if err != nil {
		agentMemWriteErr(w, err)
		return c, false
	}
	return c, true
}

// HandleAgentMemoryIndex lists the memories the calling session sees, without bodies, ranked and
// cut to the optional `budget` (bytes; see agentMemClampBudget).
func HandleAgentMemoryIndex(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	c, ok := agentMemCallerFrom(w, q.Get("session"))
	if !ok {
		return
	}
	budget, _ := strconv.Atoi(q.Get("budget"))
	out, err := agentMemListIndex(c, budget)
	if err != nil {
		agentMemWriteErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// agentMemSearchWire is the answer to a search.
type agentMemSearchWire struct {
	Project *agentMemProject `json:"project"`
	Hits    []agentMemHit    `json:"hits"`
}

// HandleAgentMemorySearch finds memories by text.
func HandleAgentMemorySearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	c, ok := agentMemCallerFrom(w, q.Get("session"))
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	hits, err := agentMemSearch(c, q.Get("q"), limit)
	if err != nil {
		agentMemWriteErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, agentMemSearchWire{Project: c.Project, Hits: hits})
}

// HandleAgentMemoryRead returns one memory with its body.
func HandleAgentMemoryRead(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	c, ok := agentMemCallerFrom(w, q.Get("session"))
	if !ok {
		return
	}
	e, err := agentMemRead(c, q.Get("scope"), q.Get("name"))
	if err != nil {
		agentMemWriteErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, e)
}

// HandleAgentMemorySave creates or updates a memory and publishes it at once.
func HandleAgentMemorySave(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, agentMemMaxBody+16<<10)
	var req agentMemSaveReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	c, ok := agentMemCallerFrom(w, req.Session)
	if !ok {
		return
	}
	res, err := agentMemSave(c, req, time.Now())
	if err != nil {
		agentMemWriteErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

// HandleAgentMemoryForget removes a memory from what is published.
func HandleAgentMemoryForget(w http.ResponseWriter, r *http.Request) {
	var req agentMemForgetReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	c, ok := agentMemCallerFrom(w, req.Session)
	if !ok {
		return
	}
	res, err := agentMemForget(c, req, time.Now())
	if err != nil {
		agentMemWriteErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

// HandleAgentMemoryChanges lists the published changes for the Console (ADR 0108 decision 8).
func HandleAgentMemoryChanges(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	out, err := agentMemListChanges(limit)
	if err != nil {
		agentMemWriteErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// HandleAgentMemoryChangeDiff is one change's diff, scanned before it leaves the Agent.
func HandleAgentMemoryChangeDiff(w http.ResponseWriter, r *http.Request) {
	out, err := agentMemChangeDiff(r.URL.Query().Get("commit"))
	if err != nil {
		agentMemWriteErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// HandleAgentMemoryRevert undoes one change, or forgets the memory as that change left it.
func HandleAgentMemoryRevert(w http.ResponseWriter, r *http.Request) {
	var req agentMemRevertReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	res, err := agentMemRevert(req, time.Now())
	if err != nil {
		agentMemWriteErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

// HandleAgentMemoryClaudeSources lists the claude projects that have memory to import. Like the
// preview it works while the switch is off: reading claude's own files changes nothing.
func HandleAgentMemoryClaudeSources(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, agentMemImportList())
}

// HandleAgentMemoryClaudePreview says what an import of one claude project would do.
func HandleAgentMemoryClaudePreview(w http.ResponseWriter, r *http.Request) {
	pv, err := agentMemImportPreviewFor(r.URL.Query().Get("slug"))
	if err != nil {
		agentMemWriteErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, pv)
}

// HandleAgentMemoryClaudeApply imports the listed items. Unlike the two reads above it needs
// the switch on: it writes to the store the sessions read.
func HandleAgentMemoryClaudeApply(w http.ResponseWriter, r *http.Request) {
	if !agentMemRequireEnabled(w) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var req agentMemImportReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	res, err := agentMemImportApply(req, time.Now())
	if err != nil {
		agentMemWriteErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}
