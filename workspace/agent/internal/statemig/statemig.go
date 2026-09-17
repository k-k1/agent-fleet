// Package statemig moves the agent's mutable state out of ~/.config/agent-fleet and into
// ~/.local/state/agent-fleet, once, on the first boot of a build that reads the new location
// (ADR 0087 decision 4 — the two roots and the line between them are documented on
// paths.AgentConfigDir / paths.AgentStateDir).
//
// Without it the move is not a relocation but a reset: the session ledger is the Console's
// session list, so every session would vanish from the list the moment the new build starts,
// and the chat working directories would be orphaned in a tree nothing reads any more.
//
// Three rules decide what happens when it is interrupted — the Agent can be killed at any
// point, and an ECS task replacement during a rollout makes that ordinary rather than rare:
//
//  1. THE DESTINATION IS THE TRUTH. An entry already at the destination is never
//     overwritten. Only the new build writes there, so what is there is newer than the copy
//     under .config by construction — including a file a hook subprocess wrote while this
//     was running.
//  2. Per FILE, not per directory. A half-copied directory would otherwise be indis-
//     tinguishable from a finished one, and the next boot would skip the rest of it forever.
//     Copying file by file means an interrupted run simply has less left to do next time.
//  3. The source file is removed once its copy is on disk, and the entry is recorded in the
//     marker below once it is fully done. Both exist to stop a MIGRATED-AND-THEN-DELETED
//     entry from coming back: without them, deleting a session would drop its meta from the
//     state dir and the next boot would restore it from the leftover under .config.
//
// Leftovers under .config/agent-fleet are therefore expected and harmless — the next boot
// converges. What must never happen is the reverse, a stale copy overwriting a live file.
package statemig

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// Entries are the top-level names under ~/.config/agent-fleet that moved. It is an
// allowlist, not "everything except the credentials", and deliberately so: a name forgotten
// here leaves one store behind on the old volume, while a name forgotten in a denylist would
// carry a credential or a user's own configuration onto a volume the ADR says must not hold
// it. statemig_drift_test.go fails when a store resolves through paths.AgentStateDir and is
// not named here.
//
// `deploy` is the clearest reason the inverse would be wrong: it is written by
// deploy/aws/ecs/env.sh, outside the agent entirely, and moving it would break every script
// that reads `$HOME/.config/agent-fleet/deploy`.
var Entries = []string{
	// The session ledger, and everything keyed by a session name or sid.
	"sessions",
	"session-status",
	"session-exit",
	"session-turn-end",
	"session-injections",
	"session-auth-resume",
	"session-abort-resume",
	"session-managed-abort",
	"session-rate-limit",
	"session-report",
	"session-resume",
	"session-translations",
	"session-marks",
	"session-handoffs",
	"carried-interaction",
	"pending-question",
	"pending-plan",
	"pending-perm",
	"pending-text",
	// Per-agent id ledgers and message rings (agents.NewSidStore / NewMsgLedger).
	"claude-sid",
	"codex-sid",
	"opencode-sid",
	"copilot-sid",
	"cursor-sid",
	"kiro-sid",
	"agy-sid",
	"agy-prelaunch",
	"agy-brain-prelaunch",
	"codex-msgledger",
	"opencode-msgledger",
	"copilot-msgledger",
	"cursor-msgledger",
	"kiro-msgledger",
	// Chat bridge: the outbound queue and the per-provider binding ledgers.
	"bridge-queue",
	"bridge-approvals",
	"bridge-answers",
	"bridge-operator-turn",
	"bridge-operator.json",
	"bridge-operator-slack.json",
	"bridge-threads.json",
	"bridge-threads-slack.json",
	// Notifications, instruction ledger, browser handoffs.
	"notification-outbox",
	"notification-markers",
	"instr-ledger",
	"browser-handoff-ledger",
	// Per-boot / per-run state of the CLIs themselves.
	"mcp-output-cursor",
	"mcp-af-name",
	"agy-account.json",
	"af-db",
	// The chat runs' scratch: working directories and the private CLI homes. The bulk of
	// the bytes (measured: 94 MB of the 113 MB under .config/agent-fleet).
	"chat-wd",
	"chat-codex",
	"chat-claude",
}

// markerName records which entries are finished, so an entry whose source could not be
// removed is still not migrated a second time (rule 3). It lives at the destination: a
// marker on the source side would be lost with the source.
const markerName = ".migrated-from-config.json"

type marker struct {
	// Done maps a finished entry to when it finished, RFC3339. A map rather than a list so
	// the file merges cleanly with itself across interrupted runs.
	Done map[string]string `json:"done"`
}

// Result is what one Run did, for the boot log.
type Result struct {
	Entries int // entries finished this run
	Files   int // files and symlinks copied
	Bytes   int64
	Took    time.Duration
	Errs    []error
}

// Run performs the migration. It is safe to call on every boot: with nothing left under
// .config/agent-fleet it costs one failed stat per entry plus one read of the marker.
//
// Errors are collected, never fatal. A store that could not be moved is a store the user
// lost the history of; a boot that refused to start over it would cost them the whole
// Workspace.
func Run() Result {
	return run(paths.AgentConfigDir(), paths.AgentStateDir())
}

func run(src, dst string) Result {
	start := time.Now()
	var res Result
	if src == dst {
		return res
	}
	m := readMarker(dst)
	for _, name := range Entries {
		if _, done := m.Done[name]; done {
			continue
		}
		from := filepath.Join(src, name)
		if _, err := os.Lstat(from); err != nil {
			continue // never existed, or already fully moved
		}
		n, b, errs := copyTree(from, filepath.Join(dst, name))
		res.Files += n
		res.Bytes += b
		res.Errs = append(res.Errs, errs...)
		if len(errs) > 0 {
			continue // not done: leave it unmarked so the next boot finishes it
		}
		_ = os.RemoveAll(from)
		m.Done[name] = time.Now().UTC().Format(time.RFC3339)
		res.Entries++
	}
	if res.Entries > 0 {
		if err := writeMarker(dst, m); err != nil {
			res.Errs = append(res.Errs, err)
		}
	}
	res.Took = time.Since(start)
	return res
}

// copyTree copies from onto to, creating nothing that is already there, and removes each
// source file it has copied. Returns the number of files written and their total size.
func copyTree(from, to string) (files int, bytes int64, errs []error) {
	err := filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			errs = append(errs, err)
			return nil // a subtree we cannot read must not abort the rest
		}
		rel, err := filepath.Rel(from, p)
		if err != nil {
			errs = append(errs, err)
			return nil
		}
		target := filepath.Join(to, rel)
		if d.IsDir() {
			fi, err := d.Info()
			mode := fs.FileMode(0o700)
			if err == nil {
				mode = fi.Mode().Perm()
			}
			if err := os.MkdirAll(target, mode); err != nil {
				errs = append(errs, err)
				return fs.SkipDir
			}
			return nil
		}
		n, err := copyEntry(p, target, d)
		if err != nil {
			errs = append(errs, err)
			return nil
		}
		if n >= 0 {
			files++
			bytes += n
		}
		// Removed even when the destination already existed (n < 0): the destination is the
		// truth, so the source copy is stale and keeping it would resurrect it later.
		if err := os.Remove(p); err != nil {
			errs = append(errs, err)
		}
		return nil
	})
	if err != nil {
		errs = append(errs, err)
	}
	return files, bytes, errs
}

// copyEntry writes one file or symlink. It returns -1 when the destination already exists
// (rule 1: never overwrite), and never follows a link.
//
// 🔴 A SYMLINK IS COPIED AS A SYMLINK. The chat scratch directories borrow the real
// credentials through links — chat-claude/.credentials.json → the claude config mount,
// chat-codex/auth.json → ~/.codex/auth.json, and agy's OAuth token under chat-wd. Following
// them here would write plaintext copies of all three onto the home volume, which is the one
// thing ADR 0045 decision 3-6 forbids, and would leave the copy-back reconcile
// (reconcileChatCreds) folding a rotated token into a file nothing else reads.
func copyEntry(from, to string, d fs.DirEntry) (int64, error) {
	if _, err := os.Lstat(to); err == nil {
		return -1, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return 0, err
	}
	if d.Type()&fs.ModeSymlink != 0 {
		dest, err := os.Readlink(from)
		if err != nil {
			return 0, err
		}
		return 0, os.Symlink(dest, to)
	}
	if !d.Type().IsRegular() {
		return -1, nil // socket / fifo / device: nothing to carry over
	}
	fi, err := d.Info()
	if err != nil {
		return 0, err
	}
	in, err := os.Open(from)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	// O_EXCL, so two agents racing on the same tree cannot half-write the same file.
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fi.Mode().Perm())
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return -1, nil
		}
		return 0, err
	}
	n, err := io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(to) // a partial file at the destination would become "the truth"
		return 0, err
	}
	return n, nil
}

func readMarker(dst string) marker {
	m := marker{Done: map[string]string{}}
	b, err := os.ReadFile(filepath.Join(dst, markerName))
	if err != nil {
		return m
	}
	var got marker
	if json.Unmarshal(b, &got) != nil || got.Done == nil {
		return m
	}
	m.Done = got.Done
	return m
}

// writeMarker folds m into whatever is on disk and replaces the file atomically.
//
// It MERGES rather than overwrites because nothing serializes two agents against each other,
// and a marker written from a snapshot read minutes earlier would drop the entries another
// one finished in between. On its own that is not destructive — their sources are already
// gone, so the next boot's Lstat finds nothing and skips them — but the marker exists for the
// one case where source removal FAILED, and dropping an entry reopens exactly that case: the
// leftover under .config is migrated a second time, and something the user deleted in between
// comes back.
//
// Read-modify-write still has a window between the read and the rename, and closing it would
// need a lock. That is out of proportion here: the migration runs once per box, from one
// process, at a point where nothing else has started. The tmp+rename is what actually has to
// hold — a torn marker reads as "nothing is done" and re-migrates everything.
func writeMarker(dst string, m marker) error {
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return err
	}
	merged := readMarker(dst)
	for entry, at := range m.Done {
		merged.Done[entry] = at
	}
	b, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dst, markerName+".tmp")
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dst, markerName))
}
