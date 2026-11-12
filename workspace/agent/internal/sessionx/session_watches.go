package sessionx

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/watch"
)

// watchRowWire is one live watch as the mirror shows it. It is a slim projection of
// watch.Listed on purpose: the Subscription behind it carries delivery bookkeeping (Told,
// Pending, the Control Plane wake booking) that the Console has no use for and must not see.
// Ref and Error are member-, session- or ext-source-written text; the Console renders them as
// text only.
type watchRowWire struct {
	ID     string   `json:"id"`
	Source string   `json:"source"`
	Ref    string   `json:"ref"`
	Types  []string `json:"types"`
	URL    string   `json:"url,omitempty"`
	// Due is when a time watch fires; Expires is when any watch ends on its own (both RFC 3339).
	Due     string `json:"due,omitempty"`
	Expires string `json:"expires,omitempty"`
	// By is the child session that asked for the watch with notify parent; empty otherwise.
	By string `json:"by,omitempty"`
	// Error is the subject's last read error: the provider is not answering.
	Error string `json:"error,omitempty"`
}

// watchRowsWire lists name's live watches for the transcript poll. It reads the engine's
// memory only (no provider call) and returns nil when there are none, so a session without
// watches adds nothing to the answer. A notify-parent watch is owned by the parent, so it
// lists here under the parent.
func watchRowsWire(name string) []watchRowWire {
	var out []watchRowWire
	for _, l := range watchEngine().List(name) {
		out = append(out, watchRowOf(l))
	}
	return out
}

// watchErrorRunes bounds the error text on the wire. The engine stores a provider or MCP
// transport error verbatim, and the poll repeats every row's copy on each answer.
const watchErrorRunes = 200

// watchWireText cuts s to at most max runes (never mid-rune), marking a cut with an ellipsis.
func watchWireText(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max-1]) + "…"
}

func watchRowOf(l watch.Listed) watchRowWire {
	row := watchRowWire{ID: l.ID, Source: l.Source, Ref: watchWireText(l.Ref, 300), Types: l.Types, URL: watchWireText(l.URL, 500), By: l.Requester,
		Error: watchWireText(strings.Join(strings.Fields(l.LastErr), " "), watchErrorRunes)}
	if row.Types == nil {
		row.Types = []string{}
	}
	if !l.Due.IsZero() {
		row.Due = l.Due.UTC().Format(time.RFC3339)
	}
	if !l.Expires.IsZero() {
		row.Expires = l.Expires.UTC().Format(time.RFC3339)
	}
	return row
}
