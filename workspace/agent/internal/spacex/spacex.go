// Package spacex keeps spaces (ADR 0142 decisions 1-3): folders under ~/spaces where agents run
// without a repository. A space is a directory ~/spaces/<folder> that carries MarkerFile, which
// holds its display name. The folder name is assigned here, once, and never renamed or moved: a
// session's id and Claude's transcript path both derive from the directory, so moving a space
// would break --resume for every session in it.
package spacex

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// MarkerFile is the file that makes a folder under Root a space. A folder without it (one the
// member made by hand) is not listed.
const MarkerFile = ".af-space.json"

// NameMaxRunes bounds a display name. It matches the session title limit's order of magnitude
// while leaving room for a sentence-long name.
const NameMaxRunes = 100

// slugMaxLen bounds the readable half of a folder name, so the folder (and every path an agent
// prints from inside it) stays short.
const slugMaxLen = 32

// Space is one space as the API reports it.
type Space struct {
	// Folder is the directory name under Root: the space's identity.
	Folder string `json:"folder"`
	// Name is the display name; "" when none was given (the Console then shows Folder).
	Name string `json:"name"`
	// Dir is the absolute path of the space.
	Dir       string `json:"dir"`
	CreatedAt string `json:"createdAt,omitempty"`
}

// marker is MarkerFile's content.
type marker struct {
	Name      string `json:"name"`
	CreatedAt string `json:"createdAt,omitempty"`
}

// Root is ~/spaces. Recreate keeps it (it is outside ~/repos); Clean deletes it with the rest of
// the home directory.
func Root() string { return filepath.Join(paths.HomeDir(), "spaces") }

// FolderOf answers the space folder a working directory is, or "" when dir is not directly
// under Root. Spaces are told from repositories by this root, never by the folder name: a space
// and a repository can share a basename.
func FolderOf(dir string) string {
	if dir == "" || !filepath.IsAbs(dir) {
		return ""
	}
	dir = filepath.Clean(dir)
	if filepath.Dir(dir) != Root() {
		return ""
	}
	f := filepath.Base(dir)
	if !ValidFolder(f) {
		return ""
	}
	return f
}

// ValidFolder reports whether f can name a space folder: lower-case letters, digits and '-',
// starting with a letter or digit. Every folder Create makes passes; the check keeps a request's
// folder from reaching outside Root.
func ValidFolder(f string) bool {
	if f == "" || len(f) > slugMaxLen+16 {
		return false
	}
	for i, r := range f {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '-' && i > 0:
		default:
			return false
		}
	}
	return true
}

// CleanName trims a display name and refuses one that is too long or holds control characters.
func CleanName(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > NameMaxRunes {
		return "", false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return "", false
		}
	}
	return s, true
}

// slugOf is the readable half of a folder name: the ASCII letters and digits of name, lower-cased,
// with every other run collapsed into one '-'. "" when the name has none (a Japanese name, say).
func slugOf(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
			if b.Len() >= slugMaxLen {
				break
			}
			continue
		}
		dash = true
	}
	return strings.TrimRight(b.String(), "-")
}

// randSuffix makes the unique half of a folder name. A variable so a test can force a collision.
var randSuffix = func() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	var raw [6]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic(err) // crypto/rand does not fail on Linux; a folder name must never repeat silently
	}
	out := make([]byte, len(raw))
	for i, c := range raw {
		out[i] = alphabet[int(c)%len(alphabet)]
	}
	return string(out)
}

// Create makes a new space with the given display name (already cleaned) and answers it. The
// folder is "<slug>-<random>", or "space-<random>" when the name has no ASCII letters; it is
// claimed with os.Mkdir, so two creates can never share one.
func Create(name string) (Space, error) {
	root := Root()
	if err := os.MkdirAll(root, 0o755); err != nil {
		return Space{}, err
	}
	base := slugOf(name)
	if base == "" {
		base = "space"
	}
	for attempt := 0; attempt < 8; attempt++ {
		folder := base + "-" + randSuffix()
		dir := filepath.Join(root, folder)
		if err := os.Mkdir(dir, 0o755); err != nil {
			if errors.Is(err, fs.ErrExist) {
				continue
			}
			return Space{}, err
		}
		m := marker{Name: name, CreatedAt: time.Now().UTC().Format(time.RFC3339)}
		if err := writeMarker(dir, m); err != nil {
			// The folder is ours (Mkdir made it) and still empty but for a partial marker, so
			// removing it leaves nothing behind for the next attempt to trip over.
			_ = os.RemoveAll(dir)
			return Space{}, err
		}
		return Space{Folder: folder, Name: m.Name, Dir: dir, CreatedAt: m.CreatedAt}, nil
	}
	return Space{}, errors.New("could not find a free space folder name")
}

// writeMarker writes MarkerFile through a temporary file and a rename, so a reader never sees a
// half-written one.
func writeMarker(dir string, m marker) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".af-space-*.tmp")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, MarkerFile))
}

// Lookup answers the space in folder, and false when there is none (no such folder, or a folder
// without the marker).
func Lookup(folder string) (Space, bool) {
	if !ValidFolder(folder) {
		return Space{}, false
	}
	dir := filepath.Join(Root(), folder)
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return Space{}, false
	}
	b, err := os.ReadFile(filepath.Join(dir, MarkerFile))
	if err != nil {
		return Space{}, false
	}
	var m marker
	if json.Unmarshal(b, &m) != nil {
		// A marker that does not parse still marks the space; only its name is lost.
		m = marker{}
	}
	name, ok := CleanName(m.Name)
	if !ok {
		name = ""
	}
	return Space{Folder: folder, Name: name, Dir: dir, CreatedAt: m.CreatedAt}, true
}

// List answers every space, newest first (by creation time, then folder). A missing Root is no
// spaces, not an error.
func List() ([]Space, error) {
	ents, err := os.ReadDir(Root())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return []Space{}, nil
		}
		return nil, err
	}
	out := []Space{}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		if sp, ok := Lookup(e.Name()); ok {
			out = append(out, sp)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt > out[j].CreatedAt
		}
		return out[i].Folder < out[j].Folder
	})
	return out, nil
}

// RemoveIfUnused removes a space that holds nothing but its marker. A create that made a space
// and then failed calls it, so a refused launch leaves no empty space behind; a space anything
// was written to is kept.
func RemoveIfUnused(dir string) {
	if FolderOf(dir) == "" {
		return
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if e.Name() != MarkerFile {
			return
		}
	}
	_ = os.RemoveAll(dir)
}

// listResponse is GET /spaces.
type listResponse struct {
	// Root is the absolute path of ~/spaces, so the Console can tell a space session by its
	// directory without knowing the home directory.
	Root   string  `json:"root"`
	Spaces []Space `json:"spaces"`
}

// HandleList is GET /spaces.
func HandleList(w http.ResponseWriter, r *http.Request) {
	spaces, err := List()
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, listResponse{Root: Root(), Spaces: spaces})
}

// createRequest is POST /spaces.
type createRequest struct {
	Name string `json:"name"`
}

// createResponse is POST /spaces' answer.
type createResponse struct {
	Space Space `json:"space"`
}

// HandleCreate is POST /spaces: a new, empty space.
func HandleCreate(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	name, ok := CleanName(req.Name)
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name",
			fmt.Sprintf("name is too long (max %d) or contains control characters", NameMaxRunes))
		return
	}
	sp, err := Create(name)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "create_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, createResponse{Space: sp})
}
