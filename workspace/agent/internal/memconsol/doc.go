// Package memconsol is the Agent Fleet adapter around the consolidation engine (ADR 0130 decisions
// 6 and 7): it collects observations about memories, groups them into topics, decides which
// topics are ripe, calls the engine (internal/memengine) with the configured runner when the
// workspace is idle, validates what comes back and queues proposals for the member.
//
// The invariant the whole package is built around: nothing reaches the memory store without the
// member's approval. The run only writes proposals into this package's own queue directory;
// the one function that publishes (Approve) is reachable from the member-origin approve route
// alone, re-validates against the memory as it is at that moment, and publishes through the
// store's normal save/patch path authored as the consolidator, so the change shows in the change
// list and can be reverted. Model output is never trusted: every answer goes through the engine's
// schema validation and this package's own checks before it can become a proposal.
package memconsol
