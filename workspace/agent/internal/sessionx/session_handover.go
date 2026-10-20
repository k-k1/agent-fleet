package sessionx

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/agy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/copilot"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/cursor"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/kiro"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/lcpp"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/muse"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/bridge"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

// The cross-agent move's handover file (ADR 0115 decisions 6–8). The builder is I/O-free: the
// caller reads the transcript, queues, spool, git state and children and passes them in, so
// every rule below is testable on values.

const (
	// handoverSizeTarget is the soft size, in UTF-8 bytes of the final Markdown, past which
	// the oldest assistant texts are cut. A starting value, to be revised after the first
	// live moves (ADR 0115 decision 7).
	handoverSizeTarget = 256 << 10
	// handoverRecentTurns is how many of the newest user turns keep every tool call with its
	// output, and are never cut for size. Same status as handoverSizeTarget.
	handoverRecentTurns = 3
)

// ErrHandoverNoTurns means the source's read yielded no conversation. A move refuses it
// instead of writing an empty file that the target would trust as the whole record.
var ErrHandoverNoTurns = errors.New("the source session has no readable conversation")

// readOnlyTools is each kind's list of tool names that only read, kept beside that kind's
// parser. A kind (or a name) that is absent keeps every call.
var readOnlyTools = map[string][]string{
	session.KindClaude:   claude.ReadOnlyTools,
	session.KindCodex:    codex.ReadOnlyTools,
	session.KindOpencode: opencode.ReadOnlyTools,
	session.KindCursor:   cursor.ReadOnlyTools,
	session.KindKiro:     kiro.ReadOnlyTools,
	session.KindAgy:      agy.ReadOnlyTools,
	session.KindCopilot:  copilot.ReadOnlyTools,
	session.KindLcpp:     lcpp.ReadOnlyTools,
	session.KindMuse:     muse.ReadOnlyTools,
}

// HandoverInput is everything the file is built from.
type HandoverInput struct {
	Source session.Meta
	// Turns is the whole conversation as exportTurns reads it. Pending is the question the
	// source still awaits (TranscriptData.Pending), appended to the turns when the kind has
	// not left it there already.
	Turns   []transcript.Turn
	Pending []transcript.Question
	// Queued and QueuedItems are the instructions that never reached the transcript.
	Queued      []string
	QueuedItems []agents.QueueItem
	// Discards are the inputs a stop threw away and the driver kept for return (ADR 0105
	// decision 4): like the queue they never reached the transcript.
	Discards []agents.Discard
	// PeerMessages is the source's pending peer spool (agents.PendingPeers).
	PeerMessages []agents.PendingPeer
	// CutOffByLimit is true when the last turn was cut off by the usage limit.
	CutOffByLimit bool
	Git           HandoverGit
	ChangedFiles  []transcript.FileTouch
	Children      []HandoverChild
	// Proposals names the source's pending handoff proposals.
	Proposals []string
	// Earlier lists the handover files the source itself owns (HandoverChain), by file name.
	Earlier []string
	// Instructions are the project instruction files the source's kind read and the target's
	// kind does not, relative to the working folder (decision 14).
	Instructions []string
	Now          time.Time
	Loc          *time.Location
}

// HandoverGit is the working copy at the moment of the move. Available is false when Dir is
// not a git working tree or git could not be read.
type HandoverGit struct {
	Available bool
	Status    string // git status --porcelain
	DiffStat  string // git diff --stat
	Ahead     string // commits ahead of the upstream, one per line
}

// HandoverChild is a child session the source started.
type HandoverChild struct{ Name, State string }

// Handover is the in-memory form the file is rendered from: decision 7 is already applied to
// Turns (dropped parts removed, Output/Edits cleared where only the call is kept).
type Handover struct {
	Title   string
	Header  HandoverHeader
	Stopped HandoverStopped
	Git     HandoverGit
	Changed []transcript.FileTouch
	Threads HandoverThreads
	Turns   []transcript.Turn
	// RecentFrom is the index in Turns of the first of the last handoverRecentTurns user
	// turns; everything from there on is exempt from the size cut.
	RecentFrom int
	// Cut counts the assistant texts the size rule shortened.
	Cut int
}

type HandoverHeader struct {
	Source, Kind, Model, Dir, Branch string
	MovedAt                          time.Time
	Loc                              *time.Location
	Earlier                          []string
	Instructions                     []string
}

type HandoverStopped struct {
	LastUser      string
	LastAssistant string
	Question      []transcript.Question
	Queued        []HandoverQueued
	Discarded     []HandoverDiscard
	Peers         []HandoverPeer
	CutOff        bool
}

type HandoverQueued struct {
	Text, Origin string
	Attachments  []string
}

// HandoverDiscard is one discarding stop: why, when (RFC 3339 as the driver recorded it) and
// the inputs it threw away.
type HandoverDiscard struct {
	Reason, At string
	Items      []HandoverQueued
}

type HandoverPeer struct{ From, Intent, Body string }

type HandoverThreads struct {
	Children  []HandoverChild
	Proposals []string
}

// BuildHandover assembles the value and applies decision 7's filter to it.
func BuildHandover(in HandoverInput) (Handover, error) {
	turns := withPendingQuestions(in.Turns, in.Pending)
	if !slices.ContainsFunc(turns, func(t transcript.Turn) bool { return !t.Sidechain && !t.CostOnly }) {
		return Handover{}, ErrHandoverNoTurns
	}
	kept, recentFrom := filterHandoverTurns(in.Source.Kind, turns, handoverRecentTurns)
	h := Handover{
		Title: exportTitle(in.Source),
		Header: HandoverHeader{
			Source: in.Source.Name, Kind: in.Source.Kind, Model: in.Source.Model,
			Dir: in.Source.Dir, Branch: in.Source.Branch,
			MovedAt: in.Now, Loc: in.Loc, Earlier: slices.Clone(in.Earlier),
			Instructions: slices.Clone(in.Instructions),
		},
		Git:        in.Git,
		Changed:    in.ChangedFiles,
		Threads:    HandoverThreads{Children: in.Children, Proposals: in.Proposals},
		Turns:      kept,
		RecentFrom: recentFrom,
	}
	h.Stopped = handoverStopped(in, kept)
	return h, nil
}

func handoverStopped(in HandoverInput, kept []transcript.Turn) HandoverStopped {
	s := HandoverStopped{CutOff: in.CutOffByLimit}
	for i := len(kept) - 1; i >= 0 && (s.LastUser == "" || s.LastAssistant == ""); i-- {
		t := kept[i]
		switch {
		case t.Compact:
		case t.Role == "user" && s.LastUser == "":
			s.LastUser = turnText(t)
		case t.Role == "assistant" && s.LastAssistant == "":
			s.LastAssistant = lastTextPart(t)
		}
	}
	if n := len(kept); n > 0 && kept[n-1].Role == "assistant" {
		for _, p := range kept[n-1].Parts {
			if p.Kind == "question" && p.Answer == "" && !p.Declined {
				s.Question = append(s.Question, p.Questions...)
			}
		}
	}
	seen := map[string]bool{}
	for _, q := range in.QueuedItems {
		seen[q.Text] = true
		s.Queued = append(s.Queued, handoverQueuedOf(q))
	}
	for _, q := range in.Queued {
		if !seen[q] {
			s.Queued = append(s.Queued, HandoverQueued{Text: q})
		}
	}
	for _, d := range in.Discards {
		hd := HandoverDiscard{Reason: d.Reason, At: d.At}
		for _, q := range d.Items {
			hd.Items = append(hd.Items, handoverQueuedOf(q))
		}
		if len(hd.Items) > 0 {
			s.Discarded = append(s.Discarded, hd)
		}
	}
	for _, p := range in.PeerMessages {
		s.Peers = append(s.Peers, HandoverPeer{From: p.From, Intent: p.Intent, Body: p.Message})
	}
	return s
}

func handoverQueuedOf(q agents.QueueItem) HandoverQueued {
	return HandoverQueued{Text: q.Text, Origin: originLabel(q.Origin), Attachments: q.Attachments}
}

func originLabel(o agents.Origin) string {
	if o.From != "" {
		return o.Kind + " from " + o.From
	}
	return o.Kind
}

// turnText is a turn's own words: its text parts, or Text when the parser left no parts.
func turnText(t transcript.Turn) string {
	var ss []string
	for _, p := range t.Parts {
		if p.Kind == "text" && strings.TrimSpace(p.Text) != "" {
			ss = append(ss, strings.TrimSpace(p.Text))
		}
	}
	if len(ss) == 0 {
		return strings.TrimSpace(t.Text)
	}
	return strings.Join(ss, "\n\n")
}

func lastTextPart(t transcript.Turn) string {
	for i := len(t.Parts) - 1; i >= 0; i-- {
		if p := t.Parts[i]; p.Kind == "text" && strings.TrimSpace(p.Text) != "" {
			return strings.TrimSpace(p.Text)
		}
	}
	return ""
}

// isPersonTurn is a user turn that carries something somebody said; the bare tool-result rows
// some parsers emit as user turns are not turns of the conversation.
func isPersonTurn(t transcript.Turn) bool {
	return t.Role == "user" && !t.Sidechain && !t.CostOnly && !t.Compact && (len(t.Parts) > 0 || strings.TrimSpace(t.Text) != "")
}

// isAgentPrompt reports whether a user turn is nothing but a prompt the Agent itself
// injected. The text decides, not Turn.Source: Source is "schedule" for the Agent's resume and
// for a user's own schedule alike, and the tag ledger forgets old bodies. But the whole turn
// must be that text: any other part (a file, a tool, an unknown kind) or an explicit origin
// that is not the Agent's (peer, operator, chat bridge, spawn) keeps the turn, since the data
// then cannot show that dropping it is safe.
func isAgentPrompt(t transcript.Turn) bool {
	if t.Role != "user" || t.Compact || t.PeerFrom != "" {
		return false
	}
	switch t.Source {
	case "", TurnSourceSchedule, TurnSourceAutoResume:
	default:
		return false
	}
	for _, p := range t.Parts {
		if p.Kind != "text" {
			return false
		}
	}
	s := turnText(t)
	return isRateLimitResumePrompt(s) || isAbortResumePrompt(s)
}

// filterHandoverTurns applies ADR 0115 decision 7 and returns the surviving turns and the
// index of the first recent one. It copies: the caller's turns are never modified.
func filterHandoverTurns(kind string, turns []transcript.Turn, recent int) ([]transcript.Turn, int) {
	// The recent window is counted in user turns that survive, so a resume nudge sitting
	// between two real instructions does not push one of them out.
	var userAt []int
	for i, t := range turns {
		if isPersonTurn(t) && !isAgentPrompt(t) {
			userAt = append(userAt, i)
		}
	}
	boundary := 0
	if len(userAt) > recent {
		boundary = userAt[len(userAt)-recent]
	}
	ro := readOnlyTools[kind]
	var out []transcript.Turn
	recentFrom := -1
	for i, t := range turns {
		if t.Sidechain || t.CostOnly || isAgentPrompt(t) {
			continue
		}
		inRecent := i >= boundary
		t = filterTurn(t, ro, inRecent)
		if t.Role == "assistant" && len(t.Parts) == 0 && t.Text == "" {
			continue
		}
		if inRecent && recentFrom < 0 {
			recentFrom = len(out)
		}
		out = append(out, t)
	}
	if recentFrom < 0 {
		recentFrom = len(out)
	}
	return out, recentFrom
}

func filterTurn(t transcript.Turn, readOnly []string, inRecent bool) transcript.Turn {
	t.Model = "" // model badges are dropped
	if len(t.Parts) == 0 {
		return t
	}
	parts := make([]transcript.Part, 0, len(t.Parts))
	for _, p := range t.Parts {
		switch p.Kind {
		case "thinking":
			continue
		case "tool":
			if !inRecent {
				if slices.Contains(readOnly, p.Tool) && p.File == "" && len(p.Edits) == 0 {
					continue
				}
				p.Output, p.Edits = "", nil
			}
		case "error":
			if !inRecent && p.Info != "" {
				p.Text = "" // the label is the line; the message body is the long part
			}
		}
		parts = append(parts, p)
	}
	if len(parts) == 0 {
		// Nothing of the turn survives; Text would make the renderer print the thinking's
		// sibling text again.
		t.Text = ""
	}
	t.Parts = parts
	return t
}

// --- rendering -----------------------------------------------------------------------

// RenderHandover lays the value out as the Markdown the target reads. When the result passes
// handoverSizeTarget it cuts the oldest assistant texts (outside the recent turns) to their
// first paragraph, one turn at a time, and returns the value as cut. User messages, the
// recent turns and the sections before the conversation are never cut, so the result can
// still exceed the target.
func RenderHandover(h Handover, lang string) (string, Handover) {
	if lang == "" {
		lang = uiprefs.Locale()
	}
	// One normalised language for both renderers: transcript.Markdown reads anything but "en"
	// as Japanese, while an unknown locale must read as English.
	if lang != "en" && lang != "ja" {
		lang = "en"
	}
	marker := handoverLabelsFor(lang).CutMarker
	md := renderHandover(h, lang)
	if len(md) <= handoverSizeTarget {
		return md, h
	}
	h.Turns = slices.Clone(h.Turns)
	// open is the code fence left open by the assistant texts before this one. A fence can
	// span the text parts of one answer and the rows of one reply, so a text is cut only when
	// it neither starts nor ends inside a fence: cutting one half would swallow the rest of
	// the conversation into a code block.
	open := ""
	for i := 0; i < h.RecentFrom && len(md) > handoverSizeTarget; i++ {
		t := h.Turns[i]
		if t.Role != "assistant" || t.Compact {
			open = ""
			continue
		}
		parts := slices.Clone(t.Parts)
		cut := 0
		for j, p := range parts {
			if p.Kind != "text" {
				continue
			}
			before := open
			open = fenceAfter(open, strings.TrimSpace(p.Text))
			if before != "" || open != "" || hasUnparsedFence(p.Text) {
				continue
			}
			if short, ok := firstParagraph(p.Text); ok {
				parts[j].Text = short + "\n\n" + marker
				cut++
			}
		}
		if cut == 0 {
			continue
		}
		t.Parts = parts
		h.Turns[i] = t
		h.Cut += cut
		md = renderHandover(h, lang)
	}
	return md, h
}

// fenceStep advances the code-fence state over one line: open is the fence string currently
// open ("" = none).
func fenceStep(open, line string) string {
	tl := strings.TrimSpace(line)
	if open != "" {
		if strings.HasPrefix(tl, open) && strings.Trim(tl, open[:1]) == "" {
			return ""
		}
		return open
	}
	if strings.HasPrefix(tl, "```") || strings.HasPrefix(tl, "~~~") {
		return strings.Repeat(tl[:1], len(tl)-len(strings.TrimLeft(tl, tl[:1])))
	}
	return ""
}

// fenceAfter is the fence state at the end of s, given the state before it.
func fenceAfter(open, s string) string {
	for _, l := range strings.Split(s, "\n") {
		open = fenceStep(open, l)
	}
	return open
}

// hasUnparsedFence reports a fence marker somewhere fenceStep does not look: inside a list
// item or a blockquote ("- ```go"), or inline. The state tracker would miscount it, so a text
// holding one is not cut at all; the size target is soft and a kept paragraph costs less than a
// broken block.
func hasUnparsedFence(s string) bool {
	for _, l := range strings.Split(s, "\n") {
		tl := strings.TrimSpace(l)
		if strings.HasPrefix(tl, "```") || strings.HasPrefix(tl, "~~~") {
			continue
		}
		if strings.Contains(tl, "```") || strings.Contains(tl, "~~~") {
			return true
		}
	}
	return false
}

// firstParagraph returns s up to its first blank line outside a code fence; ok is false when
// there is nothing after it to cut. A fence the paragraph opens is carried to its close, so
// the cut never leaves one open.
func firstParagraph(s string) (string, bool) {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	fence := ""
	for i, l := range lines {
		if fence == "" && strings.TrimSpace(l) == "" && i > 0 {
			rest := strings.TrimSpace(strings.Join(lines[i:], "\n"))
			return strings.TrimSpace(strings.Join(lines[:i], "\n")), rest != ""
		}
		fence = fenceStep(fence, l)
	}
	return "", false
}

func renderHandover(h Handover, lang string) string {
	sc := bridge.ScrubSecrets
	l := handoverLabelsFor(lang)
	var b strings.Builder
	hd := h.Header
	fmt.Fprintf(&b, "# "+l.Title+"\n\n", oneLineText(sc(h.Title)))
	bullet := func(label, v string) {
		if v != "" {
			fmt.Fprintf(&b, "- %s: %s\n", label, oneLineText(sc(v)))
		}
	}
	bullet(l.Source, hd.Source)
	bullet(l.Agent, strings.TrimSpace(hd.Kind+" "+hd.Model))
	bullet(l.Folder, hd.Dir)
	bullet(l.Branch, hd.Branch)
	if !hd.MovedAt.IsZero() {
		at := hd.MovedAt
		if hd.Loc != nil {
			at = at.In(hd.Loc)
		}
		bullet(l.Moved, at.Format("2006-01-02 15:04 MST"))
	}
	for _, f := range hd.Earlier {
		bullet(l.Earlier, f)
	}
	for _, f := range hd.Instructions {
		bullet(l.Instructions, f)
	}
	b.WriteString("\n" + l.Intro + "\n\n")

	st := h.Stopped
	b.WriteString("# " + l.Stopped + "\n\n")
	if st.CutOff {
		b.WriteString(l.CutOff + "\n\n")
	}
	if st.LastUser != "" {
		b.WriteString("**" + l.LastUser + "**\n\n" + transcript.Fenced("", sc(st.LastUser)))
	}
	if st.LastAssistant != "" {
		b.WriteString("**" + l.LastAssistant + "**\n\n" + transcript.Fenced("", sc(st.LastAssistant)))
	}
	for _, q := range st.Question {
		b.WriteString("**" + l.Question + "** " + oneLineText(sc(q.Question)) + "\n\n")
		for _, o := range q.Options {
			b.WriteString("- " + oneLineText(sc(o.Label)))
			if o.Description != "" {
				b.WriteString(" — " + oneLineText(sc(o.Description)))
			}
			b.WriteString("\n")
		}
		if len(q.Options) > 0 {
			b.WriteString("\n")
		}
	}
	queuedItems := func(items []HandoverQueued) {
		for _, q := range items {
			head := "- "
			if q.Origin != "" {
				head += "(" + oneLineText(sc(q.Origin)) + ") "
			}
			b.WriteString(head + "\n\n" + transcript.Fenced("", sc(q.Text)))
			for _, a := range q.Attachments {
				b.WriteString("  " + l.QueuedAttachment + ": `" + oneLineText(sc(a)) + "`\n")
			}
		}
	}
	if len(st.Queued) > 0 {
		b.WriteString("**" + l.Queued + "**\n\n")
		queuedItems(st.Queued)
	}
	if len(st.Discarded) > 0 {
		b.WriteString("**" + l.Discarded + "**\n\n")
		for _, d := range st.Discarded {
			reason := l.DiscardReasons[d.Reason]
			if reason == "" {
				reason = d.Reason
			}
			head := reason
			if d.At != "" {
				head += ", " + d.At
			}
			b.WriteString("_" + oneLineText(sc(head)) + "_\n\n")
			queuedItems(d.Items)
		}
	}
	if len(st.Peers) > 0 {
		b.WriteString("**" + l.Peers + "**\n\n")
		for _, p := range st.Peers {
			fmt.Fprintf(&b, "- "+l.PeerFrom+"\n\n%s", oneLineText(sc(p.From)), oneLineText(sc(p.Intent)),
				transcript.Fenced("", sc(p.Body)))
		}
	}

	b.WriteString("# " + l.WorkingCopy + "\n\n")
	if !h.Git.Available {
		b.WriteString(l.NotGit + "\n\n")
	} else {
		section := func(label, body string) {
			if strings.TrimSpace(body) == "" {
				body = l.None
			}
			b.WriteString("**" + label + ":**\n\n" + transcript.Fenced("", sc(body)))
		}
		section("git status --porcelain", h.Git.Status)
		section("git diff --stat", h.Git.DiffStat)
		section(l.CommitsAhead, h.Git.Ahead)
	}

	b.WriteString("# " + l.Changed + "\n\n")
	if len(h.Changed) == 0 {
		b.WriteString(l.NoneRecorded + "\n\n")
	}
	for _, f := range h.Changed {
		line := fmt.Sprintf("- `%s` — %s", oneLineText(sc(f.Path)), f.Verb)
		if f.Added > 0 || f.Removed > 0 {
			line += fmt.Sprintf(" (+%d/-%d)", f.Added, f.Removed)
		}
		if f.Count > 1 {
			line += fmt.Sprintf(l.EditsFmt, f.Count)
		}
		if f.Sidechain {
			line += l.SubagentOnly
		}
		b.WriteString(line + "\n")
	}
	if len(h.Changed) > 0 {
		b.WriteString("\n")
	}

	b.WriteString("# " + l.Threads + "\n\n")
	if len(h.Threads.Children) == 0 && len(h.Threads.Proposals) == 0 {
		b.WriteString(l.None + "\n\n")
	}
	for _, c := range h.Threads.Children {
		fmt.Fprintf(&b, l.ChildFmt, oneLineText(sc(c.Name)), oneLineText(sc(c.State)))
	}
	for _, p := range h.Threads.Proposals {
		fmt.Fprintf(&b, l.ProposalFmt, oneLineText(sc(p)))
	}
	if len(h.Threads.Children)+len(h.Threads.Proposals) > 0 {
		b.WriteString("\n" + l.ThreadsNote + "\n\n")
	}

	b.WriteString("# " + l.Conversation + "\n\n")
	// ToolsFull throughout: ToolsNames would also drop compaction summaries, delegation
	// prompts and results and option previews. What each tool call keeps was already decided
	// on the value.
	b.WriteString(transcript.Markdown(h.Turns, transcript.MarkdownOptions{
		Tools: transcript.ToolsFull,
		Lang:  lang,
		Loc:   hd.Loc,
		Scrub: sc,
	}))
	return b.String()
}

func oneLineText(s string) string { return strings.Join(strings.Fields(s), " ") }
