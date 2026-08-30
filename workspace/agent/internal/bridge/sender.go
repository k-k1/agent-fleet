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
// the "goroutine + a few MB" budget (docs/log/37 「メモリ」). Providers are rebuilt
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
	provs := Providers(s, cacheDiscordDM, cacheSlackDM)
	if len(provs) == 0 {
		for _, n := range names {
			_ = os.Remove(filepath.Join(dir, n))
		}
		return
	}
	drainWith(provs)
}

// drainWith is the provider-agnostic queue pass (split out for tests). Each provider
// resumes from its own persisted delivery cursor (docs/log/37 重複対策), so a partial
// failure never re-posts what already landed. An attempt that made ANY progress
// resets the retry counter (a long message that advances every tick can't be dropped
// mid-stream); only a fully-stuck tick counts against maxAttempts.
func drainWith(provs []Provider) {
	dir := queueDir()
	for _, n := range queueFiles(dir) {
		path := filepath.Join(dir, n)
		q, ok := readQueued(path)
		if !ok {
			continue
		}
		if q.Delivered == nil {
			q.Delivered = map[string]int{}
		}
		key := eventKeyFor(q.Kind)
		allOK := true
		progressed := false
		for _, p := range provs {
			if !p.Wants(key) {
				continue
			}
			if rs, ok := p.(ResumableSender); ok {
				from := q.Delivered[p.Name()]
				delivered, err := rs.SendFrom(q.Message, from)
				if delivered > from {
					progressed = true
				}
				q.Delivered[p.Name()] = delivered
				if err != nil {
					log.Printf("bridge: %s send %s failed (attempt %d): %v", p.Name(), q.Kind, q.Attempts+1, err)
					allOK = false
				}
				continue
			}
			// Whole-message fallback for a non-resumable provider (a partial failure
			// may duplicate on the provider that succeeded).
			if err := p.Send(q.Message); err != nil {
				log.Printf("bridge: %s send %s failed (attempt %d): %v", p.Name(), q.Kind, q.Attempts+1, err)
				allOK = false
			}
		}
		if allOK {
			_ = os.Remove(path)
			continue
		}
		if progressed {
			q.Attempts = 0 // healthy — advanced this tick; don't let a long send hit the cap
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
	_ = secrets.Update(func(s *secrets.Data) error {
		if s.Discord != nil {
			s.Discord.DMChannelID = channelID
		}
		return nil
	})
}

// cacheSlackDM is the Slack write-through DM cache (docs/log/37 Slack 追随).
func cacheSlackDM(channelID string) {
	_ = secrets.Update(func(s *secrets.Data) error {
		if s.Slack != nil {
			s.Slack.DMChannelID = channelID
		}
		return nil
	})
}
