import { useT } from "../../../lib/i18n/index.ts";
import type { MsgKey } from "../../../lib/i18n/index.ts";

// Lists longer than this collapse: a server that grants every read scope of its API (well over a
// hundred) otherwise turns one row of the MCP list into a screenful of scope names.
export const SCOPE_INLINE_MAX = 12;

// One OAuth scope list on an MCP server row. Short lists stay on one line as "<label>: a b c";
// long ones become a closed disclosure whose summary carries the count, opened to read them all.
export function ScopeList({
  scopes,
  inlineKey,
  summaryKey,
  className,
  monoClassName,
}: {
  scopes: string[];
  inlineKey: MsgKey; // takes {scopes}
  summaryKey: MsgKey; // takes {n}
  className?: string;
  monoClassName?: string; // for the element that holds the scope names themselves
}) {
  const tr = useT();
  if (scopes.length <= SCOPE_INLINE_MAX) {
    return <div className={`mono ${monoClassName || ""} ${className || ""}`.replace(/ +/g, " ").trim()}>{tr(inlineKey, { scopes: scopes.join(" ") })}</div>;
  }
  return (
    <details className={`mcp-scopes ${className || ""}`.trim()} data-testid="mcp-scope-list">
      <summary>{tr(summaryKey, { n: scopes.length })}</summary>
      <div className={`mono mcp-scopes-mono ${monoClassName || ""}`.trim()}>{scopes.join(" ")}</div>
    </details>
  );
}
