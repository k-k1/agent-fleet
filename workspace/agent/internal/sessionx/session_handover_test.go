package sessionx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

func hoUser(text string) transcript.Turn {
	return transcript.Turn{Role: "user", Text: text, Parts: []transcript.Part{{Kind: "text", Text: text}}}
}

func hoAsst(parts ...transcript.Part) transcript.Turn {
	return transcript.Turn{Role: "assistant", Model: "m-1", Parts: parts}
}

func hoText(s string) transcript.Part { return transcript.Part{Kind: "text", Text: s} }

func hoMeta() session.Meta {
	return session.Meta{Name: "src", Kind: session.KindClaude, Model: "opus", Dir: "/w/repo", Branch: "feat/x", Title: "Fix the thing"}
}

func hoBuild(t *testing.T, turns []transcript.Turn, mut func(*HandoverInput)) Handover {
	t.Helper()
	in := HandoverInput{Source: hoMeta(), Turns: turns, Now: time.Date(2026, 10, 16, 9, 30, 0, 0, time.UTC), Loc: time.UTC}
	if mut != nil {
		mut(&in)
	}
	h, err := BuildHandover(in)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// hoOldWork is a first turn whose tool calls fall outside the recent window once three more
// user turns follow.
func hoOldWork() []transcript.Turn {
	return []transcript.Turn{
		hoUser("start the work"),
		hoAsst(
			transcript.Part{Kind: "thinking", Text: "secret scratch"},
			hoText("on it"),
			transcript.Part{Kind: "tool", Tool: "Read", Info: "a.go", Output: "READ-OUT"},
			transcript.Part{Kind: "tool", Tool: "Bash", Info: "go test ./...", Output: "BASH-OUT"},
			transcript.Part{Kind: "tool", Tool: "Edit", File: "a.go", Info: "a.go", Output: "EDIT-OUT",
				Edits: []transcript.Edit{{Old: "OLDLINE", New: "NEWLINE"}}},
			transcript.Part{Kind: "tool", Tool: "mcp__x__unknown", Info: "do it", Output: "MCP-OUT"},
			transcript.Part{Kind: "error", Info: "APIError 500", Text: "ERRBODY"},
			transcript.Part{Kind: "delegation", Tool: "Agent", Info: "scan", Prompt: "DELEG-PROMPT", Output: "DELEG-RESULT"},
			transcript.Part{Kind: "mystery", Text: "MYSTERY"},
		),
	}
}

func hoRecentTail() []transcript.Turn {
	return []transcript.Turn{
		hoUser("second"), hoAsst(hoText("r2")),
		hoUser("third"), hoAsst(hoText("r3")),
		hoUser("fourth"), hoAsst(transcript.Part{Kind: "tool", Tool: "Read", Info: "b.go", Output: "RECENT-READ"},
			transcript.Part{Kind: "tool", Tool: "Bash", Info: "ls", Output: "RECENT-BASH"}),
	}
}

func partsOf(h Handover, turn int) []transcript.Part { return h.Turns[turn].Parts }

func findTool(h Handover, tool, info string) (transcript.Part, bool) {
	for _, t := range h.Turns {
		for _, p := range t.Parts {
			if p.Kind == "tool" && p.Tool == tool && p.Info == info {
				return p, true
			}
		}
	}
	return transcript.Part{}, false
}

func TestHandoverFilterOutsideRecentTurns(t *testing.T) {
	h := hoBuild(t, append(hoOldWork(), hoRecentTail()...), nil)

	var kinds []string
	for _, p := range partsOf(h, 1) {
		kinds = append(kinds, p.Kind+":"+p.Tool)
	}
	got := strings.Join(kinds, ",")
	// thinking and the read-only Read are gone; the rest stays in order.
	if want := "text:,tool:Bash,tool:Edit,tool:mcp__x__unknown,error:,delegation:Agent,mystery:"; got != want {
		t.Fatalf("parts of the old turn = %s, want %s", got, want)
	}
	bash, _ := findTool(h, "Bash", "go test ./...")
	if bash.Output != "" || bash.Info != "go test ./..." {
		t.Errorf("a non-read-only call outside the window keeps its line and loses its output: %+v", bash)
	}
	edit, _ := findTool(h, "Edit", "a.go")
	if edit.Output != "" || len(edit.Edits) != 0 || edit.File != "a.go" {
		t.Errorf("an edit keeps verb and file only: %+v", edit)
	}
	if mcp, _ := findTool(h, "mcp__x__unknown", "do it"); mcp.Output != "" {
		t.Errorf("an unlisted tool is kept as a line: %+v", mcp)
	}
	if e := partsOf(h, 1)[4]; e.Info != "APIError 500" || e.Text != "" {
		t.Errorf("an error outside the window is one line: %+v", e)
	}
	d := partsOf(h, 1)[5]
	if d.Prompt != "DELEG-PROMPT" || d.Output != "DELEG-RESULT" {
		t.Errorf("a delegation keeps instruction and result: %+v", d)
	}
	if h.Turns[1].Model != "" {
		t.Errorf("model badge kept: %q", h.Turns[1].Model)
	}
}

func TestHandoverRecentTurnsKeepEveryCall(t *testing.T) {
	h := hoBuild(t, append(hoOldWork(), hoRecentTail()...), nil)
	read, ok := findTool(h, "Read", "b.go")
	if !ok || read.Output != "RECENT-READ" {
		t.Errorf("a read-only call in the last 3 turns is kept with its output: %+v ok=%v", read, ok)
	}
	if b, _ := findTool(h, "Bash", "ls"); b.Output != "RECENT-BASH" {
		t.Errorf("tool output in the last 3 turns is kept: %+v", b)
	}
	if _, ok := findTool(h, "Read", "a.go"); ok {
		t.Error("the old read-only call survived")
	}
	// 4 user turns, window = 3 → the window starts at "second".
	if got := turnText(h.Turns[h.RecentFrom]); got != "second" {
		t.Errorf("RecentFrom points at %q, want the second user turn", got)
	}
}

func TestHandoverDropsRowsThatAreNotTheConversation(t *testing.T) {
	turns := []transcript.Turn{
		hoUser("real instruction"),
		{Role: "assistant", Sidechain: true, Parts: []transcript.Part{hoText("SIDECHAIN")}},
		{Role: "assistant", CostOnly: true, CostUSD: 1},
		hoAsst(transcript.Part{Kind: "thinking", Text: "only thinking"}),
		hoAsst(hoText("answer")),
	}
	h := hoBuild(t, turns, nil)
	if len(h.Turns) != 2 || turnText(h.Turns[1]) != "answer" {
		t.Fatalf("turns = %+v", h.Turns)
	}
}

func TestHandoverAgentPromptsAreDroppedByText(t *testing.T) {
	enResume := rateLimitResumePromptFor("en")
	jaResume := rateLimitResumePromptFor("ja")
	turns := []transcript.Turn{
		hoUser("do the real work"),
		hoAsst(hoText("working")),
		hoUser(enResume),
		hoAsst(hoText("a")),
		hoUser(jaResume),
		hoAsst(hoText("b")),
		hoUser(abortResumePromptFor("en")),
		hoUser(abortResumePromptFor("ja")),
		// A user's own schedule is tagged like the Agent's resume, but its text is theirs.
		{Role: "user", Source: "schedule", Text: "nightly report please", Parts: []transcript.Part{hoText("nightly report please")}},
		// Unknown origin, plausible words: kept.
		hoUser("continue (auto-resume) but check the tests first"),
	}
	h := hoBuild(t, turns, nil)
	var users []string
	for _, tr := range h.Turns {
		if tr.Role == "user" {
			users = append(users, turnText(tr))
		}
	}
	want := []string{"do the real work", "nightly report please", "continue (auto-resume) but check the tests first"}
	if strings.Join(users, "|") != strings.Join(want, "|") {
		t.Fatalf("user turns = %q, want %q", users, want)
	}
	if h.Stopped.LastUser != want[2] {
		t.Errorf("last user message = %q", h.Stopped.LastUser)
	}
}

func TestHandoverLastUserSkipsAgentPrompt(t *testing.T) {
	h := hoBuild(t, []transcript.Turn{hoUser("the real ask"), hoAsst(hoText("did half")), hoUser(rateLimitResumePromptFor("en")), hoAsst(hoText("stopped here"))}, nil)
	if h.Stopped.LastUser != "the real ask" || h.Stopped.LastAssistant != "stopped here" {
		t.Errorf("stopped = %+v", h.Stopped)
	}
}

func TestHandoverUnknownKindKeepsAllCalls(t *testing.T) {
	turns := append(hoOldWork(), hoRecentTail()...)
	h := hoBuild(t, turns, func(in *HandoverInput) { in.Source.Kind = "futurekind" })
	if _, ok := findTool(h, "Read", "a.go"); !ok {
		t.Error("with no read-only list for the kind, no call may be dropped")
	}
}

func TestHandoverReadOnlyListsPerKind(t *testing.T) {
	for kind, names := range map[string][]string{
		session.KindClaude: {"Read"}, session.KindCodex: {"view_image"}, session.KindOpencode: {"read"},
		session.KindCursor: {"Read"}, session.KindKiro: {"fsRead"}, session.KindAgy: {"VIEW_FILE"},
		session.KindCopilot: {"view"}, session.KindLcpp: {"read"},
	} {
		for _, n := range names {
			turns := []transcript.Turn{hoUser("a"), hoAsst(transcript.Part{Kind: "tool", Tool: n, Info: "x", Output: "o"})}
			turns = append(turns, hoUser("b"), hoUser("c"), hoUser("d"))
			h := hoBuild(t, turns, func(in *HandoverInput) { in.Source.Kind = kind })
			if _, ok := findTool(h, n, "x"); ok {
				t.Errorf("%s: %s should be dropped outside the window", kind, n)
			}
		}
	}
	if len(readOnlyTools[session.KindClaude]) == 0 || len(readOnlyTools) != 9 {
		t.Errorf("every transcript kind needs an entry: %v", readOnlyTools)
	}
}

func TestHandoverNoTurnsIsRefused(t *testing.T) {
	for _, turns := range [][]transcript.Turn{nil, {{Role: "assistant", Sidechain: true}, {Role: "assistant", CostOnly: true}}} {
		if _, err := BuildHandover(HandoverInput{Source: hoMeta(), Turns: turns}); err != ErrHandoverNoTurns {
			t.Errorf("err = %v", err)
		}
	}
}

func TestHandoverWhereItStopped(t *testing.T) {
	q := transcript.Question{Question: "Which db?", Options: []transcript.Option{{Label: "pg"}, {Label: "sqlite"}}}
	h := hoBuild(t, []transcript.Turn{hoUser("pick a db"), hoAsst(hoText("thinking about it"))}, func(in *HandoverInput) {
		in.Pending = []transcript.Question{q}
		in.CutOffByLimit = true
		in.Queued = []string{"queued-A", "queued-B"}
		in.QueuedItems = []agents.QueueItem{{ID: "1", Text: "queued-A", Origin: agents.Origin{Kind: "peer", From: "boss"}}}
		in.PeerMessages = []agents.PendingPeer{{From: "boss", Intent: "question", Message: "PEER-BODY"}}
	})
	s := h.Stopped
	if !s.CutOff || len(s.Question) != 1 || s.Question[0].Question != "Which db?" {
		t.Errorf("stopped = %+v", s)
	}
	if len(s.Queued) != 2 || s.Queued[0].Origin != "peer from boss" || s.Queued[1].Text != "queued-B" {
		t.Errorf("queued = %+v (the same text from Queued and QueuedItems must appear once)", s.Queued)
	}
	if len(s.Peers) != 1 || s.Peers[0].From != "boss" || s.Peers[0].Intent != "question" || s.Peers[0].Body != "PEER-BODY" {
		t.Errorf("peers = %+v", s.Peers)
	}
	md, _ := RenderHandover(h, "en")
	for _, w := range []string{"cut off by the agent's usage limit", "Which db?", "queued-B", "PEER-BODY", "pick a db", "thinking about it"} {
		if !strings.Contains(md, w) {
			t.Errorf("markdown lacks %q", w)
		}
	}
}

func TestHandoverMarkdownKeepsWhatToolsNamesWouldDrop(t *testing.T) {
	turns := []transcript.Turn{
		hoUser("start"),
		{Role: "user", Compact: true, Text: "COMPACTION-SUMMARY"},
		hoAsst(
			transcript.Part{Kind: "delegation", Tool: "Agent", Info: "scan", Prompt: "DELEG-PROMPT", Output: "DELEG-RESULT"},
			transcript.Part{Kind: "question", Tool: "AskUserQuestion", Questions: []transcript.Question{{
				Question: "Which layout?", Options: []transcript.Option{{Label: "A", Preview: "PREVIEW-BOX"}, {Label: "B"}}}}, Answer: "A"},
			transcript.Part{Kind: "plan", Tool: "ExitPlanMode", Plan: "PLAN-BODY"},
			transcript.Part{Kind: "userfile", Tool: "SendUserFile", Files: []string{"out/report.png"}, Caption: "the chart"},
		),
	}
	h := hoBuild(t, turns, nil)
	md, _ := RenderHandover(h, "en")
	for _, w := range []string{"COMPACTION-SUMMARY", "DELEG-PROMPT", "DELEG-RESULT", "PREVIEW-BOX", "PLAN-BODY", "out/report.png", "the chart", "Which layout?"} {
		if !strings.Contains(md, w) {
			t.Errorf("markdown lacks %q", w)
		}
	}
	// The order the target reads: header, then the state, then the history.
	last := -1
	for _, sec := range []string{"# Handover: Fix the thing", "# Where it stopped", "# Working copy", "# Changed files", "# Open threads", "# Conversation"} {
		i := strings.Index(md, sec)
		if i <= last {
			t.Errorf("section %q out of order or missing (at %d, previous %d)", sec, i, last)
		}
		last = i
	}
	for _, w := range []string{"- Moved: 2026-10-16 09:30 UTC", "- Agent: claude opus", "- Working folder: /w/repo", "- Branch: feat/x"} {
		if !strings.Contains(md, w) {
			t.Errorf("header lacks %q", w)
		}
	}
}

func TestHandoverSectionsAndHeader(t *testing.T) {
	h := hoBuild(t, []transcript.Turn{hoUser("go"), hoAsst(hoText("ok"))}, func(in *HandoverInput) {
		in.Git = HandoverGit{Available: true, Status: " M a.go", DiffStat: " a.go | 2 +-", Ahead: "abc123 wip"}
		in.ChangedFiles = []transcript.FileTouch{{Path: "repos/r/a.go", Verb: "edit", Added: 3, Removed: 1, Count: 2}}
		in.Children = []HandoverChild{{Name: "kid", State: "working"}}
		in.Proposals = []string{"handoff-1"}
		in.Earlier = []string{"handover-first.md"}
	})
	md, _ := RenderHandover(h, "en")
	for _, w := range []string{" M a.go", "a.go | 2 +-", "abc123 wip", "`repos/r/a.go` — edit (+3/-1), 2 edits", "child session `kid`: working",
		"pending handoff proposal: `handoff-1`", "- Earlier history (same folder as this file): handover-first.md",
		"do not open the source's own session files"} {
		if !strings.Contains(md, w) {
			t.Errorf("markdown lacks %q", w)
		}
	}
	if strings.Contains(md, "transcript path") {
		t.Error("the header must not carry a CLI store path")
	}
}

func TestHandoverScrubsSecrets(t *testing.T) {
	tok := "ghp_" + strings.Repeat("A1b2C3d4", 5)
	h := hoBuild(t, []transcript.Turn{hoUser("use " + tok), hoAsst(hoText("ok"))}, func(in *HandoverInput) {
		in.Git = HandoverGit{Available: true, Status: "M " + tok}
		in.PeerMessages = []agents.PendingPeer{{From: "p", Intent: "i", Message: tok}}
		in.Queued = []string{tok}
	})
	md, _ := RenderHandover(h, "en")
	if strings.Contains(md, tok) {
		t.Error("a token reached the file")
	}
}

func TestFirstParagraph(t *testing.T) {
	for _, c := range []struct {
		in, want string
		ok       bool
	}{
		{"one\n\ntwo", "one", true},
		{"only one paragraph", "", false},
		{"intro\n```go\na\n\nb\n```\nafter fence\n\nlast", "intro\n```go\na\n\nb\n```\nafter fence", true},
		{"```\nnever closed\n\nstill inside", "", false},
		{"one\n\n\n", "", false},
	} {
		got, ok := firstParagraph(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("firstParagraph(%q) = %q,%v want %q,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func longText(tag string, n int) string {
	var b strings.Builder
	b.WriteString(tag + " first paragraph.\n\n")
	for b.Len() < n {
		b.WriteString(tag + " filler filler filler filler filler filler filler filler.\n\n")
	}
	return b.String()
}

func TestHandoverSizeCutsOldestAssistantTextsFirst(t *testing.T) {
	var turns []transcript.Turn
	for i := 0; i < 12; i++ {
		tag := string(rune('A' + i))
		turns = append(turns, hoUser("ask "+tag), hoAsst(hoText(longText("ANS-"+tag, 40<<10))))
	}
	h := hoBuild(t, turns, nil)
	md, cut := RenderHandover(h, "en")
	if len(md) > handoverSizeTarget {
		t.Fatalf("size %d over the target %d", len(md), handoverSizeTarget)
	}
	if cut.Cut == 0 || !strings.Contains(md, handoverCutMarker) {
		t.Fatalf("nothing was cut or the marker is missing (cut=%d)", cut.Cut)
	}
	// Oldest first: the first answer is cut, the newest three turns are whole.
	if strings.Count(md, "ANS-A filler") != 0 || !strings.Contains(md, "ANS-A first paragraph.") {
		t.Error("the oldest answer should be down to its first paragraph")
	}
	for _, tag := range []string{"J", "K", "L"} {
		if !strings.Contains(md, "ANS-"+tag+" filler") {
			t.Errorf("recent answer %s was cut", tag)
		}
	}
	for i := 0; i < 12; i++ {
		if !strings.Contains(md, "ask "+string(rune('A'+i))) {
			t.Errorf("user message %d was cut", i)
		}
	}
	// The cut stops once the target is met: a later, un-needed answer stays whole.
	if cut.Cut >= 8 {
		t.Errorf("cut %d texts; it should stop at the target", cut.Cut)
	}
	// The caller's value is not modified.
	if strings.Contains(turnText(h.Turns[1]), "cut for size") {
		t.Error("RenderHandover modified its argument")
	}
}

func TestHandoverSizeNeverCutsWhatItMustKeep(t *testing.T) {
	big := strings.Repeat("user words\n\n", 30<<10) // > 256 KiB of user text alone
	turns := []transcript.Turn{hoUser(big), hoAsst(hoText(longText("OLD", 4<<10))), hoUser("b"), hoAsst(hoText(longText("RECENT", 4<<10)))}
	h := hoBuild(t, turns, nil)
	md, cut := RenderHandover(h, "en")
	if len(md) <= handoverSizeTarget {
		t.Fatalf("test setup: %d bytes should exceed the target", len(md))
	}
	if cut.Cut != 0 || strings.Contains(md, handoverCutMarker) {
		t.Errorf("everything is inside the last 3 turns; nothing may be cut (cut=%d)", cut.Cut)
	}
	if !strings.Contains(md, big[:1000]) || !strings.Contains(md, "RECENT filler") {
		t.Error("kept content missing")
	}
}

func TestIsAbortResumePrompt(t *testing.T) {
	for _, p := range []string{abortResumePromptFor("en"), " " + abortResumePromptFor("ja") + "\n"} {
		if !isAbortResumePrompt(p) {
			t.Errorf("not recognised: %q", p)
		}
	}
	if isAbortResumePrompt("continue") || isAbortResumePrompt("continue (auto-resume) now") {
		t.Error("a near miss was taken for the nudge")
	}
}

func TestWriteHandoverFileAndChain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// B was itself a move target: its directory holds A's record. Moving B → C copies it.
	if _, err := WriteHandoverFile("b", "a", "A-RECORD"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.SessionWorkDir("b"), "notes.txt"), []byte("scratch"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := ListHandoverFiles("b"); err != nil || len(got) != 1 || got[0] != "handover-a.md" {
		t.Fatalf("list = %v", got)
	}
	copied, err := CopyHandoverChain("b", "c")
	if err != nil || len(copied) != 1 || copied[0] != "handover-a.md" {
		t.Fatalf("copied = %v, %v", copied, err)
	}
	path, err := WriteHandoverFile("c", "b", "B-RECORD")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".af-work", "c", "handover-b.md"); path != want {
		t.Errorf("path = %s, want %s", path, want)
	}
	// Deleting A and B's directories takes nothing C needs.
	_ = os.RemoveAll(paths.SessionWorkDir("a"))
	_ = os.RemoveAll(paths.SessionWorkDir("b"))
	for n, want := range map[string]string{"handover-a.md": "A-RECORD", "handover-b.md": "B-RECORD"} {
		b, err := os.ReadFile(filepath.Join(paths.SessionWorkDir("c"), n))
		if err != nil || string(b) != want {
			t.Errorf("%s = %q, %v", n, b, err)
		}
	}
	if got, err := ListHandoverFiles("c"); err != nil || len(got) != 2 {
		t.Errorf("c's files = %v, %v", got, err)
	}
	// No temp file is left behind.
	ents, _ := os.ReadDir(paths.SessionWorkDir("c"))
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover %s", e.Name())
		}
	}
}

func TestWriteHandoverFileRejectsBadNames(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, c := range [][2]string{{"", "a"}, {"..", "a"}, {"t", "../x"}, {"t", ""}, {"a/b", "s"}} {
		if p, err := WriteHandoverFile(c[0], c[1], "x"); err == nil {
			t.Errorf("WriteHandoverFile(%q,%q) wrote %s", c[0], c[1], p)
		}
	}
}

func TestWriteHandoverFileReplacesInPlace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := WriteHandoverFile("t", "s", "first"); err != nil {
		t.Fatal(err)
	}
	p, err := WriteHandoverFile("t", "s", "second")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "second" {
		t.Errorf("content = %q", b)
	}
}

func TestHandoverKeepsATurnThatIsMoreThanTheNudge(t *testing.T) {
	nudge := rateLimitResumePromptFor("en")
	for name, tr := range map[string]transcript.Turn{
		"with a file": {Role: "user", Parts: []transcript.Part{hoText(nudge),
			{Kind: "userfile", Tool: "SendUserFile", Files: []string{"image.png"}, Caption: "check this"}}},
		"with an unknown part": {Role: "user", Parts: []transcript.Part{hoText(nudge), {Kind: "mystery", Text: "M"}}},
		"explicit peer":        {Role: "user", Source: transcript.SourcePeer, PeerFrom: "boss", Text: nudge, Parts: []transcript.Part{hoText(nudge)}},
		"peer without a name":  {Role: "user", Source: transcript.SourcePeer, Text: nudge, Parts: []transcript.Part{hoText(nudge)}},
		"operator":             {Role: "user", Source: "operator", Text: nudge, Parts: []transcript.Part{hoText(nudge)}},
	} {
		h := hoBuild(t, []transcript.Turn{hoUser("real"), tr, hoAsst(hoText("a"))}, nil)
		if len(h.Turns) != 3 {
			t.Errorf("%s: the turn was dropped: %+v", name, h.Turns)
		}
	}
	// The Agent's own nudge, tagged as a schedule or not at all, still goes.
	for _, src := range []string{"", TurnSourceSchedule, TurnSourceAutoResume} {
		h := hoBuild(t, []transcript.Turn{hoUser("real"), {Role: "user", Source: src, Text: nudge, Parts: []transcript.Part{hoText(nudge)}}, hoAsst(hoText("a"))}, nil)
		if len(h.Turns) != 2 {
			t.Errorf("source %q: nudge kept", src)
		}
	}
}

func TestHandoverSizeCutKeepsFencesBalancedAcrossParts(t *testing.T) {
	var turns []transcript.Turn
	// The old answer's fence spans two text parts; cutting either half would break it.
	turns = append(turns, hoUser("ask"), hoAsst(hoText("```go\nalpha"), hoText("beta\n\ngamma\n```\n\nend")))
	for i := 0; i < 6; i++ {
		turns = append(turns, hoUser("u"+string(rune('a'+i))), hoAsst(hoText(longText("FILL", 50<<10))))
	}
	h := hoBuild(t, turns, nil)
	md, cut := RenderHandover(h, "en")
	if cut.Cut == 0 {
		t.Fatal("setup: nothing was cut")
	}
	if !strings.Contains(md, "```go\nalpha") || !strings.Contains(md, "beta\n\ngamma\n```") {
		t.Error("a fence spanning two parts of one answer was cut")
	}
	if strings.Count(md, "```")%2 != 0 {
		t.Error("unbalanced fences in the output")
	}
}

func TestListHandoverFilesReportsAReadFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := WriteHandoverFile("b", "a", "A"); err != nil {
		t.Fatal(err)
	}
	dir := paths.SessionWorkDir("b")
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 directory")
	}
	if _, err := ListHandoverFiles("b"); err == nil {
		t.Error("an unreadable directory listed as empty")
	}
	if copied, err := CopyHandoverChain("b", "c"); err == nil {
		t.Errorf("the move would succeed without the chain: %v", copied)
	}
	if got, err := ListHandoverFiles("never-existed"); err != nil || got != nil {
		t.Errorf("a missing directory is an empty history: %v, %v", got, err)
	}
}

func TestHandoverFilesRefuseASymlinkedSessionDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(home, "repos", "r")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".af-work"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(repo, paths.SessionWorkDir("t")); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteHandoverFile("t", "s", "x"); err == nil {
		t.Error("wrote through a symlinked target directory")
	}
	if ents, _ := os.ReadDir(repo); len(ents) != 0 {
		t.Errorf("the working copy got files: %v", ents)
	}
	if err := os.WriteFile(filepath.Join(repo, "handover-z.md"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(repo, paths.SessionWorkDir("src")); err != nil {
		t.Fatal(err)
	}
	if got, err := ListHandoverFiles("src"); err == nil {
		t.Errorf("listed through a symlinked source directory: %v", got)
	}
	if _, err := CopyHandoverChain("src", "t2"); err == nil {
		t.Error("copied from a symlinked source directory")
	}
}

func TestHandoverSizeCutSkipsFencesInsideLists(t *testing.T) {
	listed := "- ```go\n  alpha\n\n  beta\n  ```\n- ```go\n  gamma\n  ```"
	turns := []transcript.Turn{hoUser("ask"), hoAsst(hoText(listed))}
	for i := 0; i < 6; i++ {
		turns = append(turns, hoUser("u"+string(rune('a'+i))), hoAsst(hoText(longText("FILL", 50<<10))))
	}
	md, cut := RenderHandover(hoBuild(t, turns, nil), "en")
	if cut.Cut == 0 {
		t.Fatal("setup: nothing was cut")
	}
	if !strings.Contains(md, listed) {
		t.Error("a text with fences inside a list was cut")
	}
}

func hoDiscards() []agents.Discard {
	return []agents.Discard{
		{ID: "d1", At: "2026-10-16T09:00:00Z", Reason: agents.DiscardSecondStop, Items: []agents.QueueItem{
			{ID: "a", Text: "DISCARDED-A\nsecond line", Origin: agents.Origin{Kind: "peer", From: "boss"}, Attachments: []string{"/w/shot.png"}},
			{ID: "b", Text: "DISCARDED-B"},
		}},
		{ID: "d2", At: "2026-10-16T09:05:00Z", Reason: agents.DiscardFirstStop},
		{ID: "d3", At: "2026-10-16T09:06:00Z", Reason: "from_the_future", Items: []agents.QueueItem{{ID: "c", Text: "DISCARDED-C"}}},
	}
}

func TestHandoverDiscardsOnTheValue(t *testing.T) {
	h := hoBuild(t, []transcript.Turn{hoUser("go"), hoAsst(hoText("ok"))}, func(in *HandoverInput) {
		in.Discards = hoDiscards()
	})
	d := h.Stopped.Discarded
	if len(d) != 2 {
		t.Fatalf("discarded = %+v (a discard with no item says nothing and is left out)", d)
	}
	if d[0].Reason != agents.DiscardSecondStop || d[0].At != "2026-10-16T09:00:00Z" || len(d[0].Items) != 2 {
		t.Errorf("first discard = %+v", d[0])
	}
	if it := d[0].Items[0]; it.Text != "DISCARDED-A\nsecond line" || it.Origin != "peer from boss" || len(it.Attachments) != 1 {
		t.Errorf("an item keeps its text verbatim, origin and attachments: %+v", it)
	}
	if len(h.Stopped.Queued) != 0 {
		t.Errorf("a discarded input must not read as a queued one: %+v", h.Stopped.Queued)
	}
}

func TestHandoverDiscardsRenderedInBothLanguages(t *testing.T) {
	tok := "ghp_" + strings.Repeat("A1b2C3d4", 5)
	h := hoBuild(t, []transcript.Turn{hoUser("go"), hoAsst(hoText("ok"))}, func(in *HandoverInput) {
		in.Discards = append(hoDiscards(), agents.Discard{ID: "d4", At: "2026-10-16T09:07:00Z",
			Reason: agents.DiscardQueue, Items: []agents.QueueItem{{ID: "s", Text: "use " + tok}}})
	})
	for _, c := range []struct {
		lang, head string
		reasons    []string
	}{
		{"en", "Inputs a stop threw away", []string{"second stop, 2026-10-16T09:00:00Z", "from_the_future", "queue discarded"}},
		{"ja", "停止で破棄された入力", []string{"2 回目の停止, 2026-10-16T09:00:00Z", "from_the_future", "キューの破棄"}},
	} {
		md, _ := RenderHandover(h, c.lang)
		stopped := md[strings.Index(md, "\n# "):]
		for _, w := range append([]string{c.head, "DISCARDED-A\nsecond line", "DISCARDED-B", "DISCARDED-C", "(peer from boss)", "`/w/shot.png`"}, c.reasons...) {
			if !strings.Contains(stopped, w) {
				t.Errorf("%s: markdown lacks %q:\n%s", c.lang, w, md)
			}
		}
		if strings.Contains(md, tok) {
			t.Errorf("%s: a token in a discarded input reached the file", c.lang)
		}
		// They sit under "where it stopped", before the working copy.
		if i, j := strings.Index(md, "DISCARDED-A"), strings.Index(md, "\n# "+handoverLabelsFor(c.lang).WorkingCopy); i < 0 || j < 0 || i > j {
			t.Errorf("%s: discards at %d, working copy at %d", c.lang, i, j)
		}
	}
}

// The scaffolding is written by renderHandover, not taken from the conversation: every
// heading and fixed label follows the language, and none of the other language's survives.
func TestHandoverScaffoldingFollowsTheLanguage(t *testing.T) {
	h := hoBuild(t, []transcript.Turn{hoUser("go"), hoAsst(hoText("ok"))}, func(in *HandoverInput) {
		in.CutOffByLimit = true
		in.Queued = []string{"q"}
		in.Discards = hoDiscards()
		in.PeerMessages = []agents.PendingPeer{{From: "boss", Intent: "question", Message: "PEER"}}
		in.Git = HandoverGit{Available: true}
		in.ChangedFiles = []transcript.FileTouch{{Path: "a.go", Verb: "edit", Count: 2, Sidechain: true}}
		in.Children = []HandoverChild{{Name: "kid", State: "working"}}
		in.Proposals = []string{"p1"}
		in.Earlier = []string{"handover-first.md"}
		in.Instructions = []string{"CLAUDE.md"}
	})
	en, _ := RenderHandover(h, "en")
	ja, _ := RenderHandover(h, "ja")
	for _, w := range []string{"# Handover: Fix the thing", "- Source session: src", "# Where it stopped", "# Working copy", "# Changed files",
		"# Open threads", "# Conversation", "**Last user message:**", "**Instructions queued but never delivered:**",
		"**Peer messages waiting for the source:**", "- from boss (intent: question)", "**Commits ahead of upstream:**", "(none)",
		"2 edits, by a subagent only", "child session `kid`", "- Moved: 2026-10-16 09:30 UTC"} {
		if !strings.Contains(en, w) {
			t.Errorf("en lacks %q", w)
		}
	}
	for _, w := range []string{"# 引き継ぎ: Fix the thing", "- 元のセッション: src", "# 中断した時点", "# 作業コピー", "# 変更されたファイル",
		"# 未完了のスレッド", "# 会話", "**最後のユーザー発言:**", "**キューに入ったまま届かなかった指示:**",
		"**元のセッション宛てに待っているピアのメッセージ:**", "- 送信元 boss（意図: question）", "**upstream より先のコミット:**", "（なし）",
		"2 回編集、サブエージェントのみ", "子セッション `kid`", "- 移行日時: 2026-10-16 09:30 UTC"} {
		if !strings.Contains(ja, w) {
			t.Errorf("ja lacks %q", w)
		}
	}
	for _, w := range []string{"Where it stopped", "Working copy", "Changed files", "Open threads", "Last user message", "queued but never delivered", "Peer messages", "Source session", "usage limit"} {
		if strings.Contains(ja, w) {
			t.Errorf("ja still carries the English scaffolding %q", w)
		}
	}
	for _, w := range []string{"中断した時点", "作業コピー", "未完了のスレッド", "最後のユーザー発言", "元のセッション", "利用上限"} {
		if strings.Contains(en, w) {
			t.Errorf("en carries Japanese scaffolding %q", w)
		}
	}
	// Both stay the same file in every other respect: the conversation text is untouched.
	for _, md := range []string{en, ja} {
		for _, w := range []string{"DISCARDED-A", "PEER", "\ngo\n", "ok"} {
			if !strings.Contains(md, w) {
				t.Errorf("markdown lacks the verbatim %q", w)
			}
		}
	}
}

// setLocale saves the Console display language where the Agent reads it (uiprefs.Locale).
func setLocale(t *testing.T, locale string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(uiprefs.Path()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := uiprefs.WriteAtomic([]byte(`{"locale":"` + locale + `"}`)); err != nil {
		t.Fatal(err)
	}
}

// An empty lang is the Agent's own known display language, the one the Markdown export uses.
func TestRenderHandoverDefaultsToTheAgentsLocale(t *testing.T) {
	withTempHome(t)
	h := hoBuild(t, []transcript.Turn{hoUser("go"), hoAsst(hoText("ok"))}, nil)
	if md, _ := RenderHandover(h, ""); !strings.Contains(md, "# 中断した時点") {
		t.Errorf("no locale saved must read as Japanese, the default:\n%s", md)
	}
	setLocale(t, "en")
	if md, _ := RenderHandover(h, ""); !strings.Contains(md, "# Where it stopped") {
		t.Errorf("locale en must read as English:\n%s", md)
	}
}

// The move's snapshot hands the kind's discards on: dropping them there would leave the
// builder's input empty while its own tests stay green.
func TestReadMoveSourceCarriesDiscards(t *testing.T) {
	withTempHome(t)
	prev := agentRegistry[session.KindLcpp]
	agentRegistry[session.KindLcpp] = discardingAgent{Agent: prev}
	t.Cleanup(func() { agentRegistry[session.KindLcpp] = prev })
	in := readMoveSource(session.Meta{Name: "mvdisc", Kind: session.KindLcpp, Dir: t.TempDir()})
	if len(in.Discards) != 3 || in.Discards[0].Items[0].Text != "DISCARDED-A\nsecond line" {
		t.Fatalf("snapshot discards = %+v", in.Discards)
	}
}

type discardingAgent struct{ agents.Agent }

func (discardingAgent) Transcript(session.Meta) (agents.TranscriptData, bool) {
	return agents.TranscriptData{Turns: []transcript.Turn{hoUser("go"), hoAsst(hoText("ok"))}, Discards: hoDiscards()}, true
}
