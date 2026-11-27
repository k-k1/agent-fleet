import { useEffect, useId, useState } from "react";
import { errText } from "../../core/api/client.ts";
import { useT } from "../../lib/i18n/index.ts";
import { Trans } from "../../lib/i18n/Trans.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { SessionLink } from "../awsconfirm/AwsConfirmCard.tsx";
import { indentJSON, optionTexts, visible } from "./format.ts";
import { useTenantStore } from "../../core/store/tenant.ts";
import { live, ownerKey, useMcpElicitStore, type McpElicitChoices, type McpElicitation } from "./store.ts";
import "./mcpElicit.css";

/** Chosen is the card's answer so far: input key → field name → value. Maps, because the keys
 *  are the server's and a plain object would read "constructor" or "__proto__" off its prototype. */
export type Chosen = Map<string, Map<string, string>>;

/** complete reports whether chosen answers every required field of e. */
export function complete(e: McpElicitation, chosen: Chosen): boolean {
  return e.inputs.every((input) => input.fields.every((f) => !f.required || !!chosen.get(input.key)?.has(f.name)));
}

/** toChoices is the answer as the route takes it: every input named, even one whose fields are
 *  all optional and left unchosen. Object.fromEntries defines own members, so no key reaches a
 *  prototype. */
export function toChoices(e: McpElicitation, chosen: Chosen): McpElicitChoices {
  return Object.fromEntries(e.inputs.map((input) => [input.key, Object.fromEntries(chosen.get(input.key) ?? [])]));
}

const NO_CALLS: McpElicitation[] = [];

/** useHeld is the list as the member signed in now may see it: nothing while the tenant is not
 *  settled yet, or while the list was read for another tenant or member. It re-renders on a
 *  switch of either, before anything resets the list, so a card never shows one member's call
 *  to another. */
export function useHeld(): McpElicitation[] {
  useTenantStore((s) => s.tenant);
  useTenantStore((s) => s.identityRev);
  const resolved = useTenantStore((s) => s.resolved);
  const items = useMcpElicitStore((s) => s.items);
  const owner = useMcpElicitStore((s) => s.owner);
  return resolved && owner === ownerKey() ? items : NO_CALLS;
}

const TICK_MS = 5000;

/** useNow is performance.now(), renewed every few seconds while active, so a card goes once its
 *  time is up (live). */
export function useNow(active: boolean): number {
  const [now, setNow] = useState(() => performance.now());
  useEffect(() => {
    if (!active) return;
    setNow(performance.now());
    const t = window.setInterval(() => setNow(performance.now()), TICK_MS);
    return () => clearInterval(t);
  }, [active]);
  return now;
}

/** McpElicitCard asks the member to confirm a tool call an MCP server holds for a person (ADR
 *  0146 decision 6). Everything it shows comes from the Agent's list, with hidden characters
 *  marked; the tool and server names go in as elements, never into the catalogue line, so no
 *  name can pass for the line's markup. A card whose call has left the list shows nothing. */
export function McpElicitCard({ id, showSession }: { id: string; showSession?: boolean }) {
  const e = useHeld().find((x) => x.id === id);
  if (!e) return null;
  return <CardBody e={e} showSession={showSession} />;
}

function CardBody({ e, showSession }: { e: McpElicitation; showSession?: boolean }) {
  const tr = useT();
  const toast = useToast();
  const answer = useMcpElicitStore((s) => s.answer);
  const busy = useMcpElicitStore((s) => Object.hasOwn(s.answering, e.id));
  const until = useMcpElicitStore((s) => s.until.get(e.id));
  const [chosen, setChosen] = useState<Chosen>(() => new Map());
  // Radio groups are document-wide outside a form: each field gets its own group, by position,
  // and two cards of one call (a split pane) get different prefixes.
  const group = useId();
  const pick = (key: string, field: string, value: string | undefined) =>
    setChosen((c) => {
      const next = new Map(c);
      const fields = new Map(c.get(key) ?? []);
      if (value === undefined) fields.delete(field);
      else fields.set(field, value);
      next.set(key, fields);
      return next;
    });
  const go = async (accept: boolean) => {
    const who = ownerKey();
    const err = await answer(e, accept ? toChoices(e, chosen) : null);
    if (ownerKey() !== who) return;
    // busy and stale: an answer is already on its way, or the card is a leftover; nothing new.
    if (err && err.code !== "busy" && err.code !== "stale") toast(tr("mcpelicit.answer_failed", { reason: errText(err) }));
  };
  // The time on this browser's clock: the Agent's own deadline may be on a clock far from it.
  // An Agent from before the list carried the arguments as text sends them as JSON (and no
  // expiresInMs, which came with the text): shown that way they could not be told from what the
  // server runs (a member written twice, a long number, a string that reads as an object), so
  // such a call can only be declined here.
  const argsUnreadable = e.arguments !== undefined && (typeof e.arguments !== "string" || typeof e.expiresInMs !== "number");
  const deadline = until === undefined ? new Date(NaN) : new Date(Date.now() + until - performance.now());
  const tool = <b className="mcp-elicit-name">{visible(e.tool)}</b>;
  const host = (
    <b className="mcp-elicit-name">
      {visible(e.serverLabel || e.server)}
      {e.origin ? <span className="mcp-elicit-origin"> ({visible(e.origin)})</span> : null}
    </b>
  );
  return (
    <div className="mcp-elicit" role="alert">
      <div className="mcp-elicit-head">
        <Icon name="shield" />
        <span>
          {showSession && e.session ? (
            <Trans k="mcpelicit.title_session" vars={{ session: e.session }} components={[<SessionLink name={e.session} />, tool, host]} />
          ) : (
            <Trans k={showSession ? "mcpelicit.title_unattributed" : "mcpelicit.title"} components={[tool, host]} />
          )}
        </span>
      </div>
      {argsUnreadable ? (
        <div className="mcp-elicit-sub">{tr("mcpelicit.arguments_unreadable")}</div>
      ) : e.arguments ? (
        <div className="mcp-elicit-section">
          <div className="mcp-elicit-sub">{tr("mcpelicit.arguments")}</div>
          <pre className="mcp-elicit-args">{indentJSON(e.arguments)}</pre>
        </div>
      ) : null}
      {e.inputs.map((input, ii) => (
        <div key={input.key} className="mcp-elicit-section">
          {input.message ? <div className="mcp-elicit-message">{visible(input.message)}</div> : null}
          {input.fields.map((f, fi) => {
            const texts = optionTexts(f);
            const chosenValue = chosen.get(input.key)?.get(f.name);
            const name = `${group}-${ii}-${fi}`;
            return (
              <fieldset key={f.name} className="mcp-elicit-field" disabled={busy}>
                <legend>
                  {visible(f.title || f.name)}
                  {f.required ? null : <span className="mcp-elicit-sub"> {tr("mcpelicit.optional")}</span>}
                  {/* Taking back an optional choice is a button, not one more option: an option
                      could carry any label, this one's included. A legend's controls are outside
                      its fieldset's disabled state, so it is disabled on its own. */}
                  {!f.required && chosenValue !== undefined ? (
                    <button
                      type="button"
                      className="linklike mcp-elicit-clear"
                      disabled={busy}
                      onClick={() => pick(input.key, f.name, undefined)}
                    >
                      {tr("mcpelicit.clear_choice")}
                    </button>
                  ) : null}
                </legend>
                {f.options.map((o, i) => (
                  <label key={o.value}>
                    <input type="radio" name={name} checked={chosenValue === o.value} onChange={() => pick(input.key, f.name, o.value)} />
                    {texts[i]}
                  </label>
                ))}
              </fieldset>
            );
          })}
        </div>
      ))}
      <div className="mcp-elicit-sub">
        {tr("mcpelicit.deadline", { time: isNaN(deadline.getTime()) ? "" : deadline.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }) })}
      </div>
      <div className="mcp-elicit-actions">
        <button type="button" className="primary" disabled={busy || argsUnreadable || !complete(e, chosen)} onClick={() => void go(true)}>
          {tr("mcpelicit.send")}
        </button>
        <button type="button" className="ghost" disabled={busy} onClick={() => void go(false)}>
          {tr("mcpelicit.decline")}
        </button>
      </div>
    </div>
  );
}

/** MirrorElicitCards shows the held calls attributed to the session whose mirror this is, above
 *  the composer. A call being answered stays, disabled, so a failed answer keeps the choices. */
export function MirrorElicitCards({ session }: { session: string }) {
  const items = useHeld();
  const until = useMcpElicitStore((s) => s.until);
  const now = useNow(items.length > 0);
  const mine = items.filter((e) => e.session === session && live(until, e, now));
  if (!mine.length) return null;
  return (
    <div className="mcp-elicit-mirror">
      {mine.map((e) => (
        <McpElicitCard key={e.id} id={e.id} />
      ))}
    </div>
  );
}
