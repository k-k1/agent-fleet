import { createPortal } from "react-dom";
import type { ReactNode, RefObject } from "react";
import { Icon } from "../../../ui/Icon.tsx";
import { ViewHead } from "../../../ui/ViewHead.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { kindClass } from "../../../lib/sessionkind.ts";
import { agentOf, AGENTS } from "../../../agents/registry.ts";
import { SESSION_KINDS } from "../../../types/session.ts";
import type { Conversation } from "../../../types/chat.ts";
import type { Assistant } from "../../../types/assistant.ts";
import type { ConnectionsStatus, SessionKind } from "../../../types/session.ts";

// Backends a conversation can run on, straight off the registry cap — the same source as
// the assistant form's picker and the Agent's chatProviders map.
const CHAT_KINDS: SessionKind[] = SESSION_KINDS.filter((k) => AGENTS[k].caps.headlessChat);

// ChatHead is the pane's title row: the conversation title, the backend chip (which
// doubles as the agent-switch picker on a real conversation), the state chip, and the
// 作業計画 toggle. It owns no state — the picker's open flag, its dismiss wiring and its
// placement all live in ChatView, because they are what the refs below are anchored to.
export function ChatHead({
  headerActions,
  title,
  draftAsst,
  conv,
  conversationId,
  agent,
  agentKind,
  agentTagRef,
  agentMenuRef,
  agentPickerOpen,
  onToggleAgentPicker,
  onSwitchAgent,
  chatConns,
  switching,
  showStreaming,
  compacting,
  wsRunning,
  stateChip,
  planOpen,
  onTogglePlan,
}: {
  headerActions?: ReactNode;
  title: string;
  draftAsst: Assistant | null;
  conv: Conversation | null;
  conversationId: string | null;
  agent: ReturnType<typeof agentOf> | null;
  agentKind: SessionKind | null;
  agentTagRef: RefObject<HTMLButtonElement | null>;
  agentMenuRef: RefObject<HTMLDivElement | null>;
  agentPickerOpen: boolean;
  onToggleAgentPicker: () => void;
  onSwitchAgent: (kind: SessionKind) => void;
  chatConns: ConnectionsStatus | null;
  switching: boolean;
  showStreaming: boolean;
  compacting: boolean;
  wsRunning: boolean;
  stateChip: { cls: string; icon: string; spin: boolean; text: string };
  planOpen: boolean;
  onTogglePlan: () => void;
}) {
  const tr = useT();
  return (
        <ViewHead className="fileinfo" actions={headerActions}>
          <span className="fi-name">
            <Icon name={draftAsst?.icon || "comment-discussion"} /> {title}
          </span>
          {/* エージェントのチップ。既存会話ではそのまま切替ピッカーのボタンを兼ねる
              （draft はまだ会話が無いので表示のみ）。 */}
          {agent && conversationId && (
            <button
              type="button"
              ref={agentTagRef}
              className={"kind-tag kind-" + kindClass(agentKind!) + " chat-agent-pick"}
              title={tr("chat.switch_agent_tip")}
              aria-haspopup="menu"
              aria-expanded={agentPickerOpen}
              disabled={switching || showStreaming || compacting || !wsRunning}
              onClick={onToggleAgentPicker}
            >
              <Icon name={switching ? "loading" : agent.icon} spin={switching} />
              {agent.assistantName}
              <Icon name="chevron-down" className="chat-agent-caret" />
            </button>
          )}
          {agent && !conversationId && (
            <span className={"kind-tag kind-" + kindClass(agentKind!)}>
              <Icon name={agent.icon} />
              {agent.assistantName}
            </span>
          )}
          {agentPickerOpen &&
            createPortal(
              <div className="ui-menu chat-agent-menu" ref={agentMenuRef} role="menu" onMouseDown={(e) => e.stopPropagation()}>
                <div className="assistant-picker-label">{tr("chat.switch_agent")}</div>
                {CHAT_KINDS.map((k) => (
                  <button
                    key={k}
                    type="button"
                    className="ui-menu-item"
                    role="menuitemradio"
                    aria-checked={k === conv?.agent}
                    // 未接続の CLI にピン留めしても、送信時に接続済みのバックエンドへ退避する
                    // だけ（chatProviderFor）＝選ばせても効かない。接続状況が分からないとき
                    // （キャッシュが冷えている）は塞がない。
                    disabled={!!chatConns && !agentOf(k).available({ conns: chatConns })}
                    title={
                      chatConns && !agentOf(k).available({ conns: chatConns })
                        ? tr("chat.switch_agent_offline")
                        : undefined
                    }
                    onClick={() => onSwitchAgent(k)}
                  >
                    <Icon name={k === conv?.agent ? "check" : "blank"} />
                    {/* kind の色は tokens.css の --kind-* が1ソース（agent-display-naming）。 */}
                    <span className="chat-agent-ic" style={{ color: `var(--kind-${kindClass(k)})` }}>
                      <Icon name={agentOf(k).icon} />
                    </span>
                    {agentOf(k).assistantName}
                  </button>
                ))}
                <div className="chat-agent-note muted">{tr("chat.switch_agent_note")}</div>
              </div>,
              document.body,
            )}
          {(conv || showStreaming || !wsRunning) && (
            <span className={"session-state " + stateChip.cls}>
              <Icon name={stateChip.icon} spin={stateChip.spin} /> {stateChip.text}
            </span>
          )}
          {/* 作業計画（docs/log/33 第5段）: 圧縮を跨いで原文のまま運ばれる枠の開閉。計画が
              入っている会話は塗って示す — 「アシスタントが絶対に忘れない内容」がどれかを
              一目で分かるようにするのがこのバッジの役目。 */}
          {conversationId && (
            <button
              type="button"
              className={"chat-plan-toggle" + (conv?.plan ? " has-plan" : "") + (planOpen ? " open" : "")}
              title={tr("chat.plan.toggle_tip")}
              aria-expanded={planOpen}
              onClick={onTogglePlan}
            >
              <Icon name="checklist" /> {tr("chat.plan.title")}
            </button>
          )}
        </ViewHead>
  );
}
