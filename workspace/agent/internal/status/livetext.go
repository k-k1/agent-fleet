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
	// liveTextWindow is how much of the file's tail a read looks at first. The mirror polls
	// about once a second while a turn runs, so this bounds the usual per-poll cost. When the
	// newest message's first flush is not inside it, the read widens until it is (ReadLiveText).
	liveTextWindow = 256 << 10
)

// LiveFlush is one MessageDisplay flush as the hook received it.
type LiveFlush struct {
	// Prompt is claude's prompt_id: the user prompt the turn answers. The transcript carries
	// the same id as promptId on that prompt's rows, which is how a message is tied to its turn.
	Prompt string
	Turn   string // turn_id
	Msg    string // message_id: claude's display id for the assistant message
	Index  int    // the flush counter within the message
	Final  bool   // the message's last flush
	Delta  string
}

type liveRecord struct {
	Prompt string `json:"prompt,omitempty"`
	Turn   string `json:"turn"`
	Msg    string `json:"msg"`
	Index  int    `json:"i"`
	Final  bool   `json:"final,omitempty"`
	Delta  string `json:"d"`
	At     int64  `json:"at"` // unix nanoseconds, when the hook appended this flush
}

// LiveReply is the message being streamed, assembled from its flushes.
type LiveReply struct {
	Text   string
	Prompt string // the prompt_id of the turn the message belongs to
	// Final: the message's last flush is in Text, i.e. the message is complete.
	Final bool
	// FinalAt is when that last flush was appended; zero unless Final.
	FinalAt time.Time
	// LastAt is when the newest flush in Text was appended.
	LastAt time.Time
}

// AppendLiveText records one MessageDisplay flush. An empty delta is kept only when it is the
// final flush: that record is what marks the message complete.
func AppendLiveText(sid string, fl LiveFlush) {
	if fl.Msg == "" || (fl.Delta == "" && !fl.Final) {
		return
	}
	if err := os.MkdirAll(liveTexts.Dir(), 0o700); err != nil {
		return
	}
	path := liveTexts.Path(sid)
	if fi, err := os.Stat(path); err == nil && fi.Size() >= liveTextFileCap {
		return
	}
	b, err := json.Marshal(liveRecord{
		Prompt: fl.Prompt, Turn: fl.Turn, Msg: fl.Msg, Index: fl.Index, Final: fl.Final, Delta: fl.Delta,
		At: time.Now().UnixNano(),
	})
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
// The newest message is the one whose FIRST record comes last, not the one that wrote the last
// line: the final flush of the previous message can straggle in after the next message has
// begun (its hook process simply ran late). That rule needs the message's first record, so a
// read whose window holds the newest message without its first flush widens until it does,
// and a whole file without it means that flush's hook has not finished yet: nothing is shown.
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
	size := fi.Size()
	for window := int64(liveTextWindow); ; window *= 4 {
		off := max(0, size-window)
		recs, ok := newestMessage(f, off, size)
		if !ok {
			return LiveReply{}, false
		}
		if recs[0].Index == 0 {
			return assemble(recs), true
		}
		if off == 0 {
			return LiveReply{}, false
		}
	}
}

// newestMessage reads [off, size) of the file and returns the records of its newest message,
// in index order.
func newestMessage(f *os.File, off, size int64) ([]liveRecord, bool) {
	buf, err := io.ReadAll(io.NewSectionReader(f, off, size-off))
	if err != nil {
		return nil, false
	}
	if off > 0 {
		// The window starts mid-line; that fragment belongs to a record we cannot read whole.
		i := bytes.IndexByte(buf, '\n')
		if i < 0 {
			return nil, false
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
		return nil, false
	}
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].Index < recs[j].Index })
	return recs, true
}

// assemble joins a message's records, which start at index 0, up to the first missing index.
func assemble(recs []liveRecord) LiveReply {
	out := LiveReply{Prompt: recs[0].Prompt}
	var text bytes.Buffer
	next := 0
	for _, rec := range recs {
		if rec.Index < next {
			continue // the same flush recorded twice
		}
		if rec.Index > next {
			break // an earlier flush has not landed yet
		}
		text.WriteString(rec.Delta)
		out.LastAt = time.Unix(0, rec.At)
		next++
		if rec.Final {
			out.Final = true
			out.FinalAt = out.LastAt
			break
		}
	}
	out.Text = text.String()
	return out
}

// RemoveLiveText drops sid's streamed text; called when the turn ends.
func RemoveLiveText(sid string) { liveTexts.Remove(sid) }
