// StudioRedirect — what a mirror pane shows for a session bound to an image studio (ADR 0100
// decision 10). The studio pane embeds the same mirror; a second one on the same session would
// overwrite the first's composer draft, attachments and send echo, so this pane points there.
import type { ReactNode } from "react";
import { useT } from "../../../lib/i18n/index.ts";
import { Button } from "../../../ui/Button.tsx";
import { EmptyState } from "../../../ui/EmptyState.tsx";
import { ViewHead } from "../../../ui/ViewHead.tsx";
import { openImagegen } from "../open.ts";

export function StudioRedirect({ studioId, headerActions }: { studioId: string; headerActions?: ReactNode }) {
  const tr = useT();
  return (
    <div className="igen-redirect">
      <ViewHead actions={headerActions} />
      <EmptyState icon="wand" title={tr("imggen.studio_redirect")} hint={tr("imggen.studio_redirect_hint")}>
        <Button variant="primary" icon="wand" onClick={() => openImagegen({ studioId })}>
          {tr("imggen.studio_open")}
        </Button>
      </EmptyState>
    </div>
  );
}
