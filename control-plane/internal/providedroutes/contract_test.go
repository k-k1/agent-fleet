package providedroutes

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/routeprice"
)

func TestParseCall(t *testing.T) {
	for _, c := range []struct {
		method, path, model, op, code string
	}{
		{"POST", "/model/us.anthropic.claude-opus-5-5/invoke-with-response-stream", "us.anthropic.claude-opus-5-5", OpInvokeStream, ""},
		{"POST", "/model/anthropic.claude-haiku-4-5-20251001-v1%3A0/converse", "anthropic.claude-haiku-4-5-20251001-v1:0", OpConverse, ""},
		// Decoded once: a double-encoded colon stays "%3A" and is not a model id.
		{"POST", "/model/anthropic.claude-haiku-4-5-20251001-v1%253A0/converse", "", "", CodeModelNotAllowed},
		{"GET", "/model/us.anthropic.claude-opus-5-5/invoke", "", "", CodeOperationNotAllowed},
		{"POST", "/model/us.anthropic.claude-opus-5-5/count-tokens", "", "", CodeOperationNotAllowed},
		{"POST", "/foundation-models", "", "", CodeOperationNotAllowed},
		{"POST", "/model/x/invoke/extra", "", "", CodeOperationNotAllowed},
		// Any ARN, escaped or not, is refused: an application inference profile of another
		// account must not be reachable by naming it.
		{"POST", "/model/arn%3Aaws%3Abedrock%3Aus-east-1%3A111122223333%3Aapplication-inference-profile%2Fabc/invoke", "", "", CodeModelNotAllowed},
		{"POST", "/model/ARN:aws:bedrock:us-east-1::foundation-model%2Fx/invoke", "", "", CodeModelNotAllowed},
	} {
		model, op, ref := ParseCall(c.method, c.path)
		if code := codeOf(ref); code != c.code || model != c.model || op != c.op {
			t.Errorf("%s %s = %q %q %v, want %q %q %q", c.method, c.path, model, op, ref, c.model, c.op, c.code)
		}
	}
}

func codeOf(r *Refusal) string {
	if r == nil {
		return ""
	}
	return r.Code
}

func TestCheckModelIsVerbatim(t *testing.T) {
	allow := []string{"us.anthropic.claude-opus-5-5"}
	if r := CheckModel(allow, "us.anthropic.claude-opus-5-5"); r != nil {
		t.Fatal(r)
	}
	for _, m := range []string{"US.anthropic.claude-opus-5-5", "anthropic.claude-opus-5-5", "us.anthropic.claude-opus-5", "eu.anthropic.claude-opus-5-5"} {
		if r := CheckModel(allow, m); codeOf(r) != CodeModelNotAllowed {
			t.Errorf("%s: %v", m, r)
		}
	}
}

func TestUpstreamPathEscapesLikeTheSDK(t *testing.T) {
	p, raw := UpstreamPath("anthropic.claude-haiku-4-5-20251001-v1:0", OpInvoke)
	if p != "/model/anthropic.claude-haiku-4-5-20251001-v1:0/invoke" || raw != "/model/anthropic.claude-haiku-4-5-20251001-v1%3A0/invoke" {
		t.Fatalf("%q %q", p, raw)
	}
}

// Only content-type, accept and the named Bedrock header pass; the caller's Authorization never
// does; anything that names identity, credentials, a Region, a role, an external id or a tag is
// refused rather than dropped.
func TestUpstreamHeaders(t *testing.T) {
	in := http.Header{
		"Content-Type":            {"application/json"},
		"Accept":                  {"application/vnd.amazon.eventstream"},
		"X-Amzn-Bedrock-Accept":   {"application/json"},
		"Authorization":           {"Bearer afr_x"},
		"User-Agent":              {"claude-cli"},
		"Anthropic-Version":       {"bedrock-2023-05-31"},
		"X-Stainless-Retry-Count": {"0"},
	}
	out, ref := UpstreamHeaders(in)
	if ref != nil {
		t.Fatal(ref)
	}
	if len(out) != 3 || out.Get("Authorization") != "" || out.Get("X-Amzn-Bedrock-Accept") != "application/json" {
		t.Fatalf("out = %v", out)
	}
	for _, h := range []string{"X-Amz-Security-Token", "X-Amz-Date", "X-Amz-Target", "X-Amzn-Bedrock-GuardrailIdentifier",
		"X-Amzn-Bedrock-Service-Tier", "X-Amzn-Bedrock-PerformanceConfig-Latency", "X-Af-Membership", "X-Af-Tenant",
		"X-Region", "Aws-Region", "X-Role-Arn", "X-External-Id", "Externalid", "X-Session-Tags", "Tag"} {
		if _, ref := UpstreamHeaders(http.Header{h: {"v"}}); codeOf(ref) != CodeRequestRefused {
			t.Errorf("%s: %v", h, ref)
		}
	}
	if out, _ := UpstreamHeaders(http.Header{}); out.Get("Content-Type") != "application/json" {
		t.Fatalf("default content type = %v", out)
	}
}

func TestOutputCap(t *testing.T) {
	for _, c := range []struct {
		op, body string
		cap      int64
		code     string
	}{
		{OpInvokeStream, `{"anthropic_version":"bedrock-2023-05-31","max_tokens":32000,"messages":[]}`, 32000, ""},
		{OpInvoke, `{"messages":[]}`, 0, CodeOutputCap},
		{OpInvoke, `{"max_tokens":0}`, 0, CodeOutputCap},
		{OpInvoke, `{"max_tokens":1.5}`, 0, CodeOutputCap},
		{OpInvoke, `{"max_tokens":"100"}`, 0, CodeOutputCap},
		{OpInvoke, `{"max_tokens":100,"service_tier":"priority"}`, 0, CodeRequestRefused},
		{OpInvoke, `[1]`, 0, CodeRequestRefused},
		{OpInvoke, `{"max_tokens":1} {"max_tokens":2}`, 0, CodeRequestRefused},
		{OpConverseStream, `{"messages":[],"inferenceConfig":{"maxTokens":4096}}`, 4096, ""},
		{OpConverse, `{"messages":[]}`, 0, CodeOutputCap},
		{OpConverse, `{"messages":[],"inferenceConfig":{"maxTokens":10},"performanceConfig":{"latency":"optimized"}}`, 0, CodeRequestRefused},
		{OpConverse, `{"messages":[],"inferenceConfig":{"maxTokens":10},"guardrailConfig":{}}`, 0, CodeRequestRefused},
		{OpConverse, `{"messages":[],"inferenceConfig":{"maxTokens":10},"serviceTier":{"type":"flex"}}`, 0, CodeRequestRefused},
	} {
		n, ref := OutputCap(c.op, []byte(c.body))
		if n != c.cap || codeOf(ref) != c.code {
			t.Errorf("%s %s = %d %v, want %d %q", c.op, c.body, n, ref, c.cap, c.code)
		}
	}
}

func TestSessionPolicyIsTheClosure(t *testing.T) {
	role := "arn:aws:iam::123456789012:role/af-model-route/bedrock"
	pol, err := SessionPolicy(role, "us-east-1", []string{"us.anthropic.claude-opus-5-5", "anthropic.claude-haiku-5-5"})
	if err != nil {
		t.Fatal(err)
	}
	var doc policyDoc
	if err := json.Unmarshal([]byte(pol), &doc); err != nil {
		t.Fatal(err)
	}
	want := `{"Version":"2012-10-17","Statement":[` +
		`{"Sid":"Invoke","Effect":"Allow","Action":["bedrock:InvokeModel","bedrock:InvokeModelWithResponseStream"],"Resource":[` +
		`"arn:aws:bedrock:us-east-1::foundation-model/anthropic.claude-haiku-5-5",` +
		`"arn:aws:bedrock:us-east-1:123456789012:inference-profile/us.anthropic.claude-opus-5-5"]},` +
		`{"Sid":"ProfileModels","Effect":"Allow","Action":["bedrock:InvokeModel","bedrock:InvokeModelWithResponseStream"],` +
		`"Resource":["arn:aws:bedrock:*::foundation-model/anthropic.claude-opus-5-5"],` +
		`"Condition":{"StringEquals":{"bedrock:InferenceProfileArn":["arn:aws:bedrock:us-east-1:123456789012:inference-profile/us.anthropic.claude-opus-5-5"]}}},` +
		`{"Sid":"Profiles","Effect":"Allow","Action":["bedrock:GetInferenceProfile"],` +
		`"Resource":["arn:aws:bedrock:us-east-1:123456789012:inference-profile/us.anthropic.claude-opus-5-5"]}]}`
	if pol != want {
		t.Fatalf("policy =\n%s\nwant\n%s", pol, want)
	}
	// Nothing beyond the inference actions, ever.
	for _, bad := range []string{`"bedrock:*"`, "GetInvoke", "ListFoundationModels", "iam:", "sts:"} {
		if strings.Contains(pol, bad) {
			t.Fatalf("policy carries %s", bad)
		}
	}
	if _, err := SessionPolicy(role, "us-east-1", nil); err == nil {
		t.Fatal("an empty list built a policy")
	}
	if _, err := SessionPolicy(role, "us-east-1", []string{"meta.llama"}); err == nil {
		t.Fatal("an unknown shape built a policy")
	}
	if _, err := SessionPolicy("arn:aws:iam::1:role/x", "us-east-1", []string{"anthropic.claude-haiku-5-5"}); err == nil {
		t.Fatal("a bad role ARN built a policy")
	}
	// Every model the table has does not fit in one session policy: refused, never cut.
	var all []string
	for _, m := range routeprice.Models() {
		all = append(all, m.ID)
	}
	if _, err := SessionPolicy(role, "us-east-1", all); err == nil || !strings.Contains(err.Error(), "2048") {
		t.Fatalf("an oversized policy: %v", err)
	}
}

// eventMessage encodes one AWS event-stream message with string headers (the CRCs are not read).
func eventMessage(headers map[string]string, payload []byte) []byte {
	var h []byte
	for k, v := range headers {
		h = append(h, byte(len(k)))
		h = append(h, k...)
		h = append(h, 7)
		h = binary.BigEndian.AppendUint16(h, uint16(len(v)))
		h = append(h, v...)
	}
	total := 12 + len(h) + len(payload) + 4
	out := binary.BigEndian.AppendUint32(nil, uint32(total))
	out = binary.BigEndian.AppendUint32(out, uint32(len(h)))
	out = append(out, 0, 0, 0, 0)
	out = append(out, h...)
	out = append(out, payload...)
	return append(out, 0, 0, 0, 0)
}

func chunk(event string) []byte {
	p, _ := json.Marshal(map[string]string{"bytes": base64.StdEncoding.EncodeToString([]byte(event))})
	return eventMessage(map[string]string{":event-type": "chunk", ":message-type": "event"}, p)
}

// A settlement takes only totals the upstream stated as final.
func TestTerminalUsage(t *testing.T) {
	start := chunk(`{"type":"message_start","message":{"usage":{"input_tokens":10,"cache_read_input_tokens":100,"cache_creation_input_tokens":20}}}`)
	delta := chunk(`{"type":"message_delta","usage":{"output_tokens":7}}`)
	stop := chunk(`{"type":"message_stop","amazon-bedrock-invocationMetrics":{"inputTokenCount":10,"outputTokenCount":7}}`)

	feed := func(op string, status int, parts ...[]byte) *Terminal {
		t := NewTerminal(op, status)
		for _, p := range parts {
			// Byte by byte, so a frame split across reads is reassembled.
			for i := range p {
				t.Write(p[i : i+1])
			}
		}
		return t
	}
	if u, ok := feed(OpInvokeStream, 200, start, delta, stop).Final(true); !ok || u != (routeprice.Usage{In: 10, Out: 7, CacheRead: 100, CacheWrite: 20}) {
		t.Fatalf("complete stream = %+v %v", u, ok)
	}
	// Cut after message_delta: message_start and message_delta are an estimate, not totals.
	if _, ok := feed(OpInvokeStream, 200, start, delta).Final(true); ok {
		t.Fatal("a stream without its closing metrics settled on its deltas")
	}
	// The closing event arrived but the copy ended with an error.
	if _, ok := feed(OpInvokeStream, 200, start, delta, stop).Final(false); ok {
		t.Fatal("a stream that did not end cleanly settled")
	}
	exc := eventMessage(map[string]string{":message-type": "exception", ":exception-type": "throttlingException"}, []byte(`{"message":"x"}`))
	if _, ok := feed(OpInvokeStream, 200, start, exc, stop).Final(true); ok {
		t.Fatal("a stream with an exception settled on its counts")
	}
	if _, ok := feed(OpInvokeStream, 200, start, delta, stop[:len(stop)-3]).Final(true); ok {
		t.Fatal("a truncated closing frame settled")
	}
	meta := eventMessage(map[string]string{":event-type": "metadata", ":message-type": "event"},
		[]byte(`{"usage":{"inputTokens":5,"outputTokens":6,"cacheReadInputTokens":7,"cacheWriteInputTokens":8},"metrics":{}}`))
	if u, ok := feed(OpConverseStream, 200, meta).Final(true); !ok || u != (routeprice.Usage{In: 5, Out: 6, CacheRead: 7, CacheWrite: 8}) {
		t.Fatalf("converse stream = %+v %v", u, ok)
	}
	if _, ok := feed(OpConverseStream, 200).Final(true); ok {
		t.Fatal("a converse stream without metadata settled")
	}
	body := []byte(`{"id":"m","stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":4,"cache_read_input_tokens":1}}`)
	if u, ok := feed(OpInvoke, 200, body).Final(true); !ok || u != (routeprice.Usage{In: 3, Out: 4, CacheRead: 1}) {
		t.Fatalf("invoke = %+v %v", u, ok)
	}
	if _, ok := feed(OpInvoke, 200, body[:len(body)-5]).Final(true); ok {
		t.Fatal("a truncated body settled")
	}
	if u, ok := feed(OpConverse, 200, []byte(`{"output":{},"usage":{"inputTokens":1,"outputTokens":2}}`)).Final(true); !ok || u.In != 1 || u.Out != 2 {
		t.Fatalf("converse = %+v %v", u, ok)
	}
	// An error answer never carries totals, but a whole AWS JSON error is a clean refusal.
	refused := feed(OpInvokeStream, 429, []byte(`{"message":"Too many requests"}`))
	if _, ok := refused.Final(true); ok {
		t.Fatal("a 429 settled on usage")
	}
	if !refused.CleanRefusal(true) || refused.CleanRefusal(false) {
		t.Fatal("a whole JSON 429 is a clean refusal, a cut one is not")
	}
	if feed(OpInvoke, 500, []byte(`<html>`)).CleanRefusal(true) || feed(OpInvoke, 200, body).CleanRefusal(true) {
		t.Fatal("a non-JSON error or a success read as a clean refusal")
	}
}
