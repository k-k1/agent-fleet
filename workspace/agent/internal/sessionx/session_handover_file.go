package sessionx

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// The handover file lives in the target's throwaway directory, ~/.af-work/<target>, never in
// the working copy, so it cannot be committed (ADR 0115 decision 9). The path is computed from
// the name, not read from $AF_WORK_DIR: this runs in the Agent, whose own environment belongs to
// no session, and the target may not have started yet.

const handoverPrefix, handoverSuffix = "handover-", ".md"

var handoverNameRe = regexp.MustCompile(`^handover-([A-Za-z0-9._-]+)\.md$`)

// HandoverFileName is the file that records a move out of source.
func HandoverFileName(source string) string { return handoverPrefix + source + handoverSuffix }

// WriteHandoverFile writes md as target's handover-<source>.md and returns its path. The write
// is complete and synced when it returns: a reader never sees a half file, and a crash right
// after cannot leave the target pointing at an empty one.
func WriteHandoverFile(target, source, md string) (string, error) {
	if !session.ValidName(source) {
		return "", fmt.Errorf("handover: bad source name %q", source)
	}
	dir := paths.SessionWorkDir(target)
	if dir == "" || !session.ValidName(target) {
		return "", fmt.Errorf("handover: bad target name %q", target)
	}
	dest := filepath.Join(dir, HandoverFileName(source))
	// A managed target's directory is made here, not at launch. It is marked as the Agent's
	// own only when this call's Mkdir made it (unmarked just means the boot prune keeps it);
	// the parent sync writeFileSynced would have done for a new directory is done here.
	_, created, err := paths.MakeSessionWorkDirCreated(target)
	if err != nil {
		return dest, err
	}
	if err := writeFileSynced(dest, []byte(md)); err != nil {
		return dest, err
	}
	if created {
		return dest, syncDir(filepath.Dir(dir))
	}
	return dest, nil
}

// ListHandoverFiles names the handover files in a session's own directory, sorted. Those are
// the earlier records the session itself depends on (decision 9's chain). A directory that does
// not exist is an empty history; any other failure is an error, because reading it as empty
// would let a move succeed and lose the chain when the source is later deleted.
func ListHandoverFiles(name string) ([]string, error) {
	dir := paths.SessionWorkDir(name)
	if dir == "" {
		return nil, fmt.Errorf("handover: bad session name %q", name)
	}
	if err := requireRealDir(dir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		m := handoverNameRe.FindStringSubmatch(e.Name())
		if m == nil || !e.Type().IsRegular() || !session.ValidName(m[1]) {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out, nil
}

// requireRealDir fails unless dir is a directory and not a symlink: a session directory that
// points elsewhere (a working copy, say) would make the file land, or the chain be read, outside
// the throwaway directory decision 9 puts it in.
func requireRealDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("handover: %s is not a plain directory", dir)
	}
	return nil
}

// CopyHandoverChain copies source's own handover files into target's directory and returns
// their names, so target owns every file it depends on: deleting an earlier session later
// takes nothing the target needs. A file named for target or source is skipped (a retried
// move's own file, never an earlier record).
func CopyHandoverChain(source, target string) ([]string, error) {
	if !session.ValidName(source) || !session.ValidName(target) {
		return nil, errors.New("handover: bad session name")
	}
	srcDir, dstDir := paths.SessionWorkDir(source), paths.SessionWorkDir(target)
	if srcDir == "" || dstDir == "" {
		return nil, errors.New("handover: bad session name")
	}
	names, err := ListHandoverFiles(source)
	if err != nil {
		return nil, err
	}
	var copied []string
	madeDir := false
	for _, n := range names {
		if n == HandoverFileName(target) || n == HandoverFileName(source) {
			continue
		}
		if len(copied) == 0 {
			// The first copy makes target's directory (a Managed target has no launch to do
			// it): through the shared helper, so it is marked as the Agent's own.
			_, created, err := paths.MakeSessionWorkDirCreated(target)
			if err != nil {
				return copied, err
			}
			madeDir = created
		}
		b, err := os.ReadFile(filepath.Join(srcDir, n))
		if err != nil {
			return copied, err
		}
		if err := writeFileSynced(filepath.Join(dstDir, n), b); err != nil {
			return copied, err
		}
		copied = append(copied, n)
	}
	if madeDir {
		if err := syncDir(filepath.Dir(dstDir)); err != nil {
			return copied, err
		}
	}
	return copied, nil
}

// writeFileSynced replaces dest atomically: temp file in the same directory, fsync, rename,
// fsync of the directory (and of its parent when this call created it). Every step's failure
// is returned: a failed directory sync means the new name may not survive a crash.
func writeFileSynced(dest string, b []byte) error {
	dir := filepath.Dir(dest)
	created := false
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		created = true
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := requireRealDir(dir); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".handover-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	fail := func(err error) error {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if _, err := f.Write(b); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := syncDir(dir); err != nil {
		return err
	}
	if created {
		return syncDir(filepath.Dir(dir))
	}
	return nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	if err := d.Sync(); err != nil {
		_ = d.Close()
		return err
	}
	return d.Close()
}

// RemoveHandoverFiles undoes what a move that created no target wrote into target's directory
// (ADR 0115 decision 11.4): the named handover files, then the directory when that empties it,
// since a target that never existed owns nothing else there. Only names of the handover form
// are removed, so a caller cannot reach any other file through it. A file already gone is not
// an error; every other failure is returned, because a file left behind would be the record
// of a move that did not happen, waiting in the directory of a session that does not exist.
func RemoveHandoverFiles(target string, names []string) error {
	dir := paths.SessionWorkDir(target)
	if dir == "" || !session.ValidName(target) {
		return fmt.Errorf("handover: bad target name %q", target)
	}
	if err := requireRealDir(dir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	var errs []error
	for _, n := range names {
		if !handoverNameRe.MatchString(n) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, n)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	if ents, err := os.ReadDir(dir); err == nil && len(ents) == 1 && ents[0].Name() == paths.SessionWorkDirMarker {
		_ = os.Remove(filepath.Join(dir, paths.SessionWorkDirMarker))
	}
	_ = os.Remove(dir) // fails, harmlessly, when anything else is in it
	return nil
}
