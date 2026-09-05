package main

import (
	"reflect"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// The launch arguments for AWS MCP (Agent Toolkit for AWS — docs/log/25 §AWS MCP).
// Two of them break silently when dropped, and neither looks like breakage:
//   - Without --read-only, a user who believes they merely connected has grown call_aws
//     (some 15,000 AWS API actions) and run_script (arbitrary code).
//   - The endpoint region feeds both the URL and the SigV4 signing region, so getting it
//     wrong only ever looks like "the MCP server won't connect".
func TestAWSMCPArgs(t *testing.T) {
	cases := []struct {
		name string
		conn secrets.AWSConn
		want []string
	}{
		{
			name: "default is read-only on the default endpoint",
			conn: secrets.AWSConn{},
			want: []string{"https://aws-mcp.us-east-1.api.aws/mcp", "--retries", "3", "--read-only"},
		},
		{
			name: "resource region is passed as metadata, not the signing region",
			conn: secrets.AWSConn{
				AWSProfileRef: secrets.AWSProfileRef{Region: "ap-northeast-1"},
				Endpoint:      "eu-central-1",
			},
			want: []string{
				"https://aws-mcp.eu-central-1.api.aws/mcp", "--retries", "3", "--read-only",
				"--metadata", "AWS_REGION=ap-northeast-1",
			},
		},
		{
			name: "write opt-in drops --read-only",
			conn: secrets.AWSConn{Write: true},
			want: []string{"https://aws-mcp.us-east-1.api.aws/mcp", "--retries", "3"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := mcpx.AWSMCPArgs(&c.conn, nil); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("awsMCPArgs = %v, want %v", got, c.want)
			}
		})
	}
}

// The endpoint may only be one of the two regions AWS publishes; anything unknown or garbage
// is rounded to the default. The value goes into both the hostname and the signing region, so
// letting it through yields a connection error whose reason appears nowhere.
func TestAWSMCPEndpointNormalizes(t *testing.T) {
	for in, want := range map[string]string{
		"":               "us-east-1",
		"us-east-1":      "us-east-1",
		"eu-central-1":   "eu-central-1",
		"ap-northeast-1": "us-east-1", // not a published region
		"'; rm -rf /":    "us-east-1",
	} {
		if got := awsMCPEndpoint(in); got != want {
			t.Errorf("awsMCPEndpoint(%q) = %q, want %q", in, got, want)
		}
	}
}
