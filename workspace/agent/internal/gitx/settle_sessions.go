package gitx

import "github.com/k-k1/agent-fleet/workspace/agent/internal/session"

// SessionsToSettleUnder is sessionsToSettleUnder for a delete that is not a working copy's: the
// stopped, unlocked, unshelved sessions living in dir. A space's delete settles its sessions the
// way a working copy's does (ADR 0142 decision 2), so it reads the same list.
func SessionsToSettleUnder(dir string) ([]session.Meta, error) { return sessionsToSettleUnder(dir) }

// ShelveSessionsUnder is shelveSessionsUnder for the same caller: after the move, the stopped
// sessions still living in dir (settleAfterRemoval; before is what the delete decided, and
// onlyDecided keeps it to them) go to the shelf, and a shell / ssm one to the trash.
func ShelveSessionsUnder(dir string, before []session.Meta, onlyDecided bool) {
	settleAfterRemoval(dir, before, onlyDecided)
}
