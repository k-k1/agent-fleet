// session_wire_test.go — sessionWire 中継の drop 回帰テスト。
// sessionWire は Agent の /sessions 応答を decode→再 emit する中継 struct なので、
// Agent 側 wire（workspace/agent/internal/session.Session）に在って ここに無い
// field は silently drop される（Title → driver → color/context/exit 系と同じ
// 事故が 3 回起きている）。Agent 相当の JSON を丸ごと往復させ、Console が消費する
// field が生き残ることを固定する。
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// stubRuntime は Endpoint/Token だけ本物を返す最小 Runtime。
type stubRuntime struct {
	endpoint string
	token    string
	state    string // "" = running（既存の呼び出しはこの既定に依存している）
}

func (s stubRuntime) Start(context.Context) error { return nil }
func (s stubRuntime) Stop(context.Context) error  { return nil }
func (s stubRuntime) State(context.Context) string {
	if s.state != "" {
		return s.state
	}
	return "running"
}
func (s stubRuntime) Endpoint() string { return s.endpoint }
func (s stubRuntime) Token() string    { return s.token }
func (s stubRuntime) Name() string     { return "stub" }

// Agent の /sessions 1 行ぶんの実形状（workspace/agent の wireSession が出す
// JSON と同じ key）。exit 系は docs/log/26（OOM で死んだ停止セッション）の実例値。
const agentSessionsPayload = `{"sessions":[{
	"name":"s1","tmux":"claude_s1","dir":"/home/dev/repos/x","workingCopyId":"wc_123","kind":"claude",
	"driver":"managed","repo":"x","title":"t","display":"[AF] t","color":"#332211",
	"label":"[AF] t","started":"07/15 12:00","createdAt":"2026-07-15T12:00:00+09:00",
	"remoteUrl":"","state":"","alive":false,"resumable":true,"locked":true,"backgroundBusy":false,
	"context":{"read":1000,"create":200,"fresh":30,"model":"claude-fable-5"},
	"branch":"main","currentBranch":"dev","branchDrift":true,"worktree":true,
	"exitReason":"oom","exitCode":137,"exitSignal":9
}]}`

// TestAgentSessionsRelayKeepsFields: CP の decode→再 emit 往復で、Console が
// 消費する field（特に docs/log/26 の exit chip を出す exitReason/exitCode/exitSignal、
// SSM 背景色の color、ContextBar の context）が drop されないこと。
func TestAgentSessionsRelayKeepsFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sessions" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization = %q, want Bearer tok", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(agentSessionsPayload))
	}))
	defer srv.Close()

	list, err := (&manager{}).agentSessions(context.Background(), stubRuntime{endpoint: srv.URL, token: "tok"})
	if err != nil {
		t.Fatalf("agentSessions: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("len = %d, want 1", len(list))
	}

	// 再 emit（sessionsList が writeJSON する形）を JSON に戻して field 単位で確認。
	out, err := json.Marshal(list[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	want := map[string]any{
		// docs/log/26 exit chip（OOM/クラッシュ表示）— CP 中継で drop されていた本丸。
		"exitReason": "oom",
		"exitCode":   float64(137),
		"exitSignal": float64(9),
		// 同時に drop されていた表示系。
		"color": "#332211",
		// P1.5 で追加済みの driver（回帰防止に固定）。
		"driver":        "managed",
		"title":         "t",
		"workingCopyId": "wc_123",
		// branch/worktree 系（既存だが同型事故の回帰防止に固定）。
		"branch":        "main",
		"currentBranch": "dev",
		"branchDrift":   true,
		"worktree":      true,
		// 削除ロック（docs/log/45）— CP 中継で落とすと鍵バッジと解除メニューが消える。
		"locked": true,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("relayed %s = %v, want %v", k, got[k], v)
		}
	}
	// context は shape を CP が解釈しない素通し（RawMessage）— 中身ごと残ること。
	ctxObj, ok := got["context"].(map[string]any)
	if !ok {
		t.Fatalf("relayed context = %v, want object", got["context"])
	}
	if ctxObj["read"] != float64(1000) || ctxObj["model"] != "claude-fable-5" {
		t.Errorf("relayed context = %v, want read=1000 model=claude-fable-5", ctxObj)
	}
	// tmux は意図して中継しない（Console 未使用・"claude_"+name で導出可能）。
	// うっかり増えたら struct コメントの棚卸しと合わせて見直すこと。
	if _, exists := got["tmux"]; exists {
		t.Errorf("tmux should not be relayed, got %v", got["tmux"])
	}
}
