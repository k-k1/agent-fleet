// TCP passthrough entries of the egress allowlist (ADR 0122, addendum 2026-11-05).
//
// An entry `tcp:<host>:<port>` or `tcp:<host>:<from>-<to>` names a destination for non-HTTP
// traffic (SSH git, a database). The Control Plane validates it again on every route that
// accepts one (control-plane/internal/egressallow/tcp.go ParseTCPEntry); the rules below are
// the same ones, so the form can say what is wrong before the request and never disagrees
// with the server about what is valid. A change to one needs the same change in the other.

export const TCP_PREFIX = "tcp:";
/** The widest range one entry may cover (egressallow.MaxTCPRange). */
export const MAX_TCP_RANGE = 2048;
/** Ports that stay on the recorded HTTP / DNS path (egressallow tcpRefusedPorts). */
export const TCP_REFUSED_PORTS = [53, 80, 443, 853, 3128];

export type TcpProblem =
  | "format" // not host + ports
  | "host" // not an exact host name
  | "ipv4" // an address that can never be a destination, or IPv6
  | "port" // not a number, or outside 1-65535, or reversed
  | "width" // a range wider than MAX_TCP_RANGE
  | "refused"; // contains a refused port

export type TcpParsed =
  | { ok: true; host: string; from: number; to: number; entry: string }
  | { ok: false; problem: TcpProblem; port?: number };

export const isTcpEntry = (s: string): boolean => s.trim().toLowerCase().startsWith(TCP_PREFIX);

// A dotted quad that Go's netip.ParseAddr accepts: four decimal fields, 0-255, no leading zeros.
function parseIPv4(s: string): number[] | null {
  const f = s.split(".");
  if (f.length !== 4) return null;
  const out: number[] = [];
  for (const p of f) {
    if (!/^(0|[1-9][0-9]{0,2})$/.test(p) || Number(p) > 255) return null;
    out.push(Number(p));
  }
  return out;
}

const HOST_RE = /^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$/;

/** formatTcpEntry is the canonical spelling the CP stores. */
export const formatTcpEntry = (host: string, from: number, to: number): string =>
  TCP_PREFIX + host + ":" + (from === to ? String(from) : from + "-" + to);

/**
 * parseTcpEntry validates a whole entry string; parseTcpParts takes the host and the
 * port/range text as the form holds them.
 */
export function parseTcpEntry(raw: string): TcpParsed {
  const s = raw.trim().toLowerCase();
  if (!s.startsWith(TCP_PREFIX)) return { ok: false, problem: "format" };
  const rest = s.slice(TCP_PREFIX.length);
  const i = rest.lastIndexOf(":");
  if (i <= 0 || i === rest.length - 1) return { ok: false, problem: "format" };
  return parseTcpParts(rest.slice(0, i), rest.slice(i + 1));
}

export function parseTcpParts(hostText: string, portText: string): TcpParsed {
  const host = hostText.trim().toLowerCase().replace(/\.$/, "");
  const ports = portText.trim();
  if (!host || !ports) return { ok: false, problem: "format" };
  const ip = parseIPv4(host);
  if (ip) {
    if (ip[0] === 0 || ip[0] === 127 || (ip[0] === 169 && ip[1] === 254) || ip[0] >= 224 && ip[0] <= 239) {
      return { ok: false, problem: "ipv4" };
    }
  } else if (host.includes(":")) {
    return { ok: false, problem: "ipv4" }; // IPv6: the relay is IPv4-only
  } else if (!HOST_RE.test(host) || host.includes("..") || host.length > 253) {
    return { ok: false, problem: "host" };
  }
  const [lo, hi, extra] = ports.split("-");
  const isRange = hi !== undefined;
  const canon = (t: string) => /^[0-9]+$/.test(t) && String(Number(t)) === t;
  if (extra !== undefined || !canon(lo) || (isRange && !canon(hi))) return { ok: false, problem: "port" };
  const from = Number(lo);
  const to = isRange ? Number(hi) : from;
  if (from < 1 || to > 65535 || from > to) return { ok: false, problem: "port" };
  if (to - from + 1 > MAX_TCP_RANGE) return { ok: false, problem: "width" };
  for (const p of TCP_REFUSED_PORTS) {
    if (from <= p && p <= to) return { ok: false, problem: "refused", port: p };
  }
  return { ok: true, host, from, to, entry: formatTcpEntry(host, from, to) };
}

/**
 * tcpEntryParts splits a stored entry for display. It is lenient on purpose: a row the CP
 * holds is shown even if it would not validate here.
 */
export function tcpEntryParts(entry: string): { host: string; ports: string } | null {
  if (!isTcpEntry(entry)) return null;
  const rest = entry.trim().slice(TCP_PREFIX.length);
  const i = rest.lastIndexOf(":");
  if (i <= 0) return null;
  return { host: rest.slice(0, i), ports: rest.slice(i + 1) };
}
