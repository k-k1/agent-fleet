package agy

// Reading back the model and mode a conversation switched to in the TUI, so a resume does
// not revert them (#987).

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// RecallSettings reads the conversation's transcript. On resume agy takes `--model` and
// `--mode plan` from the command line and otherwise the GLOBAL settings.json model — the
// conversation's own last model is never restored (measured on 1.2.11) — so the flag has
// to carry the conversation's choice.
//
// What is recorded, only once a prompt is sent: agy prefixes the next USER_INPUT with a
// <USER_SETTINGS_CHANGE> note naming the new model (display name, effort included), and
// every prompt sent in plan mode carries a "/plan " prefix inside <USER_REQUEST>. A switch
// with no prompt after it leaves nothing in the conversation.
func (agentImpl) RecallSettings(m session.Meta) agents.RecalledSettings {
	conv := sids.Read(session.UUID(m.Dir, m.Name))
	if conv == "" {
		return agents.RecalledSettings{}
	}
	f, err := os.Open(transcriptPath(conv))
	if err != nil {
		return agents.RecalledSettings{}
	}
	defer f.Close()
	return recallFrom(f, cachedModelIDsByLabel())
}

var modelChangeRe = regexp.MustCompile("changed setting `Model Selection` from .*? to (.+?)\\.(?:\\s|$)")

// modelSwitchRe is modelChangeRe with the "from" side captured too.
var modelSwitchRe = regexp.MustCompile("changed setting `Model Selection` from (.+?) to (.+?)\\.(?:\\s|$)")

func recallFrom(rd io.Reader, byLabel map[string]string) agents.RecalledSettings {
	var r agents.RecalledSettings
	sc := bufio.NewScanner(rd)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		var s stepLine
		if json.Unmarshal(sc.Bytes(), &s) != nil || s.Type != "USER_INPUT" {
			continue
		}
		if mm := modelChangeRe.FindStringSubmatch(s.Content); mm != nil {
			r.Model = modelID(strings.TrimSpace(mm[1]), byLabel)
		}
		if mm := userRequestRe.FindStringSubmatch(s.Content); mm != nil {
			r.Mode = "normal"
			if strings.HasPrefix(mm[1], "/plan ") {
				r.Mode = "plan"
			}
		}
	}
	return r
}

// modelID turns the display name the note carries into the id `agy models` lists for it.
// When the catalog has not been fetched (or no longer lists it) the display name is used
// as is: agy's own settings.json stores exactly that form as its model.
func modelID(label string, byLabel map[string]string) string {
	if id, ok := byLabel[label]; ok && id != "" {
		return id
	}
	return label
}

// cachedModelIDsByLabel is the catalog as last fetched, without fetching: a resume must not
// wait on (or spawn) `agy models`.
func cachedModelIDsByLabel() map[string]string {
	modelsMu.Lock()
	defer modelsMu.Unlock()
	out := make(map[string]string, len(modelsList))
	for _, c := range modelsList {
		out[c.Label] = c.ID
	}
	return out
}
