import { useEffect, useState, type ReactNode } from "react";
import { useT } from "../../../lib/i18n/index.ts";
import { Icon } from "../../../ui/Icon.tsx";
import { Button } from "../../../ui/Button.tsx";
import { ViewHead } from "../../../ui/ViewHead.tsx";
import { type ModelKind } from "./adminEngineModels.tsx";
import { setEngineAddView } from "./openEngineAdd.ts";
import { loadShell, saveShell } from "./catalogMemory.ts";
import { engineIsImage, engineIsRemote, engineTitle } from "./engineTypes.ts";
import { useEngineRows } from "./useEngineRows.ts";
import { CatalogBrowser, NoEngineCatalog } from "./adminEngineAddCatalogBrowser.tsx";
import { RegisteredCatalog } from "./adminEngineAddRegistered.tsx";
import { type BrowseProps } from "./adminEngineAddShared.tsx";
export { savedObjectsForHit } from "./adminEngineAddShared.tsx";

type CatalogView = "search" | "registered";

/** Full-pane catalogue (ADR 0085 decision 8). Two tabs and no wizard: 探す opens one plan card
 * per press, 登録済み holds the rows and, under them, the bucket itself. */
export function EngineAddView({ engineKey, lora, initialView = "search", paneId, headerActions }: {
  engineKey: string;
  lora: boolean;
  initialView?: CatalogView;
  /** Which pane this is, so a return lands on the face, engine and kind it was left on
   *  (catalogMemory). Absent outside a pane — the compatibility door in the settings panel — and
   *  then every mount shares one memory, which is what `clearCatalogMemory` is for in tests. */
  paneId?: string;
  headerActions?: ReactNode;
}) {
  const tr = useT();
  const { rows, isSuper, sources, err, load } = useEngineRows();
  // Read ONCE, at mount: after that this component owns the values and writes them back below.
  const [shell] = useState(() => loadShell(paneId));
  const [selectedKey, setSelectedKey] = useState(shell?.engineKey || engineKey);
  const [view, setView] = useState<CatalogView>(shell?.view || initialView);
  const [kind, setKind] = useState<ModelKind>(shell?.kind || (lora ? "lora" : "model"));
  const row = (rows || []).find((candidate) => candidate.key === selectedKey) || (rows || [])[0];

  useEffect(() => {
    if (rows?.length && !rows.some((candidate) => candidate.key === selectedKey)) setSelectedKey(rows[0].key);
  }, [rows, selectedKey]);

  useEffect(() => { saveShell(paneId, { engineKey: selectedKey, view, kind }); }, [paneId, selectedKey, view, kind]);

  // The open FACE, into the pane itself, so a browser reload comes back to it (the rest of the
  // memory is module scope and does not survive one). Only the face — see setEngineAddView.
  useEffect(() => {
    if (!paneId || view === initialView) return;
    setEngineAddView(paneId, engineKey, lora, view);
  }, [engineKey, initialView, lora, paneId, view]);

  return (
    <div className="engines-add-pane engine-catalog-pane admin-stage">
      <ViewHead actions={headerActions}>
        <span className="view-title"><Icon name="download" /> {tr("admin.catalog_title" as never)}</span>
      </ViewHead>
      {err && <p className="form-err pad">{err}</p>}
      {rows !== null && rows.length === 0 && (
        <NoEngineCatalog sources={sources} />
      )}
      {row && <>
        <div className="engine-catalog-nav">
          <div className="engine-catalog-role-tabs" role="tablist" aria-label={tr("admin.catalog_role_label" as never)}>
            {(rows || []).map((candidate) => (
              <Button key={candidate.key} variant="ghost" small role="tab" aria-selected={candidate.key === row.key}
                className={candidate.key === row.key ? "active" : ""} onClick={() => setSelectedKey(candidate.key)}>
                <Icon name={engineIsImage(candidate) ? "file-media" : "comment"} />
                {tr(engineIsImage(candidate) ? "admin.engines_role_image" : "admin.engines_role_llm")}
              </Button>
            ))}
          </div>
          <div className="seg engine-catalog-view-tabs" role="tablist" aria-label={tr("admin.catalog_view_label" as never)}>
            {(["search", "registered"] as const).map((next) => (
              <Button key={next} variant="ghost" small role="tab" aria-selected={view === next}
                className={"seg-btn" + (view === next ? " active" : "")} onClick={() => setView(next)}>
                {tr((`admin.catalog_view_${next}`) as never)}
              </Button>
            ))}
          </div>
          <span className="muted mono engine-catalog-engine-name">{engineTitle(row)}</span>
        </div>
        {engineIsRemote(row) && <p className="admin-hint pad">{tr("admin.engines_remote_catalog")} {row.url || ""}</p>}
        {/* 🔴 Keyed by the engine and the kind, which is what makes each of them a separate memory:
            a face that changes either is a different list of a different thing, so it unmounts —
            saving what it held — and the next one mounts and restores its own. Without the key,
            React reuses the instance and an effect has to tear the state down by hand, which is the
            shape that kept throwing the page away. */}
        {view === "search" ? (
          engineIsImage(row)
            ? <ImageCatalog key={`${row.key}:${kind}`} row={row} kind={kind} onKind={setKind} paneId={paneId} sources={sources} readOnly={engineIsRemote(row)} onChanged={load} />
            : <LLMCatalog key={`${row.key}:${kind}`} row={row} kind={kind} onKind={setKind} paneId={paneId} sources={sources} readOnly={engineIsRemote(row)} onChanged={load} />
        ) : (
          <RegisteredCatalog key={`${row.key}:${kind}`} row={row} kind={kind} onKind={setKind} paneId={paneId} isSuper={isSuper}
            readOnly={engineIsRemote(row)} onChanged={load} />
        )}
      </>}
    </div>
  );
}

export function ImageCatalog(props: BrowseProps) { return <CatalogBrowser {...props} image />; }
export function LLMCatalog(props: BrowseProps) { return <CatalogBrowser {...props} image={false} />; }
