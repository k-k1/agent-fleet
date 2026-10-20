// Package memreview is the engine of the background memory review (ADR 0117): the redacted input
// the helper model is given, the contract its reply must meet, and (in later phases) the run
// state and the loop that drive them. It proposes; AF's store is the only writer and nothing in
// this package writes to it.
package memreview

// The caps below were measured in phase P0 (docs/log/134-background-review-p0-measurement.md)
// and confirmed by the owner on 2026-10-18; the index and body sizes are P1 assumptions to be
// revisited from real traffic. Lengths are in characters (runes) unless the name says Bytes.
const (
	// WindowChars bounds the conversation part of one helper call. A longer window is split at a
	// turn boundary and reviewed oldest-first, never cut to its newest part.
	WindowChars = 40000
	// ToolResultChars clips each tool result inside the window.
	ToolResultChars = 500
	// IndexChars bounds the memory index lines of the input.
	IndexChars = 12000
	// BodyChars bounds each existing memory body of the input; MaxBodies how many are sent.
	BodyChars = 2000
	MaxBodies = 5

	// MaxOps, OpTextBytes and ReplyBytes bound the helper's reply; a larger one is dropped whole.
	MaxOps      = 5
	OpTextBytes = 6 << 10
	ReplyBytes  = 24 << 10
)
