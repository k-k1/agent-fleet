package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

const (
	shareOperationHeader              = "X-Agent-Fleet-Operation-ID"
	shareOperationMaxBody             = 32 << 10
	shareOperationMaxRecord           = 64 << 10
	shareOperationMaxRecords          = 512
	shareOperationMaxLedgerBytes      = 32 << 20
	shareOperationAppliedRetention    = 7 * 24 * time.Hour
	shareOperationProcessingRetention = 90 * 24 * time.Hour
)

var shareOperationIDRe = regexp.MustCompile(`^[a-f0-9]{32}$`)
var shareOperationLedgerMu sync.Mutex

type shareOperationRecord struct {
	State      string              `json:"state"`
	StatusCode int                 `json:"statusCode,omitempty"`
	Header     map[string][]string `json:"header,omitempty"`
	Body       []byte              `json:"body,omitempty"`
	UpdatedAt  string              `json:"updatedAt"`
}

func shareOperationDir() string           { return filepath.Join(session.MetaDir(), "share-operations") }
func shareOperationPath(id string) string { return filepath.Join(shareOperationDir(), id+".json") }

func readShareOperation(id string) (shareOperationRecord, bool) {
	return readShareOperationPath(shareOperationPath(id))
}

func readShareOperationPath(path string) (shareOperationRecord, bool) {
	var record shareOperationRecord
	f, err := os.Open(path)
	if err != nil {
		return record, false
	}
	body, err := io.ReadAll(io.LimitReader(f, shareOperationMaxRecord+1))
	_ = f.Close()
	if err != nil || len(body) > shareOperationMaxRecord || json.Unmarshal(body, &record) != nil ||
		(record.State != "processing" && (record.State != "applied" || record.StatusCode < 100 || record.StatusCode > 999)) {
		return record, false
	}
	return record, true
}

func writeShareOperation(path string, record shareOperationRecord) error {
	body, err := json.Marshal(record)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".share-operation-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0o600); err == nil {
		_, err = f.Write(body)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	return syncShareOperationDir()
}

func claimShareOperation(id string) (shareOperationRecord, bool, error) {
	shareOperationLedgerMu.Lock()
	defer shareOperationLedgerMu.Unlock()
	if err := os.MkdirAll(shareOperationDir(), 0o700); err != nil {
		return shareOperationRecord{}, false, err
	}
	if err := gcShareOperations(time.Now().UTC()); err != nil {
		return shareOperationRecord{}, false, err
	}
	path := shareOperationPath(id)
	record := shareOperationRecord{State: "processing", UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
	body, _ := json.Marshal(record)
	f, err := os.CreateTemp(shareOperationDir(), ".share-operation-claim-*")
	if err != nil {
		return record, false, err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0o600); err == nil {
		_, err = f.Write(body)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return record, false, err
	}
	// link publishes a fully written claim atomically and refuses to replace an
	// existing proposal id. A concurrent request can therefore never observe a
	// zero-length/partial claim and mistake itself for the executor.
	if err = os.Link(tmp, path); os.IsExist(err) {
		existing, ok := readShareOperation(id)
		if !ok {
			// An unreadable/corrupt existing claim is still a claim. Treating it as
			// absent could duplicate a side effect whose result was torn or damaged.
			return shareOperationRecord{State: "processing"}, true, nil
		}
		return existing, true, nil
	} else if err != nil {
		return record, false, err
	}
	if err = syncShareOperationDir(); err != nil {
		return record, false, err
	}
	return record, false, nil
}

type shareOperationFile struct {
	path      string
	state     string
	updatedAt time.Time
	bytes     int64
}

// gcShareOperations bounds both inode and byte use before reserving one more
// maximum-sized record. Applied responses outlive the 24-hour proposal window
// by a week. Unknown processing claims are evidence, so they receive a separate
// 90-day retention and are never evicted merely to make room; capacity exhaustion
// rejects a new operation before its side effect starts.
func gcShareOperations(now time.Time) error {
	entries, err := os.ReadDir(shareOperationDir())
	if err != nil {
		return err
	}
	files := make([]shareOperationFile, 0, len(entries))
	removed := false
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(shareOperationDir(), entry.Name())
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		record, ok := readShareOperationPath(path)
		updated := info.ModTime().UTC()
		state := "processing" // corrupt records remain unknown-outcome evidence
		if ok {
			state = record.State
			if parsed, parseErr := time.Parse(time.RFC3339, record.UpdatedAt); parseErr == nil {
				updated = parsed
			}
		}
		retention := shareOperationProcessingRetention
		if state == "applied" {
			retention = shareOperationAppliedRetention
		}
		if now.Sub(updated) > retention {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
			removed = true
			continue
		}
		if !ok {
			// Older Agent versions could leave an unbounded or torn response. Keep
			// its operation id as unknown-outcome evidence, but compact the unsafe
			// payload/header bytes to a small processing tombstone.
			tombstone := shareOperationRecord{State: "processing", UpdatedAt: updated.Format(time.RFC3339)}
			if err := writeShareOperation(path, tombstone); err != nil {
				return err
			}
			removed = true
		}
		size := info.Size()
		if state != "applied" || size > shareOperationMaxRecord {
			size = shareOperationMaxRecord // reserve a bounded eventual result
		}
		files = append(files, shareOperationFile{path: path, state: state, updatedAt: updated, bytes: size})
	}
	var used int64
	for _, file := range files {
		used += file.bytes
	}
	kept := len(files)
	sort.Slice(files, func(i, j int) bool { return files[i].updatedAt.Before(files[j].updatedAt) })
	for _, file := range files {
		if kept < shareOperationMaxRecords && used+shareOperationMaxRecord <= shareOperationMaxLedgerBytes {
			break
		}
		if file.state != "applied" || file.bytes == 0 {
			continue
		}
		if err := os.Remove(file.path); err != nil && !os.IsNotExist(err) {
			return err
		}
		used -= file.bytes
		file.bytes = 0
		kept--
		removed = true
	}
	if kept >= shareOperationMaxRecords || used+shareOperationMaxRecord > shareOperationMaxLedgerBytes {
		if removed {
			_ = syncShareOperationDir()
		}
		return &shareOperationCapacityError{}
	}
	if removed {
		return syncShareOperationDir()
	}
	return nil
}

type shareOperationCapacityError struct{}

func (*shareOperationCapacityError) Error() string {
	return "share operation ledger capacity exhausted"
}

func syncShareOperationDir() error {
	dir, err := os.Open(shareOperationDir())
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

type boundedShareResponse struct {
	header    http.Header
	body      bytes.Buffer
	status    int
	truncated bool
}

func (r *boundedShareResponse) Header() http.Header { return r.header }
func (r *boundedShareResponse) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
}
func (r *boundedShareResponse) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	remaining := shareOperationMaxBody - r.body.Len()
	if remaining > 0 {
		n := len(p)
		if n > remaining {
			n = remaining
		}
		_, _ = r.body.Write(p[:n])
	}
	if len(p) > remaining {
		r.truncated = true
	}
	return len(p), nil
}

func boundedShareHeader(src http.Header) http.Header {
	out := http.Header{}
	for _, key := range []string{"Content-Type", "Retry-After"} {
		if value := src.Get(key); value != "" {
			if len(value) > 1024 {
				value = value[:1024]
			}
			out.Set(key, value)
		}
	}
	return out
}

// withShareOperationIdempotency durably claims the CP proposal id before the
// side effect. The real response is buffered until the applied record is on disk,
// so a lost response can be replayed without executing the operation twice. A
// crash after claim but before completion deliberately stays "processing": the
// outcome is unknown and automatic retry is unsafe.
func withShareOperationIdempotency(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(shareOperationHeader)
		if id == "" {
			next.ServeHTTP(w, r)
			return
		}
		if !shareOperationIDRe.MatchString(id) {
			httpx.WriteErr(w, http.StatusBadRequest, "bad_operation_id", "invalid operation id")
			return
		}
		record, existed, err := claimShareOperation(id)
		if err != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, "operation_ledger_failed", err.Error())
			return
		}
		if existed {
			if record.State != "applied" {
				httpx.WriteErr(w, http.StatusConflict, "operation_outcome_unknown", "operation was claimed but its outcome is unknown")
				return
			}
			for key, values := range record.Header {
				for _, value := range values {
					w.Header().Add(key, value)
				}
			}
			w.Header().Set("X-Agent-Fleet-Operation-Replay", "true")
			w.WriteHeader(record.StatusCode)
			_, _ = w.Write(record.Body)
			return
		}

		buffered := &boundedShareResponse{header: http.Header{}}
		next.ServeHTTP(buffered, r)
		if buffered.status == 0 {
			buffered.status = http.StatusOK
		}
		body := buffered.body.Bytes()
		header := boundedShareHeader(buffered.header)
		if buffered.truncated {
			header.Set("X-Agent-Fleet-Response-Truncated", "true")
		}
		record = shareOperationRecord{State: "applied", StatusCode: buffered.status,
			Header: header, Body: body, UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
		if err := writeShareOperation(shareOperationPath(id), record); err != nil {
			// The side effect may already have happened. Never remove the processing
			// claim or invite a duplicate retry.
			httpx.WriteErr(w, http.StatusInternalServerError, "operation_outcome_unknown", "operation completed but its durable result could not be recorded")
			return
		}
		for key, values := range header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(buffered.status)
		_, _ = w.Write(body)
	})
}

func handleShareOperationLookup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("key")
	if !shareOperationIDRe.MatchString(id) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_operation_id", "invalid operation id")
		return
	}
	record, ok := readShareOperation(id)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "operation not found")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"state": record.State, "statusCode": record.StatusCode, "updatedAt": record.UpdatedAt})
}
