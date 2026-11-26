// MCP tool calls the workspace relay holds for a person's confirmation (ADR 0146 decision 6).
// The notification only says "look": the outbox is writable by every agent, so what a card
// shows always comes from GET /api/mcp-elicitations. The answer is the member's own signed
// request; an agent cannot give it.
import { create } from "zustand";
import { api, apiJSON, getTenant, getUser, type ApiError } from "../../core/api/client.ts";

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
  /** Seconds left until the relay declines the call, as of the Agent's answer. */
  expiresInSec?: number;
}

/** A member's answer: a choice per input key and field name, or a decline. */
export type McpElicitChoices = Record<string, Record<string, string>>;

interface McpElicitState {
  items: McpElicitation[];
  /** When each listed call stops being answerable, on this page's monotonic clock
   *  (performance.now()): the deadline the Agent states is on its own clock, which a browser's
   *  may be far from. */
  until: Map<string, number>;
  /** Calls answered from this tab: their card stays, disabled, until the list drops them. */
  answering: Record<string, true>;
  refresh(): Promise<void>;
  reset(): void;
  /** Answers a held call; returns the Agent's error, or null on success. A call already being
   *  answered from this tab is not sent again: the answer is code "busy". */
  answer(e: McpElicitation, choices: McpElicitChoices | null): Promise<ApiError | null>;
}

// Reads can overlap (start, a notification, the poll, an answer): only a read issued after the
// last one applied may replace the list, or a late empty answer would take a held call's card
// away. reset() moves `applied` up so a read from before a tenant or member switch lands nowhere,
// and a read is applied only while the member and tenant it was issued for are still current.
let issued = 0;
let applied = 0;
const owner = () => getTenant() + "\n" + getUser();

// Calls with an answer on its way, checked and marked before any await: a second press (Send
// and then a toast's close) must not send a second answer.
const sending = new Set<string>();

/** live reports whether e can still be answered at now (performance.now()): past its time the
 *  relay has declined it, even when the list could not be read since (a stopped workspace). */
export function live(until: Map<string, number>, e: McpElicitation, now: number): boolean {
  const t = until.get(e.id);
  return t === undefined || now < t;
}

export const useMcpElicitStore = create<McpElicitState>((set, get) => ({
  items: [],
  until: new Map(),
  answering: {},
  async refresh() {
    const who = owner();
    const seq = ++issued;
    let d: { elicitations?: McpElicitation[]; error?: unknown } | null = null;
    try {
      d = await api("api/mcp-elicitations");
    } catch {
      return; // a dropped connection proves nothing; the next poll asks again
    }
    if (!d || d.error || !Array.isArray(d.elicitations) || owner() !== who || seq <= applied) return;
    applied = seq;
    const at = performance.now();
    const until = new Map<string, number>();
    for (const e of d.elicitations) {
      if (typeof e.expiresInSec === "number") until.set(e.id, at + e.expiresInSec * 1000);
    }
    const ids = new Set(d.elicitations.map((e) => e.id));
    set((s) => ({
      items: d!.elicitations!,
      until,
      answering: Object.fromEntries(Object.keys(s.answering).filter((id) => ids.has(id)).map((id) => [id, true as const])),
    }));
  },
  reset() {
    applied = issued;
    set({ items: [], until: new Map(), answering: {} });
  },
  async answer(e, choices) {
    if (sending.has(e.id)) return { code: "busy" };
    sending.add(e.id);
    set((s) => ({ answering: { ...s.answering, [e.id]: true } }));
    const body = choices ? { id: e.id, action: "accept", choices } : { id: e.id, action: "decline" };
    let err: ApiError | null = null;
    try {
      const d = await apiJSON("api/mcp-elicitations/answer", "POST", body);
      if (d?.error) err = typeof d.error === "object" ? d.error : { code: "failed" };
    } catch {
      err = { code: "failed" };
    }
    sending.delete(e.id);
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
