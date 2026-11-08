package providedroutes

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// The CP's signing contract for a provided route (ADR 0129 D2): the CP gateway signs a few model
// operations and nothing else. Every rule here refuses rather than ignores, because the role
// behind a source may be broader than it should be and these rules are what narrow it. Nothing
// the caller sends is read as identity; the caller here is untrusted (the member can extract the
// CP route token and call the CP directly).

// Bedrock operations a provided route signs. Each needs bedrock:InvokeModel or
// bedrock:InvokeModelWithResponseStream and nothing else.
const (
	OpInvoke         = "invoke"
	OpInvokeStream   = "invoke-with-response-stream"
	OpConverse       = "converse"
	OpConverseStream = "converse-stream"
)

// IsStream reports whether op answers with an AWS event stream.
func IsStream(op string) bool { return op == OpInvokeStream || op == OpConverseStream }

// Refusal is a contract refusal: an HTTP status, a stable code and a message the member can act
// on. It never carries a credential or a token.
type Refusal struct {
	Status int
	Code   string
	Msg    string
}

func (r *Refusal) Error() string { return r.Code + ": " + r.Msg }

func refuse(status int, code, format string, a ...any) *Refusal {
	return &Refusal{Status: status, Code: code, Msg: fmt.Sprintf(format, a...)}
}

// Refusal codes of the contract.
const (
	CodeOperationNotAllowed = "route_operation_not_allowed"
	CodeModelNotAllowed     = "route_model_not_allowed"
	CodeRequestRefused      = "route_request_refused"
	CodeOutputCap           = "route_output_cap_required"
	CodeModelUnpriced       = "route_model_unpriced"
)

var (
	callPathRe = regexp.MustCompile(`^/model/([^/]+)/(invoke|invoke-with-response-stream|converse|converse-stream)$`)
	// modelIDRe is a foundation-model or system inference profile id. No "/" and no ARN: an ARN
	// could name an application inference profile in another account.
	modelIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,199}$`)
)

// ParseCall reads the model id and the operation from the escaped path below the gateway's
// prefix. The model id is URL-decoded exactly once, so a double-encoded id stays encoded and
// matches no allow-list entry.
func ParseCall(method, escapedPath string) (model, op string, ref *Refusal) {
	m := callPathRe.FindStringSubmatch(escapedPath)
	if method != http.MethodPost || m == nil {
		return "", "", refuse(http.StatusNotFound, CodeOperationNotAllowed,
			"a provided route signs only POST /model/<id>/{invoke,invoke-with-response-stream,converse,converse-stream}")
	}
	id, err := url.PathUnescape(m[1])
	if err != nil {
		return "", "", refuse(http.StatusBadRequest, CodeModelNotAllowed, "the model id in the path is not valid")
	}
	if strings.HasPrefix(strings.ToLower(id), "arn:") {
		return "", "", refuse(http.StatusForbidden, CodeModelNotAllowed,
			"a provided route takes a model or system inference profile id, never an ARN")
	}
	if !modelIDRe.MatchString(id) {
		return "", "", refuse(http.StatusBadRequest, CodeModelNotAllowed, "the model id in the path is not valid")
	}
	return id, m[2], nil
}

// CheckModel refuses a model that is not on the source's allow-list verbatim: no case folding,
// no prefix match, no mapping of a profile onto its model.
func CheckModel(allow []string, model string) *Refusal {
	if !slices.Contains(allow, model) {
		return refuse(http.StatusForbidden, CodeModelNotAllowed,
			"model %s is not on this route's model list; ask the administrator who set up the route", model)
	}
	return nil
}

// UpstreamPath is the path the CP sends upstream for a checked call: the model id escaped the
// way the AWS SDK escapes it (":" as %3A), so the request and its signature are the SDK's.
func UpstreamPath(model, op string) (path, rawPath string) {
	return "/model/" + model + "/" + op, "/model/" + strings.ReplaceAll(url.PathEscape(model), ":", "%3A") + "/" + op
}

// passHeaders are the caller's headers that reach Bedrock, canonical names. X-Amzn-Bedrock-Accept
// is the stream operations' content type of the model's own events.
var passHeaders = []string{"Content-Type", "Accept", "X-Amzn-Bedrock-Accept"}

// refusedHeaderRe is a caller header that tries to say something the CP decides: an AWS
// credential, date or target (x-amz-*), a Bedrock option outside passHeaders (a guardrail, a
// service tier, a latency profile: priced outside the table, or reaching other resources), an AF
// identity claim (x-af-*), or a Region, role, external id or tag under any name.
var refusedHeaderRe = regexp.MustCompile(`(?i)^(x-amz-|x-amzn-|x-af-)|region|role|external-?id|(^|-)tags?($|-)`)

// UpstreamHeaders is the header set the CP sends upstream from the caller's: content-type,
// accept and passHeaders, nothing else. Authorization is never among them; the CP signs a fresh
// one. A header that names identity, credentials, a Region, a role, an external id or a tag is
// refused, not dropped, so a caller cannot tell from a success that it was ignored.
func UpstreamHeaders(in http.Header) (http.Header, *Refusal) {
	out := http.Header{}
	for k, v := range in {
		ck := http.CanonicalHeaderKey(k)
		if slices.Contains(passHeaders, ck) {
			out[ck] = slices.Clone(v)
			continue
		}
		if refusedHeaderRe.MatchString(ck) {
			return nil, refuse(http.StatusBadRequest, CodeRequestRefused,
				"header %s is not accepted on a provided route: the Control Plane decides identity, Region, role and options", ck)
		}
	}
	if out.Get("Content-Type") == "" {
		out.Set("Content-Type", "application/json")
	}
	return out, nil
}

// refusedBodyFields are top-level request fields that change the price or reach resources the
// price table does not cover. invoke's body is the model's own (Anthropic's Messages shape), so
// only the fields that move the bill are named; Converse's shape is AWS's own, and every field
// outside convFields is refused.
var refusedInvokeFields = []string{"service_tier", "inference_geo"}

var convFields = []string{"messages", "system", "inferenceConfig", "toolConfig", "additionalModelRequestFields",
	"additionalModelResponseFieldPaths", "requestMetadata"}

// OutputCap reads the request's own output cap (D4): max_tokens for an Anthropic invoke body,
// inferenceConfig.maxTokens for Converse. A body without one, or with a field that changes the
// price outside the table, is refused: without a cap there is no upper bound to reserve.
func OutputCap(op string, body []byte) (int64, *Refusal) {
	var top map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&top); err != nil || top == nil {
		return 0, refuse(http.StatusBadRequest, CodeRequestRefused, "the request body is not a JSON object")
	}
	if dec.More() {
		return 0, refuse(http.StatusBadRequest, CodeRequestRefused, "the request body carries more than one JSON value")
	}
	var raw json.RawMessage
	switch op {
	case OpInvoke, OpInvokeStream:
		for _, f := range refusedInvokeFields {
			if _, ok := top[f]; ok {
				return 0, refuse(http.StatusBadRequest, CodeRequestRefused, "field %s is not accepted on a provided route", f)
			}
		}
		raw = top["max_tokens"]
	case OpConverse, OpConverseStream:
		for f := range top {
			if !slices.Contains(convFields, f) {
				return 0, refuse(http.StatusBadRequest, CodeRequestRefused, "field %s is not accepted on a provided route", f)
			}
		}
		var ic map[string]json.RawMessage
		if r, ok := top["inferenceConfig"]; ok && json.Unmarshal(r, &ic) != nil {
			return 0, refuse(http.StatusBadRequest, CodeRequestRefused, "inferenceConfig is not a JSON object")
		}
		raw = ic["maxTokens"]
	default:
		return 0, refuse(http.StatusNotFound, CodeOperationNotAllowed, "operation %s is not signed on a provided route", op)
	}
	var n json.Number
	// A quoted number is not a number: json.Number would accept "100".
	if len(raw) == 0 || raw[0] < '0' || raw[0] > '9' || json.Unmarshal(raw, &n) != nil {
		return 0, refuse(http.StatusBadRequest, CodeOutputCap,
			"a provided route needs the request's own output cap (max_tokens, or inferenceConfig.maxTokens for Converse)")
	}
	v, err := n.Int64()
	if err != nil || v <= 0 {
		return 0, refuse(http.StatusBadRequest, CodeOutputCap, "the request's output cap is not a positive integer")
	}
	return v, nil
}
