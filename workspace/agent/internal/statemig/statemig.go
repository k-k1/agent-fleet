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
	"log"
	"os"
	"path/filepath"
	"strings"
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
	"plan-file",
	"plan-review",
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
	// lcpp's own ClientMessageID ledger (agents.NewMsgLedger, driver.go) — a different store
	// from "lcpp" above (that one is the transcript, keyed by sid; this one is the resend/
	// idempotency ledger, keyed by session name, same shape as the other *-msgledger entries).
	// Never existed under .config — introduced directly under AgentStateDir.
	"lcpp-msgledger",
	// muse's own ClientMessageID ledger, same shape as the other *-msgledger entries, and the
	// map from a slot to the Muse Code session it opened (id plus the transcript path
	// session/start reported). Both were introduced directly under AgentStateDir and never
	// existed under .config.
	"muse-msgledger",
	"muse-sessions",
	// AF's own mirror of each muse conversation, written from the live item stream
	// (transcript.go explains why muse's own at-rest file cannot be read instead).
	"muse-transcripts",
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
	// The fleet session graph's ledgers (ADR 0096). Never existed under .config —
	// introduced after the move — but every AgentStateDir-resolving name is required to be
	// listed here (statemig_drift_test.go): run() no-ops on a source that was never there.
	"fleet-graph",
	// The lcpp kind's own transcript store (docs/log/99's re-examination of ADR 0093 decision
	// 3), keyed by sid like the rest of this list. Also never existed under .config — it was
	// introduced under AgentDataDir and only just moved onto this side.
	"lcpp",
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
	Skipped int // files deliberately left on the old volume (entrySkipped)
	// SkippedPaths are those files, for the boot log. They are paths, not contents, and a
	// path is not a secret — what makes them worth naming is that the two reasons for
	// skipping have very different consequences for the user (a live socket costs nothing; a
	// credential left behind can cost a re-login), and a count cannot tell them apart.
	SkippedPaths []string
	Bytes        int64
	Took         time.Duration
	Errs         []error
}

// Run performs the migration. It is safe to call on every boot: with nothing left under
// .config/agent-fleet it costs one failed stat per entry plus one read of the marker.
//
// Errors are collected, never fatal. A store that could not be moved is a store the user
// lost the history of; a boot that refused to start over it would cost them the whole
// Workspace.
func Run() Result {
	return run(paths.AgentConfigDir(), paths.AgentStateDir(), true)
}

// RunQuiet is Run without the "nothing is served until it is done" line, for a caller that is
// not the Agent's boot — a user's af-db invocation blocks only itself.
func RunQuiet() Result {
	return run(paths.AgentConfigDir(), paths.AgentStateDir(), false)
}

func run(src, dst string, announce bool) Result {
	start := time.Now()
	var res Result
	if src == dst {
		return res
	}
	m := readMarker(dst)
	announced := false
	for _, name := range Entries {
		if _, done := m.Done[name]; done {
			continue
		}
		from := filepath.Join(src, name)
		if _, err := os.Lstat(from); err != nil {
			continue // never existed, or already fully moved
		}
		if announce && !announced {
			// Said BEFORE the copying, because this is the one boot where it is slow: the
			// whole tree measured 113 MB / 5,400 files on EFS, and nothing is served until
			// it is here. Without this line that is an unexplained minute of "starting".
			log.Printf("state: migrating agent state out of %s (first boot after the move; "+
				"nothing is served until it is done)", src)
			announced = true
		}
		n, b, skippedPaths, errs := copyTree(from, filepath.Join(dst, name))
		res.Files += n
		res.Bytes += b
		res.Skipped += len(skippedPaths)
		res.SkippedPaths = append(res.SkippedPaths, skippedPaths...)
		res.Errs = append(res.Errs, errs...)
		if len(errs) > 0 || len(skippedPaths) > 0 {
			// Not done. Errors leave the entry for the next boot to finish; a skip leaves it
			// for good, and both take the same branch because RemoveAll below does not know
			// the difference — it would delete the very files copyEntry declined to move.
			continue
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
// source file it has accounted for. Returns the number of files written, their total size,
// and WHICH files were deliberately left behind (see entrySkipped) — a caller that gets any
// of those must not mark the entry finished, or the leftovers become permanent.
func copyTree(from, to string) (files int, bytes int64, skipped []string, errs []error) {
	// Only the chat scratch borrows credentials through links, so only there does a regular
	// file under one of those names mean "a refresh replaced the link". Scoping it keeps an
	// unrelated auth.json somewhere else from silently pinning its whole entry as unfinished.
	chatScratch := strings.HasPrefix(filepath.Base(from), "chat-")
	var emptyCandidates []string
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
			emptyCandidates = append(emptyCandidates, p)
			return nil
		}
		res, n, err := copyEntry(p, target, d, chatScratch)
		if err != nil {
			errs = append(errs, err)
			return nil
		}
		if res == entrySkipped {
			// Not ours to move, so not ours to delete either. The two reasons cost the
			// user very different things — a live socket, nothing; a rotated credential,
			// possibly a fresh sign-in (copyEntry) — which is why the caller logs the
			// paths rather than a count.
			skipped = append(skipped, p)
			return nil
		}
		if res == entryCopied {
			files++
			bytes += n
		}
		// Removed for entryAlreadyThere too: the destination is the truth, so the source is
		// stale and keeping it would resurrect it later.
		if err := os.Remove(p); err != nil {
			errs = append(errs, err)
		}
		return nil
	})
	if err != nil {
		errs = append(errs, err)
	}
	// An entry that skipped something is never marked finished, so RemoveAll never runs on it
	// and its directory tree stays on the old volume — where every later boot walks it again.
	// Pruning what is now empty (deepest first, and only what is empty) leaves just the files
	// that were deliberately kept, which is the smallest thing that can still be walked.
	for i := len(emptyCandidates) - 1; i >= 0; i-- {
		_ = os.Remove(emptyCandidates[i]) // fails, harmlessly, when anything is still inside
	}
	return files, bytes, skipped, errs
}

// entryResult says what happened to one file, and — through copyTree — whether the source
// may be removed. The three outcomes are NOT interchangeable: the first two mean the file is
// accounted for at the destination and the source is now redundant, the third means we chose
// not to touch it and deleting it would be data loss.
type entryResult int

const (
	entryCopied       entryResult = iota // written at the destination
	entryAlreadyThere                    // the destination already had it (rule 1)
	entrySkipped                         // deliberately left where it is
)

// borrowedCredentialNames are the files the chat scratch directories normally hold as
// SYMLINKS into the volume that really owns the credential (chat-claude/.credentials.json →
// the claude config mount, chat-codex/auth.json → ~/.codex/auth.json, agy's OAuth token under
// chat-wd/agy-*/home/.gemini/…).
//
// They are listed because a link is not all they can be: the CLIs rewrite these files with
// tmp+rename on a token refresh, which REPLACES THE LINK WITH A REAL FILE — that is why
// reconcileChatCreds (chat_providers.go) exists at all. Migrating one in that state would
// write the plaintext token onto the home volume, so a regular file under one of these names
// is left exactly where it is; the next chat turn's reconcile puts the link back.
var borrowedCredentialNames = map[string]bool{
	"auth.json":               true,
	".credentials.json":       true,
	"antigravity-oauth-token": true,
}

// copyEntry writes one file or symlink, and never follows a link.
//
// 🔴 A SYMLINK IS COPIED AS A SYMLINK, for the reason on borrowedCredentialNames above: the
// chat scratch borrows real credentials through links, and following one writes the plaintext
// onto the home volume — the one thing ADR 0045 decision 3-6 forbids.
func copyEntry(from, to string, d fs.DirEntry, chatScratch bool) (entryResult, int64, error) {
	if _, err := os.Lstat(to); err == nil {
		return entryAlreadyThere, 0, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return entrySkipped, 0, err
	}
	if d.Type()&fs.ModeSymlink != 0 {
		dest, err := os.Readlink(from)
		if err != nil {
			return entrySkipped, 0, err
		}
		return entryCopied, 0, os.Symlink(dest, to)
	}
	if !d.Type().IsRegular() {
		// A socket, fifo or device. There is nothing to carry over — and, unlike the case
		// above, nothing to delete either: a unix socket under a migrated tree belongs to a
		// process that is running right now, and removing it takes its listener away.
		return entrySkipped, 0, nil
	}
	if chatScratch && borrowedCredentialNames[d.Name()] {
		// A link that a token refresh turned into a real file. Left where it is — and the
		// caller logs it, because leaving it has a cost the user can see: the next chat turn
		// re-links the NEW path from the shared credential, so the rotation recorded here is
		// not folded back and a provider that retires used refresh tokens will ask for a
		// fresh login. Copying it instead would put the plaintext on the home volume, which
		// is the thing ADR 0045 decision 3-6 exists to prevent.
		return entrySkipped, 0, nil
	}
	fi, err := d.Info()
	if err != nil {
		return entrySkipped, 0, err
	}
	in, err := os.Open(from)
	if err != nil {
		return entrySkipped, 0, err
	}
	defer in.Close()
	// O_EXCL, so two agents racing on the same tree cannot half-write the same file.
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fi.Mode().Perm())
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return entryAlreadyThere, 0, nil
		}
		return entrySkipped, 0, err
	}
	n, err := io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(to) // a partial file at the destination would become "the truth"
		return entrySkipped, 0, err
	}
	return entryCopied, n, nil
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
// need a lock. That is out of proportion for what can collide: two Agents cannot, because the
// boot takes its listening socket before it migrates and a second one dies on the bind
// (main.go), so the only other caller is the af-db subcommand, which a user can start at any
// moment from a terminal. Two of those at once cost at most a marker entry — the copying
// itself stays correct through O_EXCL and "the destination is the truth", and a lost entry
// only means the next boot re-checks that entry and finds nothing to do. The loser of such a
// race also logs an ENOENT from its own os.Remove, which is noise rather than damage.
//
// The tmp+rename is the part that has to hold: a torn marker reads as "nothing is done" and
// migrates everything a second time.
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
