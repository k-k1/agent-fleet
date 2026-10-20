package sessionx

// What a move reads from its source (ADR 0115 decisions 6 and 11.3): the conversation, the
// question and queues that never reached it, the pending peer spool, the working copy, the
// changed files and the open threads. Read with input closed, so nothing can be added to any of
// them while the move works; the preview reads the same without the gate.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// readMoveSource is the snapshot as a HandoverInput, without the fields only the move knows
// (Earlier, Instructions, Now). A variable so tests need no transcript on disk.
var readMoveSource = func(m session.Meta) HandoverInput {
	in := HandoverInput{Source: m}
	var edits []transcript.FileEdit
	if m.Kind == session.KindClaude {
		// The whole jsonl, as the export reads it: the mirror's collector keeps only a tail.
		lines, _, _ := claude.TranscriptRead(session.UUID(m.Dir, m.Name))
		in.Turns = claude.CollectAllTurns(lines)
		in.Queued = claude.CollectQueued(lines) // typed into the turn the limit cut off, never run

		edits = claude.CollectFileEdits(lines, 0)
	} else if td, ok := AgentOf(m.Kind).Transcript(m); ok {
		// td.Path is deliberately dropped: the file carries no path into a CLI store (decision 15).
		in.Turns, in.Pending = td.Turns, td.Pending
		in.Queued, in.QueuedItems, in.Discards = td.Queued, td.QueuedItems, td.Discards
		for _, t := range td.Turns {
			edits = append(edits, transcript.FileEditsInTurn(t)...)
		}
	}
	tagInjectedTurns(m.Name, in.Turns)
	in.ChangedFiles = foldAllFileEdits(m.Dir, edits)
	in.PeerMessages = agents.PendingPeers(m.Name)
	state, _ := moveSourceState(m)
	_, episode := RateLimitStates.Read(m.Name)
	in.CutOffByLimit = episode || state == agents.StateLimited || state == agents.StateSpendLimit
	in.Git = readMoveGit(m.Dir)
	in.Children = moveChildren(m.Name)
	in.Proposals = pendingProposalNames(m.Name)
	return in
}

// Bounds on the git reads: the move holds the source's input closed while it runs them, and the
// preview runs them on every open of the dialog. A huge untracked tree or a long diverged
// history must cost neither unbounded time nor unbounded memory; what is cut says so.
var (
	moveGitTimeout    = 10 * time.Second
	moveGitMaxBytes   = 64 << 10
	moveGitMaxCommits = 100
)

// readMoveGit reads the working copy as decision 6.3 lists it.
func readMoveGit(dir string) HandoverGit {
	if dir == "" || !gitx.IsGitRepo(dir) {
		return HandoverGit{}
	}
	status, err := boundedGit(dir, "status", "--porcelain")
	if err != nil {
		return HandoverGit{}
	}
	g := HandoverGit{Available: true, Status: status}
	// Against HEAD, so staged changes count too; an unborn branch has no HEAD and no diff.
	g.DiffStat, _ = boundedGit(dir, "diff", "--stat", "HEAD")
	ahead, err := boundedGit(dir, "log", "--oneline", "--no-decorate",
		"--max-count="+strconv.Itoa(moveGitMaxCommits+1), "@{upstream}..HEAD")
	switch {
	case err != nil:
		g.Ahead = "(the branch has no upstream)"
	case strings.Count(ahead, "\n")+1 > moveGitMaxCommits:
		g.Ahead = ahead[:strings.LastIndex(ahead, "\n")] +
			fmt.Sprintf("\n… (only the newest %d commits are shown)", moveGitMaxCommits)
	default:
		g.Ahead = ahead
	}
	return g
}

// boundedGit runs git in dir and returns its stdout without the trailing newline, reading at
// most moveGitMaxBytes and waiting at most moveGitTimeout; a cut output ends with a line saying
// so. Leading spaces are kept: the first porcelain status column can be one.
func boundedGit(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), moveGitTimeout)
	defer cancel()
	cmd := gitx.CmdContext(ctx, dir, args...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	b, _ := io.ReadAll(io.LimitReader(out, int64(moveGitMaxBytes)+1))
	truncated := len(b) > moveGitMaxBytes
	if truncated {
		_ = cmd.Process.Kill() // the rest is not wanted, and git may be far from done
		b = b[:moveGitMaxBytes]
		if i := bytes.LastIndexByte(b, '\n'); i >= 0 {
			b = b[:i]
		}
	}
	werr := cmd.Wait()
	s := strings.TrimRight(string(b), "\n")
	switch {
	case truncated:
		return s + fmt.Sprintf("\n… (cut at %d KiB; the rest is not shown)", moveGitMaxBytes>>10), nil
	case ctx.Err() != nil:
		if s == "" {
			return "", ctx.Err()
		}
		return s + fmt.Sprintf("\n… (git did not finish within %s; the rest is not shown)", moveGitTimeout), nil
	case werr != nil:
		return "", werr
	}
	return s, nil
}

// moveChildren lists the children the source started (ADR 0073) that are not archived, with
// their states. They stay the source's (decision 12); the file only names them.
func moveChildren(parent string) []HandoverChild {
	var out []HandoverChild
	for _, m := range session.ListMetas() {
		if m.Archived || m.OriginSession != parent || session.OriginOf(m) != session.OriginSession {
			continue
		}
		out = append(out, HandoverChild{Name: m.Name, State: DriveState(m, SessionAlive(m), false)})
	}
	slices.SortFunc(out, func(a, b HandoverChild) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// pendingProposalNames names the source's handoff proposals nobody launched yet, by title
// where there is one.
func pendingProposalNames(name string) []string {
	list, _ := ReadHandoffProposals(name)
	var out []string
	for _, p := range list {
		if p.LaunchedAt != 0 {
			continue
		}
		if t := strings.TrimSpace(p.Title); t != "" {
			out = append(out, t)
		} else {
			out = append(out, p.ID)
		}
	}
	return out
}

// projectInstructionFiles is what each kind loads by itself from the working folder, relative
// to it (decision 14). A kind is listed with the files it is known to read, and nothing it
// might: a file wrongly claimed for the target is one the target is never told to read, while
// a file wrongly left out only costs the target one extra read. opencode and muse read
// CLAUDE.md only when AGENTS.md is absent, so they are not credited with it.
var projectInstructionFiles = map[string][]string{
	session.KindClaude:   {"CLAUDE.md", "CLAUDE.local.md"},
	session.KindCodex:    {"AGENTS.md"},
	session.KindOpencode: {"AGENTS.md"},
	session.KindMuse:     {"AGENTS.md"},
	session.KindAgy:      {"AGENTS.md"},
	session.KindCopilot:  {"AGENTS.md", ".github/copilot-instructions.md"},
	session.KindCursor:   {"AGENTS.md", "CLAUDE.md", "CLAUDE.local.md", ".cursorrules", ".cursor/rules"},
	session.KindKiro:     {".kiro/steering"},
}

// instructionGap lists the files the source's kind loads from dir that the target's kind does
// not, among those that exist there.
func instructionGap(dir, sourceKind, targetKind string) []string {
	var out []string
	for _, f := range projectInstructionFiles[sourceKind] {
		if slices.Contains(projectInstructionFiles[targetKind], f) {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(f))); err == nil {
			out = append(out, f)
		}
	}
	return out
}
