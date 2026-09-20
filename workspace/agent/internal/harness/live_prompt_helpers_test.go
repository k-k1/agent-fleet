package harness

import "os"

// liveParallelEnabled reports whether TestManualLiveAgenticSession's system prompt and task-0
// should instruct the model to issue independent tool calls in the same turn. Default true
// (unchanged historical behavior — every prior live run used this wording); set
// AF_LCPP_LIVE_PARALLEL=0 to drop the instruction, isolating whether a family can do the
// underlying agentic work at all from whether its tool-call format supports concurrent calls in
// one turn. ADR 0093's own open question 1 does not require parallel calls — a family whose
// template has no syntax for more than one call per turn (confirmed live for Llama 3.1: llama.cpp
// logged "unparsed peg-native output" containing several JSON objects joined by `;`, the model's
// own improvised attempt at a multi-call turn) can still pass segment G's acceptance bar without
// it.
func liveParallelEnabled() bool {
	return os.Getenv("AF_LCPP_LIVE_PARALLEL") != "0"
}

// liveAgenticSystemPrompt builds TestManualLiveAgenticSession's system prompt for a project at
// cwd. When parallel is true the returned string is byte-for-byte the historical prompt (so
// existing live-run comparisons stay valid); when false, the "issue those tool calls in the SAME
// turn" sentence is dropped entirely and the surrounding sentences are left untouched.
func liveAgenticSystemPrompt(cwd string, parallel bool) string {
	if parallel {
		return "You are a careful coding agent working in a real Go project at " + cwd + ". " +
			"You have read/write/edit/glob/grep/ls/bash tools and a todo_write tool. " +
			"Use todo_write to track multi-step work. When a step lets you look at several " +
			"independent things at once (e.g. reading multiple files), issue those tool calls " +
			"in the SAME turn rather than one at a time. Always verify your work by actually " +
			"running `go build ./...` and `go test ./...` with the bash tool before declaring " +
			"something fixed — do not just claim success without having run it."
	}
	return "You are a careful coding agent working in a real Go project at " + cwd + ". " +
		"You have read/write/edit/glob/grep/ls/bash tools and a todo_write tool. " +
		"Use todo_write to track multi-step work. Always verify your work by actually " +
		"running `go build ./...` and `go test ./...` with the bash tool before declaring " +
		"something fixed — do not just claim success without having run it."
}

// liveAgenticTaskZero builds TestManualLiveAgenticSession's first task prompt. When parallel is
// true the returned string is byte-for-byte the historical prompt; when false, the "(in parallel
// — one tool call per file, all in the same turn)" aside is dropped and the sentence reworded to
// stay natural, without changing what work the task asks for.
func liveAgenticTaskZero(parallel bool) string {
	if parallel {
		return "This Go project currently fails `go build ./...` and, once it builds, has failing " +
			"tests under `go test ./...` because of real bugs. Start by reading every .go file " +
			"under this directory (in parallel — one tool call per file, all in the same turn) " +
			"to see the whole project before changing anything. Then find and fix every bug so " +
			"that both `go build ./...` and `go test ./...` succeed. Use todo_write to track " +
			"each bug as you find and fix it."
	}
	return "This Go project currently fails `go build ./...` and, once it builds, has failing " +
		"tests under `go test ./...` because of real bugs. Start by reading every .go file " +
		"under this directory to see the whole project before changing anything. Then find and " +
		"fix every bug so that both `go build ./...` and `go test ./...` succeed. Use " +
		"todo_write to track each bug as you find and fix it."
}
