package bridge

// Fleet-operator thread (docs/log/37 P3, brought forward: an @mention opens a fleet-operator
// conversation).
// A standing chat thread whose replies are routed NOT to a session but to the built-in
// operator assistant conversation (assistants.go ID "operator"). The turn machinery is
// reused wholesale (package main's runOperatorTurn ≈ handleChatSend); this file owns only
// the chat coordinates + the return-leg post.
//
// The pointer store holds {channel, thread, conv}: `conv` is opaque to bridge (a
// package-main chat conversation UUID it resolves). It is a separate file from the
// session thread map on purpose — the operator thread is not a session thread, so
// threadToSession never matches it and the receive loop branches on the operator match
// instead. On disconnect the thread coordinates are cleared but `conv` is kept: the
// operator conversation is one continuous thread (Console can deep-link it), and a
// reconnect re-establishes a thread against the same conv.
//
// Provider-scoped (docs/log/37 Slack follow-up): each provider owns its own file and its own conv,
// so Discord and Slack operator threads coexist. A given conv belongs to exactly one
// provider's store, which is what lets PostOperatorReply / PostOperatorApproval find the
// right thread from a conv alone.

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

// operatorStore is one provider's operator-thread pointer.
type operatorStore struct {
	file string
	mu   sync.Mutex
}

var (
	discordOperator = &operatorStore{file: "bridge-operator.json"}
	slackOperator   = &operatorStore{file: "bridge-operator-slack.json"}
)

func (o *operatorStore) path() string { return filepath.Join(paths.AgentConfigDir(), o.file) }

func (o *operatorStore) state() (OperatorRef, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.loadLocked()
}

func (o *operatorStore) loadLocked() (OperatorRef, bool) {
	var ref OperatorRef
	b, err := os.ReadFile(o.path())
	if err != nil {
		return ref, false
	}
	if err := json.Unmarshal(b, &ref); err != nil {
		return ref, false
	}
	return ref, true
}

func (o *operatorStore) saveLocked(ref OperatorRef) {
	b, err := json.Marshal(ref)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(o.path()), 0o700)
	_ = os.WriteFile(o.path(), b, 0o600)
}

func (o *operatorStore) save(channel, thread, conv string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.saveLocked(OperatorRef{Channel: channel, Thread: thread, Conv: conv})
}

// reset drops the chat coordinates but KEEPS the conversation id, so the operator
// conversation persists across a disconnect and a later reconnect re-threads the same
// conv. No-op when nothing was provisioned.
func (o *operatorStore) reset() {
	o.mu.Lock()
	defer o.mu.Unlock()
	ref, ok := o.loadLocked()
	if !ok {
		return
	}
	o.saveLocked(OperatorRef{Conv: ref.Conv})
}

// match reports the conversation this thread drives, when that thread IS the operator
// thread — the routing key for the inbound operator branch. Match on Thread alone,
// exactly like threadToSession.
func (o *operatorStore) match(threadID string) (conv string, ok bool) {
	ref, present := o.state()
	if !present || ref.Thread == "" || ref.Thread != threadID || ref.Conv == "" {
		return "", false
	}
	return ref.Conv, true
}

// --- Discord free-function wrappers (zero-change for the Discord operator path) --------

// OperatorState reads the Discord operator pointer.
func OperatorState() (OperatorRef, bool) { return discordOperator.state() }

// SaveOperatorState persists the full Discord operator pointer after provisioning.
func SaveOperatorState(channel, thread, conv string) { discordOperator.save(channel, thread, conv) }

// ResetOperatorThread drops the Discord operator coordinates (keeps the conv).
func ResetOperatorThread() { discordOperator.reset() }

// OperatorThreadMatch is the Discord receive loop's operator-thread test.
func OperatorThreadMatch(threadID string) (string, bool) { return discordOperator.match(threadID) }

// CreateOperatorThread posts a seed message to the Discord channel and opens a public
// thread from it. Returns the new thread id; the caller persists it via SaveOperatorState.
func CreateOperatorThread(token, channelID, name, seed string) (string, error) {
	msgID, err := discordPostMessage(token, channelID, seed)
	if err != nil {
		return "", err
	}
	return DiscordStartThread(token, channelID, msgID, name)
}

// --- Slack operator wrappers -----------------------------------------------------------

// SlackOperatorState reads the Slack operator pointer.
func SlackOperatorState() (OperatorRef, bool) { return slackOperator.state() }

// SaveSlackOperatorState persists the Slack operator pointer.
func SaveSlackOperatorState(channel, thread, conv string) { slackOperator.save(channel, thread, conv) }

// ResetSlackOperatorThread drops the Slack operator coordinates (keeps the conv).
func ResetSlackOperatorThread() { slackOperator.reset() }

// SlackOperatorThreadMatch is the Slack receive loop's operator-thread test.
func SlackOperatorThreadMatch(threadTS string) (string, bool) { return slackOperator.match(threadTS) }

// SlackCreateOperatorThread posts a seed message and returns its ts, which is the Slack
// thread root (Slack threads share the parent channel and are keyed by the root message
// ts, unlike Discord's per-thread channel id).
func SlackCreateOperatorThread(token, channelID, seed string) (string, error) {
	return slackPostMessage(token, channelID, "", seed, nil)
}

// --- conv-scanning posting (provider chosen by which operator store owns the conv) -----

// PostOperatorReply forwards a report auto-turn's reply into whichever provider's
// operator thread owns conv (docs/log/37 P3, brought forward), so an operator's autonomous reactions to
// session reports are visible on chat too. Best-effort: no matching thread → no-op.
func PostOperatorReply(conv, text string) error {
	if ref, ok := discordOperator.state(); ok && ref.Conv == conv && ref.Thread != "" {
		if s, err := secrets.Load(); err == nil && s.Discord != nil && s.Discord.Token != "" {
			return postOperatorChunks(s.Discord.Token, ref.Thread, text)
		}
	}
	if ref, ok := slackOperator.state(); ok && ref.Conv == conv && ref.Thread != "" {
		if s, err := secrets.Load(); err == nil && s.Slack != nil && s.Slack.BotToken != "" {
			return postSlackOperatorChunks(s.Slack.BotToken, ref.Channel, ref.Thread, text)
		}
	}
	return nil
}

// postOperatorChunks scrubs, chunks and posts a reply into a Discord thread, transparently
// unarchiving it first if the 24h auto-archive window has passed. Empty (after scrub/trim)
// posts nothing — Discord rejects it.
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

// postSlackOperatorChunks scrubs, chunks (Slack limit) and posts a reply into a Slack
// thread (channel + root ts). Empty after scrub/trim posts nothing.
func postSlackOperatorChunks(token, channel, threadTS, text string) error {
	text = strings.TrimSpace(ScrubSecrets(text))
	if text == "" {
		return nil
	}
	for _, chunk := range chunkTo(text, "", slackContentLimit) {
		if _, err := slackPostMessage(token, channel, threadTS, chunk, nil); err != nil {
			return err
		}
	}
	return nil
}
