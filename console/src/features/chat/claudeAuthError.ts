// Recognising Claude's "the OAuth session is gone" failure in free-form error text.
//
// The wording comes from the upstream CLI ("Failed to authenticate: OAuth session expired and
// could not be refreshed"), so it is matched on one short fragment, case-insensitively, and the
// fragment lives only here. The Go side keeps its twin in agents/claude/errors.go; both must
// change together.
//
// Deliberately narrow: a generic "failed to authenticate" also describes a wrong API key or a
// blocked organisation, and telling someone to sign in again for a failure that signing in does
// not fix is the harm the transcript classifier already avoids.
export const CLAUDE_AUTH_EXPIRED_FRAGMENT = "oauth session expired";

export function isClaudeAuthExpired(text: string | undefined | null): boolean {
  return !!text && text.toLowerCase().includes(CLAUDE_AUTH_EXPIRED_FRAGMENT);
}
