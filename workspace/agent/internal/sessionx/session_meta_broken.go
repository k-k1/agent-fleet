package sessionx

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// brokenMetaSuffix is what a set-aside meta file gets after its name. It no longer ends in .json,
// so no walker of the sessions directory takes it (they all match *.json).
const brokenMetaSuffix = ".broken-"

// SetAsideBrokenMetas runs once at Agent start, before anything is started or any request is
// accepted. A meta file that exists but cannot be read keeps every retire from answering complete
// (it may be a child of the tree), and nothing else removes it. With no write in flight and no
// lease or launch holding a snapshot of it, the file is renamed in place to
// <name>.json.broken-<unix seconds> when no tmux session of that name exists: every reader then
// sees the session as absent. It is never deleted, so a person can repair it and rename it back.
// A file whose tmux session exists stays where it is: something runs that the Agent cannot
// describe, so it stays in doubt. Returns the new names.
func SetAsideBrokenMetas() []string {
	_, unreadable, err := session.ListMetasChecked()
	if err != nil {
		log.Printf("meta: the sessions directory could not be listed, no broken meta set aside: %v", err)
		return nil
	}
	var moved []string
	for _, name := range unreadable {
		if tmuxx.HasSession(session.TmuxName(name)) {
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
