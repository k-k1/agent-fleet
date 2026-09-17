package session

import (
	"path/filepath"
	"sync"
)

// A sid is UUIDv5(dir|name), so it cannot be turned back into the directory it was made
// from. Things that only ever hold the sid — the transcript lookups in
// internal/agents/claude, above all — therefore cannot tell which of claude's project
// directories a session's log is in, and have to search for it (ADR 0087 decision 5).
//
// This is the lookup table that answers it: every meta read or written records what its cwd
// was. That costs nothing extra, because the readers are already reading the meta — the
// session-list poll walks all of them every few seconds — and it keeps the knowledge in the
// one package that owns Meta rather than making claude re-read the ledger.
//
// It is a HINT, deliberately: unknown is an ordinary answer (an Agent that has just started
// and not listed anything yet, a sid belonging to a deleted session), and every caller must
// have a path for it. Nothing here decides anything on its own.
var (
	cwdMu    sync.RWMutex
	cwdBySID map[string]cwdHint
)

// cwdHint keeps the two fields rather than the resolved cwd: Meta.CWD() stats the subdir to
// check it still exists, and doing that on every meta read would put back a slice of the
// syscalls this ADR is removing.
type cwdHint struct{ dir, subdir string }

func rememberCWD(m Meta) {
	if m.Dir == "" || m.Name == "" {
		return
	}
	sid := UUID(m.Dir, m.Name)
	cwdMu.Lock()
	if cwdBySID == nil {
		cwdBySID = map[string]cwdHint{}
	}
	cwdBySID[sid] = cwdHint{dir: m.Dir, subdir: m.Subdir}
	cwdMu.Unlock()
}

// CWDForUUID returns the working directory the session with this sid was launched in, or ""
// when no meta carrying it has been read in this process yet.
func CWDForUUID(sid string) string {
	cwdMu.RLock()
	h, ok := cwdBySID[sid]
	cwdMu.RUnlock()
	if !ok {
		return ""
	}
	return Meta{Dir: h.dir, Subdir: h.subdir}.CWD()
}

// CWDCandidatesForUUID returns EVERY directory this session can have been launched in, most
// recent resolution first. Nil when the sid is unknown.
//
// There are at most two, and which one CWD() answers with depends on the disk: with a Subdir
// set it resolves to Dir/Subdir only while that directory EXISTS, and falls back to Dir when
// it does not (a branch switch that removed the folder). A session that lived through such a
// switch therefore has claude state under both names, and a caller that asks only for today's
// answer will not find yesterday's.
//
// That matters to anything concluding something is ABSENT: one cwd resolving to nothing is
// not the same as the session having nothing (internal/agents/claude/bg.go relies on this).
func CWDCandidatesForUUID(sid string) []string {
	cwdMu.RLock()
	h, ok := cwdBySID[sid]
	cwdMu.RUnlock()
	if !ok {
		return nil
	}
	m := Meta{Dir: h.dir, Subdir: h.subdir}
	out := []string{m.CWD()}
	if m.Dir != "" && m.Dir != out[0] {
		out = append(out, m.Dir)
	}
	if h.subdir != "" {
		if sub := filepath.Join(m.Dir, filepath.FromSlash(h.subdir)); sub != out[0] {
			out = append(out, sub)
		}
	}
	return out
}
