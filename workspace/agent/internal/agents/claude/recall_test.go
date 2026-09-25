package claude

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

// recallUserLine builds a jsonl user line whose content is text, the shape claude 2.1.282 writes
// for a slash command and for its local stdout.
func recallUserLine(text string) string {
	b, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": text}})
	return string(b)
}

func modelCmd(args string) string {
	return recallUserLine("<command-name>/model</command-name>\n            <command-message>model</command-message>\n            <command-args>" + args + "</command-args>")
}

func effortCmd(args string) string {
	return recallUserLine("<command-name>/effort</command-name>\n            <command-message>effort</command-message>\n            <command-args>" + args + "</command-args>")
}

func stdout(s string) string {
	return recallUserLine("<local-command-stdout>" + s + "</local-command-stdout>")
}

const assistantOpus = `{"type":"assistant","effort":"high","message":{"model":"claude-opus-5-5","content":[{"type":"text","text":"ok"}]}}`

func TestRecallFrom(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		want  agents.RecalledSettings
	}{
		{"nothing recorded", []string{recallUserLine("hi"), assistantOpus}, agents.RecalledSettings{}},
		{"typed model is kept verbatim even with no reply after it", []string{
			assistantOpus, modelCmd("claude-sonnet-5"), stdout("Set model to `Sonnet 5` and saved as your default for new sessions"),
		}, agents.RecalledSettings{Model: "claude-sonnet-5"}},
		{"picker records only the display name", []string{
			modelCmd(""), stdout("Set model to `Haiku 4.5` for this session only"),
		}, agents.RecalledSettings{Model: "haiku"}},
		{"picker with an effort", []string{
			modelCmd(""), stdout("Set model to `Sonnet 5` and saved as your default for new sessions with `medium` effort"),
		}, agents.RecalledSettings{Model: "sonnet", Effort: "medium"}},
		{"back to the default model", []string{
			modelCmd("opus"), stdout("Set model to `Opus 5.5` and saved as your default for new sessions"),
			modelCmd("default"), stdout("Set model to `Opus 5.5 (1M context) (default)` and saved as your default for new sessions"),
		}, agents.RecalledSettings{ClearModel: true}},
		{"a failed switch leaves no trace", []string{
			modelCmd("nosuch"), stdout("Model 'nosuch' not found"),
		}, agents.RecalledSettings{}},
		{"an unknown display name is not guessed", []string{
			modelCmd(""), stdout("Set model to `Some Custom Model` for this session only"),
		}, agents.RecalledSettings{}},
		{"typed and picker effort, cancelled one ignored", []string{
			effortCmd("low"), stdout("Set effort level to low (saved as your default for new sessions): Quick"),
			effortCmd(""), stdout("Set effort level to xhigh (this session only): Deeper reasoning than high"),
			effortCmd("high"), stdout("Kept effort level as medium"),
		}, agents.RecalledSettings{Effort: "xhigh"}},
		{"last permission-mode record wins", []string{
			`{"type":"permission-mode","permissionMode":"plan","sessionId":"x"}`,
			`{"type":"permission-mode","permissionMode":"default","sessionId":"x"}`,
		}, agents.RecalledSettings{Mode: "normal"}},
		{"plan entered in the terminal", []string{
			`{"type":"permission-mode","permissionMode":"bypassPermissions","sessionId":"x"}`,
			`{"type":"permission-mode","permissionMode":"plan","sessionId":"x"}`,
		}, agents.RecalledSettings{Mode: "plan"}},
		{"a subagent's command is not the session's", []string{
			strings.Replace(modelCmd("haiku"), `"type":"user"`, `"type":"user","isSidechain":true`, 1),
			strings.Replace(stdout("Set model to `Haiku 4.5` and saved"), `"type":"user"`, `"type":"user","isSidechain":true`, 1),
		}, agents.RecalledSettings{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := recallFrom(strings.NewReader(strings.Join(c.lines, "\n") + "\n"))
			if got != c.want {
				t.Errorf("got %+v, want %+v", got, c.want)
			}
		})
	}
}
