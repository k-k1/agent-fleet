package imagegen

// The picture history (ADR 0100 decision 9): generated/console/history.jsonl, one line per
// picture the job queue stored, appended when its sidecar is written. It is an index and not a
// record — the sidecars are the record — so when it is missing it is rebuilt from them.
//
// A picture written into a member's own out_dir sits under the browse root rather than here, so
// a rebuild cannot find it; its line survives only in the file. That is the one thing the index
// knows that the sidecars under the generated folder do not.

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

func historyPath() string { return filepath.Join(ConsoleDir(), "history.jsonl") }

var historyMu sync.Mutex

// historyIndex is the file as read: Items[i] is line i, and a line that is not a picture (the
// header, or one that does not parse) is a zero item, so a line keeps its number.
//
// Generation is the header's random id, written when the file is (re)built. A rebuild renumbers
// the lines — it is sorted by time and cannot see the pictures in a member's out_dir — so a
// cursor or a read position taken from one generation means nothing in the next.
type historyIndex struct {
	Generation string
	Items      []HistoryItem
}

type historyHeader struct {
	Generation string `json:"generation"`
}

// appendHistory adds one picture. A failure is logged by the caller and nothing else: the
// picture and its sidecar are already on disk, and the index can be rebuilt from them.
//
// A missing index is rebuilt first, under the same lock: an append that created the file would
// leave it holding one line, and every picture before it would drop out of the list for good.
func appendHistory(item HistoryItem) error {
	b, err := json.Marshal(item)
	if err != nil {
		return err
	}
	historyMu.Lock()
	defer historyMu.Unlock()
	if _, err := os.Stat(historyPath()); errors.Is(err, os.ErrNotExist) {
		idx, err := rebuildHistoryLocked()
		if err != nil {
			return err
		}
		for _, it := range idx.Items {
			if it.Path == item.Path {
				return nil
			}
		}
	}
	return appendLines(historyPath(), append(b, '\n'))
}

func historyItemOf(path string, p ImageProps) HistoryItem {
	return HistoryItem{Path: path, Studio: p.Studio, Version: p.Version, CreatedAt: p.CreatedAt, Trial: p.Trial}
}

// rebuildHistoryLocked writes a new generation from the sidecars. The caller holds historyMu.
func rebuildHistoryLocked() (historyIndex, error) {
	var g [8]byte
	_, _ = rand.Read(g[:])
	idx := historyIndex{Generation: hex.EncodeToString(g[:]), Items: []HistoryItem{{}}}
	idx.Items = append(idx.Items, scanSidecars()...)
	var buf bytes.Buffer
	h, _ := json.Marshal(historyHeader{Generation: idx.Generation})
	buf.Write(h)
	buf.WriteByte('\n')
	for _, it := range idx.Items[1:] {
		b, _ := json.Marshal(it)
		buf.Write(b)
		buf.WriteByte('\n')
	}
	if err := os.MkdirAll(ConsoleDir(), 0o700); err != nil {
		return idx, err
	}
	return idx, appendLines(historyPath(), buf.Bytes())
}

// readHistory is the index, rebuilding the file from the sidecars when it does not exist.
func readHistory() historyIndex {
	historyMu.Lock()
	defer historyMu.Unlock()
	f, err := os.Open(historyPath())
	if errors.Is(err, os.ErrNotExist) {
		idx, _ := rebuildHistoryLocked()
		return idx
	}
	if err != nil {
		return historyIndex{}
	}
	defer f.Close()
	var idx historyIndex
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for n := 0; sc.Scan(); n++ {
		var it HistoryItem
		if json.Unmarshal(sc.Bytes(), &it) != nil || it.Path == "" {
			var h historyHeader
			if n == 0 && json.Unmarshal(sc.Bytes(), &h) == nil {
				idx.Generation = h.Generation
			}
			it = HistoryItem{}
		}
		idx.Items = append(idx.Items, it)
	}
	return idx
}

// scanSidecars walks the Console's generated folder for the pictures the queue recorded, oldest
// first. The input sets are skipped: they are copies of references, not pictures made here.
func scanSidecars() []HistoryItem {
	var out []HistoryItem
	root := ConsoleDir()
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p == ConsoleInputsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		img, ok := strings.CutSuffix(p, sidecarExt)
		if !ok {
			return nil
		}
		if _, err := os.Stat(img); err != nil {
			return nil
		}
		props, ok := readSidecar(img)
		if !ok || props.Job == "" {
			return nil
		}
		out = append(out, historyItemOf(img, props))
		return nil
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out
}

// HandleHistory answers GET /imagegen/history?studio=&before=&limit=: the pictures, newest
// first. The cursor is "<generation>:<line>" — the file is append-only, so a line keeps its
// number within a generation — and a cursor from another generation, or past the end, answers
// an empty page rather than a page of the wrong pictures. A picture deleted since (the trial
// sweep, or the member) is left out rather than shown broken.
func HandleHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	studio := q.Get("studio")
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 200)
	idx := readHistory()
	end := len(idx.Items)
	page := HistoryPage{Items: []HistoryItem{}}
	if c := q.Get("before"); c != "" {
		gen, line, _ := strings.Cut(c, ":")
		b, err := strconv.Atoi(line)
		if gen != idx.Generation || err != nil || b < 0 || b > end {
			httpx.WriteJSON(w, http.StatusOK, page)
			return
		}
		end = b
	}
	i := end - 1
	for ; i >= 0 && len(page.Items) < limit; i-- {
		it := idx.Items[i]
		if it.Path == "" || studio != "" && it.Studio != studio {
			continue
		}
		if _, err := os.Stat(it.Path); err != nil {
			continue
		}
		page.Items = append(page.Items, it)
	}
	if i >= 0 {
		page.Before = idx.Generation + ":" + strconv.Itoa(i+1)
	}
	httpx.WriteJSON(w, http.StatusOK, page)
}
