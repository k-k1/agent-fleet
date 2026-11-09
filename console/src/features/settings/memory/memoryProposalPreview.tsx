// The "if you approve" preview of one consolidator proposal (#3720). A member deciding on a
// proposal has three questions — what happens to the original memory, what is added, how is it
// changed — and the item list answered none of them. Everything here is derived from the
// proposal's own get route (items, targetRevisions, itemViews[].diff); it asks the Agent for
// nothing and writes nothing.
//
// Bodies are untrusted text (a model drafted them from observations). They reach the DOM only
// through React text nodes or MarkdownView, which sanitizes its HTML; there is no
// dangerouslySetInnerHTML in this file.

import { useT, tMaybe } from "../../../lib/i18n/index.ts";
import { MarkdownView } from "../../viewer/MarkdownView.tsx";
import type { ConsolItem, ConsolItemView, ConsolProposal, ConsolProposalView } from "./memoryTypes.ts";

type TFn = ReturnType<typeof useT>;

/** The items the Agent can publish: the ones Approve would write. */
const writes = (iv: Pick<ConsolItem, "op">): boolean => iv.op === "create" || iv.op === "patch";

/**
 * What the proposal is ABOUT, for its title: the created name or the patched target. The topic
 * memory (`p.memory`) is only what the clustering grouped the observations under; for an exported
 * create it may be unrelated to the change (#3718). Advice-only proposals name the note's memory,
 * and the topic only when there is nothing else.
 */
export function proposalSubject(p: Pick<ConsolProposal, "memory" | "items">): string {
  const names = (pick: (i: ConsolItem) => boolean): string[] => [
    ...new Set(p.items.filter(pick).map((i) => i.name).filter(Boolean)),
  ];
  const own = names(writes);
  const all = own.length > 0 ? own : names(() => true);
  if (all.length === 0) return p.memory;
  return all.length === 1 ? all[0] : `${all[0]} +${all.length - 1}`;
}

/** The revision a patch was drafted against, and the one it publishes. */
export function patchRevisions(view: Pick<ConsolProposal, "targetRevisions">, iv: ConsolItem): { from: number; to: number } | null {
  const from = view.targetRevisions?.[iv.name] ?? iv.revision;
  return typeof from === "number" && from > 0 ? { from, to: from + 1 } : null;
}

/** The topic memory is changed only when some patch targets it by name. */
export const topicChanged = (view: Pick<ConsolProposal, "memory" | "items">): boolean =>
  view.items.some((i) => i.op === "patch" && i.name === view.memory);

/** The one line above the cards: how many memories approving adds and changes. */
export function previewSummary(tr: TFn, view: Pick<ConsolProposal, "items" | "targetRevisions">): string {
  const creates = view.items.filter((i) => i.op === "create");
  const patches = view.items.filter((i) => i.op === "patch");
  if (creates.length + patches.length === 0) return tr("mem.pv_sum_none");
  const list = patches
    .map((i) => {
      const r = patchRevisions(view, i);
      return r ? `${i.name}, ${tr("mem.pv_rev", { from: r.from, to: r.to })}` : i.name;
    })
    .join("; ");
  if (patches.length === 0) return tr(creates.length === 1 ? "mem.pv_sum_add_one" : "mem.pv_sum_add_many", { n: creates.length });
  if (creates.length === 0) return tr(patches.length === 1 ? "mem.pv_sum_change_one" : "mem.pv_sum_change_many", { n: patches.length, list });
  return tr("mem.pv_sum_both", { a: creates.length, p: patches.length, list });
}

export interface DiffRow {
  tag: "+" | "-" | " " | "…";
  text: string;
}

/** The Agent's line diff ("-" removed, "+" added, " " kept, a lone "…" for a folded run). */
export function parseLineDiff(diff: string | undefined): DiffRow[] {
  if (!diff) return [];
  const rows: DiffRow[] = [];
  for (const line of diff.replace(/\n$/, "").split("\n")) {
    if (line === "…") rows.push({ tag: "…", text: "" });
    else if (line[0] === "+" || line[0] === "-" || line[0] === " ") rows.push({ tag: line[0] as DiffRow["tag"], text: line.slice(1) });
    else rows.push({ tag: " ", text: line });
  }
  return rows;
}

function Side({ rows, side, label }: { rows: DiffRow[]; side: "-" | "+"; label: string }) {
  return (
    <div className="mem-pv-side" role="group" aria-label={label}>
      <div className="mem-pv-side-head">{label}</div>
      <div className="mem-pv-lines">
        {rows
          // Each side shows its own lines and the shared context; the other side's lines are absent.
          .filter((r) => r.tag === " " || r.tag === "…" || r.tag === side)
          .map((r, i) => (
            <div
              key={i}
              className={"mem-pv-line" + (r.tag === side ? (side === "-" ? " mem-pv-del" : " mem-pv-add") : r.tag === "…" ? " mem-pv-gap" : "")}
              data-change={r.tag === side ? (side === "-" ? "removed" : "added") : undefined}
            >
              <span className="mem-pv-mark" aria-hidden="true">
                {r.tag === side ? side : ""}
              </span>
              <span className="mem-pv-text">{r.tag === "…" ? "…" : r.text || " "}</span>
            </div>
          ))}
      </div>
    </div>
  );
}

function Stale({ iv }: { iv: ConsolItemView }) {
  const tr = useT();
  return iv.stale ? <p className="mem-warn">{tr("mem.cs_item_stale", { why: iv.stale })}</p> : null;
}

function CreateCard({ view, iv }: { view: ConsolProposalView; iv: ConsolItemView }) {
  const tr = useT();
  const scope = view.scope === "user" ? tr("mem.af_scope_user") : view.projectId || tr("mem.pv_scope_project");
  return (
    <article className="mem-pv-card" data-op="create">
      <h5 className="mem-pv-title">
        <span className="mem-badge af-op-create">{tr("mem.pv_new")}</span> <code>{iv.name}</code>
      </h5>
      <dl className="mem-pv-meta">
        <dt>{tr("mem.pv_scope")}</dt>
        <dd>{scope}</dd>
        {iv.type && (
          <>
            <dt>{tr("mem.pv_type")}</dt>
            <dd>{iv.type}</dd>
          </>
        )}
        {iv.description && (
          <>
            <dt>{tr("mem.pv_description")}</dt>
            <dd>{iv.description}</dd>
          </>
        )}
      </dl>
      {iv.reason && <p className="muted">{iv.reason}</p>}
      {iv.body ? (
        <div className="mem-pv-body">
          <MarkdownView source={iv.body} />
        </div>
      ) : null}
      {!topicChanged(view) && iv.name !== view.memory && (
        <p className="muted ds-note mem-pv-untouched">{tr("mem.pv_topic_untouched", { name: view.memory })}</p>
      )}
      <Stale iv={iv} />
    </article>
  );
}

function PatchCard({ view, iv }: { view: ConsolProposalView; iv: ConsolItemView }) {
  const tr = useT();
  const rev = patchRevisions(view, iv);
  const rows = parseLineDiff(iv.diff);
  return (
    <article className="mem-pv-card" data-op="patch">
      <h5 className="mem-pv-title">
        <span className="mem-badge af-op-patch">{tr("mem.pv_change")}</span> <code>{iv.name}</code>
        {rev && <span className="muted"> {tr("mem.pv_rev", { from: rev.from, to: rev.to })}</span>}
      </h5>
      {iv.reason && <p className="muted">{iv.reason}</p>}
      {rows.length > 0 ? (
        <div className="mem-pv-ba">
          <Side rows={rows} side="-" label={rev ? tr("mem.pv_before_rev", { n: rev.from }) : tr("mem.pv_before")} />
          <Side rows={rows} side="+" label={rev ? tr("mem.pv_after_rev", { n: rev.to }) : tr("mem.pv_after")} />
        </div>
      ) : null}
      <p className="muted ds-note">{tr("mem.pv_patch_note")}</p>
      <Stale iv={iv} />
    </article>
  );
}

function AdviceCard({ iv }: { iv: ConsolItemView }) {
  const tr = useT();
  const targets = iv.targets ?? [];
  const what = tMaybe("mem.pv_advice_" + iv.advice, { name: iv.name, targets: targets.join(", ") });
  return (
    <article className="mem-pv-card" data-op="note">
      <h5 className="mem-pv-title">
        <span className="mem-badge">{tr("mem.cs_advice")}</span> <code>{iv.name}</code>
      </h5>
      <p>{what ?? tMaybe("mem.cs_advice_" + iv.advice) ?? iv.advice ?? ""}</p>
      {iv.reason && <p className="muted">{iv.reason}</p>}
      {targets.length > 0 && (
        <p className="muted">
          {tr("mem.cs_advice_targets")} {targets.map((n) => <code key={n}>{n} </code>)}
        </p>
      )}
      {/* Advice has no Approve: the engine has no call that writes a replacement or a merge yet
          (#3696 builds the writer), so a member applies it by hand in Memories. */}
      <p className="muted ds-note">{tr("mem.pv_advice_note")}</p>
    </article>
  );
}

export function ProposalPreview({ view }: { view: ConsolProposalView }) {
  const tr = useT();
  const pending = view.status === "pending";
  return (
    <section className="mem-pv" aria-label={tr(pending ? "mem.pv_title" : "mem.pv_title_decided")}>
      <h4>{tr(pending ? "mem.pv_title" : "mem.pv_title_decided")}</h4>
      {pending && (
        <p className="mem-pv-summary" role="status">
          {previewSummary(tr, view)}
        </p>
      )}
      {view.itemViews.map((iv, i) =>
        iv.op === "create" ? (
          <CreateCard key={i} view={view} iv={iv} />
        ) : iv.op === "patch" ? (
          <PatchCard key={i} view={view} iv={iv} />
        ) : (
          <AdviceCard key={i} iv={iv} />
        ),
      )}
    </section>
  );
}
