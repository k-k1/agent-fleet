package notice

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// The bound on the payload one outbox entry keeps. The Control Plane reads the outbox with a byte
// bound per response, and an entry it cannot read whole stays queued ahead of every later one, so
// an entry has to stay far below that bound. The Control Plane keeps at most twice this per
// notification (maxNotificationPayload), so nothing that fits here is cut there. A healthy
// payload is a few KiB; the largest, an answer's body, is capped at chatx.BridgeBodyCap runes.
const (
	maxPayloadBytes = 128 << 10
	// maxPayloadValue bounds each member shrinkPayload keeps of an oversized payload.
	maxPayloadValue = 8 << 10
)

// payloadTruncatedKey marks a payload shrinkPayload cut; the Control Plane uses the same key.
const payloadTruncatedKey = "payloadTruncated"

// boundPayload is p encoded as the outbox file keeps it: invalid UTF-8 a json.RawMessage value
// carried repaired, and shrunk by shrinkPayload past maxPayloadBytes. Put writes these bytes as
// they are, so the bound holds on disk; decoding them into the map and encoding again would not
// (a repaired byte or a number literal can grow). Only the outbox copy is bounded: the chat
// bridge gets the event as given.
func boundPayload(p map[string]any) (json.RawMessage, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(b) {
		b = repairUTF8(b)
	}
	if len(b) > maxPayloadBytes {
		b = shrinkPayload(b, maxPayloadBytes, maxPayloadValue)
	}
	return b, nil
}

// shrinkPayload keeps what fits of a JSON object past the bound, in order: each top-level member
// whose value encodes within maxValue, a long string cut to an encoding of maxValue bytes, and a long
// "questions" list reduced to each question's text, which Web Push shows. The identifiers the
// Console navigates by are short and survive. The result carries payloadTruncatedKey and stays
// within max. Each member is decoded raw, so nothing is expanded. The Control Plane applies the
// same rule (control-plane/notification.go) to what an older Agent sends.
func shrinkPayload(obj []byte, max, maxValue int) []byte {
	const mark = `"` + payloadTruncatedKey + `":true}`
	dec := json.NewDecoder(bytes.NewReader(obj))
	out := []byte{'{'}
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return []byte("{" + mark)
	}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			break
		}
		var v json.RawMessage
		if dec.Decode(&v) != nil {
			break
		}
		k, _ := kt.(string)
		if k == payloadTruncatedKey || len(k) > 256 {
			continue
		}
		var keep []byte
		switch {
		case len(v) <= maxValue:
			keep = v
		case v[0] == '"':
			var s string
			if json.Unmarshal(v, &s) == nil {
				keep = fitString(s, maxValue)
			}
		case k == "questions":
			keep = questionHeads(v, maxValue)
		}
		if keep == nil {
			continue
		}
		kb, _ := json.Marshal(k)
		if len(out)+len(kb)+len(keep)+2+len(mark) > max {
			continue
		}
		out = append(append(append(append(out, kb...), ':'), keep...), ',')
	}
	return append(out, mark...)
}

// questionHeads reduces a questions list to the first questions' text (leading space trimmed,
// as Web Push trims it) and header, encoded within maxValue, or nil when it does not start as a
// list of objects. It reads the list element by element and stops after maxQuestions, so a list
// of millions of small elements is never decoded.
func questionHeads(v []byte, maxValue int) []byte {
	const maxQuestions = 4
	dec := json.NewDecoder(bytes.NewReader(v))
	if t, err := dec.Token(); err != nil || t != json.Delim('[') {
		return nil
	}
	out := []byte{'['}
	for n := 0; n < maxQuestions && dec.More(); n++ {
		// Raw fields, so a field of the wrong type costs only that field, as it does in
		// pushsrv.questionText, which reads the question alone.
		var q struct {
			Question json.RawMessage `json:"question"`
			Header   json.RawMessage `json:"header"`
		}
		if dec.Decode(&q) != nil {
			return nil
		}
		var question, header string
		_ = json.Unmarshal(q.Question, &question)
		_ = json.Unmarshal(q.Header, &header)
		if n > 0 {
			out = append(out, ',')
		}
		out = append(append(out, `{"question":`...), fitString(strings.TrimSpace(question), maxValue/maxQuestions-192)...)
		if header != "" {
			out = append(append(out, `,"header":`...), fitString(header, 128)...)
		}
		out = append(out, '}')
	}
	if len(out) == 1 {
		return nil
	}
	return append(out, ']')
}

// fitString is the longest head of s, cut on a rune boundary, whose encoding as a JSON string
// fits n bytes. The bound is on the encoding, not the text: an escaped character such as '<'
// takes six bytes. The encoded length only grows with the head, so a binary search finds it.
func fitString(s string, n int) []byte {
	s = headBytes(s, n)
	if b, _ := json.Marshal(s); len(b) <= n {
		return b
	}
	lo, hi := 0, len(s) // the head of lo bytes fits, the head of hi does not
	for hi-lo > 1 {
		mid := (lo + hi) / 2
		if b, _ := json.Marshal(headBytes(s, mid)); len(b) <= n {
			lo = mid
		} else {
			hi = mid
		}
	}
	b, _ := json.Marshal(headBytes(s, lo))
	return b
}

// repairUTF8 replaces each byte that is not part of valid UTF-8 with U+FFFD, one per byte, which
// is what decoding the JSON into a Go string does.
func repairUTF8(b []byte) []byte {
	out := make([]byte, 0, len(b)+len(b)/8)
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		if r == utf8.RuneError && size == 1 {
			out = append(out, "\uFFFD"...)
		} else {
			out = append(out, b[:size]...)
		}
		b = b[size:]
	}
	return out
}

// headBytes cuts s to at most n bytes without splitting a UTF-8 sequence.
func headBytes(s string, n int) string {
	if n < 0 {
		n = 0
	}
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
