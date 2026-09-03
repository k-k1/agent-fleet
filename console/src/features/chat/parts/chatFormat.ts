import { fmtDateTime } from "../../../lib/intl.ts";

// formatMsgTS renders a unix-millis timestamp as local "MM/DD HH:MM" — same shape as
// MirrorView's turn footer (date kept so a thread that spans days stays unambiguous).
export const formatMsgTS = (ms: number) => fmtDateTime(ms);
