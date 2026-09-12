package claude

// The one line of the agent's newest utterance that rides the sessions list (ADR 0078
// decision 12). The Console's overview grid shows it on every card, so this runs for every
// claude session on the 4 s list poll — the cost rules below are the whole design.

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
	"time"
)

// lastSayMax caps the line in RUNES, here rather than in the Console: a card shows one
// ellipsized line whatever the number, so anything longer is payload nobody reads, on every
// poll, for every session. 120 fills a wide card and truncates a narrow one.
const lastSayMax = 120

// lastSayCache memoizes the line per sid, keyed by the transcript's mtime — the same
// arrangement as ctxCache (context.go), for the same reason: an unchanged jsonl must cost a
// stat, not a read. A stopped session's log never changes, so its entry is written once.
var (
	lastSayMu    sync.Mutex
	lastSayCache = map[string]lastSayEntry{}
)

type lastSayEntry struct {
	mtime time.Time
	text  string
}

// LastSay returns the opening line of the newest thing this claude session SAID — the last
// assistant record carrying text, collapsed to one line and capped. "" when the session has
// not spoken yet (a fresh session, a transcript that is only bookkeeping) or when sid has no
// log at all.
//
// Two rules keep it affordable on the polled list, and both are load-bearing:
//
//   - It reads ONE tail window (transcriptTailWindow) and never widens to the whole file,
//     unlike lastLineWhere — whose tailThenWhole reads BOTH windows eagerly, so every call on
//     a transcript past the window costs the whole multi-MB file. Re-reading a whole
//     conversation log on every poll is a defect this project has already shipped once (codex's
//     rollout), and a single turn CAN fill the window with nothing but tool records (large
//     Read/Bash results), so the widening path would not even be rare.
//   - When that window holds no utterance, the previously cached line is KEPT rather than
//     blanked. It is still the truth — nothing newer has been said — and a card that empties
//     itself halfway through a long turn reads as "this session went quiet".
func LastSay(sid string) string {
	if sid == "" {
		return ""
	}
	paths := jsonlByMtime(sid)
	if len(paths) == 0 {
		return ""
	}
	mt := jsonlMtime(paths[0]) // newest sibling; an append bumps it

	lastSayMu.Lock()
	prev, cached := lastSayCache[sid]
	if cached && prev.mtime.Equal(mt) {
		lastSayMu.Unlock()
		return prev.text
	}
	lastSayMu.Unlock()

	// Newest transcript first, skipping the ones that hold no utterance: a session commonly
	// has sibling logs (a Remote Control bridge stub, a lone summary) and the stub can carry
	// the newer mtime, exactly as TranscriptRead documents.
	say := prev.text
	for _, p := range paths {
		if t, ok := lastSpokenInTail(p); ok {
			say = lastSayLine(t)
			break
		}
	}
	lastSayMu.Lock()
	lastSayCache[sid] = lastSayEntry{mtime: mt, text: say}
	lastSayMu.Unlock()
	return say
}

// lastSpokenInTail scans p's tail window backwards for the last record the agent spoke.
// ok=false means this file had nothing to say — the caller tries the next sibling, and if
// none has one, keeps what it had.
func lastSpokenInTail(p string) (string, bool) {
	lines := tailLines(p)
	for i := len(lines) - 1; i >= 0; i-- {
		if t := spokenText(lines[i]); t != "" {
			return t, true
		}
	}
	return "", false
}

// tailLines reads only the last transcriptTailWindow bytes of p. See LastSay for why it
// must not widen.
func tailLines(p string) [][]byte {
	f, err := os.Open(p)
	if err != nil {
		return nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil
	}
	lines, _ := windowLines(f, fi.Size(), transcriptTailWindow)
	return lines
}

// spokenText returns the text an assistant record carries, or "" when the line is not the
// agent speaking to the user.
//
// Three exclusions, each of which would otherwise put something on the card that the person
// reading the grid did not hear: a subagent's turn (isSidechain — older claude writes them
// inline into the main transcript), a synthesized API-error record (isApiErrorMessage, whose
// text is the CLI-facing "Please run /login" — errors.go), and a meta line. A turn that was
// pure tool calls yields "" because only text blocks count, so the scan walks past it to the
// last thing actually said.
func spokenText(line []byte) string {
	var ev struct {
		Type        string `json:"type"`
		IsMeta      bool   `json:"isMeta"`
		IsSidechain bool   `json:"isSidechain"`
		IsAPIError  bool   `json:"isApiErrorMessage"`
		Message     struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &ev) != nil {
		return ""
	}
	if ev.Type != "assistant" || ev.IsMeta || ev.IsSidechain || ev.IsAPIError {
		return ""
	}
	return contentText(ev.Message.Content)
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
