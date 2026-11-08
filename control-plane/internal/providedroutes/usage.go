package providedroutes

import (
	"bytes"
	"encoding/binary"
	"encoding/json"

	"github.com/k-k1/agent-fleet/control-plane/internal/routeprice"
)

// Terminal is what settles a call (ADR 0129 D4): the usage a response proved complete at its
// end, or nothing. It is deliberately stricter than the Agent's ledger observer
// (workspace/agent/internal/routegw/usage.go), which may take message_start and message_delta
// as an estimate: a settlement takes only totals the upstream stated as final — the whole
// non-stream body, the invocation metrics of a stream's closing chunk, Converse's metadata
// event — and a stream that ended without its closing event, or with an exception, settles at
// the reservation.
type Terminal struct {
	op     string
	status int
	stream bool // a 2xx answer to a stream operation: an AWS event stream
	buf    []byte
	body   bytes.Buffer
	off    bool
	failed bool // an exception event, or framing that is not an event stream

	u    routeprice.Usage
	done bool // the terminal totals were read

	// message_start's cache counts: an input-side figure fixed when the stream opens, used only
	// beside closing metrics that omit the cache split.
	startCacheRead, startCacheWrite int64
	metricsCache                    bool
}

// maxTerminalObserved bounds what is kept: past it the call settles at the reservation.
const maxTerminalObserved = 16 << 20

// NewTerminal observes a response to op that came with status. A non-2xx answer is a plain
// body whatever the operation.
func NewTerminal(op string, status int) *Terminal {
	return &Terminal{op: op, status: status, stream: IsStream(op) && status >= 200 && status <= 299}
}

// Write takes the bytes as they are forwarded. It never fails.
func (t *Terminal) Write(p []byte) (int, error) {
	n := len(p)
	if t.off {
		return n, nil
	}
	if !t.stream {
		if t.body.Len()+n > maxTerminalObserved {
			t.off, t.failed = true, true
			t.body.Reset()
			return n, nil
		}
		t.body.Write(p)
		return n, nil
	}
	t.buf = append(t.buf, p...)
	for !t.off {
		total, ok := t.frame()
		if !ok {
			break
		}
		t.buf = t.buf[total:]
	}
	if len(t.buf) > maxTerminalObserved {
		t.off, t.failed, t.buf = true, true, nil
	}
	return n, nil
}

func (t *Terminal) frame() (int, bool) {
	if len(t.buf) < 12 {
		return 0, false
	}
	total := int(binary.BigEndian.Uint32(t.buf[0:4]))
	hlen := int(binary.BigEndian.Uint32(t.buf[4:8]))
	if total < 16 || total > maxTerminalObserved || hlen > total-16 {
		t.off, t.failed, t.buf = true, true, nil
		return 0, false
	}
	if len(t.buf) < total {
		return 0, false
	}
	h := headerStrings(t.buf[12 : 12+hlen])
	payload := t.buf[12+hlen : total-4]
	if h[":message-type"] != "" && h[":message-type"] != "event" {
		// An exception or error message: whatever was counted is not a terminal total.
		t.failed = true
	} else {
		t.event(h[":event-type"], payload)
	}
	return total, true
}

// headerStrings reads the string-valued headers of one event-stream message.
func headerStrings(h []byte) map[string]string {
	out := map[string]string{}
	for len(h) > 0 {
		nl := int(h[0])
		if len(h) < 1+nl+1 {
			return out
		}
		name, typ := string(h[1:1+nl]), h[1+nl]
		h = h[2+nl:]
		var size int
		switch typ {
		case 0, 1:
		case 2:
			size = 1
		case 3:
			size = 2
		case 4:
			size = 4
		case 5, 8:
			size = 8
		case 9:
			size = 16
		case 6, 7:
			if len(h) < 2 {
				return out
			}
			l := int(binary.BigEndian.Uint16(h[:2]))
			if len(h) < 2+l {
				return out
			}
			if typ == 7 {
				out[name] = string(h[2 : 2+l])
			}
			size = 2 + l
		default:
			return out
		}
		if len(h) < size {
			return out
		}
		h = h[size:]
	}
	return out
}

func (t *Terminal) event(kind string, payload []byte) {
	switch kind {
	case "chunk":
		var w struct {
			Bytes []byte `json:"bytes"`
		}
		if json.Unmarshal(payload, &w) == nil && len(w.Bytes) > 0 {
			t.modelEvent(w.Bytes)
		}
	case "metadata":
		var m struct {
			Usage *converseUsage `json:"usage"`
		}
		if json.Unmarshal(payload, &m) == nil && m.Usage != nil && m.Usage.In != nil && m.Usage.Out != nil {
			t.u, t.done = m.Usage.usage(), true
		}
	}
}

func (t *Terminal) modelEvent(b []byte) {
	var e struct {
		Type    string `json:"type"`
		Message struct {
			Usage struct {
				CacheRead  int64 `json:"cache_read_input_tokens"`
				CacheWrite int64 `json:"cache_creation_input_tokens"`
			} `json:"usage"`
		} `json:"message"`
		Metrics *struct {
			In         *int64 `json:"inputTokenCount"`
			Out        *int64 `json:"outputTokenCount"`
			CacheRead  *int64 `json:"cacheReadInputTokenCount"`
			CacheWrite *int64 `json:"cacheWriteInputTokenCount"`
		} `json:"amazon-bedrock-invocationMetrics"`
	}
	if json.Unmarshal(b, &e) != nil {
		return
	}
	if e.Type == "message_start" {
		t.startCacheRead, t.startCacheWrite = e.Message.Usage.CacheRead, e.Message.Usage.CacheWrite
	}
	if m := e.Metrics; m != nil && m.In != nil && m.Out != nil {
		t.u = routeprice.Usage{In: *m.In, Out: *m.Out}
		if m.CacheRead != nil || m.CacheWrite != nil {
			t.metricsCache = true
			t.u.CacheRead, t.u.CacheWrite = deref(m.CacheRead), deref(m.CacheWrite)
		}
		t.done = true
	}
}

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

type converseUsage struct {
	In         *int64 `json:"inputTokens"`
	Out        *int64 `json:"outputTokens"`
	CacheRead  int64  `json:"cacheReadInputTokens"`
	CacheWrite int64  `json:"cacheWriteInputTokens"`
}

func (c converseUsage) usage() routeprice.Usage {
	return routeprice.Usage{In: *c.In, Out: *c.Out, CacheRead: c.CacheRead, CacheWrite: c.CacheWrite}
}

// Final is the call's complete terminal usage. ok is false unless the response proved its
// totals: a 2xx answer read to its clean end (endedClean) that carried them.
func (t *Terminal) Final(endedClean bool) (routeprice.Usage, bool) {
	if t.status < 200 || t.status > 299 || !endedClean || t.failed {
		return routeprice.Usage{}, false
	}
	switch t.op {
	case OpInvoke:
		var b struct {
			StopReason *string `json:"stop_reason"`
			Usage      *struct {
				In         *int64 `json:"input_tokens"`
				Out        *int64 `json:"output_tokens"`
				CacheRead  int64  `json:"cache_read_input_tokens"`
				CacheWrite int64  `json:"cache_creation_input_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(t.body.Bytes(), &b) != nil || b.Usage == nil || b.Usage.In == nil || b.Usage.Out == nil || b.StopReason == nil {
			return routeprice.Usage{}, false
		}
		return routeprice.Usage{In: *b.Usage.In, Out: *b.Usage.Out, CacheRead: b.Usage.CacheRead, CacheWrite: b.Usage.CacheWrite}, true
	case OpConverse:
		var b struct {
			Usage *converseUsage `json:"usage"`
		}
		if json.Unmarshal(t.body.Bytes(), &b) != nil || b.Usage == nil || b.Usage.In == nil || b.Usage.Out == nil {
			return routeprice.Usage{}, false
		}
		return b.Usage.usage(), true
	case OpInvokeStream:
		if !t.done || len(t.buf) != 0 {
			return routeprice.Usage{}, false
		}
		u := t.u
		if !t.metricsCache {
			u.CacheRead, u.CacheWrite = t.startCacheRead, t.startCacheWrite
		}
		return u, true
	case OpConverseStream:
		if !t.done || len(t.buf) != 0 {
			return routeprice.Usage{}, false
		}
		return t.u, true
	}
	return routeprice.Usage{}, false
}

// CleanRefusal reports an upstream answer that refused the call before any inference: a non-2xx
// status whose whole body was read and is one AWS JSON error object, not an event stream.
func (t *Terminal) CleanRefusal(endedClean bool) bool {
	if t.status < 400 || !endedClean || t.off {
		return false
	}
	var e map[string]json.RawMessage
	if json.Unmarshal(t.body.Bytes(), &e) != nil {
		return false
	}
	_, msg := e["message"]
	_, msg2 := e["Message"]
	return msg || msg2
}
