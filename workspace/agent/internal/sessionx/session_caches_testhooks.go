package sessionx

import (
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// SeedSessionCachesForTest and SessionCachesHeldForTest let package main's tests (trashSession
// lives there) seed and inspect the per-name caches that are otherwise unexported. Production
// code never calls them.
func SeedSessionCachesForTest(name string) {
	fileAggMu.Lock()
	fileAggs[name] = &fileAggEntry{used: time.Now()}
	fileAggMu.Unlock()
	spendCache.Lock()
	spendCache.m[name] = spendEntry{at: time.Now(), spend: session.Spend{USD: 1, Priced: true}}
	spendCache.Unlock()
}

func SessionCachesHeldForTest(name string) bool {
	f, s := sessionCachesHeld(name)
	return f && s
}

func sessionCachesHeld(name string) (files, spend bool) {
	fileAggMu.Lock()
	_, files = fileAggs[name]
	fileAggMu.Unlock()
	spendCache.Lock()
	_, spend = spendCache.m[name]
	spendCache.Unlock()
	return files, spend
}
