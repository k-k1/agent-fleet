package mcpx

// The image studio's side of the session MCP server (ADR 0100 decision 3): which sessions are
// offered the four studio tools, and the checks every call makes before it reaches the Agent.
//
// Both decisions read files, not the Agent: the session's meta for the binding, the studio's own
// file for its bound session and "let the agent run a trial". A slow Agent must not make the
// tools blink in and out of tools/list, which is what asking it on every list would do.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// mcpStudioNoGenerateImage is generate_image's refusal in a studio session.
const mcpStudioNoGenerateImage = "このセッションは画像スタジオに結ばれています。生成は利用者がスタジオのボタンで押します。" +
	"下書きは set_image_draft で直し、1 枚だけ試すなら run_image_trial を使ってください（許可されている場合）"

// studioOffer is what mcpStdioStudioTools is built from.
type studioOffer struct {
	agentTrial bool
}

// studioFile is the part of a studio's stored file this process reads. The Agent owns the file
// (internal/imagegen ImageStudio); these two keys are the ones the session side decides by.
type studioFile struct {
	Session    string `json:"session"`
	AgentTrial bool   `json:"agent_trial"`
}

func readStudioFile(id string) (studioFile, bool) {
	var f studioFile
	if !paths.ValidIDSegment(id) {
		return f, false
	}
	b, err := os.ReadFile(filepath.Join(paths.ImagegenStudiosDir(), id+".json"))
	if err != nil || json.Unmarshal(b, &f) != nil {
		return f, false
	}
	return f, true
}

// studioBoundSession reports whether the session's meta names a studio.
func studioBoundSession(name string) bool {
	m, ok := session.ReadMeta(name)
	return ok && m.Studio != ""
}

// mcpStudioAdvertise decides whether this session is offered the studio tools, in the three
// states decision 3 names:
//   - identified and bound to a studio: offered, run_image_trial only when the studio allows it;
//   - identified and not bound: not offered;
//   - not identifiable (the cwd guess is ambiguous): offered, and a call is refused with the
//     reason — a tool that simply is not there leaves the agent saying "no such tool".
//
// The third case is narrowed to a folder where some session IS bound to a studio: anywhere else
// every call would be refused, and four tool descriptions on every turn buy nothing.
func mcpStudioAdvertise() (studioOffer, bool) {
	if !selfReportOnly() {
		return studioOffer{}, false
	}
	self, err := mcpOwningSession()
	if err != nil {
		if studioBoundInThisFolder() {
			return studioOffer{agentTrial: true}, true
		}
		return studioOffer{}, false
	}
	m, ok := session.ReadMeta(self)
	if !ok || m.Studio == "" {
		return studioOffer{}, false
	}
	f, _ := readStudioFile(m.Studio)
	return studioOffer{agentTrial: f.AgentTrial}, true
}

func studioBoundInThisFolder() bool {
	cwd, err := os.Getwd()
	if err != nil {
		return false
	}
	for _, m := range session.ListMetas() {
		if !m.Archived && m.Dir == cwd && m.Studio != "" {
			return true
		}
	}
	return false
}

// mcpStudioCall runs the checks every studio tool shares, then relays to the Agent. The binding
// is checked on BOTH sides (decision 2): the meta says which studio this session claims, and
// the studio's own `session` has to name this session back — the two are written separately,
// and the studio is the truth.
func mcpStudioCall(req mcpReq, name string, args json.RawMessage) []byte {
	self, err := mcpOwningSession()
	if err != nil {
		return mcpToolErr(req.ID, "このセッションがどの画像スタジオに結ばれているか特定できません: "+err.Error())
	}
	m, ok := session.ReadMeta(self)
	if !ok || m.Studio == "" {
		return mcpToolErr(req.ID, "このセッションは画像スタジオに結ばれていません")
	}
	f, ok := readStudioFile(m.Studio)
	if !ok || f.Session != self {
		return mcpToolErr(req.ID, "このセッションと画像スタジオの結びが変わりました。スタジオのペインから結び直してください")
	}
	if name == "run_image_trial" && !f.AgentTrial {
		return mcpToolErr(req.ID, "このスタジオではエージェントの試走が許可されていません（スタジオの設定で切り替えられます）")
	}
	studio := url.PathEscape(m.Studio)
	var (
		method = http.MethodGet
		path   string
		body   []byte
	)
	switch name {
	case "get_image_studio":
		path = "/imagegen/studios/" + studio + "?view=agent&session=" + url.QueryEscape(self)
	case "set_image_draft":
		method, path = http.MethodPut, "/imagegen/studios/"+studio
		body, _ = json.Marshal(map[string]any{"author": "agent", "session": self, "draft": json.RawMessage(nonEmptyArgs(args))})
	case "run_image_trial":
		method, path = http.MethodPost, "/imagegen/studios/"+studio+"/press"
		body, _ = json.Marshal(map[string]any{"mode": "agent_trial", "session": self})
	case "add_image_knowledge":
		var in map[string]any
		_ = json.Unmarshal(nonEmptyArgs(args), &in)
		if in == nil {
			in = map[string]any{}
		}
		in["session"] = self
		method, path = http.MethodPost, "/imagegen/knowledge"
		body, _ = json.Marshal(in)
	}
	out, err := agentDo(method, path, body)
	if err != nil {
		return mcpToolErr(req.ID, "画像スタジオへの問い合わせに失敗しました: "+agentErrDetail(err))
	}
	return mcpResult(req.ID, map[string]any{
		"content": []any{map[string]any{"type": "text", "text": out}},
	})
}

func nonEmptyArgs(args json.RawMessage) json.RawMessage {
	if len(args) == 0 || string(args) == "null" {
		return json.RawMessage("{}")
	}
	return args
}
