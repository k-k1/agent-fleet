package sessionx

// generated_images.go — the two fields a session row carries about its generated images
// (ADR 0080 decision 8): how many there are, and where.

import (
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/filemeta"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/imagegen"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// generatedImages counts the images generate_image has stored for this session and returns
// the folder holding them, browse-root relative — the pair the Console's gallery entry needs
// before it can decide whether to offer itself at all.
//
// Zero and "" whenever there is nothing to open: no folder, no images in it, or a folder the
// file API would refuse anyway. The fields are omitempty, so a deployment that has never
// generated an image adds no bytes to the list.
//
// Only af's own output is counted. codex's own generated_images sit under .codex, which the
// file tree deliberately refuses to enumerate (fsDeny), and pasted images are a different
// axis with a different folder.
func generatedImages(m session.Meta) (int, string) {
	dir := imagegen.GeneratedDir(session.UUID(m.Dir, m.Name))
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		forgetGeneratedImages(dir)
		return 0, ""
	}
	n := countGeneratedImages(dir, fi.ModTime())
	if n == 0 {
		return 0, ""
	}
	// The same step a generated image's userfile part takes (resolveUserFiles): the file API
	// speaks browse-root relative. toBrowseRel hands back an absolute path when it cannot
	// place one under the root — the Console could not open that, so offer nothing.
	rel := toBrowseRel(dir, "", browseRoot())
	if filepath.IsAbs(rel) {
		return 0, ""
	}
	return n, rel
}

// generatedImagesMemo caches each folder's count against the folder's own mtime.
//
// The listing builds every session row, and "a small directory" is not a guarantee: images
// are kept for 30 days (imagegen/store.go), so a session that generates daily holds hundreds
// of names — and counting them means reading every name. A directory's mtime moves whenever
// an entry is added or removed, which is exactly when the count can change, so it is the key.
//
// The one thing it cannot see is two writes inside a single filesystem timestamp tick with a
// read in between; the count is then one generation stale until the next write. That is a
// number beside a menu item, and the next generation corrects it.
var generatedImagesMemo = struct {
	mu sync.Mutex
	m  map[string]generatedImagesEntry
}{m: map[string]generatedImagesEntry{}}

type generatedImagesEntry struct {
	mod time.Time
	n   int
}

func countGeneratedImages(dir string, mod time.Time) int {
	generatedImagesMemo.mu.Lock()
	hit, ok := generatedImagesMemo.m[dir]
	generatedImagesMemo.mu.Unlock()
	if ok && hit.mod.Equal(mod) {
		return hit.n
	}
	// Read outside the lock: one slow folder must not hold up the rest of the listing.
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range ents {
		// filemeta is the single axis for "is this an image" (ADR 0080 decision 3); it also
		// leaves out imagegen's half-written ".image-*" temporaries, which carry no extension.
		if e.IsDir() || filemeta.ImageContentType(e.Name()) == "" {
			continue
		}
		n++
	}
	generatedImagesMemo.mu.Lock()
	generatedImagesMemo.m[dir] = generatedImagesEntry{mod: mod, n: n}
	generatedImagesMemo.mu.Unlock()
	return n
}

// forgetGeneratedImages drops a folder that is no longer there (a deleted session, a swept
// directory), so the memo does not hold an entry per session ever seen.
func forgetGeneratedImages(dir string) {
	generatedImagesMemo.mu.Lock()
	delete(generatedImagesMemo.m, dir)
	generatedImagesMemo.mu.Unlock()
}
