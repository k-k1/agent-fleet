package main

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Archive of raw egress events before the retention purge deletes them (ADR 0122, addendum
// 2026-10-25, #2699). Off unless AF_EGRESS_ARCHIVE_URL is set.
//
// One object per UTC day, gzip-compressed JSON lines:
//
//	{"type":"header", ...}     schema version, the day and its [from, to) in unix ms
//	{"type":"address", ...}    every workspace_address row whose interval meets the day
//	{"type":"event", ...}      the day's events in (at, id) order
//	{"type":"manifest", ...}   row counts and the id and time ranges actually written
//
// The manifest comes last because the counts are only known once the rows have been streamed
// (a day is millions of rows). An object without it, or whose counts disagree with its lines,
// is partial or wrong; the sinks make a half-written object impossible, so this is for a
// reader that got a damaged copy. Addresses precede events so a reader attributes in one
// pass. Read-back is offline: download, then attribute by source and time exactly as
// ListAttributedEgressEvents does. Archived events are never re-ingested.
const egressArchiveSchema = 1

type egressArchiveHeader struct {
	Type   string `json:"type"`
	Schema int    `json:"schema"`
	Day    string `json:"day"`
	From   int64  `json:"from"`
	To     int64  `json:"to"`
}

type egressArchiveAddress struct {
	Type         string `json:"type"`
	MembershipID string `json:"membership_id"`
	WorkspaceID  string `json:"workspace_id"`
	Address      string `json:"address"`
	Ref          string `json:"ref"`
	From         int64  `json:"from"`
	Until        int64  `json:"until"`
}

type egressArchiveEvent struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	At      int64  `json:"at"`
	Source  string `json:"source"`
	Dest    string `json:"dest"`
	Host    string `json:"host"`
	Allowed bool   `json:"allowed"`
	Count   int    `json:"count"`
}

type egressArchiveManifest struct {
	Type      string `json:"type"`
	Schema    int    `json:"schema"`
	Events    int    `json:"events"`
	Addresses int    `json:"addresses"`
	FirstID   string `json:"first_id"`
	LastID    string `json:"last_id"`
	FirstAt   int64  `json:"first_at"`
	LastAt    int64  `json:"last_at"`
}

const egressArchiveDay = 24 * time.Hour

// egressArchiver writes the days the purge is about to delete.
type egressArchiver struct {
	st    store.EgressAttributionStore
	sink  egressArchiveSink
	batch int
}

func newEgressArchiver(st store.EgressAttributionStore, sink egressArchiveSink) *egressArchiver {
	return &egressArchiver{st: st, sink: sink, batch: egressPurgeBatch}
}

// egressArchiveKey is the deterministic Hive-style key of a day.
func egressArchiveKey(day time.Time) string {
	return fmt.Sprintf("egress/yyyy=%04d/mm=%02d/dd=%02d/events-%s.jsonl.gz",
		day.Year(), int(day.Month()), day.Day(), day.Format("2006-01-02"))
}

// egressDayStart floors a unix-ms instant to its UTC midnight.
func egressDayStart(ms int64) int64 {
	t := time.UnixMilli(ms).UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).UnixMilli()
}

// archive stores every day that lies wholly before `before` (a UTC midnight) and holds an
// event, or an address row's last moment, that the purge would delete. It returns upTo, the
// instant before which every such row is archived: the purge may delete events and closed
// address rows older than upTo and nothing at or after it. On a failure upTo is the start of
// the day that failed, so days are archived oldest first and never skipped over.
//
// A day whose object already exists is not written again. Events older than the floor cannot
// be added (the caller raised the floor to `before` first), so an existing object is complete
// for events; rewriting it after a purge that was cut off midway would replace the full day
// with the part that is left. That makes a retry after a crash between write and delete safe.
func (a *egressArchiver) archive(ctx context.Context, before int64) (upTo int64, err error) {
	start := before
	if at, ok, err := a.st.OldestEgressEvent(ctx); err != nil {
		return 0, err
	} else if ok && at < start {
		start = at
	}
	if end, ok, err := a.st.OldestPurgeableAddressEnd(ctx, before); err != nil {
		return 0, err
	} else if ok && end-1 < start {
		// The interval is half-open: an end at midnight belongs to the day before.
		start = end - 1
	}
	day := egressDayStart(start)
	for ; day < before; day = time.UnixMilli(day).UTC().Add(egressArchiveDay).UnixMilli() {
		if err := ctx.Err(); err != nil {
			return day, err
		}
		if err := a.archiveDay(ctx, day); err != nil {
			return day, fmt.Errorf("archive %s: %w", time.UnixMilli(day).UTC().Format("2006-01-02"), err)
		}
	}
	return before, nil
}

func (a *egressArchiver) archiveDay(ctx context.Context, from int64) error {
	d := time.UnixMilli(from).UTC()
	to := d.Add(egressArchiveDay).UnixMilli()
	key := egressArchiveKey(d)
	if ok, err := a.sink.Exists(ctx, key); err != nil || ok {
		return err
	}
	// Staged on disk: the object is streamed once, and a seekable file is what S3 needs to
	// sign and retry the upload. Memory stays one page of rows plus the gzip window.
	f, err := os.CreateTemp("", "egress-archive-*.jsonl.gz")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	m, err := a.write(ctx, f, d.Format("2006-01-02"), from, to)
	if err != nil {
		return err
	}
	if m.Events == 0 && m.endsHere == 0 {
		return nil
	}
	size, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return a.sink.Put(ctx, key, f, size)
}

type egressArchiveWritten struct {
	egressArchiveManifest
	// endsHere counts address rows whose last moment falls inside the day: the purge deletes a
	// closed row once its end is old, so a day where one ends is archived even without events.
	endsHere int
}

func (a *egressArchiver) write(ctx context.Context, f *os.File, day string, from, to int64) (egressArchiveWritten, error) {
	m := egressArchiveWritten{egressArchiveManifest: egressArchiveManifest{Type: "manifest", Schema: egressArchiveSchema}}
	zw := gzip.NewWriter(f)
	bw := bufio.NewWriterSize(zw, 64<<10)
	enc := json.NewEncoder(bw)
	if err := enc.Encode(egressArchiveHeader{"header", egressArchiveSchema, day, from, to}); err != nil {
		return m, err
	}
	err := a.st.EachWorkspaceAddressOverlapping(ctx, from, to, func(w store.WorkspaceAddress) error {
		if w.Until > from && w.Until <= to {
			m.endsHere++
		}
		m.Addresses++
		return enc.Encode(egressArchiveAddress{"address", w.MembershipID, w.WorkspaceID, w.Address, w.Ref, w.From, w.Until})
	})
	if err != nil {
		return m, err
	}
	err = a.st.EachEgressEvent(ctx, from, to, a.batch, func(e store.EgressEvent) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if m.Events == 0 {
			m.FirstID, m.LastID, m.FirstAt = e.ID, e.ID, e.At
		}
		m.FirstID, m.LastID = min(m.FirstID, e.ID), max(m.LastID, e.ID)
		m.LastAt = e.At
		m.Events++
		return enc.Encode(egressArchiveEvent{"event", e.ID, e.At, e.Source, e.Dest, e.Host, e.Allowed, e.Count})
	})
	if err != nil {
		return m, err
	}
	if err := enc.Encode(m.egressArchiveManifest); err != nil {
		return m, err
	}
	if err := bw.Flush(); err != nil {
		return m, err
	}
	if err := zw.Close(); err != nil {
		return m, err
	}
	return m, f.Sync()
}
