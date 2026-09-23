package sessionx

// Cache orphans: the per-session directories under ~/.cache/agent-fleet that nothing can
// reach any more.
//
// Two subtrees are keyed by a session UUID and have no retention of their own: pasted/<sid>
// (images and files the Console uploaded into a session — session_paste.go) and
// codex-view-image/<sid> (the screenshots codex's view_image read, persisted so the mirror
// can show them — agents/codex/transcript.go). The time sweeps next door (fs_thumb.go,
// imagegen/store.go) never look at them, and deleting a session leaves them behind, so they
// only ever grow — 2.7 months and ~145 MB on the deployment this was measured on.
//
// A time TTL is the wrong rule for them: a pasted image is the INPUT of a conversation and
// its absolute path is written into the prompt, so ageing it out would punch holes into the
// past turns of a session that is still alive. The right rule is reachability, and it can be
// decided exactly, because a session name is a random slug that is never reused
// (session_name.go) and session.UUID is a pure function of (dir, name). A directory whose
// UUID belongs to
//   - no session meta (live, stopped or on the shelf), and
//   - no session inside a cleanup archive (the gz trash — restoring one brings the meta back
//     and the transcript would then point at images we deleted),
// can never be named by anything again. pasted/chat-<id> is the assistant chat's, keyed by
// conversation id instead: it is an orphan once the conversation file is gone (chat delete
// has no trash).
//
// generated/ is deliberately NOT here: pictures are products, not inputs, and they already
// age out after 30 days (imagegen/store.go). thumbs/ is derived data with its own sweep.
//
// Nothing here removes anything on its own. The scan feeds a cleanup candidate
// (session_cleanup.go) and a person presses the button; the delete re-runs the scan so a
// directory that became reachable in between (a restore) is spared.

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Cache subtrees swept by reachability. The strings are directory names under CacheRoot and
// double as the candidate id the Console sends back, so they are a wire contract.
const (
	CacheFeaturePasted         = "pasted"
	CacheFeatureCodexViewImage = "codex-view-image"
)

// CacheOrphanFeatures is the fixed set a delete may name. Anything else is refused, so a
// request can never walk out of these two subtrees.
var CacheOrphanFeatures = []string{CacheFeaturePasted, CacheFeatureCodexViewImage}

// cacheOrphanGrace keeps a directory touched in the last few minutes out of the scan. A
// session writes its meta before anything can land in these directories, so this is not
// needed for correctness today; it is there so a future write path that runs the other way
// round loses a race to a scan rather than a user's upload.
const cacheOrphanGrace = 10 * time.Minute

// ErrCacheScanUnsafe means the scan could not prove what is reachable — a session meta or
// an archive it could not read. Every caller treats it as "delete nothing".
var ErrCacheScanUnsafe = errors.New("cannot tell which cache directories are still reachable")

// CacheRoot is ~/.cache/agent-fleet.
func CacheRoot() string { return filepath.Join(paths.HomeDir(), ".cache", "agent-fleet") }

// CleanupArchiveDir is where cleanup_archive.go (package main) keeps the gz trash. Read here
// only to learn which sessions a restore could bring back; the format is main's, and a test
// on that side writes a real archive and checks this reader sees it.
func CleanupArchiveDir() string { return filepath.Join(paths.AgentDataDir(), "cleanup") }

// CacheOrphanDir is one unreachable directory.
type CacheOrphanDir struct {
	Name  string
	Path  string
	Bytes int64
	Files int
}

// CacheOrphans is the scan of one feature.
type CacheOrphans struct {
	Feature string
	Dirs    []CacheOrphanDir
	Bytes   int64
	Files   int
	// Truncated = the entry budget ran out before every directory was looked at. Dirs holds
	// only directories that were walked to the end, so it is still safe to delete; the totals
	// are a lower bound.
	Truncated bool
}

// CacheScanMaxEntries is the default budget of one scan: how many directory entries it may
// visit before it stops. The measured cache is ~9k files; this only bites on something
// pathological (or an EFS home), where holding the cleanup lock for minutes is worse than
// an answer that says it is partial.
const CacheScanMaxEntries = 500_000

var sidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// cleanupMu is the one lock everything that changes what is reachable goes through: the
// cache delete (scan, then remove) and — through WithCleanupLock — the trash's restore and
// purge. Without the last two, a purge could drop an archive while a restore that had
// already read it into memory has not yet written the meta back; a delete scanning in that
// gap sees the session nowhere and takes the images the restore is about to need.
var cleanupMu sync.Mutex

// WithCleanupLock runs fn under the cleanup lock. For main's restore and purge of a cleanup
// archive (cleanup_ops.go).
func WithCleanupLock(fn func()) {
	cleanupMu.Lock()
	defer cleanupMu.Unlock()
	fn()
}

// ScanCacheOrphans lists the unreachable directories of one feature, spending at most
// *budget directory entries (decremented as it goes; nil = CacheScanMaxEntries).
func ScanCacheOrphans(feature string, now time.Time, budget *int) (CacheOrphans, error) {
	cleanupMu.Lock()
	defer cleanupMu.Unlock()
	return scanCacheOrphans(feature, now, budget)
}

// RemoveCacheOrphans re-scans and deletes what is still unreachable. It returns what it
// removed; a directory that fails to delete is left out of the totals.
func RemoveCacheOrphans(feature string, now time.Time) (CacheOrphans, error) {
	cleanupMu.Lock()
	defer cleanupMu.Unlock()
	found, err := scanCacheOrphans(feature, now, nil)
	if err != nil {
		return CacheOrphans{Feature: feature}, err
	}
	out := CacheOrphans{Feature: feature, Truncated: found.Truncated}
	for _, d := range found.Dirs {
		// The root was checked not to be a link when the scan began; check again right
		// before each removal, so a swap in between cannot aim RemoveAll elsewhere.
		if !realDir(filepath.Dir(d.Path)) || os.RemoveAll(d.Path) != nil {
			continue
		}
		out.Dirs = append(out.Dirs, d)
		out.Bytes += d.Bytes
		out.Files += d.Files
	}
	return out, nil
}

func validCacheFeature(feature string) bool {
	for _, f := range CacheOrphanFeatures {
		if f == feature {
			return true
		}
	}
	return false
}

// realDir reports whether path is a directory itself, not a symlink to one.
func realDir(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.IsDir()
}

func scanCacheOrphans(feature string, now time.Time, budget *int) (CacheOrphans, error) {
	out := CacheOrphans{Feature: feature}
	if !validCacheFeature(feature) {
		return out, errors.New("unknown cache feature: " + feature)
	}
	if budget == nil {
		b := CacheScanMaxEntries
		budget = &b
	}
	root := filepath.Join(CacheRoot(), feature)
	// The feature directory must be a real directory. If it were a link, ReadDir and
	// RemoveAll would follow it and delete UUID-named folders wherever it points. Links
	// further up (~/.cache kept on persistent storage via AF_WS_KEEP_DIRS) are legitimate —
	// what they lead to is still this cache — so only the last hop is checked.
	if fi, err := os.Lstat(root); errors.Is(err, fs.ErrNotExist) {
		return out, nil
	} else if err != nil {
		return out, err
	} else if !fi.IsDir() {
		return out, ErrCacheScanUnsafe
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	reachable, err := reachableSessionIDs()
	if err != nil {
		return out, err
	}
	cutoff := now.Add(-cacheOrphanGrace)
	for _, e := range entries {
		// ReadDir reports a symlinked entry as a link, not a directory, so links to
		// elsewhere are never candidates (RemoveAll on one would only drop the link anyway).
		if !e.IsDir() {
			continue
		}
		if *budget <= 0 {
			out.Truncated = true
			break
		}
		name := e.Name()
		if !orphanName(feature, name, reachable) {
			continue
		}
		dir := filepath.Join(root, name)
		bytes, files, newest, complete := dirStats(dir, budget)
		if !complete {
			// A directory not walked to the end may hold a fresh file the walk never
			// reached; it is neither counted nor deleted.
			out.Truncated = true
			break
		}
		if newest.After(cutoff) {
			continue
		}
		out.Dirs = append(out.Dirs, CacheOrphanDir{Name: name, Path: dir, Bytes: bytes, Files: files})
		out.Bytes += bytes
		out.Files += files
	}
	return out, nil
}

// orphanName decides one directory name. Only the two shapes we write are ever candidates:
// a session UUID, and (pasted only) chat-<conversation id>. Any other name is somebody
// else's and stays.
func orphanName(feature, name string, reachable map[string]bool) bool {
	if sidPattern.MatchString(name) {
		return !reachable[name]
	}
	if feature == CacheFeaturePasted && strings.HasPrefix(name, "chat-") {
		id := strings.TrimPrefix(name, "chat-")
		if !paths.ValidIDSegment(id) {
			return false
		}
		_, err := chatx.LoadConv(id)
		// Only a conversation that is provably gone. A file that exists but does not parse
		// is still somebody's conversation.
		return errors.Is(err, fs.ErrNotExist)
	}
	return false
}

// reachableSessionIDs is every session UUID something can still name: each meta on disk,
// and each session a cleanup archive would restore. It fails rather than under-counting —
// a meta or an archive it cannot read would otherwise make that session's files look
// orphaned.
func reachableSessionIDs() (map[string]bool, error) {
	ids := map[string]bool{}

	ents, err := os.ReadDir(session.MetaDir())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, ErrCacheScanUnsafe
	}
	for _, e := range ents {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if e.IsDir() || !ok {
			continue
		}
		// A meta that cannot be read is exactly the session we would mistake for gone.
		m, ok := session.ReadMeta(name)
		if !ok {
			return nil, ErrCacheScanUnsafe
		}
		// Two keys, because the writers do not agree on which name they use: the paste
		// endpoint keys by the name in the URL — the meta's FILE name (session_paste.go) —
		// and codex's view_image by the name inside it (agents/codex/transcript.go). They
		// are the same for every meta the Agent writes; if one ever is not, both stay safe.
		ids[session.UUID(m.Dir, name)] = true
		ids[session.UUID(m.Dir, m.Name)] = true
	}

	archived, err := archivedSessionIDs()
	if err != nil {
		return nil, err
	}
	for id := range archived {
		ids[id] = true
	}
	return ids, nil
}

// archiveManifest is the part of cleanup_archive.go's manifest this reader needs.
type archiveManifest struct {
	Sessions []struct {
		Meta string `json:"meta"`
	} `json:"sessions"`
}

// archivedSessionIDs reads every archive's manifest: the sidecar <id>.json when there is
// one, else manifest.json from inside <id>.tar.gz (the sidecar is written best-effort, the
// tarball is the source of truth). A session whose meta does not parse is skipped — the
// restore skips it too, so it cannot come back.
func archivedSessionIDs() (map[string]bool, error) {
	ids := map[string]bool{}
	dir := CleanupArchiveDir()
	ents, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return ids, nil
	}
	if err != nil {
		return nil, ErrCacheScanUnsafe
	}
	sidecars := map[string]bool{}
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".json") {
			sidecars[strings.TrimSuffix(e.Name(), ".json")] = true
		}
	}
	add := func(m archiveManifest) {
		for _, s := range m.Sessions {
			var meta session.Meta
			if json.Unmarshal([]byte(s.Meta), &meta) != nil || meta.Name == "" {
				continue
			}
			ids[session.UUID(meta.Dir, meta.Name)] = true
		}
	}
	for id := range sidecars {
		b, err := os.ReadFile(filepath.Join(dir, id+".json"))
		if err != nil {
			return nil, ErrCacheScanUnsafe
		}
		var m archiveManifest
		if json.Unmarshal(b, &m) != nil {
			// A broken sidecar: fall back to the tarball, as for a missing one.
			delete(sidecars, id)
			continue
		}
		add(m)
	}
	for _, e := range ents {
		id, ok := strings.CutSuffix(e.Name(), ".tar.gz")
		if !ok || sidecars[id] {
			continue
		}
		m, err := manifestFromTarball(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, ErrCacheScanUnsafe
		}
		add(m)
	}
	return ids, nil
}

// manifestFromTarball reads manifest.json, the first entry writeCleanupArchive writes, and
// stops there — the payloads behind it can be hundreds of megabytes.
func manifestFromTarball(path string) (archiveManifest, error) {
	var m archiveManifest
	f, err := os.Open(path)
	if err != nil {
		return m, err
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		return m, err
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	h, err := tr.Next()
	if err != nil {
		return m, err
	}
	if h.Name != "manifest.json" || h.Size < 0 || h.Size > 4<<20 {
		return m, errors.New("no manifest at the head of " + path)
	}
	b, err := io.ReadAll(tr)
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal(b, &m)
}

// dirStats walks one directory: total bytes, file count, and the newest mtime seen
// (directories included, so an upload in progress counts). It spends one unit of budget per
// entry; complete is false when the budget ran out before the walk did.
func dirStats(dir string, budget *int) (bytes int64, files int, newest time.Time, complete bool) {
	complete = true
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if *budget <= 0 {
			complete = false
			return filepath.SkipAll
		}
		*budget--
		if err != nil {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		if d.Type().IsRegular() {
			bytes += info.Size()
			files++
		}
		return nil
	})
	return bytes, files, newest, complete
}
