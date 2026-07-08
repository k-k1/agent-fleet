package main

import (
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// session.ContextUsage（claude セッションの現在のコンテキスト充填率、ワイヤ型）は
// internal/session へ移設（docs/23 残① Wave A）。ここには jsonl の解析と
// mtime キャッシュ（main 依存: transcriptRead/parseTurn）だけが残る。

// ctxCache memoizes the parsed context per sid, keyed by the transcript's mtime, so
// repeated sessions-list polls don't re-read and re-parse an unchanged jsonl. A new
// turn appends to the log (bumping its mtime), which invalidates the entry.
var (
	ctxCacheMu sync.Mutex
	ctxCache   = map[string]ctxCacheEntry{}
)

type ctxCacheEntry struct {
	mtime time.Time
	usage *session.ContextUsage
}

// latestSessionContext returns the current context fill for a claude session, or
// nil when none is recorded yet (a fresh session, or an older Agent that predates
// the usage field). Cheap on the common path: it stats the transcript and reuses
// the cached parse while the mtime is unchanged; it only reads+parses on change.
// It reuses the same parseTurn as /messages, so the value matches the chat view's
// own ContextBar exactly.
func latestSessionContext(sid string) *session.ContextUsage {
	paths := jsonlByMtime(sid)
	if len(paths) == 0 {
		return nil
	}
	mt := jsonlMtime(paths[0]) // newest sibling; an append bumps it

	ctxCacheMu.Lock()
	if e, ok := ctxCache[sid]; ok && e.mtime.Equal(mt) {
		u := e.usage
		ctxCacheMu.Unlock()
		return u
	}
	ctxCacheMu.Unlock()

	lines, _, _ := transcriptRead(sid)
	var u *session.ContextUsage
	// The last assistant event carrying usage is the current context size (matching
	// MirrorView's latestContext, which takes the last event's input/cache).
	for i, ln := range lines {
		t, ok := parseTurn(ln, i)
		if !ok || t.Role != "assistant" {
			continue
		}
		if t.InTok+t.CacheRead+t.CacheCreate > 0 {
			u = &session.ContextUsage{Read: t.CacheRead, Create: t.CacheCreate, Fresh: t.InTok, Model: t.Model}
		}
	}

	ctxCacheMu.Lock()
	ctxCache[sid] = ctxCacheEntry{mtime: mt, usage: u}
	ctxCacheMu.Unlock()
	return u
}
