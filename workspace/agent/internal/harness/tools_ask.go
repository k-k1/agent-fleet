package harness

// tools_ask.go is the ask_user builtin — docs/log/99 §4.6's "AskUserQuestion 相当".
// This package does not know about Interaction(kind=question) or any Console card
// (those are a later phase's wiring, per ADR 0093 decision 9's segment split); it
// only defines the tool's shape and calls whatever Runtime.AskUser a caller wired
// up. ask_user never mutates anything, so it is not gated by approval — the
// approval gate exists for actions that change state outside the conversation
// itself, not for talking to the user.

import (
	"context"
	"encoding/json"
	"fmt"
)

type askUserArgs struct {
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
}

var askUserToolDef = ToolDef{
	Name:        "ask_user",
	Description: "Ask the user a clarifying question and wait for their answer.",
	Parameters: json.RawMessage(`{
		"type":"object",
		"properties":{
			"question":{"type":"string"},
			"options":{"type":"array","items":{"type":"string"},"description":"Optional suggested answers."}
		},
		"required":["question"]
	}`),
}

func runAskUser(ctx context.Context, rt *Runtime, argsJSON string) (string, error) {
	var a askUserArgs
	if err := decodeArgs(argsJSON, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.Question == "" {
		return "", fmt.Errorf("question is required")
	}
	if rt.AskUser == nil {
		return "no interactive channel is available to ask the user right now", nil
	}
	answer, err := rt.AskUser(ctx, a.Question, a.Options)
	if err != nil {
		return "", err
	}
	return answer, nil
}
