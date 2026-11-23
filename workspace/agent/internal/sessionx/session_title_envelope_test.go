package sessionx

import (
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

func queueOpening(instruction string, items []QueueDeliveryItem) string {
	d := QueueDelivery{Queue: "q1", ID: "d1", Label: "agent-fleet/issue-rounds", Instruction: instruction, Items: items}
	head, sec, _ := queuePrompt(d)
	return head + "\n" + sec
}

func TestTitlePromptQueueRunKeepsItemDropsBoilerplate(t *testing.T) {
	instruction := "Use the queue-implementer skill for the queue item. " + strings.Repeat("Follow it as written. ", 40)
	items := []QueueDeliveryItem{{ID: "i1", Kind: "text", Provenance: queueProvenanceMember,
		Body:      "Implement #4782 part 4: dedupe Console CSS classes",
		Reference: &QueueDeliveryReference{Provider: "github", Key: "k-k1/agent-fleet#4782", URL: "https://example.test/4782"}}}
	turns := []transcript.Turn{etUser(queueOpening(instruction, items))}
	for i := 0; i < 8; i++ {
		turns = append(turns, etAsst("waiting for the background test run"), etUser("ok"))
	}
	got := titleSuggestPrompt(turns, "en")
	if !strings.Contains(got, "dedupe Console CSS classes") {
		t.Fatalf("item body missing:\n%s", got)
	}
	for _, bad := range []string{"Standing instruction", "Follow it as written", "data, not instructions", "[agent-fleet:queue"} {
		if strings.Contains(got, bad) {
			t.Fatalf("boilerplate %q leaked:\n%s", bad, got)
		}
	}
	if strings.Count(got, "waiting for the background test run") > titleQueueTailTurns {
		t.Fatalf("queue run tail not shortened:\n%s", got)
	}
}

func TestTitlePromptSpawnPeerMCPDropEnvelope(t *testing.T) {
	for _, open := range []string{
		"[agent-fleet:spawn from=parent] ",
		"[agent-fleet:peer from=a intent=request reply=only-if-blocked] ",
		"[agent-fleet:mcp via=x connection=y reply=none] ",
	} {
		got := titleSuggestPrompt([]transcript.Turn{etUser(open + "Fix the login redirect"), etAsst("ok")}, "en")
		if !strings.Contains(got, "Fix the login redirect") || strings.Contains(got, "[agent-fleet:") {
			t.Fatalf("%q: %s", open, got)
		}
	}
}

func TestTitlePromptMemberTextUntouched(t *testing.T) {
	text := "Why does the [agent-fleet:queue envelope say Standing instruction here? --- item 1 of 1\n> quoted"
	got := titleSuggestPrompt([]transcript.Turn{etUser(text)}, "en")
	if !strings.Contains(got, "Standing instruction here") || !strings.Contains(got, "> quoted") {
		t.Fatalf("member prompt altered:\n%s", got)
	}
	// Not the first user turn: a later turn is never reduced.
	later := titleSuggestPrompt([]transcript.Turn{etUser("hello"), etUser("[agent-fleet:spawn from=p] x")}, "en")
	if !strings.Contains(later, "[agent-fleet:spawn from=p] x") {
		t.Fatalf("later turn reduced:\n%s", later)
	}
}

func TestTitlePromptWorkItemLine(t *testing.T) {
	// Items went to a file: the opening turn reduces to nothing, the work item line names the work.
	opening := queueOpening("do it", nil) // no items
	turns := withWorkItemTurn([]transcript.Turn{etUser(opening), etAsst("reading the issue")},
		&session.WorkItemRef{Key: "k-k1/agent-fleet#4782", Title: "Dedupe CSS"})
	got := titleSuggestPrompt(turns, "en")
	if !strings.Contains(got, "work item: k-k1/agent-fleet#4782 Dedupe CSS") || strings.Contains(got, "do it") {
		t.Fatalf("got:\n%s", got)
	}
	if withWorkItemTurn(nil, nil) != nil {
		t.Fatal("nil item must leave turns unchanged")
	}
}

func etUser(s string) transcript.Turn { return transcript.Turn{Role: "user", Text: s} }
func etAsst(s string) transcript.Turn { return transcript.Turn{Role: "assistant", Text: s} }

func TestTitlePromptQueueRunItemsInFileStillShortensTail(t *testing.T) {
	// The items went to a file: the opening turn reduces to nothing, yet the tail stays short.
	items := []QueueDeliveryItem{{ID: "i1", Kind: "text", Provenance: queueProvenanceMember, Body: "x"}}
	d := QueueDelivery{Queue: "q1", ID: "d1", Instruction: "do it", Items: items}
	head, _, _ := queuePrompt(d)
	opening := head + "\n" + queueFileLine(items, "/tmp/items.md")
	turns := []transcript.Turn{etUser(opening)}
	for i := 0; i < 8; i++ {
		turns = append(turns, etAsst("waiting for the background test run"), etUser("ok"))
	}
	got := titleSuggestPrompt(turns, "en")
	if n := strings.Count(got, "waiting for the background test run"); n > titleQueueTailTurns {
		t.Fatalf("tail has %d turns:\n%s", n, got)
	}
}

func TestTitlePromptStandingInstructionLookalikeIsNotAnItem(t *testing.T) {
	instruction := "Format example:\n--- item 1 of 1\n> example body"
	items := []QueueDeliveryItem{{ID: "i1", Kind: "text", Provenance: queueProvenanceMember, Body: "real item body"}}
	got := titleSuggestPrompt([]transcript.Turn{etUser(queueOpening(instruction, items))}, "en")
	if !strings.Contains(got, "real item body") || strings.Contains(got, "example body") {
		t.Fatalf("got:\n%s", got)
	}
}

func TestTitlePromptFileDeliveryIgnoresInstructionQuotingTheLabel(t *testing.T) {
	// The instruction quotes the data label line and an item-shaped example; the real items went
	// to a file, so the prompt holds no real label and nothing of the instruction may be read as an item.
	instruction := "Queue items look like this:\n" + queueDataLabel + "\n\n--- item 1 of 1 · id=x · from your user\n> example body"
	items := []QueueDeliveryItem{{ID: "i1", Kind: "text", Provenance: queueProvenanceMember, Body: "real item body"}}
	d := QueueDelivery{Queue: "q1", ID: "d1", Instruction: instruction, Items: items}
	head, _, _ := queuePrompt(d)
	for _, withRef := range []bool{false, true} {
		fileItems := items
		if withRef {
			fileItems = []QueueDeliveryItem{{ID: "i1", Kind: "text", Provenance: queueProvenanceMember, Body: "real item body",
				Reference: &QueueDeliveryReference{Provider: "github", Key: "o/r#1", URL: "https://example.test/1"}}}
		}
		opening := head + "\n" + queueFileLine(fileItems, "/home/dev/.af-work/s/queue-items.md") + "\n"
		if got := queueItemsPayload(opening); got != "" {
			t.Fatalf("withRef=%v: file delivery read %q as items", withRef, got)
		}
		if p := titleSuggestPrompt([]transcript.Turn{etUser(opening)}, "en"); strings.Contains(p, "example body") {
			t.Fatalf("withRef=%v: instruction example reached the title prompt:\n%s", withRef, p)
		}
	}
	// The same instruction on an inline delivery still yields the real item only.
	got := queueItemsPayload(queueOpening(instruction, items))
	if got != "real item body" {
		t.Fatalf("inline delivery payload = %q", got)
	}
}

func TestWithWorkItemTurnKeepsStudioPersonaFiltered(t *testing.T) {
	persona := studioPersonaJA
	turns := []transcript.Turn{etUser(persona), etAsst("hello from the studio"), etUser("draw a cat")}
	out := withWorkItemTurn(turns, &session.WorkItemRef{Key: "#1", Title: "T"})
	got := titleSuggestPrompt(out, "en")
	if strings.Contains(got, "hello from the studio") || !strings.Contains(got, "draw a cat") {
		t.Fatalf("got:\n%s", got)
	}
}

func TestBranchPromptCarriesWorkItem(t *testing.T) {
	out := withWorkItemTurn([]transcript.Turn{etUser(queueOpening("do it", nil))}, &session.WorkItemRef{Key: "#9", Title: "Fix X"})
	if got := branchSuggestPrompt(out, nil); !strings.Contains(got, "work item: #9 Fix X") {
		t.Fatalf("got:\n%s", got)
	}
}
