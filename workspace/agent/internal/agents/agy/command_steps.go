package agy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/fstore"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

const conversationIDPattern = `[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`

var conversationIDRe = regexp.MustCompile("^" + conversationIDPattern + "$")

func commandSteps(conv string) fstore.Store[string] {
	return fstore.Strings(func() string {
		return filepath.Join(paths.AgentStateDir(), "agy-command-steps")
	}, conv, ".txt")
}

// maxCommandSteps is how many recorded commands one conversation keeps, newest step index
// first. Each file is up to transcript.CapOutput's cap and the hook writes one per run_command,
// so a long-lived conversation would otherwise grow the state dir without bound. Older rows
// of the mirror fall back to the unenriched tool line, which is what they showed before the
// store existed.
const maxCommandSteps = 500

// pruneCommandSteps removes all but the newest keep recorded steps of one conversation.
// Best effort: the hook runs as a separate process per event, so two prunes can race, and
// both only ever remove the oldest entries.
func pruneCommandSteps(conv string, keep int) {
	store := commandSteps(conv)
	ents, err := os.ReadDir(store.Dir())
	if err != nil || len(ents) <= keep {
		return
	}
	var idxs []int
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		if n, err := strconv.Atoi(strings.TrimSuffix(e.Name(), ".txt")); err == nil {
			idxs = append(idxs, n)
		}
	}
	if len(idxs) <= keep {
		return
	}
	sort.Ints(idxs)
	for _, n := range idxs[:len(idxs)-keep] {
		store.Remove(strconv.Itoa(n))
	}
}

// removeCommandSteps deletes every recorded command of one conversation, including a
// staging directory a killed hook left behind.
func removeCommandSteps(conv string) {
	if conversationIDRe.MatchString(conv) {
		_ = os.RemoveAll(commandSteps(conv).Dir())
	}
}

// dropSlotCommandSteps removes the command steps of the conversations slotSid names, unless
// another slot still names the same one: a slot that adopted a sibling's conversation by
// mistake must not take the sibling's steps with it.
func dropSlotCommandSteps(slotSid string) {
	convs := map[string]bool{sids.Read(slotSid): true, hookConversation(slotSid): true}
	for _, m := range session.ListMetas() {
		if other := session.UUID(m.Dir, m.Name); m.Kind == session.KindAgy && other != slotSid {
			delete(convs, sids.Read(other))
			delete(convs, hookConversation(other))
		}
	}
	for conv := range convs {
		removeCommandSteps(conv)
	}
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
		if commandSteps(in.ConversationID).Write(strconv.Itoa(*in.StepIdx), cmd) == nil {
			pruneCommandSteps(in.ConversationID, maxCommandSteps)
		}
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
