package status

import (
	"encoding/json"
	"os"
	"strings"
	"time"
)

// Carried interaction (docs/log/75 §75.6).
//
// pending-question / pending-plan / pending-perm mean "a modal is on screen RIGHT NOW",
// and the Console answers those with key sequences (Down/Enter). Once the session is
// folded away that modal never comes back — resuming claude routes around an unanswered
// tool_use through the parent pointer, dropping it out of the conversation tree (measured,
// docs/log/75 §75.10 A). So what survives folding is the INTENT, not the modal, and the
// answer can only be injected as prose.
//
// Hence a separate store and separate wire keys rather than reusing the same files: it
// makes the accident of a card from a stopped session firing Down/Enter into a live pane
// (the misdelivered-AskUserQuestion class) impossible by construction. pending-* means
// "answer with keys", carried-interaction means "answer with prose".
type Carried struct {
	// Kind is "question" | "plan" | "permission". A question outranks a plan (the same
	// order as EffectiveModal).
	Kind string `json:"kind"`
	// CapturedAt is when it was folded away (RFC3339). The TTL counts from here.
	CapturedAt string `json:"capturedAt"`
	// Reason is how it came to be folded: "halt" (tier 1 / the user stopping it) |
	// "stopped" (the list found the pane gone = workspace stop, crash, or the user's
	// /exit).
	Reason string `json:"reason,omitempty"`
	// Questions is AskUserQuestion's tool_input.questions, raw.
	Questions json.RawMessage `json:"questions,omitempty"`
	// Plan is the ExitPlanMode plan body. A pending plan never reaches the transcript
	// (measured, docs/log/75 §75.10 D), so this is its only record.
	Plan string `json:"plan,omitempty"`
	// Permission describes the tool that was asking for approval ("Bash · npm ci"). An
	// answer cannot reach a dead tool call, so this is a record of the fact rather than
	// something to respond to.
	Permission string `json:"permission,omitempty"`
	// Text is the prose right before the question (pending-text) — the card's context.
	Text string `json:"text,omitempty"`
}

// CarriedTTL is how long a carried interaction lives. Anything older is dropped at the
// PromoteCarried / ReadCarried entry points.
//
// It needs a lifetime because pending-* has none: a real development machine held
// unanswered payloads five to six weeks old (docs/log/75 D9). sid is deterministic, so a
// future session with the same dir+name would surface those as ghost cards.
const CarriedTTL = 14 * 24 * time.Hour

func carriedFresh(c Carried, now time.Time) bool {
	t, err := time.Parse(time.RFC3339, c.CapturedAt)
	if err != nil {
		return false // unreadable timestamp = we cannot say how old it is; do not carry it
	}
	return now.Sub(t) < CarriedTTL
}

// PromoteCarried promotes "folded away while a modal was up" into a carried interaction.
// Returns true when it promoted one.
//
// Three callers (docs/log/75 §75.6.3): halt, just before status.Remove deletes the
// payloads; the sessions list, the FIRST time it finds the pane gone (which covers
// workspace stop, crash and the user's /exit at once); and SessionStart(boot), just before
// the resume clears them. The third is the safety net: it still catches a path where
// neither of the first two ran, such as a SIGKILL.
//
// Idempotent, but it never OVERWRITES: an existing carried interaction that is still fresh
// wins. Because the resume boot hook promotes before clearing, there is an ordering where
// an empty set of pending payloads would otherwise overwrite what halt already promoted.
func PromoteCarried(sid, reason string) bool {
	if sid == "" {
		return false
	}
	if prev, ok := ReadCarried(sid); ok && prev.Kind != "" {
		return false
	}
	c := Carried{CapturedAt: time.Now().Format(time.RFC3339), Reason: reason}
	if q, ok := ReadPendingQuestion(sid); ok && len(q) > 0 {
		c.Kind = "question"
		c.Questions = append(json.RawMessage(nil), q...)
	} else if p, ok := ReadPendingPlan(sid); ok && strings.TrimSpace(p) != "" {
		c.Kind = "plan"
		c.Plan = p
	} else if pm, ok := ReadPendingPermission(sid); ok && strings.TrimSpace(pm) != "" {
		c.Kind = "permission"
		c.Permission = pm
	} else {
		return false
	}
	if txt, ok := ReadPendingText(sid); ok {
		c.Text = strings.TrimSpace(txt)
	}
	if err := carriedFiles.Write(sid, c); err != nil {
		return false
	}
	return true
}

// ReadCarried returns the carried interaction, dropping (and deleting) one that has
// outlived CarriedTTL.
func ReadCarried(sid string) (Carried, bool) {
	c, ok := carriedFiles.Read(sid)
	if !ok || c.Kind == "" {
		return Carried{}, false
	}
	if !carriedFresh(c, time.Now()) {
		carriedFiles.Remove(sid)
		return Carried{}, false
	}
	return c, true
}

func RemoveCarried(sid string) { carriedFiles.Remove(sid) }

// SweepCarried drops every carried entry past its TTL. Called at agent start —
// the store is small (one small file per session that was ever halted mid-modal),
// but nothing else would ever delete an entry whose session is gone.
func SweepCarried() int {
	dir := carriedFiles.Dir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	now := time.Now()
	dropped := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		sid := strings.TrimSuffix(name, ".json")
		if c, ok := carriedFiles.Read(sid); !ok || !carriedFresh(c, now) {
			carriedFiles.Remove(sid)
			dropped++
		}
	}
	return dropped
}

// PutCarried writes a carried interaction WITHOUT going through the pending payload files.
//
// It exists for the non-claude kinds (docs/log/75 P5): their pending interaction is not in
// pending-question / pending-perm but in the conversation DB, events.jsonl, the pane
// footer, or the runtime handle's Interaction. Which one it came from is the kind's
// business (agents.ModalReporter); this only takes the result.
//
// Like PromoteCarried it never OVERWRITES (a carried interaction that is still fresh
// wins), because promotion has several triggers and the list path can call again after
// halt already promoted.
func PutCarried(sid string, c Carried, reason string) bool {
	if sid == "" {
		return false
	}
	switch c.Kind {
	case "question":
		if len(c.Questions) == 0 {
			return false // a question with no answer form cannot be acted on once carried
		}
	case "plan", "permission":
	default:
		return false
	}
	if prev, ok := ReadCarried(sid); ok && prev.Kind != "" {
		return false
	}
	c.CapturedAt = time.Now().Format(time.RFC3339)
	c.Reason = reason
	return carriedFiles.Write(sid, c) == nil
}
