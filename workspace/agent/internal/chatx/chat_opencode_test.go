package chatx

import (
	"encoding/json"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/assistants"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readJSONFile は生成された opencode 設定を map で読む（テスト補助）。
func readJSONFile(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return m
}

// mcpCommand は設定中の mcp.af.command を []string で返す（無ければ nil）。
func mcpCommand(t *testing.T, cfg map[string]any) []string {
	t.Helper()
	mcp, ok := cfg["mcp"].(map[string]any)
	if !ok {
		return nil
	}
	af, ok := mcp["af"].(map[string]any)
	if !ok {
		t.Fatalf("mcp に af が無い: %+v", mcp)
	}
	raw, _ := af["command"].([]any)
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, _ := v.(string)
		out = append(out, s)
	}
	return out
}

// docs/log/30 の要: af_write の会話が起こしたセッションは、その会話へ完了報告を返す。
// 報告リンクは mcp-stdio の --conv でしか張れないので、opencode チャットの設定が
// --conv を落とすと報告は永久に届かない（実際に落ちていた）。
func TestOpencodeChatConfigCarriesConv(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const id = "ce4f94b9-2854-44ee-8425-61859128d669"
	c := &chatConversation{ID: id, Tools: assistants.ToolsAFWrite}

	path := opencodeChatConfig(c)
	if path == "" {
		t.Fatal("af_write の会話で設定が生成されない")
	}
	if got := filepath.Base(path); got != id+".json" {
		t.Fatalf("設定ファイル名 = %q, want %q", got, id+".json")
	}
	cfg := readJSONFile(t, path)
	cmd := mcpCommand(t, cfg)
	if len(cmd) == 0 {
		t.Fatalf("af MCP サーバーが設定されていない: %+v", cfg)
	}
	joined := strings.Join(cmd, " ")
	if !strings.Contains(joined, "--write") || !strings.Contains(joined, "--conv "+id) {
		t.Fatalf("mcp command = %q, want --write と --conv %s", joined, id)
	}
	// チャット契約（編集・シェル拒否）は会話別設定にも乗せる。opencode が
	// OPENCODE_CONFIG をプロジェクト設定と併合するか置換するかは未文書のため、
	// どちらでも姿勢が保たれるようにしてある。
	perm, _ := cfg["permission"].(map[string]any)
	if perm["edit"] != "deny" || perm["bash"] != "deny" {
		t.Fatalf("permission = %+v, want edit/bash deny", perm)
	}
}

// 読み取り grant には --conv を渡さない（report_to は書き込み側の配線）。
func TestOpencodeChatConfigReadGrantHasNoConv(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	c := &chatConversation{ID: "ce4f94b9-2854-44ee-8425-61859128d669", Tools: assistants.ToolsAFRead}
	path := opencodeChatConfig(c)
	if path == "" {
		t.Fatal("af_read の会話で設定が生成されない")
	}
	joined := strings.Join(mcpCommand(t, readJSONFile(t, path)), " ")
	if strings.Contains(joined, "--conv") || strings.Contains(joined, "--write") {
		t.Fatalf("mcp command = %q, want 読み取り専用", joined)
	}
}

// ツール無しの会話には設定を書かない（余計なファイルを残さない）。
func TestOpencodeChatConfigSkippedWithoutGrant(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	c := &chatConversation{ID: "ce4f94b9-2854-44ee-8425-61859128d669", Tools: assistants.ToolsNone}
	if path := opencodeChatConfig(c); path != "" {
		t.Fatalf("ツール無しで設定が生成された: %q", path)
	}
}

// プロジェクト側（--dir）の設定には af MCP を書かない。opencode は設定を**併合**し、
// 衝突時は**プロジェクト設定が勝つ**（1.18.7 実測・TestContractOpencodeConfigPrecedence
// が固定）ので、ここに af を書くと会話別設定の --conv 付き定義を上書きしてしまい、
// セッション報告（docs/log/30）が恒久的に届かなくなる。レジストリサーバーは両方の設定が
// 同じ会話から作るので食い違わず、ここに載っていて構わない。
func TestOpencodeChatDirHasNoMCP(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	c := &chatConversation{ID: "ce4f94b9-2854-44ee-8425-61859128d669", Tools: assistants.ToolsAFWrite}
	dir := opencodeChatDir(c)
	if filepath.Base(dir) != "opencode-write" {
		t.Fatalf("dir = %q, want …/opencode-write（grant 別の据え置き）", dir)
	}
	cfg := readJSONFile(t, filepath.Join(dir, "opencode.json"))
	if _, ok := cfg["mcp"]; ok {
		t.Fatalf("プロジェクト設定に mcp が残っている: %+v", cfg)
	}
	perm, _ := cfg["permission"].(map[string]any)
	if perm["edit"] != "deny" || perm["bash"] != "deny" {
		t.Fatalf("permission = %+v, want edit/bash deny", perm)
	}
}

// 会話を消したら会話別設定も消える（handleChatDelete）。
func TestChatDeleteRemovesOpencodeConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const id = "ce4f94b9-2854-44ee-8425-61859128d669"
	path := opencodeChatConfig(&chatConversation{ID: id, Tools: assistants.ToolsAFWrite})
	if path == "" {
		t.Fatal("設定が生成されない")
	}
	if err := os.Remove(filepath.Join(homeDir(), ".config", "agent-fleet", "chat-wd",
		"opencode-conv", id+".json")); err != nil {
		t.Fatalf("削除経路が指すパスに設定が無い: %v", err)
	}
}

// 失敗ターンの理由を拾う。opencode は失敗を stdout の error イベントで出し、
// stderr は空のまま非ゼロ終了する（実測 1.18.5）ので、これを読まないと利用者には
// 「exit status 1」しか出ない。
func TestParseOpencodeRunEventsError(t *testing.T) {
	out := strings.Join([]string{
		`{"type":"step_start","sessionID":"ses_1","part":{"id":"p1","type":"step-start"}}`,
		`{"type":"error","timestamp":1785119549237,"sessionID":"ses_1","error":{"name":"UnknownError","data":{"message":"Unexpected server error. Check server logs for details.","ref":"err_26a07104"}}}`,
	}, "\n")
	reply, sesID, _, turnErr, _ := parseOpencodeRunEvents([]byte(out))
	if reply != "" {
		t.Fatalf("reply = %q, want 空", reply)
	}
	if sesID != "ses_1" {
		t.Fatalf("sesID = %q", sesID)
	}
	if !strings.Contains(turnErr, "Unexpected server error") || !strings.Contains(turnErr, "err_26a07104") {
		t.Fatalf("turnErr = %q, want メッセージと ref", turnErr)
	}
}

// message が無いエラーは name で代替する（形が変わっても空文字にしない）。
func TestParseOpencodeRunEventsErrorNameOnly(t *testing.T) {
	out := `{"type":"error","sessionID":"ses_1","error":{"name":"ProviderAuthError"}}`
	_, _, _, turnErr, _ := parseOpencodeRunEvents([]byte(out))
	if turnErr != "ProviderAuthError" {
		t.Fatalf("turnErr = %q, want ProviderAuthError", turnErr)
	}
}

// 正常ターンは turnErr を立てない（回帰よけ）。
func TestParseOpencodeRunEventsOKHasNoError(t *testing.T) {
	out := `{"type":"text","sessionID":"ses_1","part":{"id":"p1","type":"text","text":"OK"}}`
	reply, _, _, turnErr, _ := parseOpencodeRunEvents([]byte(out))
	if reply != "OK" || turnErr != "" {
		t.Fatalf("reply=%q turnErr=%q", reply, turnErr)
	}
}
