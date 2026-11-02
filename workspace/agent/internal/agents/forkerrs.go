package agents

import "errors"

// Fork refusals shared by every kind. They reach the Console as free text (not_resumable /
// fork_bad_anchor) and the Agent does not know the member's language, so each is a "ja / en"
// pair, Japanese first. Kept in one place so a kind cannot drift back to a Japanese-only copy.
var (
	// ErrNoConversationToFork: the session has no stored conversation yet.
	ErrNoConversationToFork = errors.New("分岐できる会話がまだありません / there is no conversation to fork yet")
	// ErrNoForkPoint: a fork-at request without an anchor.
	ErrNoForkPoint = errors.New("分岐点が指定されていません / no fork point was given")
	// ErrForkPointNotFound: the anchor is not in the conversation (a stray id, or a subagent turn).
	ErrForkPointNotFound = errors.New("指定された分岐点がこの会話に見つかりません / the given fork point is not in this conversation")
	// ErrForkAtManagedOnly: a CLI launch cannot carry a fork point, only a managed one can.
	ErrForkAtManagedOnly = errors.New("発言時点からの分岐は managed のセッションでのみ利用できます / " +
		"forking at a past message works only for a managed session")
)
