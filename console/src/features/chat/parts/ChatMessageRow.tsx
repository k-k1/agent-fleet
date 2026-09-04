import { Icon } from "../../../ui/Icon.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { splitPastedImages } from "../../../lib/pastedImages.ts";
import { agentOf } from "../../../agents/registry.ts";
import { assistantVoiceOpts } from "../tts.ts";
import { noticeText } from "../notice.ts";
import { reportText } from "../report.ts";
import { ChatMarkdown } from "./ChatMarkdown.tsx";
import { AssistantTurn } from "./AssistantTurn.tsx";
import { ChatCopyButton } from "./ChatCopyButton.tsx";
import { ChatPastedThumb } from "./ChatPastedThumb.tsx";
import { formatMsgTS } from "./chatFormat.ts";
import type { Conversation, ChatMessage } from "../../../types/chat.ts";

// ChatMessageRow renders one persisted message of a conversation. The four roles
// (report / notice / assistant / user) are four different cards, not one bubble with
// variants — which is why the branches read as four early returns.
export function ChatMessageRow({
  m,
  conv,
  assistId,
  assistVoice,
  paneId,
  highlight,
}: {
  m: ChatMessage;
  conv: Conversation;
  assistId?: string;
  assistVoice: string;
  paneId: string;
  /** The sentence being read in karaoke mode. Null for every turn but the last (the caller
   * has already resolved it). */
  highlight: string | null;
}) {
  const tr = useT();
  // Session reports (docs/log/30) render as a session-origin card — the sender is
  // the reporting session, not the user or the assistant.
  if (m.role === "report") {
    return (
      <div className="chat-msg role-report">
        <div className="chat-role">
          <Icon name="broadcast" /> {tr("chat.report_role")}
          {m.session && <span className="chat-report-session">{m.session}</span>}
        </div>
        <div className="chat-body">
          <ChatMarkdown source={reportText(m)} breaks />
        </div>
        <div className="chat-msg-foot">
          {m.ts > 0 && <span className="cm-time">{formatMsgTS(m.ts)}</span>}
        </div>
      </div>
    );
  }
  // System notices (docs/log/30) — e.g. the operator's auto-turn budget ran out and
  // the loop paused. Rendered as a centered informational card, not a bubble; it
  // tells the user why the operator went quiet and how to resume. The body comes
  // from the catalog (ADR 0033), so it follows the UI language even on old threads.
  if (m.role === "notice") {
    return (
      <div className="chat-msg role-notice">
        <div className="chat-notice">
          <Icon name="info" />
          <div className="chat-notice-body">
            <ChatMarkdown source={noticeText(m)} breaks />
          </div>
        </div>
      </div>
    );
  }
  // Assistant replies render through AssistantTurn, which owns the bubble ref so
  // its footer can karaoke-read the rendered Markdown (docs/log/24).
  if (m.role === "assistant") {
    const turnAgent = agentOf(m.agent || conv.agent);
    return (
      <div className="chat-msg role-assistant">
        <AssistantTurn
          text={m.content}
          steps={m.steps}
          ts={m.ts}
          agentName={turnAgent?.assistantName || tr("chat.assistant_fallback")}
          model={m.model}
          voice={{ ...(assistantVoiceOpts(assistId, assistVoice) ?? {}), paneId }}
          highlight={highlight}
        />
      </div>
    );
  }
  // Split off any pasted-image references so a user bubble shows the user's
  // words + clickable thumbnails, not the machine-facing paths — and so the
  // copy button copies the words, not the image instruction.
  const { text, images } = splitPastedImages(m.content);
  return (
    <div className="chat-msg role-user">
      <div className="chat-role">{tr("chat.you")}</div>
      <div className="chat-body">
        {/* Both roles render as Markdown; `breaks` keeps plain newlines as
            line breaks (mirrors MirrorView's user turns). */}
        {text && <ChatMarkdown source={text} breaks />}
        {images.length > 0 && conv && (
          <div className="chat-imgs">
            {images.map((nm) => (
              <ChatPastedThumb key={nm} convId={conv.id} name={nm} />
            ))}
          </div>
        )}
      </div>
      {/* Footer under the bubble — time + copy, mirroring MirrorView's turn foot. */}
      <div className="chat-msg-foot">
        {m.ts > 0 && <span className="cm-time">{formatMsgTS(m.ts)}</span>}
        <ChatCopyButton text={text} />
      </div>
    </div>
  );
}
