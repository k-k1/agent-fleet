// SpaceSupportHint — the line under a space launch while the Agent has not said it supports
// spaces: checking, unsupported, or the check failed (with a way to ask again).
import { useT } from "../../lib/i18n/index.ts";
import type { SpaceSupport } from "./useSpaceSupport.ts";

export function SpaceSupportHint({ support, onRecheck }: { support: SpaceSupport; onRecheck: () => void }) {
  const tr = useT();
  if (support === "yes") return null;
  return (
    <span className={"ui-field-hint" + (support === "checking" ? "" : " ui-field-hint--warn")} role="status">
      {tr(support === "checking" ? "start.space_checking" : support === "no" ? "start.space_unsupported" : "start.space_check_failed")}
      {support === "failed" && (
        <>
          {" "}
          <button type="button" className="linklike" onClick={onRecheck}>
            {tr("start.space_check_again")}
          </button>
        </>
      )}
    </span>
  );
}
