package gitx

import "github.com/k-k1/agent-fleet/workspace/agent/internal/session"

// SessionsToSettleUnder is sessionsToSettleUnder for a delete that is not a working copy's: the
// stopped, unlocked, unshelved sessions at or under dir. A space's delete settles its sessions the
// way a working copy's does (ADR 0142 decision 2), so it reads the same list.
func SessionsToSettleUnder(dir string) []session.Meta { return sessionsToSettleUnder(dir) }

// ShelveSessionsUnder is shelveSessionsUnder for the same caller: stopped AI sessions go to the
// shelf, and a shell / ssm one that appeared since the trash step goes to the trash.
func ShelveSessionsUnder(dir string) { shelveSessionsUnder(dir) }

// SessionNamesUnder lists the sessions living at or under dir, by the match the "being deleted"
// mark settles them by (BeginDelete's sessions argument).
func SessionNamesUnder(dir string) []string { return sessionNamesUnder(dir) }
