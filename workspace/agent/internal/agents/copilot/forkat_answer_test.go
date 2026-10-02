package copilot

// The verdict of TestContractLiveCopilotForkAt's final question, kept outside the contract
// build tag so it is unit-tested on every run while the contract build still compiles it.

import (
	"strings"
	"testing"
)

type forkAnswer int

const (
	// forkAnswerCarried: the branch named ALPHA and not BETA — it restored exactly the
	// truncated history.
	forkAnswerCarried forkAnswer = iota
	// forkAnswerLeaked: BETA appears, so the branch knows the turn we cut away. This wins
	// over ALPHA: "ALPHA, later changed to BETA" still proves the leak.
	forkAnswerLeaked
	// forkAnswerFormatEcho: the reply is only the earlier prompts' "Reply exactly: OK". That
	// is model nondeterminism, not evidence about the restore, so it may be asked once more.
	forkAnswerFormatEcho
	// forkAnswerUnanswered: anything else — the branch could not answer from its history.
	forkAnswerUnanswered
)

// classifyForkAnswer reads `copilot -p` output. Only the first paragraph is the model's
// reply; copilot prints a stats footer (Changes / AI Credits / Tokens / Resume) after a
// blank line, so the echo test must not look at the whole output.
func classifyForkAnswer(out string) forkAnswer {
	up := strings.ToUpper(out)
	switch {
	case strings.Contains(up, "BETA"):
		return forkAnswerLeaked
	case strings.Contains(up, "ALPHA"):
		return forkAnswerCarried
	}
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
		{"alpha", "ALPHA" + footer, forkAnswerCarried},
		{"alpha lower case in a sentence", "The codeword is alpha." + footer, forkAnswerCarried},
		{"beta", "BETA" + footer, forkAnswerLeaked},
		{"both words: beta means the cut turn leaked", "It was ALPHA, then BETA." + footer, forkAnswerLeaked},
		{"bare OK", "OK" + footer, forkAnswerFormatEcho},
		{"OK with a period", "OK." + footer, forkAnswerFormatEcho},
		{"ok lower case, no footer", "  ok\n", forkAnswerFormatEcho},
		{"OK followed by more text", "OK, but I don't know the codeword." + footer, forkAnswerUnanswered},
		{"other text", "I don't know." + footer, forkAnswerUnanswered},
		{"empty", "", forkAnswerUnanswered},
	}
	for _, c := range cases {
		if got := classifyForkAnswer(c.out); got != c.want {
			t.Errorf("%s: classifyForkAnswer = %d, want %d", c.name, got, c.want)
		}
	}
}
