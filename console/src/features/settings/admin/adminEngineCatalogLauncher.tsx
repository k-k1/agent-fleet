import { useEffect, useRef } from "react";
import { useT } from "../../../lib/i18n/index.ts";
import { useSettingsUI } from "../store.ts";
import { EngineModelsAdminView } from "./adminEngineModels.tsx";
import { useEngineRows } from "./engineTypes.ts";
import { openEngineAdd } from "./openEngineAdd.ts";

/** Compatibility door for the former settings page. Existing navigation still works, but the
 * durable model-management surface is now the pop-out-capable catalogue pane. */
export function EngineCatalogLauncher() {
  const tr = useT();
  const { rows, err } = useEngineRows();
  const closeAdmin = useSettingsUI((state) => state.closeAdmin);
  const closeTenant = useSettingsUI((state) => state.closeTenantSettings);
  const opened = useRef(false);
  useEffect(() => {
    if (opened.current || !rows?.length) return;
    opened.current = true;
    openEngineAdd(rows[0].key, false, "registered");
    closeAdmin();
    closeTenant();
  }, [closeAdmin, closeTenant, rows]);
  if (err) return <p className="form-err pad">{err}</p>;
  if (rows === null) return <p className="muted pad">{tr("common.loading")}</p>;
  // With no engine there is no pane target; retain the engine-less upstream browser.
  if (rows.length === 0) return <EngineModelsAdminView />;
  return <p className="muted pad">{tr("admin.catalog_opening" as never)}</p>;
}
