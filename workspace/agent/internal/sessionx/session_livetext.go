package sessionx

import (
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// The reply claude is still writing, for the mirror's in-progress block (#1250). claude appends
// an assistant text block to its jsonl only once the block is complete, so without this the
// mirror shows nothing while a long reply is generated and then all of it at once. The
// MessageDisplay hook hands us the reply line by line meanwhile (status.AppendLiveText).

const (
	// liveFinalGrace is how long a finished message may still be shown from the live-text
	// file. Its transcript row lands within milliseconds of the last flush, before or after it.
	// The grace covers a row the text match in liveReplyText misses (claude strips memory tags
	// from what it displays but stores them), which would otherwise duplicate the reply until
	// the turn ends.
	liveFinalGrace = 3 * time.Second
	// liveStaleAfter drops a message that stopped mid-way: no new line for this long and no
	// final flush means its turn was cut off (interrupted, or claude died) before it ended. A
	// line is flushed once it ends, and even a long paragraph ends well within this.
	liveStaleAfter = 2 * time.Minute
	// liveTextMax bounds what one poll carries. The tail of a very long reply is enough for a
	// preview, and the Console renders it as Markdown again on every poll.
	liveTextMax = 16 << 10
)

// wantsLiveReply: the in-progress reply goes out only when the Console asks for it (its
// per-kind "stream replies" setting) and only while a turn runs. With the setting off the
// response is exactly what it always was, and at rest the field is absent, so an unchanged
// poll stays byte-identical (TestUnchangedPollIsByteIdentical).
func wantsLiveReply(r *http.Request, alive bool, state string) bool {
	return alive && state == "working" && r.URL.Query().Get("live") == "1"
}

// liveReplyText is the streamed text of the newest assistant message that the transcript does
// not show yet, or "". lines is the whole transcript.
func liveReplyText(sid string, lines [][]byte, now time.Time) string {
	lr, ok := status.ReadLiveText(sid)
	if !ok {
		return ""
	}
	current := status.ReadLivePrompt(sid)
	switch {
	case lr.Final && now.Sub(lr.FinalAt) > liveFinalGrace:
		return ""
	case !lr.Final && now.Sub(lr.LastAt) > liveStaleAfter:
		return ""
	case lr.Prompt != "" && current != "" && lr.Prompt != current:
		// A message of an earlier turn. A turn that ends without Stop (interrupted, healed from
		// the pane) leaves its last message in the file, and a flush that lands after Stop
		// re-creates it, while the next turn has not streamed anything yet.
		return ""
	}
	text := strings.TrimSpace(lr.Text)
	if text == "" {
		return ""
	}
	// What of this message is in the transcript already: the rows between the user row it
	// answers and its own tool results (MessageTextFrom). Without row timestamps to find that
	// boundary, the rows after the newest user row, which is exact while the message is still
	// being written.
	landed, ok := claude.MessageTextFrom(lines, lr.FirstAt)
	if !ok {
		landed = claude.PendingAssistantText(lines)
	}
	landed = strings.TrimSpace(landed)
	if landed != "" {
		if strings.HasPrefix(landed, text) {
			return ""
		}
		// A response with text on both sides of a server tool call lands one text block at a
		// time; what is left is the part still missing.
		if strings.HasPrefix(text, landed) {
			text = strings.TrimSpace(text[len(landed):])
			if text == "" {
				return ""
			}
		}
	}
	// Left over: a one-line message has its only flush at its end, so a tool of its own that
	// finishes before the hook has appended that flush puts its tool results before FirstAt, and
	// the boundary lands after the message. It is then shown until liveFinalGrace ends.
	if len(text) > liveTextMax {
		cut := text[len(text)-liveTextMax:]
		if i := strings.IndexByte(cut, '\n'); i >= 0 {
			cut = cut[i+1:]
		}
		for len(cut) > 0 && !utf8.RuneStart(cut[0]) {
			cut = cut[1:]
		}
		text = "…\n" + cut
	}
	return text
}
