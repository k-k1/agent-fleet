// MCP tool calls the workspace relay holds for a person's confirmation (ADR 0146 decision 6).
// The notification only says "look": the outbox is writable by every agent, so what a card
// shows always comes from GET /api/mcp-elicitations. The answer is the member's own signed
// request; an agent cannot give it.
import { create } from "zustand";
import { api, apiJSON, getTenant, type ApiError } from "../../core/api/client.ts";

/** One choice of a field: the value sent back, and the server's label for it. */
export interface McpElicitOption {
  value: string;
  label: string;
}

/** One choice the server asks for. */
export interface McpElicitField {
  name: string;
  title?: string;
  required?: boolean;
  options: McpElicitOption[];
}

/** One of the server's input requests: its message (the server's text) and its fields. */
export interface McpElicitInput {
  key: string;
  message?: string;
  fields: McpElicitField[];
}

/** One held call. `session` is a label for where to show it, "" when the relay could not tell. */
export interface McpElicitation {
  id: string;
  serverId: string;
  server: string;
  serverLabel?: string;
  /** Where the call went: the scheme, host and port of the server's URL, nothing else. */
  origin?: string;
  tool: string;
  session?: string;
  /** The call's arguments as the exact JSON text the server will run. */
  arguments?: string;
  inputs: McpElicitInput[];
  createdAt: string;
  deadline: string;
}

/** A member's answer: a choice per input key and field name, or a decline. */
export type McpElicitChoices = Record<string, Record<string, string>>;

interface McpElicitState {
  items: McpElicitation[];
  /** Calls answered from this tab: their card stays, disabled, until the list drops them. */
  answering: Record<string, true>;
  refresh(): Promise<void>;
  reset(): void;
  /** Answers a held call; returns the Agent's error, or null on success. */
  answer(e: McpElicitation, choices: McpElicitChoices | null): Promise<ApiError | null>;
}

// Reads can overlap (start, a notification, the poll, an answer): only a read issued after the
// last one applied may replace the list, or a late empty answer would take a held call's card
// away. reset() moves `applied` up so a read from before a tenant switch lands nowhere.
let issued = 0;
let applied = 0;

export const useMcpElicitStore = create<McpElicitState>((set, get) => ({
  items: [],
  answering: {},
  async refresh() {
    const tenant = getTenant();
    const seq = ++issued;
    let d: { elicitations?: McpElicitation[]; error?: unknown } | null = null;
    try {
      d = await api("api/mcp-elicitations");
    } catch {
      return; // a dropped connection proves nothing; the next poll asks again
    }
    if (!d || d.error || !Array.isArray(d.elicitations) || getTenant() !== tenant || seq <= applied) return;
    applied = seq;
    const ids = new Set(d.elicitations.map((e) => e.id));
    set((s) => ({
      items: d!.elicitations!,
      answering: Object.fromEntries(Object.keys(s.answering).filter((id) => ids.has(id)).map((id) => [id, true as const])),
    }));
  },
  reset() {
    applied = issued;
    set({ items: [], answering: {} });
  },
  async answer(e, choices) {
    set((s) => ({ answering: { ...s.answering, [e.id]: true } }));
    const body = choices ? { id: e.id, action: "accept", choices } : { id: e.id, action: "decline" };
    let err: ApiError | null = null;
    try {
      const d = await apiJSON("api/mcp-elicitations/answer", "POST", body);
      if (d?.error) err = typeof d.error === "object" ? d.error : { code: "failed" };
    } catch {
      err = { code: "failed" };
    }
    if (err) {
      set((s) => {
        const { [e.id]: _, ...rest } = s.answering;
        return { answering: rest };
      });
    }
    await get().refresh();
    return err;
  },
}));
