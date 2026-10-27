package mempressure

import (
	"os"
	"path/filepath"
	"sync"
)

// Series is the on-disk time series: one JSON line per minute, in a file capped at max bytes.
// When the next line would not fit, the file is renamed to <path>.1 (replacing the previous
// generation) and a new one starts, so the footprint is bounded by 2×max whatever the uptime.
// At ~600 bytes a line, the default 2 MiB per generation is about two days each, four days of
// history in all, which is what threshold-fixing needs and nothing a member would notice.
type Series struct {
	path string
	max  int64
	mu   sync.Mutex
}

// NewSeries returns a series writing to path. max <= 0 selects DefaultSeriesBytes.
func NewSeries(path string, max int64) *Series {
	if max <= 0 {
		max = DefaultSeriesBytes
	}
	return &Series{path: path, max: max}
}

// DefaultSeriesBytes is the per-generation cap.
const DefaultSeriesBytes = 2 << 20

// Append writes one line (a newline is added). A failure is returned and otherwise ignored by
// the caller: losing a data point must never take the Agent down.
func (s *Series) Append(line []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	if fi, err := os.Stat(s.path); err == nil && fi.Size()+int64(len(line))+1 > s.max {
		if err := os.Rename(s.path, s.path+".1"); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(line[:len(line):len(line)], '\n'))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}
