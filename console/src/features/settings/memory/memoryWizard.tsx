// The shared frame and the shared grouped preview of every Import & export wizard (claude import,
// muse import, write-back, project move, bundle import). Only the chrome lives here: each panel
// keeps its own API calls, validation, secret handling, scope choice and per-row options, and
// hands the frame its current step.
//
// Steps are source -> preview -> confirm -> apply -> result. A panel derives the step from its
// own state (nothing picked / preview loading / preview ready / batches in flight / finished);
// the frame only draws it, so a step can never disagree with what the panel does.

import type { ReactNode } from "react";
import { Button } from "../../../ui/Button.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { useEscLayer } from "../../../lib/escLayer.ts";

export type WizardStep = "source" | "preview" | "confirm" | "apply" | "result";
export const WIZARD_STEPS: WizardStep[] = ["source", "preview", "confirm", "apply", "result"];

export function WizardFrame({
  title,
  intro,
  step,
  busy = false,
  result,
  onClose,
  children,
}: {
  title: string;
  intro?: string;
  step: WizardStep;
  /** An apply is in flight: it cannot be aborted (batches already sent are written), so leaving waits. */
  busy?: boolean;
  /** The outcome line of the last apply; stays until the member picks something else. */
  result?: string | null;
  /** Leaves the wizard. Absent when the panel is mounted without a hub around it. */
  onClose?: () => void;
  children: ReactNode;
}) {
  const tr = useT();
  const at = WIZARD_STEPS.indexOf(step);
  // Escape follows Cancel: it leaves to the hub when idle and is consumed while an apply runs. The
  // layer sits above the Settings dialog's, so it cannot close Settings (and unmount a run in
  // flight, leaving later batches unsent) behind a disabled Cancel.
  useEscLayer(() => {
    if (!busy) onClose?.();
  }, !!onClose);
  return (
    <section className="mem-wiz" aria-label={title}>
      <h4>{title}</h4>
      <ol className="mem-wiz-steps" aria-label={tr("mem.wz_steps")}>
        {WIZARD_STEPS.map((s, i) => (
          <li
            key={s}
            className={"mem-wiz-step" + (i < at ? " done" : "") + (i === at ? " current" : "")}
            aria-current={i === at ? "step" : undefined}
          >
            {tr(("mem.wz_step_" + s) as "mem.wz_step_source")}
          </li>
        ))}
      </ol>
      {intro && <p className="muted ds-hint">{intro}</p>}
      {children}
      {result && <p className="mem-wiz-result">{result}</p>}
      {onClose && (
        <div className="mem-ci-actions mem-wiz-foot">
          <Button icon={step === "result" ? "arrow-left" : undefined} disabled={busy} onClick={onClose}>
            {step === "result" ? tr("mem.wz_back") : tr("common.cancel")}
          </Button>
        </div>
      )}
    </section>
  );
}

/** One expandable group: a summary line and its body. The status groups and the bundle's scope groups are both this. */
export function PreviewSection({ summary, open, children }: { summary: ReactNode; open: boolean; children?: ReactNode }) {
  return (
    <details className="mem-ci-group" open={open}>
      <summary>{summary}</summary>
      {children}
    </details>
  );
}

/** Row checkboxes for the statuses a panel lets the member opt into (the write-back's conflicts). */
export interface PreviewSelection<T> {
  shows: (item: T, status: string) => boolean;
  checked: (item: T) => boolean;
  disabled: (item: T) => boolean;
  label: (item: T) => string;
  toggle: (item: T) => void;
}

/**
 * Items grouped by status: one `<details>` per non-empty status, in `order`, headed by a badge
 * and the count. An unknown status from a newer Agent is not drawn, as before.
 */
export function GroupedPreview<T>({
  items,
  order,
  statusOf,
  label,
  badge,
  open,
  keyOf,
  renderItem,
  selection,
}: {
  items: T[];
  order: readonly string[];
  statusOf: (item: T) => string;
  label: (status: string) => string;
  /** The badge's modifier class, e.g. "ci-new". */
  badge: (status: string) => string;
  open: (status: string) => boolean;
  keyOf: (item: T) => string;
  renderItem: (item: T, status: string) => ReactNode;
  selection?: PreviewSelection<T>;
}) {
  return (
    <>
      {order.map((st) => {
        const rows = items.filter((i) => statusOf(i) === st);
        if (rows.length === 0) return null;
        return (
          <PreviewSection
            key={st}
            open={open(st)}
            summary={
              <>
                <span className={"mem-badge " + badge(st)}>{label(st)}</span> {rows.length}
              </>
            }
          >
            <ul className="mem-ci-items">
              {rows.map((i) => (
                <li key={keyOf(i)}>
                  {selection?.shows(i, st) && (
                    <>
                      <input
                        type="checkbox"
                        aria-label={selection.label(i)}
                        checked={selection.checked(i)}
                        disabled={selection.disabled(i)}
                        onChange={() => selection.toggle(i)}
                      />{" "}
                    </>
                  )}
                  {renderItem(i, st)}
                </li>
              ))}
            </ul>
          </PreviewSection>
        );
      })}
    </>
  );
}

/** The masked findings of a file the secret scan flagged; the values themselves never reach the Console. */
export function Findings({ findings }: { findings?: { rule: string; line: number; hint: string }[] }) {
  const tr = useT();
  return (
    <ul className="mem-findings">
      {(findings ?? []).map((f, n) => (
        <li key={n}>
          <code>{f.rule}</code> {tr("mem.af_secret_line", { line: f.line })} <code>{f.hint}</code>
        </li>
      ))}
    </ul>
  );
}
