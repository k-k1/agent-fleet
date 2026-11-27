package agy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/fstore"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

const conversationIDPattern = `[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`

var conversationIDRe = regexp.MustCompile("^" + conversationIDPattern + "$")

func commandSteps(conv string) fstore.Store[string] {
	return fstore.Strings(func() string {
		return filepath.Join(paths.AgentStateDir(), "agy-command-steps")
	}, conv, ".txt")
}

func recordCommandStep(in hookInput) {
	if in.ToolCall.Name != "run_command" || in.Error != "" || in.StepIdx == nil || *in.StepIdx < 0 ||
		!conversationIDRe.MatchString(in.ConversationID) {
		return
	}
	var args struct {
		CommandLine string `json:"CommandLine"`
	}
	if json.Unmarshal(in.ToolCall.Args, &args) != nil {
		return
	}
	if cmd := transcript.CapOutput(args.CommandLine); cmd != "" {
		// Independent atomic writes let background completions arrive out of order.
		_ = commandSteps(in.ConversationID).Write(strconv.Itoa(*in.StepIdx), cmd)
	}
}

func readCommandStep(conv string) func(int) string {
	if !conversationIDRe.MatchString(conv) {
		return nil
	}
	store := commandSteps(conv)
	if _, err := os.Stat(store.Dir()); err != nil {
		return nil
	}
	return func(idx int) string {
		cmd, _ := store.Read(strconv.Itoa(idx))
		return cmd
	}
}

func withCommand(p transcript.Part, s stepLine, commandAt func(int) string) transcript.Part {
	// PostToolUse can also fire for an unsuccessful tool attempt. Enrich only rows
	// with a recorded execution outcome or launch. Undecoded wrappers (including
	// command edits) keep their original output until the executed text is verified.
	if commandAt == nil || s.Index == nil || s.Source != "MODEL" || p.Info == "" ||
		(p.Tool != "RUN_COMMAND" && p.Tool != "Background task") {
		return p
	}
	if cmd := commandAt(*s.Index); cmd != "" {
		p.Tool = "RUN_COMMAND"
		p.Info = transcript.Clip(cmd) + " · " + p.Info
		p.Command = cmd
	}
	return p
}
