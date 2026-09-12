package sessionx

// The three whole-transcript aggregates of a /messages response — answers, tasks and files —
// and the digest that lets a steady-state poll leave them out.
//
// WHY. They do not depend on the window of turns being sent: each is folded from the whole
// transcript, so every poll rebuilds and re-sends the lot. On a real session that is 21-53 file
// rows, 5.3-13.3 KiB raw and 0.9-2.6 KiB gzipped, ON EVERY TICK — 1.2s while a turn runs. When
// nothing at all changed the Control Plane's conditional GET already answers 304
// (control-plane/etag.go, and the byte-identity that layer depends on is pinned by
// TestUnchangedPollIsByteIdentical). While a turn RUNS it cannot: the live turn's parts move
// every tick, so the response legitimately differs and the aggregates ride along unchanged
// inside it.
//
// So the client carries the digest of what it already holds (`?agg=`), and a poll whose
// aggregates hash the same is answered with `aggSame: true` and none of the three.
//
// COMPATIBILITY runs both ways without a version check: a client that sends no `agg` gets the
// aggregates as before, and an Agent that does not know the parameter simply sends them too —
// the client applies whatever arrives and only holds its state when it is told `aggSame`.

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// aggHeldIsCurrent reports whether the client already holds these aggregates — i.e. it sent
// `?agg=<digest>` matching them on the steady-state poll the exchange applies to: `since=<cursor>`
// with no window parameters and no reset. A windowed read (the first open, a backward page) and a
// reset hand the client turns it has never seen, and answers are patched onto turns as they
// arrive, so withholding them there would render a decided question as undecided.
func aggHeldIsCurrent(r *http.Request, reset bool, sig string) bool {
	if reset || sig == "" {
		return false
	}
	q := r.URL.Query()
	if q.Get("agg") != sig {
		return false
	}
	return q.Get("since") != "" && q.Get("since") != "0" && q.Get("before") == "" && q.Get("tail") == ""
}

// aggDigest is FNV-1a over the three marshalled aggregates. Only ever compared against a value
// this same code produced, so the hash needs to be stable and cheap, not cryptographic — and it
// is computed from the JSON that would have been sent, so it cannot drift from what it stands
// for. A marshalling error (impossible for these types) degrades to a digest that never
// matches, i.e. to sending everything.
func aggDigest(answers map[string]claude.InteractionAnswer, tasks []transcript.Task, files []transcript.FileTouch) string {
	h := fnv.New64a()
	for _, v := range []any{answers, tasks, files} {
		b, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		_, _ = h.Write(b)
		_, _ = h.Write([]byte{0})
	}
	return fmt.Sprintf("%016x", h.Sum64())
}
