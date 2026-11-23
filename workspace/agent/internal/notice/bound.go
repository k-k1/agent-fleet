package notice

import (
	"bytes"
	"encoding/json"
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

// boundPayload returns p when it encodes within maxPayloadBytes, and what shrinkPayload keeps of
// it otherwise. Only the outbox copy is bounded: the chat bridge gets the event as given.
func boundPayload(p map[string]any) map[string]any {
	b, err := json.Marshal(p)
	if err != nil || len(b) <= maxPayloadBytes {
		return p // a marshal error fails the Put on its own
	}
	var out map[string]any
	if json.Unmarshal(shrinkPayload(b, maxPayloadBytes, maxPayloadValue), &out) != nil {
		return map[string]any{payloadTruncatedKey: true}
	}
	return out
}

// shrinkPayload keeps what fits of a JSON object past the bound, in order: each top-level member
// whose value encodes within maxValue, a long string cut to maxValue bytes, and a long
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
				keep, _ = json.Marshal(headBytes(s, maxValue))
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

// questionHeads reduces a questions list to the first questions' text and header, or nil when it
// is not a list of objects.
func questionHeads(v []byte, maxValue int) []byte {
	var qs []struct {
		Question string `json:"question"`
		Header   string `json:"header,omitempty"`
	}
	if json.Unmarshal(v, &qs) != nil || len(qs) == 0 {
		return nil
	}
	const maxQuestions = 4
	if len(qs) > maxQuestions {
		qs = qs[:maxQuestions]
	}
	for i := range qs {
		qs[i].Question = headBytes(qs[i].Question, maxValue/maxQuestions-256)
		qs[i].Header = headBytes(qs[i].Header, 128)
	}
	b, _ := json.Marshal(qs)
	return b
}

// headBytes cuts s to at most n bytes without splitting a UTF-8 sequence.
func headBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
