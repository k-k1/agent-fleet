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
	"strings"
	"time"

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

// studioBoundSession reports whether the session is bound to a studio.
func studioBoundSession(name string) bool { return sessionStudio(name) != "" }

// sessionStudio is the studio the session is bound to, "" for none. The meta's Studio is the copy
// this process advertises from, but a Managed create starts the CLI — and this process answers
// its first tools/list — BEFORE the meta is written; only the studio's own `session` is written
// first (decision 2 ③). Measured with codex Managed: that first list lacked the studio tools, and
// codex never lists again, so the session could not reach its studio at all. With no meta yet,
// the studio naming this session back is the answer.
func sessionStudio(name string) string {
	if m, ok := session.ReadMeta(name); ok {
		return m.Studio
	}
	if !session.ValidName(name) {
		return ""
	}
	ents, err := os.ReadDir(paths.ImagegenStudiosDir())
	if err != nil {
		return ""
	}
	for _, e := range ents {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || e.IsDir() {
			continue
		}
		if f, ok := readStudioFile(id); ok && f.Session == name {
			return id
		}
	}
	return ""
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
	self, err := mcpListOwningSession()
	if err != nil {
		if studioBoundInThisFolder() {
			return studioOffer{agentTrial: true}, true
		}
		return studioOffer{}, false
	}
	studio := sessionStudio(self)
	if studio == "" {
		return studioOffer{}, false
	}
	f, _ := readStudioFile(studio)
	return studioOffer{agentTrial: f.AgentTrial}, true
}

func studioBoundInThisFolder() bool {
	cwd, err := os.Getwd()
	if err != nil {
		return false
	}
	for _, m := range session.ListMetas() {
		if !m.Archived && mcpRunsIn(m, cwd) && m.Studio != "" {
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
	bound := sessionStudio(self)
	if bound == "" {
		return mcpToolErr(req.ID, "このセッションは画像スタジオに結ばれていません")
	}
	f, ok := readStudioFile(bound)
	if !ok || f.Session != self {
		return mcpToolErr(req.ID, "このセッションと画像スタジオの結びが変わりました。スタジオのペインから結び直してください")
	}
	if name == "run_image_trial" && !f.AgentTrial {
		return mcpToolErr(req.ID, "このスタジオではエージェントの試走が許可されていません（スタジオの設定で切り替えられます）")
	}
	studio := url.PathEscape(bound)
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
		body, _ = json.Marshal(map[string]any{"author": "agent", "session": self, "draft": draftWithAbsoluteInputs(args)})
	case "run_image_trial":
		return mcpRunImageTrial(req, studio, self)
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

// The agent trial's wait (decision 3). The press answers at once with the job; waiting for the
// picture is this process's, polling the queue, for at most mcpStudioTrialWait. 120 s because a
// warm engine answers in 8-21 s and a cold one takes minutes — past the wait, the job id and "it
// arrives in get_image_studio" are a better answer than holding the agent's turn.
var (
	mcpStudioTrialWait = 120 * time.Second
	mcpStudioTrialPoll = 2 * time.Second
)

func mcpRunImageTrial(req mcpReq, studio, self string) []byte {
	// The heartbeat keeps opencode, which cuts a silent call at 60 s, on the line meanwhile
	// (mcp_imagegen.go).
	stop := startProgressHeartbeat(req, "試走しています…")
	defer stop()
	body, _ := json.Marshal(map[string]any{"mode": "agent_trial", "session": self})
	out, err := agentDo(http.MethodPost, "/imagegen/studios/"+studio+"/press", body)
	if err != nil {
		return mcpToolErr(req.ID, "試走できませんでした: "+agentErrDetail(err))
	}
	var pressed struct {
		Version string `json:"version"`
		Jobs    []struct {
			ID string `json:"id"`
		} `json:"jobs"`
	}
	if json.Unmarshal([]byte(out), &pressed) != nil || len(pressed.Jobs) == 0 {
		return mcpToolErr(req.ID, "試走の結果を読み取れませんでした")
	}
	jobID := pressed.Jobs[0].ID
	deadline := time.Now().Add(mcpStudioTrialWait)
	for {
		if j, ok := studioTrialJob(jobID); ok {
			switch j.State {
			case "done":
				value := map[string]any{
					"version": pressed.Version, "job": jobID, "files": j.Files,
					"warnings": append([]string{}, j.Warnings...), "elapsed_ms": j.ElapsedMS,
					"note": "パスは試走の絵。確かめる必要があるときだけ開くこと。warnings は実際に起きたこと。",
				}
				// The seed this picture came out at — what "make a variation of THIS one" needs.
				if len(j.Files) > 0 && j.Files[0].Seed != nil {
					value["seed"] = *j.Files[0].Seed
				}
				return mcpStructuredResult(req.ID, value)
			case "failed", "cancelled":
				return mcpToolErr(req.ID, "試走 "+pressed.Version+" は失敗しました: "+firstNonEmpty(j.Error, j.State))
			}
		}
		if !time.Now().Before(deadline) {
			return mcpStructuredResult(req.ID, map[string]any{
				"version": pressed.Version, "job": jobID, "state": "running",
				"note": "試走はまだ終わっていません（エンジンが起動中のことがあります）。結果は次の get_image_studio の since に出ます。呼び直さないでください。",
			})
		}
		time.Sleep(min(mcpStudioTrialPoll, time.Until(deadline)))
	}
}

type studioTrialFile struct {
	Path   string `json:"path"`
	Seed   *int64 `json:"seed,omitempty"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
}

type studioTrialJobWire struct {
	ID        string            `json:"id"`
	State     string            `json:"state"`
	Files     []studioTrialFile `json:"files"`
	Warnings  []string          `json:"warnings"`
	ElapsedMS int64             `json:"elapsed_ms"`
	Error     string            `json:"error"`
}

// studioTrialJob finds one job in the queue's list. A failed read is "not yet": the wait is
// bounded, and a transient error must not turn a running trial into a reported failure.
func studioTrialJob(id string) (studioTrialJobWire, bool) {
	out, err := agentGET("/imagegen/jobs")
	if err != nil {
		return studioTrialJobWire{}, false
	}
	var list struct {
		Jobs []studioTrialJobWire `json:"jobs"`
	}
	if json.Unmarshal([]byte(out), &list) != nil {
		return studioTrialJobWire{}, false
	}
	for _, j := range list.Jobs {
		if j.ID == id {
			return j, true
		}
	}
	return studioTrialJobWire{}, false
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// draftWithAbsoluteInputs is set_image_draft's arguments with each reference in `inputs` made
// absolute from this process's working folder — see absFromCWD.
func draftWithAbsoluteInputs(args json.RawMessage) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(nonEmptyArgs(args), &m) != nil {
		return nonEmptyArgs(args)
	}
	var inputs []string
	if raw, ok := m["inputs"]; ok && json.Unmarshal(raw, &inputs) == nil {
		b, _ := json.Marshal(absFromCWDAll(inputs))
		m["inputs"] = b
	}
	out, _ := json.Marshal(m)
	return out
}

// absFromCWD makes an agent's relative path absolute from this process's working folder, which
// is the session's own. The Agent resolves a relative reference against the browse root
// (internal/imagegen/inputs.go) because the Console sends browse-root paths; an agent means its
// cwd, and `docs/ref.png` from a session in a repository would otherwise silently name
// ~/docs/ref.png.
func absFromCWD(p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	cwd, err := os.Getwd()
	if err != nil {
		return p
	}
	return filepath.Join(cwd, p)
}

func absFromCWDAll(ps []string) []string {
	if ps == nil {
		return nil
	}
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = absFromCWD(p)
	}
	return out
}

func nonEmptyArgs(args json.RawMessage) json.RawMessage {
	if len(args) == 0 || string(args) == "null" {
		return json.RawMessage("{}")
	}
	return args
}
