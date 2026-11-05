package routegw

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"strconv"
)

// Usage is the token counts of one forwarded call, as far as the response said. Known is false
// when it said nothing: that is "not measured", never zero (usagex.MeasuredNone).
type Usage struct {
	In, Out, CacheRead, CacheCreate int
	Known                           bool
}

// maxObserved bounds what the observer keeps: a body past it is forwarded unobserved, because a
// usage figure is never worth holding a large response in memory for.
const maxObserved = 16 << 20

// usageObserver reads a response's usage from the bytes as they are forwarded. It never fails
// and never delays: Write always accepts everything, and an unparseable response just leaves
// Usage unknown. It reads no text of a prompt or an answer into any record: only counts.
type usageObserver struct {
	stream bool // an AWS event stream (invoke-with-response-stream, converse-stream)
	buf    []byte
	body   bytes.Buffer // a non-stream body, for the usage object it carries
	off    bool
	u      Usage
	// messageStart/messageDelta accumulate Anthropic's own streaming usage when the closing
	// invocation metrics are absent.
	startIn, startCacheRead, startCacheCreate, deltaOut int
	haveMetrics                                         bool
}

func newUsageObserver(op string) *usageObserver {
	return &usageObserver{stream: op == "invoke-with-response-stream" || op == "converse-stream"}
}

func (o *usageObserver) Write(p []byte) (int, error) {
	n := len(p)
	if o.off {
		return n, nil
	}
	if !o.stream {
		if o.body.Len()+n > maxObserved {
			o.off = true
			o.body.Reset()
			return n, nil
		}
		o.body.Write(p)
		return n, nil
	}
	o.buf = append(o.buf, p...)
	for {
		total, ok := o.frame()
		if !ok {
			break
		}
		o.buf = o.buf[total:]
	}
	if len(o.buf) > maxObserved {
		o.off, o.buf = true, nil
	}
	return n, nil
}

// frame consumes one complete event-stream message from the front of the buffer and returns its
// length. ok is false when more bytes are needed or the stream is not one (the observer then
// turns itself off).
func (o *usageObserver) frame() (int, bool) {
	if len(o.buf) < 12 {
		return 0, false
	}
	total := int(binary.BigEndian.Uint32(o.buf[0:4]))
	hlen := int(binary.BigEndian.Uint32(o.buf[4:8]))
	if total < 16 || total > maxObserved || hlen > total-16 {
		o.off, o.buf = true, nil
		return 0, false
	}
	if len(o.buf) < total {
		return 0, false
	}
	headers := o.buf[12 : 12+hlen]
	payload := o.buf[12+hlen : total-4]
	o.event(eventType(headers), payload)
	return total, true
}

// eventType reads the ":event-type" header of an event-stream message, "" when absent.
func eventType(h []byte) string {
	for len(h) > 0 {
		nl := int(h[0])
		if len(h) < 1+nl+1 {
			return ""
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
				return ""
			}
			l := int(binary.BigEndian.Uint16(h[:2]))
			if len(h) < 2+l {
				return ""
			}
			if typ == 7 && name == ":event-type" {
				return string(h[2 : 2+l])
			}
			size = 2 + l
		default:
			return ""
		}
		if len(h) < size {
			return ""
		}
		h = h[size:]
	}
	return ""
}

func (o *usageObserver) event(kind string, payload []byte) {
	switch kind {
	case "chunk": // invoke-with-response-stream: {"bytes":"<base64 of the model's own event>"}
		var w struct {
			Bytes []byte `json:"bytes"`
		}
		if json.Unmarshal(payload, &w) != nil || len(w.Bytes) == 0 {
			return
		}
		o.modelEvent(w.Bytes)
	case "metadata": // converse-stream
		var m struct {
			Usage *converseUsage `json:"usage"`
		}
		if json.Unmarshal(payload, &m) == nil && m.Usage != nil {
			o.u = m.Usage.usage()
		}
	}
}

// modelEvent reads one decoded Anthropic-on-Bedrock stream event. The closing chunk carries
// Bedrock's own invocation metrics, which win; message_start and message_delta are the fallback
// for a model that does not.
func (o *usageObserver) modelEvent(b []byte) {
	var e struct {
		Type    string `json:"type"`
		Message struct {
			Usage anthropicUsage `json:"usage"`
		} `json:"message"`
		Usage   anthropicUsage `json:"usage"`
		Metrics *struct {
			In  *int `json:"inputTokenCount"`
			Out *int `json:"outputTokenCount"`
		} `json:"amazon-bedrock-invocationMetrics"`
	}
	if json.Unmarshal(b, &e) != nil {
		return
	}
	switch {
	case e.Metrics != nil && e.Metrics.In != nil && e.Metrics.Out != nil:
		o.haveMetrics = true
		o.u.In, o.u.Out, o.u.Known = *e.Metrics.In, *e.Metrics.Out, true
	case e.Type == "message_start":
		o.startIn, o.startCacheRead, o.startCacheCreate = e.Message.Usage.In, e.Message.Usage.CacheRead, e.Message.Usage.CacheCreate
	case e.Type == "message_delta":
		o.deltaOut = e.Usage.Out
	}
}

type anthropicUsage struct {
	In          int `json:"input_tokens"`
	Out         int `json:"output_tokens"`
	CacheRead   int `json:"cache_read_input_tokens"`
	CacheCreate int `json:"cache_creation_input_tokens"`
}

type converseUsage struct {
	In          int `json:"inputTokens"`
	Out         int `json:"outputTokens"`
	CacheRead   int `json:"cacheReadInputTokens"`
	CacheCreate int `json:"cacheWriteInputTokens"`
}

func (c converseUsage) usage() Usage {
	return Usage{In: c.In, Out: c.Out, CacheRead: c.CacheRead, CacheCreate: c.CacheCreate, Known: true}
}

// Finish is the call's usage once the response ended. hdr is the response's header: Bedrock's
// invoke answers carry the counts there.
func (o *usageObserver) Finish(op string, hdr http.Header) Usage {
	switch op {
	case "invoke":
		u := Usage{}
		in, ok1 := headerInt(hdr, "X-Amzn-Bedrock-Input-Token-Count")
		out, ok2 := headerInt(hdr, "X-Amzn-Bedrock-Output-Token-Count")
		if ok1 && ok2 {
			u = Usage{In: in, Out: out, Known: true}
		}
		var b struct {
			Usage *anthropicUsage `json:"usage"`
		}
		if !o.off && json.Unmarshal(o.body.Bytes(), &b) == nil && b.Usage != nil {
			if !u.Known {
				u.In, u.Out, u.Known = b.Usage.In, b.Usage.Out, true
			}
			u.CacheRead, u.CacheCreate = b.Usage.CacheRead, b.Usage.CacheCreate
		}
		return u
	case "converse":
		var b struct {
			Usage *converseUsage `json:"usage"`
		}
		if !o.off && json.Unmarshal(o.body.Bytes(), &b) == nil && b.Usage != nil {
			return b.Usage.usage()
		}
	case "invoke-with-response-stream":
		u := o.u
		if o.haveMetrics {
			u.CacheRead, u.CacheCreate = o.startCacheRead, o.startCacheCreate
		} else if o.startIn > 0 || o.deltaOut > 0 {
			u = Usage{In: o.startIn, Out: o.deltaOut, CacheRead: o.startCacheRead, CacheCreate: o.startCacheCreate, Known: true}
		}
		return u
	case "converse-stream":
		return o.u
	}
	return Usage{}
}

func headerInt(h http.Header, k string) (int, bool) {
	n, err := strconv.Atoi(h.Get(k))
	return n, err == nil && n >= 0
}
