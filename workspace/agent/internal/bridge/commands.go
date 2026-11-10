package bridge

// Chat-side commands (ADR 0020, note of 2026-11-10): "!attach <session>" re-binds the thread
// the message was typed in to another existing session, and "!start <prompt>" starts a new
// session from the thread's session. Both are recognised only on the path a reply already
// takes — a bound user's message inside a known session thread — so they inherit contract 5
// (person-only, no channel listening) unchanged and add no new entry point.

import (
	"regexp"
	"strings"
)

// chatCommandRe matches "!attach x" / "!start x". "/" is accepted too for Discord, but Slack
// swallows a leading "/" as a slash command before the bot ever sees it, so "!" is the form
// the guide documents. The command word must be the whole first token ("!starter" is text).
var chatCommandRe = regexp.MustCompile(`(?is)^[!/](attach|start)(?:\s+(.*))?$`)

type chatCommand struct {
	name string // "attach" | "start"
	arg  string
}

func parseChatCommand(text string) (chatCommand, bool) {
	m := chatCommandRe.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return chatCommand{}, false
	}
	return chatCommand{name: strings.ToLower(m[1]), arg: strings.TrimSpace(m[2])}, true
}

// cmdEnv is what one command run needs from its provider's receive path.
type cmdEnv struct {
	store   *threadStore
	thread  string // the thread the message arrived in (routing key of store)
	session string // the session that thread is currently bound to
	source  string // sourceDiscord / sourceSlack
	key     string // provider message identity, for create idempotency
	deps    ReceiverDeps
}

func (d ReceiverDeps) say(ja, en string) string {
	if d.Localize != nil {
		return d.Localize(ja, en)
	}
	return en
}

// handlesCommand reports whether text is a command this deployment wired. An unwired command
// falls through to ordinary reply injection, so the text is never silently dropped.
func (d ReceiverDeps) handlesCommand(c chatCommand) bool {
	switch c.name {
	case "attach":
		return d.CheckAttach != nil
	case "start":
		return d.Start != nil
	}
	return false
}

// runChatCommand executes c and returns the line to post back into the thread.
func runChatCommand(c chatCommand, e cmdEnv) string {
	switch c.name {
	case "attach":
		return runAttach(c.arg, e)
	case "start":
		return runStart(c.arg, e)
	}
	return ""
}

func runAttach(arg string, e cmdEnv) string {
	d := e.deps
	target := strings.Fields(arg)
	if len(target) != 1 {
		return d.say("使い方: `!attach <セッション名>`", "Usage: `!attach <session>`")
	}
	to := target[0]
	if to == e.session {
		return d.say("このスレッドはすでに "+to+" に紐づいています", "This thread is already bound to "+to)
	}
	// The same gate a reply passes (kind, archived, moved): a thread bound to a session that
	// refuses replies would only produce errors on every later message.
	if reason, err := d.CheckAttach(to); err != nil {
		if reason == "" {
			reason = d.say("⚠️ そのセッションには紐づけられません", "⚠️ That session can't be bound")
		}
		return reason
	}
	prev, hadPrev, ok := e.store.rebind(e.thread, e.session, to)
	if !ok {
		// The thread stopped belonging to e.session between routing and now (a concurrent
		// rebind or a recreated thread): do nothing rather than guess.
		return d.say("⚠️ このスレッドの紐づけが変わったため中止しました。もう一度試してください",
			"⚠️ This thread's binding changed meanwhile — nothing was changed; try again")
	}
	msg := d.say("✓ このスレッドを "+to+" に紐づけました。以降の返信は "+to+" に届きます（"+e.session+" の通知は新しいスレッドに出ます）",
		"✓ This thread is now bound to "+to+". Replies here go to "+to+" (notifications for "+e.session+" will start a new thread)")
	if hadPrev && prev.Thread != "" {
		msg += d.say("\n"+to+" の元のスレッドへの返信はもう届きません", "\n"+to+"'s previous thread no longer receives replies")
	}
	return msg
}

func runStart(prompt string, e cmdEnv) string {
	d := e.deps
	if prompt == "" {
		return d.say("使い方: `!start <最初の指示>`", "Usage: `!start <first instruction>`")
	}
	reply, err := d.Start(e.session, prompt, e.source, e.key)
	if err != nil && reply == "" {
		reply = d.say("⚠️ セッションを開始できませんでした", "⚠️ Couldn't start the session")
	}
	return reply
}
