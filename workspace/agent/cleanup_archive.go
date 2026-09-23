package main

// Cleanup archive — a gz safety net for destructive tidy-up (docs/log/32). When a session
// or branch is deleted to reclaim space, what would truly be LOST is bundled first into
// a compressed archive under ~/.local/share/agent-fleet/cleanup/ (persists across
// container recreate), so the removal is recoverable:
//   - session: its meta + transcript jsonl(s) — the conversation, otherwise gone.
//   - branch:  its name + tip SHA — a merged branch's commits already live in the
//              target's object store, so recording the ref is enough to recreate it.
// A worktree's working files are reconstructable from git (delete_worktree refuses
// dirty/ahead), so they are NOT archived — only the sessions/branch tied to it are.
//
// Each archive is a self-contained <id>.tar.gz (manifest.json + jsonl files inside),
// with a sidecar <id>.json manifest for cheap listing without extracting.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
)

// cleanupStoreDir is sessionx's CleanupArchiveDir: the cache orphan scan reads the same
// directory to learn which sessions a restore could bring back, and one definition keeps
// the two from drifting apart.
func cleanupStoreDir() string { return sessionx.CleanupArchiveDir() }

// cleanupArchivedSession is one session captured in an archive: enough to restore the
// listed row (meta) and the conversation (jsonl payloads, stored as tar entries).
type cleanupArchivedSession struct {
	Name       string   `json:"name"`
	Display    string   `json:"display"`
	Kind       string   `json:"kind"`
	Meta       string   `json:"meta"`                 // marshaled session.Meta, replayed on restore
	JSONLPaths []string `json:"jsonlPaths,omitempty"` // original absolute paths (restore targets)
	JSONLNames []string `json:"jsonlNames,omitempty"` // tar entry names, index-aligned with JSONLPaths
}

// cleanupArchivedBranch is a deleted branch's coordinates. The commits survive in the
// repo's object store (merged branches only), so name+SHA recreate the ref.
type cleanupArchivedBranch struct {
	Repo string `json:"repo"`
	Name string `json:"name"`
	SHA  string `json:"sha"`
}

type cleanupManifest struct {
	ID        string                   `json:"id"`
	At        string                   `json:"at"` // RFC3339
	Reason    string                   `json:"reason,omitempty"`
	Sessions  []cleanupArchivedSession `json:"sessions,omitempty"`
	Branches  []cleanupArchivedBranch  `json:"branches,omitempty"`
	Worktrees []string                 `json:"worktrees,omitempty"` // names removed (informational)
}

// newCleanupID builds a sortable, unique archive id. now is passed in (never
// time.Now() inside) so tests are deterministic.
func newCleanupID(now time.Time, slug string) string {
	return now.UTC().Format("20060102-150405") + "-" + slug
}

// writeCleanupArchive persists the manifest + jsonl payloads as <id>.tar.gz plus a
// sidecar <id>.json. payloads maps a tar entry name → bytes.
func writeCleanupArchive(m cleanupManifest, payloads map[string][]byte) error {
	dir := cleanupStoreDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	mj, _ := json.MarshalIndent(m, "", "  ")
	if err := tarAdd(tw, "manifest.json", mj); err != nil {
		return err
	}
	// Deterministic entry order (tests + reproducibility).
	names := make([]string, 0, len(payloads))
	for n := range payloads {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := tarAdd(tw, n, payloads[n]); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gw.Close(); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, m.ID+".tar.gz"), buf.Bytes(), 0o600); err != nil {
		return err
	}
	// Sidecar manifest for listing; best-effort (the tar.gz is the source of truth).
	_ = os.WriteFile(filepath.Join(dir, m.ID+".json"), mj, 0o600)
	return nil
}

func tarAdd(tw *tar.Writer, name string, b []byte) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(b))}); err != nil {
		return err
	}
	_, err := tw.Write(b)
	return err
}

// listCleanupArchives returns stored manifests, newest first (id is time-sortable).
func listCleanupArchives() []cleanupManifest {
	entries, _ := os.ReadDir(cleanupStoreDir())
	var out []cleanupManifest
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(cleanupStoreDir(), e.Name()))
		if err != nil {
			continue
		}
		var m cleanupManifest
		if json.Unmarshal(b, &m) == nil && m.ID != "" {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out
}

// readCleanupArchive loads a manifest and the tar payloads for restore.
func readCleanupArchive(id string) (cleanupManifest, map[string][]byte, error) {
	var m cleanupManifest
	if filepath.Base(id) != id || strings.Contains(id, "..") {
		return m, nil, fmt.Errorf("invalid archive id")
	}
	b, err := os.ReadFile(filepath.Join(cleanupStoreDir(), id+".tar.gz"))
	if err != nil {
		return m, nil, err
	}
	gr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return m, nil, err
	}
	defer gr.Close()
	payloads := map[string][]byte{}
	tr := tar.NewReader(gr)
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		// Cap the per-entry allocation: h.Size comes straight from the (possibly
		// corrupt) tar header, and a huge value would OOM this memory-constrained host.
		const maxCleanupEntryBytes = 64 << 20
		if h.Size < 0 || h.Size > maxCleanupEntryBytes {
			return m, nil, fmt.Errorf("archive %s: entry %s too large (%d bytes)", id, h.Name, h.Size)
		}
		data := make([]byte, h.Size)
		_, _ = io.ReadFull(tr, data)
		if h.Name == "manifest.json" {
			_ = json.Unmarshal(data, &m)
		} else {
			payloads[h.Name] = data
		}
	}
	if m.ID == "" {
		return m, nil, fmt.Errorf("archive %s has no manifest", id)
	}
	return m, payloads, nil
}

// purgeCleanupArchive permanently removes an archive (reclaims its space for good).
// errRestoreIncomplete refuses a purge while a restore of the same archive has not finished:
// transcripts it placed may already point at the session's cache, and with neither the meta
// nor the archive left, the cache scan would take that cache as orphaned.
var errRestoreIncomplete = errors.New("a restore of this archive did not finish; restore it again first")

// restoringMarker is the file that says a restore of archive id started and has not finished.
// Written before anything is placed and removed only when every session is back.
func restoringMarker(id string) string { return filepath.Join(cleanupStoreDir(), id+".restoring") }

func purgeCleanupArchive(id string) error {
	if filepath.Base(id) != id || strings.Contains(id, "..") {
		return fmt.Errorf("invalid archive id")
	}
	if _, err := os.Lstat(restoringMarker(id)); err == nil {
		return errRestoreIncomplete
	}
	_ = os.Remove(filepath.Join(cleanupStoreDir(), id+".json"))
	return os.Remove(filepath.Join(cleanupStoreDir(), id+".tar.gz"))
}

// restoreCleanupArchive replays an archive: re-create each branch ref (name→sha, if
// absent) and each session (meta + jsonl written back). Returns per-item outcomes.
// stageNextTo writes data to a temporary file in dest's directory — the same filesystem, so
// placing it is a link or a rename, never a copy.
func stageNextTo(dest string, data []byte) (string, error) {
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".restore-*")
	if err != nil {
		return "", err
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmp.Name())
		return "", errors.Join(werr, cerr)
	}
	return tmp.Name(), nil
}

// linkFile is os.Link; a var only so a test can make it fail.
var linkFile = os.Link

// placeNoReplace puts tmp at dest unless something is already there, in which case that
// file wins. A hard link is the atomic "create only if absent". There is deliberately no
// fallback: a rename would silently replace a transcript that appeared after the last check,
// so where a link cannot be made the restore fails and says why (EFS and local disks have
// hard links).
func placeNoReplace(tmp, dest string) error {
	err := linkFile(tmp, dest)
	if err == nil || errors.Is(err, fs.ErrExist) {
		return nil
	}
	return err
}

// restoreAfterStage, when set, runs after a restore has read its archive and staged the
// transcripts and before it takes the cleanup lock. Tests only: it is the point a purge has to
// win at to exercise the race.
var restoreAfterStage func()

func restoreCleanupArchive(id string) (map[string]any, error) {
	m, payloads, err := readCleanupArchive(id)
	if err != nil {
		return nil, err
	}
	restored := map[string]any{"sessions": []string{}, "branches": []string{}}
	var sessions, branches []string
	var metas []session.Meta
	// Transcripts are staged next to their destination and only placed under the cleanup lock,
	// together with the metas. A restore never removes or replaces anything it did not create
	// in this call, and it never reports a session as back unless its meta was written. When a
	// step fails it stops there and says so: transcripts it already placed stay (a transcript
	// with no meta is inert), and running the restore again finishes the job — a transcript
	// already at its path is kept, and the metas are simply written again. Undoing a placed
	// transcript instead would mean deleting a file some other process may already be
	// appending to.
	type stagedFile struct{ tmp, dest string }
	var staged []stagedFile
	dropStaged := func() {
		for _, f := range staged {
			_ = os.Remove(f.tmp)
		}
	}
	for _, s := range m.Sessions {
		var meta session.Meta
		if json.Unmarshal([]byte(s.Meta), &meta) != nil || meta.Name == "" {
			continue
		}
		for i, name := range s.JSONLNames {
			if i >= len(s.JSONLPaths) {
				break
			}
			data, ok := payloads[name]
			if !ok {
				continue
			}
			dest := s.JSONLPaths[i]
			// A transcript already at the path is the live one — this archive restored once
			// before and the session has moved on since, or the agent rewrote it. It is at least
			// as new as the archived copy, so it is kept rather than rolled back.
			if _, err := os.Lstat(dest); err == nil {
				continue
			}
			tmp, err := stageNextTo(dest, data)
			if err != nil {
				dropStaged()
				return nil, fmt.Errorf("archive %s: cannot stage %s: %w", id, dest, err)
			}
			staged = append(staged, stagedFile{tmp: tmp, dest: dest})
		}
		metas = append(metas, meta)
		sessions = append(sessions, s.Name)
	}
	if restoreAfterStage != nil {
		restoreAfterStage()
	}
	// Only the hand-over is under the cleanup lock: from here on the meta, not the archive,
	// keeps these sessions' cache reachable. Reading the archive and staging the transcripts
	// above can take a while on a large archive, and holding the lock through them would stall
	// the cleanup survey and the Machine tab behind it. So the archive is checked again here —
	// a purge that won the race means a cache delete may already have run, and bringing the
	// conversation back without its files is the one outcome this lock exists to prevent.
	var handErr error
	sessionx.WithCleanupLock(func() {
		if _, err := os.Stat(filepath.Join(cleanupStoreDir(), id+".tar.gz")); err != nil {
			handErr = fmt.Errorf("archive %s was purged while it was being restored", id)
			return
		}
		// Mark the restore as under way before anything is placed. Until it is removed below,
		// the archive cannot be purged, so whatever this restore leaves half done stays
		// reachable through the archive.
		if err := os.WriteFile(restoringMarker(id), nil, 0o600); err != nil {
			handErr = fmt.Errorf("archive %s: cannot mark the restore: %w", id, err)
			return
		}
		for _, f := range staged {
			if err := placeNoReplace(f.tmp, f.dest); err != nil {
				handErr = fmt.Errorf("archive %s: cannot place %s (no session was restored; restoring again is safe): %w", id, f.dest, err)
				return
			}
		}
		for i, meta := range metas {
			// An existing meta is newer than the archived snapshot — a session already
			// restored and then locked, renamed or run — and is kept as it is.
			if _, err := session.CreateMetaIfAbsent(meta); err != nil {
				handErr = fmt.Errorf("archive %s: restored %d of %d sessions, then could not write %s (restoring again is safe): %w",
					id, i, len(metas), meta.Name, err)
				return
			}
		}
		_ = os.Remove(restoringMarker(id))
	})
	dropStaged() // placed ones are hard links, so the staging names are only temporary
	if handErr != nil {
		return nil, handErr
	}
	for _, b := range m.Branches {
		dir, ok := gitx.ResolveRepoDir(b.Repo)
		if !ok || b.SHA == "" {
			continue
		}
		// Only create if absent; ignore errors (e.g. SHA gone after a GC).
		if !gitx.GitBranchExists(dir, b.Name) {
			if gitx.GitCreateBranch(dir, b.Name, b.SHA) {
				branches = append(branches, b.Name)
			}
		}
	}
	restored["sessions"] = sessions
	restored["branches"] = branches
	return restored, nil
}
