package skillcurator

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// The member's way back from the skill curator (#3864): list what it archived and restore one.
// The af MCP tool skill_restore is the only caller (loopback, like the memory entry routes);
// Settings has no skills screen to hang this on, and the curator switch itself has no control.

// API serves the member's restore routes. Roots is the one source the curator's pass and these
// routes share: a request never names a path, so only the member's own roots can be reached.
type API struct {
	// Roots returns the user skills roots, de-duplicated, and the agent kinds sharing each.
	Roots func() (roots []string, kinds map[string][]string)
}

// archivedSkillOut is one archived skill in GET /agents/skills/archive.
type archivedSkillOut struct {
	// Archived is the entry's name in the archive: what the restore request carries.
	Archived string `json:"archived"`
	// Name is the name the skill gets back.
	Name string `json:"name"`
	// ArchivedAt is RFC 3339; empty when the time cannot be read.
	ArchivedAt string `json:"archived_at"`
	// Root is the skills directory it came from, with the home directory written as ~.
	Root string `json:"root"`
	// Kinds are the agent kinds whose skills directory that is.
	Kinds []string `json:"kinds"`
	// NameTaken is true when a live skill of that name exists, so a restore would be refused.
	NameTaken bool `json:"name_taken"`
}

// archivedSkillsOut is GET /agents/skills/archive.
type archivedSkillsOut struct {
	Skills []archivedSkillOut `json:"skills"`
}

// skillRestoreRequest is POST /agents/skills/archive/restore.
type skillRestoreRequest struct {
	// Root is a root as GET returned it (the ~ form).
	Root string `json:"root"`
	// Archived is the entry name GET returned.
	Archived string `json:"archived"`
}

// skillRestoreOut is the restore's answer.
type skillRestoreOut struct {
	Name string `json:"name"`
	Root string `json:"root"`
}

// tildePath writes a path under the home directory with a leading ~.
func tildePath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, err := filepath.Rel(home, p); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
			if rel == "." {
				return "~"
			}
			return "~/" + filepath.ToSlash(rel)
		}
	}
	return p
}

// GET /agents/skills/archive
func (a API) HandleList(w http.ResponseWriter, r *http.Request) {
	roots, kinds := a.Roots()
	out := archivedSkillsOut{Skills: []archivedSkillOut{}}
	for _, root := range roots {
		for _, e := range ListArchived(root) {
			at := ""
			if !e.At.IsZero() {
				at = e.At.UTC().Format(time.RFC3339)
			}
			_, err := os.Lstat(filepath.Join(root, e.Name))
			out.Skills = append(out.Skills, archivedSkillOut{
				Archived: e.Archived, Name: e.Name, ArchivedAt: at,
				Root: tildePath(root), Kinds: kinds[root], NameTaken: err == nil,
			})
		}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// POST /agents/skills/archive/restore
func (a API) HandleRestore(w http.ResponseWriter, r *http.Request) {
	var req skillRestoreRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	// The root is matched against the member's own roots, never opened as given; the entry name
	// is checked again by Restore (a plain archived agent skill, no separators).
	root := ""
	roots, _ := a.Roots()
	for _, c := range roots {
		if tildePath(c) == req.Root || c == req.Root {
			root = c
			break
		}
	}
	if root == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_root", "root is not one of your skills directories; take it from the list")
		return
	}
	var entry *ArchivedSkill
	for _, e := range ListArchived(root) {
		if e.Archived == req.Archived {
			e := e
			entry = &e
			break
		}
	}
	if entry == nil {
		httpx.WriteErr(w, http.StatusNotFound, "not_archived", "no such archived skill; take the name from the list")
		return
	}
	if _, err := os.Lstat(filepath.Join(root, entry.Name)); err == nil {
		httpx.WriteErr(w, http.StatusConflict, "skill_exists", "a skill named "+entry.Name+" already exists; nothing was restored")
		return
	}
	if err := Restore(root, entry.Archived, time.Now()); err != nil {
		if errors.Is(err, ErrSkillExists) { // a skill appeared since the check above
			httpx.WriteErr(w, http.StatusConflict, "skill_exists", "a skill named "+entry.Name+" already exists; nothing was restored")
			return
		}
		httpx.WriteErr(w, http.StatusInternalServerError, "restore_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, skillRestoreOut{Name: entry.Name, Root: tildePath(root)})
}
