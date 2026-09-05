import { useEffect, useState } from "react";
import type { FormEvent } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { AGENTS } from "../../agents/registry.ts";
import { SESSION_KINDS } from "../../types/session.ts";
import type { SessionKind } from "../../types/session.ts";
import type { Assistant, AssistantInput, ToolGrant } from "../../types/assistant.ts";
import { api } from "../../core/api/client.ts";
import type { McpServer } from "../settings/mcpWire.ts";
import { readerVoiceChoices } from "./tts.ts";
import { loadSpeakers } from "./ttsSpeakers.ts";
import { useT, type MsgKey } from "../../lib/i18n/index.ts";

// AssistantModal creates or edits a user-defined assistant template (docs/log/19 Q2):
// name, backend agent, optional model, a persona (system prompt), a tool grant, and
// optional knowledge dirs. Builtins are never edited here (the section hides edit for
// them), so this form always drives a user assistant.

// Chat-capable agent kinds, data-driven off the registry cap (same source as the New
// Chat picker) — codex lights up automatically once headlessChat flips (Phase A.2).
const chatKinds: SessionKind[] = SESSION_KINDS.filter((k) => AGENTS[k].caps.headlessChat);

// A small palette of codicons a user can tag an assistant with (kept short on purpose).
const ICONS = ["comment-discussion", "sparkle", "rocket", "globe", "book", "beaker", "heart", "star-full"];

const TOOLS: { value: ToolGrant; labelKey: MsgKey; helpKey: MsgKey }[] = [
  { value: "none", labelKey: "asst.tool_none", helpKey: "asst.tool_none_help" },
  { value: "af_read", labelKey: "asst.tool_read", helpKey: "asst.tool_read_help" },
  { value: "af_write", labelKey: "asst.tool_write", helpKey: "asst.tool_write_help" },
];

interface AssistantModalProps {
  initial?: Assistant | null; // present = edit; absent = create
  onClose: () => void;
  onSave: (input: AssistantInput) => Promise<void>;
}

export function AssistantModal({ initial, onClose, onSave }: AssistantModalProps) {
  const tr = useT();
  const [name, setName] = useState(initial?.name ?? "");
  const [description, setDescription] = useState(initial?.description ?? "");
  const [icon, setIcon] = useState(initial?.icon ?? ICONS[0]);
  const [agent, setAgent] = useState<SessionKind>(initial?.agent ?? chatKinds[0] ?? "claude");
  const [model, setModel] = useState(initial?.model ?? "");
  const [persona, setPersona] = useState(initial?.persona ?? "");
  const [tools, setTools] = useState<ToolGrant>(initial?.tools ?? "none");
  const [knowledge, setKnowledge] = useState((initial?.knowledge ?? []).join("\n"));
  const [voice, setVoice] = useState(initial?.voice ?? "");
  const [busy, setBusy] = useState(false);
  // Attachable MCP servers come from the EFFECTIVE registry (docs/log/48 §7): the builtin
  // ops integrations, the user's own registrations, and anything the tenant
  // distributes — one list, exactly what the chat will actually resolve. null = still
  // loading, so an empty registry and a pending fetch don't look alike.
  const [mcp, setMcp] = useState<McpServer[] | null>(null);
  const [integrations, setIntegrations] = useState<string[]>(initial?.integrations ?? []);
  useEffect(() => {
    let alive = true;
    void api("api/mcp-servers")
      .then((d) => {
        if (!alive) return;
        const servers: McpServer[] = d && !d.error && Array.isArray(d.servers) ? d.servers : [];
        setMcp(servers.filter((s) => s.targets?.assistant));
      })
      .catch(() => alive && setMcp([]));
    return () => {
      alive = false;
    };
  }, []);
  const toggleIntegration = (id: string) =>
    setIntegrations((ids) => (ids.includes(id) ? ids.filter((x) => x !== id) : [...ids, id]));
  // Voice choices come from the character settings crossed with the engine's live catalog
  // (tts.ts); re-render once it arrives.
  const [, setCatalogLoaded] = useState(false);
  useEffect(() => {
    let alive = true;
    void loadSpeakers().then((l) => alive && l && setCatalogLoaded(true));
    return () => {
      alive = false;
    };
  }, []);
  // Re-label the leading "speaker from settings" entry as "auto" — an assistant's "" means
  // it is assigned automatically from the pool.
  const voiceChoices: [string, string][] = [["", tr("asst.voice_auto")], ...readerVoiceChoices().slice(1)];
  if (voice && !voiceChoices.some(([v]) => v === voice)) voiceChoices.push([voice, voice]); // show a saved value from outside the pool too

  const canSubmit = name.trim().length > 0 && !busy;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!canSubmit) return;
    setBusy(true);
    try {
      await onSave({
        name: name.trim(),
        description: description.trim(),
        icon,
        agent,
        model: model.trim(),
        persona: persona.trim(),
        tools,
        knowledge: knowledge
          .split("\n")
          .map((s) => s.trim())
          .filter(Boolean),
        integrations,
        voice,
      });
      onClose();
    } catch {
      // Save failed (onSave owns the toast); the modal stays open so nothing typed is lost.
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      title={initial ? tr("asst.edit_assistant") : tr("asst.create_assistant")}
      onClose={onClose}
      className="session-modal"
      as="form"
      onSubmit={submit}
      lockClose={busy}
    >
      <div className="ui-modal-body">
        <div className="ui-field">
          <div className="ui-field-label">{tr("asst.name")}</div>
          <label className="ui-field">
            <input value={name} onChange={(e) => setName(e.target.value)} placeholder={tr("asst.name_ph")} autoFocus />
          </label>
        </div>

        <div className="ui-field">
          <div className="ui-field-label">{tr("asst.desc_label")}</div>
          <div className="ui-field-hint">{tr("asst.desc_hint")}</div>
          <label className="ui-field">
            <textarea
              className="assistant-persona"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              rows={2}
              placeholder={tr("asst.desc_ph")}
            />
          </label>
        </div>

        <div className="ui-field">
          <div className="ui-field-label">{tr("asst.icon")}</div>
          <div className="assistant-icon-pick">
            {ICONS.map((ic) => (
              <button
                key={ic}
                type="button"
                className={"icon assistant-icon-opt" + (icon === ic ? " active" : "")}
                title={ic}
                onClick={() => setIcon(ic)}
              >
                <Icon name={ic} />
              </button>
            ))}
          </div>
        </div>

        {chatKinds.length > 1 && (
          <div className="ui-field">
            <div className="ui-field-label">{tr("asst.agent")}</div>
            <div className="ui-seg">
              {chatKinds.map((k) => (
                <button
                  key={k}
                  type="button"
                  className={"seg-btn" + (agent === k ? " active" : "")}
                  onClick={() => setAgent(k)}
                >
                  {AGENTS[k].assistantName}
                </button>
              ))}
            </div>
          </div>
        )}

        <div className="ui-field">
          <div className="ui-field-label">{tr("asst.model")}</div>
          <label className="ui-field">
            <input value={model} onChange={(e) => setModel(e.target.value)} placeholder={tr("asst.model_ph")} />
          </label>
        </div>

        <div className="ui-field">
          <div className="ui-field-label">{tr("asst.voice_label")}</div>
          <div className="ui-field-hint">{tr("asst.voice_hint")}</div>
          <label className="ui-field">
            <select value={voice} onChange={(e) => setVoice(e.target.value)}>
              {voiceChoices.map(([v, label]) => (
                <option key={v} value={v}>
                  {label}
                </option>
              ))}
            </select>
          </label>
        </div>

        <div className="ui-field">
          <div className="ui-field-label">{tr("asst.persona_label")}</div>
          <div className="ui-field-hint">{tr("asst.persona_hint")}</div>
          <label className="ui-field">
            <textarea
              className="assistant-persona"
              value={persona}
              onChange={(e) => setPersona(e.target.value)}
              rows={5}
              placeholder={tr("asst.persona_ph")}
            />
          </label>
        </div>

        <div className="ui-field">
          <div className="ui-field-label">{tr("asst.tools_label")}</div>
          <div className="ui-seg">
            {TOOLS.map((t) => (
              <button
                key={t.value}
                type="button"
                className={"seg-btn" + (tools === t.value ? " active" : "")}
                onClick={() => setTools(t.value)}
              >
                {tr(t.labelKey)}
              </button>
            ))}
          </div>
          <div className="ui-field-hint">{tr(TOOLS.find((t) => t.value === tools)?.helpKey ?? "asst.tool_none_help")}</div>
        </div>

        <div className="ui-field">
          <div className="ui-field-label">{tr("asst.mcp_label")}</div>
          <div className="ui-field-hint">{tr("asst.mcp_hint")}</div>
          {mcp !== null && mcp.length === 0 && <div className="ui-field-hint">{tr("asst.mcp_empty")}</div>}
          {mcp?.map((s) => {
            // A definition scoped away from this backend, or still missing a value it
            // needs, is shown but flagged: it stays selectable (the scope or the
            // credential can be fixed later) and simply won't attach until then.
            const scoped = !s.kinds?.length || s.kinds.includes(agent);
            return (
              <label key={s.id} className="assistant-mcp-opt">
                <input
                  type="checkbox"
                  checked={integrations.includes(s.id)}
                  onChange={() => toggleIntegration(s.id)}
                />
                <span className="assistant-mcp-name">{s.label || s.name}</span>
                {!s.enabled && <span className="ui-field-hint">{tr("asst.mcp_disabled")}</span>}
                {s.enabled && !s.ready && <span className="ui-field-hint">{tr("asst.mcp_not_ready")}</span>}
                {s.enabled && s.ready && !scoped && (
                  <span className="ui-field-hint">{tr("asst.mcp_out_of_scope", { agent: AGENTS[agent].assistantName })}</span>
                )}
              </label>
            );
          })}
        </div>

        <div className="ui-field">
          <div className="ui-field-label">{tr("asst.knowledge_label")}</div>
          <div className="ui-field-hint">{tr("asst.knowledge_hint")}</div>
          <label className="ui-field">
            <textarea
              className="assistant-knowledge"
              value={knowledge}
              onChange={(e) => setKnowledge(e.target.value)}
              rows={2}
              placeholder="/home/dev/repos/my-repo/docs"
            />
          </label>
        </div>
      </div>

      <footer className="ui-modal-foot">
        <button type="button" className="ui-btn ui-btn-ghost" onClick={onClose}>
          {tr("asst.cancel")}
        </button>
        <button type="submit" className="ui-btn ui-btn-primary" disabled={!canSubmit}>
          {initial ? tr("asst.save") : tr("asst.create")}
        </button>
      </footer>
    </Modal>
  );
}
