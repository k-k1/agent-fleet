package bridge

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// StartSender launches the daemon-side delivery loop. One goroutine, a slow
// ticker, sequential sends — the WSS/network side of the bridge must stay in
// the "goroutine + a few MB" budget (docs/37 「メモリ」). Providers are rebuilt
// from the secrets store every drain, so connect/disconnect in the Console
// takes effect on the next tick without any registry.
func StartSender() {
	go func() {
		for range time.Tick(3 * time.Second) {
			DrainOnce()
		}
	}()
}

// DrainOnce processes the whole queue once: send each entry through every
// send-capable provider that wants its event group, retry a bounded number of
// times across ticks (attempt count persisted in the entry), drop + log beyond
// maxAttempts. With no provider configured the queue is simply cleared —
// Enqueue writes unconditionally (hook subprocesses can't cheaply know whether
// a bridge is configured) and this is where that decision lives.
func DrainOnce() {
	dir := queueDir()
	names := queueFiles(dir)
	if len(names) == 0 {
		return
	}
	s, err := secrets.Load()
	if err != nil {
		return // store unreadable — leave the queue for the next tick
	}
	provs := Providers(s, cacheDiscordDM)
	if len(provs) == 0 {
		for _, n := range names {
			_ = os.Remove(filepath.Join(dir, n))
		}
		return
	}
	drainWith(provs)
}

// drainWith is the provider-agnostic queue pass (split out for tests).
func drainWith(provs []Provider) {
	dir := queueDir()
	for _, n := range queueFiles(dir) {
		path := filepath.Join(dir, n)
		q, ok := readQueued(path)
		if !ok {
			continue
		}
		key := eventKeyFor(q.Kind)
		delivered := true
		for _, p := range provs {
			if !p.Wants(key) {
				continue
			}
			// A partial multi-provider failure retries the whole entry (possible
			// duplicate on the provider that succeeded). Accepted for P1 — there
			// is one provider; revisit with per-provider sent-markers for Slack.
			if err := p.Send(q.Message); err != nil {
				log.Printf("bridge: %s send %s failed (attempt %d): %v", p.Name(), q.Kind, q.Attempts+1, err)
				delivered = false
			}
		}
		if delivered {
			_ = os.Remove(path)
			continue
		}
		q.Attempts++
		if q.Attempts >= maxAttempts {
			log.Printf("bridge: dropping %s after %d attempts", q.Kind, q.Attempts)
			_ = os.Remove(path)
			continue
		}
		rewriteQueued(path, q)
	}
}

// rewriteQueued persists the bumped attempt count; if the rewrite fails the
// entry just retries with a stale count — harmless.
func rewriteQueued(path string, q queued) {
	b, err := json.Marshal(q)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, b, 0o600)
}

// cacheDiscordDM writes a freshly resolved DM channel id back to the store
// (write-through cache, same pattern as the connections status handlers).
func cacheDiscordDM(channelID string) {
	s, err := secrets.Load()
	if err != nil || s.Discord == nil {
		return
	}
	s.Discord.DMChannelID = channelID
	_ = s.Save()
}
