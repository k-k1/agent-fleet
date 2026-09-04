package main

// Chat-bridge P3, brought forward (docs/log/37): @mention → the fleet-operator
// conversation. A reply the
// bound user posts in the dedicated Discord operator thread is delivered here as a user
// turn on the built-in operator assistant conversation (assistants.go ID "operator"),
// which can inspect and drive the fleet (af_write MCP). The reply is posted back into
// the thread by the receiver (internal/bridge). This is the package-main half wired into
// bridge via ReceiverDeps.Operator — the same import-cycle dodge as Inject / Answer.
//
// The turn machinery is NOT reinvented: runOperatorTurn is handleChatSend's non-HTTP
// twin (same lock, auto-compaction, pending-report injection, overflow self-heal, and
// AutoTurns reset). Bloat is capped by docs/log/33 preventive auto-compaction exactly like
// the Console operator chat — this IS a Console-visible operator conversation, just one
// the bridge also owns a deep link to.

import (
	"context"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"log"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/assistants"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/bridge"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// runOperatorTurn runs ONE operator turn over the conversation and returns the assistant
// reply. On failure it returns an already-localized reason line (for the receiver to post
// back) plus the error to log — mirroring ReceiverDeps.Inject's (reason, err) contract.
//
// This is the Discord/Slack entry point. The scheduled assistant fire (docs/log/38
// session_mode=assistant) shares the same machinery through runOperatorTurnAs — the two
// are the same turn but NOT the same consumption to a reader of the usage graph, so the
// usage tag is the caller's to supply (ADR 0029 §2).
func runOperatorTurn(conv, text string) (string, error) {
	return runOperatorTurnAs(conv, text, usagex.Tag{
		Feature: usagex.FeatureAssistantBridge, Trigger: usagex.TriggerBridge, Ref: conv,
	})
}

// runOperatorTurnAs is runOperatorTurn with an explicit usage tag.
func runOperatorTurnAs(conv, text string, tag usagex.Tag) (string, error) {
	en := sessionx.BridgeAnswerEN()
	text = strings.TrimSpace(text)
	if text == "" {
		return "", sessionx.ErrInjectEmpty
	}
	unlock := chatx.LockConv(conv)
	defer unlock()

	// P3 approval gate: mark THIS turn as the Discord-driven (unattended) operator turn so
	// the mcp-stdio subprocess's destructive tools gate on a Discord approval button. Console
	// operator chat (handleChatSend) never arms this, so it is never gated. Self-clears on a
	// TTL if the process dies before the defer runs.
	sessionx.ArmOperatorTurn(conv)
	defer sessionx.DisarmOperatorTurn(conv)

	c, err := chatx.LoadConv(conv)
	if err != nil {
		return sessionx.Fb(en, "⚠️ オペレーター会話が見つかりません", "⚠️ Operator conversation not found"), err
	}
	prov := chatx.ChatProviderFor(c)
	actualAgent := chatx.ChatProviderKind(c, prov)

	c.Messages = append(c.Messages, chatx.ChatMessage{Role: "user", Content: text, TS: chatx.NowMs()})
	// A real user message resets the unattended auto-turn budget (docs/log/30), same as
	// handleChatSend — subsequent session reports get a fresh follow-up allowance.
	c.AutoTurns, c.AutoPausedNotified = 0, false

	// A longer ceiling than Console chat (chatTimeout): a Discord-driven turn may pause on a
	// human approval (bridgeApprovalTimeout), which must fit inside the turn.
	ctx, cancel := context.WithTimeout(context.Background(), sessionx.OperatorTurnTimeout)
	defer cancel()
	ctx = usagex.WithTag(ctx, tag)                     // usage ledger (ADR 0029 §3)
	deregister := chatx.RegisterLiveTurn(conv, cancel) // Stop button / in_progress work as usual
	defer deregister()

	// docs/log/33 stage 4: entering a new turn while still over the threshold means a
	// pre-emptive auto-compaction first.
	chatx.MaybeAutoCompact(ctx, c, prov)
	// docs/log/30: undelivered session reports ride this prompt; docs/log/33: a compaction
	// summary rides the new session's first prompt, outermost.
	prompt, pendingReports := chatx.InjectPendingReports(c, text)
	prompt, handoff := chatx.InjectCarryover(c, actualAgent, prompt)
	prompt = chatx.SyncProviderPrompt(c, actualAgent, prompt, len(c.Messages)-1)

	reply, err := prov.Send(ctx, c, prompt)
	if err != nil && chatx.RecoverForRetry(ctx, c, prov, err) {
		// docs/log/33 stage 3: overflow detected → summarize, fold, and retry in a new session.
		prompt, pendingReports = chatx.InjectPendingReports(c, text)
		prompt, handoff = chatx.InjectCarryover(c, actualAgent, prompt)
		prompt = chatx.SyncProviderPrompt(c, actualAgent, prompt, len(c.Messages)-1)
		reply, err = prov.Send(ctx, c, prompt)
	}
	if err != nil {
		if chatx.IsContextOverflowErr(err) {
			chatx.NoteContextOverflow(c)
		}
		c.UpdatedAt = chatx.NowMs()
		_ = chatx.SaveConv(c) // persist the user turn + resume handle so a retry continues
		return sessionx.Fb(en, "⚠️ オペレーターの応答に失敗しました。時間をおいて試すか Console で確認してください",
			"⚠️ The operator turn failed — retry later or check the Console"), err
	}
	chatx.MarkReportsDelivered(pendingReports)
	if handoff {
		c.PendingHandoff = ""
	}
	c.Messages = append(c.Messages, chatx.ChatMessage{Role: "assistant", Content: reply, Agent: actualAgent, Model: c.TurnModel, TS: chatx.NowMs()})
	c.ActiveAgent = actualAgent
	chatx.MarkProviderSynced(c, actualAgent, len(c.Messages))
	chatx.NoteContextPressure(c)
	c.UpdatedAt = chatx.NowMs()
	if err := chatx.SaveConv(c); err != nil {
		log.Printf("bridge: save operator conv %s: %v", conv, err)
	}
	return reply, nil
}

// createOperatorConversation materializes a fresh chat conversation bound to the
// built-in "operator" assistant — snapshotting its persona/tools/knowledge exactly like
// handleChatCreate so af_write MCP attaches (a bare conversation would get no tools).
func createOperatorConversation() (string, error) {
	a, err := assistants.Get("operator", assistantDeps())
	if err != nil {
		return "", err
	}
	now := chatx.NowMs()
	c := &chatx.ChatConversation{
		ID: chatx.RandUUID(), Slug: chatx.NewConvSlug(), Title: a.Name, CreatedAt: now, UpdatedAt: now, Messages: []chatx.ChatMessage{},
		AssistantID: a.ID, Agent: a.Agent, Model: chatx.ResolveChatModel(a.Agent, a.Model),
		Persona: a.Persona, Tools: a.Tools, Knowledge: a.Knowledge, Integrations: a.Integrations,
	}
	if err := chatx.SaveConv(c); err != nil {
		return "", err
	}
	return c.ID, nil
}

// provisionDiscordOperator ensures a standing operator thread + conversation exist for
// the connection (docs/log/37, P3 brought forward), reusing both across reconnects.
// Best-effort: any step failing just logs — a missing operator thread degrades to "no @mention route",
// never a failed connect. Called async from handlePutDiscordConn (channel + receive).
func provisionDiscordOperator(token, channelID, lang string) {
	ref, _ := bridge.OperatorState()
	conv := ref.Conv
	// Reuse the canonical conversation when it still exists (one continuous thread —
	// Console shares it); otherwise create it once.
	if conv == "" || !operatorConvExists(conv) {
		newConv, err := createOperatorConversation()
		if err != nil {
			log.Printf("bridge: create operator conversation failed: %v", err)
			return
		}
		conv = newConv
	}
	thread := ref.Thread
	// Reuse the thread only if it targets the current channel; a channel change (or a
	// prior disconnect that cleared it) means opening a new one.
	if thread == "" || ref.Channel != channelID {
		t, err := bridge.CreateOperatorThread(token, channelID, operatorThreadName(lang), operatorThreadSeed(lang))
		if err != nil {
			log.Printf("bridge: create operator thread failed: %v", err)
			return
		}
		thread = t
	}
	bridge.SaveOperatorState(channelID, thread, conv)
}

// provisionSlackOperator is the Slack twin of provisionDiscordOperator (docs/log/37,
// the Slack follow-up):
// ensure a standing operator thread + conversation for the Slack connection, reusing both
// across reconnects. Slack threads carry no name (the seed message titles it) and are keyed
// by the seed's ts. Best-effort + async, called from handlePutSlackConn.
func provisionSlackOperator(botToken, channelID, lang string) {
	ref, _ := bridge.SlackOperatorState()
	conv := ref.Conv
	if conv == "" || !operatorConvExists(conv) {
		newConv, err := createOperatorConversation()
		if err != nil {
			log.Printf("bridge: create slack operator conversation failed: %v", err)
			return
		}
		conv = newConv
	}
	thread := ref.Thread
	if thread == "" || ref.Channel != channelID {
		t, err := bridge.SlackCreateOperatorThread(botToken, channelID, operatorThreadSeed(lang))
		if err != nil {
			log.Printf("bridge: create slack operator thread failed: %v", err)
			return
		}
		thread = t
	}
	bridge.SaveSlackOperatorState(channelID, thread, conv)
}

func operatorConvExists(conv string) bool {
	_, err := chatx.LoadConv(conv)
	return err == nil
}

// maybePushOperatorReply forwards a report auto-turn's reply into whichever provider's
// operator thread owns the conversation, so the operator's autonomous reactions to
// session reports are visible on chat too (the raw report already lands in the session's
// own thread via the P1 path). Best-effort + async; a no-match conv is a silent no-op.
func maybePushOperatorReply(conv, reply string) {
	if strings.TrimSpace(reply) == "" {
		return
	}
	go func() {
		if err := bridge.PostOperatorReply(conv, reply); err != nil {
			log.Printf("bridge: push operator auto-turn reply failed: %v", err)
		}
	}()
}

// operatorThreadName / operatorThreadSeed are the localized strings used to open the
// standing operator thread. The 🛰 glyph mirrors the Console's operator assistant icon vibe.
func operatorThreadName(lang string) string {
	if lang == "en" {
		return "🛰 Fleet Operator"
	}
	return "🛰 フリート・オペレーター"
}

func operatorThreadSeed(lang string) string {
	if lang == "en" {
		return "🛰 **Fleet Operator** — reply in this thread to talk to the fleet operator. " +
			"Ask what the fleet is doing, kick off a session, or steer running work; replies come back here."
	}
	return "🛰 **フリート・オペレーター** — このスレッドに返信するとフリート・オペレーターと会話できます。" +
		"稼働状況の確認・新しいセッションの起動・作業中セッションへの指示などを頼めます（応答はこのスレッドに返ります）。"
}
