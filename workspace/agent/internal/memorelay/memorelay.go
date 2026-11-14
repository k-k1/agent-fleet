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
}

// Patch is PATCH /memos/{id}: the fields update_memo may change. Nil leaves a field as it is,
// the Control Plane's memoPatch contract.
type Patch struct {
	Body     *string `json:"body,omitempty"`
	RefPath  *string `json:"refPath,omitempty"`
	Repo     *string `json:"repo,omitempty"`
	Category *string `json:"category,omitempty"`
	Position *int    `json:"position,omitempty"`
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

// cpDoFn is the Control Plane call behind a seam for tests.
var cpDoFn = cpDo

// cpResult is one Control Plane answer.
type cpResult struct {
	status int
	body   []byte
}

// errUnavailable is cpDo's answer when the bridge is not configured.
var errUnavailable = fmt.Errorf("%s", UnavailableMessage)

func cpDo(method, path string, body []byte) (cpResult, error) {
	base := cpurl.Request()
	tok := os.Getenv("AF_MEMO_TOKEN")
	if base == "" || tok == "" {
		return cpResult{}, errUnavailable
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, base+path, rdr)
	if err != nil {
		return cpResult{}, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
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
	memos, err := cpDoFn(http.MethodGet, "/internal/memos", nil)
	if err != nil || memos.status >= 300 || !json.Valid(memos.body) {
		relay(w, memos, err)
		return
	}
	out := ListResponse{Memos: memos.body}
	cats, err := cpDoFn(http.MethodGet, "/internal/memo-categories", nil)
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
	producer, origin, msg := Producer(in.From)
	if msg != "" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_from", msg)
		return
	}
	body, err := json.Marshal(cpCreate{Fields: in.Memo, Producer: producer, ProducerOrigin: origin})
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "encode_failed", err.Error())
		return
	}
	res, err := cpDoFn(http.MethodPost, "/internal/memos", body)
	relay(w, res, err)
}

// Producer is the attribution a create from `from` carries: the session's name and its origin
// as its meta records it, or the operator for "". A name with no meta is refused rather than
// stamped: the attribution must name a session that exists here.
func Producer(from string) (producer, origin, refusal string) {
	from = strings.TrimSpace(from)
	if from == "" {
		return ProducerOperator, session.OriginOperator, ""
	}
	if !session.ValidName(from) {
		return "", "", "from is not a session name"
	}
	m, ok := session.ReadMeta(from)
	if !ok {
		return "", "", "no session named " + from
	}
	return from, session.OriginOf(m), ""
}

// HandleUpdate is PATCH /memos/{id}.
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
	body, err := json.Marshal(p)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "encode_failed", err.Error())
		return
	}
	res, err := cpDoFn(http.MethodPatch, "/internal/memos/"+url.PathEscape(id), body)
	relay(w, res, err)
}
