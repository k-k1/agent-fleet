package cursor

import (
	"os"
	"path/filepath"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// cursor is an "imposed id" kind: we mint a v4 UUID, pass it with `--resume` to make a new chat,
// and read both transcript and state assuming it is still in use. If the CLI stops using that id
// the mirror silently stays empty, and cursor has no status hook to ask the id back from (see
// internal/agents/imposedsid.go). Disk is the only clue left.
//
// resolveSid only kicks in when the imposed id has no transcript: it re-picks the conversation
// for this cwd and replaces the ledger entry. Keep it out of the read hot path (ChatID, called by
// Transcript/LiveState).
func resolveSid(m session.Meta) string {
	return agents.ResolveImposedSID(sids, m, cliSessions)
}

// cliSessions enumerates cursor's own chats launched in dir.
//
// cursor stores transcripts at projects/<cwdSlug(dir)>/agent-transcripts/<chatID>/<chatID>.jsonl;
// the cwd is part of the path, so attribution needs nothing but a directory read.
//
// The transcript directory's mtime stands in for the creation time. cursor records no created_at,
// but this directory is touched once when the .jsonl inside it is created and never again on
// append (measured: over 10 local chats, dir mtime <= file mtime, and the busier the chat the
// wider the gap). So mtime is approximately the chat creation time and can be matched against the
// slot creation time.
func cliSessions(dir string) []agents.CLISession {
	root := filepath.Join(projectsDir(), cwdSlug(dir), "agent-transcripts")
	ents, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []agents.CLISession
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		id := e.Name()
		// A directory with no transcript file yet is not a conversation, just a shell made at startup.
		if _, err := os.Stat(filepath.Join(root, id, id+".jsonl")); err != nil {
			continue
		}
		s := agents.CLISession{ID: id}
		if fi, err := e.Info(); err == nil {
			s.Created = fi.ModTime()
		}
		out = append(out, s)
	}
	return out
}
