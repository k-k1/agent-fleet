package bridge

// Fleet-operator thread (docs/37 P3先取り = @メンション→フリート・オペレーター会話).
// A standing Discord thread whose replies are routed NOT to a session but to the
// built-in operator assistant conversation (assistants.go ID "operator"). The turn
// machinery is reused wholesale (package main's runOperatorTurn ≈ handleChatSend);
// this file owns only the Discord coordinates + the return-leg post.
//
// The pointer store bridge-operator.json holds {channel, thread, conv}: `conv` is
// opaque to bridge (a package-main chat conversation UUID it resolves). It is a
// separate file from bridge-threads.json on purpose — the operator thread is not a
// session thread, so ThreadToSession never matches it and routeInbound branches on
// OperatorThreadMatch instead. On disconnect the thread coordinates are cleared but
// `conv` is kept: the operator conversation is one continuous thread (Console can
// deep-link it), and a reconnect re-establishes a thread against the same conv.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// OperatorRef is the persisted pointer: where the operator thread lives and which
// conversation it drives. Conv survives a disconnect (Channel/Thread do not).
type OperatorRef struct {
	Channel string `json:"channel"`
	Thread  string `json:"thread"`
	Conv    string `json:"conv"`
}

var operatorMu sync.Mutex

func operatorPath() string { return filepath.Join(paths.AgentConfigDir(), "bridge-operator.json") }

// OperatorState reads the operator pointer. ok is false when nothing has been
// provisioned yet (no file / unreadable).
func OperatorState() (OperatorRef, bool) {
	operatorMu.Lock()
	defer operatorMu.Unlock()
	return loadOperatorLocked()
}

func loadOperatorLocked() (OperatorRef, bool) {
	var ref OperatorRef
	b, err := os.ReadFile(operatorPath())
	if err != nil {
		return ref, false
	}
	if err := json.Unmarshal(b, &ref); err != nil {
		return ref, false
	}
	return ref, true
}

func saveOperatorLocked(ref OperatorRef) {
	b, err := json.Marshal(ref)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(operatorPath()), 0o700)
	_ = os.WriteFile(operatorPath(), b, 0o600)
}

// SaveOperatorState persists the full operator pointer after provisioning.
func SaveOperatorState(channel, thread, conv string) {
	operatorMu.Lock()
	defer operatorMu.Unlock()
	saveOperatorLocked(OperatorRef{Channel: channel, Thread: thread, Conv: conv})
}

// ResetOperatorThread drops the Discord coordinates but KEEPS the conversation id,
// so the operator conversation persists across a disconnect (Console still links it)
// and a later reconnect re-threads the same conv. No-op when nothing was provisioned.
func ResetOperatorThread() {
	operatorMu.Lock()
	defer operatorMu.Unlock()
	ref, ok := loadOperatorLocked()
	if !ok {
		return
	}
	saveOperatorLocked(OperatorRef{Conv: ref.Conv})
}

// OperatorThreadMatch reports the conversation a Discord thread drives, when that
// thread IS the operator thread — the routing key for the inbound operator branch
// (routeInbound). A thread MESSAGE_CREATE carries only its own id (channel_id), so we
// match on Thread alone, exactly like ThreadToSession.
func OperatorThreadMatch(threadID string) (conv string, ok bool) {
	ref, present := OperatorState()
	if !present || ref.Thread == "" || ref.Thread != threadID || ref.Conv == "" {
		return "", false
	}
	return ref.Conv, true
}

// CreateOperatorThread posts a seed message to the channel and opens a public thread
// from it (the only way Discord makes a thread — mirrors sendThreaded's 起票). Returns
// the new thread id; the caller persists it via SaveOperatorState.
func CreateOperatorThread(token, channelID, name, seed string) (string, error) {
	msgID, err := discordPostMessage(token, channelID, seed)
	if err != nil {
		return "", err
	}
	return DiscordStartThread(token, channelID, msgID, name)
}

// PostToOperatorThread posts text back into the operator thread — the return leg for
// an operator turn that ran OUTSIDE the receiver (a session report's auto-turn, which
// has no Discord context). Best-effort: silently no-op when no operator thread /
// connection exists. The receiver's own inbound path posts directly to m.ChannelID.
func PostToOperatorThread(text string) error {
	ref, ok := OperatorState()
	if !ok || ref.Thread == "" {
		return nil
	}
	s, err := secrets.Load()
	if err != nil || s.Discord == nil || s.Discord.Token == "" {
		return nil
	}
	return postOperatorChunks(s.Discord.Token, ref.Thread, text)
}

// postOperatorChunks scrubs, chunks and posts a reply into a thread, transparently
// unarchiving it first if the 24h auto-archive window has passed (a long-idle
// operator thread). Empty (after scrub/trim) posts nothing — Discord rejects it.
func postOperatorChunks(token, threadID, text string) error {
	text = strings.TrimSpace(ScrubSecrets(text))
	if text == "" {
		return nil
	}
	for _, chunk := range chunkMessage(text, "") {
		_, err := discordPostMessage(token, threadID, chunk)
		if isThreadArchived(err) {
			if uerr := DiscordUnarchiveThread(token, threadID); uerr == nil {
				_, err = discordPostMessage(token, threadID, chunk)
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}
