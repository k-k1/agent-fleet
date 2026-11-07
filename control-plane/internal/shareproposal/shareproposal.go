// Package shareproposal validates the payload of a shared-session proposal.
package shareproposal

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

// A proposal payload is relayed verbatim to the owner's Agent once the owner approves it, and
// the owner's card can only show what the Console knows how to draw. So the payload is rebuilt
// here from a per-action allow-list of typed fields: what is stored, what the card shows and
// what the Agent receives are the same bytes, and anything the proposer added on top is
// refused rather than carried along.
//
// Attachments are refused for every action. A share has no area of files the proposer put
// there, so any path would name the owner's own files; the Agent refuses them too
// (workspace/agent/internal/sessionx/share_relay.go, keep the field lists in step).

var ErrAttachments = errors.New("attachments are not accepted on a shared-session proposal")

type proposalAnswer struct {
	Text    string `json:"text,omitempty"`
	Options []int  `json:"options,omitempty"`
}

type proposalTurn struct {
	Op              string `json:"op"`
	Prompt          string `json:"prompt"`
	ClientMessageID string `json:"clientMessageID,omitempty"`
}

type proposalRespond struct {
	ID       string           `json:"id"`
	Decision string           `json:"decision"`
	Scope    string           `json:"scope,omitempty"`
	Answers  []proposalAnswer `json:"answers,omitempty"`
}

type proposalAnswerQuestion struct {
	Choices []int `json:"choices"`
}

type proposalPlanRespond struct {
	Decision string `json:"decision"`
	Feedback string `json:"feedback,omitempty"`
}

func strictProposalDecode(raw []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("trailing data")
	}
	return nil
}

// Normalize validates payload for action and returns the canonical bytes to
// store and relay. The error text is safe to show the proposer.
func Normalize(action string, payload []byte) ([]byte, error) {
	var probe map[string]json.RawMessage
	if json.Unmarshal(payload, &probe) != nil {
		return nil, errors.New("payload must be a JSON object")
	}
	if _, has := probe["attachments"]; has {
		return nil, ErrAttachments
	}
	var v any
	switch action {
	case "turn":
		var t proposalTurn
		if err := strictProposalDecode(payload, &t); err != nil {
			return nil, err
		}
		if t.Op != "start" && t.Op != "steer" {
			return nil, errors.New("op must be start or steer")
		}
		if strings.TrimSpace(t.Prompt) == "" {
			return nil, errors.New("prompt is required")
		}
		v = t
	case "respond":
		var t proposalRespond
		if err := strictProposalDecode(payload, &t); err != nil {
			return nil, err
		}
		switch t.Decision {
		case "allow", "deny", "cancel", "answer":
		default:
			return nil, errors.New("decision must be allow, deny, cancel or answer")
		}
		switch t.Scope {
		case "", "once", "turn", "thread":
		default:
			return nil, errors.New("scope must be once, turn or thread")
		}
		if t.ID == "" {
			return nil, errors.New("interaction id is required")
		}
		v = t
	case "answer-question":
		var t proposalAnswerQuestion
		if err := strictProposalDecode(payload, &t); err != nil {
			return nil, err
		}
		if len(t.Choices) == 0 {
			return nil, errors.New("choices is required")
		}
		v = t
	case "plan-respond":
		var t proposalPlanRespond
		if err := strictProposalDecode(payload, &t); err != nil {
			return nil, err
		}
		if t.Decision != "approve" && t.Decision != "reject" {
			return nil, errors.New("decision must be approve or reject")
		}
		v = t
	default:
		return nil, errors.New("unknown action")
	}
	// HTML escaping would grow the body past what the caller bounded (a "<" becomes six
	// bytes), and the Agent refuses a larger one after the proposal is already claimed.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	out := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	if len(out) > MaxBytes {
		return nil, errors.New("proposal exceeds 32 KiB")
	}
	return out, nil
}

// MaxBytes bounds a canonical payload; the Agent's relay guard uses the same limit.
const MaxBytes = 32 << 10

// SessionRefused reports session kinds that never accept a proposal: shell and ssm
// are plain terminals, where an approved "prompt" is a command typed into the owner's shell.
func SessionRefused(kind string) bool { return kind == "shell" || kind == "ssm" }
