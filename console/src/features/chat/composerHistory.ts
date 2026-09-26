// composerHistory is what ↑/↓ walks in the chat composer: the messages the member typed there,
// oldest first, consecutive repeats folded. A user message with a source (a reply in the
// bridge's operator thread, a scheduled fire) was not typed in this composer, so recalling it
// would hand the member words they never wrote. Only the visible words are kept — the
// machine-facing pasted-image instruction is stripped. The mirror's twin is
// features/mirror/transcript/model.ts composerHistory.
import type { ChatMessage } from "../../types/chat.ts";
import { splitPastedImages } from "../../lib/pastedImages.ts";

export function composerHistory(messages: ChatMessage[]): string[] {
  const out: string[] = [];
  for (const m of messages) {
    if (m.role !== "user" || m.source) continue;
    const s = splitPastedImages(m.content).text.trim();
    if (s && out[out.length - 1] !== s) out.push(s);
  }
  return out;
}
