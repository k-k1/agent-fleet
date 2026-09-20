// Package lcpp is the ADR 0093 decision 3 transcript store for the (not yet wired) lcpp
// session kind: unlike every other kind, which normalizes a CLI's own store into
// transcript.Turn, lcpp's harness writes its own store and reads it back. store.go is that
// store — the one place a record log becomes both representations decision 3 asks for: the
// OpenAI messages slice the harness sends next turn, and the transcript.Turn slice the mirror
// renders. Nothing here wires a session kind, a driver, or a KindLcpp constant; those are a
// separate, parallel piece of work (docs/log/99-lcpp-agent-kind.md §4.3/§4.4).
package lcpp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/harness"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// Kind is a Record's own type, one of the five decision 3 lists (user/assistant/tool-result/
// system-note/usage) plus Continuation — the synthetic loop turn split out of "user" so a
// reader never has to re-derive it from content (see AppendMessage).
type Kind string

const (
	KindUser         Kind = "user"
	KindContinuation Kind = "continuation"
	KindAssistant    Kind = "assistant"
	KindToolResult   Kind = "tool_result"
	KindSystemNote   Kind = "system_note"
	KindUsage        Kind = "usage"
)

// Note distinguishes the two system-note reasons decision 3 lists.
type Note string

const (
	NoteCompaction  Note = "compaction"
	NoteModelChange Note = "model_change"
)

// Record is one append-only JSONL line. Every field beyond ID/TS/Kind is meaningful for only
// some Kinds (see Kind's own constants); the rest are left at their zero value and omitted
// from the wire by their own `omitempty` tag, so a compaction note's line doesn't carry a
// spurious empty ToolCalls array and so on.
type Record struct {
	ID   string `json:"id"`
	TS   string `json:"ts"`
	Kind Kind   `json:"kind"`

	// Content is the user/continuation text, the assistant's own reply text, the tool
	// result's text, or (Kind==KindSystemNote, Note==NoteCompaction) the compaction summary
	// message harness.Compact produced — INCLUDING its internal boundary marker, verbatim,
	// because Full below has to hand that string back to harness.BuildSendMessages unchanged
	// for it to still recognize the boundary.
	Content string `json:"content,omitempty"`
	// Reasoning is an assistant record's own chain-of-thought (harness.Message.Reasoning).
	// Kept in the store forever (decision 3's append-only rule) even though Full drops it
	// from every record but the newest before handing messages back to the engine.
	Reasoning string `json:"reasoning,omitempty"`
	// ToolCalls is an assistant record's requested calls, reused verbatim from harness's own
	// type rather than a second copy of the same three fields.
	ToolCalls []harness.ToolCall `json:"toolCalls,omitempty"`
	// ToolCallID pairs a KindToolResult record with the ToolCalls entry (on some earlier
	// assistant record) it answers.
	ToolCallID string `json:"toolCallId,omitempty"`
	// Note classifies a KindSystemNote record.
	Note Note `json:"note,omitempty"`
	// Model is the new model id (Note==NoteModelChange only).
	Model string `json:"model,omitempty"`
	// Usage is set on a KindUsage record (a pointer, not a value, so an all-zero
	// harness.Usage that was never actually reported — types.go's own "Usage's zero value
	// means the engine reported none" rule — round-trips as visibly present, not
	// indistinguishable from an absent one).
	Usage *harness.Usage `json:"usage,omitempty"`
}

// Store is one lcpp session's JSONL log at
// <paths.AgentDataDir()>/lcpp/sessions/<sid>.jsonl. The zero value is not usable; build one
// with Open. Safe for concurrent use: mu serializes every write so two goroutines appending
// at once (harness's own runToolCalls dispatches a turn's tool calls concurrently) can never
// interleave two partial JSON lines in the file.
type Store struct {
	dir string
	sid string
	mu  sync.Mutex
}

// sessionsDir is paths.AgentDataDir()'s lcpp subtree, resolved fresh on every Open (never
// cached) — paths.HomeDir reads $HOME per call, and tests redirect it with t.Setenv.
func sessionsDir() string {
	return filepath.Join(paths.AgentDataDir(), "lcpp", "sessions")
}

// Open returns the store for sid. It touches nothing on disk — the file, and its parent
// directories, are created lazily by the first Append* call (an lcpp session that never
// sends a turn leaves no empty file behind).
func Open(sid string) *Store {
	return &Store{dir: sessionsDir(), sid: sid}
}

// SID is the session id this store was opened for.
func (s *Store) SID() string { return s.sid }

// Path is the backing JSONL file, for a caller that needs to say where a session's history
// lives (diagnostics, TranscriptData.Path-style fields).
func (s *Store) Path() string { return filepath.Join(s.dir, s.sid+".jsonl") }

// recordIDSeq disambiguates two records minted within the same nanosecond — realistic here
// because a turn's several tool results are appended back to back with no engine round trip
// between them.
var recordIDSeq uint64

// newRecordID mints decision 3's "書き手が発番する安定 id" (fork/mirror-anchor
// identifier for one record): time-ordered, unique within this process, and never reused —
// forking copies existing IDs into the new file verbatim (ForkAt), so this never has to
// avoid colliding with a value it did not itself mint.
func newRecordID() string {
	return fmt.Sprintf("%d.%d", time.Now().UnixNano(), atomic.AddUint64(&recordIDSeq, 1))
}

// append is every Append* method's shared tail: stamp ID/TS, marshal, and write ONE line in
// a single os.File.Write call while holding mu — never a read-modify-write of the whole file
// ([[fstore-no-read-modify-write]] is exactly the trap this avoids), and never open with
// O_TRUNC, so a concurrent reader (the mirror poll, or Records below) never observes a
// truncated file, only a prefix of complete lines.
func (s *Store) append(rec Record) (Record, error) {
	rec.ID = newRecordID()
	rec.TS = time.Now().UTC().Format(time.RFC3339Nano)
	line, err := json.Marshal(rec)
	if err != nil {
		return Record{}, err
	}
	line = append(line, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return Record{}, err
	}
	f, err := os.OpenFile(s.Path(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return Record{}, err
	}
	defer f.Close()
	if _, err := f.Write(line); err != nil {
		return Record{}, err
	}
	return rec, nil
}

// AppendUser appends a genuine, top-level user turn — the operator's or the console user's
// own words, never something the tool loop synthesized. This is the ONE call a driver makes
// for real input, which is what makes the negative control in store_test.go hold: a user who
// happens to type the exact wording of harness's own continuation prompt still goes through
// here, still comes back Kind==KindUser, and is never hidden from the mirror. Compare
// AppendMessage, the call a driver makes instead for anything harness.Run itself produced.
func (s *Store) AppendUser(content string) (Record, error) {
	return s.append(Record{Kind: KindUser, Content: content})
}

// AppendMessage persists one harness.Message out of a harness.Run call's Result.Messages —
// the shape a driver replays record-by-record once a Run call returns. It is never the right
// call for the turn's own new input (use AppendUser for that): Run never invents a genuine
// user message on its own, so every RoleUser entry a driver sees appearing THROUGH a Run
// result is, by construction, the synthetic turn harness.IsContinuationPrompt identifies
// (loop.go's continuationPrompt) — never a second real user turn, because Run has no
// mechanism to add one. That is the "レコード側に印" decision 3's acceptance criteria ask
// for: the mark is which method the driver called, decided from where the message came from
// (Run's own output vs. the driver's own new input), not from sniffing the text at read time
// — content alone cannot tell the two apart (a real user can, if vanishingly unlikely, type
// the identical sentence), and store_test.go's negative control exercises exactly that case
// through AppendUser to show it is unaffected.
//
// A RoleSystem message is folded in as a compaction system-note (Note==NoteCompaction): the
// only RoleSystem message harness.Compact ever adds to a message slice IS the compaction
// boundary (compact.go), so no further classification is needed here. A model-change note
// has no harness.Message shape at all (it is a driver-level settings change, not something
// the engine ever sees) — call AppendModelChangeNote for that instead.
func (s *Store) AppendMessage(m harness.Message) (Record, error) {
	switch m.Role {
	case harness.RoleUser:
		k := KindUser
		if harness.IsContinuationPrompt(m) {
			k = KindContinuation
		}
		return s.append(Record{Kind: k, Content: m.Content})
	case harness.RoleAssistant:
		return s.append(Record{Kind: KindAssistant, Content: m.Content, Reasoning: m.Reasoning, ToolCalls: m.ToolCalls})
	case harness.RoleTool:
		return s.append(Record{Kind: KindToolResult, Content: m.Content, ToolCallID: m.ToolCallID})
	case harness.RoleSystem:
		return s.append(Record{Kind: KindSystemNote, Note: NoteCompaction, Content: m.Content})
	default:
		return Record{}, fmt.Errorf("lcpp: AppendMessage: unknown role %q", m.Role)
	}
}

// AppendModelChangeNote records a driver-level model switch (UpdateSettings, docs/log/99
// §4.5) — bookkeeping only. Full excludes it from what gets sent to the engine; it exists
// for the record and for a future mirror rendering (none is wired in this phase — see Full's
// own doc comment).
func (s *Store) AppendModelChangeNote(model string) (Record, error) {
	return s.append(Record{Kind: KindSystemNote, Note: NoteModelChange, Model: model})
}

// AppendUsage records one turn's exact token accounting (decision 8: this kind never
// estimates). u is copied so the caller's own variable can't alias the stored pointer.
func (s *Store) AppendUsage(u harness.Usage) (Record, error) {
	uu := u
	return s.append(Record{Kind: KindUsage, Usage: &uu})
}

// maxRecordLine bounds one JSONL line Records will accept — generous (a large tool result or
// a long reasoning trace can legitimately run to hundreds of KB) but finite, so a corrupted
// or torn line fails as a decode error rather than growing bufio.Scanner's buffer without
// limit.
const maxRecordLine = 32 * 1024 * 1024

// Records reads back every line of the session's log, in append order. A session with no
// file yet (Open was called but nothing was ever appended) returns (nil, nil), not an error.
func (s *Store) Records() ([]Record, error) {
	f, err := os.Open(s.Path())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), maxRecordLine)
	var out []Record
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil {
			return out, fmt.Errorf("lcpp: %s: %w", s.Path(), err)
		}
		out = append(out, rec)
	}
	if err := sc.Err(); err != nil {
		return out, err
	}
	return out, nil
}

// Full reconstructs decision 3's append-only "full" history as harness's own []Message
// shape — the same shape harness.Run/PrepareTurn/BuildSendMessages already operate on, so a
// caller need not (and must not) re-implement their compaction-boundary or reasoning-drop
// rules here; SendMessages below just calls straight through to BuildSendMessages once Full
// has done the record-to-Message translation.
//
// KindUser/KindContinuation both become a plain RoleUser message: the continuation marker
// only matters to the MIRROR (Transcript drops it there); the engine still needs it replayed
// exactly, because it is what keeps the request that follows it template-valid (loop.go).
// KindSystemNote is carried over only when Note==NoteCompaction — BuildSendMessages finds its
// boundary by matching content, and a NoteModelChange record has no place in the messages the
// engine is sent (docs/log/99 §4.5: a model switch is driver state, not conversation
// content). KindUsage never contributes a message at all.
func (s *Store) Full() ([]harness.Message, error) {
	recs, err := s.Records()
	if err != nil {
		return nil, err
	}
	return fullFromRecords(recs), nil
}

func fullFromRecords(recs []Record) []harness.Message {
	out := make([]harness.Message, 0, len(recs))
	for _, r := range recs {
		switch r.Kind {
		case KindUser, KindContinuation:
			out = append(out, harness.Message{Role: harness.RoleUser, Content: r.Content})
		case KindAssistant:
			out = append(out, harness.Message{
				Role: harness.RoleAssistant, Content: r.Content, Reasoning: r.Reasoning, ToolCalls: r.ToolCalls,
			})
		case KindToolResult:
			out = append(out, harness.Message{Role: harness.RoleTool, Content: r.Content, ToolCallID: r.ToolCallID})
		case KindSystemNote:
			if r.Note == NoteCompaction {
				out = append(out, harness.Message{Role: harness.RoleSystem, Content: r.Content})
			}
		case KindUsage:
			// Accounting only — never replayed to the engine.
		}
	}
	return out
}

// SendMessages is representation 1 (ADR 0093 decision 3's "正史"): the OpenAI messages slice
// ready for the next Client.Send/InputTokens call. It is Full plus harness.BuildSendMessages'
// existing rules — last-compaction-boundary onward, past reasoning stripped, one leading
// system message, as already proven live against the Qwen chat template (compact.go) — never
// a second implementation of those rules.
func (s *Store) SendMessages(systemPrompt string) ([]harness.Message, error) {
	full, err := s.Full()
	if err != nil {
		return nil, err
	}
	return harness.BuildSendMessages(systemPrompt, full), nil
}

// Transcript is representation 2: the mirror's []transcript.Turn, normalizing straight into
// the SAME type every other kind's parser produces (transcript.go's own doc comment — sharing
// it is what keeps changed-files/marks/sharing working for this kind for free). A
// KindContinuation record is never emitted as a Turn at all: it is not something the user
// said, and transcript.Turn has no "hide this from the composer history but keep it
// somewhere" slot, so dropping it entirely (rather than e.g. rendering it as a blank turn) is
// the only faithful choice. A KindSystemNote with Note==NoteModelChange is likewise not
// rendered — no Part/Turn shape has been designed for it yet (decision 3 only specifies the
// two representations this method and SendMessages produce, not every record kind's mirror
// treatment), and inventing one is wiring work for the kind itself, out of this package's
// scope.
func (s *Store) Transcript() ([]transcript.Turn, error) {
	recs, err := s.Records()
	if err != nil {
		return nil, err
	}
	return transcriptFromRecords(recs), nil
}

// toolCallSite is where an in-flight tool_calls entry landed, so a later KindToolResult
// record (a separate line, possibly several calls deep into the same turn) can fill in the
// Part's Output once it arrives.
type toolCallSite struct {
	turn, part int
}

func transcriptFromRecords(recs []Record) []transcript.Turn {
	var turns []transcript.Turn
	sites := make(map[string]toolCallSite)
	lastAssistant := -1

	for _, r := range recs {
		switch r.Kind {
		case KindContinuation:
			// Never the user's own words — see this method's own doc comment.
		case KindUser:
			turns = append(turns, transcript.Turn{
				Role: "user", TS: r.TS, AnchorID: r.ID, Idx: len(turns),
				Parts: []transcript.Part{{Kind: "text", Text: r.Content}},
				Text:  r.Content,
			})
		case KindAssistant:
			t := transcript.Turn{Role: "assistant", TS: r.TS, AnchorID: r.ID, Idx: len(turns), Text: r.Content}
			if strings.TrimSpace(r.Reasoning) != "" {
				t.Parts = append(t.Parts, transcript.Part{Kind: "thinking", Text: r.Reasoning})
			}
			if strings.TrimSpace(r.Content) != "" {
				t.Parts = append(t.Parts, transcript.Part{Kind: "text", Text: r.Content})
			}
			for _, tc := range r.ToolCalls {
				t.Parts = append(t.Parts, transcript.Part{Kind: "tool", Tool: tc.Name, Info: transcript.Clip(tc.Arguments)})
				if tc.ID != "" {
					sites[tc.ID] = toolCallSite{turn: len(turns), part: len(t.Parts) - 1}
				}
			}
			turns = append(turns, t)
			lastAssistant = len(turns) - 1
		case KindToolResult:
			if site, ok := sites[r.ToolCallID]; ok {
				turns[site.turn].Parts[site.part].Output = transcript.CapOutput(r.Content)
				delete(sites, r.ToolCallID) // a ToolCallID answers exactly one call
			}
		case KindSystemNote:
			if r.Note == NoteCompaction {
				// Same convention claude's own compaction summary uses (transcript.go's
				// Turn.Compact doc comment): a collapsible "context compacted" block, not an
				// ordinary user prompt.
				turns = append(turns, transcript.Turn{
					Role: "user", Compact: true, TS: r.TS, AnchorID: r.ID, Idx: len(turns),
					Parts: []transcript.Part{{Kind: "text", Text: r.Content}},
					Text:  r.Content,
				})
			}
		case KindUsage:
			if lastAssistant >= 0 && r.Usage != nil {
				turns[lastAssistant].InTok = r.Usage.PromptTokens
				turns[lastAssistant].OutTok = r.Usage.CompletionTokens
			}
		}
	}
	return turns
}

// ForkAt implements decision 3's fork/fork-at: copy this session's records up to and
// including anchorID, verbatim (same IDs — a fork keeps its own history's anchors valid, the
// same claude-style contract agents.go:243's ForkAtResolver already documents), into a brand
// new session newSID. It refuses to touch newSID if a file already exists there (O_EXCL): a
// fork always names an as-yet-unused sid, and silently overwriting an existing session's
// history would be exactly the kind of surprise decision 3's append-only rule exists to rule
// out for the ORIGINAL session — ForkAt must not become a backdoor around it for a second one.
func (s *Store) ForkAt(newSID, anchorID string) (*Store, error) {
	recs, err := s.Records()
	if err != nil {
		return nil, err
	}
	idx := -1
	for i, r := range recs {
		if r.ID == anchorID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, fmt.Errorf("lcpp: fork-at: anchor %q not found in session %q", anchorID, s.sid)
	}

	dst := &Store{dir: s.dir, sid: newSID}
	if err := os.MkdirAll(dst.dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(dst.Path(), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, r := range recs[:idx+1] {
		if err := enc.Encode(r); err != nil {
			return nil, err
		}
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		return nil, err
	}
	return dst, nil
}
