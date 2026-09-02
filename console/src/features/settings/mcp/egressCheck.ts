// Egress allowlist ↔ MCP registry (docs/log/48 §9, docs/log/20 M3) — the client half.
//
// A remote MCP server is an outbound destination. Where the deployment routes workspace
// egress through the forward proxy, a host that is not on the allowlist either cannot be
// reached (enforce) or works right up until an operator flips enforce (log-only). The CLI
// side of that failure looks like "the MCP server is broken", so the registration screens
// say it here — and offer the one action a non-admin has: ask for the host to be allowed
// (POST /api/egress/propose, which can only ever create a PROPOSED entry).
//
// The pure parts live in this file so the rules that decide WHETHER to warn are unit
// tested rather than only observable in a running browser — the same split as mcpWire.ts.

export interface HostVerdict {
  /** The normalized host the CP answered about (what the proxy would match on). */
  host: string;
  allowed: boolean;
  /** A pending allowlist request already covers this host. */
  proposed: boolean;
}

export interface EgressCheck {
  /**
   * Whether this deployment routes workspace egress through the proxy at all. False on
   * every deployment that has not wired it (the default), and then NOTHING is warned
   * about: a warning about a restriction that does not exist is worse than silence.
   */
  configured: boolean;
  mode: string; // "log-only" | "enforce"
  enforce: boolean;
  /** Keyed by exactly the host string that was asked about. */
  hosts: Record<string, HostVerdict>;
}

/**
 * hostOf extracts the host a URL would be reached at, or "" when there is nothing to
 * check (empty / unparseable / not http(s)). "" always means "say nothing".
 */
export const hostOf = (url?: string): string => {
  const raw = (url || "").trim();
  if (!raw) return "";
  try {
    const u = new URL(raw);
    if (u.protocol !== "http:" && u.protocol !== "https:") return "";
    // URL.hostname keeps IPv6 in brackets; the allowlist stores the bare address.
    return u.hostname.replace(/^\[|\]$/g, "").toLowerCase();
  } catch {
    return "";
  }
};

/** hostsOf collects the distinct hosts of a set of URLs, skipping the unusable ones. */
export const hostsOf = (urls: (string | undefined)[]): string[] => {
  const seen = new Set<string>();
  for (const u of urls) {
    const h = hostOf(u);
    if (h) seen.add(h);
  }
  return [...seen].sort();
};

export const checkQuery = (hosts: string[]): string =>
  hosts.map((h) => "host=" + encodeURIComponent(h)).join("&");

/**
 * What to tell the user about one destination:
 *   none        … nothing to say (no egress control here, allowed, or not yet known)
 *   pending     … a request is already filed; the action now is to wait
 *   would_block … not allowed, but the deployment is log-only, so it works TODAY
 *   blocked     … not allowed and enforced: it will not connect
 *
 * An unknown host (no verdict yet, or the check failed) is deliberately "none". Guessing
 * that a host is blocked because the check did not answer would make every CP hiccup look
 * like a policy problem.
 */
export type EgressLevel = "none" | "pending" | "would_block" | "blocked";

export const egressLevel = (check: EgressCheck | null | undefined, host: string): EgressLevel => {
  if (!check || !check.configured || !host) return "none";
  const v = check.hosts?.[host];
  if (!v || v.allowed) return "none";
  if (v.proposed) return "pending";
  return check.enforce ? "blocked" : "would_block";
};
