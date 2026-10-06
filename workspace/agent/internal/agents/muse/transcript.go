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
// internal format with no stability promise. The one narrow exception is the commentary the
// wire omits, which the read layer splices in beside its tool call (commentary.go).
//
// The protocol's own answer is `session/read`, which returns `SessionHistory.items` — the
// stable surface. But it needs a running host, and `Transcript` is called from the usage
// aggregation as well as the mirror, so it must be a local disk read and must never spawn a
// 73 MiB process. So AF keeps its own store, the shape ADR 0093 decision 3 already uses for
// lcpp. The difference from lcpp is worth stating: there the store IS the conversation, here
// the host owns it and this is a MIRROR of what AF saw. What that costs is a turn that ran
// while AF was not watching, which under decision 2 (managed-only, AF is the only writer) can
// only happen if the Agent died mid-turn — and the next Resume backfills it from the host's
// folded history (backfill.go).
package muse

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
	// Model is the model the host reported as selected when AF saw the item; items carry no
	// model of their own. Empty on lines written before it was recorded.
	Model string `json:"model,omitempty"`
	// Images are the paths of the attachments AF sent as `image` parts, on a `userMessage`
	// only. The wire echoes their metadata but never the path (handle.go's sentImages).
	Images []string `json:"images,omitempty"`
	// Order, on a line with no item, is the host's own item order as of a resume backfill
	// (backfill.go). An older Agent skips the line as an item without an id.
	Order []string `json:"order,omitempty"`
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
func (s *store) Append(it msp.Item) error { return s.AppendFrom(it, "") }

// AppendFrom is Append recording the model in force when the item arrived.
func (s *store) AppendFrom(it msp.Item, model string) error {
	return s.appendRecord(record{Item: it, Model: model})
}

// appendRecord writes one record, stamping when AF saw it.
func (s *store) appendRecord(r record) error {
	r.Seen = time.Now().UTC().Format(time.RFC3339Nano)
	line, err := json.Marshal(r)
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
	items, _, err := s.itemsWithMeta()
	return items, err
}

// itemMeta is what AF recorded beside an item rather than in it.
type itemMeta struct {
	model  string
	images []string
}

// itemsWithMeta is Items plus AF's own stamps on each item, by item id.
func (s *store) itemsWithMeta() ([]msp.Item, map[string]itemMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.Open(s.Path())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil // a session that has not spoken yet is not an error
		}
		return nil, nil, err
	}
	defer f.Close()

	var order []string
	byID := map[string]msp.Item{}
	meta := map[string]itemMeta{}
	sc := bufio.NewScanner(f)
	// One item can carry a whole tool output, so the stock 64 KiB line limit would turn a
	// large-but-legitimate record into a truncated conversation.
	sc.Buffer(make([]byte, 0, 256*1024), 16*1024*1024)
	for sc.Scan() {
		var r record
		if json.Unmarshal(sc.Bytes(), &r) != nil {
			continue // a torn last line after a crash must not lose the whole history
		}
		if r.Item.ItemID == "" {
			// Applied where it stands, to the order built so far: a later, shorter fold (an
			// anchored snapshot) must not undo what an earlier one put right before its anchor,
			// and an item first seen after the line lands after the fold it describes.
			if len(r.Order) > 0 {
				order = mergeOrder(order, r.Order, byID)
			}
			continue
		}
		prev, seen := byID[r.Item.ItemID]
		if !seen {
			order = append(order, r.Item.ItemID)
		}
		if seen && r.Item.Revision < prev.Revision {
			continue
		}
		byID[r.Item.ItemID] = r.Item
		// The first stamp wins: a later revision of the same item may arrive after a switch,
		// and only the first revision of a user message carries its images.
		m := meta[r.Item.ItemID]
		if m.model == "" {
			m.model = r.Model
		}
		if m.images == nil {
			m.images = r.Images
		}
		meta[r.Item.ItemID] = m
	}
	items := make([]msp.Item, 0, len(order))
	for _, id := range order {
		items = append(items, byID[id])
	}
	return items, meta, sc.Err()
}

// mergeOrder puts the items in the host's order where the host has spoken, and keeps every
// other item where AF saw it. First-seen order is the conversation's order only when nothing
// was missed: an item backfilled after a failed mirror write or a lost notification is
// first seen at the end, and turnsFromItems would fold a missed reply into the NEXT user
// message's turn. So the host's order wins for the items it lists. An item it does not list
// follows the latest-placed host item it followed in seen: one from before a compaction
// anchor (preceding every listed item) stays in front. A live item newer than the fold is
// never in seen here — the resume holds live items until the order line is written
// (holdItems), so they are first seen after it.
func mergeOrder(seen, host []string, byID map[string]msp.Item) []string {
	if len(host) == 0 {
		return seen
	}
	idx := make(map[string]int, len(host))
	for i, id := range host {
		if _, ok := byID[id]; ok {
			if _, dup := idx[id]; !dup {
				idx[id] = i
			}
		}
	}
	var front []string
	after := make(map[int][]string)
	last := -1
	for _, id := range seen {
		if i, ok := idx[id]; ok {
			last = max(last, i)
			continue
		}
		if last < 0 {
			front = append(front, id)
		} else {
			after[last] = append(after[last], id)
		}
	}
	out := make([]string, 0, len(seen))
	out = append(out, front...)
	for i, id := range host {
		if j, ok := idx[id]; ok && j == i {
			out = append(out, id)
		}
		out = append(out, after[i]...)
	}
	return out
}

// Remove drops the stored conversation (a slot whose identity is being discarded).
func (s *store) Remove() {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = os.Remove(s.Path())
}

// --- items to turns ----------------------------------------------------------

// imagePlaceholder is how the host writes an `image` part into a user message's text.
var imagePlaceholder = regexp.MustCompile(`\[Image #\d+\]`)

// withImagePaths puts the paths of a user turn's images into its text, replacing the host's
// `[Image #N]` placeholders. The mirror finds thumbnails (and reconciles the send's echo) by
// the pasted path in the text (console/src/lib/pastedImages.ts), the same way it reads the
// path codex leaves in its rollout; a placeholder gives it nothing to load.
func withImagePaths(t *transcript.Turn, images []string) {
	text := strings.TrimSpace(imagePlaceholder.ReplaceAllString(t.Text, ""))
	text = strings.TrimSpace(text + " " + strings.Join(images, " "))
	t.Text = text
	for i := range t.Parts {
		if t.Parts[i].Kind == "text" {
			t.Parts[i].Text = text
			return
		}
	}
	t.Parts = append(t.Parts, transcript.Part{Kind: "text", Text: text})
}

// turnsFromItems renders the item log as the mirror's turn model.
//
// Grouping: a `userMessage` opens a user turn, and every assistant-side item that follows
// folds into ONE assistant turn, which closes at the next user message. Items carry a
// `turnId`, but it is not the grouping key — a turn that was steered carries items from
// before and after the injection, and the host also emits items with no turn id at all
// (a compaction between turns), so grouping on it would drop them.
func turnsFromItems(items []msp.Item) []transcript.Turn { return turnsWithCommentary(items, nil) }

// turnsWithCommentary is turnsFromItems with the runtime log's commentary (commentary.go) put
// in front of the tool call it introduced. A commentary the wire did deliver — an agentMessage
// with the same id, which is the commit's message_id — is not added a second time.
func turnsWithCommentary(items []msp.Item, cs *commentarySet) []transcript.Turn {
	delivered := map[string]bool{}
	if cs != nil {
		for _, it := range items {
			if it.Kind == msp.ItemKindAgentMessage {
				delivered[it.ItemID] = true
			}
		}
	}
	var turns []transcript.Turn
	assistant := -1 // index of the open assistant turn, -1 when none

	// 🔴 EndTS is advanced on every folded item, and it is not a nicety. muse's whole assistant
	// turn is ONE row (unlike claude/codex, which split a turn across rows whose own ts is each
	// row's end), so with only TS the mirror's footer shows the turn's START — `footTime` falls
	// back to ts when endTs is absent, and a 90-second turn would be stamped 90 seconds early.
	// This is the same shape opencode and copilot carry endTs for (turnTime.ts's own comment);
	// muse is the third kind in that family and the first to fold it in the Agent rather than
	// read it off the agent's own span record.
	openAssistant := func(it msp.Item) *transcript.Turn {
		if assistant < 0 {
			turns = append(turns, transcript.Turn{
				Role: "assistant", TS: itemTS(it), AnchorID: it.ItemID, Idx: len(turns),
				Sidechain: isSidechain(it),
			})
			assistant = len(turns) - 1
		}
		t := &turns[assistant]
		// First-seen order is the store's order (transcript.go's header), so the newest item
		// folded is the latest moment the turn is known to have reached. An item with no
		// recorded time leaves the previous end alone rather than clearing it.
		if ts := itemTS(it); ts != "" {
			t.EndTS = ts
		}
		return t
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
			if it.CallID != nil {
				for _, c := range cs.take(*it.CallID) {
					if !delivered[c.id] {
						t.Parts = append(t.Parts, transcript.Part{Kind: "text", Text: c.text})
						t.Text = joinText(t.Text, c.text)
					}
				}
			}
			if str(it.Tool) == "request_user_input" {
				// A question stays in the history as a question, the way claude's and codex's
				// do, rather than as a raw tool line. While it waits it adds nothing: the
				// pending card stands for it, and the host settles every prompt with an output
				// (answered, cancelled, or aborted with its run), so none is left out.
				if qp, ok := questionPart(it); ok {
					t.Parts = append(t.Parts, qp)
				} else if strings.TrimSpace(str(it.VisibleOutput)) != "" {
					t.Parts = append(t.Parts, toolPart(it))
				}
			} else {
				t.Parts = append(t.Parts, toolPart(it))
			}
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

		default:
			// `hookRun` and any kind a later muse adds: the schema's rule is that an unknown
			// kind is rendered generically, never skipped — a dropped item is a hook that ran
			// (and perhaps blocked a tool) with no trace in the mirror.
			t := openAssistant(it)
			t.Parts = append(t.Parts, genericPart(it))
			applyUsage(t, it)
		}
	}
	return turns
}

// genericPart renders an item whose kind this client has no dedicated shape for: the kind name
// as the tool, the one-line summary the host supplies as the info, and — only when the item
// did not complete — the reason, so a blocked or failed hook is not shown as a silent success.
// A `hookRun` carries its summary in `label`; other kinds in `fallbackText` (tdd SS4.10).
func genericPart(it msp.Item) transcript.Part {
	p := transcript.Part{Kind: "tool", Tool: string(it.Kind)}
	for _, s := range []string{str(it.Label), str(it.FallbackText), str(it.DisplayText)} {
		if s != "" {
			p.Info = transcript.Clip(s)
			break
		}
	}
	if p.Info == "" && it.Event != nil {
		p.Info = string(*it.Event)
	}
	if it.Status != msp.ItemStatusCompleted {
		p.Output = failureText(it)
		if it.RunStatus != nil && *it.RunStatus != msp.HookRunStatusCompleted && str(it.Reason) == "" {
			p.Output = string(*it.RunStatus)
		}
	}
	return p
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
	// An edit-family call additionally carries its target and before/after, which is what the
	// changed-files strip counts and what opens the trace as a diff (fileedits.go).
	if f, verb, es := toolEdits(p.Tool, str(it.Args)); f != "" {
		p.File, p.Verb, p.Edits = f, verb, es
	}
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

// ForkAt copies this store's items into a new slot's store, up to and including the last item
// of the turn named by cutTurnID. An empty cutTurnID copies everything, which is what a
// whole-conversation fork means.
//
// It exists because AF's store is the at-rest transcript (this file's header): `session/fork`
// copies the HOST's history into the new muse session, and without this the forked AF session
// would show an empty conversation until its first turn — the opposite of what forking is for.
//
// The destination is written once and never merged into: a store that already exists belongs
// to a slot that has already lived, and copying over it would splice two conversations.
func (s *store) ForkAt(newSID, cutTurnID string) error {
	items, meta, err := s.itemsWithMeta()
	if err != nil {
		return err
	}
	cut := len(items) - 1
	if cutTurnID != "" {
		cut = -1
		for i, it := range items {
			if it.TurnID != nil && *it.TurnID == cutTurnID {
				cut = i
			}
		}
		if cut < 0 {
			return fmt.Errorf("フォーク位置のターンが見つかりません: %s", cutTurnID)
		}
	}
	dst := openStore(newSID)
	if _, err := os.Stat(dst.Path()); err == nil {
		return nil
	}
	for i := 0; i <= cut && i < len(items); i++ {
		m := meta[items[i].ItemID]
		if err := dst.appendRecord(record{Item: items[i], Model: m.model, Images: m.images}); err != nil {
			return err
		}
	}
	return nil
}

// turnOrder is the distinct turn ids in first-seen order, which is the order the conversation
// happened in. Items with no turn id (a compaction between turns, a `userShell`) carry no
// boundary and are skipped rather than treated as a turn of their own.
func turnOrder(items []msp.Item) []string {
	seen := map[string]bool{}
	var out []string
	for _, it := range items {
		if it.TurnID == nil || *it.TurnID == "" || seen[*it.TurnID] {
			continue
		}
		seen[*it.TurnID] = true
		out = append(out, *it.TurnID)
	}
	return out
}

// questionPart renders a settled request_user_input call as the mirror's question block. ok is
// false while the call has no output yet, or when its arguments carry no question.
func questionPart(it msp.Item) (transcript.Part, bool) {
	out := strings.TrimSpace(str(it.VisibleOutput))
	if out == "" {
		return transcript.Part{}, false
	}
	qs, ids := userInputQuestions(str(it.Args))
	if len(qs) == 0 {
		return transcript.Part{}, false
	}
	p := transcript.Part{Kind: "question", Tool: "request_user_input", Questions: qs, QID: str(it.CallID)}
	p.Answer, p.Declined = userInputOutcome(out, qs, ids)
	return p, true
}

// userInputQuestions reads the model-authored arguments. Muse's own selection shape
// (`selection.mode`) and the AskUserQuestion-style `multiSelect` are both accepted, since the
// arguments are whatever the model wrote. The ids are dropped: a question with an id is one the
// Console offers to answer, and this one is history; they come back separately, for matching
// the answers.
func userInputQuestions(args string) ([]transcript.Question, []string) {
	var a struct {
		Questions []struct {
			transcript.Question
			Selection struct {
				Mode string `json:"mode"`
			} `json:"selection"`
		} `json:"questions"`
	}
	if json.Unmarshal([]byte(args), &a) != nil {
		return nil, nil
	}
	qs := make([]transcript.Question, 0, len(a.Questions))
	ids := make([]string, 0, len(a.Questions))
	for _, q := range a.Questions {
		tq := q.Question
		ids = append(ids, tq.ID)
		tq.ID = ""
		if q.Selection.Mode == string(msp.UserInputSelectionModeMultiple) {
			tq.MultiSelect = true
		}
		qs = append(qs, tq)
	}
	return qs, ids
}

// userInputOutcome turns the tool's output into the answer text the question block reads, and
// whether the prompt was declined rather than answered.
//
// Measured on 1.4.2-R4684.1, an answered prompt reads
// {"status":"answered","answers":[{"id":"color","selected_label":"Red"},…]} — keyed by `id`,
// snake_case — and a declined or aborted one {"status":"cancelled"|"aborted","answers":[],
// "reason":"…"}. The camelCase keys of the wire's own UserInputAnswer are read too, in case a
// release aligns the two. A declined block still needs a
// non-empty answer to show as settled, so it carries the reason. An output this cannot read is
// shown verbatim rather than dropped.
func userInputOutcome(out string, qs []transcript.Question, ids []string) (string, bool) {
	var o struct {
		Status  string            `json:"status"`
		Reason  string            `json:"reason"`
		Answers []json.RawMessage `json:"answers"`
	}
	if json.Unmarshal([]byte(out), &o) != nil {
		return out, false
	}
	if len(o.Answers) == 0 {
		why := o.Reason
		if why == "" {
			why = o.Status
		}
		if why == "" {
			why = out
		}
		return why, true
	}
	per := make([]string, len(qs))
	byID := map[string]int{}
	for i, id := range ids {
		if id != "" {
			byID[id] = i
		}
	}
	for i, raw := range o.Answers {
		id, text := userInputAnswerText(raw)
		at := i
		if j, ok := byID[id]; ok {
			at = j
		}
		if at < len(per) && text != "" {
			per[at] = text
		}
	}
	if len(qs) == 1 {
		return per[0], false
	}
	// The anchored form claude's AskUserQuestion result uses, which the Console splits per
	// question (questionAnswers.ts).
	pairs := make([]string, 0, len(qs))
	for i, q := range qs {
		pairs = append(pairs, `"`+q.Question+`"="`+per[i]+`"`)
	}
	return strings.Join(pairs, ", "), false
}

// userInputAnswerText reads one answer: the question it belongs to, and its picks and typed
// text joined the way the question block resolves them (labels it knows become picks, the
// rest is the member's own words).
func userInputAnswerText(raw json.RawMessage) (string, string) {
	var a struct {
		ID             string   `json:"id"`
		QuestionID     string   `json:"questionId"`
		QuestionIDSn   string   `json:"question_id"`
		SelectedLabel  string   `json:"selectedLabel"`
		SelectedLabelS string   `json:"selected_label"`
		Labels         []string `json:"selectedLabels"`
		LabelsS        []string `json:"selected_labels"`
		FreeText       string   `json:"freeText"`
		FreeTextS      string   `json:"free_text"`
	}
	if json.Unmarshal(raw, &a) != nil {
		return "", strings.TrimSpace(string(raw))
	}
	id := firstNonEmpty(a.ID, a.QuestionID, a.QuestionIDSn)
	labels := append(a.Labels, a.LabelsS...)
	if len(labels) == 0 {
		if l := firstNonEmpty(a.SelectedLabel, a.SelectedLabelS); l != "" {
			labels = []string{l}
		}
	}
	if t := strings.TrimSpace(firstNonEmpty(a.FreeText, a.FreeTextS)); t != "" {
		labels = append(labels, t)
	}
	return id, strings.Join(labels, ", ")
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
