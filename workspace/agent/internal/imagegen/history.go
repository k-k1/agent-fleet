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

// appendHistory adds one picture. A failure is logged by the caller and nothing else: the
// picture and its sidecar are already on disk, and the index can be rebuilt from them.
func appendHistory(item HistoryItem) error {
	b, err := json.Marshal(item)
	if err != nil {
		return err
	}
	historyMu.Lock()
	defer historyMu.Unlock()
	if err := os.MkdirAll(ConsoleDir(), 0o700); err != nil {
		return err
	}
	return appendLines(historyPath(), append(b, '\n'))
}

func historyItemOf(path string, p ImageProps) HistoryItem {
	return HistoryItem{Path: path, Studio: p.Studio, Version: p.Version, CreatedAt: p.CreatedAt, Trial: p.Trial}
}

// readHistory is every parseable line, oldest first, rebuilding the file from the sidecars when
// it does not exist.
func readHistory() []HistoryItem {
	historyMu.Lock()
	defer historyMu.Unlock()
	f, err := os.Open(historyPath())
	if errors.Is(err, os.ErrNotExist) {
		items := scanSidecars()
		var buf strings.Builder
		for _, it := range items {
			b, _ := json.Marshal(it)
			buf.Write(b)
			buf.WriteByte('\n')
		}
		if len(items) > 0 && os.MkdirAll(ConsoleDir(), 0o700) == nil {
			_ = appendLines(historyPath(), []byte(buf.String()))
		}
		return items
	}
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []HistoryItem
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		var it HistoryItem
		if json.Unmarshal(sc.Bytes(), &it) == nil && it.Path != "" {
			out = append(out, it)
		}
	}
	return out
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
		if !ok || filepath.Base(p) == filepath.Base(historyPath()) {
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
// first. The cursor is the line number of the oldest picture on the page — the file is
// append-only, so a line keeps its number. A picture that has since been deleted (the trial
// sweep, or the member) is left out rather than shown broken.
func HandleHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	studio := q.Get("studio")
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 200)
	items := readHistory()
	end := len(items)
	if b, err := strconv.Atoi(q.Get("before")); err == nil && b >= 0 && b < end {
		end = b
	}
	page := HistoryPage{Items: []HistoryItem{}}
	i := end - 1
	for ; i >= 0 && len(page.Items) < limit; i-- {
		it := items[i]
		if studio != "" && it.Studio != studio {
			continue
		}
		if _, err := os.Stat(it.Path); err != nil {
			continue
		}
		page.Items = append(page.Items, it)
	}
	if i >= 0 {
		page.Before = strconv.Itoa(i + 1)
	}
	httpx.WriteJSON(w, http.StatusOK, page)
}
