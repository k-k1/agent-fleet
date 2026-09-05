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
// work-plan toggle. It owns no state — the picker's open flag, its dismiss wiring and its
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
          {/* The agent chip. On an existing conversation it doubles as the button that opens the
              switch picker; a draft has no conversation yet, so it is display-only. */}
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
                    // Pinning to an unconnected CLI has no effect: on send, chatProviderFor
                    // just falls back to a connected backend. When connection state is
                    // unknown (cold cache) do not block the choice.
                    disabled={!!chatConns && !agentOf(k).available({ conns: chatConns })}
                    title={
                      chatConns && !agentOf(k).available({ conns: chatConns })
                        ? tr("chat.switch_agent_offline")
                        : undefined
                    }
                    onClick={() => onSwitchAgent(k)}
                  >
                    <Icon name={k === conv?.agent ? "check" : "blank"} />
                    {/* --kind-* in tokens.css is the single source for kind colors. */}
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
          {/* Work plan (docs/log/33 stage 5): opens and closes the box that is carried verbatim
              across a compaction. A conversation that holds a plan is tinted, so it is obvious at
              a glance which content the assistant will never forget. */}
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
