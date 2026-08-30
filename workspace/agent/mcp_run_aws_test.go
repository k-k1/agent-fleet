package main

import (
	"reflect"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// AWS MCP（Agent Toolkit for AWS — docs/log/25 §AWS MCP）の起動引数。
// 落とすと静かに壊れるのは 2 つで、どちらも「壊れた」ようには見えない:
//   - --read-only が抜けると、接続しただけのつもりの利用者に call_aws（AWS API 約
//     15,000 アクション）と run_script（任意コード）が生えている。
//   - エンドポイントのリージョンは URL と SigV4 の署名リージョンの両方に効くので、
//     取り違えても「MCP サーバーが繋がらない」としか見えない。
func TestAWSMCPArgs(t *testing.T) {
	cases := []struct {
		name string
		conn secrets.AWSConn
		want []string
	}{
		{
			name: "既定は読み取り専用・既定エンドポイント",
			conn: secrets.AWSConn{},
			want: []string{"https://aws-mcp.us-east-1.api.aws/mcp", "--retries", "3", "--read-only"},
		},
		{
			name: "リソースリージョンは metadata で渡す（署名リージョンではない）",
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
			name: "write opt-in で --read-only が外れる",
			conn: secrets.AWSConn{Write: true},
			want: []string{"https://aws-mcp.us-east-1.api.aws/mcp", "--retries", "3"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := awsMCPArgs(&c.conn, nil); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("awsMCPArgs = %v, want %v", got, c.want)
			}
		})
	}
}

// エンドポイントは AWS が公開している 2 リージョンだけ。未知の値やゴミは既定へ丸める
// — ホスト名にも署名リージョンにも入る値なので、そのまま通すと接続エラーになるだけで
// 理由がどこにも出ない。
func TestAWSMCPEndpointNormalizes(t *testing.T) {
	for in, want := range map[string]string{
		"":               "us-east-1",
		"us-east-1":      "us-east-1",
		"eu-central-1":   "eu-central-1",
		"ap-northeast-1": "us-east-1", // 公開されていないリージョン
		"'; rm -rf /":    "us-east-1",
	} {
		if got := awsMCPEndpoint(in); got != want {
			t.Errorf("awsMCPEndpoint(%q) = %q, want %q", in, got, want)
		}
	}
}
