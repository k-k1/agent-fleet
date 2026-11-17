// Package memotidy is the Agent's side of a queue's tidy run (ADR 0137 §8): POST /memos/tidy
// makes the one deny-all model call that proposes operations over a queue's pending messages, and
// answers with the parsed operations. The Control Plane validates them again and is the one that
// applies them; nothing here writes a memo.
//
// The messages are a third party's text. They reach the model only as JSON inside a prompt with no
// tool, no MCP server, no file and no network (chatx.OneShotDenyAll), and nothing of the reply is
// returned or logged on an error: a proposal that is not exactly the agreed shape is dropped whole.
package memotidy

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// handlerBudget bounds the whole handler, the deny-all probe included (a variable for tests). The Control Plane waits
// longer, so its own timeout never fires first.
var handlerBudget = 120 * time.Second

// denyAll is the one model call. A variable so that no test reaches a model.
var denyAll = chatx.OneShotDenyAllResult

// busy admits one tidy call at a time in the workspace: the call holds a CLI process, and a
// second one would only queue behind the first until the Control Plane gave up on it.
var busy sync.Mutex

// Response is the 200 answer.
type Response struct {
	Run        string      `json:"run"`
	Operations []Operation `json:"operations"`
	Kind       string      `json:"kind"`
	Model      string      `json:"model"`
}

// HandleTidy is POST /memos/tidy.
func HandleTidy(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), handlerBudget)
	defer cancel()

	req, msg := decodeRequest(r)
	if req == nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_tidy_request", msg)
		return
	}
	if !busy.TryLock() {
		httpx.WriteErr(w, http.StatusConflict, "tidy_busy", "another tidy call is running in this workspace")
		return
	}
	defer busy.Unlock()

	prompt, err := buildPrompt(req)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "tidy_failed", "the prompt could not be built")
		return
	}
	ctx = usagex.WithTag(ctx, usagex.Tag{Feature: usagex.FeatureMemoTidy, Trigger: usagex.TriggerAuto})
	res, err := denyAll(ctx, usagex.FeatureMemoTidy, chatx.OneShotProse, persona, prompt)
	if err != nil {
		writeCallErr(w, ctx, req.Run, err)
		return
	}
	ops, err := parseReply(res.Reply, req.Limits)
	if err != nil {
		log.Printf("memo tidy: run %s: the reply was refused (malformed)", req.Run)
		httpx.WriteErr(w, http.StatusUnprocessableEntity, "tidy_malformed", errMalformed.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, Response{Run: req.Run, Operations: ops, Kind: res.Kind, Model: res.Model})
}

// writeCallErr maps a failed call. The order matters: a credential policy refusal keeps its own
// code, the deadline wins over a probe that was cut by it, and only ErrDenyAllUnavailable says
// that nothing reached a model. Every other failure, including a real run whose output did not
// show the deny-all posture, is a failed run whose reply was discarded. The messages are fixed
// text: nothing the call printed is passed on.
func writeCallErr(w http.ResponseWriter, ctx context.Context, run string, err error) {
	if status, code, ok := chatx.PolicyRefusal(err); ok {
		log.Printf("memo tidy: run %s: refused by the credential policy (%s)", run, code)
		httpx.WriteErr(w, status, code, "the credential policy does not allow this call")
		return
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		log.Printf("memo tidy: run %s: timed out", run)
		httpx.WriteErr(w, http.StatusGatewayTimeout, "tidy_timeout", "the tidy call did not finish in time")
		return
	}
	if errors.Is(err, chatx.ErrDenyAllUnavailable) {
		log.Printf("memo tidy: run %s: no deny-all backend is available", run)
		httpx.WriteErr(w, http.StatusServiceUnavailable, "deny_all_unavailable", "no backend that can run without tools is available")
		return
	}
	log.Printf("memo tidy: run %s: the call failed: %v", run, err)
	httpx.WriteErr(w, http.StatusBadGateway, "tidy_failed", "the tidy call failed and its reply was discarded")
}
