package bridge

// Receive supervisor (docs/log/37 P2a): owns the long-lived Discord Gateway connection and
// routes inbound thread replies back into sessions. Split from the send path on purpose —
// sending is a stateless per-drain REST call, receiving is one persistent WSS connection
// with its own lifecycle (bridge.go 契約1 note).
//
// Security (ADR0020 契約5, the ONLY defense): route ONLY the bound user's messages, and
// ONLY when they land in a session's own thread. No channel listening, no other authors.
// The received text is injected verbatim as user input — never mixed with system prompts.

import (
	"context"
	"errors"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// sourceDiscord tags turns injected from Discord so the mirror can badge them like
// operator-injected turns (docs/log/37 追加要件). Matches package main's turnSourceDiscord.
const sourceDiscord = "discord"

// ReceiverDeps is the capability the receiver needs from package main, passed as a
// callback to avoid an import cycle (the session-injection primitives live in main).
type ReceiverDeps struct {
	// Inject delivers an inbound chat message into a session as user input and records
	// its origin so the transcript can badge the turn. On failure it returns a short,
	// already-localized reason line the receiver posts back into the thread (so the user
	// learns why their reply was dropped — e.g. a pending question needs a button), plus
	// the underlying error the receiver only logs. reason is "" on success.
	Inject func(sessionName, text, source string) (reason string, err error)
	// Answer applies a P2b button click (an AskUserQuestion pick or a permission/plan
	// decision) to the session, structurally — never via free-text (契約6). It returns a
	// short user-facing outcome line the receiver shows on the (button) message, plus an
	// error it only logs. nil when P2b isn't wired.
	Answer func(pi ParsedInteraction) (feedback string, err error)
	// Operator delivers an inbound message from the dedicated fleet-operator thread to
	// the built-in operator assistant conversation (docs/log/37 P3先取り) and returns the
	// assistant's reply. Unlike Inject (a session, whose answer rides an existing
	// answer-ready notification), the operator conversation's reply has no such push,
	// so the receiver posts the returned reply back into the thread itself. On failure
	// reply carries an already-localized reason line to post, plus the error to log.
	Operator func(conv, text string) (reply string, err error)
}

// receiverPollInterval: how often to re-read secrets to notice a connect/disconnect or an
// identity change. There is no secrets-change notification, so we poll (mirrors the sender).
var receiverPollInterval = 5 * time.Second

var (
	receiverBackoffMin = 1 * time.Second
	receiverBackoffMax = 60 * time.Second
	// receiverHealthyAfter: a connection that lived this long is considered healthy, so the
	// reconnect backoff resets (a flapping token won't, a normal Gateway reconnect will).
	receiverHealthyAfter = 60 * time.Second
)

// StartReceiver launches the receive supervisors — one per chat provider (Discord Gateway +
// Slack Socket Mode), sharing the same provider-neutral deps (docs/log/37 Slack 追随). No-op
// without an Inject callback; cheap when no provider has receive configured — each just polls
// secrets and does nothing.
func StartReceiver(deps ReceiverDeps) {
	if deps.Inject == nil {
		return
	}
	ctx := context.Background()
	go superviseReceiver(ctx, deps)
	StartSlackReceiver(ctx, deps)
}

// superviseReceiver polls secrets and starts/stops the Gateway connection to match the
// configured Discord receive state. A change of token or bound user tears down the current
// connection and starts a fresh one.
func superviseReceiver(ctx context.Context, deps ReceiverDeps) {
	var (
		cancel   context.CancelFunc
		curToken string
		curUser  string
	)
	stop := func() {
		if cancel != nil {
			cancel()
			cancel = nil
			curToken, curUser = "", ""
		}
	}
	tick := time.NewTicker(receiverPollInterval)
	defer tick.Stop()
	for {
		token, boundUser := desiredReceive()
		switch {
		case token != "" && (cancel == nil || token != curToken || boundUser != curUser):
			stop()
			cctx, cc := context.WithCancel(ctx)
			cancel, curToken, curUser = cc, token, boundUser
			go runReceiverConn(cctx, token, boundUser, deps)
		case token == "" && cancel != nil:
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

// desiredReceive reads the current Discord receive intent from secrets: the bot token and
// the bound user id to trust, or ("","") when receive is off / unconfigured. Receive REQUIRES
// a bound user (契約5) — the mention target (channel mode, auto-filled owner) or the DM user.
func desiredReceive() (token, boundUser string) {
	s, err := secrets.Load()
	if err != nil || s.Discord == nil {
		return "", ""
	}
	d := s.Discord
	if d.Token == "" || !d.Receive {
		return "", ""
	}
	boundUser = d.MentionUserID
	if boundUser == "" {
		boundUser = d.UserID
	}
	if boundUser == "" {
		return "", "" // no identity to verify against — refuse to listen
	}
	return d.Token, boundUser
}

// runReceiverConn is the reconnect/backoff loop around one identity's Gateway connection.
// It returns (stops trying) on a fatal config error — the supervisor restarts it when the
// creds change.
func runReceiverConn(ctx context.Context, token, boundUser string, deps ReceiverDeps) {
	// Handling runs OFF the gateway read loop (serialDispatcher): an Answer can
	// legitimately take seconds (session resume, 429 retries up to 15s), and a
	// blocked read loop stops processing heartbeat ACKs — the connection then
	// looks dead and reconnects over and over.
	dispatch := serialDispatcher(ctx, 64)
	gw := &gateway{
		token:      token,
		onMsg:      func(m gatewayMessage) { dispatch(func() { routeInbound(m, token, boundUser, deps) }) },
		onInteract: func(gi gatewayInteraction) { dispatch(func() { routeInteraction(gi, token, boundUser, deps) }) },
	}
	backoff := receiverBackoffMin
	for {
		if ctx.Err() != nil {
			return
		}
		start := time.Now()
		err := gw.connectOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errDisallowedIntent) || errors.Is(err, errAuthFailed) {
			log.Printf("bridge: discord receive disabled: %v", err)
			return // fatal for this identity; supervisor revives it on a creds change
		}
		if err != nil {
			log.Printf("bridge: discord gateway dropped: %v", err)
		}
		if time.Since(start) >= receiverHealthyAfter {
			backoff = receiverBackoffMin // was healthy — reset backoff
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

// serialDispatcher returns an enqueue function backed by ONE worker goroutine.
// Inbound handling must not run on a WSS read loop (see runReceiverConn /
// runSlackReceiverConn); a single worker also preserves arrival order, which
// multi-goroutine dispatch would not.
func serialDispatcher(ctx context.Context, depth int) func(func()) {
	work := make(chan func(), depth)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case fn := <-work:
				fn()
			}
		}
	}()
	return func(fn func()) {
		select {
		case work <- fn:
		case <-ctx.Done():
		}
	}
}

// mentionPrefixRe strips leading Discord mentions (the bot's own, if the user tapped
// "reply" which prepends one, or an explicit @bot) so the injected text is what the user
// actually wrote.
var mentionPrefixRe = regexp.MustCompile(`^(?:<@!?\d+>\s*)+`)

// routeInbound is the identity + routing gate. It runs on the Gateway reader goroutine for
// EVERY message the bot can see, so it must be cheap and must reject aggressively.
func routeInbound(m gatewayMessage, token, boundUser string, deps ReceiverDeps) {
	if m.Author.Bot {
		return // ignore all bots, including our own notification echoes
	}
	if boundUser == "" || m.Author.ID != boundUser {
		return // 契約5: only the explicitly bound user is ever routed
	}
	text := strings.TrimSpace(mentionPrefixRe.ReplaceAllString(m.Content, ""))
	if text == "" {
		return
	}
	if name, ok := ThreadToSession(m.ChannelID); ok {
		routeSessionInbound(m, name, text, token, deps)
		return
	}
	if conv, ok := OperatorThreadMatch(m.ChannelID); ok {
		routeOperatorInbound(m, conv, text, token, deps)
		return
	}
	// Neither a known session thread nor the operator thread — no channel listening.
}

// routeSessionInbound injects a bound-user reply into the session its thread groups,
// then acks (👀 + typing) on success or posts a localized reason on failure.
func routeSessionInbound(m gatewayMessage, name, text, token string, deps ReceiverDeps) {
	reason, err := deps.Inject(name, text, sourceDiscord)
	if err != nil {
		log.Printf("bridge: inject into %s from discord failed: %v", name, err)
		// Tell the user WHY their reply was dropped (a pending question, a stopped
		// session) instead of leaving them guessing. Best-effort — a failed reply post
		// just logs.
		if reason != "" {
			if _, e := discordPostMessage(token, m.ChannelID, reason); e != nil {
				log.Printf("bridge: post inject-failure reason to %s failed: %v", name, e)
			}
		}
		return
	}
	// Received & now working: a durable 👀 receipt on the user's message, plus a typing
	// pulse for liveliness (the answer replaces it when the turn completes). Both are
	// best-effort — the inject already succeeded, so a missing permission only logs.
	if err := DiscordAddReaction(token, m.ChannelID, m.ID, receiptEmoji); err != nil {
		log.Printf("bridge: react to injected reply in %s failed: %v", name, err)
	}
	if err := DiscordTriggerTyping(token, m.ChannelID); err != nil {
		log.Printf("bridge: typing pulse in %s failed: %v", name, err)
	}
}

// typingPulseInterval re-fires the typing indicator (~10s lifetime each) while a slow
// operator turn runs, so the thread reads as "working" the whole time instead of going
// quiet after the first pulse. A var so tests can shrink it.
var typingPulseInterval = 8 * time.Second

// routeOperatorInbound handles a bound-user reply in the dedicated operator thread: it
// acks immediately (👀), then runs the (slow — LLM + MCP tools) operator turn OFF the
// Gateway reader goroutine so heartbeats and other messages aren't blocked, keeping a
// typing pulse alive until the reply lands and posting the reply back into the thread.
func routeOperatorInbound(m gatewayMessage, conv, text, token string, deps ReceiverDeps) {
	if deps.Operator == nil {
		return
	}
	if err := DiscordAddReaction(token, m.ChannelID, m.ID, receiptEmoji); err != nil {
		log.Printf("bridge: react to operator reply failed: %v", err)
	}
	go func() {
		stop := startTypingPulse(token, m.ChannelID)
		reply, err := deps.Operator(conv, text)
		stop()
		if err != nil {
			log.Printf("bridge: operator turn for conv %s failed: %v", conv, err)
		}
		// On success reply is the assistant text; on failure it's a localized reason.
		// Either way post it (best-effort) so the user isn't left staring at 👀.
		if e := postOperatorChunks(token, m.ChannelID, reply); e != nil {
			log.Printf("bridge: post operator reply failed: %v", e)
		}
	}()
}

// startTypingPulse fires the typing indicator immediately and then every
// typingPulseInterval until the returned stop func is called.
//
// **stop は goroutine の終了まで待つ**（idempotent）。以前は close(done) するだけで
// 戻っていたため、「止めた」あとも in-flight の typing POST が走り続け、返信を出した
// 後に打鍵中インジケータが一瞬残った。テストではもっとはっきり出て、次のテストが
// 差し替えた discordAPIBase をこの goroutine が読み、`-race` がデータ競合として検出した
// （slack 側の post 漏れと同じ「テストを跨いで生き残る goroutine」— docs/log/60 作業中に発見）。
func startTypingPulse(token, channelID string) func() {
	done := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		_ = DiscordTriggerTyping(token, channelID)
		t := time.NewTicker(typingPulseInterval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				_ = DiscordTriggerTyping(token, channelID)
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(done) })
		<-exited
	}
}

// routeInteraction is the P2b button-click gate — the interactive counterpart of
// routeInbound. Same sole defense (契約5): only the bound user's clicks are ever
// honored. It ACKs immediately (deferred update — no visible loading), applies the
// answer structurally via deps.Answer, then edits the message to show the outcome
// and clear the buttons so a decision can't be double-submitted.
func routeInteraction(gi gatewayInteraction, token, boundUser string, deps ReceiverDeps) {
	if deps.Answer == nil {
		return
	}
	if boundUser == "" || gi.authorID() != boundUser {
		return // only the explicitly bound user's clicks
	}
	pi, ok := ParseCustomID(gi.Data.CustomID)
	if !ok {
		return // not one of ours / malformed
	}
	// ACK within Discord's 3s window before the (possibly slower) apply.
	if err := DiscordAckInteraction(token, gi.ID, gi.Token); err != nil {
		log.Printf("bridge: ack interaction failed: %v", err)
	}
	feedback, err := deps.Answer(pi)
	if err != nil {
		log.Printf("bridge: answer interaction for %s failed: %v", pi.Session, err)
	}
	if feedback == "" {
		return // nothing to show (e.g. deps chose to stay silent)
	}
	if err := DiscordEditMessage(token, gi.ChannelID, gi.Message.ID, feedback, nil); err != nil {
		log.Printf("bridge: edit interaction message failed: %v", err)
	}
}
