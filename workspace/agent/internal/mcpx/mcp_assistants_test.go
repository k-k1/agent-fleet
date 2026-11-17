package mcpx

import (
	"strings"
	"testing"
)

// A blank assistant must be refused here: the Agent reads an empty name as "no assistant" and
// answers with a plain turn, which would hand the model a generic answer in place of the
// specialist it thought it asked.
func TestAskAssistantRefusesABlankAssistant(t *testing.T) {
	for _, tl := range assistantTools() {
		if tl.name != "ask_assistant" {
			continue
		}
		a := tl.handle.newArgs()
		*(a.(*askAssistantArgs)) = askAssistantArgs{Assistant: "  ", Prompt: "hi"}
		out := string(tl.handle.call(mcpReq{}, toolCallParams{}, a))
		if !strings.Contains(out, "assistant") || !strings.Contains(out, "isError") {
			t.Fatalf("blank assistant was not refused: %s", out)
		}
		return
	}
	t.Fatal("ask_assistant tool not found")
}
