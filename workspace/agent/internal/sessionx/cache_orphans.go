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
	// are a lower bound, and the next scan picks up where this one could not reach.
	Truncated bool
	// Unreadable counts orphan directories left out because something inside could not be
	// read. Unlike Truncated this does not go away by scanning again; it needs the
	// permissions or the filesystem looked at.
	Unreadable int
	// Stalled = the session records and the trash alone exceed the budget, so nothing could
	// be judged at all. Surveying again does not help; a smaller trash does.
	Stalled bool
	// Stuck = the budget ran out before a single directory was finished — a folder too big to
	// walk, or too much ahead of it in the listing. The listing order is the same every time,
	// so pressing again would stop at the same place: unlike Truncated, this needs a person.
	Stuck bool
}

// CacheScanMaxEntries is the default budget of one scan: how many directory entries it may
// visit before it stops. The measured cache is ~9k files; this only bites on something
// pathological (or an EFS home), where holding the cleanup lock for minutes is worse than
// an answer that says it is partial.
// A var only so a test can shrink it.
var CacheScanMaxEntries = 500_000

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
	r, err := openFeatureRoot(feature)
	if err != nil || r == nil {
		return CacheOrphans{Feature: feature}, err
	}
	defer r.Close()
	return scanCacheOrphans(r, feature, now, budget)
}

// RemoveCacheOrphans re-scans and deletes what is still unreachable. It returns what it
// removed; a directory that fails to delete is left out of the totals. Truncated says the
// scan stopped at its budget, so a press did not take everything and another one can.
func RemoveCacheOrphans(feature string, now time.Time) (CacheOrphans, error) {
	cleanupMu.Lock()
	defer cleanupMu.Unlock()
	r, err := openFeatureRoot(feature)
	if err != nil || r == nil {
		return CacheOrphans{Feature: feature}, err
	}
	defer r.Close()
	found, err := scanCacheOrphans(r, feature, now, nil)
	if err != nil {
		return CacheOrphans{Feature: feature}, err
	}
	out := CacheOrphans{Feature: feature, Truncated: found.Truncated, Unreadable: found.Unreadable, Stalled: found.Stalled, Stuck: found.Stuck}
	for _, d := range found.Dirs {
		// Relative to the pinned directory: whatever happens to the path in the meantime,
		// this cannot reach outside it (os.Root does not follow a link out of its root).
		if r.RemoveAll(d.Name) != nil {
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

// openFeatureRoot pins one feature directory by file descriptor, so the scan and the
// delete act on exactly the directory that was checked. nil, nil = it does not exist.
//
// The feature directory itself must be a real directory. If it were a link, the scan and
// the delete would act on wherever it points and remove UUID-named folders there. Links
// further up the path (~/.cache kept on persistent storage via AF_WS_KEEP_DIRS) are trusted:
// what they lead to is this user's own cache, put there by the workspace's own setup. The
// Lstat and the open are two calls, so the opened directory is compared with the one that
// was checked — a swap in between is refused rather than followed.
func openFeatureRoot(feature string) (*os.Root, error) {
	if !validCacheFeature(feature) {
		return nil, errors.New("unknown cache feature: " + feature)
	}
	path := filepath.Join(CacheRoot(), feature)
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, ErrCacheScanUnsafe
	}
	r, err := os.OpenRoot(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if pinned, err := r.Stat("."); err != nil || !os.SameFile(fi, pinned) {
		r.Close()
		return nil, ErrCacheScanUnsafe
	}
	return r, nil
}

// errBudget is reachableSessionIDs running out of budget: nothing can be decided, which
// the scan reports as a truncated answer with no directories rather than as an error.
var errBudget = errors.New("cache scan budget exhausted")

func scanCacheOrphans(r *os.Root, feature string, now time.Time, budget *int) (CacheOrphans, error) {
	out, err := scanCacheOrphansOnce(r, feature, now, budget)
	// Cut short with nothing finished: the next scan starts at the same place and ends the
	// same way. Say that, rather than "partial", which promises the next press gets further.
	if err == nil && out.Truncated && len(out.Dirs) == 0 {
		out.Truncated, out.Stuck = false, true
	}
	return out, err
}

func scanCacheOrphansOnce(r *os.Root, feature string, now time.Time, budget *int) (CacheOrphans, error) {
	out := CacheOrphans{Feature: feature}
	if budget == nil {
		b := CacheScanMaxEntries
		budget = &b
	}
	// Everything below spends the budget — every meta and archive the reachability check
	// reads, the listing, every chat lookup, every entry walked — so the whole scan, and the
	// time it holds the cleanup lock, is bounded.
	//
	// Reachability comes first and must fit whole: without it nothing can be judged. If the
	// session records and the trash alone exceed the budget, surveying again cannot help —
	// that is Stalled, not Truncated.
	reachable, err := reachableSessionIDs(budget)
	if errors.Is(err, errBudget) {
		out.Stalled = true
		return out, nil
	}
	if err != nil {
		return out, err
	}
	top, err := r.Open(".")
	if err != nil {
		return out, err
	}
	defer top.Close()
	cutoff := now.Add(-cacheOrphanGrace)
	root := filepath.Join(CacheRoot(), feature)
	chatOK := chatStoreOK()
	// The listing is consumed a chunk at a time and each chunk judged before the next is
	// read, so a cut-off scan still yields every directory it finished. Deleting those is what
	// makes the next survey progress: they are gone from the head of the listing, and the
	// budget reaches further. (It cannot progress past a head of more reachable or fresh
	// directories than the budget — hundreds of thousands of live sessions — which is not a
	// cache anyone has.)
	for {
		// At most half of what is left goes on listing, so the walk of the directories just
		// listed always has budget of its own. Listing a full chunk first could spend it all
		// and leave the first directory unwalkable — a scan that stops at the same place
		// every time.
		n := min(readDirChunk, *budget/2)
		if *budget > 0 && n == 0 {
			n = 1
		}
		if n <= 0 {
			if more, err := top.ReadDir(1); !(errors.Is(err, io.EOF) && len(more) == 0) {
				out.Truncated = true
			}
			return out, nil
		}
		chunk, err := top.ReadDir(n)
		*budget -= len(chunk)
		for _, e := range chunk {
			// ReadDir reports a symlinked entry as a link, not a directory, so links to
			// elsewhere are never candidates.
			if !e.IsDir() {
				continue
			}
			name := e.Name()
			if !orphanName(feature, name, reachable, chatOK) {
				continue
			}
			bytes, files, newest, walked := dirStats(r, name, budget)
			// Not walked to the end, so it may hold a fresh file the walk never saw: neither
			// counted nor deleted. The two reasons are told apart because they are fixed in
			// different ways — a budget by surveying again, a read error only by a person.
			if walked == walkBudget {
				out.Truncated = true
				return out, nil
			}
			if walked == walkUnreadable {
				out.Unreadable++
				continue
			}
			if newest.After(cutoff) {
				continue
			}
			out.Dirs = append(out.Dirs, CacheOrphanDir{Name: name, Path: filepath.Join(root, name), Bytes: bytes, Files: files})
			out.Bytes += bytes
			out.Files += files
		}
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
	}
}

// orphanName decides one directory name. Only the two shapes we write are ever candidates:
// a session UUID, and (pasted only) chat-<conversation id>. Any other name is somebody
// else's and stays.
func orphanName(feature, name string, reachable map[string]bool, chatStoreOK bool) bool {
	if sidPattern.MatchString(name) {
		return !reachable[name]
	}
	if feature == CacheFeaturePasted && strings.HasPrefix(name, "chat-") {
		id := strings.TrimPrefix(name, "chat-")
		// Only a conversation that is provably gone, in a store that is provably there: a
		// missing store (an unmounted volume under a kept ~/.config) would make every chat look
		// gone. A file that exists — readable or not — is still somebody's conversation.
		if !chatStoreOK || !paths.ValidIDSegment(id) {
			return false
		}
		_, err := os.Lstat(chatx.ConvPath(id))
		return errors.Is(err, fs.ErrNotExist)
	}
	return false
}

// chatStoreOK reports whether the conversation store is there to be asked. It follows links:
// ~/.config may legitimately be one onto persistent storage.
func chatStoreOK() bool {
	fi, err := os.Stat(chatx.ChatDir())
	return err == nil && fi.IsDir()
}

// reachableSessionIDs is every session UUID something can still name: each meta on disk,
// and each session a cleanup archive would restore. It fails rather than under-counting —
// a meta or an archive it cannot read would otherwise make that session's files look
// orphaned.
func reachableSessionIDs(budget *int) (map[string]bool, error) {
	ids := map[string]bool{}

	// The session store must be there. A missing one — an unmounted volume, a migration that
	// stopped half way — would make every session look gone. (This runs only when the cache
	// directory exists, and nothing lands there before a session's meta does.)
	if fi, err := os.Stat(session.MetaDir()); err != nil || !fi.IsDir() {
		return nil, ErrCacheScanUnsafe
	}
	ents, err := readDirPathBudget(session.MetaDir(), budget)
	if errors.Is(err, errBudget) {
		return nil, err
	}
	if err != nil {
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
		// A fork's history carries its ancestors' pasted paths (session.Meta.ForkSids). Forks
		// made before ForkSids existed name only their parent, and only claude's ForkFrom is in
		// this id space — the parent's own UUID — so that one is honoured too.
		for _, sid := range m.ForkSids {
			ids[sid] = true
		}
		if sidPattern.MatchString(m.ForkFrom) {
			ids[m.ForkFrom] = true
		}
	}

	archived, err := archivedSessionIDs(budget)
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
func archivedSessionIDs(budget *int) (map[string]bool, error) {
	ids := map[string]bool{}
	dir := CleanupArchiveDir()
	// Every archive file costs one unit whether or not it is read.
	ents, err := readDirPathBudget(dir, budget)
	if errors.Is(err, errBudget) {
		return nil, err
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

// readDirChunk is how many entries one directory read asks for. Listing in chunks is what
// makes the budget a bound on work done rather than a count taken afterwards: a single
// ReadDir of a directory with a million entries reads all of them into memory before a
// single one can be charged.
const readDirChunk = 1024

// dirLister is *os.File and the files an os.Root opens.
type dirLister interface {
	ReadDir(n int) ([]fs.DirEntry, error)
}

// readDirBudget lists a directory chunk by chunk, charging one unit per entry. It returns
// errBudget when the budget runs out before the listing does.
func readDirBudget(f dirLister, budget *int) ([]fs.DirEntry, error) {
	var out []fs.DirEntry
	for {
		n := readDirChunk
		if *budget < n {
			n = *budget
		}
		if n <= 0 {
			// Out of budget: complete only if there is nothing left to read.
			if more, err := f.ReadDir(1); errors.Is(err, io.EOF) && len(more) == 0 {
				return out, nil
			}
			return out, errBudget
		}
		ents, err := f.ReadDir(n)
		*budget -= len(ents)
		out = append(out, ents...)
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
	}
}

// readDirPathBudget is readDirBudget on a path; a directory that does not exist is empty.
func readDirPathBudget(path string, budget *int) ([]fs.DirEntry, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readDirBudget(f, budget)
}

// walkResult says how far dirStats got.
type walkResult int

const (
	walkComplete   walkResult = iota
	walkBudget                // the budget ran out
	walkUnreadable            // something inside could not be listed or stat'ed
)

// dirStats walks one directory inside the pinned root: total bytes, file count, and the
// newest mtime seen (directories included, so an upload in progress counts). Every entry
// costs one unit of budget, charged as each chunk is read. Links are never followed — an
// os.Root would not follow one out of the root anyway, and inside it a link is not our file.
func dirStats(r *os.Root, name string, budget *int) (bytes int64, files int, newest time.Time, result walkResult) {
	self, err := r.Lstat(name)
	if err != nil {
		return 0, 0, newest, walkUnreadable
	}
	newest = self.ModTime()
	stack := []string{name}
	for len(stack) > 0 {
		dir := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		f, err := r.Open(dir)
		if err != nil {
			return bytes, files, newest, walkUnreadable
		}
		ents, err := readDirBudget(f, budget)
		f.Close()
		if errors.Is(err, errBudget) {
			return bytes, files, newest, walkBudget
		}
		if err != nil {
			return bytes, files, newest, walkUnreadable
		}
		for _, e := range ents {
			info, err := e.Info()
			if err != nil {
				return bytes, files, newest, walkUnreadable
			}
			if info.ModTime().After(newest) {
				newest = info.ModTime()
			}
			switch {
			case e.IsDir():
				stack = append(stack, filepath.Join(dir, e.Name()))
			case e.Type().IsRegular():
				bytes += info.Size()
				files++
			}
		}
	}
	return bytes, files, newest, walkComplete
}
