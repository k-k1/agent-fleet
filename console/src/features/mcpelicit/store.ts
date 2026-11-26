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
  /** Milliseconds left until the relay declines the call, as of the Agent's answer. */
  expiresInMs?: number;
}

/** A member's answer: a choice per input key and field name, or a decline. */
export type McpElicitChoices = Record<string, Record<string, string>>;

interface McpElicitState {
  items: McpElicitation[];
  /** The tenant and member the list was read for (ownerKey). Only they may see or answer it. */
  owner: string;
  /** When each listed call stops being answerable, on this page's monotonic clock
   *  (performance.now()): the deadline the Agent states is on its own clock, which a browser's
   *  may be far from. */
  until: Map<string, number>;
  /** Calls answered from this tab: their card stays, disabled, until the answer is settled. */
  answering: Record<string, true>;
  refresh(): Promise<void>;
  reset(): void;
  /** Answers a held call; returns the Agent's error, or null on success. A call already
   *  answered or being answered from this tab is not sent again (code "busy"), nor is a list
   *  read for another tenant or member (code "stale"). */
  answer(e: McpElicitation, choices: McpElicitChoices | null): Promise<ApiError | null>;
}

/** ownerKey names the tenant and member signed in now. */
export const ownerKey = () => getTenant() + "\n" + getUser();

// Reads can overlap (start, a notification, the poll, an answer): only a read issued after the
// last one applied may replace the list, or a late empty answer would take a held call's card
// away. reset() moves `applied` up so a read from before a tenant or member switch lands nowhere,
// and a read is applied only while the member and tenant it was issued for are still current.
let issued = 0;
let applied = 0;

// Calls with an answer on its way or given, marked before any await: a second press (Send and
// then a toast's close) must not send a second answer. An answered call stays here, and in
// `answered` with the last read issued before its answer was taken, so an older read that
// still lists it cannot bring its card back.
const sending = new Set<string>();
const answered = new Map<string, number>();

/** isSending reports whether an answer to the call is on its way or given from this tab. */
export const isSending = (id: string) => sending.has(id);

/** live reports whether e can still be answered at now (performance.now()): past its time the
 *  relay has declined it, even when the list could not be read since (a stopped workspace). */
export function live(until: Map<string, number>, e: McpElicitation, now: number): boolean {
  const t = until.get(e.id);
  return t === undefined || now < t;
}

export const useMcpElicitStore = create<McpElicitState>((set, get) => ({
  items: [],
  owner: "",
  until: new Map(),
  answering: {},
  async refresh() {
    const who = ownerKey();
    const seq = ++issued;
    // Time left counts from when the read was sent: the call may have aged while it travelled.
    const sent = performance.now();
    let d: { elicitations?: McpElicitation[]; error?: unknown } | null = null;
    try {
      d = await api("api/mcp-elicitations");
    } catch {
      return; // a dropped connection proves nothing; the next poll asks again
    }
    if (!d || d.error || !Array.isArray(d.elicitations) || ownerKey() !== who || seq <= applied) return;
    applied = seq;
    for (const [id, at] of answered) {
      if (seq > at) {
        answered.delete(id); // a read sent after the answer: the Agent no longer lists it
        sending.delete(id);
      }
    }
    const items = d.elicitations.filter((e) => !answered.has(e.id));
    const until = new Map<string, number>();
    for (const e of items) {
      if (typeof e.expiresInMs === "number") until.set(e.id, sent + e.expiresInMs);
    }
    const ids = new Set(items.map((e) => e.id));
    set((s) => ({
      items,
      owner: who,
      until,
      answering: Object.fromEntries(Object.keys(s.answering).filter((id) => ids.has(id)).map((id) => [id, true as const])),
    }));
  },
  reset() {
    applied = issued;
    sending.clear();
    answered.clear();
    set({ items: [], owner: ownerKey(), until: new Map(), answering: {} });
  },
  async answer(e, choices) {
    if (get().owner !== ownerKey()) return { code: "stale" };
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
    if (err) {
      sending.delete(e.id);
      set((s) => {
        const { [e.id]: _, ...rest } = s.answering;
        return { answering: rest };
      });
    } else {
      // Taken: the card goes now, and no read sent before this point brings it back.
      answered.set(e.id, issued);
      set((s) => ({ items: s.items.filter((x) => x.id !== e.id) }));
    }
    await get().refresh();
    return err;
  },
}));
