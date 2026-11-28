package main

// Cleanup archive — a gz safety net for destructive tidy-up (docs/log/32). When a session
// or branch is deleted to reclaim space, what would truly be LOST is bundled first into
// a compressed archive under ~/.local/share/agent-fleet/cleanup/ (persists across
// container recreate), so the removal is recoverable:
//   - session: its meta + transcript jsonl(s) — the conversation, otherwise gone.
//   - branch:  its name + tip SHA — a merged branch's commits already live in the
//              target's object store, so recording the ref is enough to recreate it.
//   - worktree: a tombstone (path, branch, HEAD, a snapshot commit of uncommitted work) whose
//              commits a refs/af/deleted-worktrees/* ref pins against gc until the entry is
//              purged (worktree_tombstone.go). Deleting a worktree also moves its stopped AI
//              sessions to the shelf and its shell / ssm to this trash (ADR 0101 decision 4).
//
// Each archive is a self-contained <id>.tar.gz (manifest.json + jsonl files inside),
// with a sidecar <id>.json manifest for cheap listing without extracting.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/rmtree"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
)

// cleanupStoreDir is sessionx's CleanupArchiveDir: the cache orphan scan reads the same
// directory to learn which sessions a restore could bring back, and one definition keeps
// the two from drifting apart.
func cleanupStoreDir() string { return sessionx.CleanupArchiveDir() }

// cleanupArchivedSession is one session captured in an archive: enough to restore the
// listed row (meta) and the conversation (jsonl payloads, stored as tar entries). The same
// lists carry a move target's handover files (ADR 0115 decision 9): any payload with a
// destination restores the same way.
type cleanupArchivedSession struct {
	Name       string   `json:"name"`
	Display    string   `json:"display"`
	Kind       string   `json:"kind"`
	Meta       string   `json:"meta"`                 // marshaled session.Meta, replayed on restore
	JSONLPaths []string `json:"jsonlPaths,omitempty"` // original absolute paths (restore targets)
	JSONLNames []string `json:"jsonlNames,omitempty"` // tar entry names, index-aligned with JSONLPaths
	// History is the tar entry holding a trashed shell's terminal history (terminal_history.go),
	// kept only while the tenant's history retention is on. HistoryMod is the history file's
	// modification time (RFC3339): restore drops a history older than the retention and gives the
	// placed file this time, so the janitor ages it from when it was written, not from the restore.
	History    string `json:"history,omitempty"`
	HistoryMod string `json:"historyMod,omitempty"`
}

// cleanupArchivedBranch is a deleted branch's coordinates. The commits survive in the
// repo's object store (merged branches only), so name+SHA recreate the ref.
type cleanupArchivedBranch struct {
	Repo string `json:"repo"`
	Name string `json:"name"`
	SHA  string `json:"sha"`
}

type cleanupManifest struct {
	ID       string                   `json:"id"`
	At       string                   `json:"at"` // RFC3339
	Reason   string                   `json:"reason,omitempty"`
	Sessions []cleanupArchivedSession `json:"sessions,omitempty"`
	Branches []cleanupArchivedBranch  `json:"branches,omitempty"`
	Worktree *gitx.WorktreeTombstone  `json:"worktree,omitempty"`
	// Space records a deleted space (space_delete.go); its folder waits in the trash directory
	// as <id>.space.
	Space *spaceTombstone `json:"space,omitempty"`
	// Bytes is the size of the archive's tarball, filled in by listCleanupArchives for the
	// trash tab (what a purge reclaims). Never written into the archive itself.
	Bytes int64 `json:"bytes,omitempty"`
	// BytesPartial marks Bytes as a lower bound: the walk of a deleted space's folder ran out of
	// its shared entry budget. Never written into the archive itself.
	BytesPartial bool `json:"bytesPartial,omitempty"`
}

// newCleanupID builds a sortable, unique archive id. now is passed in (never
// time.Now() inside) so tests are deterministic.
func newCleanupID(now time.Time, slug string) string {
	return now.UTC().Format("20060102-150405") + "-" + slug
}

// writeCleanupArchive persists the manifest + jsonl payloads as <id>.tar.gz plus a
// sidecar <id>.json. payloads maps a tar entry name → bytes.
//
// It never overwrites an archive: the id is only second-precise, so two deletes of the same
// name in one second produced the same id and the second write replaced the first (and a
// cleanup of the loser could then purge the winner's only copy). The tarball is created with
// O_EXCL, and a taken id gets a -2, -3, … suffix — m.ID is updated to the id actually written.
func writeCleanupArchive(m *cleanupManifest, payloads map[string][]byte) error {
	return writeCleanupBundle(m, payloads, false)
}

// pendingTrash protects session bundles between preparation and the last mutation under
// their name. Keep the sidecar on disk so an interrupted Agent still leaves a recoverable row.
var pendingTrash sync.Map

var errTrashPending = errors.New("a trash or restore operation for this entry is still in progress. Try again in a moment")

func trashPending(id string) bool {
	_, ok := pendingTrash.Load(filepath.Join(cleanupStoreDir(), id))
	return ok
}

func releaseTrash(id string) { pendingTrash.Delete(filepath.Join(cleanupStoreDir(), id)) }

func writeCleanupBundle(m *cleanupManifest, payloads map[string][]byte, pending bool) error {
	dir := cleanupStoreDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	return writeCleanupBundleIn(root, m, payloads, pending)
}

func writeCleanupBundleIn(root *os.Root, m *cleanupManifest, payloads map[string][]byte, pending bool) error {
	base := m.ID
	var f *os.File
	for n := 1; ; n++ {
		if n > 1 {
			m.ID = fmt.Sprintf("%s-%d", base, n)
		}
		var err error
		open := func() {
			f, err = root.OpenFile(m.ID+".tar.gz", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err == nil && pending {
				pendingTrash.Store(filepath.Join(root.Name(), m.ID), true)
			}
		}
		if pending {
			sessionx.WithCleanupLock(open)
		} else {
			open()
		}
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrExist) || n >= 100 {
			return err
		}
	}
	ok := false
	defer func() {
		if !ok {
			_ = f.Close()
			_ = root.Remove(m.ID + ".tar.gz")
			if pending {
				releaseTrash(m.ID)
			}
		}
	}()
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
	if _, err := f.Write(buf.Bytes()); err != nil {
		return err
	}
	if pending {
		if err := f.Sync(); err != nil {
			return err
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	if pending {
		sidecar, err := root.OpenFile(m.ID+".json", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		_, werr := sidecar.Write(mj)
		if err := errors.Join(werr, sidecar.Close()); err != nil {
			_ = root.Remove(m.ID + ".json")
			return err
		}
		ok = true
		return nil
	}
	ok = true
	// Sidecar manifest for listing; best-effort (the tar.gz is the source of truth).
	_ = writeArchiveFile(root, m.ID+".json", mj)
	return nil
}

func tarAdd(tw *tar.Writer, name string, b []byte) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(b))}); err != nil {
		return err
	}
	_, err := tw.Write(b)
	return err
}

// listCleanupArchives returns stored manifests, newest first (id is time-sortable). Bytes is the
// tarball's size only: measuring a deleted space's folder walks it, which callers that only read
// manifests must not pay for.
func listCleanupArchives() []cleanupManifest { return listArchives(nil, true) }

// listCleanupArchivesSized is listCleanupArchives with each deleted space's folder counted into
// Bytes (what a purge gives back), the whole list sharing one walk budget so a trash full of
// large spaces is bounded as a whole.
func listCleanupArchivesSized() []cleanupManifest {
	budget := cleanupUsageMaxEntries
	return listArchives(&budget, false)
}

func listArchives(budget *int, includePending bool) []cleanupManifest {
	root, err := os.OpenRoot(cleanupStoreDir())
	if err != nil {
		return nil
	}
	defer root.Close()
	return listArchivesIn(root, budget, includePending)
}

func listArchivesIn(root *os.Root, budget *int, includePending bool) []cleanupManifest {
	entries, _ := fs.ReadDir(root.FS(), ".")
	var out []cleanupManifest
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		if !validArchiveID(id) || (!includePending && trashPending(id)) {
			continue
		}
		m, err := readArchiveSidecar(root, id)
		if err != nil {
			continue
		}
		if info, err := root.Lstat(id + ".tar.gz"); err == nil && info.Mode().IsRegular() {
			m.Bytes = info.Size()
		}
		if m.Space != nil && budget != nil {
			name := spaceTrashName(id)
			if info, err := root.Lstat(name); err == nil && info.IsDir() {
				b, _ := measureWalk(func(visit fs.WalkDirFunc) error {
					return fs.WalkDir(root.FS(), name, visit)
				}, budget)
				m.Bytes += b
				// Each visited entry spends one unit, so an empty budget afterwards means the
				// walk may have stopped early. A walk that used the last unit on its last entry
				// is flagged too: a lower bound that turns out exact is harmless, a silent one
				// is not.
				if *budget <= 0 {
					m.BytesPartial = true
				}
			}
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out
}

// maxCleanupEntryBytes is the largest payload a restore will read. Archiving must not write
// an entry above it: such an archive would delete the original and then refuse to restore.
const maxCleanupEntryBytes = 64 << 20

func validArchiveID(id string) bool {
	return filepath.IsLocal(id) && filepath.Base(id) == id && id != "." && !strings.Contains(id, "..")
}

var errArchiveID = errors.New("archive manifest id does not match its filename")

func decodeArchiveManifest(data []byte, id string) (cleanupManifest, error) {
	var m cleanupManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return cleanupManifest{}, err
	}
	if m.ID != id {
		return cleanupManifest{}, fmt.Errorf("%w: %s", errArchiveID, id)
	}
	return m, nil
}

// openArchiveFile verifies the identity after OpenFile because os.Root can resolve a symlink
// within the root even with O_NOFOLLOW. NONBLOCK keeps a raced-in FIFO from blocking.
func openArchiveFile(root *os.Root, name string) (*os.File, os.FileInfo, error) {
	want, err := root.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if !want.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("trash file %s is not a regular file", name)
	}
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	got, err := f.Stat()
	if err != nil || !os.SameFile(want, got) {
		_ = f.Close()
		return nil, nil, fmt.Errorf("trash file %s changed while opening", name)
	}
	return f, got, nil
}

func readArchiveFile(root *os.Root, name string) ([]byte, error) {
	f, info, err := openArchiveFile(root, name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if info.Size() > maxCleanupEntryBytes {
		return nil, fmt.Errorf("trash metadata %s is too large", name)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxCleanupEntryBytes+1))
	if len(data) > maxCleanupEntryBytes {
		return nil, fmt.Errorf("trash metadata %s is too large", name)
	}
	return data, err
}

func writeArchiveFile(root *os.Root, name string, data []byte) error {
	if len(data) > maxCleanupEntryBytes {
		return fmt.Errorf("trash metadata %s is too large", name)
	}
	tmp := ".archive-" + rand.Text()
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer root.Remove(tmp)
	_, werr := f.Write(data)
	if err := errors.Join(werr, f.Close()); err != nil {
		return err
	}
	return root.Rename(tmp, name)
}

func readArchiveSidecar(root *os.Root, id string) (cleanupManifest, error) {
	b, err := readArchiveFile(root, id+".json")
	if err != nil {
		return cleanupManifest{}, err
	}
	return decodeArchiveManifest(b, id)
}

func readCleanupTar(f io.Reader, id string) (cleanupManifest, map[string][]byte, error) {
	gr, err := gzip.NewReader(f)
	if err != nil {
		return cleanupManifest{}, nil, err
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	m, err := readTarManifest(tr, id, maxCleanupEntryBytes)
	if err != nil {
		return cleanupManifest{}, nil, err
	}
	payloads := map[string][]byte{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return m, nil, err
		}
		if h.Name == "manifest.json" {
			return m, nil, fmt.Errorf("archive %s has duplicate manifests", id)
		}
		// Cap the allocation even for a corrupt header.
		if h.Size < 0 || h.Size > maxCleanupEntryBytes {
			return m, nil, fmt.Errorf("archive %s: entry %s too large (%d bytes)", id, h.Name, h.Size)
		}
		data := make([]byte, h.Size)
		if _, err := io.ReadFull(tr, data); err != nil {
			return m, nil, err
		}
		payloads[h.Name] = data
	}
	return m, payloads, nil
}

// errRestoreIncomplete refuses a purge while a restore of the same archive has not finished:
// transcripts it placed may already point at the session's cache, and with neither the meta
// nor the archive left, the cache scan would take that cache as orphaned.
var errRestoreIncomplete = errors.New("a restore of this archive did not finish; restore it again first")

// restoringMarker is the file that says a restore of archive id started and has not finished.
// Written before anything is placed and removed only when every session is back.
func restoringMarkerName(id string) string { return id + ".restoring" }

// manifestAtHead reads manifest.json, the first entry writeCleanupArchive writes, from the
// archive's tarball and stops there.
func manifestAtHead(root *os.Root, id string) (cleanupManifest, error) {
	var m cleanupManifest
	f, _, err := openArchiveFile(root, id+".tar.gz")
	if err != nil {
		return m, err
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		return m, err
	}
	defer gr.Close()
	return readTarManifest(tar.NewReader(gr), id, 4<<20)
}

func readTarManifest(tr *tar.Reader, id string, limit int64) (cleanupManifest, error) {
	h, err := tr.Next()
	if err != nil {
		return cleanupManifest{}, err
	}
	if h.Name != "manifest.json" || h.Size < 0 || h.Size > limit {
		return cleanupManifest{}, fmt.Errorf("archive %s: no readable manifest at its head", id)
	}
	b, err := io.ReadAll(tr)
	if err != nil {
		return cleanupManifest{}, err
	}
	return decodeArchiveManifest(b, id)
}

// errRestoreStopped wraps a restore that stopped part way: it reports what failed, promises
// nothing was undone, and that restoring again is safe and finishes the job.
var errRestoreStopped = errors.New("the restore stopped part way; nothing was undone, and restoring again is safe")

// restoreLeftHalfDone reports whether any session in archive id is half back: a transcript at
// its destination with no meta. Only that state needs the archive kept — a session whose meta
// is back is reachable by it, and one with neither is untouched.
//
// An archive whose manifest cannot be read at all counts as half done, erring toward keeping
// it: that is the one case a purge can be refused for good, and ADR 0097 says so. It is also
// the case where nothing else could be done with the archive — a restore needs the same
// manifest. The caller reads only the tarball's first entry under the cleanup lock.
func restoreLeftHalfDone(m cleanupManifest) bool {
	if m.ID == "" {
		return true
	}
	for _, s := range m.Sessions {
		var meta session.Meta
		if json.Unmarshal([]byte(s.Meta), &meta) != nil || meta.Name == "" {
			continue
		}
		if _, ok := session.ReadMeta(meta.Name); ok {
			continue
		}
		for _, p := range s.JSONLPaths {
			if _, err := os.Lstat(p); err == nil {
				return true
			}
		}
	}
	return false
}

// purgeCleanupArchive removes one archive, drops its worktree pin and returns its manifest. Not
// for use under the cleanup lock (see unpinPurged). It does not touch the
// managed ledgers: whether a session's ledger may go depends on every OTHER archive too, which
// the caller checks once for everything it purged (dropPurgedLedgers).
func purgeCleanupArchive(id string) (cleanupManifest, error) {
	root, err := os.OpenRoot(cleanupStoreDir())
	if err != nil {
		return cleanupManifest{}, err
	}
	defer root.Close()
	man, err := purgeCleanupArchiveIn(root, id)
	if err == nil {
		unpinPurged(man)
	}
	return man, err
}

func purgeCleanupArchiveIn(root *os.Root, id string) (cleanupManifest, error) {
	var empty cleanupManifest
	if !validArchiveID(id) {
		return empty, fmt.Errorf("invalid archive id")
	}
	if trashPending(id) {
		return empty, errTrashPending
	}
	// Check both identities before removing anything. The embedded manifest is authoritative
	// for effects outside the trash, such as releasing a worktree pin or a session ledger.
	_, sideErr := readArchiveSidecar(root, id)
	if errors.Is(sideErr, errArchiveID) {
		return empty, sideErr
	}
	man, tarErr := manifestAtHead(root, id)
	if errors.Is(tarErr, errArchiveID) {
		return empty, tarErr
	}
	// Only a half-placed session needs its archive retained. A space restore stores its own
	// progress, and a purge that wins after a failed restore may discard that progress.
	if _, err := root.Lstat(restoringMarkerName(id)); err == nil {
		if restoreLeftHalfDone(man) {
			return empty, errRestoreIncomplete
		}
		_ = root.Remove(restoringMarkerName(id))
	}
	// The requested id owns this folder even if its manifest is unreadable. Keep the
	// archive if removing the folder stops part way, so purge can be retried.
	if err := rmtree.RemoveAllIn(root, spaceTrashName(id)); err != nil {
		return empty, err
	}
	// Restore progress goes only once the archive cannot be restored any more: a purge that stops
	// before that keeps an entry whose next restore must still find what it already did. A
	// purge of an id whose tarball is already gone still clears what an earlier purge left.
	// Left behind, a record blocks its id for a space (entryIDFree) and is ignored by a worktree
	// entry with another pin.
	dropProgress := func() {
		_ = root.Remove(spaceRestoreRecordName(id))
		_ = root.Remove(worktreeRestoreRecordName(id))
	}
	for _, name := range []string{id + ".json", id + ".tar.gz"} {
		if err := root.Remove(name); err != nil && (!errors.Is(err, fs.ErrNotExist) || name == id+".tar.gz") {
			if errors.Is(err, fs.ErrNotExist) {
				dropProgress()
			}
			return empty, err
		}
	}
	dropProgress()
	return man, nil
}

// unpinPurged lets gc have a purged worktree entry's commits. Run it outside the cleanup lock:
// update-ref runs the repository's reference-transaction hook, which may call back into the
// Agent and wait on the deletion gate a restore holds while it waits for the cleanup lock.
func unpinPurged(man cleanupManifest) {
	if man.Worktree != nil {
		gitx.UnpinDeletedWorktree(*man.Worktree)
	}
}

// dropPurgedLedgers removes the managed kinds' ClientMessageID ledger of each session a purged
// archive held. The trash keeps the ledger because a session in it can still be restored and
// resumed (ADR 0101 decision 1); once the archive is purged nothing can bring the session back,
// so the ledger is what is left over. A session whose meta exists (restored since) keeps its
// ledger, and so does one that another archive still holds (deleted, restored, deleted again):
// restoring that archive must find its ledger, or a resumed managed session could run a
// message it had already run. The remaining archives are read once for the whole batch.
func dropPurgedLedgers(purged []cleanupManifest) {
	held := map[string]bool{}
	for _, a := range listCleanupArchives() {
		for _, s := range a.Sessions {
			held[s.Name] = true
		}
	}
	for _, man := range purged {
		dropLedgersOf(man, held)
	}
}

func dropLedgersOf(man cleanupManifest, held map[string]bool) {
	for _, s := range man.Sessions {
		var meta session.Meta
		if json.Unmarshal([]byte(s.Meta), &meta) != nil || meta.Name == "" {
			continue
		}
		if _, ok := session.ReadMeta(meta.Name); ok || held[meta.Name] {
			continue
		}
		sessionx.RemoveManagedLedger(meta)
	}
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
func placeNoReplace(tmp, dest string) (created bool, err error) {
	err = linkFile(tmp, dest)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrExist) {
		return false, nil
	}
	return false, err
}

// chtimes is os.Chtimes; a var only so a test can make it fail.
var chtimes = os.Chtimes

// restoreTerminalHistory puts a restored shell's archived terminal history back where nothing
// is, best effort: the shell is already back, and a screen history is not worth failing a restore
// over. It is skipped when retention is off or the history has outlived it, and the placed file
// keeps its original modification time so the janitor ages it from when it was written.
func restoreTerminalHistory(s cleanupArchivedSession, meta session.Meta, payloads map[string][]byte) {
	data, ok := payloads[s.History]
	// The destination comes from the restored meta, whose name lock this restore holds, and only
	// for a shell: the outer record's name is not what was locked.
	if s.History == "" || !ok || s.Name != meta.Name || !session.ValidName(meta.Name) ||
		(meta.Kind != session.KindShell && meta.Kind != session.KindSSM) {
		return
	}
	retention := terminalHistoryRetention()
	mod, err := time.Parse(time.RFC3339Nano, s.HistoryMod)
	if retention <= 0 || err != nil || time.Since(mod) >= retention {
		return
	}
	dest := terminalHistoryPath(meta.Name)
	if _, err := os.Lstat(dest); err == nil {
		return // a history written since is the live one
	}
	tmp, err := stageNextTo(dest, data)
	if err != nil {
		log.Printf("restore %s: terminal history not restored: %v", meta.Name, err)
		return
	}
	defer os.Remove(tmp)
	// Without the original time the janitor would age the file from the restore, stretching the
	// retention: leave it out instead.
	if err := chtimes(tmp, mod, mod); err != nil {
		log.Printf("restore %s: terminal history not restored: %v", meta.Name, err)
		return
	}
	if _, err := placeNoReplace(tmp, dest); err != nil {
		log.Printf("restore %s: terminal history not restored: %v", meta.Name, err)
	}
}

// restoreAfterStage, when set, runs after a restore has read its archive and staged the
// transcripts and before it takes the cleanup lock. Tests only: it is the point a purge has to
// win at to exercise the race.
var restoreAfterStage func()

func restoreCleanupArchive(id string) (map[string]any, error) {
	if !validArchiveID(id) {
		return nil, fmt.Errorf("invalid archive id")
	}
	if trashPending(id) {
		return nil, errTrashPending
	}
	root, err := os.OpenRoot(cleanupStoreDir())
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, archiveInfo, err := openArchiveFile(root, id+".tar.gz")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	m, payloads, err := readCleanupTar(f, id)
	if err != nil {
		return nil, err
	}
	var sessions, branches []string
	restored := map[string]any{"sessions": sessions, "branches": branches}
	if m.Space != nil {
		if len(m.Sessions) != 0 || len(m.Branches) != 0 || m.Worktree != nil {
			return nil, fmt.Errorf("archive %s: a space cannot share an archive with other items", id)
		}
		if restoreAfterStage != nil {
			restoreAfterStage()
		}
		if err := restoreDeletedSpace(root, id, archiveInfo, *m.Space); err != nil {
			return nil, err
		}
		restored["space"] = m.Space.Folder
		return restored, nil
	}
	type archivedMeta struct {
		record cleanupArchivedSession
		meta   session.Meta
	}
	var entries []archivedMeta
	var names []string
	for _, s := range m.Sessions {
		var meta session.Meta
		if json.Unmarshal([]byte(s.Meta), &meta) != nil || meta.Name == "" {
			continue
		}
		entries = append(entries, archivedMeta{record: s, meta: meta})
		names = append(names, meta.Name)
		sessions = append(sessions, s.Name)
	}
	// Take the same name locks as trash before staging anything. Checking only at the
	// metadata write would let an old archive put files back after the delete removed
	// them, so those files would win over the newer archive's payload on restore.
	slices.Sort(names)
	var locks []*sync.Mutex
	defer func() {
		for _, lock := range locks {
			lock.Unlock()
		}
	}()
	for _, name := range slices.Compact(names) {
		lock := sessionTrashLock(name)
		if !lock.TryLock() {
			return nil, errTrashPending
		}
		locks = append(locks, lock)
	}
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
	for _, entry := range entries {
		s := entry.record
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
		if err := checkRestoreArchive(root, id, archiveInfo); err != nil {
			handErr = err
			return
		}
		// Mark the restore as under way before anything is placed. While the mark stands and
		// something is left half done, the archive cannot be purged, so that half stays
		// reachable through the archive (purgeCleanupArchive).
		marker := restoringMarkerName(id)
		info, statErr := root.Lstat(marker)
		markedBefore := statErr == nil && info.Mode().IsRegular()
		if !markedBefore {
			f, err := root.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err == nil {
				err = f.Close()
			}
			if err != nil {
				handErr = fmt.Errorf("%w: archive %s: cannot mark the restore: %w", errRestoreStopped, id, err)
				return
			}
		}
		changed := false
		// A failure that changed nothing, on an archive no earlier attempt left half done,
		// leaves no mark: otherwise a restore that can never succeed (a filesystem without
		// hard links) would make its archive impossible to purge.
		stop := func(err error) {
			if !changed && !markedBefore {
				_ = root.Remove(marker)
			}
			handErr = fmt.Errorf("%w: %w", errRestoreStopped, err)
		}
		for _, f := range staged {
			created, err := placeNoReplace(f.tmp, f.dest)
			if err != nil {
				stop(fmt.Errorf("cannot place %s: %w", f.dest, err))
				return
			}
			changed = changed || created
		}
		for i, entry := range entries {
			meta := entry.meta
			// An existing meta is newer than the archived snapshot — a session already
			// restored and then locked, renamed or run — and is kept as it is.
			created, err := session.CreateMetaIfAbsent(meta)
			if err != nil {
				stop(fmt.Errorf("restored %d of %d sessions, then could not write %s: %w", i, len(entries), meta.Name, err))
				return
			}
			changed = changed || created
		}
		_ = root.Remove(marker)
	})
	dropStaged() // placed ones are hard links, so the staging names are only temporary
	if handErr != nil {
		return nil, handErr
	}
	for _, entry := range entries {
		restoreTerminalHistory(entry.record, entry.meta, payloads)
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
	if m.Worktree != nil {
		if err := restoreDeletedWorktree(root, id, archiveInfo, *m.Worktree); err != nil {
			return nil, err
		}
		restored["worktree"] = m.Worktree.Name
	}

	return restored, nil
}

// checkRestoreArchive is called under the cleanup lock before placing anything. A purge may
// have won while the restore was reading the archive or staging its transcripts.
func checkRestoreArchive(root *os.Root, id string, original os.FileInfo) error {
	if trashPending(id) {
		return errTrashPending
	}
	current, err := root.Lstat(id + ".tar.gz")
	if err != nil || !os.SameFile(original, current) {
		return fmt.Errorf("archive %s was removed or replaced while it was being restored", id)
	}
	if _, err := readArchiveSidecar(root, id); errors.Is(err, errArchiveID) {
		return err
	}
	return nil
}
