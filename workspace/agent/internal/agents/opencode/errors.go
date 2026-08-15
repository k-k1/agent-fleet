package opencode

// Turn-level failures opencode records on an assistant message.
//
// opencode does NOT express a failed turn as an HTTP error or as a part: the blocking
// v1 `POST /session/{id}/message` answers **200** and the assistant message it returns
// (and stores) carries `error` NEXT TO an EMPTY `parts` array. Both layers used to miss
// this — the driver only checked the HTTP status, so a failed turn landed as
// TurnCompleted / idle, and the read layer dropped any message with no displayable
// part, so the mirror and get_session_output showed nothing at all. A session that hit
// e.g. an exhausted opencode Zen balance therefore went silently back to 入力待ち with
// zero tokens and no trace of why.
//
// 実測（1.18.5、opencode Zen の残高切れ）:
//
//	{"info":{…,"error":{"name":"APIError","data":{
//	   "statusCode":401,"isRetryable":false,
//	   "message":"Insufficient balance. Manage your billing here: …",
//	   "metadata":{"url":"https://opencode.ai/zen/v1/chat/completions"}}}},
//	 "parts":[]}
//
// Other observed names carry their detail in the same `data` envelope (ProviderAuthError
// adds providerID; MessageOutputLengthError carries no message at all), so the decoder
// stays permissive and falls back to the name.

import (
	"encoding/json"
	"io"
	"strconv"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// abortedErrorName is the error opencode stamps when a turn was interrupted on purpose
// (our own Interrupt → POST /abort, or the TUI user's Esc). It is a normal outcome —
// the driver already lands TurnCancelled and the partial answer is in the parts — so it
// must NOT be reported as a failure.
const abortedErrorName = "MessageAbortedError"

// messageError is the wire shape of an assistant message's `error` field.
//
// responseBody / responseHeaders / metadata は上限や残高の失敗にだけ現れる付随情報
// （workspaceid.go）。opencode 本体も同じ場所を読んで「どの枠が・いつ戻るか」を出して
// いる。載っていない版・載っていない失敗では素直に空になる（全部 optional）。
type messageError struct {
	Name string `json:"name"`
	Data struct {
		Message         string            `json:"message"`
		StatusCode      int               `json:"statusCode"`
		ProviderID      string            `json:"providerID"`
		ResponseBody    string            `json:"responseBody"`
		ResponseHeaders map[string]string `json:"responseHeaders"`
		Metadata        struct {
			Workspace string `json:"workspace"`
			LimitName string `json:"limitName"`
		} `json:"metadata"`
	} `json:"data"`
}

// label renders the short error identity ("APIError (HTTP 401)").
func (e messageError) label() string {
	name := strings.TrimSpace(e.Name)
	if name == "" {
		name = "error"
	}
	if e.Data.StatusCode > 0 {
		return name + " (HTTP " + strconv.Itoa(e.Data.StatusCode) + ")"
	}
	return name
}

// detail renders the human-facing message, falling back through the fields opencode
// actually fills for the error names that carry no `message`.
func (e messageError) detail() string {
	if m := strings.TrimSpace(e.Data.Message); m != "" {
		return m
	}
	if p := strings.TrimSpace(e.Data.ProviderID); p != "" {
		return "provider: " + p
	}
	return e.label()
}

// summary is the one-line form used where a turn is flattened to text: the operator's
// get_session_output, the chat-bridge body and the Agent log. Tagged so a reader (human
// or the operator assistant) can tell it apart from the agent's own prose.
func (e messageError) summary() string {
	d := e.detail()
	if d == e.label() {
		return "[error] " + d
	}
	return "[error] " + e.label() + ": " + d
}

// ok reports whether this is a failure worth surfacing (a deliberate abort is not).
func (e messageError) ok() bool {
	return strings.TrimSpace(e.Name) != "" && strings.TrimSpace(e.Name) != abortedErrorName
}

// part renders the failure as the ordered part the Console renders as an error block.
func (e messageError) part() transcript.Part {
	return transcript.Part{Kind: "error", Info: e.label(), Text: e.detail()}
}

// errorEnvelope accepts both carriers of the same field: the driver's response body
// wraps the message in {"info":…,"parts":…}, while the store keeps the info object
// itself as the message row.
type errorEnvelope struct {
	Error *messageError `json:"error"`
	Info  *struct {
		Error *messageError `json:"error"`
	} `json:"info"`
}

// pick returns the failure worth surfacing, or false for a clean turn / an abort.
func (w errorEnvelope) pick() (messageError, bool) {
	e := w.Error
	if e == nil && w.Info != nil {
		e = w.Info.Error
	}
	if e == nil || !e.ok() {
		return messageError{}, false
	}
	// 失敗はここに必ず集まるので、workspace id と枠情報の採取もここに置く
	// （workspaceid.go）。採れなければ何もしない。
	scanFailure(*e)
	return *e, true
}

// decodeMessageError pulls the failure out of a stored message row (read layer).
func decodeMessageError(data []byte) (messageError, bool) {
	var wire errorEnvelope
	if json.Unmarshal(data, &wire) != nil {
		return messageError{}, false
	}
	return wire.pick()
}

// decodeTurnError pulls the failure out of the assistant message a blocking /message
// call answers with. Streamed rather than buffered on purpose: a SUCCESSFUL turn's body
// carries the whole answer (every text and tool part) and can be large, while the only
// field that matters here is info.error.
func decodeTurnError(r io.Reader) (messageError, bool) {
	var wire errorEnvelope
	if json.NewDecoder(r).Decode(&wire) != nil {
		return messageError{}, false
	}
	return wire.pick()
}
