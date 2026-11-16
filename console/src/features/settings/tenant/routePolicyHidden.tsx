// What the credential policy hides from a tenant's members, and from which layer (ADR 0138
// D10a, D16, M2 P9). Members' own screens never name a layer or a hidden item; this list on the
// Model access pages is where "why can't my members see X" is answered.
//
// It covers the administered layers only: a member's own switches live in their Workspace and
// the CP cannot see them (the user's decision 9 on #4169), and the list says so.
//
// Fail closed: an answer without a well-formed `hidden` draws a line saying the list is not
// available, never "nothing is hidden".
import { useT } from "../../../lib/i18n/index.ts";
import { SESSION_KINDS } from "../../../types/session.ts";
import { kindDisplayName } from "../../../lib/sessionkind.ts";
import { RouteName, layersLabel } from "./routePolicyRuleEditor.tsx";
import { parseHidden, type HiddenItem, type TenantPolicyView } from "./routePolicyRule.ts";

type Tr = ReturnType<typeof useT>;

// Image providers that are not agent kinds: the deployment engine's two wires.
const PROVIDER_KEY = {
  comfy: "admin.mpol_hidden_provider_comfy",
  "openai-compat": "admin.mpol_hidden_provider_openai_compat",
} as const;

// kindDisplayName falls back to Claude for a kind it does not know, so an id outside the
// registry is shown as itself rather than under another harness's name.
function ownerName(tr: Tr, id: string): string {
  if (id in PROVIDER_KEY) return tr(PROVIDER_KEY[id as keyof typeof PROVIDER_KEY]);
  return (SESSION_KINDS as readonly string[]).includes(id) ? kindDisplayName(id) : id;
}

function Group({
  title,
  items,
  layersOf,
}: {
  title: string;
  items: HiddenItem[];
  layersOf: (route: string) => string[] | undefined;
}) {
  const tr = useT();
  if (items.length === 0) return null;
  return (
    <>
      <h5 className="admin-subhead">{title}</h5>
      <ul className="mpol-hidden">
        {items.map((it) => (
          <li key={it.id} data-hidden={it.id}>
            <strong>{ownerName(tr, it.id)}</strong>{" "}
            <span className="mpol-no">{tr("admin.mpol_hidden_by", { layers: layersLabel(tr, it.forbiddenBy) })}</span>
            <ul className="mpol-routes">
              {it.routes.map((r) => (
                <li key={r} data-route={r}>
                  <RouteName id={r} /> <span className="muted">— {layersLabel(tr, layersOf(r) ?? it.forbiddenBy)}</span>
                </li>
              ))}
            </ul>
          </li>
        ))}
      </ul>
    </>
  );
}

export function HiddenList({ view }: { view: TenantPolicyView }) {
  const tr = useT();
  const hidden = parseHidden(view.hidden);
  const byRoute = new Map(view.routes.map((r) => [r.id, r.forbiddenBy]));
  const layersOf = (route: string) => {
    const l = byRoute.get(route);
    return l && l.length > 0 ? l : undefined;
  };
  return (
    <section className="admin-panel model-access-hidden">
      <h4>{tr("admin.mpol_hidden_title")}</h4>
      <p className="admin-hint">{tr("admin.mpol_hidden_note")}</p>
      {!hidden ? (
        <p className="admin-hint warn" role="alert">
          {tr("admin.mpol_hidden_unavailable")}
        </p>
      ) : hidden.kinds.length + hidden.chat.length + hidden.imagegen.length === 0 ? (
        <p className="muted" data-testid="hidden-none">
          {tr("admin.mpol_hidden_none")}
        </p>
      ) : (
        <>
          <Group title={tr("admin.mpol_hidden_kinds")} items={hidden.kinds} layersOf={layersOf} />
          <Group title={tr("admin.mpol_hidden_imagegen")} items={hidden.imagegen} layersOf={layersOf} />
          <Group title={tr("admin.mpol_hidden_chat")} items={hidden.chat} layersOf={layersOf} />
        </>
      )}
    </section>
  );
}
