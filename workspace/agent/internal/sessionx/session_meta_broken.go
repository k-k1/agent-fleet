package sessionx

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// brokenMetaSuffix is what a set-aside meta file gets after its name. It no longer ends in .json,
// so no walker of the sessions directory takes it (they all match *.json).
const brokenMetaSuffix = ".broken-"

// RuntimeProbe says whether an adoptable Managed runtime may be up. Only a definite "not running"
// may answer false.
type RuntimeProbe struct {
	Name         string
	MayBeRunning func() bool
}

// SetAsideBrokenMetas runs once at Agent start, before anything is started or any request is
// accepted. A meta file that exists but cannot be read keeps every retire from answering complete
// (it may be a child of the tree), and nothing else removes it. With no write in flight and no
// lease or launch holding a snapshot of it, the file is renamed in place to
// <name>.json.broken-<unix seconds> when no tmux session of that name exists: every reader then
// sees the session as absent. It is never deleted, so a person can repair it and rename it back.
// A file whose tmux session exists stays where it is: something runs that the Agent cannot
// describe, so it stays in doubt. Returns the new names.
//
// "Nothing has been started" does not mean "nothing runs": a Managed runtime the previous Agent
// started can outlive an Agent restart and be adopted again, with a turn still going and no tmux
// session. Its meta is unreadable here, so its kind is unknown. The rule: runtimes lists a probe
// for EVERY runtime the Agent adopts or reattaches to after a restart (today opencode serve and
// the codex app-server; the other Managed kinds are the Agent's own stdio children or have no
// process), and the whole set-aside is skipped when any may be up. A new adoptable runtime gets a
// probe here, or this rename can hide a running session from a retire.
func SetAsideBrokenMetas(runtimes ...RuntimeProbe) []string {
	_, unreadable, err := session.ListMetasChecked()
	if err != nil {
		log.Printf("meta: the sessions directory could not be listed, no broken meta set aside: %v", err)
		return nil
	}
	if len(unreadable) == 0 {
		return nil
	}
	for _, rt := range runtimes {
		if rt.MayBeRunning() {
			log.Printf("meta: %d unreadable meta file(s) left in doubt: the %s runtime may be running from the previous Agent and one of them may be its session", len(unreadable), rt.Name)
			return nil
		}
	}
	var moved []string
	for _, name := range unreadable {
		switch alive, err := tmuxSessionAlive(name); {
		case err != nil:
			log.Printf("meta: %s.json cannot be read and tmux could not say whether its session runs; left in place: %v", name, err)
			continue
		case alive:
			log.Printf("meta: %s.json cannot be read but its tmux session is running; left in place, the session stays in doubt", name)
			continue
		}
		if _, st := session.ReadMetaState(name); st != session.MetaUnreadable {
			continue // repaired or removed since the listing
		}
		to := brokenMetaName(name)
		if to == "" {
			continue
		}
		if err := os.Rename(session.MetaPath(name), to); err != nil {
			log.Printf("meta: %s.json cannot be read and could not be set aside: %v", name, err)
			continue
		}
		log.Printf("meta: %s.json cannot be read; set aside as %s (rename it back after repairing it)", name, strings.TrimPrefix(to, session.MetaDir()+"/"))
		moved = append(moved, to)
	}
	return moved
}

// tmuxSessionAlive tells a session that is not there (tmux exits 1) from a probe that failed
// (a timeout, tmux missing, any other exit): only the first lets the caller act on "absent".
func tmuxSessionAlive(name string) (bool, error) {
	err := tmuxx.Run("has-session", "-t", session.ExactTarget(session.TmuxName(name)))
	var ee *exec.ExitError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &ee) && ee.ExitCode() == 1:
		return false, nil
	}
	return false, err
}

// brokenMetaName is a free path for the set-aside file; "" when none could be found.
func brokenMetaName(name string) string {
	base := fmt.Sprintf("%s%s%d", session.MetaPath(name), brokenMetaSuffix, time.Now().Unix())
	for i := 0; i < 100; i++ {
		p := base
		if i > 0 {
			p = fmt.Sprintf("%s-%d", base, i)
		}
		if _, err := os.Lstat(p); errors.Is(err, fs.ErrNotExist) {
			return p
		}
	}
	return ""
}
