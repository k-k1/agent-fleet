package mempressure

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"sort"
)

// Window is one minute of the series as the Control Plane's usage history takes it (ADR 0139
// decision 3): the launch it was measured under and the five figures the history folds. Every
// figure is a pointer and omitted when the minute could not measure it. The Control Plane
// counts a minute per signal and never treats an absent one as zero.
type Window struct {
	// T is the unix second the window ended, which is also the cursor value.
	T int64 `json:"t"`
	// LID is the launch id (LaunchIDEnv). Windows without one are never returned.
	LID string `json:"lid"`

	// CPUPct is percent of ONE core over the window (Record.CPUPct).
	CPUPct *float64 `json:"cpu_pct,omitempty"`
	// WorkingSet is memory.current minus inactive_file at the window's end, in bytes.
	WorkingSet *uint64 `json:"ws,omitempty"`
	// Some10Max is the highest PSI `some` avg10 sampled in the window, Full60 the PSI `full`
	// avg60 at its end. Both are needed for the window to count as pressure-measured.
	Some10Max *float64 `json:"some10_max,omitempty"`
	Full60    *float64 `json:"full60,omitempty"`
	// RefaultRate is the mean file-refault rate per second over the window.
	RefaultRate *float64 `json:"refault_rate,omitempty"`
}

// WindowsPage is the answer to "the minutes after this cursor".
type WindowsPage struct {
	Windows []Window `json:"windows"`
	// Cursor is what the caller passes back as `since` next time. It moves past lines that
	// carry no launch id (written before the Control Plane told the Agent one), so an old
	// series is read once and not again.
	Cursor int64 `json:"cursor"`
	// More is true when the page was cut at the limit; Cursor then sits on the last window
	// returned.
	More bool `json:"more,omitempty"`
}

// seriesLine is the subset of Record the windows are built from. A session pass can make a
// line a few KiB, so the rest is deliberately not decoded.
type seriesLine struct {
	T           int64    `json:"t"`
	LID         string   `json:"lid"`
	CPUPct      *float64 `json:"cpu_pct"`
	WS          *uint64  `json:"ws"`
	Some10Max   *float64 `json:"some10_max"`
	Full60      *float64 `json:"full60"`
	RefaultRate *float64 `json:"refault_rate_avg"`
}

// WindowsSince returns up to limit windows whose t is strictly after since, oldest first, from
// both generations of the series file. A line that does not parse is skipped: a torn tail from
// a crash must not hide the minutes before it. Reading takes the series lock, so a rotation
// cannot move a generation under the reader.
func (s *Series) WindowsSince(since int64, limit int) (WindowsPage, error) {
	if limit <= 0 {
		limit = DefaultWindowsLimit
	}
	if limit > MaxWindowsLimit {
		limit = MaxWindowsLimit // a caller inside the workspace must not make it load the whole series
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	page := WindowsPage{Windows: []Window{}, Cursor: since}
	// Both generations are read and sorted together: after a clock jump a rotation can leave the
	// older file holding later timestamps, and sorting each alone would drop minutes at the cursor.
	var lines []seriesLine
	for _, p := range []string{s.path + ".1", s.path} {
		l, err := readSeries(p, since)
		if err != nil {
			return WindowsPage{Windows: []Window{}, Cursor: since}, err
		}
		lines = append(lines, l...)
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].T < lines[j].T })
	for _, l := range lines {
		if l.T <= page.Cursor {
			continue // the cursor must never go back
		}
		if l.LID != "" {
			if len(page.Windows) >= limit {
				page.More = true
				break
			}
			page.Windows = append(page.Windows, Window{
				T: l.T, LID: l.LID, CPUPct: l.CPUPct, WorkingSet: l.WS,
				Some10Max: l.Some10Max, Full60: l.Full60, RefaultRate: l.RefaultRate,
			})
		}
		page.Cursor = l.T
	}
	return page, nil
}

// DefaultWindowsLimit bounds one page: about a day of minutes, a few hundred KiB on the wire.
const DefaultWindowsLimit = 1500

// MaxWindowsLimit is the most one page may hold, whatever the caller asks.
const MaxWindowsLimit = 3 * DefaultWindowsLimit

// readSeries returns the parsable lines of one generation with t after since.
func readSeries(path string, since int64) ([]seriesLine, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	var lines []seriesLine
	for sc.Scan() {
		var l seriesLine
		if json.Unmarshal(sc.Bytes(), &l) != nil || l.T <= since {
			continue
		}
		lines = append(lines, l)
	}
	// A line longer than the buffer ends the scan; what was read stands.
	return lines, nil
}
