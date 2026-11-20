package agents

import "github.com/k-k1/agent-fleet/workspace/agent/internal/session"

// StopBackgroundBusyer is an optional Agent capability: whether the session still has
// background work that a stop-after-turn would cut off (a worker process, a background shell,
// a running host command). The answer reads process or driver state, so callers ask it only
// once the cheaper evidence is quiet. A kind without it answers false. Resolved by exact
// registry key; callers normalize an unknown kind to claude first.
type StopBackgroundBusyer interface {
	StopBackgroundBusy(m session.Meta) bool
}

// LocalTranscriptKeeper is an optional Agent capability: the kind's conversation lives in
// transcript files the Agent owns (and so the session trash must bundle and remove), rather
// than in the provider's own store, which the trash never takes. A kind without it keeps
// none. Resolved by exact registry key; callers normalize an unknown kind to claude first.
type LocalTranscriptKeeper interface {
	KeepsLocalTranscript() bool
}
