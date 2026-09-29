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
	if !ok || (lr.Final && now.Sub(lr.FinalAt) > liveFinalGrace) {
		return ""
	}
	text := strings.TrimSpace(lr.Text)
	if text == "" {
		return ""
	}
	// Landed: the transcript's newest assistant text starts with what was streamed. When only
	// the head of the message has landed (a response with text on both sides of a server tool
	// call is written as one row per text block), what is left is the part still missing.
	if landed := strings.TrimSpace(claude.LatestAssistantText(lines)); landed != "" {
		switch {
		case lr.Partial:
			if strings.Contains(landed, text) {
				return ""
			}
		case strings.HasPrefix(landed, text):
			return ""
		case strings.HasPrefix(text, landed):
			text = strings.TrimSpace(text[len(landed):])
			if text == "" {
				return ""
			}
		}
	}
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
