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
//     "decline" (the time limit, or a person declining) and never invents a choice. A
//     requestState or inputResponses the CLI sent itself is dropped: a confirmation reaches the
//     server only through the pending list.
//   - The arguments are passed on byte for byte: a changed argument voids the server's approval,
//     and the person is shown exactly what the server will run.
//   - Only tools/call of the 2026-07-28 era (a protocolVersion in _meta) is read. Other methods,
//     and every older-era exchange, are relayed as bytes as for any other definition, so the relay
//     never declares a capability for a result it does not handle.
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
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
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
)

// elicitCall is one 2026-07-28-era tools/call, with form elicitation declared in its _meta.
type elicitCall struct {
	id     json.RawMessage
	tool   string
	top    map[string]json.RawMessage
	params map[string]json.RawMessage
	// progressToken is the CLI's own token, nil when it sent none.
	progressToken json.RawMessage
	body          []byte
}

// parseElicitRequest reads body as a 2026-07-28-era tools/call and sets form elicitation as the
// client capability it declares. ok is false for anything else, which is then relayed unchanged.
//
// The capability is replaced, not merged: the relay answers form elicitation only, so a "url"
// capability the CLI declared would invite a request nobody here can answer.
func parseElicitRequest(body []byte) (elicitCall, bool) {
	var c elicitCall
	var method string
	if json.Unmarshal(body, &c.top) != nil || json.Unmarshal(c.top["method"], &method) != nil || method != "tools/call" {
		return elicitCall{}, false
	}
	c.id = c.top["id"]
	if len(c.id) == 0 || json.Unmarshal(c.top["params"], &c.params) != nil || c.params == nil ||
		json.Unmarshal(c.params["name"], &c.tool) != nil {
		return elicitCall{}, false
	}
	var meta map[string]json.RawMessage
	if json.Unmarshal(c.params["_meta"], &meta) != nil || meta == nil {
		return elicitCall{}, false
	}
	if _, ok := meta[metaProtocolVersion]; !ok {
		return elicitCall{}, false
	}
	var caps map[string]json.RawMessage
	if raw, ok := meta[metaClientCaps]; ok {
		if json.Unmarshal(raw, &caps) != nil {
			return elicitCall{}, false
		}
	}
	if caps == nil {
		caps = map[string]json.RawMessage{}
	}
	caps["elicitation"] = json.RawMessage(`{"form":{}}`)
	meta[metaClientCaps] = rawObject(caps)
	c.params["_meta"] = rawObject(meta)
	delete(c.params, "requestState")
	delete(c.params, "inputResponses")
	c.top["params"] = rawObject(c.params)
	c.progressToken = meta["progressToken"]
	c.body = rawObject(c.top)
	return c, true
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
		panic(err) // only plain maps and strings the relay built are marshalled here
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
	// through untouched (relayTransport).
	up := r.Clone(ctx)
	up.Header.Set("Accept-Encoding", "identity")
	out := &elicitOut{w: w, r: r, cliID: call.id, upID: call.id, token: call.progressToken,
		acceptsSSE: acceptsEventStream(r.Header.Values("Accept"))}
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
		resp, p := relayExchange(ctx, up, def.ID, target, body)
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

func acceptsEventStream(values []string) bool {
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			mt, _, err := mime.ParseMediaType(strings.TrimSpace(part))
			if err == nil && mt == "text/event-stream" {
				return true
			}
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
	// cliID is the id the CLI sent; upID the one the current attempt went out with. A message
	// for upID reaches the CLI under cliID.
	cliID, upID json.RawMessage
	// token is the CLI's progress token. Progress on it must only grow (MCP progress), across
	// the server's notifications of every attempt (a retry counts afresh) and the relay's own:
	// lastProgress is the highest value the CLI has seen.
	token        json.RawMessage
	lastProgress float64
	sentProgress bool
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

// keepalive is sent while a held call streams: a progress notification when the CLI gave a token,
// an SSE comment otherwise.
func (o *elicitOut) keepalive() bool {
	if !o.streaming {
		return true
	}
	if len(o.token) == 0 {
		return o.raw([]byte(": waiting for a person to answer in the Agent Fleet Console\n\n"))
	}
	v := 1.0
	if o.sentProgress {
		v = o.lastProgress + 1
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

// forCall reports whether m is the response to the current attempt.
func (o *elicitOut) forCall(m message) bool {
	return (len(m.Result) > 0 || len(m.Error) > 0) && sameJSONID(m.ID, o.upID)
}

// heldResult returns the result of m when it is the current attempt's input_required.
func (o *elicitOut) heldResult(m message) (json.RawMessage, bool) {
	if len(m.Result) == 0 || !sameJSONID(m.ID, o.upID) {
		return nil, false
	}
	var res struct {
		ResultType string `json:"resultType"`
	}
	if json.Unmarshal(m.Result, &res) != nil || res.ResultType != "input_required" {
		return nil, false
	}
	return m.Result, true
}

// rewrite returns msg as the CLI must see it, and whether it differs: the current attempt's
// response under the CLI's id, and progress on the CLI's token lifted to keep growing.
func (o *elicitOut) rewrite(msg []byte, m message) ([]byte, bool) {
	switch {
	case o.forCall(m) && !bytes.Equal(o.upID, o.cliID):
		var top map[string]json.RawMessage
		if json.Unmarshal(msg, &top) != nil {
			return msg, false
		}
		top["id"] = o.cliID
		return rawObject(top), true
	case m.Method == "notifications/progress" && len(o.token) > 0:
		var p map[string]json.RawMessage
		var v float64
		if json.Unmarshal(m.Params, &p) != nil || !sameJSONID(p["progressToken"], o.token) || json.Unmarshal(p["progress"], &v) != nil {
			return msg, false
		}
		lifted := v
		if o.sentProgress && lifted <= o.lastProgress {
			lifted = o.lastProgress + 1
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
	encoded := false
	if ce := resp.Header.Get("Content-Encoding"); ce != "" && !strings.EqualFold(ce, "identity") {
		encoded = true
	}
	firstAttempt := bytes.Equal(o.upID, o.cliID)
	switch {
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		o.fail(&relayProblem{http.StatusBadGateway, relayRedirectMsg})
		return nil, false
	case resp.StatusCode != http.StatusOK && !o.streaming && firstAttempt && (encoded || mt != "application/json"):
		// Nothing of it has to be read: passed on as any other definition's answer is.
		relayCopyResponse(o.w, o.r, resp)
		return nil, false
	case encoded:
		o.fail(&relayProblem{http.StatusBadGateway, "the MCP server answered compressed although the relay asked it not to"})
		return nil, false
	case resp.StatusCode == http.StatusOK && mt == "text/event-stream":
		o.startStream(resp.Header)
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
		var m message
		_ = json.Unmarshal(b, &m)
		if resp.StatusCode == http.StatusOK {
			if res, ok := o.heldResult(m); ok {
				return res, true
			}
		}
		b, _ = o.rewrite(b, m)
		if o.streaming {
			if !o.forCall(m) || !o.event(b) {
				o.fail(&relayProblem{http.StatusBadGateway, fmt.Sprintf("the MCP server answered HTTP %d without a usable message", resp.StatusCode)})
			}
			return nil, false
		}
		copyResponseHeaders(o.w.Header(), resp.Header)
		o.w.Header().Del("Content-Length")
		o.w.WriteHeader(resp.StatusCode)
		_, _ = o.w.Write(b)
		return nil, false
	default:
		o.fail(&relayProblem{http.StatusBadGateway, fmt.Sprintf("the MCP server answered HTTP %d with an unexpected content type", resp.StatusCode)})
		return nil, false
	}
}

// passStream forwards a server's event stream event by event (WHATWG event-stream parsing: LF,
// CRLF and CR end a line, a leading BOM is dropped, only an event ended by a blank line is
// dispatched). The current attempt's input_required event is kept back; its response and
// progress events are rewritten where the CLI must see other values; every other event is passed
// as it came, with LF line ends.
func (o *elicitOut) passStream(body io.Reader) (json.RawMessage, bool) {
	rd := &sseLines{r: body, buf: make([]byte, 32<<10)}
	var ev bytes.Buffer
	var data []string
	hasData := false
	for {
		line, raw, err := rd.next()
		if raw != nil {
			if int64(ev.Len()+len(raw)) > relayMaxBody {
				o.fail(&relayProblem{http.StatusBadGateway, "the MCP server's answer could not be read"})
				return nil, false
			}
			ev.Write(raw)
			if len(line) == 0 {
				if hasData {
					msg := []byte(strings.Join(data, "\n"))
					var m message
					_ = json.Unmarshal(msg, &m)
					if res, ok := o.heldResult(m); ok {
						return res, true
					}
					if out, changed := o.rewrite(msg, m); changed {
						if !o.event(out) {
							return nil, false
						}
					} else if !o.raw(ev.Bytes()) {
						return nil, false
					}
				} else if !o.raw(ev.Bytes()) {
					return nil, false
				}
				ev.Reset()
				data, hasData = nil, false
			} else if line == "data" || strings.HasPrefix(line, "data:") {
				v := strings.TrimPrefix(strings.TrimPrefix(line, "data"), ":")
				data, hasData = append(data, strings.TrimPrefix(v, " ")), true
			}
		}
		if err == io.EOF {
			// An event cut off before its blank line is not dispatched; its bytes are passed
			// on, and a CLI drops them the same way.
			if ev.Len() > 0 {
				o.raw(ev.Bytes())
			}
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

// next returns one line without its terminator, and the line's bytes as they are passed on
// (terminator included). raw is nil when no line is complete; err is set once the reader is done.
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
			case '\n':
				return s.finish(cur)
			case '\r':
				s.afterCR = true
				return s.finish(cur)
			}
			cur = append(cur, c)
			if int64(len(cur)) > relayMaxBody {
				return "", nil, errSSELineTooLong
			}
		}
		if s.err != nil {
			if len(cur) > 0 {
				cur = s.bom(cur)
				return string(cur), cur, s.err
			}
			return "", nil, s.err
		}
		n, err := s.r.Read(s.buf)
		s.start, s.end, s.err = 0, n, err
	}
}

// finish ends a line. Every terminator is passed on as LF: a CR followed later by an LF must not
// read as two line ends to the CLI.
func (s *sseLines) finish(cur []byte) (string, []byte, error) {
	cur = s.bom(cur)
	return string(cur), append(append([]byte(nil), cur...), '\n'), nil
}

// bom drops a byte-order mark at the very start of the stream, from what is passed on too: the
// relay's own stream to the CLI may have begun earlier, and a BOM inside it is not one.
func (s *sseLines) bom(cur []byte) []byte {
	if !s.began {
		s.began = true
		cur = bytes.TrimPrefix(cur, []byte("\uFEFF"))
	}
	return cur
}

// sameJSONID compares two JSON-RPC ids exactly: a string never equals a number, and numbers are
// compared as written, never through float64.
func sameJSONID(a, b json.RawMessage) bool {
	norm := func(r json.RawMessage) (string, bool) {
		dec := json.NewDecoder(bytes.NewReader(r))
		dec.UseNumber()
		var v any
		if dec.Decode(&v) != nil {
			return "", false
		}
		switch x := v.(type) {
		case string:
			return "s:" + x, true
		case json.Number:
			return "n:" + x.String(), true
		}
		return "", false
	}
	x, ok1 := norm(a)
	y, ok2 := norm(b)
	return ok1 && ok2 && x == y
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
// with or without labels): an approval. Anything else (free text, sampling, roots, a URL to open)
// is refused, since a person answering a card cannot give it. A result with only a requestState
// has no inputs and is retried at once.
func parseInputRequired(result json.RawMessage) (inputRequired, error) {
	var res struct {
		InputRequests map[string]struct {
			Method string `json:"method"`
			Params struct {
				Mode            string `json:"mode"`
				Message         string `json:"message"`
				RequestedSchema struct {
					Type       string                     `json:"type"`
					Properties map[string]json.RawMessage `json:"properties"`
					Required   []string                   `json:"required"`
				} `json:"requestedSchema"`
			} `json:"params"`
		} `json:"inputRequests"`
		RequestState json.RawMessage `json:"requestState"`
	}
	if err := json.Unmarshal(result, &res); err != nil {
		return inputRequired{}, errors.New("an unreadable input request")
	}
	out := inputRequired{}
	if len(res.RequestState) > 0 {
		var s string
		if json.Unmarshal(res.RequestState, &s) != nil {
			return inputRequired{}, errors.New("a requestState that is not a string")
		}
		out.requestState = res.RequestState
	}
	if len(res.InputRequests) == 0 && out.requestState == nil {
		return inputRequired{}, errors.New("no input request")
	}
	for key, req := range res.InputRequests {
		if req.Method != "elicitation/create" {
			return inputRequired{}, fmt.Errorf("a %s request", cut(req.Method, elicitMaxText))
		}
		if req.Params.Mode != "" && req.Params.Mode != "form" {
			return inputRequired{}, fmt.Errorf("a %s-mode elicitation", cut(req.Params.Mode, elicitMaxText))
		}
		schema := req.Params.RequestedSchema
		if schema.Type != "object" || len(schema.Properties) == 0 {
			return inputRequired{}, errors.New("a form with no fields")
		}
		in := ElicitInput{Key: key, Message: cut(req.Params.Message, elicitMaxMessage)}
		required := map[string]bool{}
		for _, n := range schema.Required {
			required[n] = true
		}
		for name, raw := range schema.Properties {
			f, err := parseChoiceField(name, raw)
			if err != nil {
				return inputRequired{}, err
			}
			f.Required = required[name]
			in.Fields = append(in.Fields, f)
		}
		sort.Slice(in.Fields, func(i, j int) bool { return in.Fields[i].Name < in.Fields[j].Name })
		out.inputs = append(out.inputs, in)
	}
	sort.Slice(out.inputs, func(i, j int) bool { return out.inputs[i].Key < out.inputs[j].Key })
	return out, nil
}

// parseChoiceField reads one string property that is a choice: enum (with optional enumNames
// labels, Google's form) or oneOf of const/title. Every value must be a string the server wrote;
// a null or a missing const is refused rather than read as "".
func parseChoiceField(name string, raw json.RawMessage) (ElicitField, error) {
	var p struct {
		Type      string            `json:"type"`
		Title     string            `json:"title"`
		Enum      []json.RawMessage `json:"enum"`
		EnumNames []json.RawMessage `json:"enumNames"`
		OneOf     []struct {
			Const json.RawMessage `json:"const"`
			Title string          `json:"title"`
		} `json:"oneOf"`
	}
	if json.Unmarshal(raw, &p) != nil || p.Type != "string" {
		return ElicitField{}, errors.New("a field that is not a choice")
	}
	str := func(r json.RawMessage) (string, bool) {
		var s string
		return s, len(r) > 0 && r[0] == '"' && json.Unmarshal(r, &s) == nil
	}
	f := ElicitField{Name: name, Title: cut(p.Title, elicitMaxText)}
	switch {
	case len(p.Enum) > 0:
		for i, r := range p.Enum {
			v, ok := str(r)
			if !ok {
				return ElicitField{}, errors.New("a choice that is not a string")
			}
			label := v
			if i < len(p.EnumNames) {
				if l, ok := str(p.EnumNames[i]); ok && l != "" {
					label = l
				}
			}
			f.Options = append(f.Options, ElicitOption{Value: v, Label: cut(label, elicitMaxText)})
		}
	case len(p.OneOf) > 0:
		for _, o := range p.OneOf {
			v, ok := str(o.Const)
			if !ok {
				return ElicitField{}, errors.New("a choice that is not a string")
			}
			label := o.Title
			if label == "" {
				label = v
			}
			f.Options = append(f.Options, ElicitOption{Value: v, Label: cut(label, elicitMaxText)})
		}
	default:
		return ElicitField{}, errors.New("a free-text field")
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
// every input, even one whose fields are all optional (then with an empty map).
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
	// gone is closed when the CLI's request ends.
	gone   <-chan struct{}
	answer chan ElicitAnswer
}

type elicitStore struct {
	mu      sync.Mutex
	pending map[string]*pendingElicit
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
			Arguments: call.params["arguments"], Inputs: ir.inputs, CreatedAt: now, Deadline: now.Add(elicitWait),
		},
		gone:   ctx.Done(),
		answer: make(chan ElicitAnswer, 1),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pending) >= elicitMaxPending {
		return nil, errors.New("too many calls are already waiting for a person's confirmation; the call was not completed")
	}
	s.pending[p.ID] = p
	return p, nil
}

func (s *elicitStore) remove(id string) { s.take(id) }

// take removes a held call and reports whether it was still waiting: false means an answer was
// already handed to it.
func (s *elicitStore) take(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.pending[id]
	delete(s.pending, id)
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
// takes one answer, and none once its deadline has passed or its CLI has gone.
func AnswerElicitation(id string, a ElicitAnswer) error {
	s := elicitations
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pending[id]
	if !ok {
		return ErrElicitationNotFound
	}
	select {
	case <-p.gone:
		return ErrElicitationNotFound
	default:
	}
	if !time.Now().Before(p.Deadline) {
		return ErrElicitationNotFound
	}
	if !a.Decline {
		clean, err := checkChoices(p.Inputs, a.Choices)
		if err != nil {
			return err
		}
		a.Choices = clean
	} else {
		a.Choices = nil
	}
	delete(s.pending, id)
	p.answer <- a
	return nil
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
		got, answered := choices[in.Key]
		if !answered {
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
