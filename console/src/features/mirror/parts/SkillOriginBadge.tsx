import { Icon } from "../../../ui/Icon.tsx";
import { t as tr } from "../../../lib/i18n/index.ts";
import { kindIcon, kindLabel, kindClass } from "../../../lib/sessionkind.ts";
import { originKind } from "../skillPicker.ts";

// Origin badge for a foreign skill (docs/log/50 §8): a mini chip in the kind colour (--kind-*,
// one source) naming the agent it came from. .agents is a shared convention belonging to no kind,
// so it gets the neutral "shared" label. Native entries carry no badge.
export function SkillOriginBadge({ origin }: { origin: string }) {
  const k = originKind(origin);
  if (!k) return <span className="mirror-skill-src" title={origin}>{tr("mirror.skills_src_shared")}</span>;
  return (
    <span className={"mirror-skill-src kind-" + kindClass(k)} title={origin}>
      <Icon name={kindIcon(k)} /> {kindLabel(k)}
    </span>
  );
}
