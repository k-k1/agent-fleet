package routegw

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// The gateway's ledger row is the only record of a model call it forwarded: the fold drops the
// harness's own transcript rows of the route (ADR 0128 decision 8). So a row the ledger refused
// (a full or unwritable disk) is not dropped. It is written to a spool file before the append,
// removed after the append succeeds, and replayed by the minute sweep until it does, across an
// Agent restart too. Every row carries a Key, so a replay of a row that did reach the ledger
// (an append that wrote and then failed, a crash before the spool file was removed) is counted
// once by the aggregation's Key dedup.

// spoolSuffix marks a spool file; anything else in the directory is not the gateway's.
const spoolSuffix = ".row.json"

type rowSpool struct {
	dir    string
	append func([]usagex.Record) error

	mu  sync.Mutex
	mem []usagex.Record // rows that could not be spooled either, kept until the next replay
}

// write records one row: spooled, appended, unspooled. It never fails the call it records.
func (s *rowSpool) write(row usagex.Record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, spooled := s.save(row)
	if err := s.append([]usagex.Record{row}); err != nil {
		log.Printf("route gateway: ledger: %v (row kept for a retry)", err)
		if !spooled {
			s.mem = append(s.mem, row)
		}
		return
	}
	if spooled {
		_ = os.Remove(path)
	}
}

// save writes the row's spool file (through a temporary name, so a replay never reads half of
// one). ok is false when it could not.
func (s *rowSpool) save(row usagex.Record) (string, bool) {
	b, err := json.Marshal(row)
	if err != nil || s.dir == "" || os.MkdirAll(s.dir, 0o700) != nil {
		return "", false
	}
	path := filepath.Join(s.dir, row.Call+spoolSuffix)
	tmp := path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) != nil || os.Rename(tmp, path) != nil {
		_ = os.Remove(tmp)
		log.Printf("route gateway: ledger spool: could not keep a row in %s", s.dir)
		return "", false
	}
	return path, true
}

// replay appends what is spooled or held, removing each once the ledger took it, and says how
// many rows it recovered.
func (s *rowSpool) replay() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	if len(s.mem) > 0 && s.append(s.mem) == nil {
		n += len(s.mem)
		s.mem = nil
	}
	ents, err := os.ReadDir(s.dir)
	if err != nil {
		return n
	}
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), spoolSuffix) {
			continue
		}
		path := filepath.Join(s.dir, e.Name())
		b, err := os.ReadFile(path)
		var row usagex.Record
		if err != nil || json.Unmarshal(b, &row) != nil || row.Key == "" {
			continue
		}
		if s.append([]usagex.Record{row}) != nil {
			return n // the ledger still refuses; the rest waits for the next sweep
		}
		_ = os.Remove(path)
		n++
	}
	return n
}
