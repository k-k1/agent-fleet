// Package spacex keeps spaces (ADR 0142 decisions 1-3): folders under ~/spaces where agents run
// without a repository. A space is a directory ~/spaces/<folder> that carries MarkerFile, which
// holds its display name. The folder name is assigned here, once, and never renamed or moved: a
// session's id and Claude's transcript path both derive from the directory, so moving a space
// would break --resume for every session in it.
package spacex

import (
	"bytes"
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

// ErrNotFound is what Rename answers for a folder that is no space (never made, removed, or a
// symlink in its place).
var ErrNotFound = errors.New("no such space")

// ErrChanged is what Rename answers when the space or its marker was replaced while the new name
// was being written: nothing was changed, and asking again reads the new state.
var ErrChanged = errors.New("the space changed while it was being renamed")

// ErrMarkerTooBig is what Rename answers for a marker larger than any name needs: it cannot be
// told from a valid one, so it is not overwritten.
var ErrMarkerTooBig = errors.New("the space's marker file is too big to be rewritten")

// renameMu serialises the renames this Agent makes, so two of them never interleave their
// read-modify-write of one marker.
var renameMu sync.Mutex

// beforePublish runs between the last read of a rename and its final check. A variable so a test
// can change the space at exactly that point.
var beforePublish = func() {}

// Rename changes the display name of the space in folder to name (already cleaned) and answers
// the space as it now is. Only the marker is rewritten: the folder keeps its name, so the
// sessions in it keep their ids and their transcripts (see the package comment).
//
// What the rename may touch is decided on the opened files, not on paths checked earlier:
//   - the folder has to be a real directory under Root (a link, even one that stays inside Root,
//     is no space, as Lookup says), and the directory opened is the one that was checked;
//   - everything inside runs in an os.Root on that directory, and the marker is opened without
//     following a link and without blocking, so a symlink or FIFO an agent put there is refused
//     rather than read or written through;
//   - the new marker is written to a temporary file in the space and published by a rename, so a
//     reader sees the old name or the new one and a crash leaves the old one;
//   - just before publishing, the folder and the marker are checked to be the ones that were read:
//     a space removed meanwhile stays removed (ErrNotFound, no marker is recreated) and one
//     replaced is refused (ErrChanged). Only the instant between that check and the rename is not
//     covered: nothing short of a lock the agents do not take could.
//
// A marker that is missing or no regular file is ErrNotFound; one that cannot be read is an error
// and nothing is written; only one that was read and does not parse is repaired.
func Rename(folder, name string) (Space, error) {
	if !ValidFolder(folder) {
		return Space{}, ErrNotFound
	}
	renameMu.Lock()
	defer renameMu.Unlock()
	// Go opens a Root without O_DIRECTORY or O_NONBLOCK, so a FIFO in the place of ~/spaces would
	// block here (with renameMu held): refuse anything that is no directory first.
	if fi, err := os.Stat(Root()); err != nil {
		return Space{}, notExistIsNotFound(err)
	} else if !fi.IsDir() {
		return Space{}, ErrNotFound
	}
	top, err := os.OpenRoot(Root())
	if err != nil {
		return Space{}, notExistIsNotFound(err)
	}
	defer top.Close()
	topFi, err := top.Stat(".")
	if err != nil {
		return Space{}, err
	}
	dirFi, err := top.Lstat(folder)
	if err != nil {
		return Space{}, notExistIsNotFound(err)
	}
	if !dirFi.IsDir() {
		return Space{}, ErrNotFound
	}
	sp, err := top.OpenRoot(folder)
	if err != nil {
		return Space{}, notExistIsNotFound(err)
	}
	defer sp.Close()
	if openFi, err := sp.Stat("."); err != nil || !os.SameFile(dirFi, openFi) {
		return Space{}, ErrChanged
	}
	markFi, err := sp.Lstat(MarkerFile)
	if err != nil {
		return Space{}, notExistIsNotFound(err)
	}
	if !markFi.Mode().IsRegular() {
		return Space{}, ErrNotFound
	}
	fields, raw, err := readMarkerIn(sp, markFi)
	if err != nil {
		return Space{}, err
	}
	// Every other field of the marker is kept as it is: only the name is the rename's.
	nameJSON, err := json.Marshal(name)
	if err != nil {
		return Space{}, err
	}
	fields["name"] = nameJSON
	// The readers decode name and createdAt as strings and give up on the whole marker when one
	// is not, so a createdAt of another type cannot be kept: dropping it is what lets the new
	// name be read back.
	var createdAt string
	if err := json.Unmarshal(fields["createdAt"], &createdAt); err != nil {
		delete(fields, "createdAt")
		createdAt = ""
	}
	b, err := json.Marshal(fields)
	if err != nil {
		return Space{}, err
	}
	b = append(b, '\n')
	// What is written has to be readable again by the same limit, or the rename would succeed and
	// the next Lookup lose both names. Nothing has been created yet, so the old marker stays.
	if len(b) > markerMaxBytes {
		return Space{}, ErrMarkerTooBig
	}
	tmp, err := writeTempIn(sp, b)
	if err != nil {
		return Space{}, err
	}
	beforePublish()
	if err := checkUnchanged(top, sp, folder, topFi, dirFi, markFi, raw); err != nil {
		_ = sp.Remove(tmp)
		return Space{}, err
	}
	if err := sp.Rename(tmp, MarkerFile); err != nil {
		_ = sp.Remove(tmp)
		return Space{}, err
	}
	return Space{Folder: folder, Name: name, Dir: filepath.Join(Root(), folder), CreatedAt: createdAt}, nil
}

// checkUnchanged says whether ~/spaces, folder under it and the marker in sp are still the
// directories and the file Rename started from, and the marker still holds the bytes that were
// read (raw): a writer that rewrites it in place keeps its inode, and one that restores times
// keeps its size and modification time too, so only the content tells.
func checkUnchanged(top, sp *os.Root, folder string, topFi, dirFi, markFi os.FileInfo, raw []byte) error {
	// ~/spaces itself may have been moved or replaced: top still names the old directory.
	if now, err := os.Stat(Root()); err != nil {
		return notExistIsNotFound(err)
	} else if !os.SameFile(topFi, now) {
		return ErrChanged
	}
	cur, err := top.Lstat(folder)
	if err != nil {
		return notExistIsNotFound(err)
	}
	if !os.SameFile(dirFi, cur) {
		return ErrChanged
	}
	mark, err := sp.Lstat(MarkerFile)
	if err != nil {
		return notExistIsNotFound(err)
	}
	if !os.SameFile(markFi, mark) {
		return ErrChanged
	}
	again, _, err := readMarkerBytes(sp, markFi)
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, again) {
		return ErrChanged
	}
	return nil
}

// notExistIsNotFound answers ErrNotFound for "no such file or directory" and leaves every other
// error (permission, I/O) as it is, so a failure is never reported as a missing space.
func notExistIsNotFound(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	}
	return err
}

// readMarkerBytes reads sp's MarkerFile the way readMarker reads one by path: opened without
// following a link and without blocking, and read only when the opened file is the regular file
// want describes (Root.OpenFile re-resolves a link inside the space when O_NOFOLLOW is refused,
// so the identity of what was opened is checked, not assumed), at most markerMaxBytes. A missing
// file or one that is no regular file is ErrNotFound, a different one ErrChanged, a read that
// fails is that error, and a marker over the limit cannot be told from a valid one, so it is
// left alone (ErrMarkerTooBig).
func readMarkerBytes(sp *os.Root, want os.FileInfo) ([]byte, os.FileInfo, error) {
	f, err := sp.OpenFile(MarkerFile, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, notExistIsNotFound(err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, nil, ErrNotFound
	}
	if !os.SameFile(want, fi) {
		return nil, nil, ErrChanged
	}
	b, err := io.ReadAll(io.LimitReader(f, markerMaxBytes+1))
	if err != nil {
		return nil, nil, err
	}
	if len(b) > markerMaxBytes {
		return nil, nil, ErrMarkerTooBig
	}
	return b, fi, nil
}

// readMarkerIn answers the marker's fields as raw JSON values and the bytes they came from. One
// that was read whole and is no JSON object gives no fields, which a rename repairs.
func readMarkerIn(sp *os.Root, want os.FileInfo) (fields map[string]json.RawMessage, raw []byte, err error) {
	raw, _, err = readMarkerBytes(sp, want)
	if err != nil {
		return nil, nil, err
	}
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		fields = map[string]json.RawMessage{}
	}
	return fields, raw, nil
}

// writeTempIn writes data to a new file in sp (created exclusively, so nothing is overwritten)
// and answers its name; the file is removed again when the write fails.
func writeTempIn(sp *os.Root, data []byte) (string, error) {
	for attempt := 0; ; attempt++ {
		tmp := fmt.Sprintf(".af-space-%s.tmp", randSuffix())
		f, err := sp.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			if errors.Is(err, fs.ErrExist) && attempt < 8 {
				continue
			}
			return "", err
		}
		_, werr := f.Write(data)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			_ = sp.Remove(tmp)
			return "", werr
		}
		return tmp, nil
	}
}

// renameRequest is PATCH /spaces/{folder}. Name is a pointer so a body without one (or with
// null) is told from an explicit empty name, which clears the display name.
type renameRequest struct {
	Name *string `json:"name"`
}

// HandleRename is PATCH /spaces/{folder}: the display name only (the folder never changes).
func HandleRename(w http.ResponseWriter, r *http.Request) {
	folder := r.PathValue("folder")
	var req renameRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	var name string
	ok := req.Name != nil
	if ok {
		name, ok = CleanName(*req.Name)
	}
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name",
			fmt.Sprintf("name is required, at most %d characters, with no control characters", NameMaxRunes))
		return
	}
	sp, err := Rename(folder, name)
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.WriteErr(w, http.StatusNotFound, "space_not_found", "no such space")
	case errors.Is(err, ErrChanged):
		httpx.WriteErr(w, http.StatusConflict, "space_changed", err.Error())
	case err != nil:
		httpx.WriteErr(w, http.StatusInternalServerError, "rename_failed", err.Error())
	default:
		httpx.WriteJSON(w, http.StatusOK, createResponse{Space: sp})
	}
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

// AttachmentsDir is the folder inside a space that holds the files attached to its sessions'
// first prompts, so the member sees them next to their own work.
const AttachmentsDir = "attachments"

// attachmentNameMaxBytes bounds the name a copied attachment keeps. A file name is limited in
// bytes (255 on the usual file systems), not characters, and a Japanese character takes three;
// the margin leaves room for the "-2" a name collision adds.
const attachmentNameMaxBytes = 200

// attachmentName reduces a client-supplied file name to one safe, readable basename: directory
// parts, control characters and leading dots dropped, a long name cut (at a character boundary,
// in one pass) so its extension survives. Unlike the agent-facing upload name, Unicode is kept:
// this is the name the member sees, and a Japanese file name must not collapse to its extension.
func attachmentName(name string) string {
	base := name[strings.LastIndexAny(name, `/\`)+1:]
	var b strings.Builder
	for _, r := range base {
		if r < 0x20 || r == 0x7f || r == '/' || r == '\\' {
			continue
		}
		b.WriteRune(r)
	}
	s := strings.Trim(strings.TrimLeft(b.String(), "."), " ")
	if s == "" {
		return "file"
	}
	if len(s) > attachmentNameMaxBytes {
		ext := filepath.Ext(s)
		if len(ext) > 32 {
			ext = ""
		}
		cut := attachmentNameMaxBytes - len(ext)
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + ext
	}
	return s
}

// CopyAttachment copies src into the attachments folder of the space in folder (as FolderOf
// answered it) under name (see attachmentName) and answers the copy's absolute path. A name
// already taken keeps both files: the copy gets "-2", "-3" ... before its extension, claimed with
// O_EXCL so two copies at once never share one.
//
// Every step runs inside an os.Root opened on the space, so the folder, the marker and the
// attachments folder are all resolved relative to it and a symlink that points outside the space
// is refused whenever it was put there, however the check and the write interleave with an agent
// that is rewriting the space. src is the bytes the caller already holds open: the copy is of
// those, not of a path opened again.
func CopyAttachment(folder string, src io.Reader, name string) (string, error) {
	if !ValidFolder(folder) {
		return "", errors.New("not a space folder")
	}
	top, err := os.OpenRoot(Root())
	if err != nil {
		return "", err
	}
	defer top.Close()
	sp, err := top.OpenRoot(folder)
	if err != nil {
		return "", err
	}
	defer sp.Close()
	if fi, err := sp.Lstat(MarkerFile); err != nil || !fi.Mode().IsRegular() {
		return "", errors.New("the space was removed")
	}
	fi, err := sp.Lstat(AttachmentsDir)
	if errors.Is(err, fs.ErrNotExist) {
		if err = sp.Mkdir(AttachmentsDir, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return "", err
		}
		fi, err = sp.Lstat(AttachmentsDir)
	}
	if err != nil {
		return "", err
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("%s is not a folder", AttachmentsDir)
	}
	base := attachmentName(name)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	for n := 1; n < 1000; n++ {
		cand := base
		if n > 1 {
			cand = fmt.Sprintf("%s-%d%s", stem, n, ext)
		}
		rel := AttachmentsDir + "/" + cand
		out, err := sp.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			if errors.Is(err, fs.ErrExist) {
				continue
			}
			return "", err
		}
		if _, err := io.Copy(out, src); err != nil {
			out.Close()
			_ = sp.Remove(rel)
			return "", err
		}
		if err := out.Close(); err != nil {
			_ = sp.Remove(rel)
			return "", err
		}
		return filepath.Join(Root(), folder, rel), nil
	}
	return "", errors.New("could not find a free attachment name")
}
