// Package memorelay is the Agent's relay for the memo tools a session reaches: list_memos,
// add_memo and update_memo (ADR 0137 §3). The MCP process calls these routes instead of the
// Control Plane, and the relay calls /internal/memos* and /internal/memo-categories with the
// workspace's AF_MEMO_TOKEN.
//
// What the relay adds is attribution: on a create it stamps producer (the calling session's
// name) and producerOrigin (that session's origin, session.OriginOf) from the session's own
// meta, and never from the memo fields the model wrote, which have no field for either. It is
// attribution, not authentication. The caller names itself, as peer_from does, and any process
// in the workspace holds AGENT_TOKEN and AF_MEMO_TOKEN; the Control Plane's wall is the
// member's release, not this name.
package memorelay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/cpurl"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// UnavailableMessage is the answer when the workspace has no memo bridge: the Control Plane
// injects AF_CP_BASE_URL and AF_MEMO_TOKEN only when it has a public base URL.
const UnavailableMessage = "メモ機能はこの環境では利用できません（CP の公開URL/トークンが未設定）"

// maxRequest bounds a request body: a memo is text, and the Control Plane caps it far lower.
const maxRequest = 1 << 20

// Fields are the memo fields a create forwards: those the tools advertise. Anything else in
// the model's arguments (producer, producerOrigin, provenance, state) has no field here, so it
// is dropped by the decode, not by a rule that could be forgotten.
type Fields struct {
	Kind     string `json:"kind"`
	Body     string `json:"body"`
	RefPath  string `json:"refPath"`
	Repo     string `json:"repo"`
	Category string `json:"category"`
	// Reference points at a work item, never at a copy of it. Nil is none; the Control Plane
	// validates a given one.
	Reference *Reference `json:"reference,omitempty"`
	// After is the messages of the same queue this one waits for, memo ids or reference keys.
	// The Control Plane resolves and checks them.
	After []string `json:"after,omitempty"`
	// StartAfter is the earliest time the message may be started, RFC 3339 with an offset. The
	// Control Plane checks it.
	StartAfter string `json:"startAfter,omitempty"`
	// AgentKind, Model, Effort and Route are the run recipe the message asks for in place of its
	// category's setting (ADR 0137, addendum 2026-11-23). The Control Plane validates them.
	AgentKind string `json:"agentKind,omitempty"`
	Model     string `json:"model,omitempty"`
	Effort    string `json:"effort,omitempty"`
	Route     string `json:"route,omitempty"`
	// Continuation is the model's claim that this follows the message its queue run was given
	// (ADR 0137, addendum 2026-11-20). It names no id: the relay adds the delivery from the
	// session's own meta, and the Control Plane decides from its own rows.
	Continuation bool `json:"continuation,omitempty"`
	// RunNext marks the message this create adds to run before the waiting ones that are not
	// marked (#5021). Patch has no field for it: a session never changes the mark on a message
	// already in the queue.
	RunNext bool `json:"runNext,omitempty"`
}

// Reference is the typed pointer {provider, key, url} a queue message may carry. The fields are
// omitted when empty so that {} survives as {}: on an update that is "clear", and it must not
// turn into three empty strings that a reader could take for a half-filled object.
type Reference struct {
	Provider string `json:"provider,omitempty"`
	Key      string `json:"key,omitempty"`
	URL      string `json:"url,omitempty"`
}

// CreateRequest is POST /memos. From is the calling session's name, filled by the MCP process
// from its own identity; "" is the operator assistant, which has no session.
type CreateRequest struct {
	From string `json:"from"`
	Memo Fields `json:"memo"`
}

// cpCreate is the body POST /internal/memos gets: the memo plus the attribution the relay set.
type cpCreate struct {
	Fields
	Producer       string `json:"producer"`
	ProducerOrigin string `json:"producerOrigin"`
	// QueueDelivery is the queue delivery that started the calling session, set only when the
	// create claims to be a continuation. Fields has no field for it, so what the model writes
	// under that name is dropped by the decode.
	QueueDelivery string `json:"queueDelivery,omitempty"`
}

// Patch is PATCH /memos/{id}: the fields update_memo may change. Nil leaves a field as it is,
// the Control Plane's memoPatch contract.
type Patch struct {
	Body     *string `json:"body,omitempty"`
	RefPath  *string `json:"refPath,omitempty"`
	Repo     *string `json:"repo,omitempty"`
	Category *string `json:"category,omitempty"`
	Position *int    `json:"position,omitempty"`
	// Reference has the Control Plane's three meanings: nil (absent, and a JSON null) leaves the
	// reference as it is, an empty object clears it, a full one sets it.
	Reference *Reference `json:"reference,omitempty"`
	// After is the prerequisites: nil leaves them, an empty list clears them (it survives the
	// encoding as []), a list replaces them.
	After *[]string `json:"after,omitempty"`
	// StartAfter is the start time: nil leaves it, "" clears it, a time replaces it.
	StartAfter *string `json:"startAfter,omitempty"`
	// AgentKind, Model, Effort and Route are the run recipe: nil leaves a field, "" clears it.
	AgentKind *string `json:"agentKind,omitempty"`
	Model     *string `json:"model,omitempty"`
	Effort    *string `json:"effort,omitempty"`
	Route     *string `json:"route,omitempty"`
}

// ListResponse is GET /memos: the memos (each with its state) and the categories (each with
// queue), both as the Control Plane wrote them. Categories is absent, with the reason in
// CategoriesError, when only that read failed: the memo list is still worth answering.
type ListResponse struct {
	Memos           json.RawMessage `json:"memos"`
	Categories      json.RawMessage `json:"categories,omitempty"`
	CategoriesError string          `json:"categoriesError,omitempty"`
}

// ProducerOperator is the producer stamped for the operator assistant, which is no session.
const ProducerOperator = "operator"

// CPTimeout bounds one Control Plane call. GET /memos makes two in a row, so a caller of the
// relay has to wait longer than twice this (mcpx's memoAgentTimeout) or it gives up before the
// relay answers with what it has.
var CPTimeout = CPTimeoutDefault

// CPTimeoutDefault is CPTimeout's value outside tests.
const CPTimeoutDefault = 20 * time.Second

// cpDoFn is the Control Plane call behind a seam for tests.
var cpDoFn = cpDo

// cpResult is one Control Plane answer.
type cpResult struct {
	status int
	body   []byte
}

// errUnavailable is cpDo's answer when the bridge is not configured.
var errUnavailable = fmt.Errorf("%s", UnavailableMessage)

func cpDo(ctx context.Context, method, path string, body []byte) (cpResult, error) {
	base := cpurl.Request()
	tok := os.Getenv("AF_MEMO_TOKEN")
	if base == "" || tok == "" {
		return cpResult{}, errUnavailable
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	ctx, cancel := context.WithTimeout(ctx, CPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, base+path, rdr)
	if err != nil {
		return cpResult{}, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return cpResult{}, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return cpResult{status: resp.StatusCode, body: b}, nil
}

// relay writes the Control Plane's answer back as it came, so a refusal keeps its status and
// code (409 memo_locked, 413 memo_too_large, ...).
func relay(w http.ResponseWriter, res cpResult, err error) {
	if err == errUnavailable {
		httpx.WriteErr(w, http.StatusServiceUnavailable, "memo_unavailable", UnavailableMessage)
		return
	}
	if err != nil {
		httpx.WriteErr(w, http.StatusBadGateway, "memo_cp_unreachable", "CP メモAPI に届きません: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(res.status)
	_, _ = w.Write(res.body)
}

// HandleList is GET /memos.
func HandleList(w http.ResponseWriter, r *http.Request) {
	memos, err := cpDoFn(r.Context(), http.MethodGet, "/internal/memos", nil)
	if err != nil || memos.status >= 300 || !json.Valid(memos.body) {
		relay(w, memos, err)
		return
	}
	out := ListResponse{Memos: memos.body}
	cats, err := cpDoFn(r.Context(), http.MethodGet, "/internal/memo-categories", nil)
	switch {
	case err != nil:
		out.CategoriesError = err.Error()
	case cats.status >= 300 || !json.Valid(cats.body):
		out.CategoriesError = fmt.Sprintf("CP memo-categories answered %d", cats.status)
	default:
		out.Categories = cats.body
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// HandleCreate is POST /memos.
func HandleCreate(w http.ResponseWriter, r *http.Request) {
	var in CreateRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxRequest)
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	caller, msg := callerMeta(in.From)
	if msg != "" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_from", msg)
		return
	}
	producer, origin := attribution(in.From, caller)
	in.Memo.Repo = bucketFor(caller, in.Memo.Repo)
	out := cpCreate{Fields: in.Memo, Producer: producer, ProducerOrigin: origin}
	if in.Memo.Continuation {
		delivery := QueueDeliveryOf(in.From)
		if delivery == "" {
			httpx.WriteErr(w, http.StatusBadRequest, "continuation_not_a_queue_run",
				"continuation is only for a session a queue started: this session was not given a queued message")
			return
		}
		out.QueueDelivery, out.ProducerOrigin = delivery, session.OriginQueue
	}
	body, err := json.Marshal(out)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "encode_failed", err.Error())
		return
	}
	res, err := cpDoFn(r.Context(), http.MethodPost, "/internal/memos", body)
	relay(w, res, err)
}

// callerMeta reads the meta of the calling session `from`: nil for "" (the operator), a refusal
// for a name that is not a session here. A create's attribution must name a session that exists
// here, so a name with no meta is refused rather than stamped.
func callerMeta(from string) (*session.Meta, string) {
	from = strings.TrimSpace(from)
	if from == "" {
		return nil, ""
	}
	if !session.ValidName(from) {
		return nil, "from is not a session name"
	}
	m, ok := session.ReadMeta(from)
	if !ok {
		return nil, "no session named " + from
	}
	return &m, ""
}

// attribution is the producer and origin of a caller whose meta callerMeta read.
func attribution(from string, m *session.Meta) (producer, origin string) {
	if m == nil {
		return ProducerOperator, session.OriginOperator
	}
	name := strings.TrimSpace(from)
	if m.QueueMove != nil || m.QueueSuccessor != "" || m.MovedTo != "" || m.QueueDelivery != "" {
		return name, session.OriginOf(session.ReadQueueGraph().Project(*m))
	}
	return name, session.OriginOf(*m)
}

// HandleUpdate is PATCH /memos/{id}. The query's from is the calling session, filled by the MCP
// process from its own identity like a create's From, and absent for the operator and for a move
// whose bucket does not depend on the caller: it decides only which bucket a repo the model names
// is filed under (bucketFor).
func HandleUpdate(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_id", "id is required")
		return
	}
	var p Patch
	r.Body = http.MaxBytesReader(w, r.Body, maxRequest)
	if !httpx.DecodeJSON(w, r, &p) {
		return
	}
	if p.Repo != nil {
		// Only a move to a name that could be the caller's space reads the session (NeedsCaller):
		// any other edit goes through whatever from says, so a meta that cannot be read stops
		// nothing but such a move, which it could otherwise file under a same-named repository.
		var caller *session.Meta
		if NeedsCaller(*p.Repo) {
			var msg string
			if caller, msg = callerMeta(r.URL.Query().Get("from")); msg != "" {
				httpx.WriteErr(w, http.StatusBadRequest, "bad_from", msg)
				return
			}
		}
		base := bucketFor(caller, *p.Repo)
		p.Repo = &base
	}
	body, err := json.Marshal(p)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "encode_failed", err.Error())
		return
	}
	res, err := cpDoFn(r.Context(), http.MethodPatch, "/internal/memos/"+url.PathEscape(id), body)
	relay(w, res, err)
}

// DeferRequest is POST /memos/defer: defer_queue_item of the session From (filled by the MCP
// process from its own identity, never by the model). The relay sends it for the queue delivery
// the session's meta names, so a session hands back only the run that started it.
type DeferRequest struct {
	From      string `json:"from"`
	Reason    string `json:"reason"`
	NotBefore string `json:"notBefore,omitempty"`
	Cancel    bool   `json:"cancel,omitempty"`
}

// cpDefer is the body POST /internal/memo-deliveries/{id}/defer gets. At is this host's clock: the
// Control Plane compares it with the turn starts of the run's tree, which the same clock stamps.
type cpDefer struct {
	Session   string `json:"session"`
	Reason    string `json:"reason"`
	NotBefore string `json:"notBefore,omitempty"`
	Cancel    bool   `json:"cancel,omitempty"`
	At        string `json:"at"`
}

// QueueDeliveryOf is the delivery the named session currently owns as the run's root.
func QueueDeliveryOf(from string) string {
	from = strings.TrimSpace(from)
	if !session.ValidName(from) {
		return ""
	}
	if m, ok := session.ReadMeta(from); !ok || m.Archived {
		return ""
	}
	g := session.ReadQueueGraph()
	role, queue := g.Lineage(from)
	if !queue || role.Origin != session.OriginQueue || g.Current(from) != from {
		return ""
	}
	return role.Delivery
}

// HandleDefer is POST /memos/defer (ADR 0137, addendum 2026-11-19, part B).
func HandleDefer(w http.ResponseWriter, r *http.Request) {
	var in DeferRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxRequest)
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	delivery := QueueDeliveryOf(in.From)
	if delivery == "" {
		httpx.WriteErr(w, http.StatusConflict, "no_queue_delivery", "this session was not started by a queue run, so it has no message to hand back")
		return
	}
	body, err := json.Marshal(cpDefer{Session: strings.TrimSpace(in.From), Reason: in.Reason, NotBefore: in.NotBefore,
		Cancel: in.Cancel, At: time.Now().Format(time.RFC3339Nano)})
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "encode_failed", err.Error())
		return
	}
	res, err := cpDoFn(r.Context(), http.MethodPost, "/internal/memo-deliveries/"+url.PathEscape(delivery)+"/defer", body)
	relay(w, res, err)
}
