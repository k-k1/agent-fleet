package gitx

import "github.com/k-k1/agent-fleet/workspace/agent/internal/session"

// SessionsToSettleUnder is sessionsToSettleUnder for a delete that is not a working copy's: the
// stopped, unlocked, unshelved sessions living in dir. A space's delete settles its sessions the
// way a working copy's does (ADR 0142 decision 2), so it reads the same list.
func SessionsToSettleUnder(dir string) ([]session.Meta, error) { return sessionsToSettleUnder(dir) }

// ShelveSessionsUnder is shelveSessionsUnder for the same caller: after the move, the sessions
// decided before it (before) and any found in dir since; stopped AI sessions go to the shelf, and a
// shell / ssm one that appeared since the trash step goes to the trash.
func ShelveSessionsUnder(dir string, before []session.Meta) { shelveSessionsUnder(dir, before) }
