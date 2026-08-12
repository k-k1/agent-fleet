package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

const shareOperationHeader = "X-Agent-Fleet-Operation-ID"

var shareOperationIDRe = regexp.MustCompile(`^[a-f0-9]{32}$`)

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
	var record shareOperationRecord
	body, err := os.ReadFile(shareOperationPath(id))
	if err != nil || json.Unmarshal(body, &record) != nil {
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
	if err := os.MkdirAll(shareOperationDir(), 0o700); err != nil {
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

func syncShareOperationDir() error {
	dir, err := os.Open(shareOperationDir())
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
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

		buffered := httptest.NewRecorder()
		next.ServeHTTP(buffered, r)
		result := buffered.Result()
		body, _ := io.ReadAll(result.Body)
		_ = result.Body.Close()
		record = shareOperationRecord{State: "applied", StatusCode: result.StatusCode,
			Header: result.Header.Clone(), Body: body, UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
		if err := writeShareOperation(shareOperationPath(id), record); err != nil {
			// The side effect may already have happened. Never remove the processing
			// claim or invite a duplicate retry.
			httpx.WriteErr(w, http.StatusInternalServerError, "operation_outcome_unknown", "operation completed but its durable result could not be recorded")
			return
		}
		for key, values := range result.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(result.StatusCode)
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
