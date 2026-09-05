package sessionx

// The injecting side of the self-report fast path (docs/log/51 §self-report fast path /
// Phase 3).
//
// The reconciler infers completion from "mechanical idle". However much evidence it
// gathers, an inference stays an inference: only the session itself can measure semantic
// completion (the instruction was actually carried out), so instructions that carry a
// reporting duty get one extra line — "call af_report once when you are done".
//
// This is not backbone (ADR 0035 decision 5). Forgetting the call, or calling it early,
// leaves overall correctness unchanged; the reconciler picks it up at settle. All the
// report buys is shrinking a two-tick confirmation to one tick. So this line is allowed to
// fail, and it is not added for a kind that never gets the tool.
//
// Only the session name is passed; the server generates the report body (fact-only).
// Letting the model write the body would needlessly widen the prompt-injection surface
// reached through reports.

import (
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// selfReportToolAvailable reports whether the session's CLI actually gets the af MCP
// server (mcpreg's builtin "af" — every kind that gets materialized). shell / ssm have no
// config file to write and no such tool, so they are excluded here rather than being told
// to call a tool that does not exist.
func selfReportToolAvailable(kind string) bool {
	k := NormalizeKind(kind)
	for _, m := range mcpreg.MaterializedKinds {
		if m == k {
			return true
		}
	}
	return false
}

// selfReportHintLine is the sentence appended to an instruction prompt. It is bilingual
// for the reason docs/log/30 gives: pouring Japanese-only text into a session that is
// working in English drags its later output language along (there is no way to read a
// session's language). The explicit "keep your output language unchanged" is the
// insurance against that.
func selfReportHintLine(name string) string {
	return "[agent-fleet] この指示をやり切って作業が残っていない時点で、MCP ツール af_report(session=\"" + name +
		"\") を1回だけ呼んでください（質問や承認で止まる場合・作業が続く場合は呼ばないこと）。" +
		"この注記自体への返答は不要で、回答の言語も変えないでください。 / " +
		"When this instruction is fully done, call the MCP tool af_report(session=\"" + name +
		"\") exactly once. Do not call it if you are stopping to ask, or if work remains. " +
		"No reply to this note is needed; keep your output language unchanged."
}

// withSelfReportHint appends the fast-path line to an instruction prompt for the
// sessions that can act on it. Only injection paths that carry report_to call it, i.e.
// instructions with a reporting duty. Input the user typed in the Console gets no line —
// there is nowhere to report to.
func withSelfReportHint(prompt string, m session.Meta) string {
	if strings.TrimSpace(prompt) == "" || !selfReportToolAvailable(m.Kind) {
		return prompt
	}
	return prompt + "\n\n" + selfReportHintLine(m.Name)
}
