package bridge

// Slack Socket Mode receive (docs/log/37 Slack 追随 = P2a/P2b/P3先取り parity): the Slack twin of
// gateway.go + the receive half of receiver.go. Socket Mode delivers events AND interactive
// button clicks over ONE outbound WSS (no public endpoint), exactly the constraint that made
// Discord's Gateway the right fit. Each frame is a typed envelope; every events_api /
// interactive envelope must be ACKed within 3s by echoing its envelope_id back.
//
// Security is identical (ADR0020 契約5, the sole defense): route ONLY the bound user's
// messages, and ONLY when they land in a session's / the operator's thread (matched by
// thread_ts — Slack's thread key). No channel listening; received text is injected verbatim.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// sourceSlack tags turns injected from Slack so the mirror badges them (docs/log/37 追加要件).
// Matches package main's turnSourceSlack.
const sourceSlack = "slack"

// slackReadTimeout bounds a silent socket: Slack pings the client roughly every 30–45s, so a
// window with no frame at all means the connection is dead — drop it and let the supervisor
// reconnect. Refreshed on every frame (data or ping).
var slackReadTimeout = 2 * time.Minute

// --- Socket Mode client ----------------------------------------------------------------

type slackInboundMsg struct {
	User, Text, Channel, TS, ThreadTS, Subtype, BotID string
}

type slackInboundInteraction struct {
	UserID, ChannelID, MessageTS, CustomID string
}

type slackSocket struct {
	appToken   string
	onEvent    func(slackInboundMsg)
	onInteract func(slackInboundInteraction)
	conn       *websocket.Conn

	// seenTS dedupes deliveries by channel/ts: Slack redelivers an event it
	// considers unACKed (e.g. across a reconnect), and an injected prompt must
	// not run twice. Small FIFO window; touched only from the read loop.
	seenTS map[string]bool
	seenQ  []string
}

// dedupTS reports whether the channel/ts pair was already delivered, recording it.
func (ss *slackSocket) dedupTS(channel, ts string) bool {
	if ts == "" {
		return false
	}
	key := channel + "/" + ts
	if ss.seenTS[key] {
		return true
	}
	if ss.seenTS == nil {
		ss.seenTS = map[string]bool{}
	}
	ss.seenTS[key] = true
	ss.seenQ = append(ss.seenQ, key)
	if len(ss.seenQ) > 512 {
		delete(ss.seenTS, ss.seenQ[0])
		ss.seenQ = ss.seenQ[1:]
	}
	return false
}

// slackEnvelope is one Socket Mode frame (server → client).
type slackEnvelope struct {
	Type       string          `json:"type"`        // hello | disconnect | events_api | interactive | slash_commands
	EnvelopeID string          `json:"envelope_id"` // ack target (events_api / interactive)
	Payload    json.RawMessage `json:"payload"`
	Reason     string          `json:"reason"` // disconnect reason (warning / refresh_requested / …)
}

// connectOnce opens one Socket Mode connection (a fresh apps.connections.open ticket each
// time) and pumps frames until it drops or ctx is cancelled. A `disconnect` frame (Slack
// asking us to reconnect) returns a non-nil error so the supervisor redials.
func (ss *slackSocket) connectOnce(ctx context.Context) error {
	wssURL, err := slackOpenSocket(ss.appToken)
	if err != nil {
		return err
	}
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.DialContext(ctx, wssURL, nil)
	if err != nil {
		return err
	}
	ss.conn = conn
	defer conn.Close()

	// Close the conn when ctx is cancelled so the blocking ReadMessage below
	// returns (same watchdog as gateway.go) — otherwise a stopped/re-credentialed
	// receiver keeps routing as the OLD bound user until the read timeout.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stop:
		}
	}()

	conn.SetReadLimit(1 << 20)
	_ = conn.SetReadDeadline(time.Now().Add(slackReadTimeout))
	conn.SetPingHandler(func(appData string) error {
		_ = conn.SetReadDeadline(time.Now().Add(slackReadTimeout))
		return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(5*time.Second))
	})
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		_ = conn.SetReadDeadline(time.Now().Add(slackReadTimeout))
		var env slackEnvelope
		if json.Unmarshal(data, &env) != nil {
			continue
		}
		switch env.Type {
		case "hello":
			// connected & authenticated
		case "disconnect":
			return fmt.Errorf("slack socket disconnect: %s", env.Reason)
		case "events_api":
			ss.ack(env.EnvelopeID)
			ss.handleEvent(env.Payload)
		case "interactive":
			ss.ack(env.EnvelopeID)
			ss.handleInteractive(env.Payload)
		case "slash_commands":
			ss.ack(env.EnvelopeID) // acknowledged but unused in v1
		}
	}
}

// ack echoes the envelope id so Slack doesn't retry the delivery. Same goroutine as the read
// loop, so no write races with the ping handler's pong.
func (ss *slackSocket) ack(envelopeID string) {
	if envelopeID == "" || ss.conn == nil {
		return
	}
	_ = ss.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := ss.conn.WriteJSON(map[string]string{"envelope_id": envelopeID}); err != nil {
		log.Printf("bridge: slack ack failed: %v", err)
	}
}

// handleEvent forwards a plain user message event (message.* subscriptions). Non-message
// types, message subtypes (edits/joins), and bot messages are dropped here or in the router.
func (ss *slackSocket) handleEvent(payload json.RawMessage) {
	if ss.onEvent == nil {
		return
	}
	var p struct {
		Event struct {
			Type     string `json:"type"`
			Subtype  string `json:"subtype"`
			User     string `json:"user"`
			BotID    string `json:"bot_id"`
			Text     string `json:"text"`
			Channel  string `json:"channel"`
			TS       string `json:"ts"`
			ThreadTS string `json:"thread_ts"`
		} `json:"event"`
	}
	if json.Unmarshal(payload, &p) != nil {
		return
	}
	if p.Event.Type != "message" {
		return // app_mention etc. — replies route via the plain message event
	}
	if ss.dedupTS(p.Event.Channel, p.Event.TS) {
		return // redelivery of an already-handled message
	}
	ss.onEvent(slackInboundMsg{
		User: p.Event.User, Text: p.Event.Text, Channel: p.Event.Channel,
		TS: p.Event.TS, ThreadTS: p.Event.ThreadTS, Subtype: p.Event.Subtype, BotID: p.Event.BotID,
	})
}

// handleInteractive forwards a Block Kit button click (block_actions).
func (ss *slackSocket) handleInteractive(payload json.RawMessage) {
	if ss.onInteract == nil {
		return
	}
	var p struct {
		Type string `json:"type"`
		User struct {
			ID string `json:"id"`
		} `json:"user"`
		Channel struct {
			ID string `json:"id"`
		} `json:"channel"`
		Message struct {
			TS string `json:"ts"`
		} `json:"message"`
		Actions []struct {
			ActionID string `json:"action_id"`
			Value    string `json:"value"`
		} `json:"actions"`
	}
	if json.Unmarshal(payload, &p) != nil || p.Type != "block_actions" || len(p.Actions) == 0 {
		return
	}
	cid := p.Actions[0].Value
	if cid == "" {
		cid = p.Actions[0].ActionID
	}
	ss.onInteract(slackInboundInteraction{
		UserID: p.User.ID, ChannelID: p.Channel.ID, MessageTS: p.Message.TS, CustomID: cid,
	})
}

// --- receive supervisor ----------------------------------------------------------------

// slackReceiveCreds is the identity + token set one Socket Mode connection routes against.
type slackReceiveCreds struct {
	botToken  string
	boundUser string
	botUserID string
}

// StartSlackReceiver launches the Slack receive supervisor (called from StartReceiver). No-op
// without an Inject callback, cheap when no Slack receive is configured.
func StartSlackReceiver(ctx context.Context, deps ReceiverDeps) {
	go superviseSlackReceiver(ctx, deps)
}

// superviseSlackReceiver polls secrets and starts/stops the Socket Mode connection to match
// the configured Slack receive state — the twin of superviseReceiver.
func superviseSlackReceiver(ctx context.Context, deps ReceiverDeps) {
	var (
		cancel context.CancelFunc
		curKey string
	)
	stop := func() {
		if cancel != nil {
			cancel()
			cancel = nil
			curKey = ""
		}
	}
	tick := time.NewTicker(receiverPollInterval)
	defer tick.Stop()
	for {
		bot, app, boundUser, botUserID := desiredSlackReceive()
		key := bot + "|" + app + "|" + boundUser
		switch {
		case app != "" && (cancel == nil || key != curKey):
			stop()
			cctx, cc := context.WithCancel(ctx)
			cancel, curKey = cc, key
			creds := slackReceiveCreds{botToken: bot, boundUser: boundUser, botUserID: botUserID}
			go runSlackReceiverConn(cctx, creds, app, deps)
		case app == "" && cancel != nil:
			stop()
		}
		select {
		case <-ctx.Done():
			stop()
			return
		case <-tick.C:
		}
	}
}

// desiredSlackReceive reads the current Slack receive intent: bot + app tokens and the bound
// user id, or zeros when receive is off / unconfigured. Requires the app token (Socket Mode)
// and a bound user (契約5).
func desiredSlackReceive() (bot, app, boundUser, botUserID string) {
	s, err := secrets.Load()
	if err != nil || s.Slack == nil {
		return "", "", "", ""
	}
	sl := s.Slack
	if sl.BotToken == "" || sl.AppToken == "" || !sl.Receive || sl.UserID == "" {
		return "", "", "", ""
	}
	return sl.BotToken, sl.AppToken, sl.UserID, sl.BotUserID
}

// runSlackReceiverConn is the reconnect/backoff loop around one identity's socket. It stops on
// a fatal auth error (bad token) — the supervisor revives it when the creds change.
func runSlackReceiverConn(ctx context.Context, creds slackReceiveCreds, appToken string, deps ReceiverDeps) {
	// Same off-read-loop dispatch as the Discord receiver: routing can block for
	// seconds, and the read loop must keep consuming frames to ACK within Slack's
	// 3s window (an unACKed event is redelivered → double injection).
	dispatch := serialDispatcher(ctx, 64)
	ss := &slackSocket{
		appToken:   appToken,
		onEvent:    func(m slackInboundMsg) { dispatch(func() { routeSlackInbound(m, creds, deps) }) },
		onInteract: func(gi slackInboundInteraction) { dispatch(func() { routeSlackInteraction(gi, creds, deps) }) },
	}
	backoff := receiverBackoffMin
	for {
		if ctx.Err() != nil {
			return
		}
		start := time.Now()
		err := ss.connectOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, ErrSlackUnauthorized) {
			log.Printf("bridge: slack receive disabled: %v", err)
			return // fatal for this identity; supervisor revives on a creds change
		}
		if err != nil {
			log.Printf("bridge: slack socket dropped: %v", err)
		}
		if time.Since(start) >= receiverHealthyAfter {
			backoff = receiverBackoffMin
		}
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
		if backoff *= 2; backoff > receiverBackoffMax {
			backoff = receiverBackoffMax
		}
	}
}

// --- inbound routing (contract 5 identity gate → session / operator thread) --------------

// slackMentionPrefixRe strips leading Slack mentions (<@Uxxx>) so the injected text is what
// the user actually wrote.
var slackMentionPrefixRe = regexp.MustCompile(`^(?:<@[A-Z0-9]+>\s*)+`)

// routeSlackInbound is the identity + routing gate (twin of routeInbound). Runs per message
// the bot can see, so it rejects aggressively.
func routeSlackInbound(m slackInboundMsg, creds slackReceiveCreds, deps ReceiverDeps) {
	if m.BotID != "" || m.Subtype != "" {
		return // bot echoes (including our own posts) and edits/joins/etc.
	}
	if m.User == "" || (creds.botUserID != "" && m.User == creds.botUserID) {
		return
	}
	if creds.boundUser == "" || m.User != creds.boundUser {
		return // 契約5: only the explicitly bound user is ever routed
	}
	if m.ThreadTS == "" {
		return // only in-thread replies route; a top-level channel message is not a session
	}
	text := strings.TrimSpace(slackMentionPrefixRe.ReplaceAllString(m.Text, ""))
	if text == "" {
		return
	}
	if name, ok := slackThreads.threadToSession(m.ThreadTS); ok {
		routeSlackSession(m, name, text, creds, deps)
		return
	}
	if conv, ok := slackOperator.match(m.ThreadTS); ok {
		routeSlackOperator(m, conv, text, creds, deps)
		return
	}
	// Neither a known session thread nor the operator thread — no channel listening.
}

// routeSlackSession injects a bound-user reply into the session its thread groups, then acks
// with a 👀 receipt on success or posts a localized reason on failure. Slack's Web API has no
// typing indicator, so the receipt + the eventual answer are the only feedback.
func routeSlackSession(m slackInboundMsg, name, text string, creds slackReceiveCreds, deps ReceiverDeps) {
	reason, err := deps.Inject(name, text, sourceSlack)
	if err != nil {
		log.Printf("bridge: inject into %s from slack failed: %v", name, err)
		if reason != "" {
			if _, e := slackPostMessage(creds.botToken, m.Channel, m.ThreadTS, reason, nil); e != nil {
				log.Printf("bridge: post inject-failure reason to slack %s failed: %v", name, e)
			}
		}
		return
	}
	if err := SlackAddReaction(creds.botToken, m.Channel, m.TS, slackReceiptReaction); err != nil {
		log.Printf("bridge: react to injected slack reply in %s failed: %v", name, err)
	}
}

// routeSlackOperator handles a bound-user reply in the operator thread: ack (👀), then run the
// (slow) operator turn OFF the read goroutine and post the reply back into the thread.
func routeSlackOperator(m slackInboundMsg, conv, text string, creds slackReceiveCreds, deps ReceiverDeps) {
	if deps.Operator == nil {
		return
	}
	if err := SlackAddReaction(creds.botToken, m.Channel, m.TS, slackReceiptReaction); err != nil {
		log.Printf("bridge: react to slack operator reply failed: %v", err)
	}
	go func() {
		reply, err := deps.Operator(conv, text)
		if err != nil {
			log.Printf("bridge: slack operator turn for conv %s failed: %v", conv, err)
		}
		if e := postSlackOperatorChunks(creds.botToken, m.Channel, m.ThreadTS, reply); e != nil {
			log.Printf("bridge: post slack operator reply failed: %v", e)
		}
	}()
}

// routeSlackInteraction is the button-click gate (twin of routeInteraction). The envelope was
// already ACKed by the read loop (that IS the 3s ack), so this just applies the answer and
// edits the message to show the outcome + clear the buttons. Same sole defense (契約5).
func routeSlackInteraction(gi slackInboundInteraction, creds slackReceiveCreds, deps ReceiverDeps) {
	if deps.Answer == nil {
		return
	}
	if creds.boundUser == "" || gi.UserID != creds.boundUser {
		return
	}
	pi, ok := ParseCustomID(gi.CustomID)
	if !ok {
		return
	}
	feedback, err := deps.Answer(pi)
	if err != nil {
		log.Printf("bridge: answer slack interaction for %s failed: %v", pi.Session, err)
	}
	if feedback == "" {
		return
	}
	if err := SlackUpdateMessage(creds.botToken, gi.ChannelID, gi.MessageTS, feedback, nil); err != nil {
		log.Printf("bridge: edit slack interaction message failed: %v", err)
	}
}
