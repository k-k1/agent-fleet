package mcpx

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/cpurl"
)

// cpMemoDo calls the CP's /internal/memos bridge (cpurl.Request: the public hairpin, or the
// CP's workspace listener where AF_CP_INTERNAL_URL is set) authenticated by the
// per-membership AF_MEMO_TOKEN — the queue lives in the CP store,
// not the local Agent. Both env vars are injected by the CP only when PUBLIC_BASE_URL is
// set; absent them the memo feature is unavailable and we say so in-band.
func cpMemoDo(method, path string, body []byte) (string, error) {
	base := cpurl.Request()
	if base == "" || os.Getenv("AF_MEMO_TOKEN") == "" {
		return "", fmt.Errorf("メモ機能はこの環境では利用できません（CP の公開URL/トークンが未設定）")
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, base+path, rdr)
	if err != nil {
		return "", err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+os.Getenv("AF_MEMO_TOKEN"))
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("CP メモAPI エラー (%d): %s", resp.StatusCode, string(b))
	}
	return string(b), nil
}

// CPScheduleDo calls the CP's /internal/schedules bridge (cpurl.Request, like cpMemoDo)
// authenticated by the per-membership AF_SCHEDULE_TOKEN — schedules
// live in the CP store (docs/log/38), not the local Agent. Mirrors cpMemoDo; both env vars
// are injected by the CP only when PUBLIC_BASE_URL is set.
func CPScheduleDo(method, path string, body []byte) (string, error) {
	base := cpurl.Request()
	if base == "" || os.Getenv("AF_SCHEDULE_TOKEN") == "" {
		return "", fmt.Errorf("定時実行機能はこの環境では利用できません（CP の公開URL/トークンが未設定）")
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, base+path, rdr)
	if err != nil {
		return "", err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+os.Getenv("AF_SCHEDULE_TOKEN"))
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return "", &CPScheduleError{Status: resp.StatusCode, Body: string(b)}
	}
	return string(b), nil
}

// CPScheduleError is a CP schedule API answer of 4xx/5xx. It is typed so a caller can tell
// "the schedule is gone" (404) from a failure that may still have left it in place.
type CPScheduleError struct {
	Status int
	Body   string
}

func (e *CPScheduleError) Error() string {
	return fmt.Sprintf("CP 定時実行API エラー (%d): %s", e.Status, e.Body)
}

// WithOwnerConv stamps owner_conv onto a create_schedule body so the schedule's
// completion reports (docs/log/30) land in the operator's own conversation. A client-
// supplied owner_conv is overridden — the operator only ever reports to itself. On a
// parse failure the original body is returned unchanged (the CP then validates it).
func WithOwnerConv(args json.RawMessage, conv string) []byte {
	var m map[string]any
	if err := json.Unmarshal(args, &m); err != nil || m == nil {
		return []byte(args)
	}
	m["owner_conv"] = conv
	b, err := json.Marshal(m)
	if err != nil {
		return []byte(args)
	}
	return b
}

// agentGET calls the local Agent REST with the shared AGENT_TOKEN.
func agentGET(path string) (string, error) { return agentDo(http.MethodGet, path, nil) }

// AgentPOST calls the local Agent REST with a JSON body and the shared AGENT_TOKEN.
func AgentPOST(path string, body []byte) (string, error) {
	return agentDo(http.MethodPost, path, body)
}

func agentPOSTHeaders(path string, body []byte, headers map[string]string) (string, error) {
	return agentDoTimeoutHeaders(http.MethodPost, path, body, 15*time.Second, headers)
}

func agentDo(method, path string, body []byte) (string, error) {
	return agentDoTimeout(method, path, body, 15*time.Second)
}

func agentDoTimeout(method, path string, body []byte, timeout time.Duration) (string, error) {
	return agentDoTimeoutHeaders(method, path, body, timeout, nil)
}

func agentDoTimeoutHeaders(method, path string, body []byte, timeout time.Duration, headers map[string]string) (string, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, agentBaseURL()+path, rdr)
	if err != nil {
		return "", err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if tok := os.Getenv("AGENT_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return "", &agentHTTPError{StatusCode: resp.StatusCode, Body: string(b)}
	}
	return string(b), nil
}

// CreateSessionKey derives a STABLE idempotency key from the launch intent so the LLM
// re-issuing create_session with the same arguments reproduces it — that is what lets a
// timed-out-then-retried create collapse onto the first session (see session_idempotency.go).
// Scoped by the CALLER so two unrelated callers never collide: the operator passes its
// conversation id, a session its own name (ADR 0073 decision 2). The scope is not optional —
// an empty one would fold two sessions' identical launches into a single child, and the second
// caller would be handed the first's session as if it were the one it asked for.
func CreateSessionKey(scope, dir, subdir, kind, model, effort, prompt string, worktree bool, branch, newBranch string, allowShared bool) string {
	if scope == "" {
		return ""
	}
	h := sha256.New()
	for _, f := range []string{scope, dir, subdir, kind, model, effort, prompt, strconv.FormatBool(worktree), branch, newBranch, strconv.FormatBool(allowShared)} {
		h.Write([]byte(f))
		h.Write([]byte{0})
	}
	return "cs_" + hex.EncodeToString(h.Sum(nil))
}

// checkCreateEffort refuses an effort the child would not run at, naming the ones it would, so
// the caller can correct itself in the same turn. It reads the catalog list_models answers
// from and resolves the model with the launch's own rule (agents.MatchModel): validating
// against any other row than the one that starts lets a value through that the model lacks.
//
// When the catalog cannot be read, or does not name the model unambiguously, the effort is let
// through: the Agent keeps launching on a degraded catalog and refuses unknown models itself,
// and blocking here would also block the retry that replays a create which already went through.
func checkCreateEffort(kind, model, effort string) error {
	if kind == "" {
		kind = "claude"
	}
	switch kind {
	case "agy", "cursor":
		return fmt.Errorf("%s は effort を別に指定できません。effort はモデル id に含まれているので、list_models から目的の effort の id を選んでください", kind)
	case "lcpp":
		return fmt.Errorf("lcpp は effort を指定できません（推論量はエンジン側の起動設定で決まります）")
	case "shell", "ssm":
		return fmt.Errorf("%s は effort を指定できません", kind)
	case "opencode", "kiro":
		// Their catalogs list no efforts, yet the CLI takes one (opencode's variant, kiro's
		// --effort), so the CLI is the only judge of the value. Nothing in this repo declares
		// their valid levels, and a guessed list would refuse real ones.
		return nil
	}
	if kind == "copilot" && (model == "" || strings.EqualFold(model, "auto")) {
		// The launch drops effort for auto, because copilot refuses it there.
		return fmt.Errorf("copilot の auto は effort を指定できません。effort を使うなら list_models から model も指定してください")
	}
	if model == "" {
		// The default is the CLI's own choice (the user's Claude Code settings, codex's
		// config), which no catalog row identifies and which may be a hidden or no-effort
		// model, so no value is known to hold for it.
		return fmt.Errorf("effort を指定するときは model も list_models から指定してください（既定モデルは CLI 側の設定で決まり、その effort を確かめられないため）")
	}
	out, err := agentGET("/agents/" + url.PathEscape(kind) + "/models")
	if err != nil {
		return nil
	}
	var cat struct {
		Models []agents.ModelChoice `json:"models"`
	}
	if json.Unmarshal([]byte(out), &cat) != nil {
		return nil
	}
	matched := agents.MatchModel(model, cat.Models)
	if len(matched) != 1 {
		return nil
	}
	m := matched[0]
	if slices.Contains(m.Efforts, effort) {
		return nil
	}
	if len(m.Efforts) == 0 {
		return fmt.Errorf("%s は effort を指定できません（effort を省略してください）", m.ID)
	}
	return fmt.Errorf("effort %q は %s では使えません。使える値: %s（省略するとモデルの既定）", effort, m.ID, strings.Join(m.Efforts, ", "))
}

// agentCreateSession POSTs /sessions and, crucially, does NOT let a client-side timeout
// turn a successful backend launch into a "failed" tool result the model would retry into
// a duplicate. It waits longer than the shared 15s client (launch does real work), and on
// a timeout OR a create_in_progress conflict it reconciles via the idempotency ledger:
// poll GET /sessions-idempotency/{key} until the session materializes, then return it. If
// the create genuinely failed the ledger clears and the original error is surfaced.
func agentCreateSession(body []byte, key string) (string, error) {
	out, err := agentDoTimeout(http.MethodPost, "/sessions", body, 40*time.Second)
	if err == nil {
		return out, nil
	}
	if key != "" && (isTimeoutErr(err) || isCreateInProgress(err)) {
		if out, ok := agentAwaitCreated(key, 45*time.Second); ok {
			return out, nil
		}
	}
	return "", err
}

func isTimeoutErr(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func isCreateInProgress(err error) bool {
	var he *agentHTTPError
	return errors.As(err, &he) && he.hasCode("create_in_progress")
}

// agentAwaitCreated polls the idempotency lookup until the create resolves. Because the
// lookup returns 202 while still launching (which agentDo would treat as success), it
// inspects the raw status itself: 200 => the session, 202 => keep waiting, a short run of
// 404s => the create failed (or never registered) so give up, transport errors => retry.
func agentAwaitCreated(key string, deadline time.Duration) (string, bool) {
	url_ := agentBaseURL() + "/sessions-idempotency/" + url.PathEscape(key)
	end := time.Now().Add(deadline)
	notFound := 0
	for time.Now().Before(end) {
		status, body := agentRawGET(url_)
		switch {
		case status == http.StatusOK:
			return body, true
		case status == http.StatusAccepted:
			notFound = 0 // still launching
		case status == http.StatusNotFound:
			if notFound++; notFound >= 3 {
				return "", false
			}
		case status == 0:
			// transient transport error (agent momentarily unreachable) — retry
		default:
			return "", false
		}
		time.Sleep(1500 * time.Millisecond)
	}
	return "", false
}

func agentRawGET(fullURL string) (int, string) {
	req, err := http.NewRequest(http.MethodGet, fullURL, nil)
	if err != nil {
		return 0, ""
	}
	if tok := os.Getenv("AGENT_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return 0, ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, string(b)
}

type agentHTTPError struct {
	StatusCode int
	Body       string
}

func (e *agentHTTPError) Error() string {
	return fmt.Sprintf("Agent API エラー (%d): %s", e.StatusCode, e.Body)
}

func (e *agentHTTPError) code() string {
	var body struct {
		Code  string `json:"code"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(e.Body), &body) != nil {
		return ""
	}
	if body.Error.Code != "" {
		return body.Error.Code
	}
	return body.Code
}

func (e *agentHTTPError) hasCode(code string) bool {
	return e.code() == code
}

// AgentSendToSession makes the orchestration contract atomic from the model's point
// of view: try delivery, resume only on the explicit stopped-state response, then
// retry delivery. Other conflicts (for example question_pending) remain errors and
// can never be reported as successful sends.
func AgentSendToSession(name string, body []byte) (out string, resumed bool, err error) {
	inputPath := "/sessions/" + url.PathEscape(name) + "/input"
	state, err := agentSessionStatus(name)
	if err != nil {
		return "", false, fmt.Errorf("送信前の状態確認に失敗しました: %w", err)
	}
	if !state.Alive {
		return agentResumeAndSend(name, inputPath, body)
	}
	// /input with confirm (delivery verification) blocks until the turn provably started — up to two
	// evidence windows plus one self-heal — so the client budget must exceed the
	// server-side worst case (AgentPOST's default 15s does not).
	out, err = agentDoTimeout(http.MethodPost, inputPath, body, 45*time.Second)
	if err == nil {
		return out, false, nil
	}
	var httpErr *agentHTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusConflict || !httpErr.hasCode("not_running") {
		return "", false, err
	}
	// The session stopped between the status read and input POST. Apply the same
	// resume path as an initially stopped session.
	return agentResumeAndSend(name, inputPath, body)
}

func agentResumeAndSend(name, inputPath string, body []byte) (out string, resumed bool, err error) {
	if _, err = AgentPOST("/sessions/"+url.PathEscape(name)+"/start", nil); err != nil {
		return "", false, fmt.Errorf("停止中セッションの再開に失敗しました: %w", err)
	}
	if err = agentWaitSessionReady(name, 30*time.Second, 500*time.Millisecond); err != nil {
		return "", true, err
	}
	// 45s for the same reason as the alive path: /input with confirm blocks until the
	// prompt provably became a turn (delivery verification), beyond AgentPOST's default 15s.
	out, err = agentDoTimeout(http.MethodPost, inputPath, body, 45*time.Second)
	if err != nil {
		return "", true, fmt.Errorf("再開後の送信に失敗しました: %w", err)
	}
	return out, true, nil
}

type agentSessionState struct {
	Alive bool `json:"alive"`
	Ready bool `json:"ready"`
}

// agentPeerSessionState returns the live state string for list_peer_sessions: the
// Agent's own drive state ("working" / "idle" / "question" / …) or "stopped" when the
// session isn't running. Separate from agentSessionStatus because that one intentionally
// decodes only the two fields the delivery path needs (alive/ready).
func agentPeerSessionState(name string) (string, error) {
	out, err := agentGET("/sessions/" + url.PathEscape(name) + "/status")
	if err != nil {
		return "", err
	}
	var st struct {
		Alive  bool   `json:"alive"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		return "", err
	}
	if !st.Alive {
		return "stopped", nil
	}
	return st.Status, nil
}

func agentSessionStatus(name string) (agentSessionState, error) {
	var state agentSessionState
	out, err := agentGET("/sessions/" + url.PathEscape(name) + "/status")
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal([]byte(out), &state); err != nil {
		return state, err
	}
	return state, nil
}

func agentWaitSessionReady(name string, timeout, interval time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		state, err := agentSessionStatus(name)
		if err != nil {
			return fmt.Errorf("再開後の状態確認に失敗しました: %w", err)
		}
		if state.Alive && state.Ready {
			return nil
		}
		if time.Now().Add(interval).After(deadline) {
			return errors.New("セッションを再開しましたが、入力可能になる前にタイムアウトしました")
		}
		time.Sleep(interval)
	}
}

// agentBaseURL derives the loopback URL of the in-container Agent from AGENT_ADDR.
func agentBaseURL() string {
	addr := envOr("AGENT_ADDR", ":7700")
	_, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		port = "7700"
	}
	return "http://127.0.0.1:" + port
}
