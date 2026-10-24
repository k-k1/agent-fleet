package egressagent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// maxBatchBytes keeps one POST well under the CP's 1 MiB ingest limit (egressAPI.ingest).
const maxBatchBytes = 256 << 10

// maxLine bounds one log line. The format's fields are short; a longer line is not one
// Squid wrote in it, and is skipped rather than buffered without limit.
const maxLine = 64 << 10

// identPrefix is how much of a file's start identifies it when its first line is longer.
const identPrefix = maxLine

// shipState is what survives a restart: for each log file still on disk, by identity, the
// offset up to which every line has been accepted by the CP.
type shipState struct {
	Offsets map[string]int64 `json:"offsets"`
}

func loadState(path string) (shipState, error) {
	st := shipState{Offsets: map[string]int64{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	// A state file that does not decode is an error, not a fresh start: starting over
	// would resend everything, which the CP absorbs, but silently hiding a broken disk is
	// worse than stopping.
	if err := json.Unmarshal(b, &st); err != nil {
		return st, fmt.Errorf("state %s: %w", path, err)
	}
	if st.Offsets == nil {
		st.Offsets = map[string]int64{}
	}
	return st, nil
}

func (st shipState) save(path string) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return writeAtomic(path, b)
}

// shipper reads Squid's log from the committed offsets and POSTs it.
type shipper struct {
	cfg   *Config
	pol   *policySync
	state shipState
}

// logFiles lists the log and its rotated siblings (path.0, path.1, ...), oldest first, so
// a rotated file is finished before the newer ones.
func logFiles(path string) []string {
	matches, _ := filepath.Glob(path + ".*")
	type rot struct {
		n    int
		path string
	}
	var rots []rot
	for _, m := range matches {
		n, err := strconv.Atoi(strings.TrimPrefix(m, path+"."))
		if err != nil || n < 0 {
			continue
		}
		rots = append(rots, rot{n, m})
	}
	sort.Slice(rots, func(i, j int) bool { return rots[i].n > rots[j].n })
	out := make([]string, 0, len(rots)+1)
	for _, r := range rots {
		out = append(out, r.path)
	}
	return append(out, path)
}

// fileIdent identifies a log file by the gateway, its inode, its birth time and a hash of
// its first line. All four survive a rename and a restart. A colliding identity makes the
// CP drop a real file's events as duplicates, and each part closes one way to collide: two
// gateways can write the same line; two connections that started in the same millisecond
// from one source to one name can be the first lines of two files; and a new file can
// reuse a deleted one's inode, which only the birth time tells apart. "" means the file
// has no complete first line yet and is not read.
func fileIdent(f *os.File, gateway string) (string, error) {
	buf := make([]byte, identPrefix)
	n, err := f.ReadAt(buf, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	buf = buf[:n]
	if i := bytes.IndexByte(buf, '\n'); i >= 0 {
		buf = buf[:i+1]
	} else if n < identPrefix {
		return "", nil
	}
	ino, btime, err := fileID(f)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(buf)
	return gateway + "." + strconv.FormatUint(ino, 36) + "." + strconv.FormatInt(btime, 36) + "." +
		hex.EncodeToString(sum[:8]), nil
}

// errShipFailed stops a pass at the first rejected POST; the offset stays where the CP
// last accepted, and the next pass resends from there.
var errShipFailed = errors.New("ship failed")

// pass ships everything complete in every log file. It returns at the first failure.
func (s *shipper) pass(ctx context.Context) error {
	snap := s.pol.cur.Load()
	if snap == nil {
		// No policy yet: verdicts would all read "not allowed" and raise false would-block
		// audits. The lines wait on disk.
		return nil
	}
	seen := map[string]bool{}
	for _, p := range logFiles(s.cfg.LogPath) {
		id, err := s.shipFile(ctx, p, snap)
		if id != "" {
			seen[id] = true
		}
		if err != nil {
			return err
		}
	}
	// Forget files that are gone (rotated past the last sibling), so the state stays small.
	pruned := false
	for id := range s.state.Offsets {
		if !seen[id] {
			delete(s.state.Offsets, id)
			pruned = true
		}
	}
	if pruned {
		return s.state.save(s.cfg.StatePath)
	}
	return nil
}

// shipFile sends the complete lines of one file from its committed offset, in batches,
// committing after each accepted batch. It returns the file's identity.
func (s *shipper) shipFile(ctx context.Context, path string, snap *snapshot) (string, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	id, err := fileIdent(f, s.cfg.GatewayID)
	if err != nil || id == "" {
		return id, err
	}
	fi, err := f.Stat()
	if err != nil {
		return id, err
	}
	off := s.state.Offsets[id]
	if off > fi.Size() {
		log.Printf("egress-agent: %s is shorter than its committed offset (%d > %d): truncated in place? reading it again from the start", path, off, fi.Size())
		off = 0
	}
	r := bufio.NewReaderSize(f, 64<<10)
	for {
		// Every batch starts from a fresh reader at the committed offset: a reader that hit
		// EOF inside a line has already consumed that line's first bytes, and reusing it
		// after Squid finished the line would split it and shift every later id.
		if _, err := f.Seek(off, io.SeekStart); err != nil {
			return id, err
		}
		r.Reset(f)
		batch, next, err := readBatch(r, id, off, snap)
		if err != nil {
			return id, err
		}
		if next == off {
			return id, nil // nothing complete past the offset
		}
		if len(batch) > 0 {
			if err := s.post(ctx, batch); err != nil {
				log.Printf("egress-agent: report of %s from offset %d failed (will retry): %v", path, off, err)
				return id, errShipFailed
			}
		}
		s.state.Offsets[id] = next
		if err := s.state.save(s.cfg.StatePath); err != nil {
			return id, err
		}
		off = next
	}
}

// readBatch reads complete lines from r, which is positioned at off, until the batch is
// full or no complete line is left. It returns the events and the offset just past the
// last line consumed. An incomplete last line is not consumed: Squid is still writing it.
func readBatch(r *bufio.Reader, ident string, off int64, snap *snapshot) ([]Event, int64, error) {
	var evs []Event
	size := 0
	for size < maxBatchBytes {
		line, err := readLine(r)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, off, err
		}
		pos := off
		off += line.n
		if line.tooLong {
			log.Printf("egress-agent: skipping a line longer than %d bytes at %s@%d", maxLine, ident, pos)
			continue
		}
		l, ok := parseLine(strings.TrimRight(string(line.text), "\r\n"))
		if !ok {
			log.Printf("egress-agent: skipping a line not in the af1 format at %s@%d: %.200q", ident, pos, line.text)
			continue
		}
		ev := Event{
			ID: ident + "@" + strconv.FormatInt(pos, 10), At: l.at, Source: l.source, Dest: l.dest,
			Host: l.host, Allowed: !l.denied && l.host != "" && snap.match.Allows(l.host), Count: 1,
		}
		b, _ := json.Marshal(ev)
		size += len(b) + 1
		evs = append(evs, ev)
	}
	return evs, off, nil
}

type rawLine struct {
	n       int64  // the bytes consumed, newline included: what the offset moves by
	text    []byte // the line's content; nil when tooLong
	tooLong bool
}

// readLine returns the next complete line. io.EOF means no complete line is left; the
// caller does not count the partial bytes, and the next pass re-seeks to the committed
// offset, so Squid can finish writing the line meanwhile.
func readLine(r *bufio.Reader) (rawLine, error) {
	var out rawLine
	for {
		chunk, err := r.ReadSlice('\n')
		out.n += int64(len(chunk))
		if !out.tooLong && len(out.text)+len(chunk) > maxLine {
			out.tooLong, out.text = true, nil
		}
		if !out.tooLong {
			out.text = append(out.text, chunk...)
		}
		if err == nil {
			return out, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return rawLine{}, err // io.EOF included: the line is not complete
	}
}

func (s *shipper) post(ctx context.Context, evs []Event) error {
	body, err := json.Marshal(struct {
		Events []Event `json:"events"`
	}{evs})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.IngestURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.cfg.Token)
	resp, err := s.cfg.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("CP answered %s", resp.Status)
	}
	return nil
}
