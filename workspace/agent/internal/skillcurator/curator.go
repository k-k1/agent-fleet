// Package skillcurator ages out skills that an agent wrote, never skills that a person wrote
// (#1562; hermes' curator is the reference). A skill unused for StaleAfter is marked stale, and
// one unused for ArchiveAfter is moved to `<root>/.archive/`; nothing is ever deleted, and
// Restore puts an archived skill back.
//
// Ownership is the whole safety argument. The skills roots are shared with the user's own skills
// ($CLAUDE_CONFIG_DIR/skills among them), so a directory is touched only when ALL of these hold:
// its name starts with Prefix, it is a real directory (not a link), and its regular SKILL.md
// carries AgentMarker right after the frontmatter. fleetskills' own `af-` notes, a user's
// `afa-foo` without the marker, and a doc that merely quotes the marker are all left alone.
//
// Status: nothing in Agent Fleet writes marked skills yet (ADR 0117 decision 7 was never built;
// ADR 0130 step 4, #3498, is where promotion to skills lands). Until a writer exists the curator
// finds no candidates and does nothing.
//
// "Used" is a file timestamp, not a transcript reading: the later of the mtime of `.last-used`
// (a writer or a hook that records a use touches it) and of SKILL.md. Without a recorded use the
// age is therefore the age of the last write. A timestamp in the future counts as "now": a clock
// that jumped must not archive anything.
package skillcurator

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/fleetskills"
)

const (
	// AgentMarker is the ownership line of an agent-authored skill, the counterpart of
	// fleetskills.Marker. Its text differs, so neither package's check accepts the other's.
	AgentMarker = "<!-- agent-fleet:agent-skill — written by an agent; the Agent may mark it stale and archive it when unused -->"
	// Prefix is the directory-name prefix of an agent-authored skill.
	Prefix = "afa-"

	PinnedFile   = ".pinned"    // present: the skill is exempt from curation
	LastUsedFile = ".last-used" // its mtime records the last use
	StaleFile    = ".stale"     // written by the curator when it marks a skill stale
	OriginFile   = ".origin"    // written into an archived skill: the name it had in the root
	ArchiveDir   = ".archive"   // under the skills root; not a skill directory itself
)

// Config holds the thresholds.
type Config struct {
	StaleAfter, ArchiveAfter time.Duration
}

// DefaultConfig is hermes' 14 and 30 days.
func DefaultConfig() Config {
	return Config{StaleAfter: 14 * 24 * time.Hour, ArchiveAfter: 30 * 24 * time.Hour}
}

// Result lists what one pass did, by skill directory name.
type Result struct {
	Stale, Archived, Revived []string
	// Stopped: cont said no before the pass finished.
	Stopped bool
}

// Candidate reports whether root/name is a skill the curator may act on.
func Candidate(root, name string) bool {
	if !strings.HasPrefix(name, Prefix) || name == Prefix {
		return false
	}
	dir := filepath.Join(root, name)
	if st, err := os.Lstat(dir); err != nil || !st.IsDir() { // Lstat: a symlinked dir is not ours
		return false
	}
	return fleetskills.OwnedBy(filepath.Join(dir, "SKILL.md"), AgentMarker)
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

// lastUsed is the later of the two timestamps; ok is false when SKILL.md cannot be read.
func lastUsed(dir string) (t time.Time, ok bool) {
	st, err := os.Stat(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return time.Time{}, false
	}
	t = st.ModTime()
	if u, err := os.Lstat(filepath.Join(dir, LastUsedFile)); err == nil && u.ModTime().After(t) {
		t = u.ModTime()
	}
	return t, true
}

// Run is one pass over a skills root. cont is asked before every skill; false stops the pass
// (the workspace stopped being idle). A skill that fails is logged and skipped.
func Run(root string, now time.Time, c Config, cont func() bool) Result {
	var res Result
	ents, err := os.ReadDir(root)
	if err != nil {
		return res
	}
	for _, e := range ents {
		if cont != nil && !cont() {
			res.Stopped = true
			break
		}
		name := e.Name()
		if !Candidate(root, name) {
			continue
		}
		dir := filepath.Join(root, name)
		if exists(filepath.Join(dir, PinnedFile)) {
			// Pinned means exempt, so a mark left from before the pin is cleared too.
			_ = os.Remove(filepath.Join(dir, StaleFile))
			continue
		}
		used, ok := lastUsed(dir)
		if !ok {
			continue
		}
		age := now.Sub(used) // negative for a future timestamp: never stale
		switch {
		case age >= c.ArchiveAfter:
			if _, err := archive(root, name); err != nil {
				log.Printf("skill curator: archiving %s failed: %v", name, err)
				continue
			}
			res.Archived = append(res.Archived, name)
		case age >= c.StaleAfter:
			if !exists(filepath.Join(dir, StaleFile)) {
				if err := os.WriteFile(filepath.Join(dir, StaleFile), []byte(now.UTC().Format(time.RFC3339)+"\n"), 0o644); err != nil {
					log.Printf("skill curator: marking %s stale failed: %v", name, err)
					continue
				}
				res.Stale = append(res.Stale, name)
			}
		default:
			// Used again since it was marked: the mark is stale itself.
			if exists(filepath.Join(dir, StaleFile)) && os.Remove(filepath.Join(dir, StaleFile)) == nil {
				res.Revived = append(res.Revived, name)
			}
		}
	}
	return res
}

// archive moves root/name to root/.archive/name (a numeric suffix on a clash). The rename is
// atomic on one filesystem, so a reader sees the whole skill or none of it.
func archive(root, name string) (string, error) {
	ad := filepath.Join(root, ArchiveDir)
	if err := os.MkdirAll(ad, 0o755); err != nil {
		return "", err
	}
	if err := realArchiveDir(ad); err != nil {
		return "", err
	}
	dst := filepath.Join(ad, name)
	for i := 2; exists(dst); i++ {
		dst = filepath.Join(ad, fmt.Sprintf("%s.%d", name, i))
	}
	if err := os.Rename(filepath.Join(root, name), dst); err != nil {
		return "", err
	}
	// The clash suffix is not part of the name, so the name is recorded rather than guessed at
	// restore (a skill may legitimately be called afa-a.2). Best effort: without it Restore
	// falls back to the archived name as it is.
	_ = os.WriteFile(filepath.Join(dst, OriginFile), []byte(name+"\n"), 0o644)
	return dst, nil
}

// realArchiveDir refuses an archive that is a link or not a directory: MkdirAll follows a link,
// and a rename through it would carry the skill out of the root.
func realArchiveDir(ad string) error {
	st, err := os.Lstat(ad)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("%s is not a plain directory", ad)
	}
	return nil
}

// Archived lists the names under root/.archive, for a restore.
func Archived(root string) []string {
	ents, _ := os.ReadDir(filepath.Join(root, ArchiveDir))
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

// ArchivedSkill is one entry of a root's archive, as a member sees it.
type ArchivedSkill struct {
	// Archived is the entry's name under .archive/ (it may carry a clash suffix); this is what
	// Restore takes.
	Archived string
	// Name is the name the skill will have again: its .origin record, else Archived.
	Name string
	// At is when it was archived: the mtime of its .origin record, else of the directory.
	At time.Time
}

// originName is the name a restore gives the skill; Restore and ListArchived must agree on it.
func originName(src, archived string) string {
	if b, err := os.ReadFile(filepath.Join(src, OriginFile)); err == nil {
		if o := strings.TrimSpace(string(b)); o != "" && filepath.Base(o) == o && strings.HasPrefix(o, Prefix) {
			return o
		}
	}
	return archived
}

// ListArchived lists what Restore would accept under root/.archive, oldest archive first. Only
// entries Restore itself would take are listed (a plain directory carrying the agent marker),
// so a stray folder a person dropped in the archive is not offered. A missing or non-plain
// archive directory lists nothing.
func ListArchived(root string) []ArchivedSkill {
	ad := filepath.Join(root, ArchiveDir)
	if realArchiveDir(ad) != nil {
		return nil
	}
	var out []ArchivedSkill
	for _, n := range Archived(root) {
		if !Candidate(ad, n) {
			continue
		}
		src := filepath.Join(ad, n)
		at := time.Time{}
		if st, err := os.Lstat(filepath.Join(src, OriginFile)); err == nil {
			at = st.ModTime()
		} else if st, err := os.Lstat(src); err == nil {
			at = st.ModTime()
		}
		out = append(out, ArchivedSkill{Archived: n, Name: originName(src, n), At: at})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// Restore moves an archived skill back to root under its original name (from its .origin
// record) and clears its stale mark. It refuses when that name is taken, when the archive is not
// a plain directory, or when the archived entry no longer carries the marker. The skill gets a
// fresh last-use time so the next pass does not archive it again at once.
func Restore(root, archived string, now time.Time) error {
	ad := filepath.Join(root, ArchiveDir)
	if err := realArchiveDir(ad); err != nil {
		return err
	}
	src := filepath.Join(ad, archived)
	if filepath.Base(archived) != archived || !Candidate(ad, archived) {
		return fmt.Errorf("%q is not an archived agent skill", archived)
	}
	name := originName(src, archived)
	dst := filepath.Join(root, name)
	if exists(dst) {
		return fmt.Errorf("%s already exists", name)
	}
	_ = os.Remove(filepath.Join(src, StaleFile))
	_ = os.Remove(filepath.Join(src, OriginFile))
	if err := os.Rename(src, dst); err != nil {
		return err
	}
	// Remove then create exclusively: a .last-used that is a link must not be written through.
	lu := filepath.Join(dst, LastUsedFile)
	if err := os.Remove(lu); err != nil && !os.IsNotExist(err) {
		return err
	}
	f, err := os.OpenFile(lu, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chtimes(lu, now, now)
}
