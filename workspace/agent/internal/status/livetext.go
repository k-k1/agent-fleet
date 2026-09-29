package status

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"sort"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/fstore"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// live-text: the reply claude is streaming right now, for the mirror's in-progress block
// (#1250). One MessageDisplay flush is one line of a per-sid JSONL file.
//
// Append-only, never rewritten: claude keeps up to three MessageDisplay hooks in flight, each a
// separate `workspace-agent session-status message` process, so flushes of one message arrive
// concurrently and out of order. fstore offers no lock, and a read-modify-write of a single
// record would drop whichever flush lost the race (see ObservedTurnEnd for what that costs
// here). Each process appends one complete line in a single write; ReadLiveText puts the
// message back together from the records.
var liveTexts = fstore.Strings(paths.AgentStateDir, "live-text", ".jsonl")

const (
	// liveTextFileCap stops appending once the file is this large. The file is removed at every
	// turn end, and one turn's streamed text stays far below this; the cap only bounds a
	// runaway.
	liveTextFileCap = 4 << 20
	// liveTextWindow is how much of the file's tail one read looks at. The mirror polls about
	// once a second while a turn runs, so this bounds the per-poll cost. A message longer than
	// the window comes back as its tail (LiveReply.Partial).
	liveTextWindow = 256 << 10
)

type liveRecord struct {
	Turn  string `json:"turn"`
	Msg   string `json:"msg"`
	Index int    `json:"i"`
	Final bool   `json:"final,omitempty"`
	Delta string `json:"d"`
	At    int64  `json:"at"` // unix nanoseconds, when the hook appended this flush
}

// LiveReply is the message being streamed, assembled from its flushes.
type LiveReply struct {
	Text string
	// Final: the message's last flush is in Text, i.e. the message is complete.
	Final bool
	// FinalAt is when that last flush was appended; zero unless Final.
	FinalAt time.Time
	// Partial: Text starts mid-message, because the head fell outside liveTextWindow.
	Partial bool
}

// AppendLiveText records one MessageDisplay flush. turn and msg are claude's turn_id and
// message_id, index its flush counter within the message. An empty delta is kept only when
// it is the final flush: that record is what marks the message complete.
func AppendLiveText(sid, turn, msg string, index int, final bool, delta string) {
	if msg == "" || (delta == "" && !final) {
		return
	}
	if err := os.MkdirAll(liveTexts.Dir(), 0o700); err != nil {
		return
	}
	path := liveTexts.Path(sid)
	if fi, err := os.Stat(path); err == nil && fi.Size() >= liveTextFileCap {
		return
	}
	b, err := json.Marshal(liveRecord{Turn: turn, Msg: msg, Index: index, Final: final, Delta: delta, At: time.Now().UnixNano()})
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	// One write per record: O_APPEND positions each write atomically, so concurrent hook
	// processes never interleave inside a line.
	_, _ = f.Write(append(b, '\n'))
}

// ReadLiveText assembles the newest message in sid's live-text file.
//
// The newest message is the one whose FIRST record comes last in the file, not the one that
// wrote the last line: the final flush of the previous message can straggle in after the next
// message has begun (its hook process simply ran late).
//
// Records are joined in index order and only up to the first missing index. A gap is a flush
// whose hook is still running, and showing what comes after it would put lines out of order.
func ReadLiveText(sid string) (LiveReply, bool) {
	f, err := os.Open(liveTexts.Path(sid))
	if err != nil {
		return LiveReply{}, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.Size() == 0 {
		return LiveReply{}, false
	}
	off := max(0, fi.Size()-liveTextWindow)
	buf, err := io.ReadAll(io.NewSectionReader(f, off, fi.Size()-off))
	if err != nil {
		return LiveReply{}, false
	}
	if off > 0 {
		// The window starts mid-line; that fragment belongs to a record we cannot read whole.
		i := bytes.IndexByte(buf, '\n')
		if i < 0 {
			return LiveReply{}, false
		}
		buf = buf[i+1:]
	}

	type key struct{ turn, msg string }
	byMsg := map[key][]liveRecord{}
	var newest key
	for line := range bytes.SplitSeq(buf, []byte{'\n'}) {
		var rec liveRecord
		if len(line) == 0 || json.Unmarshal(line, &rec) != nil || rec.Msg == "" {
			continue // an empty tail or a line still being written
		}
		k := key{rec.Turn, rec.Msg}
		if _, seen := byMsg[k]; !seen {
			newest = k
		}
		byMsg[k] = append(byMsg[k], rec)
	}
	recs := byMsg[newest]
	if len(recs) == 0 {
		return LiveReply{}, false
	}
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].Index < recs[j].Index })

	var out LiveReply
	var text bytes.Buffer
	next := recs[0].Index
	if next > 0 && off == 0 {
		// The whole file was read, so the message's first flush is not outside the window: its
		// hook has not finished yet, and nothing can be shown before it.
		return LiveReply{}, false
	}
	out.Partial = next > 0
	for _, rec := range recs {
		if rec.Index < next {
			continue // the same flush recorded twice
		}
		if rec.Index > next {
			break // an earlier flush has not landed yet
		}
		text.WriteString(rec.Delta)
		next++
		if rec.Final {
			out.Final = true
			out.FinalAt = time.Unix(0, rec.At)
			break
		}
	}
	out.Text = text.String()
	return out, true
}

// RemoveLiveText drops sid's streamed text; called when the turn ends.
func RemoveLiveText(sid string) { liveTexts.Remove(sid) }
