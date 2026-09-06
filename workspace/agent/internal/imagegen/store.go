package imagegen

// Where a generated image lives, and when it stops living there.
//
// The convention is session_paste.go's: ~/.cache/agent-fleet/<feature>/<sid>, keyed by the
// session UUID so the files stay associated with the session and survive across turns. It
// sits under the browse root and outside every fsDeny prefix, so the Console's file viewer
// opens the result with no new endpoint — which is the point of moving it off
// $CODEX_HOME/generated_images, a path the file tree deliberately refuses to enumerate.

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// GeneratedDir is one session's generated-image directory.
func GeneratedDir(sid string) string {
	return filepath.Join(paths.HomeDir(), ".cache", "agent-fleet", "generated", sid)
}

func generatedRootDir() string {
	return filepath.Join(paths.HomeDir(), ".cache", "agent-fleet", "generated")
}

// storeImages writes a provider's bytes into the session's directory. The name carries the
// timestamp so a later generation never overwrites an earlier one — the user may still be
// looking at it.
func storeImages(sid string, images []Image) ([]StoredFile, error) {
	dir := GeneratedDir(sid)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	stamp := time.Now().UnixNano()
	out := make([]StoredFile, 0, len(images))
	for i, img := range images {
		name := fmt.Sprintf("image-%d-%d%s", stamp, i+1, extOf(img.MIME))
		path := filepath.Join(dir, name)
		// tmp + rename, so a reader that is already watching the directory never sees a
		// half-written PNG.
		tmp, err := os.CreateTemp(dir, ".image-*")
		if err != nil {
			return nil, err
		}
		if _, err := tmp.Write(img.Bytes); err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
			return nil, err
		}
		if err := tmp.Close(); err != nil {
			os.Remove(tmp.Name())
			return nil, err
		}
		if err := os.Rename(tmp.Name(), path); err != nil {
			os.Remove(tmp.Name())
			return nil, err
		}
		out = append(out, StoredFile{
			Path: path, Name: name, MIME: img.MIME,
			Bytes: int64(len(img.Bytes)), Width: img.Width, Height: img.Height,
		})
	}
	sweepGenerated(generatedRootDir())
	return out, nil
}

func extOf(mime string) string {
	switch mime {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	}
	return ".bin"
}

// Retention. Unlike the thumbnail cache next door these are not derived data — a deleted
// image cannot be regenerated for free, and regenerating it would spend the user's plan
// quota again — so the window is long. It is still bounded: at the measured 2-3 MB per image
// an unswept directory is what let $CODEX_HOME/generated_images reach 80 MB on the container
// this was designed in.
const (
	generatedTTL        = 30 * 24 * time.Hour
	generatedSweepEvery = time.Hour
)

var generatedSweep struct {
	mu   sync.Mutex
	last time.Time
}

// sweepGenerated drops images older than the retention window and then the session
// directories that are left empty. Throttled to once an hour, so a burst of generations pays
// for one directory walk rather than one each.
func sweepGenerated(root string) {
	generatedSweep.mu.Lock()
	if !generatedSweep.last.IsZero() && time.Since(generatedSweep.last) < generatedSweepEvery {
		generatedSweep.mu.Unlock()
		return
	}
	generatedSweep.last = time.Now()
	generatedSweep.mu.Unlock()
	sweepGeneratedNow(root, time.Now().Add(-generatedTTL))
}

// sweepGeneratedNow is the unthrottled sweep, so a test can drive the retention rule itself
// without depending on the clock the throttle keeps.
func sweepGeneratedNow(root string, cutoff time.Time) {
	dirs, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		sub := filepath.Join(root, d.Name())
		files, err := os.ReadDir(sub)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() {
				continue
			}
			info, err := f.Info()
			if err != nil || info.ModTime().After(cutoff) {
				continue
			}
			_ = os.Remove(filepath.Join(sub, f.Name()))
		}
		// Empty only; never a recursive delete.
		_ = os.Remove(sub)
	}
}

// ResetSweepClock rewinds the sweep throttle. A test-only hook, for the same reason
// usagex.ResetPruneClock exists: the throttle state is unexported and a test that generates
// twice would otherwise never see the second sweep.
func ResetSweepClock() {
	generatedSweep.mu.Lock()
	generatedSweep.last = time.Time{}
	generatedSweep.mu.Unlock()
}
