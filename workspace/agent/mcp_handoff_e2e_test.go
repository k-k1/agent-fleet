package main

// 引き継ぎ提案の E2E。**実バイナリの `workspace-agent mcp-stdio` を子プロセスとして
// 起動し**、実 MCP プロトコル（initialize → tools/call）で叩いて、実 Agent ルートが
// 実ファイルを正しいセッションの下へ書くところまでを通しで見る。
//
// なぜユニットテストで足りないか: この機能が壊れた実障害（2026-08-09）は
// mcpOwningSession 単体ではなく「MCP 子プロセスが AF_SESSION_NAME を渡されない」という
// **プロセス境界**で起きた。呼び出し元を関数呼び出しで模したテストは、その境界を跨がない
// ので同じ壊れ方を再現できない。ここは env と cwd を本番と同じ形で子へ渡す。
//
// 3 つの形をそのまま並べている:
//   - managed codex: thread 単位 config で AF_SESSION_NAME が届く（mcpreg.CodexThreadServers）
//   - managed opencode / 縮退した codex: 届かないので cwd＋生存で絞る
//   - 絞れないとき: 取り違えるくらいなら拒否する
//
// 認証・Docker 不要。このコンテナに Docker は無いので e2e/ モジュール（L2、実コンテナ）
// ではなくここに置いた。
//
// 制約: セッションの生存判定（sessionAlive）は tmux / managed daemon を見るので、テストが
// 正直に作れない。/status だけはスタブで、生存は**入力**として与える。検証対象である
// handoff-proposal 側は本番ハンドラをそのまま載せている。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func TestE2EHandoffProposalLandsUnderTheCallingSession(t *testing.T) {
	// Build BEFORE any subtest rewrites HOME: a `go build` under a temp HOME gets a
	// fresh module cache and re-downloads the world (45s per case, measured), then
	// leaves read-only files t.TempDir cannot clean up.
	bin := buildAgentBinary(t)

	for _, tc := range []struct {
		name string
		// sessionName is what the thread config would put in AF_SESSION_NAME. Empty =
		// the managed shapes that cannot carry it.
		sessionName string
		alive       map[string]bool
		wantOwner   string
		wantRefusal string
	}{
		{
			name:        "managed codex: the thread config names the session",
			sessionName: "slotcodex",
			// Both alive: the env must settle it without any liveness probing at all.
			alive:     map[string]bool{"slotcodex": true, "slotother": true},
			wantOwner: "slotcodex",
		},
		{
			name:      "no env: the live session in the shared worktree wins",
			alive:     map[string]bool{"slotcodex": true, "slotother": false},
			wantOwner: "slotcodex",
		},
		{
			name:        "no env, two live sessions: refuse rather than misfile",
			alive:       map[string]bool{"slotcodex": true, "slotother": true},
			wantRefusal: "複数のセッション",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newHandoffE2E(t, bin, tc.alive)
			resp := env.call(t, tc.sessionName, "次のセッションへ", "続き")

			if tc.wantRefusal != "" {
				if !resp.isError || !strings.Contains(resp.text, tc.wantRefusal) {
					t.Fatalf("tools/call = %+v, want a refusal mentioning %q", resp, tc.wantRefusal)
				}
				for name := range tc.alive {
					if p, _ := readHandoffProposal(name); p != nil {
						t.Fatalf("a proposal was filed under %q despite the refusal: %+v", name, p)
					}
				}
				return
			}
			if resp.isError {
				t.Fatalf("tools/call failed: %s", resp.text)
			}
			p, err := readHandoffProposal(tc.wantOwner)
			if err != nil || p == nil {
				t.Fatalf("no proposal under %q (err=%v) — the card would never appear in that "+
					"session's mirror", tc.wantOwner, err)
			}
			if p.Prompt != "続き" || p.Title != "次のセッションへ" {
				t.Fatalf("proposal = %+v, want the prompt/title the tool was called with", p)
			}
			for name := range tc.alive {
				if name == tc.wantOwner {
					continue
				}
				if other, _ := readHandoffProposal(name); other != nil {
					t.Fatalf("the proposal ALSO landed under %q — a handoff was filed in "+
						"somebody else's session", name)
				}
			}
		})
	}
}

// --- harness ------------------------------------------------------------------

type handoffE2E struct {
	bin  string
	cwd  string
	addr string
}

type mcpCallResult struct {
	isError bool
	text    string
}

// newHandoffE2E builds the real binary, mounts the real handoff routes (plus a stubbed
// /status), and lays down two session metas that SHARE one working folder — the shape
// that broke the real handoff.
func newHandoffE2E(t *testing.T, bin string, alive map[string]bool) *handoffE2E {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home) // handoffProposalPath resolves under this
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))

	cwd := t.TempDir()
	for name := range alive {
		session.WriteMeta(session.Meta{Name: name, Dir: cwd, Kind: session.KindCodex, Driver: session.DriverManaged})
	}

	mux := http.NewServeMux()
	// The route under test: production's own handler, writing production's own file.
	mux.HandleFunc("POST /sessions/{name}/handoff-proposal", handleSessionHandoffProposal)
	mux.HandleFunc("GET /sessions/{name}/handoff-proposal", handleSessionHandoffProposal)
	// Stub: real aliveness needs tmux or a managed daemon (see the file comment).
	mux.HandleFunc("GET /sessions/{name}/status", func(w http.ResponseWriter, r *http.Request) {
		a := alive[r.PathValue("name")]
		_, _ = fmt.Fprintf(w, `{"alive":%t,"ready":%t}`, a, a)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	return &handoffE2E{bin: bin, cwd: cwd, addr: u.Host}
}

// buildAgentBinary compiles the binary under test with the caller's real environment.
func buildAgentBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "workspace-agent")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Env = os.Environ()
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build workspace-agent: %v\n%s", err, out)
	}
	return bin
}

// call runs one propose_session_handoff through a real mcp-stdio child, launched the
// way a session's MCP server is launched: --self-report, in the session's working
// folder, with only the environment the runtime would have given it.
func (e *handoffE2E) call(t *testing.T, sessionName, title, prompt string) mcpCallResult {
	t.Helper()
	args, _ := json.Marshal(map[string]any{"title": title, "prompt": prompt})
	params, _ := json.Marshal(map[string]any{"name": "propose_session_handoff", "arguments": json.RawMessage(args)})
	req, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": json.RawMessage(params)})

	cmd := exec.Command(e.bin, "mcp-stdio", "--self-report")
	cmd.Dir = e.cwd
	cmd.Env = []string{
		"HOME=" + os.Getenv("HOME"),
		"PATH=" + os.Getenv("PATH"),
		"AF_SESSIONS_DIR=" + os.Getenv("AF_SESSIONS_DIR"),
		"AGENT_ADDR=" + e.addr,
	}
	if sessionName != "" {
		cmd.Env = append(cmd.Env, "AF_SESSION_NAME="+sessionName)
	}
	cmd.Stdin = strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"e2e","version":"0"}}}` +
			"\n" + string(req) + "\n")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("mcp-stdio: %v\n%s", err, out)
	}

	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		var resp struct {
			ID     int `json:"id"`
			Result struct {
				IsError bool `json:"isError"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"result"`
		}
		if json.Unmarshal([]byte(line), &resp) != nil || resp.ID != 2 {
			continue
		}
		var text string
		if len(resp.Result.Content) > 0 {
			text = resp.Result.Content[0].Text
		}
		return mcpCallResult{isError: resp.Result.IsError, text: text}
	}
	t.Fatalf("no tools/call response from mcp-stdio:\n%s", out)
	return mcpCallResult{}
}
