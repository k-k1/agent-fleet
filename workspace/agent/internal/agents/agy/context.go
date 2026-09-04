package agy

// Session-level context fill for the chat mirror's ContextBar (docs/log/32).
// Neither agy's transcript (transcript_full.jsonl) nor any other persisted state
// holds a token count (measured: 0 hits on a real machine), so the TUI's /context
// panel ("Visualize current context usage" - it draws the total `26.0k/1.0M
// tokens` plus a per-category breakdown) is the only source. Through the same
// agents.Flow plumbing as the /usage scrape (usage.go), the conversation is
// resumed in a separate process with `--conversation <uuid>`, /context is typed
// and only the total line is parsed. The numbers are agy's own client-side
// estimate (the panel says "Estimated usage" itself), not API-reported values.
//
// Resuming a live conversation in parallel is verified on a real machine
// (2026-07-20, v1.1.4): a second process B resumes running session A's
// conversation with --conversation, reads /context, and killing B leaves A
// answering and transcript_full.jsonl intact (SQLite WAL, and /context only
// reads).
//
// Starting the TUI takes seconds, so it must not run on every poll: the reading
// is cached per conversation and refreshed in the background only when the
// transcript's size+mtime changed, no more often than ctxScrapeMinInterval.
// ContextFill itself never blocks (it returns the cache immediately, nil before
// the first scrape).

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// ctxScrapeMinInterval floors how often one conversation may be re-scraped —
// a running turn touches the transcript every few seconds, and each scrape
// spawns a whole TUI (memory-constrained host).
const ctxScrapeMinInterval = 60 * time.Second

type ctxCacheEnt struct {
	val     *transcript.Context
	sig     string    // transcript size+mtime the reading was scraped at
	at      time.Time // last attempt (success OR failure) — retry floor
	running bool
}

var (
	ctxMu    sync.Mutex
	ctxCache = map[string]*ctxCacheEnt{}
	// One scrape at a time process-wide: parallel agy mirrors must not stack
	// TUI spawns on the shared, memory-constrained host.
	ctxGate = make(chan struct{}, 1)
)

// ContextFill implements agents.ContextReporter for the generic /messages
// handler. Non-blocking: returns the cached reading (nil before the first
// scrape lands) and lets a background refresh follow the transcript.
func (agentImpl) ContextFill(m session.Meta) *transcript.Context {
	conv := sids.Read(session.UUID(m.Dir, m.Name))
	if conv == "" {
		return nil // no conversation yet — nothing to read /context against
	}
	return contextFill(conv)
}

func contextFill(conv string) *transcript.Context {
	sig := transcriptSig(conv)
	if sig == "" {
		return nil // transcript not materialized yet (first prompt pending)
	}
	ctxMu.Lock()
	defer ctxMu.Unlock()
	ent := ctxCache[conv]
	if ent == nil {
		ent = &ctxCacheEnt{}
		ctxCache[conv] = ent
	}
	// Refresh when the transcript moved past the last reading — but never more
	// often than the floor, and never two at once for the same conversation.
	// at advances on failure too, so a broken conversation cannot fire a scrape on
	// every poll.
	if ent.sig != sig && !ent.running && time.Since(ent.at) > ctxScrapeMinInterval {
		ent.running = true
		go func() {
			c, err := scrapeContext(conv)
			ctxMu.Lock()
			defer ctxMu.Unlock()
			ent.running = false
			ent.at = time.Now()
			if err == nil {
				ent.val, ent.sig = c, sig
			}
		}()
	}
	return ent.val
}

// transcriptSig fingerprints the conversation's transcript so a reading can be
// tied to the state it measured ("" = no transcript yet).
func transcriptSig(conv string) string {
	fi, err := os.Stat(transcriptPath(conv))
	if err != nil {
		return ""
	}
	return strconv.FormatInt(fi.Size(), 10) + "-" + strconv.FormatInt(fi.ModTime().UnixNano(), 10)
}

var (
	// The panel's last category line — once it renders, the total line above it
	// is on screen too.
	ctxPanelRe = regexp.MustCompile(`Free space`)
	// Total line: "Gemini 3.5 Flash (Medium) · 26.0k/1.0M tokens" ("(2.5%)" may
	// wrap to the next line — not needed, the pct is derivable).
	ctxTotalRe = regexp.MustCompile(`·\s*([0-9][0-9.]*[kM]?)/([0-9][0-9.]*[kM]?) tokens`)
)

// scrapeContext resumes conv in a scratch-dir agy (`--conversation` resumes
// regardless of cwd — verified on a real machine), drives it to the /context
// panel, and parses the total. The scratch dir is pre-trusted so the trust
// prompt never renders (and interactively accepted as a fallback, same as
// scrapeUsage).
func scrapeContext(conv string) (*transcript.Context, error) {
	ctxGate <- struct{}{}
	defer func() { <-ctxGate }()
	dir := filepath.Join(os.TempDir(), "af-agy-ctx")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	EnsureWorkspaceTrusted(dir)
	enforceTelemetryOff() // launch-time re-pin, same as BuildLaunch
	cmd := exec.Command("agy", "--conversation", conv)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	f, err := agents.StartFlow(cmd)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// With the pre-trust in place this screen never appears. If it does, never blind-press
	// Enter: upstream swaps which option is the default (trustprompt.go).
	if m := f.WaitFor(regexp.MustCompile(trustRe.String()+`|`+readyRe.String()), 25*time.Second); m == "" {
		return nil, errString("agy did not reach the prompt (timeout)")
	} else if trustRe.MatchString(m) {
		if err := answerTrustPrompt(f); err != nil {
			return nil, err
		}
		if f.WaitFor(readyRe, 20*time.Second) == "" {
			return nil, errString("agy did not reach the prompt after trust")
		}
	}
	// Same Ink quirk as the /usage scrape: the carriage return must be a
	// separate keystroke or it is dropped.
	if _, err := f.Ptmx.Write([]byte("/context")); err != nil {
		return nil, err
	}
	time.Sleep(400 * time.Millisecond)
	if _, err := f.Ptmx.Write([]byte("\r")); err != nil {
		return nil, err
	}
	if f.WaitFor(ctxPanelRe, 20*time.Second) == "" {
		return nil, errString("context panel did not render")
	}
	// One extra beat so the final full frame lands in the buffer before parsing.
	time.Sleep(500 * time.Millisecond)
	return parseContext(f.Clean())
}

// parseContext extracts the total line from the LAST panel render — the resumed
// conversation's own replayed history could contain the same words, so only the
// text from the final "Context Usage" header on is trusted.
func parseContext(out string) (*transcript.Context, error) {
	if i := strings.LastIndex(out, "Context Usage"); i >= 0 {
		out = out[i:]
	}
	m := ctxTotalRe.FindStringSubmatch(out)
	if m == nil {
		return nil, errString("no context total found in /context output")
	}
	tok, err := parseTokCount(m[1])
	if err != nil {
		return nil, err
	}
	win, err := parseTokCount(m[2])
	if err != nil {
		return nil, err
	}
	if win <= 0 {
		return nil, errString("context window parsed as 0")
	}
	return &transcript.Context{
		Tokens: tok,
		Window: win,
		At:     time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// parseTokCount turns the panel's abbreviated count ("80", "26.0k", "1.0M")
// into tokens.
func parseTokCount(s string) (int, error) {
	mult := 1.0
	switch {
	case strings.HasSuffix(s, "k"):
		mult, s = 1e3, strings.TrimSuffix(s, "k")
	case strings.HasSuffix(s, "M"):
		mult, s = 1e6, strings.TrimSuffix(s, "M")
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	return int(v * mult), nil
}
