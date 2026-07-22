package bridge

// Receive supervisor (docs/37 P2a): owns the long-lived Discord Gateway connection and
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
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// sourceDiscord tags turns injected from Discord so the mirror can badge them like
// operator-injected turns (docs/37 追加要件). Matches package main's turnSourceDiscord.
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

// StartReceiver launches the supervisor. No-op without an Inject callback. Cheap when no
// Discord receive is configured — it just polls secrets and does nothing.
func StartReceiver(deps ReceiverDeps) {
	if deps.Inject == nil {
		return
	}
	go superviseReceiver(context.Background(), deps)
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
	gw := &gateway{
		token:      token,
		onMsg:      func(m gatewayMessage) { routeInbound(m, token, boundUser, deps) },
		onInteract: func(gi gatewayInteraction) { routeInteraction(gi, token, boundUser, deps) },
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
	name, ok := ThreadToSession(m.ChannelID)
	if !ok {
		return // not a known session thread — no channel listening
	}
	text := strings.TrimSpace(mentionPrefixRe.ReplaceAllString(m.Content, ""))
	if text == "" {
		return
	}
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
