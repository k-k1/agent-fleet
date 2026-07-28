// System-notice bodies (role==="notice") are rendered from the catalog, not from the
// text the backend stored (ADR 0033). The agent writes only a key + arguments
// (workspace/agent/chat_notice.go), so a card written while the Console was in Japanese
// reads in English the moment the user switches Lang — the language of a notice is a
// display concern, not a property of the stored thread.
//
// `content` remains the source-language (ja) fallback: notices written before the keys
// existed carry no key, and an unknown key must degrade to readable text rather than to
// an empty card.
import { t, tCount, tMaybe } from "../../lib/i18n/index.ts";
import type { ChatMessage } from "../../types/chat.ts";

// The auto-pause notice is assembled from three fragments because its middle sentence is
// conditional (and plural in English); every other notice is a single template.
const AUTO_PAUSED = "chat.notice.auto_paused";

export function noticeText(m: Pick<ChatMessage, "content" | "notice_key" | "notice_args">): string {
  const key = m.notice_key;
  if (!key) return m.content;
  const args = m.notice_args ?? {};
  if (key === AUTO_PAUSED) {
    const pending = Number(args.pending ?? 0);
    return (
      t(`${AUTO_PAUSED}.head`, { limit: args.limit ?? "" }) +
      (pending > 0 ? tCount(`${AUTO_PAUSED}.pending`, pending) : "") +
      t(`${AUTO_PAUSED}.tail`)
    );
  }
  return tMaybe(key, args) ?? m.content;
}
