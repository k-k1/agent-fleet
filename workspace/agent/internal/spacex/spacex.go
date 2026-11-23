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
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
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

// FolderOf answers the space a working directory is, or "" when it is none: dir has to sit
// directly under Root (compared after cleaning, and against Root's resolved path too, so a
// symlinked home or a directory recorded through the other spelling still matches) and carry
// MarkerFile. Spaces are told from repositories by this, never by the folder name: a space and a
// repository can share a basename, and a folder made by hand under ~/spaces is no space.
func FolderOf(dir string) string {
	if dir == "" || !filepath.IsAbs(dir) {
		return ""
	}
	// The last element is checked as it is, never followed: a symlink under Root is no space
	// whichever way the path is spelt. A trailing "/" or "/." names that same element, so strip
	// them first, or ~/spaces/link/. would resolve through the link that ~/spaces/link refuses.
	last := trimSelf(dir)
	f := filepath.Base(last)
	if f != ".." && !ValidFolder(f) {
		return ""
	}
	var parent string
	if f == ".." {
		// The directory above something: resolving the whole path yields a real directory, with
		// no symlink left to follow.
		real, err := filepath.EvalSymlinks(last)
		if err != nil {
			return ""
		}
		parent, f = filepath.Dir(real), filepath.Base(real)
	} else {
		parent = strings.TrimRight(last[:len(last)-len(f)], "/")
		if parent == "" {
			parent = "/"
		}
		if clean := filepath.Clean(parent); clean != parent {
			// Clean drops a ".." lexically, and after a symlink the real parent is another one:
			// ~/spaces/link/../notes runs wherever link's target's parent is. Only a spelling
			// that is not already clean pays for resolving it.
			real, err := filepath.EvalSymlinks(parent)
			if err != nil {
				return ""
			}
			parent = real
		}
	}
	if parent != Root() && parent != realRoot() {
		return ""
	}
	if !ValidFolder(f) || !markerPresent(filepath.Join(parent, f)) {
		return ""
	}
	return f
}

// trimSelf drops trailing "/" and "/." elements, which name the directory before them.
func trimSelf(p string) string {
	for len(p) > 1 {
		switch {
		case strings.HasSuffix(p, "/"):
			p = p[:len(p)-1]
		case strings.HasSuffix(p, "/."):
			// Drop the "." alone: the "/" before it may be the root's own.
			p = p[:len(p)-1]
		default:
			return p
		}
	}
	return p
}

// realRoots caches Root's resolved path per Root. Every session row asks FolderOf, so resolving
// the symlinks each time would put a chain of lstat calls on every poll; home does not move
// under a running Agent. A Root that does not resolve yet (no ~/spaces) is remembered for
// rootMissTTL, so the first space is noticed soon without every row paying for the miss.
var realRoots sync.Map

type rootEntry struct {
	real  string
	until time.Time // zero for a resolved root, which never expires
}

const rootMissTTL = 30 * time.Second

func realRoot() string {
	root := Root()
	if v, ok := realRoots.Load(root); ok {
		e := v.(rootEntry)
		if e.until.IsZero() || time.Now().Before(e.until) {
			return e.real
		}
	}
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		realRoots.Store(root, rootEntry{real: root, until: time.Now().Add(rootMissTTL)})
		return root
	}
	realRoots.Store(root, rootEntry{real: real})
	return real
}

// markerPresent reports whether dir is a real directory (not a symlink) holding a regular
// MarkerFile.
func markerPresent(dir string) bool {
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
		return false
	}
	fi, err := os.Lstat(filepath.Join(dir, MarkerFile))
	return err == nil && fi.Mode().IsRegular()
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
	realRoots.Delete(root) // a root remembered as missing exists now
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
			// writeMarker leaves nothing behind, so the folder Mkdir made is empty and unmarked:
			// no Lookup can have handed it out. os.Remove, not RemoveAll, so a folder something
			// did write to is kept.
			_ = os.Remove(dir)
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
	if err := os.Rename(tmp.Name(), filepath.Join(dir, MarkerFile)); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

// markerMaxBytes bounds what readMarker reads: a display name is at most NameMaxRunes runes.
const markerMaxBytes = 4096

// readMarker reads dir's MarkerFile without following a symlink and only when it is a regular
// file, so a link or a FIFO planted in its place cannot point the read elsewhere or hang the
// list.
func readMarker(dir string) (marker, bool) {
	f, err := os.OpenFile(filepath.Join(dir, MarkerFile), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return marker{}, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return marker{}, false
	}
	b, err := io.ReadAll(io.LimitReader(f, markerMaxBytes+1))
	if err != nil {
		return marker{}, false
	}
	var m marker
	if len(b) > markerMaxBytes || json.Unmarshal(b, &m) != nil {
		// A marker that does not parse still marks the space; only its name is lost.
		m = marker{}
	}
	return m, true
}

// Lookup answers the space in folder, and false when there is none: no such folder, a symlink
// in its place (List skips those too, so both agree on what a space is), or no marker. It
// decides with the same test as FolderOf (markerPresent), so a session row, the list and a
// create naming the folder never disagree; a marker that cannot be read or parsed only loses
// the name.
func Lookup(folder string) (Space, bool) {
	if !ValidFolder(folder) {
		return Space{}, false
	}
	dir := filepath.Join(Root(), folder)
	if !markerPresent(dir) {
		return Space{}, false
	}
	m, _ := readMarker(dir)
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
