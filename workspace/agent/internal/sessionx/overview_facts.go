package sessionx

// What the sessions list carries about a NON-claude conversation for the overview card (ADR 0078
// decisions 12 and 13): the context fill, the per-reply token spend and the last utterance.
// claude reads these off its own jsonl tail (claude.TailFacts); every other kind that has them
// only has them in its generic Transcript(), which is a whole-conversation read — so the cost
// rules below are what make it affordable on the 4 s list poll.

import (
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// overviewFactsRefresh is how stale a live session's facts may get. The card is glanced at,
// not read turn by turn, and Transcript() costs tens of milliseconds on a long opencode or
// copilot conversation — paid per session, so the poll itself must not pay it.
const overviewFactsRefresh = 10 * time.Second

// overviewFactsSkip lists the kinds this must not read. claude fills the fields itself from a
// cheaper tail read; agy's Transcript() probes its conversation DB for pending prompts and its
// transcript carries no token counts anyway (its fill comes from a TUI scrape the mirror asks
// for explicitly); shell/ssm have no conversation.
var overviewFactsSkip = map[string]bool{
	session.KindClaude: true,
	session.KindAgy:    true,
	session.KindShell:  true,
	session.KindSSM:    true,
}

type overviewFacts struct {
	ctx    *session.ContextUsage
	spends []int
	say    string
}

type overviewFactsEntry struct {
	facts overviewFacts
	at    time.Time // when facts were read; zero = never
	alive bool      // liveness at that read
	busy  bool      // a refresh is in flight
}

var (
	overviewFactsMu    sync.Mutex
	overviewFactsCache = map[string]*overviewFactsEntry{}
	// overviewFactsSem bounds the refreshes running at once. A list poll asks for every
	// session at the same instant, and a host with a dozen long conversations would otherwise
	// parse all of them in parallel on a memory-constrained container.
	overviewFactsSem = make(chan struct{}, 2)
	// overviewFactsRead is the transcript source; tests swap it.
	overviewFactsRead = func(m session.Meta) ([]transcript.Turn, bool) {
		td, ok := AgentOf(m.Kind).Transcript(m)
		return td.Turns, ok
	}
)

// overviewFactsFor returns the last known facts for m and, when they are stale, starts one
// background refresh. It never blocks on a transcript read: the first poll that sees a
// session returns nothing and the next one carries the answer. A stopped session's
// conversation does not change, so it is read once per stop rather than every interval.
func overviewFactsFor(m session.Meta, alive bool) overviewFacts {
	if overviewFactsSkip[m.Kind] {
		return overviewFacts{}
	}
	now := time.Now()
	overviewFactsMu.Lock()
	e := overviewFactsCache[m.Name]
	if e == nil {
		e = &overviewFactsEntry{}
		overviewFactsCache[m.Name] = e
	}
	fresh := !e.at.IsZero() && e.alive == alive && (!alive || now.Sub(e.at) < overviewFactsRefresh)
	if !fresh && !e.busy {
		e.busy = true
		go refreshOverviewFacts(m, alive)
	}
	f := e.facts
	overviewFactsMu.Unlock()
	return f
}

func refreshOverviewFacts(m session.Meta, alive bool) {
	overviewFactsSem <- struct{}{}
	turns, ok := overviewFactsRead(m)
	<-overviewFactsSem
	overviewFactsMu.Lock()
	defer overviewFactsMu.Unlock()
	e := overviewFactsCache[m.Name]
	if e == nil {
		return
	}
	e.busy = false
	e.at, e.alive = time.Now(), alive
	// A failed read (an unreadable store) keeps what was known: it says nothing about the
	// conversation, and a card that blanks on a transient error reads as a reset.
	if ok {
		e.facts = foldOverviewFacts(turns)
	}
}

// pruneOverviewFacts drops the entries of sessions that no longer exist, so the cache cannot
// outgrow the list. The list handler calls it with the metas it just read.
func pruneOverviewFacts(exists func(name string) bool) {
	overviewFactsMu.Lock()
	for name := range overviewFactsCache {
		if !exists(name) {
			delete(overviewFactsCache, name)
		}
	}
	overviewFactsMu.Unlock()
}

// foldOverviewFacts reduces a normalized transcript to the card's three facts with the same
// arithmetic as the mirror, so a card and its chat never draw different numbers:
//
//   - A reply is the run of assistant turns between two turns a person sent (the Console's
//     groupTurns). Its spend is output SUMMED over the run plus the uncached input and
//     newly-cached tokens of the LAST turn that recorded any (spendOf) — each turn re-states
//     the whole prompt, so summing input would count the context once per tool call.
//   - The context fill is the newest reply's prompt breakdown (latestContext).
//   - A subagent's sidechain turn is skipped: it reports the subagent's context, not this
//     session's, and the mirror hides it.
//
// A user turn with neither text nor parts is not a person speaking, so it does not split a reply.
func foldOverviewFacts(turns []transcript.Turn) overviewFacts {
	var f overviewFacts
	var out, in, read, create int
	var model string
	var window int
	inReply := false
	flush := func() {
		if !inReply {
			return
		}
		if n := in + create + out; n > 0 {
			f.spends = append(f.spends, n)
		}
		if in+read+create > 0 {
			f.ctx = &session.ContextUsage{Read: read, Create: create, Fresh: in, Model: model, Window: window}
			if window > 0 {
				f.ctx.WindowSource = "recorded"
			}
		}
		out, in, read, create, model, window = 0, 0, 0, 0, "", 0
		inReply = false
	}
	for _, t := range turns {
		if t.Sidechain {
			continue
		}
		if t.Role != "assistant" {
			if len(t.Parts) > 0 || strings.TrimSpace(t.Text) != "" {
				flush()
			}
			continue
		}
		inReply = true
		out += t.OutTok
		if t.InTok+t.CacheRead+t.CacheCreate > 0 {
			in, read, create = t.InTok, t.CacheRead, t.CacheCreate
		}
		if model == "" {
			model = t.Model
		}
		if t.CtxWindow > 0 {
			window = t.CtxWindow
		}
		if s := strings.TrimSpace(t.Text); s != "" {
			f.say = s
		}
	}
	flush()
	if f.say != "" {
		f.say = claude.LastSayLine(f.say)
	}
	if len(f.spends) > claude.TokenSpendMax {
		f.spends = f.spends[len(f.spends)-claude.TokenSpendMax:]
	}
	return f
}
