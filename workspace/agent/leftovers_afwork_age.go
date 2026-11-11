package main

// Age-based, per-folder removal of unmarked ~/.af-work folders (Settings > Machine).
//
//   GET  /cleanup/af-work?older_than_days=N → the folders a removal would take, one row each
//   POST /cleanup/af-work {older_than_days, names[]} → remove the named ones that still qualify
//
// A folder qualifies when the af-work leftover kind would already take it with the Settings
// button (no session, working copy or live process names it) and it carries no
// paths.SessionWorkDirMarker (members put their own folders there, which is why the boot pass
// leaves them), and nothing inside it changed for N days. Folders of archived sessions are
// not offered: their own expiry is the af-work-archived kind's.
//
// The list the Console shows is a survey. The removal judges again from scratch and, per
// folder, once more under the session-meta lock right before the rename, so a session that
// takes the name or a process that starts writing between the list and the click keeps it.

import (
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
)

const (
	afWorkAgeMinDays = 1
	afWorkAgeMaxDays = 3650
)

// afWorkOldRow is one folder offered for age-based removal.
type afWorkOldRow struct {
	Name     string `json:"name"`
	Bytes    int64  `json:"bytes"`
	Modified string `json:"modified"` // newest mtime inside, RFC 3339
	usagePlace
}

type afWorkOldList struct {
	OlderThanDays int            `json:"older_than_days"`
	Folders       []afWorkOldRow `json:"folders"`
	// Truncated = a walk hit its entry cap; the sizes are lower bounds (a folder whose age
	// could not be established is not listed at all).
	Truncated   bool `json:"truncated,omitempty"`
	Unsupported bool `json:"unsupported,omitempty"`
}

// newestMtime is the latest modification time of dir and everything below it, without
// following a symlink (a link's own time counts, its target is never read). complete=false
// when the budget ran out: the real newest time may be later, so the caller treats the
// folder as fresh.
func newestMtime(dir string, budget *int) (newest time.Time, complete bool) {
	complete = true
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if *budget <= 0 {
			complete = false
			return filepath.SkipAll
		}
		*budget--
		if err != nil {
			// Unreadable: its time is unknown, so the folder cannot be shown to be old.
			complete = false
			return nil
		}
		if info, ierr := d.Info(); ierr == nil {
			if info.ModTime().After(newest) {
				newest = info.ModTime()
			}
		} else {
			complete = false
		}
		return nil
	})
	return newest, complete
}

// afWorkUnmarkedCandidates are the unmarked folders the af-work kind would take on a button
// press and no process is using. Archived sessions' own folders (stillDue set) are left out.
func afWorkUnmarkedCandidates(home string, pins map[string]string) []leftover {
	k, ok := findLeftoverKind("af-work")
	if !ok {
		return nil
	}
	var out []leftover
	for _, l := range k.pruneable(home, pins) {
		if l.needsConfirm && l.stillDue == nil && afWorkNameOK(l.name) {
			out = append(out, l)
		}
	}
	return out
}

// afWorkNameOK: a single path element, not a dot name. The removal never joins anything else.
func afWorkNameOK(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, `/\`) && name == filepath.Base(name)
}

// afWorkRealChild says l is a real directory directly inside a real (non-symlink) ~/.af-work:
// neither the root nor the folder is a link, so nothing below can lead outside of it.
func afWorkRealChild(l leftover) bool {
	if info, err := os.Lstat(l.root); err != nil || !info.IsDir() {
		return false
	}
	info, err := os.Lstat(l.path())
	if err != nil || !info.IsDir() {
		return false
	}
	rootReal, err := filepath.EvalSymlinks(l.root)
	if err != nil {
		return false
	}
	real, err := filepath.EvalSymlinks(l.path())
	return err == nil && real == filepath.Join(rootReal, l.name)
}

// afWorkOldFolders lists the qualifying folders, largest first.
func afWorkOldFolders(home string, pins map[string]string, days int) afWorkOldList {
	out := afWorkOldList{OlderThanDays: days, Folders: []afWorkOldRow{}}
	cutoff := pruneNow().Add(-time.Duration(days) * 24 * time.Hour)
	budget := toolCacheMaxEntries
	for _, l := range afWorkUnmarkedCandidates(home, pins) {
		if !afWorkRealChild(l) {
			continue
		}
		before := budget
		newest, complete := newestMtime(l.path(), &budget)
		if !complete || !newest.Before(cutoff) {
			out.Truncated = out.Truncated || budget <= 0
			continue
		}
		sb := before
		bytes, _ := walkSize(l.path(), &sb)
		budget = min(budget, sb)
		out.Folders = append(out.Folders, afWorkOldRow{Name: l.name, Bytes: bytes,
			Modified: newest.UTC().Format(time.RFC3339), usagePlace: placeOf(l.path())})
	}
	out.Truncated = out.Truncated || budget <= 0
	sort.Slice(out.Folders, func(i, j int) bool { return out.Folders[i].Bytes > out.Folders[j].Bytes })
	return out
}

type afWorkSkip struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

type afWorkOldRemoved struct {
	OlderThanDays int            `json:"older_than_days"`
	Removed       []afWorkOldRow `json:"removed"`
	Skipped       []afWorkSkip   `json:"skipped,omitempty"`
	Failed        string         `json:"failed,omitempty"`
}

// removeOldAfWork removes the named folders that still qualify. A name that does not
// qualify any more (a session took it, a process holds it, something changed inside, it got
// a marker) is skipped with a reason; nothing the caller names is removed on its own say-so.
// The caller holds leftoverMu.
func removeOldAfWork(home string, pins map[string]string, days int, names []string) afWorkOldRemoved {
	res := afWorkOldRemoved{OlderThanDays: days, Removed: []afWorkOldRow{}}
	skip := func(name, why string) { res.Skipped = append(res.Skipped, afWorkSkip{name, why}) }
	cutoff := func() time.Time { return pruneNow().Add(-time.Duration(days) * 24 * time.Hour) }
	listed := map[string]afWorkOldRow{}
	for _, r := range afWorkOldFolders(home, pins, days).Folders {
		listed[r.Name] = r
	}
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		row, ok := listed[name]
		if !afWorkNameOK(name) || !ok {
			skip(name, "not_eligible")
			continue
		}
		var l leftover
		var target string
		var why string
		// Verdict and rename share the lock a session create/restore writes its meta under,
		// so a session cannot take the name between them.
		sessionx.WithSessionMetaLock(func() {
			found := false
			for _, c := range afWorkUnmarkedCandidates(home, pins) {
				if c.name == name {
					l, found = c, true
				}
			}
			if !found || !afWorkRealChild(l) {
				why = "not_eligible"
				return
			}
			if _, err := os.Lstat(filepath.Join(l.path(), paths.SessionWorkDirMarker)); err == nil {
				why = "not_eligible"
				return
			}
			to, err := detachLeftover(l)
			if err != nil {
				why = "rename_failed"
				log.Printf("leftovers: af-work age: detach %s: %v", l.path(), err)
				return
			}
			target = to
		})
		if why != "" {
			skip(name, why)
			continue
		}
		// A write that landed between the walk and the rename shows up on the detached tree;
		// put the folder back rather than delete what was just written.
		budget := toolCacheMaxEntries
		if newest, complete := newestMtime(target, &budget); !complete || !newest.Before(cutoff()) {
			if err := os.Rename(target, l.path()); err != nil {
				log.Printf("leftovers: af-work age: restore %s: %v", l.path(), err)
			}
			skip(name, "changed")
			continue
		}
		if err := removeLeftover(target); err != nil {
			log.Printf("leftovers: af-work age: remove %s: %v", l.path(), err)
			if res.Failed == "" {
				res.Failed = err.Error()
			}
			continue
		}
		log.Printf("leftovers: removed %s (%d KiB, older than %d days)", l.path(), row.Bytes>>10, days)
		res.Removed = append(res.Removed, row)
	}
	return res
}

func parseOlderThanDays(s string) (int, bool) {
	n := 0
	if s == "" || len(s) > 5 {
		return 0, false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, n >= afWorkAgeMinDays && n <= afWorkAgeMaxDays
}

// handleAfWorkOldList (GET /cleanup/af-work).
func handleAfWorkOldList(w http.ResponseWriter, r *http.Request) {
	days, ok := parseOlderThanDays(r.URL.Query().Get("older_than_days"))
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_days", "older_than_days must be a whole number of days, 1 to 3650")
		return
	}
	pins, ok := workspacePins()
	if !ok {
		httpx.WriteJSON(w, http.StatusOK, afWorkOldList{OlderThanDays: days, Folders: []afWorkOldRow{}, Unsupported: true})
		return
	}
	leftoverMu.Lock()
	defer leftoverMu.Unlock()
	httpx.WriteJSON(w, http.StatusOK, afWorkOldFolders(homeDir(), pins, days))
}

// handleAfWorkOldRemove (POST /cleanup/af-work): the body names the folders the member
// confirmed; each is judged again here.
func handleAfWorkOldRemove(w http.ResponseWriter, r *http.Request) {
	var in struct {
		OlderThanDays int      `json:"older_than_days"`
		Names         []string `json:"names"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_body", "expected {older_than_days, names}")
		return
	}
	if in.OlderThanDays < afWorkAgeMinDays || in.OlderThanDays > afWorkAgeMaxDays {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_days", "older_than_days must be a whole number of days, 1 to 3650")
		return
	}
	if len(in.Names) == 0 {
		httpx.WriteErr(w, http.StatusBadRequest, "no_names", "names is empty")
		return
	}
	pins, ok := workspacePins()
	if !ok {
		httpx.WriteErr(w, http.StatusConflict, "unsupported", "not a Workspace image")
		return
	}
	leftoverMu.Lock()
	res := removeOldAfWork(homeDir(), pins, in.OlderThanDays, in.Names)
	leftoverMu.Unlock()
	httpx.WriteJSON(w, http.StatusOK, res)
}
