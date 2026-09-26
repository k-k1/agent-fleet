package agy

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

// userInput builds a transcript_full.jsonl USER_INPUT step as agy 1.2.11 writes it.
func userInput(settingsNote, request string) string {
	content := "<USER_REQUEST>\n" + request + "\n</USER_REQUEST>\n<ADDITIONAL_METADATA>\nThe current local time is: now\n</ADDITIONAL_METADATA>"
	if settingsNote != "" {
		content = "<USER_SETTINGS_CHANGE>\n" + settingsNote + "\n</USER_SETTINGS_CHANGE>\n" + content
	}
	b, _ := json.Marshal(map[string]any{"step_index": 0, "source": "USER_EXPLICIT", "type": "USER_INPUT", "status": "DONE", "content": content})
	return string(b)
}

const planner = `{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","content":"ok"}`

func TestRecallFrom(t *testing.T) {
	byLabel := map[string]string{"Gemini 3.6 Flash (High)": "gemini-3.6-flash-high"}
	cases := []struct {
		name  string
		lines []string
		want  agents.RecalledSettings
	}{
		{"first prompt names the launch model", []string{
			userInput("The user changed setting `Model Selection` from None to Gemini 3.8 Flash (Low). No need to comment on this change.", "Reply with just: ok"), planner,
		}, agents.RecalledSettings{Model: "Gemini 3.8 Flash (Low)", Mode: "normal"}},
		{"a later switch maps to the catalog id", []string{
			userInput("The user changed setting `Model Selection` from None to Gemini 3.8 Flash (Low). No need to comment on this change.", "hi"), planner,
			userInput("The user changed setting `Model Selection` from Gemini 3.8 Flash (Low) to Gemini 3.6 Flash (High). No need to comment on this change.", "again"), planner,
			userInput("", "and again"),
		}, agents.RecalledSettings{Model: "gemini-3.6-flash-high", Mode: "normal"}},
		{"the sentence typed in a request is not a switch", []string{
			userInput("", "why did it say: The user changed setting `Model Selection` from X to Y. ?"), planner,
		}, agents.RecalledSettings{Mode: "normal"}},
		{"plan prompt", []string{userInput("", "/plan Reply with just: ok"), planner},
			agents.RecalledSettings{Mode: "plan"}},
		{"left plan", []string{userInput("", "/plan draft it"), planner, userInput("", "go ahead")},
			agents.RecalledSettings{Mode: "normal"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := recallFrom(strings.NewReader(strings.Join(c.lines, "\n")), byLabel); got != c.want {
				t.Errorf("got %+v, want %+v", got, c.want)
			}
		})
	}
}
