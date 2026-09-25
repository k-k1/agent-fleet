package codex

// Reading back the model / effort a thread switched to in the TUI, so a resume does not
// revert it (#987).

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// RecallSettings reads the slot's rollout for the thread's last settings. The launch
// passes `-m` and the effort on resume, and those beat the thread's own state, so without
// this a `/model` in the TUI is undone by every stop/resume.
//
// Mode is recalled too, for the meta's sake only: codex restores plan by itself and the
// TUI route never passes a mode flag.
func (agentImpl) RecallSettings(m session.Meta) agents.RecalledSettings {
	path := rolloutPath(sids.Read(session.UUID(m.Dir, m.Name)))
	if path == "" {
		return agents.RecalledSettings{}
	}
	f, err := os.Open(path)
	if err != nil {
		return agents.RecalledSettings{}
	}
	defer f.Close()
	return recallFrom(f)
}

// recallFrom keeps the last of two records: turn_context precedes every turn, while
// thread_settings_applied is written the moment `/model`, the effort picker or the mode
// toggle changes anything — with no turn after it — and on every launch (measured on
// 0.157.0). Reading turn_context alone misses a switch nobody replied to.
func recallFrom(rd io.Reader) agents.RecalledSettings {
	var r agents.RecalledSettings
	br := bufio.NewReader(rd)
	for {
		line, err := br.ReadBytes('\n')
		if bytes.Contains(line, []byte(`"turn_context"`)) || bytes.Contains(line, []byte(`"thread_settings_applied"`)) {
			if settings, ok := settingsPayload(line); ok {
				if model, effort := turnModel(settings); model != "" {
					// A null effort is the model's own default, so the flag must go too.
					r.Model, r.Effort, r.ClearEffort = model, effort, effort == ""
				}
				if mode := turnMode(settings); mode != "" {
					r.Mode = mode
				}
			}
		}
		if err != nil {
			return r
		}
	}
}

// settingsPayload returns the object that carries model / reasoning_effort /
// collaboration_mode: the payload itself for turn_context, payload.thread_settings for
// thread_settings_applied. Both have the same shape, so turnModel / turnMode read either.
func settingsPayload(line []byte) (json.RawMessage, bool) {
	var ev struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if json.Unmarshal(line, &ev) != nil {
		return nil, false
	}
	if ev.Type == "turn_context" {
		return ev.Payload, true
	}
	if ev.Type != "event_msg" {
		return nil, false
	}
	var p struct {
		Type           string          `json:"type"`
		ThreadSettings json.RawMessage `json:"thread_settings"`
	}
	if json.Unmarshal(ev.Payload, &p) != nil || p.Type != "thread_settings_applied" || len(p.ThreadSettings) == 0 {
		return nil, false
	}
	return p.ThreadSettings, true
}
