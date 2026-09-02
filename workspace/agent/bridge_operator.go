package main

// Chat-bridge P3先取り (docs/log/37): @メンション→フリート・オペレーター会話. A reply the
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
	"log"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/assistants"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/bridge"
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
	en := bridgeAnswerEN()
	text = strings.TrimSpace(text)
	if text == "" {
		return "", errInjectEmpty
	}
	unlock := lockConv(conv)
	defer unlock()

	// P3 approval gate: mark THIS turn as the Discord-driven (unattended) operator turn so
	// the mcp-stdio subprocess's destructive tools gate on a Discord approval button. Console
	// operator chat (handleChatSend) never arms this, so it is never gated. Self-clears on a
	// TTL if the process dies before the defer runs.
	armOperatorTurn(conv)
	defer disarmOperatorTurn(conv)

	c, err := loadConv(conv)
	if err != nil {
		return fb(en, "⚠️ オペレーター会話が見つかりません", "⚠️ Operator conversation not found"), err
	}
	prov := chatProviderFor(c)
	actualAgent := chatProviderKind(c, prov)

	c.Messages = append(c.Messages, chatMessage{Role: "user", Content: text, TS: nowMs()})
	// A real user message resets the unattended auto-turn budget (docs/log/30), same as
	// handleChatSend — subsequent session reports get a fresh follow-up allowance.
	c.AutoTurns, c.AutoPausedNotified = 0, false

	// A longer ceiling than Console chat (chatTimeout): a Discord-driven turn may pause on a
	// human approval (bridgeApprovalTimeout), which must fit inside the turn.
	ctx, cancel := context.WithTimeout(context.Background(), operatorTurnTimeout)
	defer cancel()
	ctx = usagex.WithTag(ctx, tag)               // 使用量台帳（ADR 0029 §3）
	deregister := registerLiveTurn(conv, cancel) // Stop button / in_progress work as usual
	defer deregister()

	// docs/log/33 第4段: 閾値超過のまま新ターンに入るなら先に予防的自動圧縮。
	maybeAutoCompact(ctx, c, prov)
	// docs/log/30: undelivered session reports ride this prompt; docs/log/33: a compaction
	// summary rides the new session's first prompt, outermost.
	prompt, pendingReports := injectPendingReports(c, text)
	prompt, handoff := injectCarryover(c, actualAgent, prompt)
	prompt = syncProviderPrompt(c, actualAgent, prompt, len(c.Messages)-1)

	reply, err := prov.send(ctx, c, prompt)
	if err != nil && recoverForRetry(ctx, c, prov, err) {
		// docs/log/33 第3段: 超過検知 → 要約して畳み新セッションでリトライ。
		prompt, pendingReports = injectPendingReports(c, text)
		prompt, handoff = injectCarryover(c, actualAgent, prompt)
		prompt = syncProviderPrompt(c, actualAgent, prompt, len(c.Messages)-1)
		reply, err = prov.send(ctx, c, prompt)
	}
	if err != nil {
		if isContextOverflowErr(err) {
			noteContextOverflow(c)
		}
		c.UpdatedAt = nowMs()
		_ = saveConv(c) // persist the user turn + resume handle so a retry continues
		return fb(en, "⚠️ オペレーターの応答に失敗しました。時間をおいて試すか Console で確認してください",
			"⚠️ The operator turn failed — retry later or check the Console"), err
	}
	markReportsDelivered(pendingReports)
	if handoff {
		c.PendingHandoff = ""
	}
	c.Messages = append(c.Messages, chatMessage{Role: "assistant", Content: reply, Agent: actualAgent, Model: c.turnModel, TS: nowMs()})
	c.ActiveAgent = actualAgent
	markProviderSynced(c, actualAgent, len(c.Messages))
	noteContextPressure(c)
	c.UpdatedAt = nowMs()
	if err := saveConv(c); err != nil {
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
	now := nowMs()
	c := &chatConversation{
		ID: randUUID(), Slug: newConvSlug(), Title: a.Name, CreatedAt: now, UpdatedAt: now, Messages: []chatMessage{},
		AssistantID: a.ID, Agent: a.Agent, Model: resolveChatModel(a.Agent, a.Model),
		Persona: a.Persona, Tools: a.Tools, Knowledge: a.Knowledge, Integrations: a.Integrations,
	}
	if err := saveConv(c); err != nil {
		return "", err
	}
	return c.ID, nil
}

// provisionDiscordOperator ensures a standing operator thread + conversation exist for
// the connection (docs/log/37 P3先取り), reusing both across reconnects. Best-effort: any
// step failing just logs — a missing operator thread degrades to "no @mention route",
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
	// prior disconnect that cleared it) means re-起票.
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

// provisionSlackOperator is the Slack twin of provisionDiscordOperator (docs/log/37 Slack 追随):
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
	_, err := loadConv(conv)
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

// operatorThreadName / operatorThreadSeed are the localized 起票 strings for the standing
// operator thread. The 🛰 glyph mirrors the Console's operator assistant icon vibe.
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
