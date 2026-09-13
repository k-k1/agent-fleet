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
	"strings"
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

// consoleDirName is the folder the Console's own pane writes into (ADR 0081 decision 3): a
// sibling of the per-session folders, and the one the gallery's "generated images" family lists.
//
// It is NEVER swept. The 30-day window below exists because an agent's pictures are by-products
// of a conversation nobody asked to keep; these are the product, and a person pressed the button
// for each of them.
const consoleDirName = "console"

// trialDirName is the one subtree the sweep still clears, after seven days. A trial picture is
// disposable by definition — the batch remakes the keeper at full steps — so the exception to
// "never swept" is stated here, next to the rule it is an exception to.
const trialDirName = "trial"

// ConsoleDir and ConsoleTrialDir are where the job queue puts a picture when the caller named no
// folder of their own.
func ConsoleDir() string      { return filepath.Join(generatedRootDir(), consoleDirName) }
func ConsoleTrialDir() string { return filepath.Join(ConsoleDir(), trialDirName) }

// storeImages writes a provider's bytes into the session's directory. The name carries the
// timestamp so a later generation never overwrites an earlier one — the user may still be
// looking at it.
func storeImages(sid string, images []Image) ([]StoredFile, error) {
	return storeImagesAt(GeneratedDir(sid), images, nil)
}

// storeImagesAt is the same, into a directory the caller names, with a sidecar per picture when
// one is given (ADR 0081 decision 3). props is the resolved request, and the seed and the file
// name of each individual picture are filled in here — the caller knows what it asked for, and
// only this function knows what the pictures ended up being called.
func storeImagesAt(dir string, images []Image, props *ImageProps) ([]StoredFile, error) {
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
			Seed: img.Seed,
		})
		if props != nil {
			one := *props
			one.Seed = img.Seed
			if img.Width > 0 && img.Height > 0 {
				one.Size = fmt.Sprintf("%dx%d", img.Width, img.Height)
			}
			if err := writeSidecar(path, one); err != nil {
				return nil, err
			}
		}
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
	// trialTTL is the window for generated/console/trial alone (ADR 0081 decision 11). A trial is
	// a cheap, few-step preview of a picture the batch then makes properly, so keeping them for a
	// month would fill the disk with drafts of pictures that already exist next door.
	trialTTL = 7 * 24 * time.Hour
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
	now := time.Now()
	sweepGeneratedNow(root, now.Add(-generatedTTL), now.Add(-trialTTL))
}

// sweepGeneratedNow is the unthrottled sweep, so a test can drive the retention rule itself
// without depending on the clock the throttle keeps.
//
// Two cutoffs, and the second one is the whole of ADR 0081 decision 3's exception: the
// per-session folders age out at cutoff, the Console's own folder is skipped entirely, and only
// its `trial` subfolder is cleared, at trialCutoff.
func sweepGeneratedNow(root string, cutoff, trialCutoff time.Time) {
	dirs, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		sub := filepath.Join(root, d.Name())
		if d.Name() == consoleDirName {
			// The keepers stay forever; the drafts next to them do not. Nothing else under this
			// folder is touched, including a subfolder a future phase adds.
			sweepDirFiles(filepath.Join(sub, trialDirName), trialCutoff)
			continue
		}
		sweepDirFiles(sub, cutoff)
		// Empty only; never a recursive delete.
		_ = os.Remove(sub)
	}
}

// sweepDirFiles drops the files of one directory that are older than cutoff. A picture's sidecar
// is removed with it rather than on its own age: they are written together and a record of a
// picture that is gone is worse than neither.
func sweepDirFiles(dir string, cutoff time.Time) {
	files, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		info, err := f.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		path := filepath.Join(dir, f.Name())
		_ = os.Remove(path)
		if !strings.HasSuffix(f.Name(), sidecarExt) {
			_ = os.Remove(sidecarPathFor(path))
		}
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
