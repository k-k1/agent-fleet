package main

import (
	"crypto/rand"
	"encoding/base32"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// Session slugs are short, RANDOM, unique identifiers ("sk7f3q9"). A slug is the
// session's IMMUTABLE identity: the tmux name (claude_sk7f3q9), the meta filename
// (sk7f3q9.json), and — via session.UUID(dir, slug) — the claude session id that keys
// the jsonl. The user-facing, editable display name lives separately in
// session.Meta.Title; the slug itself is hidden from the list (shown only in the row
// tooltip) so it never needs to be human-meaningful.
//
// Random rather than a persisted counter: a counter file would live in the home
// volume and a same-uid shell could delete it, resetting the sequence — which, with a
// lingering orphan jsonl, could re-derive a past sid and accidentally --resume an
// archived/pruned conversation. A fresh random slug has no such shared state to
// corrupt: its derived sid never collides with a past session's, so a new session
// always starts clean. allocSessionName additionally guards on the jsonl directly, so
// correctness does not depend on the id scheme at all.

const slugLen = 7 // "s" + 6 base32 chars

// allocSessionName returns a fresh unique slug for a session that will run in dir. It
// draws random slugs until it finds one that is neither claimed by a meta / live tmux
// session NOR maps (via dir) to an sid that already has a conversation on disk.
func allocSessionName(dir string) string {
	for {
		slug := randSlug()
		if slugTaken(slug) {
			continue
		}
		// Never hand out a slug whose derived sid already has a jsonl (e.g. a lingering
		// orphan from a pruned session), so a new session can't --resume a past
		// conversation. Astronomically unlikely for a random slug, but this makes the
		// no-resurrection guarantee independent of the id scheme.
		if claude.SessionJSONLExists(session.UUID(dir, slug)) {
			continue
		}
		return slug
	}
}

// slugTaken reports whether a slug is already claimed by a meta or a live tmux session.
func slugTaken(slug string) bool {
	if _, ok := session.ReadMeta(slug); ok {
		return true
	}
	return tmuxx.HasSession(session.TmuxName(slug))
}

// randSlug returns a random slug: an "s" prefix (visual marker; keeps it non-numeric)
// followed by 6 lowercase base32 chars (a-z2-7, ~30 bits). Matches session.ValidName. Panics only
// if crypto/rand fails, which it should not — the agent can't safely mint sessions
// without a unique id.
func randSlug() string {
	var b [5]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("randSlug: " + err.Error())
	}
	enc := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:]))
	return "s" + enc[:slugLen-1]
}
