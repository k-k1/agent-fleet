package mcpx

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func skillRestoreCall(t *testing.T, args map[string]any) (string, bool) {
	t.Helper()
	raw, _ := json.Marshal(args)
	params, _ := json.Marshal(map[string]any{"name": mcpToolSkillRestore, "arguments": json.RawMessage(raw)})
	resp := mcpStdioCall(mcpReq{ID: json.RawMessage(`1`), Params: params})
	var parsed struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &parsed); err != nil || len(parsed.Result.Content) == 0 {
		t.Fatalf("resp = %s err = %v", resp, err)
	}
	return parsed.Result.Content[0].Text, parsed.Result.IsError
}

// Advertised on the session surface, and the two modes reach their routes.
func TestSkillRestoreListsAndRestores(t *testing.T) {
	old := selfReportOnly()
	t.Cleanup(func() { setSelfReportOnly(old) })
	setSelfReportOnly(true)
	found := false
	for _, tool := range mcpStdioToolList() {
		found = found || tool["name"] == mcpToolSkillRestore
	}
	if !found {
		t.Fatal("skill_restore is not advertised to a session")
	}

	var method, path, body string
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		method, path, body = r.Method, r.URL.Path, string(b)
		w.WriteHeader(status)
		if status == http.StatusConflict {
			_, _ = w.Write([]byte(`{"error":{"code":"skill_exists","message":"x"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"skills":[]}`))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	t.Setenv("AGENT_ADDR", u.Host)

	if _, isErr := skillRestoreCall(t, map[string]any{}); isErr || method != "GET" || path != "/agents/skills/archive" {
		t.Fatalf("list: %s %s isErr=%v", method, path, isErr)
	}
	if _, isErr := skillRestoreCall(t, map[string]any{"archived": "afa-x"}); !isErr {
		t.Fatal("archived without root must be refused")
	}
	if _, isErr := skillRestoreCall(t, map[string]any{"archived": " afa-x ", "root": "~/.claude/skills"}); isErr ||
		method != "POST" || path != "/agents/skills/archive/restore" ||
		!strings.Contains(body, `"archived":"afa-x"`) || !strings.Contains(body, `"root":"~/.claude/skills"`) {
		t.Fatalf("restore: %s %s %s isErr=%v", method, path, body, isErr)
	}
	status = http.StatusConflict
	text, isErr := skillRestoreCall(t, map[string]any{"archived": "afa-x", "root": "~/.claude/skills"})
	if !isErr || !strings.Contains(text, "already exists") {
		t.Fatalf("name clash: %q isErr=%v", text, isErr)
	}
}
