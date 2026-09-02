import { Icon } from "../../../ui/Icon.tsx";
import { t as tr } from "../../../lib/i18n/index.ts";
import { kindIcon, kindLabel, kindClass } from "../../../lib/sessionkind.ts";
import { originKind } from "../skillPicker.ts";

// foreign スキルの出所バッジ（docs/log/50 §8）: kind 色（--kind-* 1 ソース）のミニチップで
// 出所エージェントを示す。.agents はどの kind でもない共有規約 → 中立の「共有」。
// ネイティブ項目はバッジ無し（従来どおり）。
export function SkillOriginBadge({ origin }: { origin: string }) {
  const k = originKind(origin);
  if (!k) return <span className="mirror-skill-src" title={origin}>{tr("mirror.skills_src_shared")}</span>;
  return (
    <span className={"mirror-skill-src kind-" + kindClass(k)} title={origin}>
      <Icon name={kindIcon(k)} /> {kindLabel(k)}
    </span>
  );
}
