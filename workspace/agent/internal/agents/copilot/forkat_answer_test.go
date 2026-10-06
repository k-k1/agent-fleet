package copilot

// The verdict of TestContractLiveCopilotForkAt's final question, kept outside the contract
// build tag so it is unit-tested on every run while the contract build still compiles it.

import (
	"regexp"
	"strings"
	"testing"
)

type forkAnswer int

const (
	// forkAnswerCarried: the branch named MARMOT and not QUILL — it restored exactly the
	// truncated history.
	forkAnswerCarried forkAnswer = iota
	// forkAnswerLeaked: QUILL appears, so the branch knows the turn we cut away. This wins
	// over MARMOT: "MARMOT, later changed to QUILL" still proves the leak.
	forkAnswerLeaked
	// forkAnswerFormatEcho: the reply is only the earlier prompts' "Reply exactly: OK". That
	// is model nondeterminism, not evidence about the restore, so it may be asked once more.
	forkAnswerFormatEcho
	// forkAnswerUnanswered: anything else — the branch could not answer from its history.
	forkAnswerUnanswered
)

var (
	marmotWord = regexp.MustCompile(`(?i)\bMARMOT\b`)
	quillWord  = regexp.MustCompile(`(?i)\bQUILL\b`)
)

// classifyForkAnswer reads `copilot -p` output. Only the first paragraph is the model's
// reply; copilot prints a stats footer (Changes / AI Credits / Tokens / Resume) after a
// blank line, so the echo test must not look at the whole output. The codewords match as
// whole words only: "MARMOTS" proves no restore and "QUILLMAX" proves no leak.
func classifyForkAnswer(out string) forkAnswer {
	switch {
	case quillWord.MatchString(out):
		return forkAnswerLeaked
	case marmotWord.MatchString(out):
		return forkAnswerCarried
	}
	out = strings.ReplaceAll(out, "\r\n", "\n")
	reply, _, _ := strings.Cut(strings.TrimSpace(out), "\n\n")
	reply = strings.TrimRight(strings.TrimSpace(reply), ".!")
	if strings.EqualFold(reply, "OK") {
		return forkAnswerFormatEcho
	}
	return forkAnswerUnanswered
}

func TestClassifyForkAnswer(t *testing.T) {
	// The footer copied from a real failing run (copilot 1.0.91).
	const footer = "\n\n\nChanges    +0 -0\nAI Credits 0.38 (11s)\n" +
		"Tokens     ↑ 24.3k (7.0k cached) • ↓ 134 (64 reasoning)\n" +
		"Resume     copilot --resume=e08da94f-fc07-4004-bd86-d8f05cf08e76\n"
	cases := []struct {
		name, out string
		want      forkAnswer
	}{
		{"marmot", "MARMOT" + footer, forkAnswerCarried},
		{"marmot lower case in a sentence", "The codeword is marmot." + footer, forkAnswerCarried},
		{"quill", "QUILL" + footer, forkAnswerLeaked},
		{"both words: quill means the cut turn leaked", "It was MARMOT, then QUILL." + footer, forkAnswerLeaked},
		{"bare OK", "OK" + footer, forkAnswerFormatEcho},
		{"OK with a period", "OK." + footer, forkAnswerFormatEcho},
		{"ok lower case, no footer", "  ok\n", forkAnswerFormatEcho},
		{"OK followed by more text", "OK, but I don't know the codeword." + footer, forkAnswerUnanswered},
		{"other text", "I don't know." + footer, forkAnswerUnanswered},
		{"empty", "", forkAnswerUnanswered},
		{"footer only", strings.TrimLeft(footer, "\n"), forkAnswerUnanswered},
		{"marmot in markdown", "**MARMOT**" + footer, forkAnswerCarried},
		{"marmot quoted", "> \"Marmot\"" + footer, forkAnswerCarried},
		{"marmot in a code fence", "```\nMARMOT\n```" + footer, forkAnswerCarried},
		{"MARMOTS is not MARMOT", "MARMOTS" + footer, forkAnswerUnanswered},
		{"QUILLMAX is not QUILL", "QUILLMAX" + footer, forkAnswerUnanswered},
		{"QUILLMAX does not hide MARMOT", "MARMOT, not QUILLMAX" + footer, forkAnswerCarried},
		{"OK with CRLF footer", "OK" + strings.ReplaceAll(footer, "\n", "\r\n"), forkAnswerFormatEcho},
		{"OK after leading blank lines", "\n\nOK" + footer, forkAnswerFormatEcho},
		{"OK after leading CRLF blank lines", "\r\n\r\nOK\r\n\r\nChanges    +0 -0\r\n", forkAnswerFormatEcho},
	}
	for _, c := range cases {
		if got := classifyForkAnswer(c.out); got != c.want {
			t.Errorf("%s: classifyForkAnswer = %d, want %d", c.name, got, c.want)
		}
	}
}
