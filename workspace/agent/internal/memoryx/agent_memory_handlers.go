package memoryx

// AF-owned agent memory (ADR 0108) — the REST side the af MCP tools call. The session parameter
// names the caller: its kind is the author recorded with a write, and its working copy decides
// which project scope it sees.

import (
	"errors"
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
	memoryWriteErr(w, err, errCodeMemorySnapshotFailed)
}

func agentMemCallerFrom(w http.ResponseWriter, name string) (agentMemCaller, bool) {
	c, err := agentMemResolveCaller(name)
	if err != nil {
		agentMemWriteErr(w, err)
		return c, false
	}
	return c, true
}

// HandleAgentMemoryIndex lists the memories the calling session sees, without bodies.
func HandleAgentMemoryIndex(w http.ResponseWriter, r *http.Request) {
	c, ok := agentMemCallerFrom(w, r.URL.Query().Get("session"))
	if !ok {
		return
	}
	out, err := agentMemListIndex(c)
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
