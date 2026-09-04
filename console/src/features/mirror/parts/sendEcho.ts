// Optimistic send echoes (shown as "awaiting delivery"), stashed per session at module level.
// MirrorView unmounts on a chat-to-terminal switch, so keeping them only in component state made a
// just-sent (or worse, never-delivered) message vanish from the chat on return. They are
// restored on mount and removed exactly as before — when the real turn lands or the POST
// fails. The id counter is module-level for the same reason: a remount must not reissue
// ids still held by stashed echoes.
import type { PendingEcho } from "../pendingEcho.ts";

export type SendEcho = PendingEcho & { id: number };

export const echoStore = new Map<string, SendEcho[]>();

let echoSeqCounter = 0;

/** Next echo id. The counter is module-scoped, so a remount cannot collide with stashed ids. */
export const nextEchoId = (): number => ++echoSeqCounter;
