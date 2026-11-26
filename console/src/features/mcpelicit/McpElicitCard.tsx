import { useId, useState } from "react";
import { errText } from "../../core/api/client.ts";
import { useT } from "../../lib/i18n/index.ts";
import { Trans } from "../../lib/i18n/Trans.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { SessionLink } from "../awsconfirm/AwsConfirmCard.tsx";
import { indentJSON, optionTexts } from "./format.ts";
import { useMcpElicitStore, type McpElicitChoices, type McpElicitation } from "./store.ts";
import "./mcpElicit.css";

/** complete reports whether choices answer every required field of e. */
export function complete(e: McpElicitation, choices: McpElicitChoices): boolean {
  return e.inputs.every((input) => input.fields.every((f) => !f.required || choices[input.key]?.[f.name] !== undefined));
}

/** McpElicitCard asks the member to confirm a tool call an MCP server holds for a person (ADR
 *  0146 decision 6). Everything it shows comes from the Agent's list; the tool and server names go
 *  in as elements, never into the catalogue line, so no name can pass for the line's markup. */
export function McpElicitCard({ e: given, showSession }: { e: McpElicitation; showSession?: boolean }) {
  const tr = useT();
  const toast = useToast();
  // A toast keeps the element it was given: read the latest list for this id.
  const e = useMcpElicitStore((s) => s.items.find((x) => x.id === given.id)) ?? given;
  const answer = useMcpElicitStore((s) => s.answer);
  const busy = useMcpElicitStore((s) => !!s.answering[e.id]);
  const [choices, setChoices] = useState<McpElicitChoices>({});
  // Radio groups are document-wide outside a form: two cards of one call (a split pane) must not
  // share a group.
  const group = useId();
  const server = e.serverLabel || e.server;
  const pick = (key: string, field: string, value: string | undefined) =>
    setChoices((c) => {
      const fields = { ...c[key] };
      if (value === undefined) delete fields[field];
      else fields[field] = value;
      return { ...c, [key]: fields };
    });
  const go = async (accept: boolean) => {
    // An accept names every input, even one whose fields are all optional and left unchosen.
    const sent = accept ? Object.fromEntries(e.inputs.map((input) => [input.key, choices[input.key] ?? {}])) : null;
    const err = await answer(e, sent);
    if (err) toast(tr("mcpelicit.answer_failed", { reason: errText(err) }));
  };
  const deadline = new Date(e.deadline);
  const tool = <b className="mcp-elicit-name">{e.tool}</b>;
  const host = <b className="mcp-elicit-name">{server}</b>;
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
      {e.arguments ? (
        <div className="mcp-elicit-section">
          <div className="mcp-elicit-sub">{tr("mcpelicit.arguments")}</div>
          <pre className="mcp-elicit-args">{indentJSON(e.arguments)}</pre>
        </div>
      ) : null}
      {e.inputs.map((input) => (
        <div key={input.key} className="mcp-elicit-section">
          {input.message ? <div className="mcp-elicit-message">{input.message}</div> : null}
          {input.fields.map((f) => {
            const texts = optionTexts(f);
            const chosen = choices[input.key]?.[f.name];
            const name = `${group}:${input.key}:${f.name}`;
            return (
              <fieldset key={f.name} className="mcp-elicit-field" disabled={busy}>
                <legend>
                  {f.title || f.name}
                  {f.required ? null : <span className="mcp-elicit-sub"> {tr("mcpelicit.optional")}</span>}
                </legend>
                {f.options.map((o, i) => (
                  <label key={o.value}>
                    <input type="radio" name={name} checked={chosen === o.value} onChange={() => pick(input.key, f.name, o.value)} />
                    {texts[i]}
                  </label>
                ))}
                {f.required ? null : (
                  <label>
                    <input type="radio" name={name} checked={chosen === undefined} onChange={() => pick(input.key, f.name, undefined)} />
                    {tr("mcpelicit.no_choice")}
                  </label>
                )}
              </fieldset>
            );
          })}
        </div>
      ))}
      <div className="mcp-elicit-sub">
        {tr("mcpelicit.deadline", { time: isNaN(deadline.getTime()) ? "" : deadline.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }) })}
      </div>
      <div className="mcp-elicit-actions">
        <button type="button" className="primary" disabled={busy || !complete(e, choices)} onClick={() => void go(true)}>
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
 *  the composer. */
export function MirrorElicitCards({ session }: { session: string }) {
  const items = useMcpElicitStore((s) => s.items);
  const answering = useMcpElicitStore((s) => s.answering);
  const mine = items.filter((e) => e.session === session && !answering[e.id]);
  if (!mine.length) return null;
  return (
    <div className="mcp-elicit-mirror">
      {mine.map((e) => (
        <McpElicitCard key={e.id} e={e} />
      ))}
    </div>
  );
}
