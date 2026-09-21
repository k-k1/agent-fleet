// transcript.go is AF's own copy of a muse session's conversation, written from the live
// `item/*` stream and read back by the read layer.
//
// ADR 0095 decision 4 expected the at-rest half to parse muse's own
// `~/.local/share/muse/sessions/.../session.jsonl`, on the premise that the file "holds the
// same records" as the wire. Measured, it does not: that file is an event-sourced RUNTIME log
// in muse's internal vocabulary (`runtime.session` / `runtime.session.task` records carrying
// `started`, `model_request_configured`, `assistant_message_committed`, `terminal`,
// `goal_usage_attribution`, …), not a list of wire `Item`s. A reader for it would be exactly
// the transcript reverse-engineering this kind was supposed to get for free, against an
// internal format with no stability promise.
//
// The protocol's own answer is `session/read`, which returns `SessionHistory.items` — the
// stable surface. But it needs a running host, and `Transcript` is called from the usage
// aggregation as well as the mirror, so it must be a local disk read and must never spawn a
// 73 MiB process. So AF keeps its own store, the shape ADR 0093 decision 3 already uses for
// lcpp. The difference from lcpp is worth stating: there the store IS the conversation, here
// the host owns it and this is a MIRROR of what AF saw. What that costs is a turn that ran
// while AF was not watching, which under decision 2 (managed-only, AF is the only writer) can
// only happen if the Agent died mid-turn — and `session/read` on the next Resume is the
// documented way to backfill it.
package muse

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// record is one persisted line: the wire item, plus when AF saw it. The wire's own
// `recordedAt` is the host's clock and is optional, so the observation time is kept beside it
// rather than instead of it.
type record struct {
	Seen string   `json:"seen"`
	Item msp.Item `json:"item"`
}

// store is one session's append-only item log.
//
// Append-only is not a stylistic choice: items are REVISED on the wire (an `item/started`, any
// number of `item/delta`s and an `item/completed` share an `itemId` and carry an increasing
// `revision`), and a read-modify-write to keep one line per item would lose a concurrent
// append on a crash. Folding by revision happens on read instead.
type store struct {
	sid string
	mu  sync.Mutex
}

var storesMu sync.Mutex
var stores = map[string]*store{}

// openStore returns the (process-wide single) store for a slot sid. One writer per file
// matters: two store values would each hold their own mutex and interleave partial lines.
func openStore(sid string) *store {
	storesMu.Lock()
	defer storesMu.Unlock()
	if s := stores[sid]; s != nil {
		return s
	}
	s := &store{sid: sid}
	stores[sid] = s
	return s
}

func storeDir() string { return filepath.Join(paths.AgentStateDir(), "muse-transcripts") }

func (s *store) Path() string { return filepath.Join(storeDir(), s.sid+".jsonl") }

// Append writes one item. A failure is returned rather than swallowed so the caller can log
// it once; it is never fatal to a turn, because the host still owns the conversation.
func (s *store) Append(it msp.Item) error {
	line, err := json.Marshal(record{Seen: time.Now().UTC().Format(time.RFC3339Nano), Item: it})
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(storeDir(), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(s.Path(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}

// Items folds the log into the current state of each item: highest revision wins, and a later
// line wins a tie, because `item/updated` may re-send a revision the store already has. Order
// is FIRST-SEEN order, which is the order the conversation happened in — sorting by revision
// or by id would reshuffle a turn whose tool call completed after the text that follows it.
func (s *store) Items() ([]msp.Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.Open(s.Path())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // a session that has not spoken yet is not an error
		}
		return nil, err
	}
	defer f.Close()

	var order []string
	byID := map[string]msp.Item{}
	sc := bufio.NewScanner(f)
	// One item can carry a whole tool output, so the stock 64 KiB line limit would turn a
	// large-but-legitimate record into a truncated conversation.
	sc.Buffer(make([]byte, 0, 256*1024), 16*1024*1024)
	for sc.Scan() {
		var r record
		if json.Unmarshal(sc.Bytes(), &r) != nil || r.Item.ItemID == "" {
			continue // a torn last line after a crash must not lose the whole history
		}
		prev, seen := byID[r.Item.ItemID]
		if !seen {
			order = append(order, r.Item.ItemID)
		}
		if seen && r.Item.Revision < prev.Revision {
			continue
		}
		byID[r.Item.ItemID] = r.Item
	}
	items := make([]msp.Item, 0, len(order))
	for _, id := range order {
		items = append(items, byID[id])
	}
	return items, sc.Err()
}

// Remove drops the stored conversation (a slot whose identity is being discarded).
func (s *store) Remove() {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = os.Remove(s.Path())
}

// --- items to turns ----------------------------------------------------------

// turnsFromItems renders the item log as the mirror's turn model.
//
// Grouping: a `userMessage` opens a user turn, and every assistant-side item that follows
// folds into ONE assistant turn, which closes at the next user message. Items carry a
// `turnId`, but it is not the grouping key — a turn that was steered carries items from
// before and after the injection, and the host also emits items with no turn id at all
// (a compaction between turns), so grouping on it would drop them.
func turnsFromItems(items []msp.Item) []transcript.Turn {
	var turns []transcript.Turn
	assistant := -1 // index of the open assistant turn, -1 when none

	openAssistant := func(it msp.Item) *transcript.Turn {
		if assistant < 0 {
			turns = append(turns, transcript.Turn{
				Role: "assistant", TS: itemTS(it), AnchorID: it.ItemID, Idx: len(turns),
				Sidechain: isSidechain(it),
			})
			assistant = len(turns) - 1
		}
		return &turns[assistant]
	}

	for _, it := range items {
		switch it.Kind {
		case msp.ItemKindUserMessage:
			text := str(it.Text)
			turns = append(turns, transcript.Turn{
				Role: "user", TS: itemTS(it), AnchorID: it.ItemID, Idx: len(turns),
				Parts: []transcript.Part{{Kind: "text", Text: text}},
				Text:  text,
				// A steered message was injected into a running turn, not typed at the
				// start of one, but it is still the member's own words.
				Sidechain: isSidechain(it),
			})
			assistant = -1

		case msp.ItemKindUserShell:
			// The member's own `!command`: their input, so it opens a user turn rather than
			// being attributed to the agent.
			cmd := str(it.CommandText)
			turns = append(turns, transcript.Turn{
				Role: "user", TS: itemTS(it), AnchorID: it.ItemID, Idx: len(turns),
				Parts: []transcript.Part{{
					Kind: "tool", Tool: "shell", Info: transcript.Clip(cmd),
					Output: transcript.CapOutput(str(it.VisibleOutput)),
				}},
				Text: cmd,
			})
			assistant = -1

		case msp.ItemKindAgentMessage:
			t := openAssistant(it)
			text := str(it.Text)
			if strings.TrimSpace(text) != "" {
				t.Parts = append(t.Parts, transcript.Part{Kind: "text", Text: text})
				t.Text = joinText(t.Text, text)
			}
			applyUsage(t, it)

		case msp.ItemKindReasoning:
			t := openAssistant(it)
			if text := str(it.Text); strings.TrimSpace(text) != "" {
				t.Parts = append(t.Parts, transcript.Part{Kind: "thinking", Text: text})
			}
			applyUsage(t, it)

		case msp.ItemKindToolCall:
			t := openAssistant(it)
			t.Parts = append(t.Parts, toolPart(it))
			applyUsage(t, it)

		case msp.ItemKindSubagent, msp.ItemKindWorkflow, msp.ItemKindReminderChild:
			t := openAssistant(it)
			t.Parts = append(t.Parts, delegationPart(it))
			applyUsage(t, it)

		case msp.ItemKindCompaction:
			// The same convention claude's own compaction summary uses: a collapsible
			// "context compacted" block, not an ordinary user prompt.
			text := compactionText(it)
			turns = append(turns, transcript.Turn{
				Role: "user", Compact: true, TS: itemTS(it), AnchorID: it.ItemID, Idx: len(turns),
				Parts: []transcript.Part{{Kind: "text", Text: text}},
				Text:  text,
			})
			assistant = -1
		}
	}
	return turns
}

// toolPart renders a tool call. `visibleOutput` is the host's own already-redacted rendering
// of the result — the raw bytes live behind `outputRef` and are fetched with item/readOutput,
// which the mirror does not need for a summary line.
func toolPart(it msp.Item) transcript.Part {
	p := transcript.Part{Kind: "tool", Tool: str(it.Tool)}
	switch {
	case it.CommandText != nil && *it.CommandText != "":
		p.Info = transcript.Clip(*it.CommandText)
	case it.DisplayText != nil && *it.DisplayText != "":
		p.Info = transcript.Clip(*it.DisplayText)
	case it.Args != nil:
		p.Info = transcript.Clip(*it.Args)
	}
	p.Output = transcript.CapOutput(str(it.VisibleOutput))
	// A failed tool call whose output is empty would otherwise render as a blank result,
	// which reads as "it worked and said nothing".
	if p.Output == "" && it.Status != msp.ItemStatusCompleted {
		p.Output = failureText(it)
	}
	return p
}

// delegationPart renders a subagent, a workflow or an observer child. Agent Fleet's mirror
// already has a shape for "this turn handed work to a child", and muse's three flavours of
// child all fit it.
func delegationPart(it msp.Item) transcript.Part {
	p := transcript.Part{
		Kind:      "delegation",
		Tool:      string(it.Kind),
		AgentType: str(it.AgentPath),
		Prompt:    str(it.Objective),
		Status:    string(it.Status),
	}
	if p.AgentType == "" {
		p.AgentType = str(it.ReminderAgentID)
	}
	if p.Info = transcript.Clip(str(it.DisplayText)); p.Info == "" {
		p.Info = transcript.Clip(p.Prompt)
	}
	if it.Result != nil {
		out := it.Result.Summary
		if out == "" {
			out = str(it.Result.Text)
		}
		p.Output = transcript.CapOutput(out)
	}
	if p.Output == "" && it.Status != msp.ItemStatusCompleted {
		p.Output = failureText(it)
	}
	return p
}

// compactionText prefers the host's own summary lines; a compaction that carries none still
// has to render as something, or the member sees an empty collapsed block where their history
// was replaced.
func compactionText(it msp.Item) string {
	if len(it.Summary) > 0 {
		return strings.Join(it.Summary, "\n")
	}
	if msg := str(it.Message); msg != "" {
		return msg
	}
	if it.TokensBefore != nil && it.TokensAfter != nil {
		return fmt.Sprintf("context compacted: %d → %d tokens", *it.TokensBefore, *it.TokensAfter)
	}
	return "context compacted"
}

func failureText(it msp.Item) string {
	for _, s := range []string{str(it.FailureReason), str(it.Message), str(it.Reason), str(it.FailureKind)} {
		if s != "" {
			return transcript.CapOutput(s)
		}
	}
	return string(it.Status)
}

// applyUsage folds an item's token usage onto its turn.
//
// The numbers are per model completion and several items can share one, so they are SUMMED
// rather than overwritten — except the cache and input figures, which the schema states are
// counted once per completion, so the latest wins. What is missing here is not missing by
// accident: subagent and observer model calls never reach the wire at all, which is why this
// kind's accounting is declared partial rather than exact (ADR 0095 decision 10).
func applyUsage(t *transcript.Turn, it msp.Item) {
	u := it.Usage
	if u == nil {
		return
	}
	t.OutTok += int(u.OutputTokens)
	t.InTok = int(u.InputTokens)
	t.CacheRead = int(u.CachedTokens)
	if u.CacheReadTokens != nil {
		t.CacheRead = int(*u.CacheReadTokens)
	}
	if u.CacheWriteTokens != nil {
		t.CacheCreate = int(*u.CacheWriteTokens)
	}
}

// isSidechain reports whether the item belongs to a child rather than the root session.
// Subagent records are interleaved into the parent's own stream under their own stream id
// (measured — there is no per-child directory), so this is what tells them apart.
func isSidechain(it msp.Item) bool {
	if it.SubagentID != nil && *it.SubagentID != "" {
		return true
	}
	if it.ChildSessionID != nil && *it.ChildSessionID != "" {
		return true
	}
	return it.Depth != nil && *it.Depth > 0
}

func itemTS(it msp.Item) string { return str(it.RecordedAt) }

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func joinText(a, b string) string {
	if a == "" {
		return b
	}
	return a + "\n" + b
}

// appendStreaming splices the fragments of items that have not completed yet onto the end of
// the rendered turns, so the mirror shows an answer as it is written rather than only once
// the item lands.
//
// It appends rather than patching in place on purpose: an item still streaming has, by
// definition, not been persisted yet, so there is no rendered part to patch. Fragments for an
// item the store already holds are dropped — that is a delta that arrived after its own
// completion, and re-appending it would duplicate text the turn already shows.
func appendStreaming(turns []transcript.Turn, items []msp.Item, fragments map[string]string) []transcript.Turn {
	known := make(map[string]bool, len(items))
	for _, it := range items {
		known[it.ItemID] = true
	}
	// Map order is random, and the mirror must not reshuffle between two polls of the same
	// unchanged state, so the fragments are emitted in item-id order.
	ids := make([]string, 0, len(fragments))
	for id := range fragments {
		if !known[id] && strings.TrimSpace(fragments[id]) != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)

	for _, id := range ids {
		text := fragments[id]
		if n := len(turns); n > 0 && turns[n-1].Role == "assistant" && turns[n-1].AnchorID == "" {
			turns[n-1].Parts = append(turns[n-1].Parts, transcript.Part{Kind: "text", Text: text})
			turns[n-1].Text = joinText(turns[n-1].Text, text)
			continue
		}
		turns = append(turns, transcript.Turn{
			Role: "assistant", Idx: len(turns),
			Parts: []transcript.Part{{Kind: "text", Text: text}},
			Text:  text,
		})
	}
	return turns
}
