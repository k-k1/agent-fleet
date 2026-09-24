// StudioHistory — the studio's pictures across Agent restarts (ADR 0100 decision 9): the
// history.jsonl rows for this studio, newest first, with the three P0 verbs. "Compare" (pin up
// to four) and "show the agent" are P1.
import { downloadURL } from "../../../core/api/client.ts";
import { useT } from "../../../lib/i18n/index.ts";
import type { HistoryItem } from "../wire.ts";

const THUMB = 512;
const baseName = (p: string): string => p.split("/").filter(Boolean).pop() || p;

export interface PictureActions {
  /** Back to the settings this picture was made with. */
  onRestore: (path: string, version?: string) => void;
  /** Use it as the reference of an edit. */
  onReference: (path: string) => void;
  /** Inpaint part of it: op=inpaint with this picture as inputs[0]; the mask is the member's. */
  onFix: (path: string) => void;
}

export function PictureVerbs({ path, version, actions }: { path: string; version?: string; actions: PictureActions }) {
  const tr = useT();
  return (
    <span className="igen-verbs">
      <button type="button" className="ui-btn ui-btn-ghost ui-btn-sm" onClick={() => actions.onRestore(path, version)}>
        {tr("imggen.pic_restore")}
      </button>
      <button type="button" className="ui-btn ui-btn-ghost ui-btn-sm" onClick={() => actions.onReference(path)}>
        {tr("imggen.pic_reference")}
      </button>
      <button type="button" className="ui-btn ui-btn-ghost ui-btn-sm" onClick={() => actions.onFix(path)}>
        {tr("imggen.pic_fix")}
      </button>
    </span>
  );
}

export function StudioHistory({
  items,
  hasMore,
  onMore,
  onZoom,
  actions,
}: {
  items: HistoryItem[];
  hasMore: boolean;
  onMore: () => void;
  onZoom: (path: string) => void;
  actions: PictureActions;
}) {
  const tr = useT();
  return (
    <section className="igen-history">
      <h3>{tr("imggen.history")}</h3>
      {items.length === 0 ? (
        <p className="igen-hint">{tr("imggen.history_none")}</p>
      ) : (
        <div className="igen-grid-cards" role="list">
          {items.map((it) => (
            <div className="igen-card" role="listitem" key={it.path}>
              <button
                type="button"
                className="igen-thumb"
                title={tr("imggen.zoom", { name: baseName(it.path) })}
                onClick={() => onZoom(it.path)}
              >
                <img src={downloadURL(it.path, THUMB)} alt={baseName(it.path)} loading="lazy" decoding="async" />
              </button>
              <div className="igen-card-meta">
                {it.trial && <span className="igen-hint">{tr("imggen.history_trial")}</span>}
                <PictureVerbs path={it.path} version={it.version} actions={actions} />
              </div>
            </div>
          ))}
        </div>
      )}
      {hasMore && (
        <button type="button" className="ui-btn ui-btn-ghost ui-btn-sm" onClick={onMore}>
          {tr("imggen.history_more")}
        </button>
      )}
    </section>
  );
}
