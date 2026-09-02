// mcp_agent_text.go — CP→Agent の素の HTTP ヘルパ。MCP 家系を internal/mcpsrv へ移した
// ときに **package main へ残した** 2 つ（並列リファクタ ウェーブ C / track=CP-MCP）。
//
// 🔥 残す理由は所有権ではなく、型の可視性そのもの:
// agentHTTPError の status / body は**非公開フィールド**で、memo.go が
// `errors.As(err, &he)` で拾ったあと he.status / he.body を読んでいる。型を別 package
// へ動かすと、この errors.As は**コンパイルは通るのに恒久的に false** になり、
// /turn を持たない旧 Agent への退避（agentEndpointMissing）と Agent の安定エラーコード
// 転送（memoAgentError）が無言で死ぬ。agentText はその型を返す唯一の生成元なので、
// 一緒に残る。mcpsrv は CP.AgentText 経由で呼ぶ。
//
// 利用者は memo.go / session_share.go / claude_audit.go / mcpsrv。MCP 専用ではないので、
// エイリアス回収のパスで agent_client.go の隣へ引き取るのが本来の置き場。ファイル名の
// mcp_ 接頭辞は、この並列ウェーブでの所有 glob（control-plane/mcp_*.go）に合わせたもの。

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
)

// agentText performs an authenticated CP→Agent request and returns the body as
// text (the Agent already returns JSON; we pass it through to the model).
func agentText(ctx context.Context, rt runtime.Runtime, method, path string, body []byte) (string, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rt.Endpoint()+path, r)
	if err != nil {
		return "", err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if rt.Token() != "" {
		req.Header.Set("Authorization", "Bearer "+rt.Token())
	}
	resp, err := agentHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("workspace agent unreachable (is the workspace running?)")
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return "", &agentHTTPError{status: resp.StatusCode, body: strings.TrimSpace(string(b))}
	}
	return string(b), nil
}

// agentHTTPError keeps the Agent's status/body inspectable by callers that need
// protocol-level fallback or want to preserve a stable Agent error code. Error()
// intentionally retains agentText's former text so existing MCP responses do not
// change merely because the error became typed.
type agentHTTPError struct {
	status int
	body   string
}

func (e *agentHTTPError) Error() string {
	return fmt.Sprintf("agent %d: %s", e.status, e.body)
}
