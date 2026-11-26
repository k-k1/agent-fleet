package mcpreg

// Form elicitation through the relay (ADR 0146 decision 6, #5174).
//
// For a definition flagged Elicitation "relay", the relay reads JSON-RPC instead of passing bytes:
// it declares form elicitation on the CLI's behalf, and when a tools/call result is
// input_required it holds the CLI's request, asks a person through the pending list below, and
// retries the identical call with the server's requestState and the person's answer. The CLI only
// ever sees a complete result.
//
// Rules that must not erode:
//
//   - Only a person's answer is ever sent as "accept". The relay itself answers nothing but
//     "decline" (time limit, or a person declining) and never invents a choice.
//   - The arguments are passed on byte for byte: a changed argument would void the server's
//     approval, and the person is shown exactly what the server will run.
//   - Only requests of the 2026-07-28 era (a protocolVersion in _meta) are read. An older-era
//     exchange is relayed as bytes, as for every other definition.
//   - Nothing about the arguments or the answer's content is logged.

import (
	"bufio"
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
	// elicitWait is how long a held call waits for a person before it is declined. It has to stay
	// under what the CLIs wait for a tool call (measured on 2026-11-26, recorded in the PR of
	// #5174); a var so a test can shorten it.
	elicitWait = 10 * time.Minute
	// elicitKeepalive is how often a held call that streams to the CLI sends something: a
	// progress notification resets a CLI's request timeout where the CLI supports it (opencode),
	// and any byte keeps idle-connection timers from closing the stream.
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

// elicitCall is one 2026-07-28-era request, with form elicitation declared in its _meta.
type elicitCall struct {
	method string
	id     json.RawMessage
	tool   string
	top    map[string]json.RawMessage
	params map[string]json.RawMessage
	// progressToken is the CLI's own token, nil when it sent none.
	progressToken json.RawMessage
	body          []byte
}

// parseElicitRequest reads body as a 2026-07-28-era JSON-RPC request and sets form elicitation
// as the client capability it declares. ok is false for anything else, which is then relayed
// unchanged.
//
// The capability is replaced, not merged: the relay answers form elicitation only, so a "url"
// capability the CLI declared would invite a request nobody here can answer.
func parseElicitRequest(body []byte) (elicitCall, bool) {
	var c elicitCall
	if json.Unmarshal(body, &c.top) != nil || json.Unmarshal(c.top["method"], &c.method) != nil || c.method == "" {
		return elicitCall{}, false
	}
	if json.Unmarshal(c.top["params"], &c.params) != nil || c.params == nil {
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
	c.top["params"] = rawObject(c.params)
	c.id = c.top["id"]
	c.progressToken = meta["progressToken"]
	if c.method == "tools/call" {
		if len(c.id) == 0 || json.Unmarshal(c.params["name"], &c.tool) != nil {
			return elicitCall{}, false
		}
	}
	c.body = rawObject(c.top)
	return c, true
}

// retryBody is the same call with the server's requestState, unchanged, and the answers.
func (c elicitCall) retryBody(requestState, responses json.RawMessage) []byte {
	params := make(map[string]json.RawMessage, len(c.params)+2)
	for k, v := range c.params {
		params[k] = v
	}
	params["requestState"] = requestState
	params["inputResponses"] = responses
	top := make(map[string]json.RawMessage, len(c.top))
	for k, v := range c.top {
		top[k] = v
	}
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

// serveElicitingCall relays one tools/call of a flagged definition, holding it while a person
// answers each input_required result.
func serveElicitingCall(w http.ResponseWriter, r *http.Request, def ServerDef, target *url.URL, call elicitCall) {
	out := &elicitOut{w: w, r: r, id: call.id, acceptsSSE: acceptsEventStream(r.Header.Values("Accept"))}
	body := call.body
	for round := 0; ; round++ {
		resp, p := relayExchange(r.Context(), r, def.ID, target, body)
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
		responses, ok := holdForAnswer(r.Context(), out, def, call, ir)
		if !ok {
			return
		}
		body = call.retryBody(ir.requestState, responses)
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
	id         json.RawMessage
	acceptsSSE bool
	streaming  bool
	progress   int
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
	o.w.WriteHeader(http.StatusOK)
	_ = http.NewResponseController(o.w).Flush()
	o.streaming = true
}

func (o *elicitOut) event(data []byte) bool {
	var b bytes.Buffer
	b.WriteString("event: message\n")
	for _, line := range bytes.Split(data, []byte("\n")) {
		b.WriteString("data: ")
		b.Write(line)
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	return o.raw(b.Bytes())
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
	msg := mustJSON(map[string]json.RawMessage{"jsonrpc": json.RawMessage(`"2.0"`), "id": o.id, "result": result})
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
	o.event(mustJSON(map[string]any{"jsonrpc": "2.0", "id": o.id,
		"error": map[string]any{"code": -32603, "message": "Agent Fleet: " + p.msg}}))
}

// keepalive is sent while a held call streams: a progress notification when the CLI gave a token,
// an SSE comment otherwise.
func (o *elicitOut) keepalive(token json.RawMessage) bool {
	if !o.streaming {
		return true
	}
	if len(token) == 0 {
		return o.raw([]byte(": waiting for a person to answer in the Agent Fleet Console\n\n"))
	}
	o.progress++
	return o.event(mustJSON(map[string]any{"jsonrpc": "2.0", "method": "notifications/progress", "params": map[string]any{
		"progressToken": token, "progress": o.progress, "message": "Waiting for a person to answer in the Agent Fleet Console",
	}}))
}

// pass relays the server's answer to the CLI, all of it except an input_required result for the
// call, which it returns instead (held, ok=true). ok=false means the call is over: the answer was
// passed on whole, or it failed and the CLI was told.
func (o *elicitOut) pass(serverID string, resp *http.Response) (held json.RawMessage, ok bool) {
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusForbidden {
			noteInsufficientScope(serverID, resp.Header.Values("Www-Authenticate"))
		}
		if !o.streaming {
			if resp.StatusCode >= 300 && resp.StatusCode < 400 {
				relayFail(o.w, http.StatusBadGateway, relayRedirectMsg)
				return nil, false
			}
			relayCopyResponse(o.w, o.r, resp)
			return nil, false
		}
		o.fail(&relayProblem{http.StatusBadGateway, fmt.Sprintf("the MCP server answered the confirmed call with HTTP %d", resp.StatusCode)})
		return nil, false
	}
	mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	switch mt {
	case "application/json":
		b, err := io.ReadAll(io.LimitReader(resp.Body, relayMaxBody+1))
		if err != nil || int64(len(b)) > relayMaxBody {
			if err != nil && o.r.Context().Err() != nil {
				return nil, false
			}
			o.fail(&relayProblem{http.StatusBadGateway, "the MCP server's answer could not be read"})
			return nil, false
		}
		if res, ok := o.inputRequired(b); ok {
			return res, true
		}
		if o.streaming {
			o.event(bytes.TrimSpace(b))
			return nil, false
		}
		copyResponseHeaders(o.w.Header(), resp.Header)
		o.w.Header().Del("Content-Length")
		o.w.WriteHeader(http.StatusOK)
		_, _ = o.w.Write(b)
		return nil, false
	case "text/event-stream":
		o.startStream(resp.Header)
		return o.passStream(resp.Body)
	default:
		if o.streaming {
			o.fail(&relayProblem{http.StatusBadGateway, "the MCP server answered the confirmed call with an unexpected content type"})
			return nil, false
		}
		relayCopyResponse(o.w, o.r, resp)
		return nil, false
	}
}

// passStream forwards a server's event stream event by event, keeping back the one that carries
// an input_required result for the call.
func (o *elicitOut) passStream(body io.Reader) (json.RawMessage, bool) {
	rd := bufio.NewReaderSize(body, 32<<10)
	var ev bytes.Buffer
	var data [][]byte
	for {
		line, err := rd.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			// A line longer than the buffer: keep reading it whole.
			full := append([]byte(nil), line...)
			for errors.Is(err, bufio.ErrBufferFull) {
				line, err = rd.ReadSlice('\n')
				full = append(full, line...)
				if int64(len(full)) > relayMaxBody {
					o.fail(&relayProblem{http.StatusBadGateway, "the MCP server's answer could not be read"})
					return nil, false
				}
			}
			line = full
		}
		if len(line) > 0 {
			ev.Write(line)
			trimmed := bytes.TrimRight(line, "\r\n")
			if len(trimmed) == 0 {
				if res, ok := o.inputRequired(bytes.Join(data, []byte("\n"))); ok {
					return res, true
				}
				if !o.raw(ev.Bytes()) {
					return nil, false
				}
				ev.Reset()
				data = nil
			} else if v, ok := bytes.CutPrefix(trimmed, []byte("data:")); ok {
				data = append(data, append([]byte(nil), bytes.TrimPrefix(v, []byte(" "))...))
			}
		}
		if err == io.EOF {
			if ev.Len() > 0 {
				if res, ok := o.inputRequired(bytes.Join(data, []byte("\n"))); ok {
					return res, true
				}
				o.raw(ev.Bytes())
			}
			return nil, false
		}
		if err != nil {
			if o.r.Context().Err() == nil {
				// As relayCopyResponse: a broken upstream stream ends the CLI's abnormally.
				panic(http.ErrAbortHandler)
			}
			return nil, false
		}
	}
}

// inputRequired returns the result of msg when it is the response to the call and says
// input_required.
func (o *elicitOut) inputRequired(msg []byte) (json.RawMessage, bool) {
	var m struct {
		ID     json.RawMessage `json:"id"`
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(msg, &m) != nil || len(m.Result) == 0 || !sameJSONID(m.ID, o.id) {
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

func sameJSONID(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return fmt.Sprintf("%T:%v", x, x) == fmt.Sprintf("%T:%v", y, y)
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
	requestState json.RawMessage
	inputs       []ElicitInput
}

// parseInputRequired accepts form elicitations whose fields are all choices (an enum, with or
// without labels): an approval. Anything else (free text, sampling, a URL to open) is refused,
// since a person answering a card cannot give it.
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
	if len(res.InputRequests) == 0 {
		return inputRequired{}, errors.New("no input request")
	}
	out := inputRequired{requestState: res.RequestState}
	if len(out.requestState) == 0 {
		out.requestState = json.RawMessage("null")
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
// labels, Google's form) or oneOf of const/title.
func parseChoiceField(name string, raw json.RawMessage) (ElicitField, error) {
	var p struct {
		Type      string   `json:"type"`
		Title     string   `json:"title"`
		Enum      []string `json:"enum"`
		EnumNames []string `json:"enumNames"`
		OneOf     []struct {
			Const string `json:"const"`
			Title string `json:"title"`
		} `json:"oneOf"`
	}
	if json.Unmarshal(raw, &p) != nil || p.Type != "string" {
		return ElicitField{}, errors.New("a field that is not a choice")
	}
	f := ElicitField{Name: name, Title: cut(p.Title, elicitMaxText)}
	switch {
	case len(p.Enum) > 0:
		for i, v := range p.Enum {
			label := v
			if i < len(p.EnumNames) && p.EnumNames[i] != "" {
				label = p.EnumNames[i]
			}
			f.Options = append(f.Options, ElicitOption{Value: v, Label: cut(label, elicitMaxText)})
		}
	case len(p.OneOf) > 0:
		for _, o := range p.OneOf {
			label := o.Title
			if label == "" {
				label = o.Const
			}
			f.Options = append(f.Options, ElicitOption{Value: o.Const, Label: cut(label, elicitMaxText)})
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
	p, err := elicitations.add(def, call, ir)
	if err != nil {
		out.result(errorResult(err.Error()))
		return nil, false
	}
	defer elicitations.remove(p.ID)
	if out.acceptsSSE {
		out.startStream(nil)
	}
	timeout := time.NewTimer(elicitWait)
	defer timeout.Stop()
	tick := time.NewTicker(elicitKeepalive)
	defer tick.Stop()
	var ans ElicitAnswer
	for done := false; !done; {
		select {
		case ans = <-p.answer:
			done = true
		case <-timeout.C:
			// Taken off the list first: an answer that won the race is the one sent.
			if elicitations.take(p.ID) {
				ans, done = ElicitAnswer{Decline: true}, true
				log.Printf("mcp relay: %s: no answer to %s's confirmation within %s; declining", def.ID, call.tool, elicitWait)
			} else {
				ans, done = <-p.answer, true
			}
		case <-tick.C:
			if !out.keepalive(call.progressToken) {
				return nil, false
			}
		case <-ctx.Done():
			return nil, false
		}
	}
	responses := map[string]any{}
	for _, in := range ir.inputs {
		if ans.Decline {
			responses[in.Key] = map[string]string{"action": "decline"}
			continue
		}
		responses[in.Key] = map[string]any{"action": "accept", "content": ans.Choices[in.Key]}
	}
	action := "accept"
	if ans.Decline {
		action = "decline"
	}
	log.Printf("mcp relay: %s: confirmation of %s answered: %s", def.ID, call.tool, action)
	return mustJSON(responses), true
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

// ElicitAnswer is a person's answer: decline, or a choice per input and field.
type ElicitAnswer struct {
	Decline bool
	Choices map[string]map[string]string
}

var (
	// ErrElicitationNotFound: no held call has that id (answered, timed out, or the CLI left).
	ErrElicitationNotFound = errors.New("no pending confirmation with that id")
	// ErrElicitationAnswer: the answer does not fit the server's form.
	ErrElicitationAnswer = errors.New("the answer does not match the server's choices")
)

type pendingElicit struct {
	PendingElicitation
	answer chan ElicitAnswer
}

type elicitStore struct {
	mu      sync.Mutex
	pending map[string]*pendingElicit
}

var elicitations = &elicitStore{pending: map[string]*pendingElicit{}}

func (s *elicitStore) add(def ServerDef, call elicitCall, ir inputRequired) (*pendingElicit, error) {
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
		out = append(out, p.PendingElicitation)
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// AnswerElicitation hands a person's answer to the held call. An accept must choose one of the
// server's options for every required field and may name nothing else. The call takes one answer;
// a second one finds nothing.
func AnswerElicitation(id string, a ElicitAnswer) error {
	s := elicitations
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pending[id]
	if !ok {
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
	known := map[string]ElicitInput{}
	for _, in := range inputs {
		known[in.Key] = in
	}
	for key := range choices {
		if _, ok := known[key]; !ok {
			return nil, ErrElicitationAnswer
		}
	}
	out := map[string]map[string]string{}
	for _, in := range inputs {
		got := choices[in.Key]
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
