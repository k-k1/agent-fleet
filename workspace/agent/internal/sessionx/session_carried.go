package sessionx

// Carrying over a pending interaction (docs/log/75 §75.6).
//
// Carries over the intent of a session that was folded with a modal still open and delivers it
// as PROSE once the session resumes. The modal itself cannot be restored: even with
// `claude --resume`, an unanswered tool_use is bypassed via the parent pointer and drops out
// of the conversation tree (measured, docs/log/75 §75.10 A).
//
// Three things live here: promotion (pending-* → carried), building the delivery text, and the
// delivery entry point. The text is split out as a testable pure function because, as §75.10 C
// measured, it is not decoration but the feature itself: without the "do not ask again" line,
// claude asks again.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// PromoteCarriedFor promotes the pending interaction of a session that is about to be folded
// (or already has been) into a carried entry.
//
// Where the pending state lives differs by kind, so there are two entry points:
//
//   - claude — the hooks wrote pending-question / pending-plan / pending-perm to disk at ask
//     time, so reading them is enough (they remain after the process dies).
//   - anything else — the pending state sits in the conversation DB, events.jsonl, the pane's
//     footer or the runtime handle's Interaction, so the kind itself is asked
//     (agents.ModalReporter). Of these, the pane and the handle die with the process, so call
//     this before folding.
//
// The tier1 gate is not the kind but whether the halt is resumable (tier1Foldable,
// docs/log/75 P5). Since kinds other than claude get folded too, ADR 0055 decision 2 (fold
// only when nothing is lost) does not hold unless they can be carried over as well.
func PromoteCarriedFor(m session.Meta) bool {
	promoted := false
	if NormalizeKind(m.Kind) == session.KindClaude {
		// Still alive = the halt is about to kill it. Already dead = the listing just found
		// a workspace stop, a crash, or the user's own /exit.
		reason := "stopped"
		if SessionAlive(m) {
			reason = "halt"
		}
		promoted = status.PromoteCarried(session.UUID(m.Dir, m.Name), reason)
	} else {
		promoted = promoteCarriedOther(m)
	}
	if promoted {
		notifyCarried(m)
	}
	return promoted
}

// notifyCarried pushes "folded while still holding an interaction that awaited an answer" to
// the notification center.
//
// Folding itself is harmless (the interaction is carried, so nothing is lost), but the user
// does not know it happened. The badge in the list is visible only to someone with the Console
// open, and the notification raised when the question appeared says no more than "please
// answer". Without one message from the folding side, a question the user never realised was
// theirs to answer stays pending silently.
func notifyCarried(m session.Meta) {
	c, ok := status.ReadCarried(session.UUID(m.Dir, m.Name))
	if !ok {
		return
	}
	ev := notice.New("carried-interaction", m.Name, m.Kind, session.Display(m))
	ev.Payload["interaction"] = c.Kind // question | plan | permission
	ev.Payload["reason"] = c.Reason    // halt | stopped
	_ = notice.Put(ev)
}

// oneLine folds a string that will be typed into the TUI onto a single line.
//
// Not cosmetic: {t} goes through tmux send-keys -l, which puts the bytes on the pane as they
// are, so a newline arrives as an LF and acts as Enter (docs/build/92). A multi-line delivery
// text would be submitted partway through, and the rest would land in the next prompt or in
// another modal.
func oneLine(s string) string {
	r := strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ", "\t", " ")
	return strings.Join(strings.Fields(r.Replace(s)), " ")
}

// CarriedAnswer is one question's answer: the picked option labels (multi-select can
// have several) plus optional free text.
type CarriedAnswer struct {
	Labels []string `json:"labels,omitempty"`
	Notes  string   `json:"notes,omitempty"`
}

// carriedPreamble is the head of the delivery text. The wording was measured to work
// (§75.10 C): without it, claude reads the original "ask the user" instruction as unfulfilled
// and asks again.
const carriedPreamble = "（停止前に未回答だった質問への回答です。質問し直さず、この回答を使って作業を続けてください）"

// buildCarriedQuestionPrompt renders the carried answer as the single line typed into
// the resumed TUI. The question text always travels with it because the question is GONE from
// the conversation: sent on its own, the answer tells claude nothing about what it answers.
func buildCarriedQuestionPrompt(qs []transcript.Question, answers []CarriedAnswer) string {
	parts := make([]string, 0, len(answers))
	for i, a := range answers {
		q := ""
		if i < len(qs) {
			q = oneLine(qs[i].Question)
			if q == "" {
				q = oneLine(qs[i].Header)
			}
		}
		labels := make([]string, 0, len(a.Labels))
		for _, l := range a.Labels {
			if l = oneLine(l); l != "" {
				labels = append(labels, l)
			}
		}
		// A question with neither a selection nor free text is dropped whole. Sending
		// "nothing was selected" is meaningless to claude, which has no memory of that
		// question — it would only add one sentence that carries nothing.
		seg := ""
		if len(labels) > 0 {
			seg = strings.Join(labels, "・")
			if q != "" {
				seg = "「" + q + "」= " + seg
			}
		} else if oneLine(a.Notes) != "" && q != "" {
			seg = "「" + q + "」= "
		}
		if notes := oneLine(a.Notes); notes != "" {
			if seg == "" {
				seg = notes
			} else {
				seg += "（補足: " + notes + "）"
			}
		}
		if seg != "" {
			parts = append(parts, seg)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return carriedPreamble + strings.Join(parts, " / ")
}

// buildCarriedPlanPrompt renders the carried plan decision.
//
// An approval here is not "approval" but an INSTRUCTION TO EXECUTE (measured, §75.10 E). Given
// an approval in prose, claude proceeds without re-emitting ExitPlanMode, so the caller (the
// Console) must treat this as a decision it cannot take back.
func buildCarriedPlanPrompt(approve bool, feedback string) string {
	Fb := oneLine(feedback)
	if approve {
		s := "（停止前に承認待ちだった計画を承認します）さきほど提示した計画のとおり進めてください。"
		if Fb != "" {
			s += " 補足: " + Fb
		}
		return s
	}
	s := "（停止前に承認待ちだった計画は承認しません）"
	if Fb != "" {
		return s + Fb
	}
	return s + "計画を見直して、あらためて提示してください。"
}

// buildCarriedPermissionPrompt renders the permission case. A permission answer cannot reach
// the dead tool call, so all that travels is the FACTS: keep going, and what was being asked.
func buildCarriedPermissionPrompt(detail string) string {
	d := oneLine(detail)
	s := "（停止前に許可を求めていた操作がありましたが、セッションが停止したため回答は届いていません"
	if d != "" {
		s += "。対象: " + d
	}
	return s + "）必要ならもう一度実行を試みて、作業を続けてください。"
}

// carriedQuestions decodes the stored raw AskUserQuestion payload.
func carriedQuestions(c status.Carried) []transcript.Question {
	if len(c.Questions) == 0 {
		return nil
	}
	var qs []transcript.Question
	if json.Unmarshal(c.Questions, &qs) != nil {
		return nil
	}
	return qs
}

// HandleSessionCarriedAnswer (POST /sessions/{name}/carried-answer) answers a carried
// interaction: it resumes the session if needed and delivers the decision as ordinary
// prose, then drops the carried entry.
//
// Not one key sequence is sent. A carried interaction means no modal is on screen, so
// Down/Enter have no destination and would decide something else if they hit a live pane (the
// AUQ misdelivery class).
func HandleSessionCarriedAnswer(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	var req struct {
		Decision string          `json:"decision"` // answer | approve | reject | continue | discard
		Answers  []CarriedAnswer `json:"answers"`
		Feedback string          `json:"feedback"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_body", "invalid JSON body")
		return
	}
	m, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	sid := session.UUID(m.Dir, name)
	c, ok := status.ReadCarried(sid)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "no_carried", "this session has no carried interaction")
		return
	}
	// Discarding does not wake the session; it only removes the card.
	if req.Decision == "discard" {
		status.RemoveCarried(sid)
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"discarded": true})
		return
	}
	prompt := ""
	switch c.Kind {
	case "question":
		if len(req.Answers) == 0 {
			httpx.WriteErr(w, http.StatusBadRequest, "bad_answers", "answers is required for a carried question")
			return
		}
		prompt = buildCarriedQuestionPrompt(carriedQuestions(c), req.Answers)
	case "plan":
		if req.Decision != "approve" && req.Decision != "reject" {
			httpx.WriteErr(w, http.StatusBadRequest, "bad_decision", "decision must be approve or reject for a carried plan")
			return
		}
		prompt = buildCarriedPlanPrompt(req.Decision == "approve", req.Feedback)
	case "permission":
		prompt = buildCarriedPermissionPrompt(c.Permission)
	}
	if prompt = oneLine(prompt); prompt == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_answers", "nothing to deliver")
		return
	}
	// Do not send while a live session has a new modal up: the carried prose turns into a
	// selection in that modal (the misdelivery class in docs/build/92).
	if SessionAlive(m) {
		if blocked := promptBlocker(name); blocked != "" {
			writeBlockedErr(w, blocked)
			return
		}
	} else if err := ensureSessionTmux(name, false); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "start_failed", err.Error())
		return
	}
	// The carried entry is dropped here: delivery is asynchronous (there is a CLI boot to
	// wait for), so this keeps the card from being pressed twice while the response is
	// pending. It is not restored when delivery fails — the conversation is in the
	// transcript, and the user can say the same thing from the composer.
	status.RemoveCarried(sid)
	// No badge (no recordInjection): this is the user's own decision, the same ordinary user
	// utterance as answering from the Console's question card. Mixing it with operator or
	// peer injections destroys the distinction of whose instruction it was.
	if m.DriverKind() == session.DriverManaged {
		// managed has no pane to type into. Send one turn straight to the thread (the same
		// path as create's initial_prompt). We already went through ensureSessionTmux
		// (Resume for managed), so the handle is live.
		if err := sendManagedPrompt(m, prompt); err != nil {
			writeRuntimeErr(w, err)
			return
		}
		markSessionWorking(name)
		httpx.WriteJSON(w, http.StatusAccepted, map[string]any{"delivering": true, "prompt": prompt, "kind": c.Kind})
		return
	}
	go deliverInitialPrompt(name, prompt)
	httpx.WriteJSON(w, http.StatusAccepted, map[string]any{"delivering": true, "prompt": prompt, "kind": c.Kind})
}

// promoteCarriedOther copies a NON-claude session's live pending modal into the
// carried store, before whatever holds it goes away (docs/log/75 P5).
//
// Only claude keeps its pending state on disk (pending-question / pending-plan /
// pending-perm). For other kinds it sits in the conversation DB, events.jsonl, the pane's
// footer or the runtime handle's Interaction, and only the kind itself knows which. So there
// is a single place to ask, agents.ModalReporter — the folding side holds no per-kind
// knowledge.
//
// Neither the handle nor the pane is woken back up: this runs right before halt/shutdown, and
// resuming something already dead would start up the very thread being folded. Every
// ModalReporter implementation merely peeks at what is there and returns false when there is
// nothing.
//
// What cannot be obtained (an ACP or a pane after the container was SIGKILLed) is simply lost.
// We never pretend otherwise.
func promoteCarriedOther(m session.Meta) bool {
	mr, ok := AgentOf(m.Kind).(agents.ModalReporter)
	if !ok {
		return false // kinds with no notion of a pending interaction (shell / ssm)
	}
	pm, ok := mr.PendingModal(m)
	if !ok {
		return false
	}
	reason := "stopped"
	if SessionAlive(m) {
		reason = "halt"
	}
	c := status.Carried{Kind: pm.Kind, Permission: strings.TrimSpace(pm.Detail), Text: strings.TrimSpace(pm.Text)}
	if pm.Kind == "question" {
		raw, err := json.Marshal(pm.Questions)
		if err != nil {
			return false
		}
		c.Questions = raw
	}
	return status.PutCarried(session.UUID(m.Dir, m.Name), c, reason)
}

// sendManagedPrompt delivers one prose turn to a managed thread.
func sendManagedPrompt(m session.Meta, prompt string) error {
	d, ok := driverOf(m)
	if !ok {
		return fmt.Errorf("managed driver はこの kind ではまだ利用できません: %s", m.Kind)
	}
	h, err := d.Resume(m)
	if err != nil {
		return err
	}
	return h.Send(agents.TurnInput{Prompt: prompt})
}
