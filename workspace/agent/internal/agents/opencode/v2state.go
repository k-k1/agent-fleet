package opencode

// Session state on opencode 2, read through the serve API instead of opencode.db.
//
// v2 migrates the v1 database in place and never writes the v1 tables (message, part,
// session_input, todo) again; its own data lives in session_v2 / session_message /
// session_inbox / session_pending. Reading those would tie us to a private schema upstream
// says not to touch, so every read here goes through /api/*. The v1 SQLite path in
// transcript.go stays the default and is untouched.
//
// Measured on 2.0.28 against a stub OpenAI-compatible provider (testdata/v2/):
//   - GET /api/session/{id}/message returns typed messages: user, assistant (a list of
//     text / reasoning / tool content, cost and tokens per STEP), idle (one per execution,
//     with outcome succeeded|failed|interrupted and the error on a failure), compaction
//     (status running|completed|failed, with its own cost), plus framing (model-switched,
//     shell, synthetic). Pages hold 200 at most; follow cursor.next until a page comes back
//     empty or short.
//   - One user turn is several assistant messages (one per model step), where v1 stored one
//     message per turn. v1 filled `cost` by summing the steps and `tokens` from the last one;
//     v2Turns merges a run of assistant messages back into that shape so parseMessage and
//     the usage fold keep reading what they always read.
//   - A provider failure is explicit: session.step.failed, then session.execution.failed
//     carrying {type, message, status, response.body}; the assistant message carries the same
//     error and the idle message has outcome "failed". A 4xx fails at once; a 5xx is retried
//     (session.retry.scheduled) for about ninety seconds before it fails. An interrupt ends
//     with session.execution.interrupted and an idle message with outcome "interrupted", no
//     error — the stop is never mistaken for a failure.
//   - Compaction is session.compaction.started then .ended or .failed. A failed one still
//     ends its execution as succeeded, so the failure is only visible on the compaction
//     message. Its cost is on the compaction message, not on any assistant message.
//   - Input typed while a turn runs waits in GET /api/session/{id}/inbox ({payload.text});
//     an interrupt leaves it there.
//   - There is no todo route, event or table in the v2 API (measured on 2.1.4: none in /openapi.json).
//     The `todowrite` tool exists there only behind the experimental.todo config flag, which
//     defaults to false; with it off a call fails ("No tool named todowrite is currently
//     available"). With it on the list is the newest completed todowrite call's input (v2Tasks),
//     which is also how the opencode 2 TUI derives it. So Tasks is empty on a default config.
//   - The question tool is a form there (v2form.go): pending questions come from the form list.
//   - Fork copies keep time.created but get new ids, so a message older than its session is
//     a copy whose cost the source session already reported (same rule as v1).
//   - The event stream is global and every event names its session; a subagent's execution
//     events carry the child's id and must not touch the parent's handle.
//
// Where the daemon is and how to log in is apiTarget's business (v2TargetFor), so the Terminal
// route can be plugged in later without touching the readers.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// apiTarget is a v2 daemon the state readers talk to: its address and a client that already
// carries its credentials.
type apiTarget struct {
	Addr   string
	Client *http.Client
	// offline reads the conversation from opencode.db instead (v2store.go): the daemon is gone,
	// so no request can be made. Addr and Client are empty then.
	offline bool
}

// v2TargetFor says where slot m's state is read from, or false when it is read from opencode.db.
// A managed session is served by af's own supervised daemon (serveapi.go's client already adds
// the Basic auth), and so is a Terminal one whose TUI is attached to it (sharedTUI). A
// --standalone Terminal session keeps its state in the shared database file only, which is
// still the v1 reader's path.
var v2TargetFor = func(m session.Meta) (apiTarget, bool) {
	if !v2Served() {
		return apiTarget{}, false
	}
	if m.DriverKind() == session.DriverManaged || sharedSlot(m) {
		return apiTarget{Addr: serveAddr(), Client: serveClient}, true
	}
	return apiTarget{}, false
}

// statusError is a non-200 answer.
type statusError int

func (e statusError) Error() string { return fmt.Sprintf("opencode api: HTTP %d", int(e)) }

// v2StateTimeout bounds one state read, and v2DownFor how long a daemon that failed one is not
// asked again. The session list reads state synchronously, one slot after another, so without a
// shared verdict a hung daemon costs every slot (and every concurrent poll) a full client timeout.
var (
	v2StateTimeout = 3 * time.Second
	v2DownFor      = 2 * time.Second
)

var v2Down = struct {
	mu    sync.Mutex
	until map[string]time.Time
}{until: map[string]time.Time{}}

var errV2Down = errors.New("opencode api: daemon recently unreachable")

// get is a state read: short, and refused at once while the daemon is known to be down. A
// transport failure (not an HTTP status) marks it down for v2DownFor.
func (t apiTarget) get(path string, out any) error {
	if t.offline {
		return errV2Offline
	}
	v2Down.mu.Lock()
	down := time.Now().Before(v2Down.until[t.Addr])
	v2Down.mu.Unlock()
	if down {
		return errV2Down
	}
	ctx, cancel := context.WithTimeout(context.Background(), v2StateTimeout)
	defer cancel()
	err := t.getCtx(ctx, path, out)
	if daemonGone(err) { // an answer that does not parse is not a daemon that is down
		v2Down.mu.Lock()
		v2Down.until[t.Addr] = time.Now().Add(v2DownFor)
		v2Down.mu.Unlock()
	}
	return err
}

// getTurn is the read a waiting turn makes: the client's own timeout, and no down verdict, since
// the turn counts its own consecutive failures.
func (t apiTarget) getTurn(path string, out any) error {
	if t.offline {
		return errV2Offline
	}
	return t.getCtx(context.Background(), path, out)
}

func (t apiTarget) getCtx(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", t.Addr+path, nil)
	if err != nil {
		return err
	}
	res, err := t.Client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return statusError(res.StatusCode)
	}
	// The body is read whole before it is parsed: a read that fails is a connection cut
	// (bodyReadError), while a body that arrives complete and does not parse is the daemon's answer.
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return bodyReadError{err}
	}
	return json.Unmarshal(body, out)
}

// bodyReadError is an answer whose body could not be read to its end.
type bodyReadError struct{ error }

func (e bodyReadError) Unwrap() error { return e.error }

// --- wire shapes ----------------------------------------------------------------

// v2Err is Session.StructuredError.
type v2Err struct {
	Type     string `json:"type"`
	Message  string `json:"message"`
	Status   int    `json:"status"`
	Response struct {
		Body string `json:"body"`
	} `json:"response"`
}

// messageError maps the error onto the shape the rest of the package renders and classifies.
// Name is v2's own type ("provider.auth", "provider.internal"), so the label says what failed.
// Retryable follows the status: a 5xx and a 429 are the provider's to recover from, and v2 has
// already retried them before it reports a failure.
func (e v2Err) messageError() messageError {
	var me messageError
	me.Name = strings.TrimSpace(e.Type)
	switch me.Name {
	case "":
		me.Name = "error"
	case "aborted":
		// An interrupted step ends with this error (measured), the same stop v1 records as
		// MessageAbortedError: not a failure, and not shown as one.
		me.Name = abortedErrorName
	}
	me.Data.Message = e.Message
	me.Data.StatusCode = e.Status
	me.Data.IsRetryable = e.Status == 429 || e.Status >= 500
	me.Data.ResponseBody = e.Response.Body
	return me
}

type v2Tokens struct {
	Input     int `json:"input"`
	Output    int `json:"output"`
	Reasoning int `json:"reasoning"`
	Cache     struct {
		Read  int `json:"read"`
		Write int `json:"write"`
	} `json:"cache"`
}

type v2ModelRef struct {
	ID         string `json:"id"`
	ProviderID string `json:"providerID"`
	Variant    string `json:"variant"`
}

type v2Content struct {
	Type  string `json:"type"` // text | reasoning | tool
	Text  string `json:"text"`
	Name  string `json:"name"`
	State struct {
		Status  string          `json:"status"` // streaming | running | completed | error
		Input   json.RawMessage `json:"input"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Error    *v2Err `json:"error"`
		Metadata struct {
			SessionID string `json:"sessionID"`
		} `json:"metadata"`
	} `json:"state"`
}

type v2Msg struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Time struct {
		Created   int64 `json:"created"`
		Completed int64 `json:"completed"`
	} `json:"time"`
	Text    string      `json:"text"` // user
	Agent   string      `json:"agent"`
	Model   v2ModelRef  `json:"model"`
	Content []v2Content `json:"content"`
	Cost    float64     `json:"cost"`
	Tokens  v2Tokens    `json:"tokens"`
	Error   *v2Err      `json:"error"`
	Outcome string      `json:"outcome"` // idle
	Status  string      `json:"status"`  // compaction
}

type v2SessionInfo struct {
	ID       string     `json:"id"`
	ParentID string     `json:"parentID"`
	Agent    string     `json:"agent"`
	Model    v2ModelRef `json:"model"`
	Time     struct {
		Created int64 `json:"created"`
		Updated int64 `json:"updated"`
	} `json:"time"`
	Location struct {
		Directory string `json:"directory"`
	} `json:"location"`
}

// --- reads ----------------------------------------------------------------------

func sessionPath(ses string) string { return "/api/session/" + url.PathEscape(ses) }

// v2Session resolves the slot's opencode conversation: the id recorded for the slot (the driver
// writes it on create), else the newest root conversation in its directory that this slot itself
// can have opened (created after it, like activeSession on v1). nil when there is none yet.
func (t apiTarget) v2Session(m session.Meta) (*v2SessionInfo, error) {
	if t.offline {
		return storeSession(m)
	}
	if id := sids.Read(session.UUID(m.Dir, m.Name)); id != "" {
		switch info, err := t.sessionInfo(id); {
		case err == nil:
			return info, nil
		case !errors.Is(err, statusError(http.StatusNotFound)):
			return nil, err
		}
	}
	var w struct {
		Data []v2SessionInfo `json:"data"`
	}
	if err := t.get("/api/session?limit=100&order=desc&directory="+url.QueryEscape(m.CWD()), &w); err != nil {
		return nil, err
	}
	since := metaCreatedMs(m)
	var best *v2SessionInfo
	for i := range w.Data {
		s := &w.Data[i]
		if s.ParentID != "" || s.Time.Created < since {
			continue
		}
		if best == nil || s.Time.Updated > best.Time.Updated {
			best = s
		}
	}
	return best, nil
}

// sessionInfo is the record of conversation id.
func (t apiTarget) sessionInfo(id string) (*v2SessionInfo, error) {
	if t.offline {
		return storeSessionByID(id)
	}
	var w struct {
		Data v2SessionInfo `json:"data"`
	}
	if err := t.get(sessionPath(id), &w); err != nil {
		return nil, err
	}
	return &w.Data, nil
}

const v2PageSize = 200

// messages reads a whole conversation, oldest first.
func (t apiTarget) messages(ses string) ([]v2Msg, error) {
	if t.offline {
		return storeMessages(ses)
	}
	var all []v2Msg
	path := sessionPath(ses) + "/message?order=asc&limit=" + fmt.Sprint(v2PageSize)
	for page := 0; page < 1000; page++ {
		var w struct {
			Data   []v2Msg `json:"data"`
			Cursor struct {
				Next string `json:"next"`
			} `json:"cursor"`
		}
		if err := t.get(path, &w); err != nil {
			return all, err
		}
		all = append(all, w.Data...)
		if len(w.Data) < v2PageSize || w.Cursor.Next == "" {
			break
		}
		path = sessionPath(ses) + "/message?limit=" + fmt.Sprint(v2PageSize) + "&cursor=" + url.QueryEscape(w.Cursor.Next)
	}
	return all, nil
}

// v2ActiveTTL keeps the session list's several readers (one per opencode session, every poll)
// to one request. A var so a test can turn it off.
var v2ActiveTTL = 500 * time.Millisecond

var v2ActiveCache struct {
	mu   sync.Mutex
	addr string
	at   time.Time
	m    map[string]bool
	err  error
}

// active is the set of sessions the daemon is running; an idle session is not listed (measured).
// The answer, a failure included, is shared for v2ActiveTTL; the lock is not held over the request.
func (t apiTarget) active() (map[string]bool, error) {
	c := &v2ActiveCache
	c.mu.Lock()
	if c.addr == t.Addr && time.Since(c.at) < v2ActiveTTL && (c.m != nil || c.err != nil) {
		m, err := c.m, c.err
		c.mu.Unlock()
		return m, err
	}
	c.mu.Unlock()
	var w struct {
		Data map[string]struct {
			Type string `json:"type"`
		} `json:"data"`
	}
	err := t.get("/api/session/active", &w)
	var m map[string]bool
	if err == nil {
		m = make(map[string]bool, len(w.Data))
		for id, s := range w.Data {
			m[id] = s.Type != "" && s.Type != "idle"
		}
	}
	c.mu.Lock()
	c.addr, c.at, c.m, c.err = t.Addr, time.Now(), m, err
	c.mu.Unlock()
	return m, err
}

// liveStateV2 is liveState on the API: working while the daemon lists the session as running,
// idle otherwise. "" (unknown, fall back to the status plugin) when the daemon cannot be asked —
// the same refusal to claim idle from no information that LiveState documents.
func liveStateV2(t apiTarget, m session.Meta) string {
	info, err := t.v2Session(m)
	if err != nil {
		return ""
	}
	if info == nil {
		return "idle" // no conversation yet — sitting at the composer
	}
	act, err := t.active()
	if err != nil {
		return ""
	}
	if act[info.ID] {
		// A turn waiting on a form is active as far as /active is concerned (measured), so the
		// form list is what tells a question from work. Asked only for a running session.
		if len(pendingFormsV2(t, m, info.ID)) > 0 {
			return "question"
		}
		return "working"
	}
	return "idle"
}

// compactingV2: a compaction is being written for the slot's conversation. Under Managed the
// driver's event-fed flag (session.compaction.started/ended/failed) is the answer, and every turn
// end clears it; with no handle the newest compaction message's status says it.
func compactingV2(t apiTarget, m session.Meta) bool {
	if h := handleFor(m.Name); h != nil {
		return h.isCompacting()
	}
	info, err := t.v2Session(m)
	if err != nil || info == nil {
		return false
	}
	act, err := t.active()
	if err != nil || !act[info.ID] {
		return false
	}
	var w struct {
		Data []v2Msg `json:"data"`
	}
	if t.get(sessionPath(info.ID)+"/message?type=compaction&order=desc&limit=1", &w) != nil || len(w.Data) == 0 {
		return false
	}
	return w.Data[0].Status == "running"
}

// queuedV2 lists the input waiting in the session's inbox, in arrival order. Only user input is
// shown; a queued compaction or move is not something the member typed.
func queuedV2(t apiTarget, ses string) []string {
	var w struct {
		Data []struct {
			Type    string `json:"type"`
			Payload struct {
				Text string `json:"text"`
			} `json:"payload"`
		} `json:"data"`
	}
	if t.get(sessionPath(ses)+"/inbox", &w) != nil {
		return nil
	}
	var out []string
	for _, it := range w.Data {
		if s := strings.TrimSpace(it.Payload.Text); it.Type == "user" && s != "" {
			out = append(out, s)
		}
	}
	return out
}

func readTranscriptV2(t apiTarget, m session.Meta) agents.TranscriptData {
	c, err := t.conversation(m)
	t, info, msgs := c.T, c.Info, c.Msgs
	if err != nil || info == nil {
		td := agents.TranscriptData{Path: t.Addr}
		managedEnrich(m, &td)
		return td
	}
	turns := v2Turns(c, false)
	td := agents.TranscriptData{
		Turns:      turns,
		Path:       t.Addr + sessionPath(info.ID),
		Mode:       v2Mode(info, msgs),
		Queued:     queuedV2(t, info.ID),
		Compacting: compactingV2(t, m),
		Tasks:      v2Tasks(msgs),
	}
	td.Pending = questionsV2(pendingFormsV2(t, m, info.ID))
	managedEnrich(m, &td)
	return td
}

// v2Tasks is the ToDo list: the todos of the newest completed `todowrite` call (the tool resends
// the whole list every time, so the latest wins). nil when the conversation has none, which is
// every conversation on a default config (the tool is behind experimental.todo, v2state.go header).
func v2Tasks(msgs []v2Msg) []transcript.Task {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Type != "assistant" {
			continue
		}
		for j := len(msgs[i].Content) - 1; j >= 0; j-- {
			c := msgs[i].Content[j]
			if c.Type != "tool" || c.Name != "todowrite" || c.State.Status != "completed" {
				continue
			}
			var in struct {
				Todos []struct {
					Content string `json:"content"`
					Status  string `json:"status"`
				} `json:"todos"`
			}
			if json.Unmarshal(c.State.Input, &in) != nil {
				continue
			}
			var out []transcript.Task
			for n, td := range in.Todos {
				if td.Content == "" {
					continue
				}
				st := td.Status
				if st == "" {
					st = "pending"
				}
				out = append(out, transcript.Task{ID: strconv.Itoa(n + 1), Subject: td.Content, Status: st})
			}
			return out
		}
	}
	return nil
}

// v2Mode is the conversation's current agent normalized to "plan" | "normal", from the newest
// assistant message (v1 read the newest message's agent) and else the session's own.
func v2Mode(info *v2SessionInfo, msgs []v2Msg) string {
	agent := info.Agent
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Type == "assistant" && msgs[i].Agent != "" {
			agent = msgs[i].Agent
			break
		}
	}
	switch agent {
	case "":
		return ""
	case "plan":
		return "plan"
	}
	return "normal"
}

func usageTurnsV2(t apiTarget, m session.Meta) []transcript.Turn {
	c, err := t.conversation(m)
	if err != nil || c.Info == nil {
		return nil
	}
	return v2Turns(c, true)
}

func recallSettingsV2(t apiTarget, m session.Meta) agents.RecalledSettings {
	info, err := t.v2Session(m)
	if err != nil || info == nil {
		return agents.RecalledSettings{}
	}
	var r agents.RecalledSettings
	if info.Model.ProviderID != "" && info.Model.ID != "" {
		r.Model = info.Model.ProviderID + "/" + info.Model.ID
	}
	if info.Agent == "plan" {
		r.Mode = "plan"
	} else if info.Agent != "" {
		r.Mode = "normal"
	}
	return r
}

// resolveForkAtV2 validates a mirror anchor against the slot's own conversation exactly like the
// v1 reader does: the anchor must be a message of THIS conversation, and "include" moves it to
// just before the next user message.
func resolveForkAtV2(t apiTarget, m session.Meta, at agents.ForkPoint) (string, error) {
	info, err := t.v2Session(m)
	if err != nil {
		return "", fmt.Errorf("opencode の会話ストアを読めません")
	}
	if info == nil {
		return "", fmt.Errorf("分岐できる会話がまだありません")
	}
	msgs, err := t.messages(info.ID)
	if err != nil {
		return "", fmt.Errorf("opencode の会話ストアを読めません")
	}
	idx := -1
	for i, mm := range msgs {
		if mm.ID == at.Anchor {
			idx = i
			break
		}
	}
	// A subagent turn lives in a child session, which this list does not contain, so it is
	// refused here as "not found" along with a stray id.
	if idx < 0 {
		return "", fmt.Errorf("指定された分岐点がこの会話に見つかりません")
	}
	if !at.Include {
		return at.Anchor, nil
	}
	for _, mm := range msgs[idx+1:] {
		if mm.Type == "user" {
			return mm.ID, nil
		}
	}
	return "", nil
}

// --- messages → turns -----------------------------------------------------------

// v2Stamped is a turn with the creation time it sorts by when the parent's and its subagents'
// messages are interleaved.
type v2Stamped struct {
	created int64
	turn    transcript.Turn
}

// v2Turns turns a conversation (and its subagents') into the transcript the Console renders, by
// rewriting each message in v1's stored shape and handing it to parseMessage: one reader decides
// what a turn looks like on both majors. With usage set it also returns a CostOnly turn for every
// billed message that shows nothing (an empty failed step, a compaction).
func v2Turns(c v2Conv, usage bool) []transcript.Turn {
	win := modelWindowLookup()
	cwd := c.Info.Location.Directory
	all := v2SessionTurns(c.Msgs, cwd, c.Info.Time.Created, false, usage, win)
	for _, k := range c.Kids {
		all = append(all, v2SessionTurns(k.Msgs, cwd, k.Born, true, usage, win)...)
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].created < all[j].created })
	out := make([]transcript.Turn, len(all))
	for i, s := range all {
		s.turn.Idx = i
		out[i] = s.turn
	}
	return out
}

// v2Children lists the subagent sessions the conversation's subagent tool calls started.
func v2Children(msgs []v2Msg) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range msgs {
		for _, c := range m.Content {
			if id := c.State.Metadata.SessionID; c.Type == "tool" && c.Name == "subagent" && id != "" && !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out
}

func v2SessionTurns(msgs []v2Msg, cwd string, born int64, sidechain, usage bool, win windowFn) []v2Stamped {
	var out []v2Stamped
	add := func(created int64, t transcript.Turn) {
		t.Sidechain = sidechain
		out = append(out, v2Stamped{created, t})
	}
	costOnly := func(created int64, id, model string, cost float64) {
		if usage && cost > 0 {
			add(created, transcript.Turn{Role: "assistant", CostOnly: true, CostUSD: cost,
				TS: msTS(created), Model: model, AnchorID: id})
		}
	}
	shownErr := "" // the failure the last assistant run already rendered, so its idle message does not repeat it
	for i := 0; i < len(msgs); i++ {
		m := msgs[i]
		switch m.Type {
		case "user":
			shownErr = ""
			data := v1Data(map[string]any{"role": "user", "time": map[string]any{"created": m.Time.Created}, "path": map[string]any{"cwd": cwd}})
			if t, ok := parseMessage(m.ID, data, [][]byte{v1Part(map[string]any{"type": "text", "text": m.Text})}, 0, win); ok {
				add(m.Time.Created, t)
			}
		case "assistant":
			j := i
			for j+1 < len(msgs) && msgs[j+1].Type == "assistant" {
				j++
			}
			run := msgs[i : j+1]
			i = j
			data, parts := v2AssistantRun(run, cwd)
			created := run[0].Time.Created
			copied := created > 0 && created < born
			last := run[len(run)-1]
			if last.Error != nil {
				shownErr = last.Error.Type + "\x00" + last.Error.Message
			}
			t, ok := parseMessage(run[0].ID, data, parts, 0, win)
			if !ok {
				if !copied {
					costOnly(created, run[0].ID, last.Model.ID, v2RunCost(run))
				}
				continue
			}
			if copied {
				t.CostUSD = 0
			}
			add(created, t)
		case "idle":
			// A failure with no assistant message to carry it (the model was never reached) would
			// otherwise leave no trace in the mirror; one the assistant run already shows is not
			// repeated.
			if m.Outcome == "failed" && m.Error != nil && shownErr != m.Error.Type+"\x00"+m.Error.Message {
				add(m.Time.Created, v2ErrorTurn(m.ID, m.Time.Created, cwd, m.Error, win))
			}
			shownErr = ""
		case "compaction":
			shownErr = ""
			copied := m.Time.Created > 0 && m.Time.Created < born
			if m.Status == "failed" && m.Error != nil {
				add(m.Time.Created, v2ErrorTurn(m.ID, m.Time.Created, cwd, m.Error, win))
			}
			if !copied {
				costOnly(m.Time.Created, m.ID, m.Model.ID, m.Cost)
			}
		}
	}
	return out
}

// v2ErrorTurn is a turn that holds nothing but its failure.
func v2ErrorTurn(id string, created int64, cwd string, e *v2Err, win windowFn) transcript.Turn {
	data := v1Data(map[string]any{"role": "assistant", "time": map[string]any{"created": created}, "path": map[string]any{"cwd": cwd},
		"error": e.messageError()})
	t, _ := parseMessage(id, data, nil, 0, win)
	return t
}

func v2RunCost(run []v2Msg) float64 {
	var c float64
	for _, m := range run {
		c += m.Cost
	}
	return c
}

// v2AssistantRun is one turn's assistant messages as v1's single message row plus its part rows:
// cost summed over the steps, tokens from the last one, completed only once the last step is.
func v2AssistantRun(run []v2Msg, cwd string) ([]byte, [][]byte) {
	first, last := run[0], run[len(run)-1]
	d := map[string]any{
		"role": "assistant", "modelID": last.Model.ID, "providerID": last.Model.ProviderID,
		"variant": last.Model.Variant, "agent": last.Agent, "cost": v2RunCost(run),
		"tokens": map[string]any{"input": last.Tokens.Input, "output": last.Tokens.Output,
			"cache": map[string]any{"read": last.Tokens.Cache.Read, "write": last.Tokens.Cache.Write}},
		"time": map[string]any{"created": first.Time.Created, "completed": last.Time.Completed},
		"path": map[string]any{"cwd": cwd},
	}
	var parts [][]byte
	for _, m := range run {
		if m.Error != nil {
			d["error"] = m.Error.messageError()
		}
		parts = append(parts, v2PartRows(m.Content)...)
	}
	return v1Data(d), parts
}

var subagentWrapper = regexp.MustCompile(`(?s)^\s*<subagent[^>]*>\n?(.*?)\n?</subagent>\s*$`)

// v2PartRows rewrites message content as v1 part rows. Tool names follow v1's where the
// display depends on them: v2's subagent tool is v1's task, and v2's edit/write take `path`
// where v1's took `filePath`.
func v2PartRows(content []v2Content) [][]byte {
	var rows [][]byte
	for _, c := range content {
		switch c.Type {
		case "text", "reasoning":
			rows = append(rows, v1Part(map[string]any{"type": c.Type, "text": c.Text}))
		case "tool":
			name := c.Name
			var in map[string]any
			_ = json.Unmarshal(c.State.Input, &in)
			if in == nil {
				in = map[string]any{}
			}
			switch name {
			case "subagent":
				name = "task"
				if a, ok := in["agent"]; ok {
					in["subagent_type"] = a
				}
			case "edit", "write":
				if p, ok := in["path"]; ok && in["filePath"] == nil {
					in["filePath"] = p
				}
			}
			var out []string
			for _, cc := range c.State.Content {
				if cc.Type == "text" && cc.Text != "" {
					out = append(out, cc.Text)
				}
			}
			output := strings.Join(out, "\n")
			if c.State.Error != nil {
				output = c.State.Error.Message
			}
			if c.Name == "subagent" {
				if g := subagentWrapper.FindStringSubmatch(output); g != nil {
					output = g[1]
				}
			}
			rows = append(rows, v1Part(map[string]any{"type": "tool", "tool": name,
				"state": map[string]any{"status": c.State.Status, "input": in, "output": output}}))
		}
	}
	return rows
}

func v1Data(v map[string]any) []byte { b, _ := json.Marshal(v); return b }
func v1Part(v map[string]any) []byte { return v1Data(v) }

func msTS(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

// --- execution events -----------------------------------------------------------

var v2Watches = struct {
	mu sync.Mutex
	m  map[string][]chan struct{}
}{m: map[string][]chan struct{}{}}

// watchExec wakes a waiting turn on any session.execution.* event of its session. An event is
// only a hint to look at the daemon now instead of at the next tick: the outcome itself is read
// from the API (v2LatestOutcome), which cannot be confused by an event that belongs to an earlier
// execution. Armed before the prompt is sent, so the start cannot slip between the two. stop must
// be called.
func watchExec(ses string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	v2Watches.mu.Lock()
	v2Watches.m[ses] = append(v2Watches.m[ses], ch)
	v2Watches.mu.Unlock()
	return ch, func() {
		v2Watches.mu.Lock()
		defer v2Watches.mu.Unlock()
		l := v2Watches.m[ses]
		for i, c := range l {
			if c == ch {
				v2Watches.m[ses] = append(l[:i:i], l[i+1:]...)
				break
			}
		}
		if len(v2Watches.m[ses]) == 0 {
			delete(v2Watches.m, ses)
		}
	}
}

// wakeExec never blocks the event reader.
func wakeExec(ses string) {
	v2Watches.mu.Lock()
	defer v2Watches.mu.Unlock()
	for _, ch := range v2Watches.m[ses] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// v2OutcomeBody renders an execution outcome as the body v1's blocking /message answers with,
// which is what runTurn classifies: an error envelope for a failure, an abort for an interrupt
// (runTurn then tells a stop we asked for from one we did not), and nothing for a success.
func v2OutcomeBody(kind string, e *v2Err) string {
	var env struct {
		Info struct {
			Error *messageError `json:"error,omitempty"`
		} `json:"info"`
	}
	switch kind {
	case "failed":
		if e == nil {
			e = &v2Err{Type: "execution.failed", Message: "the turn failed"}
		}
		me := e.messageError()
		env.Info.Error = &me
	case "interrupted":
		env.Info.Error = &messageError{Name: abortedErrorName}
	}
	b, _ := json.Marshal(env)
	return string(b)
}

// v2OutcomeFor finds how the execution that took the input with id after ended: the OLDEST idle
// message newer than it, so a queued input's execution that follows is never taken for this one.
// It pages back from the newest message until it passes the input. found=false with a nil error
// means no execution of this input has ended yet; an error means the record could not be read,
// which says nothing about the outcome.
func v2OutcomeFor(addr, ses, after string) (kind string, e *v2Err, found bool, err error) {
	if after == "" {
		return "", nil, false, nil
	}
	t := apiTarget{Addr: addr, Client: serveClient}
	path := sessionPath(ses) + "/message?order=desc&limit=" + fmt.Sprint(v2PageSize)
	var oldest *v2Msg
	for page := 0; page < 50; page++ {
		var w struct {
			Data   []v2Msg `json:"data"`
			Cursor struct {
				Next string `json:"next"`
			} `json:"cursor"`
		}
		if err := t.getTurn(path, &w); err != nil {
			return "", nil, false, err
		}
		passed := false
		for i := range w.Data { // newest first
			m := &w.Data[i]
			if m.ID <= after {
				passed = true
				continue
			}
			if m.Type == "idle" {
				oldest = m
			}
		}
		if passed || len(w.Data) < v2PageSize || w.Cursor.Next == "" {
			break
		}
		path = sessionPath(ses) + "/message?limit=" + fmt.Sprint(v2PageSize) + "&cursor=" + url.QueryEscape(w.Cursor.Next)
	}
	if oldest == nil {
		return "", nil, false, nil
	}
	return oldest.Outcome, oldest.Error, true, nil
}
