package claude

// Reading back the model / effort / permission mode a conversation switched to in the
// terminal, so a resume does not revert it (#987).

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// RecallSettings reads the slot's jsonl for the last `/model`, `/effort` and permission-mode
// records. It deliberately ignores message.model on assistant lines: that is also where an
// automatic fallback shows up, and a switch with no reply after it is not there at all
// (measured on 2.1.282: `--resume` without --model reverts such a switch by itself).
func (agentImpl) RecallSettings(m session.Meta) agents.RecalledSettings {
	sid := session.UUID(m.Dir, m.Name)
	var r agents.RecalledSettings
	for _, p := range jsonlPaths(sid) {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		r = recallFrom(f)
		f.Close()
		break
	}
	return r
}

var (
	// The confirmation claude writes after a successful `/model`. The id is not recorded,
	// only the display name ("Sonnet 5", "Opus 5.5 (1M context) (default)").
	setModelRe = regexp.MustCompile("^Set model to `([^`]+)`")
	// The picker form appends the effort it confirmed along with the model.
	modelEffortRe = regexp.MustCompile("with `([a-z]+)` effort")
	// `/effort` (typed or picker). "Kept effort level as …" is a cancelled switch and does
	// not match.
	setEffortRe = regexp.MustCompile(`^Set effort level to ([a-z]+)`)
	stdoutRe    = regexp.MustCompile(`<local-command-stdout>([\s\S]*?)</local-command-stdout>`)
)

// recallFrom scans a claude jsonl in order and keeps the last value of each setting. A
// command only counts once its stdout confirms it: an invalid argument or a cancelled
// dialog leaves the command line behind without a switch.
func recallFrom(rd io.Reader) agents.RecalledSettings {
	var r agents.RecalledSettings
	var cmd, args string // the last command line, waiting for its stdout
	br := bufio.NewReader(rd)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			switch {
			case bytes.Contains(line, []byte(`"permission-mode"`)):
				var ev struct {
					Type           string `json:"type"`
					PermissionMode string `json:"permissionMode"`
				}
				if json.Unmarshal(line, &ev) == nil && ev.Type == "permission-mode" && ev.PermissionMode != "" {
					r.Mode = "normal"
					if ev.PermissionMode == "plan" {
						r.Mode = "plan"
					}
				}
			case bytes.Contains(line, []byte("command-name")), bytes.Contains(line, []byte("local-command-stdout")):
				var ev struct {
					Type        string `json:"type"`
					IsSidechain bool   `json:"isSidechain"`
					Message     struct {
						Content json.RawMessage `json:"content"`
					} `json:"message"`
				}
				if json.Unmarshal(line, &ev) != nil || ev.Type != "user" || ev.IsSidechain {
					break
				}
				text := contentText(ev.Message.Content)
				if strings.HasPrefix(text, "<command-name>") {
					cmd, args = "", ""
					if mm := commandNameRe.FindStringSubmatch(text); mm != nil {
						cmd = strings.TrimSpace(mm[1])
					}
					if mm := commandArgsRe.FindStringSubmatch(text); mm != nil {
						args = strings.TrimSpace(mm[1])
					}
					break
				}
				if mm := stdoutRe.FindStringSubmatch(text); mm != nil {
					applyCommand(&r, cmd, args, strings.TrimSpace(mm[1]))
					cmd, args = "", ""
				}
			}
		}
		if err != nil {
			return r
		}
	}
}

var (
	commandNameRe = regexp.MustCompile(`<command-name>([\s\S]*?)</command-name>`)
	commandArgsRe = regexp.MustCompile(`<command-args>([\s\S]*?)</command-args>`)
)

func applyCommand(r *agents.RecalledSettings, cmd, args, out string) {
	switch cmd {
	case "/model":
		mm := setModelRe.FindStringSubmatch(out)
		if mm == nil {
			return
		}
		model, clear, ok := modelFromCommand(args, mm[1])
		if !ok {
			return
		}
		r.Model, r.ClearModel = model, clear
		if em := modelEffortRe.FindStringSubmatch(out); em != nil {
			setEffort(r, em[1])
		}
	case "/effort":
		if em := setEffortRe.FindStringSubmatch(out); em != nil {
			setEffort(r, em[1])
		}
	}
}

func setEffort(r *agents.RecalledSettings, level string) {
	if level == "auto" || level == "default" {
		r.Effort, r.ClearEffort = "", true
		return
	}
	r.Effort, r.ClearEffort = level, false
}

// modelFromCommand turns a confirmed `/model` into the value --model takes. A typed
// argument is exactly what claude stored as its default (measured: `claude-sonnet-5` is
// kept verbatim), so it is used as is. The picker records only the display name, which
// is mapped back to the tier alias; a name that is not one of ours is not guessed at.
func modelFromCommand(args, display string) (model string, clear, ok bool) {
	if args != "" {
		if args == "default" {
			return "", true, true
		}
		return args, false, true
	}
	tier := strings.ToLower(strings.Fields(display + " ")[0])
	if tier == "default" || strings.Contains(display, "(default)") {
		return "", true, true
	}
	for _, c := range Models() {
		if c.ID == tier {
			if strings.Contains(display, "(1M context)") {
				return tier + "[1m]", false, true
			}
			return tier, false, true
		}
	}
	return "", false, false
}
