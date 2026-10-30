package copilot

// Reading back the model / effort / mode a session switched to in the TUI, so a resume
// does not revert them (#987).

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// RecallSettings reads the session's events.jsonl. `--mode plan` on resume beats the mode
// the session left in (measured on 1.0.88), and `--model` / `--effort` are passed the same
// way.
//
// Measured limits: a `/model` switch is recorded (session.model_change) only once a prompt
// is sent — copilot holds it pending until then and drops it on exit — so a switch with
// no prompt after it is lost by copilot itself. On the Free plan only Auto tiers could be
// switched; the named-model path is inferred from the same event.
func (agentImpl) RecallSettings(m session.Meta) agents.RecalledSettings {
	sid := resolveSid(m)
	if sid == "" {
		return agents.RecalledSettings{}
	}
	f, err := os.Open(EventsPath(sid))
	if err != nil {
		return agents.RecalledSettings{}
	}
	defer f.Close()
	return recallFrom(f)
}

// recallFrom keeps the latest of: session.start / session.resume (the settings a launch
// ended up with — including "auto" when the requested model was refused), session.model_change
// (a switch that took effect), and session.mode_changed.
func recallFrom(rd io.Reader) agents.RecalledSettings {
	var r agents.RecalledSettings
	br := bufio.NewReader(rd)
	for {
		line, err := br.ReadBytes('\n')
		if bytes.Contains(line, []byte(`"session.`)) {
			var ev struct {
				Type string `json:"type"`
				Data struct {
					SelectedModel   string  `json:"selectedModel"`
					NewModel        string  `json:"newModel"`
					ReasoningEffort *string `json:"reasoningEffort"`
					NewMode         string  `json:"newMode"`
				} `json:"data"`
			}
			if json.Unmarshal(line, &ev) == nil {
				d := ev.Data
				switch ev.Type {
				case "session.start", "session.resume":
					setModel(&r, d.SelectedModel, d.ReasoningEffort)
				case "session.model_change":
					setModel(&r, d.NewModel, d.ReasoningEffort)
				case "session.mode_changed":
					if d.NewMode != "" {
						r.Mode = "normal"
						if d.NewMode == "plan" {
							r.Mode = "plan"
						}
					}
				}
			}
		}
		if err != nil {
			return r
		}
	}
}

// setModel records a model together with the effort that came with it; an event with no
// effort means the model runs at its own default (Auto takes none at all), so the flag goes.
func setModel(r *agents.RecalledSettings, model string, effort *string) {
	if model == "" {
		return
	}
	r.Model = model
	if effort != nil && *effort != "" {
		r.Effort, r.ClearEffort = *effort, false
	} else {
		r.Effort, r.ClearEffort = "", true
	}
}

// ModelRefusal returns the message copilot recorded when it would not launch the model asked
// for with --model ("Model \"X\" from --model flag is not available. Using \"auto\" instead."),
// or "" when there is none. That is the only evidence that a fallback to auto was a refusal
// (the account or plan does not enable the model) and not a silently ignored flag.
func (agentImpl) ModelRefusal(m session.Meta) string {
	sid := resolveSid(m)
	if sid == "" {
		return ""
	}
	f, err := os.Open(EventsPath(sid))
	if err != nil {
		return ""
	}
	defer f.Close()
	return refusalFrom(f)
}

func refusalFrom(rd io.Reader) string {
	br := bufio.NewReader(rd)
	for {
		line, err := br.ReadBytes('\n')
		if bytes.Contains(line, []byte(`"session.error"`)) {
			var ev struct {
				Type string `json:"type"`
				Data struct {
					Message   string `json:"message"`
					ErrorType string `json:"errorType"`
				} `json:"data"`
			}
			if json.Unmarshal(line, &ev) == nil && ev.Type == "session.error" && ev.Data.ErrorType == "model" &&
				strings.Contains(ev.Data.Message, "from --model flag is not available") {
				return ev.Data.Message
			}
		}
		if err != nil {
			return ""
		}
	}
}
