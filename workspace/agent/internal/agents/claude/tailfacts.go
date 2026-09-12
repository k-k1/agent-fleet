package claude

// What the sessions list derives from the TAIL of a claude transcript (ADR 0078 decisions 12
// and 13): the one line of the agent's newest utterance, and the per-turn token spend the
// overview card draws as a trend. The Console's overview grid shows both on every card, so
// this runs for every claude session on the 4 s list poll — the cost rules below are the whole
// design, and both facts come out of ONE read.

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
	"time"
)

// lastSayMax caps the utterance in RUNES, here rather than in the Console: a card shows one
// ellipsized line whatever the number, so anything longer is payload nobody reads, on every
// poll, for every session. 120 fills a wide card and truncates a narrow one.
const lastSayMax = 120

// tokenSpendMax caps the trend at the newest N turns, for the same reason: the card draws a
// sparkline about 120px wide, so more points than this are pixels nobody can tell apart —
// paid for per session, per poll. The Console needs two to draw anything at all.
const tokenSpendMax = 24

// tailCache memoizes both facts per sid, keyed by the transcript's mtime — the same
// arrangement as ctxCache (context.go), for the same reason: an unchanged jsonl must cost a
// stat, not a read. A stopped session's log never changes, so its entry is written once.
var (
	tailMu    sync.Mutex
	tailCache = map[string]tailFacts{}
)

// tailFacts is one memoized scan of a transcript's tail.
type tailFacts struct {
	mtime  time.Time
	say    string
	spends []int
}

// TailFacts returns what the sessions list carries about a claude session's conversation:
// say is the opening line of the newest thing it SAID (the last assistant record with text,
// collapsed to one line and capped), and spends is the newly-consumed tokens of each of the
// last tokenSpendMax assistant turns, oldest first. Both are empty when the session has not
// spoken yet, or when sid has no log at all.
//
// Three rules keep it affordable on the polled list, and all three are load-bearing:
//
//   - It reads ONE tail window (transcriptTailWindow) and never widens to the whole file,
//     unlike lastLineWhere — whose tailThenWhole reads BOTH windows eagerly, so every call on
//     a transcript past the window costs the whole multi-MB file. Re-reading a whole
//     conversation log on every poll is a defect this project has already shipped once (codex's
//     rollout), and a single turn CAN fill the window with nothing but tool records (large
//     Read/Bash results), so the widening path would not even be rare.
//   - Both facts come out of that ONE scan. Asking for them separately would double the reads
//     for a second answer derived from the same lines.
//   - When the window yields nothing, what was already known is KEPT rather than blanked. It
//     is still the truth — nothing newer has been said — and a card that empties itself
//     halfway through a long turn reads as "this session went quiet".
func TailFacts(sid string) (say string, spends []int) {
	if sid == "" {
		return "", nil
	}
	paths := jsonlByMtime(sid)
	if len(paths) == 0 {
		return "", nil
	}
	mt := jsonlMtime(paths[0]) // newest sibling; an append bumps it

	tailMu.Lock()
	prev, cached := tailCache[sid]
	if cached && prev.mtime.Equal(mt) {
		tailMu.Unlock()
		return prev.say, prev.spends
	}
	tailMu.Unlock()

	// Newest transcript first, skipping the ones that hold no utterance: a session commonly
	// has sibling logs (a Remote Control bridge stub, a lone summary) and the stub can carry
	// the newer mtime, exactly as TranscriptRead documents.
	next := tailFacts{mtime: mt, say: prev.say, spends: prev.spends}
	for _, p := range paths {
		s, sp, ok := scanTail(p)
		if !ok {
			continue
		}
		next.say = lastSayLine(s)
		// A window can hold the utterance but too few turns to draw a trend; keep the
		// series that was already known rather than making the sparkline blink out.
		if len(sp) >= 2 {
			next.spends = sp
		}
		break
	}
	tailMu.Lock()
	tailCache[sid] = next
	tailMu.Unlock()
	return next.say, next.spends
}

// scanTail walks p's tail window once, oldest line first. It returns the last utterance and
// one spend per REPLY in chronological order. ok=false means this file had nothing the agent
// said — the caller tries the next sibling, and if none has one, keeps what it had.
//
// A reply is many rows: claude writes the text, each tool call and the follow-ups as separate
// assistant records, and their tool_results as user rows that are not turns at all. The
// Console folds exactly that run into one block (groupTurns), so the trend has one point per
// reply rather than per API round-trip — and this has to fold it the same way, or the card and
// the chat would draw different trends for the same session.
func scanTail(p string) (say string, spends []int, ok bool) {
	lines, truncated := tailLines(p)
	var cur replySpend
	flush := func() {
		if n := cur.total(); n > 0 {
			spends = append(spends, n)
		}
		cur = replySpend{}
	}
	for _, ln := range lines {
		row, text, u := classifyRow(ln)
		switch row {
		case rowUser:
			flush() // a person's turn ends the reply before it
		case rowAgent:
			cur.fold(u)
			if text != "" {
				say, ok = text, true
			}
		}
	}
	flush()
	// The window cut into the middle of the oldest reply, so its first point is only the
	// part that survived the cut — a low bar that says nothing about that turn. Drop it
	// rather than draw it.
	if truncated && len(spends) > 0 {
		spends = spends[1:]
	}
	if len(spends) > tokenSpendMax {
		spends = spends[len(spends)-tokenSpendMax:]
	}
	return say, spends, ok
}

// replySpend accumulates one reply's rows the way the Console's groupTurns does: output
// tokens SUM across the rows, while the input and newly-cached figures are taken from the
// LAST row that recorded any (each row re-states the whole prompt, so summing them would
// count the same context once per tool call).
type replySpend struct {
	out, in, create int
}

func (r *replySpend) fold(u rowUsage) {
	r.out += u.out
	if u.in > 0 || u.read > 0 || u.create > 0 {
		r.in, r.create = u.in, u.create
	}
}

// total is the reply's newly-consumed tokens: uncached input + newly-cached + output. Cache
// READS are reused context rather than fresh spend, which is why they are absent here —
// the identical arithmetic as the Console's spendOf (mirror/transcript/model.ts).
func (r replySpend) total() int { return r.in + r.create + r.out }

type rowKind int

const (
	rowOther rowKind = iota // bookkeeping, a tool_result, a subagent, a failed turn
	rowUser                 // a person's turn: it ends the reply before it
	rowAgent                // this session's agent speaking
)

// rowUsage is one row's token counts, as claude records them.
type rowUsage struct{ in, out, read, create int }

// classifyRow parses one transcript row into what the trend and the utterance need from it.
//
// Three exclusions put a row in rowOther, each of which would otherwise put something on the
// card that the person reading the grid did not hear: a subagent's turn (isSidechain — older
// claude writes them inline into the main transcript), a synthesized API-error record
// (isApiErrorMessage, whose text is the CLI-facing "Please run /login" — errors.go), and a
// meta line. A tool_result row carries no text and so reads as rowOther, which is what keeps
// a reply's tool calls from splitting it into several points.
func classifyRow(line []byte) (rowKind, string, rowUsage) {
	var ev struct {
		Type        string `json:"type"`
		IsMeta      bool   `json:"isMeta"`
		IsSidechain bool   `json:"isSidechain"`
		IsAPIError  bool   `json:"isApiErrorMessage"`
		Message     struct {
			Content json.RawMessage `json:"content"`
			Usage   struct {
				InputTokens              int `json:"input_tokens"`
				OutputTokens             int `json:"output_tokens"`
				CacheReadInputTokens     int `json:"cache_read_input_tokens"`
				CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			} `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &ev) != nil || ev.IsMeta || ev.IsSidechain || ev.IsAPIError {
		return rowOther, "", rowUsage{}
	}
	switch ev.Type {
	case "assistant":
		u := ev.Message.Usage
		return rowAgent, contentText(ev.Message.Content), rowUsage{
			in: u.InputTokens, out: u.OutputTokens,
			read: u.CacheReadInputTokens, create: u.CacheCreationInputTokens,
		}
	case "user":
		// Only a row with words in it is a person's turn; a tool_result row has none, and
		// treating it as one would break every reply at its first tool call.
		if contentText(ev.Message.Content) == "" {
			return rowOther, "", rowUsage{}
		}
		return rowUser, "", rowUsage{}
	}
	return rowOther, "", rowUsage{}
}

// tailLines reads only the last transcriptTailWindow bytes of p, and says whether the file
// was bigger than that. See TailFacts for why it must not widen.
func tailLines(p string) (lines [][]byte, truncated bool) {
	f, err := os.Open(p)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, false
	}
	lines, _ = windowLines(f, fi.Size(), transcriptTailWindow)
	return lines, fi.Size() > transcriptTailWindow
}

// lastSayLine folds an utterance into the single line a card can show: every run of
// whitespace — the paragraph breaks and the indentation of a Markdown answer included —
// becomes one space, and the result is capped at lastSayMax runes. Runes, not bytes: cutting
// a Japanese answer mid-codepoint would emit invalid UTF-8 onto the wire.
func lastSayLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > lastSayMax {
		return strings.TrimRight(string(r[:lastSayMax]), " ") + "…"
	}
	return s
}
