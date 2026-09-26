package chatx

// chat_providers_muse.go drives `muse exec --json` (headless exec) for the assistant chat.
// One exec per turn; session continuity comes from the session id captured on the first turn
// and passed via --session-id on subsequent turns (ADR 0095 P2-20).
//
// The JSONL event schema (measured on Muse Code 1.3.0-R3401.1 with --provider echo):
//
//	{"stream":{"kind":"session","id":"<uuid>"},…,"payload_type":"run.output.delta",
//	 "payload":{"text":"…"}}
//	{"stream":{"kind":"session","id":"<uuid>"},…,"payload_type":"run.terminal.completed",
//	 "payload":{"terminal":"completed","text":"<full reply>","reason":null}}
//
// run.terminal.completed with terminal != "completed" carries a failure reason.
// The session id is identical across all events of one exec; the stream.kind=="session" id
// is what --session-id accepts on a subsequent exec.
//
// Clamps: EnsureClamps writes settings.json before every exec (same contract as the managed
// driver's Resume), and --no-foreign-personal-context plus the env vars from ChildEnv close
// the foreign-personal-context and observer gates that are available on exec but not on serve.
// The eighth clamp is the model: see museChatModel, and never send an exec without --model.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/muse"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// museChat drives `muse exec --json` for one assistant turn.
type museChat struct{}

// museAvailable reports whether muse is installed AND a credential is present. It does not
// spend a network call — the same check the managed driver's Resume uses for availability.
func museAvailable() bool {
	return muse.Installed() && muse.HasCredential()
}

func (museChat) Send(ctx context.Context, c *ChatConversation, prompt string) (string, error) {
	c.StartTurn()
	model, err := museChatModel(c)
	if err != nil {
		return "", err
	}
	call := usagex.Call{Kind: session.KindMuse, ModelReq: model}
	defer usagex.RecordCall(ctx, &call, time.Now())

	// Clamps must be written before every exec, same as the managed driver's Resume, so a
	// member who edits the file between turns does not run an unclamped assistant.
	if err := muse.EnsureClamps(); err != nil {
		return "", fmt.Errorf("muse: %w", err)
	}

	// Write the persona+prompt to a temp file — avoids argv length limits and lets the
	// headless prompt carry a potentially long persona preamble.
	f, err := os.CreateTemp("", "muse-chat-prompt-*")
	if err != nil {
		return "", fmt.Errorf("muse: prompt ファイルを作れません: %w", err)
	}
	promptPath := f.Name()
	defer os.Remove(promptPath)
	if _, err := f.WriteString(headlessPrompt(c.personaOf(), c.knowledgeDirs(), prompt)); err != nil {
		f.Close()
		return "", fmt.Errorf("muse: prompt ファイルへの書き込みに失敗しました: %w", err)
	}
	f.Close()

	// Always present: museChatModel refuses the turn rather than return "" (clamp 8).
	args := append(museChatBaseArgs(), "--model", model)
	if c.MuseSessionID != "" {
		args = append(args, "--session-id", c.MuseSessionID)
	}
	args = append(args, "--prompt-file", promptPath)

	cmd := museChatCmd(ctx, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("muse execution failed: %s", cliErr(err))
	}

	reply, sessionID, execErr := parseMuseExecEvents(out)
	if sessionID != "" {
		c.MuseSessionID = sessionID
	}
	if execErr != "" {
		return "", fmt.Errorf("muse returned an error: %s", execErr)
	}
	reply = strings.TrimRight(strings.TrimSpace(reply), "\n")
	if reply == "" {
		return "", errors.New("no response from muse")
	}
	call.OK = true
	// Muse exec carries no token counts in its JSONL output (only serve's session/tokenUsage
	// does). Record only the requested model; usage ledger gets no totals this turn.
	c.NoteTurnModel(model)
	return reply, nil
}

// museChatModel resolves the --model for one chat turn, and refuses the turn when it cannot.
//
// 🔴 An empty model is not "let the CLI decide" here, the way it is for every other kind.
// `muse exec` with no --model runs the catalog's `isDefault` row, and that row is the
// `-contributor` twin — the same model at the same price, except that the vendor says those
// conversations may be used to improve the product (ADR 0095 decision 6 clamp 8). The managed
// driver has resolved a safe default since P2-3; the chat path shipped without one, so until
// P2-21 an assistant conversation the member never pinned a model on ran on the contributor
// model (measured: the session store recorded `muse-spark-1.3-contributor`).
//
// Refusing beats falling back. The member who WANTS a contributor model picks it in
// Settings › AI › Muse Code, where both twins are listed; what AF must never do is choose it
// for someone who chose nothing.
func museChatModel(c *ChatConversation) (string, error) {
	if model := chatModelFor(c, session.KindMuse); model != "" {
		// A model the member hid is refused, as the launch guard refuses it for a session.
		if visibleModel(session.KindMuse, model) == "" {
			return "", errors.New("muse: モデル " + model + " は設定「使わないモデル」で除外されています。会話のモデルを変えるか、設定 > エージェント > 動作設定 で除外を解除してください。")
		}
		return model, nil
	}
	if model := museSafeVisible(prefsVisibility); model != "" {
		return model, nil
	}
	if len(museSafeModels()) > 0 {
		// Never fall back to a data-sharing row or to no --model: both run the contributor
		// default the safe pick exists to avoid.
		return "", errors.New("muse: 製品改善に使われないモデルがすべて設定「使わないモデル」で除外されているため、ターンを実行できません。設定 > エージェント > 動作設定 で除外を解除するか、会話のモデルを選んでください。")
	}
	return "", errors.New("muse: モデル目録を読めないため、ターンを実行できません（モデル未指定のまま実行すると会話が製品改善に使われうる contributor モデルになります。Settings › AI › Muse Code でモデルを選んでください）")
}

// museChatBaseArgs is the shared argv prefix for a muse exec turn. Flags in order:
//
//   - --json: machine-readable JSONL events on stdout (the whole point of this driver)
//   - --disable-shell / --disable-write: the assistant chat is Q&A only; the model must
//     not mutate the host filesystem or run shell commands through a chat turn
//   - --disable-web-tools: no outbound browsing from a chat turn
//   - --no-foreign-personal-context: belt to the MUSE_EXPERIMENTAL_FOREIGN_PERSONAL_CONTEXT_KILL=1
//     env var; exec has this flag, serve does not (ADR 0095 decision 6)
//   - --approval-mode never: there is no approval UI in a headless chat turn
//   - --approval-judge off: the judge makes its own model call for every approval; headless
//     with --approval-mode never means no approvals fire, but the flag is free and stays
//     as belt-to-braces for MUSE_DISABLE_APPROVAL_JUDGE=1 in the env
func museChatBaseArgs() []string {
	return []string{
		"exec",
		"--json",
		"--disable-shell",
		"--disable-write",
		"--disable-web-tools",
		"--no-foreign-personal-context",
		"--approval-mode", "never",
		"--approval-judge", "off",
	}
}

// museChatDataHome is an isolated XDG_DATA_HOME so chat-driven exec sessions do not appear
// in `muse session list` or pollute the member's session store. XDG_CONFIG_HOME stays the
// real one so settings.json (clamps) and auth.json (credential) are shared.
func museChatDataHome() (string, error) {
	dir := filepath.Join(paths.AgentStateDir(), "chat-muse-data")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// museChatCmd builds the exec command. The child environment carries the clamp env vars
// from muse.ChildEnv (observers off, foreign-context kill, auto-update suppression) so
// they are applied whether or not the caller sets them in its own process.
func museChatCmd(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, muse.Bin(), args...)
	cmd.Dir = chatWorkdir()
	env := muse.ChildEnv(os.Environ())
	if dataHome, err := museChatDataHome(); err == nil {
		env = append(env, "XDG_DATA_HOME="+dataHome)
	}
	cmd.Env = env
	return cmd
}

// parseMuseExecEvents reads the JSONL events from a muse exec --json run.
//
// Session id: extracted from any event's stream.id where stream.kind=="session" (all events
// carry it). This id is what --session-id accepts on the next turn.
//
// Reply: accumulated from run.output.delta payload.text fragments; the authoritative final
// text is run.terminal.completed payload.text (it equals the joined deltas for the echo
// provider and the real model).
//
// Error: run.terminal.completed with terminal != "completed" or a non-empty reason field.
func parseMuseExecEvents(out []byte) (reply, sessionID, execErr string) {
	var deltas []string
	for _, ln := range bytes.Split(out, []byte("\n")) {
		if len(bytes.TrimSpace(ln)) == 0 {
			continue
		}
		var ev struct {
			Stream struct {
				Kind string `json:"kind"`
				ID   string `json:"id"`
			} `json:"stream"`
			PayloadType string `json:"payload_type"`
			Payload     struct {
				Text     string  `json:"text"`
				Terminal string  `json:"terminal"`
				Reason   *string `json:"reason"`
			} `json:"payload"`
		}
		if json.Unmarshal(ln, &ev) != nil {
			continue
		}
		if sessionID == "" && ev.Stream.Kind == "session" && ev.Stream.ID != "" {
			sessionID = ev.Stream.ID
		}
		switch ev.PayloadType {
		case "run.output.delta":
			if ev.Payload.Text != "" {
				deltas = append(deltas, ev.Payload.Text)
			}
		case "run.terminal.completed":
			if ev.Payload.Terminal != "completed" {
				if ev.Payload.Reason != nil && *ev.Payload.Reason != "" {
					execErr = *ev.Payload.Reason
				} else {
					execErr = "turn failed (terminal: " + ev.Payload.Terminal + ")"
				}
			} else if ev.Payload.Text != "" {
				// The terminal text is the authoritative complete reply; prefer it over
				// the joined deltas in case any delta was lost.
				reply = ev.Payload.Text
			}
		}
	}
	if reply == "" && len(deltas) > 0 {
		reply = strings.Join(deltas, "")
	}
	return reply, sessionID, execErr
}
