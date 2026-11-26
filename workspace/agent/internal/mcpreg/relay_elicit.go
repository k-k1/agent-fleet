package mcpreg

// Form elicitation through the relay (ADR 0146 decision 6, #5174).
//
// For a definition flagged Elicitation "relay", the relay reads the tool calls instead of passing
// bytes: it declares form elicitation on the CLI's behalf, and when a result is input_required
// (MCP 2026-07-28 multi round-trip requests) it holds the CLI's request, asks a person through the
// pending list below, and retries the call with the server's requestState and the person's
// answer. The CLI only ever sees a complete result.
//
// Rules that must not erode:
//
//   - Only a person's answer is ever sent as "accept". The relay itself answers nothing but
//     "decline" (the time limit, or a person declining) and never invents a choice. A request
//     that carries a requestState or inputResponses of its own, under any spelling a JSON decoder
//     might match (Go's matches member names case-insensitively), is refused: anything holding
//     the relay key could otherwise send its own accept.
//   - A request is refused rather than guessed at when its JSON is ambiguous (two members whose
//     names fold to the same one) or a tool call is not spelled as MCP spells it: the server may
//     read it differently from the relay.
//   - The arguments are passed on byte for byte: a changed argument voids the server's approval,
//     and the person is shown exactly what the server will run.
//   - Only tools/call of the 2026-07-28 era (a protocolVersion in _meta) is held. Other requests
//     pass as bytes, so the relay never declares a capability for a result it does not handle.
//   - On a held call the CLI only gets notifications and the response to the call, under its own
//     id. Any other response, a server request, or an unreadable event ends the call with an error.
//   - A retry goes out only on the sign-in the call was first sent with.
//   - Nothing about the arguments or the answer's content is logged.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// ElicitationRelay is the ServerDef.Elicitation value that has the relay answer form elicitation.
const ElicitationRelay = "relay"

var (
	// elicitWait is how long a held call waits for a person before it is declined. Measured on
	// 2026-11-26 (#5174): claude 2.1.332 ends an HTTP tool call after 60 s unless progress arrives,
	// and passed 660 s with progress every 10 s; opencode 2.4.1 passed 330 s. A var so a test can
	// shorten it.
	elicitWait = 10 * time.Minute
	// elicitKeepalive is how often a held call that streams to the CLI sends a progress
	// notification (or an SSE comment when the CLI gave no token). Well under claude's 60 s.
	elicitKeepalive = 15 * time.Second
)

const (
	// elicitMaxRounds bounds how often one call may come back input_required: a server that
	// keeps asking is answered with an error, not held forever.
	elicitMaxRounds = 3
	// elicitMaxPending bounds the calls held at once, so an agent in a loop cannot pile up
	// questions (and memory) without end.
	elicitMaxPending = 16
	// elicitMaxMessage and elicitMaxText cut the server's texts kept for the Console.
	elicitMaxMessage = 8 << 10
	elicitMaxText    = 256
	// ElicitAnswerMaxBytes bounds a form's largest answer, JSON-encoded: a form whose answer could
	// not fit the answer route's request limit is refused before it is put to anyone, since the
	// person could then only decline.
	ElicitAnswerMaxBytes = 32 << 10
)

var (
	// elicitMaxHeldOne and elicitMaxHeld bound what held calls keep for the Console (arguments,
	// messages, choices), one and all together. The list is copied for every reader, so a few
	// calls with large arguments would otherwise multiply into the Agent's memory limit. Vars so a
	// test can lower them.
	elicitMaxHeldOne = 1 << 20
	elicitMaxHeld    = 4 << 20
)

// digestHeaders describe a body's bytes; the relay drops them wherever it may change the body.
var digestHeaders = []string{"Content-Digest", "Repr-Digest", "Digest", "Content-Md5"}

// elicitCall is one 2026-07-28-era tools/call, with form elicitation declared in its _meta.
type elicitCall struct {
	id     json.RawMessage
	tool   string
	top    map[string]json.RawMessage
	params map[string]json.RawMessage
	// progressToken is the CLI's own token, nil when it sent none.
	progressToken json.RawMessage
	body          []byte
	// session is the session the call is attributed to (callerSession), "" when none.
	session string
}

type flaggedVerdict int

const (
	flaggedPass   flaggedVerdict = iota // relayed as bytes
	flaggedElicit                       // a 2026-07-28 tools/call, held on input_required
	flaggedRefuse                       // never reaches the server
)

// readFlagged sorts a POST body for a flagged definition. For flaggedElicit the call carries the
// body to send, with form elicitation as the declared capability (replaced, not merged: the relay
// answers form elicitation only, so a "url" capability would invite a request nobody here can
// answer). For flaggedRefuse the string says why.
func readFlagged(body []byte) (elicitCall, flaggedVerdict, string) {
	// Anything but exactly one JSON object is refused rather than passed: a lenient decoder on
	// the server (one that skips a byte-order mark, or takes a batch) could still read it as a
	// call the relay never looked at.
	keys, top, ok := objectMembers(body)
	if !ok {
		return elicitCall{}, flaggedRefuse, "a request that is not exactly one JSON object"
	}
	if foldDuplicate(keys) {
		return elicitCall{}, flaggedRefuse, "a request with two members of the same name"
	}
	pk, praw, hasParams := foldGet(keys, top, "params")
	var pkeys []string
	var params map[string]json.RawMessage
	if hasParams && firstByte(praw) == '{' {
		if pkeys, params, ok = objectMembers(praw); !ok || foldDuplicate(pkeys) {
			return elicitCall{}, flaggedRefuse, "a request with two parameters of the same name"
		}
		for _, k := range pkeys {
			if strings.EqualFold(k, "requestState") || strings.EqualFold(k, "inputResponses") {
				return elicitCall{}, flaggedRefuse, "a request that answers a confirmation itself; for this server Agent Fleet sends those only with a person's answer from the Console"
			}
		}
	}
	mk, mraw, _ := foldGet(keys, top, "method")
	method, isStr := jsonString(mraw)
	if !isStr || method != "tools/call" {
		return elicitCall{}, flaggedPass, ""
	}
	malformed := "a tool call that is not spelled the way MCP spells it"
	ik, id, hasID := foldGet(keys, top, "id")
	if mk != "method" || pk != "params" || params == nil || !hasID || ik != "id" {
		return elicitCall{}, flaggedRefuse, malformed
	}
	c := elicitCall{id: id, top: top, params: params}
	nk, nraw, _ := foldGet(pkeys, params, "name")
	if c.tool, isStr = jsonString(nraw); !isStr || nk != "name" {
		return elicitCall{}, flaggedRefuse, malformed
	}
	metaKey, metaRaw, hasMeta := foldGet(pkeys, params, "_meta")
	if !hasMeta {
		return elicitCall{}, flaggedPass, "" // an older-era call
	}
	mkeys, meta, ok := objectMembers(metaRaw)
	if metaKey != "_meta" || !ok || foldDuplicate(mkeys) {
		return elicitCall{}, flaggedRefuse, malformed
	}
	vk, _, hasVersion := foldGet(mkeys, meta, metaProtocolVersion)
	if !hasVersion {
		return elicitCall{}, flaggedPass, "" // an older-era call
	}
	if vk != metaProtocolVersion {
		return elicitCall{}, flaggedRefuse, malformed
	}
	caps := map[string]json.RawMessage{}
	if ck, capsRaw, hasCaps := foldGet(mkeys, meta, metaClientCaps); hasCaps {
		var ckeys []string
		if ckeys, caps, ok = objectMembers(capsRaw); ck != metaClientCaps || !ok || foldDuplicate(ckeys) {
			return elicitCall{}, flaggedRefuse, malformed
		}
		for _, k := range ckeys {
			if strings.EqualFold(k, "elicitation") {
				delete(caps, k)
			}
		}
	}
	caps["elicitation"] = json.RawMessage(`{"form":{}}`)
	meta[metaClientCaps] = rawObject(caps)
	params["_meta"] = rawObject(meta)
	top["params"] = rawObject(params)
	c.progressToken = meta["progressToken"]
	c.body = rawObject(top)
	return c, flaggedElicit, ""
}

// objectMembers reads raw as exactly one JSON object: its member names in order and their values
// as they are written.
func objectMembers(raw []byte) ([]string, map[string]json.RawMessage, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, nil, false
	}
	var keys []string
	vals := map[string]json.RawMessage{}
	for dec.More() {
		tok, err := dec.Token()
		k, isStr := tok.(string)
		if err != nil || !isStr {
			return nil, nil, false
		}
		var v json.RawMessage
		if dec.Decode(&v) != nil {
			return nil, nil, false
		}
		if _, dup := vals[k]; dup {
			return []string{k, k}, nil, true // reported by foldDuplicate
		}
		keys = append(keys, k)
		vals[k] = v
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return nil, nil, false
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, nil, false
	}
	return keys, vals, true
}

// foldDuplicate reports two names a case-insensitive JSON decoder would take for one (Go's
// encoding/json folds with Unicode simple folding, as strings.EqualFold does).
func foldDuplicate(keys []string) bool {
	seen := make(map[string]bool, len(keys))
	for _, k := range keys {
		f := foldKey(k)
		if seen[f] {
			return true
		}
		seen[f] = true
	}
	return false
}

// foldKey maps every rune to the smallest rune of its Unicode simple-folding orbit, so two names
// have the same key exactly when strings.EqualFold says they are equal.
func foldKey(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		m := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			if f < m {
				m = f
			}
		}
		b.WriteRune(m)
	}
	return b.String()
}

// canonical reports whether every member whose name folds to one of names is spelled exactly so:
// a decoder that folds names must not find a member the relay did not read.
func canonical(keys []string, names ...string) bool {
	for _, k := range keys {
		for _, n := range names {
			if strings.EqualFold(k, n) && k != n {
				return false
			}
		}
	}
	return true
}

// foldGet finds the member whose name folds to name, with the name as it is written.
func foldGet(keys []string, vals map[string]json.RawMessage, name string) (string, json.RawMessage, bool) {
	for _, k := range keys {
		if strings.EqualFold(k, name) {
			return k, vals[k], true
		}
	}
	return "", nil, false
}

func firstByte(raw []byte) byte {
	t := bytes.TrimLeft(raw, " \t\r\n")
	if len(t) == 0 {
		return 0
	}
	return t[0]
}

// jsonString decodes raw when it is a JSON string (null is not one).
func jsonString(raw json.RawMessage) (string, bool) {
	if firstByte(raw) != '"' {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// relayRefuse answers a request the relay will not pass on with a JSON-RPC error.
func relayRefuse(w http.ResponseWriter, why string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusBadRequest)
	_, _ = w.Write(mustJSON(map[string]any{"jsonrpc": "2.0", "id": nil,
		"error": map[string]any{"code": -32600, "message": "Agent Fleet: the relay refused " + why}}))
}

// contentEncoded reports a body that is not sent as it is: any coding other than identity in any
// of the header's lines.
func contentEncoded(h http.Header) bool {
	for _, v := range h.Values("Content-Encoding") {
		for _, c := range strings.Split(v, ",") {
			if c = strings.TrimSpace(c); c != "" && !strings.EqualFold(c, "identity") {
				return true
			}
		}
	}
	return false
}

// retryBody is the same call under a new JSON-RPC id (MRTR requires one), with the server's
// requestState echoed exactly when it sent one, and the answers when there are any.
func (c elicitCall) retryBody(id json.RawMessage, ir inputRequired, responses json.RawMessage) []byte {
	params := make(map[string]json.RawMessage, len(c.params)+2)
	for k, v := range c.params {
		params[k] = v
	}
	if ir.requestState != nil {
		params["requestState"] = ir.requestState
	}
	if responses != nil {
		params["inputResponses"] = responses
	}
	top := make(map[string]json.RawMessage, len(c.top))
	for k, v := range c.top {
		top[k] = v
	}
	top["id"] = id
	top["params"] = rawObject(params)
	return rawObject(top)
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err) // only plain maps, strings and finite numbers the relay built are marshalled here
	}
	return b
}

// rawObject writes m as a JSON object whose values are m's bytes as they are. encoding/json would
// compact a RawMessage it marshals, and the arguments must reach the server byte for byte.
func rawObject(m map[string]json.RawMessage) json.RawMessage {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(mustJSON(k))
		b.WriteByte(':')
		b.Write(m[k])
	}
	b.WriteByte('}')
	return b.Bytes()
}

// newRelayRPCID is a JSON-RPC id for a retry the relay sends.
func newRelayRPCID() json.RawMessage {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return mustJSON("af-relay-" + hex.EncodeToString(b[:]))
}

// serveElicitingCall relays one tools/call of a flagged definition, holding it while a person
// answers each input_required result.
func serveElicitingCall(w http.ResponseWriter, r *http.Request, def ServerDef, target *url.URL, call elicitCall) {
	ctx := r.Context()
	// The answers are read, so they must arrive readable: the transport passes compression
	// through untouched (relayTransport). The body is rewritten, so a digest of it would lie.
	up := r.Clone(ctx)
	up.Header.Set("Accept-Encoding", "identity")
	for _, h := range digestHeaders {
		up.Header.Del(h)
	}
	out := &elicitOut{w: w, r: r, cliID: call.id, upID: call.id, token: call.progressToken,
		acceptsSSE: acceptsEventStream(r.Header.Values("Accept"))}
	var gen string
	body := call.body
	for round := 0; ; round++ {
		if round > 0 {
			// Switched off, re-pointed or unflagged while the call waited: it is not sent again,
			// as relayTarget refuses a new request then.
			d, t, err := relayTarget(def.ID)
			if err != nil || d.Elicitation != ElicitationRelay || t.String() != target.String() {
				out.fail(&relayProblem{http.StatusBadGateway, "this MCP server was switched off or changed while the call waited for confirmation"})
				return
			}
		}
		resp, p := relayExchange(ctx, up, def.ID, target, body, &gen)
		if p != nil {
			out.fail(p)
			return
		}
		held, ok := out.pass(def.ID, resp)
		if !ok {
			return
		}
		if round == elicitMaxRounds {
			log.Printf("mcp relay: %s: %s asked for input %d times; ending the call", def.ID, call.tool, round+1)
			out.result(errorResult("the MCP server kept asking for confirmation; the call was not completed"))
			return
		}
		ir, err := parseInputRequired(held)
		if err != nil {
			log.Printf("mcp relay: %s: %s asked for input the relay cannot ask a person for: %v", def.ID, call.tool, err)
			out.result(errorResult("the MCP server asked for input that Agent Fleet cannot ask a person for (" + err.Error() + "); the call was not completed"))
			return
		}
		var responses json.RawMessage
		if len(ir.inputs) > 0 {
			if responses, ok = holdForAnswer(ctx, out, def, call, ir); !ok {
				return
			}
		}
		// A result with only a requestState is retried at once (MRTR client requirement 1).
		if ctx.Err() != nil {
			return
		}
		out.upID = newRelayRPCID()
		body = call.retryBody(out.upID, ir, responses)
	}
}

// acceptsEventStream reports whether an Accept header takes text/event-stream (q=0 refuses it).
func acceptsEventStream(values []string) bool {
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			mt, params, err := mime.ParseMediaType(strings.TrimSpace(part))
			if err != nil || mt != "text/event-stream" {
				continue
			}
			if q, ok := params["q"]; ok {
				if f, err := strconv.ParseFloat(q, 64); err != nil || f <= 0 {
					continue
				}
			}
			return true
		}
	}
	return false
}

// errorResult is a complete tool result that reports a failure to the agent.
func errorResult(msg string) json.RawMessage {
	return mustJSON(map[string]any{
		"resultType": "complete",
		"isError":    true,
		"content":    []any{map[string]any{"type": "text", "text": "Agent Fleet: " + msg}},
	})
}

// elicitOut is the CLI's side of one held call. The answer starts as nothing; it becomes an event
// stream once a stream is needed (the server streams, or a call is held for a CLI that accepts
// one), and otherwise is one JSON body written at the end.
type elicitOut struct {
	w          http.ResponseWriter
	r          *http.Request
	acceptsSSE bool
	streaming  bool
	// cliID is the id the CLI sent; upID the one the current attempt went out with. The response
	// for upID reaches the CLI under cliID.
	cliID, upID json.RawMessage
	// token is the CLI's progress token. Progress on it must only grow (MCP progress), across
	// the server's notifications of every attempt (a retry counts afresh) and the relay's own:
	// lastProgress is the highest value the CLI has seen.
	token        json.RawMessage
	lastProgress float64
	sentProgress bool
	// session is the call's attribution, read once at its first hold: the CLI's socket is the
	// same for every round.
	session    string
	attributed bool
}

// startStream sends the CLI an event stream's headers, taking the server's (minus what the relay
// never forwards) when there is a server response to take them from.
func (o *elicitOut) startStream(from http.Header) {
	if o.streaming {
		return
	}
	if from != nil {
		copyResponseHeaders(o.w.Header(), from)
	}
	o.w.Header().Set("Content-Type", "text/event-stream")
	o.w.Header().Set("Cache-Control", "no-store")
	o.w.Header().Del("Content-Length")
	o.w.Header().Del("Content-Encoding")
	for _, h := range digestHeaders {
		o.w.Header().Del(h)
	}
	o.w.WriteHeader(http.StatusOK)
	_ = http.NewResponseController(o.w).Flush()
	o.streaming = true
}

// event writes one JSON message as an event. It is compacted first: a line break inside it would
// end the data line.
func (o *elicitOut) event(msg []byte) bool {
	var c bytes.Buffer
	if json.Compact(&c, msg) != nil {
		return false
	}
	return o.raw([]byte("event: message\ndata: " + c.String() + "\n\n"))
}

func (o *elicitOut) raw(b []byte) bool {
	if _, err := o.w.Write(b); err != nil {
		return false
	}
	_ = http.NewResponseController(o.w).Flush()
	return true
}

// writeJSON ends a call that is not streaming with one JSON message.
func (o *elicitOut) writeJSON(msg []byte) {
	var c bytes.Buffer
	if json.Compact(&c, msg) != nil {
		o.fail(errStray)
		return
	}
	o.w.Header().Set("Content-Type", "application/json")
	o.w.Header().Set("Cache-Control", "no-store")
	o.w.WriteHeader(http.StatusOK)
	_, _ = o.w.Write(c.Bytes())
}

// result ends the call with a JSON-RPC result of the relay's own.
func (o *elicitOut) result(result json.RawMessage) {
	msg := rawObject(map[string]json.RawMessage{"jsonrpc": json.RawMessage(`"2.0"`), "id": o.cliID, "result": result})
	if o.streaming {
		o.event(msg)
		return
	}
	o.w.Header().Set("Content-Type", "application/json")
	o.w.Header().Set("Cache-Control", "no-store")
	o.w.WriteHeader(http.StatusOK)
	_, _ = o.w.Write(msg)
}

// fail ends the call on a relay problem: as an HTTP status while nothing was sent, as a JSON-RPC
// error on a stream already under way.
func (o *elicitOut) fail(p *relayProblem) {
	if p.status == 0 {
		return
	}
	if !o.streaming {
		p.write(o.w)
		return
	}
	o.event(rawObject(map[string]json.RawMessage{"jsonrpc": json.RawMessage(`"2.0"`), "id": o.cliID,
		"error": mustJSON(map[string]any{"code": -32603, "message": "Agent Fleet: " + p.msg})}))
}

var errStray = &relayProblem{http.StatusBadGateway, "the MCP server sent a message that does not belong to this call"}

// nextProgress is a progress value above last that JSON can carry: +1 while that still moves a
// float64, the next float64 up past 2^53, and false once there is none.
func nextProgress(last float64) (float64, bool) {
	v := last + 1
	if v <= last {
		v = math.Nextafter(last, math.Inf(1))
	}
	return v, !math.IsInf(v, 0) && !math.IsNaN(v)
}

// keepalive is sent while a held call streams: a progress notification when the CLI gave a token
// (and a higher value exists), an SSE comment otherwise.
func (o *elicitOut) keepalive() bool {
	if !o.streaming {
		return true
	}
	v, ok := 1.0, true
	if o.sentProgress {
		v, ok = nextProgress(o.lastProgress)
	}
	if len(o.token) == 0 || !ok {
		return o.raw([]byte(": waiting for a person to answer in the Agent Fleet Console\n\n"))
	}
	o.lastProgress, o.sentProgress = v, true
	return o.event(mustJSON(map[string]any{"jsonrpc": "2.0", "method": "notifications/progress", "params": map[string]any{
		"progressToken": o.token, "progress": v, "message": "Waiting for a person to answer in the Agent Fleet Console",
	}}))
}

// message is one JSON-RPC message from the server as the relay classifies it.
type message struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
	Params json.RawMessage `json:"params"`
}

// msgKind is what a server message is to a held call.
type msgKind int

const (
	msgStray        msgKind = iota // anything else: ends the call with an error
	msgNotification                // passed on (progress rewritten)
	msgResponse                    // the current attempt's response, not input_required
	msgHeld                        // the current attempt's input_required result
)

// classify reads one server message. A response counts only when its id is the current
// attempt's; input_required is held only on a 200. A message whose control members are ambiguous
// to a decoder that folds names is stray: the CLI might read another member than the relay did.
func (o *elicitOut) classify(raw []byte, status int) (message, msgKind) {
	var m message
	keys, vals, ok := objectMembers(raw)
	if !ok || vals == nil || foldDuplicate(keys) || !canonical(keys, "jsonrpc", "id", "method", "params", "result", "error") {
		return m, msgStray
	}
	m = message{ID: vals["id"], Result: vals["result"], Error: vals["error"], Params: vals["params"]}
	if raw, has := vals["method"]; has {
		var isStr bool
		if m.Method, isStr = jsonString(raw); !isStr || m.Method == "" {
			return m, msgStray
		}
		if len(m.ID) == 0 {
			return m, msgNotification
		}
		return m, msgStray // a server request: forbidden on a 2026-07-28 response stream
	}
	if (len(m.Result) == 0) == (len(m.Error) == 0) || !sameJSONID(m.ID, o.upID) {
		return m, msgStray
	}
	if len(m.Result) == 0 {
		return m, msgResponse
	}
	rkeys, rvals, ok := objectMembers(m.Result)
	if !ok || rvals == nil || foldDuplicate(rkeys) || !canonical(rkeys, "resultType", "inputRequests", "requestState") || status != http.StatusOK {
		return m, msgStray // a result needs a 200, and one that cannot be read cannot be checked
	}
	if rt, has := rvals["resultType"]; has {
		t, isStr := jsonString(rt)
		if !isStr {
			return m, msgStray
		}
		if t == "input_required" {
			return m, msgHeld
		}
	}
	return m, msgResponse
}

// rewrite returns a passed message as the CLI must see it: the response under the CLI's id, and
// progress on the CLI's token lifted to keep growing.
func (o *elicitOut) rewrite(msg []byte, m message, kind msgKind) ([]byte, bool) {
	switch {
	case kind == msgResponse && !bytes.Equal(bytes.TrimSpace(m.ID), bytes.TrimSpace(o.cliID)):
		// By the id actually sent, not by attempt: 1.0 matches 1 but a CLI decoding an integer
		// cannot read it.
		var top map[string]json.RawMessage
		if json.Unmarshal(msg, &top) != nil {
			return msg, false
		}
		top["id"] = o.cliID
		return rawObject(top), true
	case kind == msgNotification && m.Method == "notifications/progress" && len(o.token) > 0:
		var p map[string]json.RawMessage
		var v float64
		if json.Unmarshal(m.Params, &p) != nil || !sameJSONID(p["progressToken"], o.token) || json.Unmarshal(p["progress"], &v) != nil {
			return msg, false
		}
		lifted := v
		if o.sentProgress && lifted <= o.lastProgress {
			next, ok := nextProgress(o.lastProgress)
			if !ok {
				return nil, true // nothing higher exists: dropped rather than sent out of order
			}
			lifted = next
		}
		o.lastProgress, o.sentProgress = lifted, true
		if lifted == v {
			return msg, false
		}
		p["progress"] = mustJSON(lifted)
		delete(p, "total") // a lifted value is no longer a fraction of the server's total
		var top map[string]json.RawMessage
		if json.Unmarshal(msg, &top) != nil {
			return msg, false
		}
		top["params"] = rawObject(p)
		return rawObject(top), true
	}
	return msg, false
}

// pass relays the server's answer to the CLI, all of it except an input_required result for the
// current attempt, which it returns instead (held, ok=true). ok=false means the call is over: the
// answer was passed on whole, or it failed and the CLI was told.
func (o *elicitOut) pass(serverID string, resp *http.Response) (held json.RawMessage, ok bool) {
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		noteInsufficientScope(serverID, resp.Header.Values("Www-Authenticate"))
	}
	mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	encoded := contentEncoded(resp.Header)
	firstAttempt := bytes.Equal(o.upID, o.cliID)
	switch {
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		o.fail(&relayProblem{http.StatusBadGateway, relayRedirectMsg})
		return nil, false
	case encoded:
		o.fail(&relayProblem{http.StatusBadGateway, "the MCP server answered compressed although the relay asked it not to"})
		return nil, false
	case resp.StatusCode != http.StatusOK && !o.streaming && firstAttempt && mt != "application/json" && mt != "text/event-stream":
		// No MCP message can be in it: passed on as any other definition's answer is.
		relayCopyResponse(o.w, o.r, resp)
		return nil, false
	case mt == "text/event-stream":
		if resp.StatusCode != http.StatusOK {
			o.fail(&relayProblem{http.StatusBadGateway, fmt.Sprintf("the MCP server answered HTTP %d with an event stream", resp.StatusCode)})
			return nil, false
		}
		if o.acceptsSSE {
			o.startStream(resp.Header)
		}
		return o.passStream(resp.Body)
	case mt == "application/json":
		b, err := io.ReadAll(io.LimitReader(resp.Body, relayMaxBody+1))
		if err != nil || int64(len(b)) > relayMaxBody {
			if err != nil && o.r.Context().Err() != nil {
				return nil, false
			}
			o.fail(&relayProblem{http.StatusBadGateway, "the MCP server's answer could not be read"})
			return nil, false
		}
		m, kind := o.classify(b, resp.StatusCode)
		switch kind {
		case msgHeld:
			return m.Result, true
		case msgResponse:
		default:
			o.fail(&relayProblem{http.StatusBadGateway, fmt.Sprintf("the MCP server answered HTTP %d without the call's response", resp.StatusCode)})
			return nil, false
		}
		b, _ = o.rewrite(b, m, kind)
		if o.streaming {
			o.event(b)
			return nil, false
		}
		var c bytes.Buffer
		if json.Compact(&c, b) == nil {
			b = c.Bytes()
		}
		copyResponseHeaders(o.w.Header(), resp.Header)
		o.w.Header().Del("Content-Length")
		for _, h := range digestHeaders {
			o.w.Header().Del(h)
		}
		o.w.WriteHeader(resp.StatusCode)
		_, _ = o.w.Write(b)
		return nil, false
	default:
		o.fail(&relayProblem{http.StatusBadGateway, fmt.Sprintf("the MCP server answered HTTP %d with an unexpected content type", resp.StatusCode)})
		return nil, false
	}
}

// passStream forwards a server's event stream (WHATWG event-stream parsing: LF, CRLF and CR end a
// line, a leading BOM is dropped, only an event ended by a blank line is dispatched, and one cut
// off by the end of the stream is dropped). A comment line is passed at once, so a server's
// keepalive keeps the CLI's connection alive too. Of the events, notifications and the current
// attempt's response pass (rewritten where the CLI must see other values), its input_required
// result is kept back, and anything else ends the call with an error.
func (o *elicitOut) passStream(body io.Reader) (json.RawMessage, bool) {
	rd := &sseLines{r: body, buf: make([]byte, 32<<10)}
	var ev bytes.Buffer
	var data []string
	hasData := false
	for {
		line, raw, err := rd.next()
		if raw != nil {
			switch {
			case strings.HasPrefix(line, ":"):
				if o.streaming && !o.raw(raw) {
					return nil, false
				}
			case len(line) == 0:
				if hasData {
					msg := []byte(strings.Join(data, "\n"))
					m, kind := o.classify(msg, http.StatusOK)
					switch kind {
					case msgHeld:
						return m.Result, true
					case msgStray:
						o.fail(errStray)
						return nil, false
					}
					out, changed := o.rewrite(msg, m, kind)
					switch {
					case !o.streaming:
						// A CLI that takes no stream gets the response alone, as JSON.
						if kind == msgResponse {
							o.writeJSON(out)
						}
					case changed:
						if out != nil && !o.event(out) {
							return nil, false
						}
					case !o.raw(append(ev.Bytes(), '\n')):
						return nil, false
					}
					if kind == msgResponse {
						return nil, false
					}
				} else if o.streaming && ev.Len() > 0 && !o.raw(append(ev.Bytes(), '\n')) {
					return nil, false
				}
				ev.Reset()
				data, hasData = nil, false
			default:
				if int64(ev.Len()+len(raw)) > relayMaxBody {
					o.fail(&relayProblem{http.StatusBadGateway, "the MCP server's answer could not be read"})
					return nil, false
				}
				ev.Write(raw)
				if line == "data" || strings.HasPrefix(line, "data:") {
					v := strings.TrimPrefix(strings.TrimPrefix(line, "data"), ":")
					data, hasData = append(data, strings.TrimPrefix(v, " ")), true
				}
			}
		}
		if err == io.EOF {
			// The stream ended without the call's response.
			o.fail(&relayProblem{http.StatusBadGateway, "the MCP server ended its answer without the call's response"})
			return nil, false
		}
		if err != nil {
			if errors.Is(err, errSSELineTooLong) {
				o.fail(&relayProblem{http.StatusBadGateway, "the MCP server's answer could not be read"})
				return nil, false
			}
			if o.r.Context().Err() == nil {
				// As relayCopyResponse: a broken upstream stream ends the CLI's abnormally.
				panic(http.ErrAbortHandler)
			}
			return nil, false
		}
	}
}

var errSSELineTooLong = errors.New("event-stream line too long")

// sseLines splits an event stream into lines on LF, CRLF or CR without waiting for the byte after
// a CR (a CR that ends the last line so far must not stall the stream).
type sseLines struct {
	r       io.Reader
	buf     []byte
	start   int
	end     int
	afterCR bool
	began   bool
	err     error
}

// next returns one line without its terminator, and the line as it is passed on (with LF). raw is
// nil when no line is complete; a line cut off by the end of the stream is never returned. err is
// set once the reader is done.
func (s *sseLines) next() (line string, raw []byte, err error) {
	var cur []byte
	for {
		for s.start < s.end {
			c := s.buf[s.start]
			if s.afterCR {
				s.afterCR = false
				if c == '\n' {
					s.start++
					continue
				}
			}
			s.start++
			switch c {
			case '\r':
				s.afterCR = true
				fallthrough
			case '\n':
				return s.finish(cur)
			}
			cur = append(cur, c)
			if int64(len(cur)) > relayMaxBody {
				return "", nil, errSSELineTooLong
			}
		}
		if s.err != nil {
			return "", nil, s.err
		}
		n, err := s.r.Read(s.buf)
		s.start, s.end, s.err = 0, n, err
	}
}

// finish ends a line. Every terminator is passed on as LF: a CR followed later by an LF must not
// read as two line ends to the CLI.
func (s *sseLines) finish(cur []byte) (string, []byte, error) {
	if !s.began {
		// A byte-order mark at the very start is dropped from what is passed on too: the relay's
		// own stream to the CLI may have begun earlier, and a BOM inside it is not one.
		s.began = true
		cur = bytes.TrimPrefix(cur, []byte("\uFEFF"))
	}
	return string(cur), append(append([]byte(nil), cur...), '\n'), nil
}

// sameJSONID compares two JSON-RPC ids: a string never equals a number, and numbers are compared
// by value, exactly (never through float64: 2^53+1 is not 2^53, and -0 is 0).
func sameJSONID(a, b json.RawMessage) bool {
	x, ok1 := idKey(a)
	y, ok2 := idKey(b)
	return ok1 && ok2 && x == y
}

// idKey is a JSON-RPC id in a form that compares by value: "s:" and the string, or "n:" and the
// number's sign, significant digits and power of ten. It never expands a number, so an id with
// a huge exponent costs no more than its length.
func idKey(raw json.RawMessage) (string, bool) {
	t := bytes.TrimSpace(raw)
	if len(t) > 0 && t[0] == '"' {
		s, ok := jsonString(raw)
		return "s:" + s, ok
	}
	if !json.Valid(t) || len(t) == 0 || (t[0] != '-' && (t[0] < '0' || t[0] > '9')) {
		return "", false
	}
	num := string(t)
	neg := strings.HasPrefix(num, "-")
	num = strings.TrimPrefix(num, "-")
	mant, exp, _ := strings.Cut(strings.ToLower(num), "e")
	intPart, frac, _ := strings.Cut(mant, ".")
	digits := strings.TrimLeft(intPart+frac, "0")
	e, err := strconv.ParseInt(strings.TrimPrefix(exp, "+"), 10, 64)
	if exp == "" {
		e, err = 0, nil
	}
	const expLimit = 1 << 40 // far beyond any id, and no sum below can wrap past it
	if err != nil || e > expLimit || e < -expLimit {
		return "", false // an exponent no id needs
	}
	if digits == "" {
		return "n:0", true // -0 and 0.000 are 0
	}
	trimmed := strings.TrimRight(digits, "0")
	e += int64(len(digits)-len(trimmed)) - int64(len(frac))
	sign := "+"
	if neg {
		sign = "-"
	}
	return "n:" + sign + trimmed + "e" + strconv.FormatInt(e, 10), true
}

// copyResponseHeaders copies a server's response headers for the CLI, minus hop-by-hop ones and
// those the relay never passes on: WWW-Authenticate is the one that matters (see relay.go's
// header); Set-Cookie and Location would hand the CLI state or a place to go that the relay did
// not vouch for.
func copyResponseHeaders(dst, src http.Header) {
	extra := map[string]bool{}
	for _, v := range src.Values("Connection") {
		for _, f := range strings.Split(v, ",") {
			extra[http.CanonicalHeaderKey(strings.TrimSpace(f))] = true
		}
	}
	for k, vs := range src {
		c := http.CanonicalHeaderKey(k)
		if relayHopHeaders[c] || extra[c] || c == "Www-Authenticate" || c == "Set-Cookie" || c == "Location" {
			continue
		}
		dst[k] = append([]string(nil), vs...)
	}
}

// inputRequired is a parsed input_required result the relay can put to a person.
type inputRequired struct {
	// requestState is the server's value as it came, nil when the result had none (then the
	// retry carries none either).
	requestState json.RawMessage
	inputs       []ElicitInput
}

// parseInputRequired accepts form elicitations whose fields are all choices (an enum of strings,
// with or without labels): an approval. Anything else (free text, sampling, roots, a URL to open,
// a constraint the relay would not check) is refused, since a person answering a card cannot give
// it. A result with only a requestState has no inputs and is retried at once. Every object is read
// with exact member names and none that fold together: the relay must check what the server meant.
func parseInputRequired(result json.RawMessage) (inputRequired, error) {
	unreadable := errors.New("an unreadable input request")
	_, res, ok := strictObject(result, nil)
	if !ok {
		return inputRequired{}, unreadable
	}
	out := inputRequired{}
	if raw, has := res["requestState"]; has {
		if _, ok := jsonString(raw); !ok {
			return inputRequired{}, errors.New("a requestState that is not a string")
		}
		out.requestState = raw
	}
	var keys []string
	var reqs map[string]json.RawMessage
	if raw, has := res["inputRequests"]; has {
		if keys, reqs, ok = strictObject(raw, nil); !ok {
			return inputRequired{}, unreadable
		}
	}
	if len(reqs) == 0 && out.requestState == nil {
		return inputRequired{}, errors.New("no input request")
	}
	for _, key := range keys {
		_, req, ok := strictObject(reqs[key], []string{"method", "params"})
		if !ok {
			return inputRequired{}, unreadable
		}
		if method, _ := jsonString(req["method"]); method != "elicitation/create" {
			return inputRequired{}, fmt.Errorf("a %s request", cut(method, elicitMaxText))
		}
		pkeys, params, ok := strictObject(req["params"], nil)
		if !ok || !canonical(pkeys, "mode", "message", "requestedSchema") {
			return inputRequired{}, unreadable
		}
		if raw, has := params["mode"]; has {
			if mode, _ := jsonString(raw); mode != "form" {
				return inputRequired{}, fmt.Errorf("a %s-mode elicitation", cut(mode, elicitMaxText))
			}
		}
		// The message is what tells the person what they approve (MCP requires it): a form
		// without one is not put to anyone.
		msg, ok := jsonString(params["message"])
		if !ok {
			return inputRequired{}, errors.New("a form with no message")
		}
		in := ElicitInput{Key: key, Message: cut(msg, elicitMaxMessage)}
		fields, err := parseFormSchema(params["requestedSchema"])
		if err != nil {
			return inputRequired{}, err
		}
		in.Fields = fields
		out.inputs = append(out.inputs, in)
	}
	sort.Slice(out.inputs, func(i, j int) bool { return out.inputs[i].Key < out.inputs[j].Key })
	if largestAnswer(out.inputs) > ElicitAnswerMaxBytes {
		return inputRequired{}, errors.New("a form whose answer is too large to send back")
	}
	return out, nil
}

// largestAnswer is the encoded size of the largest accept a person could send: every field
// answered with its longest choice. Go escapes more than a browser does, so this is an upper bound.
func largestAnswer(inputs []ElicitInput) int {
	choices := map[string]map[string]string{}
	for _, in := range inputs {
		c := map[string]string{}
		for _, f := range in.Fields {
			for _, o := range f.Options {
				if len(o.Value) >= len(c[f.Name]) {
					c[f.Name] = o.Value
				}
			}
		}
		choices[in.Key] = c
	}
	return len(mustJSON(choices))
}

// strictObject reads raw as one JSON object with no two names that fold together. allowed, when
// not nil, lists every name it may have.
func strictObject(raw json.RawMessage, allowed []string) ([]string, map[string]json.RawMessage, bool) {
	keys, vals, ok := objectMembers(raw)
	if !ok || vals == nil || foldDuplicate(keys) {
		return nil, nil, false
	}
	if allowed != nil {
		for _, k := range keys {
			known := false
			for _, a := range allowed {
				known = known || k == a
			}
			if !known {
				return nil, nil, false
			}
		}
	}
	return keys, vals, true
}

// Members a form may carry. Anything else may be a constraint the relay would not enforce, so a
// form that has one is refused rather than shown with it dropped.
var (
	formSchemaMembers = []string{"type", "properties", "required", "title", "description", "$schema"}
	formFieldMembers  = []string{"type", "title", "description", "enum", "enumNames", "oneOf", "default"}
	formOptionMembers = []string{"const", "title", "description"}
)

// parseFormSchema reads a form's requestedSchema: an object of choice fields.
func parseFormSchema(raw json.RawMessage) ([]ElicitField, error) {
	_, schema, ok := strictObject(raw, formSchemaMembers)
	if !ok {
		return nil, errors.New("a form with members the relay does not check")
	}
	if t, _ := jsonString(schema["type"]); t != "object" {
		return nil, errors.New("a form that is not an object")
	}
	// MCP's default dialect is the only one the relay reads a form by.
	if raw, has := schema["$schema"]; has {
		if d, _ := jsonString(raw); strings.TrimSuffix(d, "#") != "https://json-schema.org/draft/2020-12/schema" {
			return nil, errors.New("a form in a schema dialect the relay does not read")
		}
	}
	names, props, ok := strictObject(schema["properties"], nil)
	if !ok || len(names) == 0 {
		return nil, errors.New("a form with no fields")
	}
	required := map[string]bool{}
	if raw, has := schema["required"]; has {
		var req []json.RawMessage
		if json.Unmarshal(raw, &req) != nil {
			return nil, errors.New("an unreadable list of required fields")
		}
		for _, r := range req {
			n, ok := jsonString(r)
			if _, described := props[n]; !ok || !described {
				return nil, errors.New("a required field the form does not describe")
			}
			required[n] = true
		}
	}
	var fields []ElicitField
	for _, name := range names {
		f, err := parseChoiceField(name, props[name])
		if err != nil {
			return nil, err
		}
		f.Required = required[name]
		fields = append(fields, f)
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].Name < fields[j].Name })
	return fields, nil
}

// parseChoiceField reads one string property that is a choice: enum (with optional enumNames
// labels, Google's form) or oneOf of const/title, never both. Every value must be a string the
// server wrote; a null or a missing const is refused rather than read as "".
func parseChoiceField(name string, raw json.RawMessage) (ElicitField, error) {
	_, p, ok := strictObject(raw, formFieldMembers)
	if !ok {
		return ElicitField{}, errors.New("a field with members the relay does not check")
	}
	if t, _ := jsonString(p["type"]); t != "string" {
		return ElicitField{}, errors.New("a field that is not a choice")
	}
	f := ElicitField{Name: name}
	if raw, has := p["title"]; has {
		title, ok := jsonString(raw)
		if !ok {
			return ElicitField{}, errors.New("a field with an unreadable title")
		}
		f.Title = cut(title, elicitMaxText)
	}
	var enum, names, oneOf []json.RawMessage
	for k, dst := range map[string]*[]json.RawMessage{"enum": &enum, "enumNames": &names, "oneOf": &oneOf} {
		if raw, has := p[k]; has && json.Unmarshal(raw, dst) != nil {
			return ElicitField{}, errors.New("a field with an unreadable list of choices")
		}
	}
	switch {
	case len(enum) > 0 && len(oneOf) > 0:
		return ElicitField{}, errors.New("a field with both enum and oneOf")
	case len(enum) > 0:
		// Labels pair with values by position: one missing would put the next one's label (say
		// "Deny") on another value (say ALLOW_TOOL_ALWAYS), so the lists must match exactly.
		if _, has := p["enumNames"]; has && len(names) != len(enum) {
			return ElicitField{}, errors.New("a field whose labels do not match its choices")
		}
		for i, r := range enum {
			v, ok := jsonString(r)
			if !ok {
				return ElicitField{}, errors.New("a choice that is not a string")
			}
			label := v
			if i < len(names) {
				l, ok := jsonString(names[i])
				if !ok {
					return ElicitField{}, errors.New("a field whose labels do not match its choices")
				}
				if l != "" {
					label = l
				}
			}
			f.Options = append(f.Options, ElicitOption{Value: v, Label: cut(label, elicitMaxText)})
		}
	case len(oneOf) > 0:
		for _, r := range oneOf {
			_, o, ok := strictObject(r, formOptionMembers)
			if !ok {
				return ElicitField{}, errors.New("a choice with members the relay does not check")
			}
			v, ok := jsonString(o["const"])
			if !ok {
				return ElicitField{}, errors.New("a choice that is not a string")
			}
			label, _ := jsonString(o["title"])
			if label == "" {
				label = v
			}
			f.Options = append(f.Options, ElicitOption{Value: v, Label: cut(label, elicitMaxText)})
		}
	default:
		return ElicitField{}, errors.New("a free-text field")
	}
	// A value offered twice is ambiguous (two labels for one value; for oneOf, a value no single
	// branch allows).
	seen := map[string]bool{}
	for _, o := range f.Options {
		if seen[o.Value] {
			return ElicitField{}, errors.New("a field that offers one choice twice")
		}
		seen[o.Value] = true
	}
	return f, nil
}

func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Cut on a rune boundary.
	for n > 0 && n < len(s) && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n] + "…"
}

// holdForAnswer files the call for a person and waits for the answer, the time limit, or the
// CLI going away. ok=false means the call is over (the CLI left, or was told why not).
func holdForAnswer(ctx context.Context, out *elicitOut, def ServerDef, call elicitCall, ir inputRequired) (json.RawMessage, bool) {
	if !out.attributed {
		out.session, out.attributed = callerSession(out.r), true
	}
	call.session = out.session
	p, err := elicitations.add(ctx, def, call, ir)
	if err != nil {
		out.result(errorResult(err.Error()))
		return nil, false
	}
	defer elicitations.remove(p.ID)
	if out.acceptsSSE {
		out.startStream(nil)
	}
	// The timer runs to the deadline the list shows and AnswerElicitation checks.
	timeout := time.NewTimer(time.Until(p.Deadline))
	defer timeout.Stop()
	tick := time.NewTicker(elicitKeepalive)
	defer tick.Stop()
	var ans ElicitAnswer
	for done := false; !done; {
		select {
		case ans = <-p.answer:
			done = true
		case <-timeout.C:
			// Taken off the list first: an answer accepted before the deadline is the one sent.
			if elicitations.take(p.ID) {
				ans, done = ElicitAnswer{Decline: true}, true
				log.Printf("mcp relay: %s: no answer to %s's confirmation within %s; declining", def.ID, call.tool, elicitWait)
			} else {
				ans, done = <-p.answer, true
			}
		case <-tick.C:
			if !out.keepalive() {
				return nil, false
			}
		case <-ctx.Done():
			return nil, false
		}
	}
	responses := map[string]json.RawMessage{}
	for _, in := range ir.inputs {
		if ans.Decline {
			responses[in.Key] = json.RawMessage(`{"action":"decline"}`)
			continue
		}
		responses[in.Key] = mustJSON(map[string]any{"action": "accept", "content": ans.Choices[in.Key]})
	}
	action := "accept"
	if ans.Decline {
		action = "decline"
	}
	log.Printf("mcp relay: %s: confirmation of %s answered: %s", def.ID, call.tool, action)
	return rawObject(responses), true
}

// ElicitOption is one choice of a field: the value sent back, and the server's label for it.
type ElicitOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// ElicitField is one choice the server asks for.
type ElicitField struct {
	Name     string         `json:"name"`
	Title    string         `json:"title,omitempty"`
	Required bool           `json:"required,omitempty"`
	Options  []ElicitOption `json:"options"`
}

// ElicitInput is one of the server's input requests: its message (Markdown, the server's text)
// and its fields.
type ElicitInput struct {
	Key     string        `json:"key"`
	Message string        `json:"message,omitempty"`
	Fields  []ElicitField `json:"fields"`
}

// PendingElicitation is one held call waiting for a person.
type PendingElicitation struct {
	ID          string `json:"id"`
	ServerID    string `json:"serverId"`
	ServerName  string `json:"server"`
	ServerLabel string `json:"serverLabel,omitempty"`
	Tool        string `json:"tool"`
	// Session is the session whose process made the call, "" when it cannot be told
	// (callerSession). It says where to show the call, never whether it may be answered.
	Session string `json:"session,omitempty"`
	// Arguments are the call's arguments exactly as the server will run them.
	Arguments json.RawMessage `json:"arguments,omitempty"`
	Inputs    []ElicitInput   `json:"inputs"`
	CreatedAt time.Time       `json:"createdAt"`
	Deadline  time.Time       `json:"deadline"`
}

// clone is a deep copy: what a caller does with a listed entry never reaches the arguments that
// are sent again or the choices an answer is checked against.
func (p PendingElicitation) clone() PendingElicitation {
	c := p
	c.Arguments = append(json.RawMessage(nil), p.Arguments...)
	c.Inputs = make([]ElicitInput, len(p.Inputs))
	for i, in := range p.Inputs {
		c.Inputs[i] = in
		c.Inputs[i].Fields = make([]ElicitField, len(in.Fields))
		for j, f := range in.Fields {
			c.Inputs[i].Fields[j] = f
			c.Inputs[i].Fields[j].Options = append([]ElicitOption(nil), f.Options...)
		}
	}
	return c
}

// ElicitAnswer is a person's answer: decline, or a choice per input and field. An accept names
// every input with a non-nil map, even one whose fields are all optional (then an empty map).
type ElicitAnswer struct {
	Decline bool
	Choices map[string]map[string]string
}

var (
	// ErrElicitationNotFound: no held call has that id (answered, past its deadline, or the CLI
	// left).
	ErrElicitationNotFound = errors.New("no pending confirmation with that id")
	// ErrElicitationAnswer: the answer does not fit the server's form.
	ErrElicitationAnswer = errors.New("the answer does not match the server's choices")
)

type pendingElicit struct {
	PendingElicitation
	// size is what the call counts against elicitMaxHeld.
	size int
	// gone is closed when the CLI's request ends.
	gone   <-chan struct{}
	answer chan ElicitAnswer
}

type elicitStore struct {
	mu      sync.Mutex
	pending map[string]*pendingElicit
	bytes   int // the sum of the pending calls' sizes
}

// heldSize is what a held call keeps for the Console, roughly as it is listed.
func (p PendingElicitation) heldSize() int {
	n := len(p.Arguments) + len(p.Tool) + len(p.ServerName) + len(p.ServerLabel)
	for _, in := range p.Inputs {
		n += len(in.Key) + len(in.Message)
		for _, f := range in.Fields {
			n += len(f.Name) + len(f.Title)
			for _, o := range f.Options {
				n += len(o.Value) + len(o.Label)
			}
		}
	}
	return n
}

var elicitations = &elicitStore{pending: map[string]*pendingElicit{}}

func (s *elicitStore) add(ctx context.Context, def ServerDef, call elicitCall, ir inputRequired) (*pendingElicit, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	now := time.Now()
	p := &pendingElicit{
		PendingElicitation: PendingElicitation{
			ID: hex.EncodeToString(b[:]), ServerID: def.ID, ServerName: def.Name, ServerLabel: def.Label, Tool: call.tool,
			Session: call.session, Arguments: call.params["arguments"], Inputs: ir.inputs, CreatedAt: now, Deadline: now.Add(elicitWait),
		},
		gone:   ctx.Done(),
		answer: make(chan ElicitAnswer, 1),
	}
	p.size = p.heldSize()
	if p.size > elicitMaxHeldOne {
		return nil, errors.New("the call is too large to put to a person for confirmation; the call was not completed")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pending) >= elicitMaxPending || s.bytes+p.size > elicitMaxHeld {
		return nil, errors.New("too many calls are already waiting for a person's confirmation; the call was not completed")
	}
	s.pending[p.ID] = p
	s.bytes += p.size
	return p, nil
}

func (s *elicitStore) remove(id string) { s.take(id) }

// take removes a held call and reports whether it was still waiting: false means an answer was
// already handed to it.
func (s *elicitStore) take(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pending[id]
	if ok {
		s.bytes -= p.size
		delete(s.pending, id)
	}
	return ok
}

// PendingElicitations lists the held calls, oldest first.
func PendingElicitations() []PendingElicitation {
	s := elicitations
	s.mu.Lock()
	out := make([]PendingElicitation, 0, len(s.pending))
	for _, p := range s.pending {
		out = append(out, p.PendingElicitation.clone())
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// AnswerElicitation hands a person's answer to the held call. An accept must answer every input,
// choose one of the server's options for every required field, and name nothing else. A call
// takes one answer, and none once its deadline has passed or its CLI has gone: both are checked
// last, at the moment the answer is handed over. It returns the call answered, for the audit:
// what names it, without its arguments or form.
func AnswerElicitation(id string, a ElicitAnswer) (PendingElicitation, error) {
	s := elicitations
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pending[id]
	if !ok {
		return PendingElicitation{}, ErrElicitationNotFound
	}
	if !a.Decline {
		clean, err := checkChoices(p.Inputs, a.Choices)
		if err != nil {
			return PendingElicitation{}, err
		}
		a.Choices = clean
	} else {
		a.Choices = nil
	}
	select {
	case <-p.gone:
		return PendingElicitation{}, ErrElicitationNotFound
	default:
	}
	if !time.Now().Before(p.Deadline) {
		return PendingElicitation{}, ErrElicitationNotFound
	}
	s.bytes -= p.size
	delete(s.pending, id)
	p.answer <- a
	return PendingElicitation{ID: p.ID, ServerID: p.ServerID, ServerName: p.ServerName, ServerLabel: p.ServerLabel,
		Tool: p.Tool, Session: p.Session, CreatedAt: p.CreatedAt, Deadline: p.Deadline}, nil
}

func checkChoices(inputs []ElicitInput, choices map[string]map[string]string) (map[string]map[string]string, error) {
	known := map[string]bool{}
	for _, in := range inputs {
		known[in.Key] = true
	}
	for key := range choices {
		if !known[key] {
			return nil, ErrElicitationAnswer
		}
	}
	out := map[string]map[string]string{}
	for _, in := range inputs {
		got := choices[in.Key]
		if got == nil { // not answered, or answered with null: neither is an empty accept
			return nil, ErrElicitationAnswer
		}
		fields := map[string]ElicitField{}
		for _, f := range in.Fields {
			fields[f.Name] = f
		}
		for name := range got {
			if _, ok := fields[name]; !ok {
				return nil, ErrElicitationAnswer
			}
		}
		content := map[string]string{}
		for _, f := range in.Fields {
			v, given := got[f.Name]
			if !given {
				if f.Required {
					return nil, ErrElicitationAnswer
				}
				continue
			}
			valid := false
			for _, o := range f.Options {
				valid = valid || o.Value == v
			}
			if !valid {
				return nil, ErrElicitationAnswer
			}
			content[f.Name] = v
		}
		out[in.Key] = content
	}
	return out, nil
}
