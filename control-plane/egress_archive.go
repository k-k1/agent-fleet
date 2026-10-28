package main

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
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
//	{"type":"address", ...}    the workspace_address rows whose interval meets the day, as
//	                           they were when the day was written
//	{"type":"event", ...}      the day's events in (at, id) order
//	{"type":"manifest", ...}   row counts and the id and time ranges actually written
//
// The manifest comes last because the counts are only known once the rows have been streamed
// (a day is millions of rows). An object without it, or whose counts disagree with its lines,
// is partial or wrong; the sinks make a half-written object impossible, so this is for a
// reader that got a damaged copy. Addresses precede events so a reader attributes in one
// pass. An address row that arrives or changes after its day was written is not in that day's
// object; it is in an address supplement (kind "addresses", one object per purge run that had
// closed rows to delete, keyed by the SHA-256 of its rows), which the purge writes and verifies
// immediately before deleting exactly those row versions. A reader attributing an archived day
// takes the address rows of every object, day and supplement alike and normalises them first
// (normalizeArchivedAddresses): one row per (ref, address, from), its end the smallest non-zero
// end seen (0 is open), as the store only ever shortens an end. A stale open copy in a day
// object would otherwise keep matching events after the end a supplement recorded. A day
// object alone is therefore attributable only up to the rows that were still changing when it
// was written. Read-back is offline: download, then attribute by source and time exactly as
// ListAttributedEgressEvents does. Archived events are never re-ingested.
const (
	egressArchiveSchema = 1
	egressKindDay       = "events-day"
	egressKindAddresses = "addresses"
)

type egressArchiveHeader struct {
	Type   string `json:"type"`
	Schema int    `json:"schema"`
	Kind   string `json:"kind"`
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

func egressSupplementKey(day time.Time, sum string) string {
	return fmt.Sprintf("egress/addresses/yyyy=%04d/mm=%02d/dd=%02d/addresses-%s.jsonl.gz",
		day.Year(), int(day.Month()), day.Day(), sum)
}

// egressDayStart floors a unix-ms instant to its UTC midnight.
func egressDayStart(ms int64) int64 {
	t := time.UnixMilli(ms).UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).UnixMilli()
}

// archive stores every day that lies wholly before `before` (a UTC midnight) and holds an
// event. It returns upTo, the instant before which every event is archived: the purge may
// delete events older than upTo and nothing at or after it. On a failure upTo is the start of
// the day that failed, so days are archived oldest first and never skipped over.
//
// A day whose object already exists is not written again, but it is read back and checked
// first (header, counts, gzip checksum): a key that is there is not proof that what is behind
// it is whole. A damaged one stops the purge at that day and is never overwritten by itself;
// an operator removes it. Events older than the floor cannot be added (the caller raised the
// floor to `before` first), so a verified object is complete for events; rewriting it after a
// purge that was cut off midway would replace the full day with the part that is left.
func (a *egressArchiver) archive(ctx context.Context, before int64) (upTo int64, err error) {
	start := before
	if at, ok, err := a.st.OldestEgressEvent(ctx); err != nil {
		return 0, err
	} else if ok && at < start {
		start = at
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
	hdr := egressArchiveHeader{"header", egressArchiveSchema, egressKindDay, d.Format("2006-01-02"), from, to}
	if ok, err := a.sink.Exists(ctx, key); err != nil {
		return err
	} else if ok {
		if _, err := a.verify(ctx, key, hdr); err != nil {
			return err
		}
		return a.covers(ctx, key, from, to)
	}
	// Staged on disk: the object is streamed once, and a seekable file is what S3 needs to
	// sign and retry the upload. Memory stays one page of rows plus the gzip window.
	f, err := os.CreateTemp("", "egress-archive-*.jsonl.gz")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	w := newArchiveWriter(f, hdr)
	err = a.st.EachWorkspaceAddressOverlapping(ctx, from, to, func(r store.WorkspaceAddress) error {
		return w.address(r)
	})
	if err != nil {
		return err
	}
	err = a.st.EachEgressEvent(ctx, from, to, a.batch, func(e store.EgressEvent) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return w.event(e)
	})
	if err != nil {
		return err
	}
	if w.m.Events == 0 {
		return nil
	}
	return a.put(ctx, f, w, key)
}

// covers checks that the existing object for [from, to) holds every event the database still
// has in that window, with the same content. A well-formed object is not necessarily a whole
// one: a day written after part of it was deleted, or by an older bug, is consistent with
// itself and still missing events the purge is about to delete. The object and the database
// are both read in (at, id) order and merged, so nothing is held in memory. A delete cut off
// midway leaves a subset of the object in the database, which passes; an event the object
// lacks stops the purge at this day, and nothing is rewritten.
func (a *egressArchiver) covers(ctx context.Context, key string, from, to int64) error {
	rc, err := a.sink.Get(ctx, key)
	if err != nil {
		return err
	}
	defer rc.Close()
	zr, err := gzip.NewReader(rc)
	if err != nil {
		return err
	}
	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	next := func() (e egressArchiveEvent, ok bool, err error) {
		for sc.Scan() {
			var t struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(sc.Bytes(), &t); err != nil {
				return e, false, err
			}
			if t.Type == "event" {
				return e, true, json.Unmarshal(sc.Bytes(), &e)
			}
		}
		return e, false, sc.Err()
	}
	cur, have, err := next()
	if err != nil {
		return err
	}
	err = a.st.EachEgressEvent(ctx, from, to, a.batch, func(e store.EgressEvent) error {
		for have && egressEventLess(cur.At, cur.ID, e.At, e.ID) {
			if cur, have, err = next(); err != nil {
				return err
			}
		}
		want := egressArchiveEvent{"event", e.ID, e.At, e.Source, e.Dest, e.Host, e.Allowed, e.Count}
		if !have || cur != want {
			return fmt.Errorf("existing archive %s does not hold event %q as the database has it (remove it to have it rewritten)", key, e.ID)
		}
		return nil
	})
	return err
}

// archiveAddresses writes, verifies and returns the closed address rows that ended before
// `before`: the rows about to be deleted. They are written as one supplement whatever the day
// objects hold, because a row can arrive or change after its day was archived and the floor
// does not cover address writers. The caller deletes exactly these row versions and nothing
// else, so no row is deleted that was not in a confirmed object.
func (a *egressArchiver) archiveAddresses(ctx context.Context, before int64) ([]store.WorkspaceAddress, error) {
	f, err := os.CreateTemp("", "egress-archive-*.jsonl.gz")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	d := time.UnixMilli(before).UTC()
	w := newArchiveWriter(f, egressArchiveHeader{"header", egressArchiveSchema, egressKindAddresses, d.Format("2006-01-02"), 0, before})
	var rows []store.WorkspaceAddress
	err = a.st.EachPurgeableWorkspaceAddress(ctx, before, func(r store.WorkspaceAddress) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		rows = append(rows, r)
		return w.address(r)
	})
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	key := egressSupplementKey(d, w.sum())
	if ok, err := a.sink.Exists(ctx, key); err != nil {
		return nil, err
	} else if ok {
		sum, err := a.verify(ctx, key, w.hdr)
		if err == nil && sum != w.sum() {
			err = fmt.Errorf("archive %s: content does not match its key", key)
		}
		return rows, err
	}
	return rows, a.put(ctx, f, w, key)
}

// put finishes the object in f and hands it to the sink.
func (a *egressArchiver) put(ctx context.Context, f *os.File, w *archiveWriter, key string) error {
	if err := w.close(); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
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

// archiveWriter streams one object: header, then rows, then the manifest on close. It keeps a
// SHA-256 over the address lines, which names a supplement.
type archiveWriter struct {
	hdr  egressArchiveHeader
	zw   *gzip.Writer
	bw   *bufio.Writer
	enc  *json.Encoder
	hash hash.Hash
	m    egressArchiveManifest
}

func newArchiveWriter(f io.Writer, hdr egressArchiveHeader) *archiveWriter {
	zw := gzip.NewWriter(f)
	bw := bufio.NewWriterSize(zw, 64<<10)
	w := &archiveWriter{hdr: hdr, zw: zw, bw: bw, enc: json.NewEncoder(bw), hash: sha256.New(),
		m: egressArchiveManifest{Type: "manifest", Schema: egressArchiveSchema}}
	w.line(hdr, nil)
	return w
}

// line writes v as one JSON line, and feeds the hash when one is given.
func (w *archiveWriter) line(v any, h hash.Hash) {
	b, _ := json.Marshal(v)
	if h != nil {
		h.Write(b)
		h.Write([]byte{'\n'})
	}
	w.bw.Write(b)
	w.bw.WriteByte('\n')
}

func (w *archiveWriter) address(r store.WorkspaceAddress) error {
	w.m.Addresses++
	w.line(egressArchiveAddress{"address", r.MembershipID, r.WorkspaceID, r.Address, r.Ref, r.From, r.Until}, w.hash)
	return nil
}

func (w *archiveWriter) event(e store.EgressEvent) error {
	if w.m.Events == 0 {
		w.m.FirstID, w.m.LastID, w.m.FirstAt = e.ID, e.ID, e.At
	}
	w.m.FirstID, w.m.LastID = min(w.m.FirstID, e.ID), max(w.m.LastID, e.ID)
	w.m.LastAt = e.At
	w.m.Events++
	w.line(egressArchiveEvent{"event", e.ID, e.At, e.Source, e.Dest, e.Host, e.Allowed, e.Count}, nil)
	return nil
}

func (w *archiveWriter) sum() string { return hex.EncodeToString(w.hash.Sum(nil))[:32] }

func (w *archiveWriter) close() error {
	w.line(w.m, nil)
	if err := w.bw.Flush(); err != nil {
		return err
	}
	return w.zw.Close()
}

// verify reads an existing object back and checks it is whole: gzip to its end (which checks
// the CRC and length), the expected header first, the manifest last, and counts that agree
// with the lines. It returns the address-line hash, so a supplement can be compared with its key.
func (a *egressArchiver) verify(ctx context.Context, key string, want egressArchiveHeader) (string, error) {
	rc, err := a.sink.Get(ctx, key)
	if err != nil {
		return "", fmt.Errorf("archive %s: %w", key, err)
	}
	defer rc.Close()
	sum, err := verifyArchive(rc, want)
	if err != nil {
		return "", fmt.Errorf("existing archive %s is not whole (remove it to have it rewritten): %w", key, err)
	}
	return sum, nil
}

// egressEventLess is the archive's event order: time, then id bytewise.
func egressEventLess(aAt int64, aID string, bAt int64, bID string) bool {
	return aAt < bAt || (aAt == bAt && aID < bID)
}

func verifyArchive(r io.Reader, want egressArchiveHeader) (string, error) {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return "", err
	}
	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	h := sha256.New()
	var events, addrs int
	var first, prev egressArchiveEvent
	var minID, maxID string
	var man *egressArchiveManifest
	for n := 0; sc.Scan(); n++ {
		if man != nil {
			return "", errors.New("lines after the manifest")
		}
		var t struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(sc.Bytes(), &t); err != nil {
			return "", err
		}
		switch {
		case n == 0:
			var got egressArchiveHeader
			if err := json.Unmarshal(sc.Bytes(), &got); err != nil {
				return "", err
			}
			if t.Type != "header" || got != want {
				return "", fmt.Errorf("header %+v, want %+v", got, want)
			}
		case t.Type == "event":
			var e egressArchiveEvent
			if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
				return "", err
			}
			if want.Kind != egressKindDay || e.At < want.From || e.At >= want.To {
				return "", fmt.Errorf("event %q at %d is outside the object's window", e.ID, e.At)
			}
			if events > 0 && !egressEventLess(prev.At, prev.ID, e.At, e.ID) {
				return "", fmt.Errorf("event %q is out of (at, id) order", e.ID)
			}
			if events == 0 {
				first = e
			}
			prev = e
			minID, maxID = min(minID, e.ID), max(maxID, e.ID)
			if events == 0 {
				minID, maxID = e.ID, e.ID
			}
			events++
		case t.Type == "address":
			addrs++
			h.Write(sc.Bytes())
			h.Write([]byte{'\n'})
		case t.Type == "manifest":
			man = new(egressArchiveManifest)
			if err := json.Unmarshal(sc.Bytes(), man); err != nil {
				return "", err
			}
		default:
			return "", fmt.Errorf("unknown line type %q", t.Type)
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	if man == nil {
		return "", errors.New("no manifest")
	}
	if man.Schema != egressArchiveSchema || man.Events != events || man.Addresses != addrs {
		return "", fmt.Errorf("manifest says %d events and %d addresses, object has %d and %d", man.Events, man.Addresses, events, addrs)
	}
	if man.FirstID != minID || man.LastID != maxID || man.FirstAt != first.At || man.LastAt != prev.At {
		return "", errors.New("manifest id or time range does not match the events")
	}
	if want.Kind == egressKindDay && events == 0 {
		return "", errors.New("a day object without events")
	}
	return hex.EncodeToString(h.Sum(nil))[:32], nil
}

// normalizeArchivedAddresses is the read-back rule for address rows gathered from day objects
// and supplements: duplicates of one interval (ref, address, from) collapse to one whose end is
// the smallest non-zero end seen, 0 meaning still open. Attribute events only after this.
func normalizeArchivedAddresses(rows []store.WorkspaceAddress) []store.WorkspaceAddress {
	type key struct {
		ref, address string
		from         int64
	}
	at := map[key]int{}
	var out []store.WorkspaceAddress
	for _, r := range rows {
		k := key{r.Ref, r.Address, r.From}
		i, seen := at[k]
		if !seen {
			at[k] = len(out)
			out = append(out, r)
			continue
		}
		if r.Until != 0 && (out[i].Until == 0 || r.Until < out[i].Until) {
			out[i].Until = r.Until
		}
	}
	return out
}
