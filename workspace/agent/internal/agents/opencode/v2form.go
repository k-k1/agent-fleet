package opencode

// The question tool on opencode 2 is a form (docs/log/27, the 2026-10-14 note on #2115).
// Measured on 2.0.28 against a loopback stub provider (fixtures: testdata/v2/form-*):
//   - GET /api/session/{id}/form lists the PENDING forms of a session; /api/form?location[directory]=
//     does the same for a project. A settled form stays readable at GET .../form/{formID} with
//     state pending | answered {answer} | cancelled {message}, until the daemon restarts: forms live in
//     memory only, so a restart (SIGTERM or SIGKILL) drops a pending one for good. A SIGKILL also
//     leaves the question tool part "running" in the messages forever, which is why the pending
//     state is read from the form list and never from the tool part.
//   - The question tool makes ONE form titled "Questions" with metadata {kind:"question", tool:{…}}
//     and one field per question: key q0, q1, … ; title = the question's header; description = the
//     question text; type "string" for a single choice and "multiselect" for `multiple: true`;
//     options[]{value, label, description} (value equals the label); custom:true, i.e. the member
//     may type an answer of their own instead of (single) or besides (multi) the options.
//   - POST …/form/{id}/reply {answer:{q0:"B", q1:["Cheese","anything typed"]}} answers with the
//     option value, or any string for a custom answer; a multiselect takes an array. 204. A wrong
//     shape or an unknown key is 400 FormInvalidAnswerError, a settled form 409
//     FormAlreadySettledError (so a second answerer is harmless), a form the daemon does not know 404.
//   - DELETE …/form/{id}?message= cancels the form. 204. The question tool then FAILS with
//     {type:"tool.execution", message} and the turn carries on: the model reads the failure and goes
//     on. An interrupt cancels the pending form too (the tool fails "aborted") and ends the
//     execution as interrupted.
//   - While a form waits the execution is still active ({type:"running"}), so the session reads as
//     working unless the form list is consulted; the question tool part is "running" in the messages.
//   - Events on /api/event: form.created {form:{…the form…}}, form.replied {id,sessionID,answer},
//     form.cancelled {id,sessionID}. They are NOT session.form.*.
//   - Fields other than string/multiselect exist in the schema (number, integer, boolean, external),
//     but only a plugin can make such a form: the question tool never does. They are mapped as well
//     as a question card can show them, so a form nobody can see never wedges a turn.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

type v2FormOption struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

type v2FormField struct {
	Key         string         `json:"key"`
	Type        string         `json:"type"` // string | number | integer | boolean | multiselect | external
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Required    bool           `json:"required"`
	Hidden      bool           `json:"hidden"`
	Options     []v2FormOption `json:"options"`
	URL         string         `json:"url"` // external
}

// v2Form is Form.Info; Form.Detail adds the state.
type v2Form struct {
	ID        string        `json:"id"`
	SessionID string        `json:"sessionID"`
	Title     string        `json:"title"`
	Fields    []v2FormField `json:"fields"`
	State     struct {
		Status string `json:"status"` // detail only: pending | answered | cancelled
	} `json:"state"`
}

// questions maps each visible field to the card the Console draws for a question.
func (f v2Form) questions() []transcript.Question {
	var out []transcript.Question
	for _, fl := range f.visibleFields() {
		q := transcript.Question{ID: f.ID, Header: fl.Title, Question: fl.Description, MultiSelect: fl.Type == "multiselect"}
		if q.Question == "" {
			q.Question, q.Header = fl.Title, ""
		}
		if fl.Type == "external" && fl.URL != "" {
			q.Question = strings.TrimSpace(q.Question + " " + fl.URL)
		}
		opts := fl.Options
		if fl.Type == "boolean" {
			opts = []v2FormOption{{Value: "true", Label: "Yes"}, {Value: "false", Label: "No"}}
		}
		for _, o := range opts {
			q.Options = append(q.Options, transcript.Option{Label: o.Label, Description: o.Description})
		}
		out = append(out, q)
	}
	return out
}

// visibleFields are the fields a member is asked: hidden ones carry a value the asker fixed.
func (f v2Form) visibleFields() []v2FormField {
	var out []v2FormField
	for _, fl := range f.Fields {
		if !fl.Hidden {
			out = append(out, fl)
		}
	}
	return out
}

func (f v2Form) interaction() *agents.Interaction {
	return &agents.Interaction{ID: f.ID, Kind: agents.InteractionQuestion, Questions: f.questions()}
}

// answer turns the Console's structured reply (per question: option indexes and/or typed text)
// into the form's answer object. A single choice takes the picked option, else the typed text; a
// multiselect takes every picked option and the typed text besides (the form's custom entry
// combines with them, measured); an empty multiselect is an empty array.
func (f v2Form) answer(answers []agents.InteractionAnswer) (map[string]any, error) {
	fields := f.visibleFields()
	if len(answers) != len(fields) {
		return nil, fmt.Errorf("回答数が質問数と一致しません (%d != %d)", len(answers), len(fields))
	}
	out := map[string]any{}
	for i, fl := range fields {
		if fl.Type == "external" {
			continue // filled elsewhere, at its url
		}
		a := answers[i]
		opts := fl.Options
		if fl.Type == "boolean" {
			opts = []v2FormOption{{Value: "true"}, {Value: "false"}}
		}
		var picked []string
		for _, oi := range a.Options {
			if oi < 0 || oi >= len(opts) {
				return nil, fmt.Errorf("質問 %d の選択肢 %d は範囲外です", i+1, oi)
			}
			picked = append(picked, opts[oi].Value)
		}
		text := strings.TrimSpace(a.Text)
		switch fl.Type {
		case "multiselect":
			if text != "" {
				picked = append(picked, text)
			}
			if picked == nil {
				picked = []string{}
			}
			out[fl.Key] = picked
			continue
		}
		var v string
		switch {
		case len(picked) > 0:
			v = picked[0]
		case text != "":
			v = text
		case fl.Required:
			return nil, fmt.Errorf("質問 %d に回答がありません", i+1)
		default:
			continue // optional and left blank
		}
		switch fl.Type {
		case "number":
			n, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return nil, fmt.Errorf("質問 %d の回答は数値ではありません", i+1)
			}
			out[fl.Key] = n
		case "integer":
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("質問 %d の回答は整数ではありません", i+1)
			}
			out[fl.Key] = n
		case "boolean":
			b, err := strconv.ParseBool(v)
			if err != nil {
				return nil, fmt.Errorf("質問 %d の回答は真偽値ではありません", i+1)
			}
			out[fl.Key] = b
		default:
			out[fl.Key] = v
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("回答がありません")
	}
	return out, nil
}

func formsPath(ses string) string { return sessionPath(ses) + "/form" }

// pendingForms lists the forms a session is waiting on.
func (t apiTarget) pendingForms(ses string) ([]v2Form, error) {
	var w struct {
		Data []v2Form `json:"data"`
	}
	if err := t.get(formsPath(ses), &w); err != nil {
		return nil, err
	}
	return w.Data, nil
}

// pendingFormsV2 is what a session waits on, read from the daemon, and only while it lists the
// session as running: a form cannot outlive its turn. A Managed slot's handle is brought in line
// with the answer, so the driver's copy (the address /respond checks) can neither keep a form the
// daemon settled while an event was missed nor lack one it created.
func pendingFormsV2(t apiTarget, m session.Meta, ses string) []v2Form {
	act, err := t.active()
	if err != nil {
		return nil
	}
	var forms []v2Form
	if act[ses] {
		if forms, err = t.pendingForms(ses); err != nil {
			return nil
		}
	}
	if h := handleFor(m.Name); h != nil {
		h.syncForms(forms)
	}
	return forms
}

// questionsV2 is the card of the oldest pending form, one at a time. It carries the form id, the address the Console's /respond takes: a Managed session
// and a Terminal slot on the shared serve are both answered through the form API (RespondTerminal).
func questionsV2(forms []v2Form) []transcript.Question {
	if len(forms) == 0 {
		return nil
	}
	return forms[0].questions()
}

// respondForm answers or dismisses a pending form for the Managed driver. The form is read again
// first: it names the field keys and types the answer has to follow, and says whether somebody else
// (an attached TUI, another client) settled it already.
func respondForm(addr, ses string, reply agents.InteractionReply) error {
	t := apiTarget{Addr: addr, Client: serveClient}
	path := formsPath(ses) + "/" + url.PathEscape(reply.ID)
	switch reply.Decision {
	case agents.DecisionCancel, agents.DecisionDeny:
		req, err := http.NewRequest(http.MethodDelete, addr+path+"?message="+url.QueryEscape("The user dismissed the question."), nil)
		if err != nil {
			return err
		}
		return doForm(req, "質問の却下")
	case agents.DecisionAnswer, agents.DecisionAllow:
		var w struct {
			Data v2Form `json:"data"`
		}
		if err := t.getTurn(path, &w); err != nil {
			if errors.Is(err, statusError(http.StatusNotFound)) {
				return errFormSettled // the daemon forgot it (restart) or it never was
			}
			return fmt.Errorf("質問を読めません: %w", err)
		}
		if w.Data.State.Status != "pending" {
			return fmt.Errorf("%w (%s)", errFormSettled, w.Data.State.Status)
		}
		ans, err := w.Data.answer(reply.Answers)
		if err != nil {
			return err
		}
		body, _ := json.Marshal(map[string]any{"answer": ans})
		req, err := http.NewRequest(http.MethodPost, addr+path+"/reply", bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		return doForm(req, "質問への回答")
	}
	return fmt.Errorf("unsupported decision: %s", reply.Decision)
}

func doForm(req *http.Request, what string) error {
	res, err := serveClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusConflict || res.StatusCode == http.StatusNotFound {
		return errFormSettled // already settled (409) or forgotten (404): somebody else got there first
	}
	if res.StatusCode >= 400 {
		return fmt.Errorf("%sが拒否されました (HTTP %d)", what, res.StatusCode)
	}
	return nil
}

// errFormSettled: the form is not pending any more (answered or cancelled by somebody else, or
// forgotten by the daemon), so there is nothing left to answer.
var errFormSettled = agents.ErrInteractionNotPending

// formByID finds a waiting form. Caller holds h.mu.
func (h *threadHandle) formByID(id string) *agents.Interaction {
	for _, f := range h.forms {
		if f.ID == id {
			return f
		}
	}
	return nil
}

// setForms makes the daemon's pending list the handle's: the oldest is the question shown, which is
// also what the mirror and the live state take from the same list. Caller holds h.mu.
func (h *threadHandle) setForms(list []v2Form) {
	h.forms = h.forms[:0:0]
	for _, f := range list {
		h.forms = append(h.forms, f.interaction())
	}
	h.inter = nil
	if len(h.forms) > 0 {
		h.inter = h.forms[0]
	}
}

// askForm adds a form that was created. A second one waits behind the first, which stays the
// question shown (it is first in the daemon's list too).
func (h *threadHandle) askForm(in *agents.Interaction) {
	h.mu.Lock()
	if h.formByID(in.ID) == nil {
		h.forms = append(h.forms, in)
	}
	h.inter = h.forms[0]
	h.state = agents.TurnWaitingInteraction
	shown := h.inter
	h.mu.Unlock()
	h.emit(agents.Event{Kind: "interaction", TurnState: agents.TurnWaitingInteraction, Interaction: shown})
}

// settleForm drops one form, whoever settled it, and hands the question over to the next one that
// still waits. The turn runs again only when none is left, and an interrupting or finished state is
// left alone.
func (h *threadHandle) settleForm(id string) {
	h.mu.Lock()
	kept := h.forms[:0:0]
	for _, f := range h.forms {
		if f.ID != id {
			kept = append(kept, f)
		}
	}
	changed := len(kept) != len(h.forms)
	h.forms = kept
	h.inter = nil
	if len(kept) > 0 {
		h.inter = kept[0]
	}
	resumed := changed && h.inter == nil && h.state == agents.TurnWaitingInteraction && h.running
	if resumed {
		h.state = agents.TurnRunning
	}
	next := h.inter
	h.mu.Unlock()
	switch {
	case resumed:
		h.emit(agents.Event{Kind: "turn_state", TurnState: agents.TurnRunning})
	case changed && next != nil:
		h.emit(agents.Event{Kind: "interaction", TurnState: agents.TurnWaitingInteraction, Interaction: next})
	}
}

// syncForms reconciles the handle with a pending list the mirror just read from the daemon, so an
// event that was missed (a dropped stream) cannot leave a settled form on the card or a new one off it.
func (h *threadHandle) syncForms(list []v2Form) {
	h.mu.Lock()
	same := len(list) == len(h.forms)
	for i := 0; same && i < len(list); i++ {
		same = list[i].ID == h.forms[i].ID
	}
	if same {
		h.mu.Unlock()
		return
	}
	h.setForms(list)
	next := h.inter
	resumed := next == nil && h.state == agents.TurnWaitingInteraction && h.running
	if resumed {
		h.state = agents.TurnRunning
	} else if next != nil && h.state == agents.TurnRunning {
		h.state = agents.TurnWaitingInteraction
	}
	h.mu.Unlock()
	if resumed {
		h.emit(agents.Event{Kind: "turn_state", TurnState: agents.TurnRunning})
	}
}
