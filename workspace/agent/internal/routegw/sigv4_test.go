package routegw

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/awsx"
)

var sigTime = time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)

// The signature AWS publishes for its SigV4 test suite case "get-vanilla" (service "service",
// us-east-1, 2015-08-30T12:36:00Z, the documented example key).
func TestSignV4AWSTestSuiteVector(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://example.amazonaws.com/", nil)
	signV4(req, nil, awsx.RouteCredentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"},
		"service", "us-east-1", sigTime)
	want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=host;x-amz-date, " +
		"Signature=5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"
	if got := req.Header.Get("Authorization"); got != want {
		t.Fatalf("Authorization =\n %s\nwant\n %s", got, want)
	}
}

// A Bedrock request as the gateway sends it: a model id whose ":" is percent-encoded on the wire
// (so signed double-encoded), a body, a session token and a streaming Accept. The expected value
// was computed once with aws-sdk-go-v2's v4 signer (v1.47.1) on the same inputs; the SDK is not a
// dependency of this module.
func TestSignV4BedrockStreamingRequest(t *testing.T) {
	body := []byte(`{"anthropic_version":"bedrock-2023-05-31","max_tokens":10}`)
	req, _ := http.NewRequest("POST", "https://bedrock-runtime.us-east-1.amazonaws.com/model/anthropic.claude-3-5-sonnet-20241022-v2%3A0/invoke-with-response-stream", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/vnd.amazon.eventstream")
	signV4(req, body, awsx.RouteCredentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY",
		SessionToken: "TOKEN123"}, "bedrock", "us-east-1", sigTime)
	want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/bedrock/aws4_request, " +
		"SignedHeaders=accept;content-type;host;x-amz-date;x-amz-security-token, " +
		"Signature=75be67788f5122943b69888a62834d258c6a5954a7b6da4eb338dc749cdf261c"
	if got := req.Header.Get("Authorization"); got != want {
		t.Fatalf("Authorization =\n %s\nwant\n %s", got, want)
	}
	if strings.Contains(req.Header.Get("Authorization"), "wJalr") {
		t.Fatal("the secret key is in the Authorization header")
	}
}
